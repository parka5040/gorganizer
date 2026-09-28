#include "ModLoaderController.h"
#include "Dialogs.h"
#include "GrpcClient.h"
#include "InstallErrorText.h"
#include "ErrorPresenter.h"
#include "ModLoaderProgressDialog.h"
#include "SessionController.h"

#include <QAction>
#include <QMenu>
#include <QMessageBox>
#include <QPushButton>
#include <QRegularExpression>
#include <QStatusBar>
#include <QTextDocument>
#include <QTimer>
#include <QVersionNumber>

namespace gorganizer {

namespace {

constexpr int kUnmountCheckIntervalMs = 3 * 1000;
constexpr int kUnmountCheckLimitMs = 5 * 60 * 1000;
constexpr int kReconcileFirstPollMs = 2 * 1000;
constexpr int kReconcilePollIntervalMs = 5 * 1000;
constexpr int kReconcileLimitMs = 30 * 60 * 1000;
constexpr int kDisconnectTimeoutMs = 90 * 1000;
constexpr int kReportTextLimit = 2000;

bool versionNewer(const QString& candidate, const QString& installed)
{
    const QVersionNumber next = QVersionNumber::fromString(candidate);
    const QVersionNumber current = QVersionNumber::fromString(installed);
    if (next.isNull() || current.isNull())
        return false;
    return QVersionNumber::compare(next, current) > 0;
}

QString unmountErrorText(const QString& error)
{
    return errorSummary(QStringLiteral("unmount mods"), error, true);
}

bool repairable(GrpcModLoaderState state)
{
    return state == GrpcModLoaderStateOk || state == GrpcModLoaderStateLauncherReverted
        || state == GrpcModLoaderStateIncomplete || state == GrpcModLoaderStateInterrupted;
}

QString capped(const QString& text)
{
    if (text.size() <= kReportTextLimit)
        return text;
    return text.left(kReportTextLimit) + QStringLiteral("…");
}

}

ModLoaderController::ModLoaderController(GrpcClient* grpc, SessionController* session, QMenu* menu,
                                         QStatusBar* statusBar, QWidget* parentWindow)
    : QObject(parentWindow)
    , m_grpc(grpc)
    , m_session(session)
    , m_menu(menu)
    , m_statusBar(statusBar)
    , m_parentWindow(parentWindow)
{
    m_statusAction = m_menu->addAction("SMAPI — status unknown");
    m_statusAction->setEnabled(false);
    m_menu->addSeparator();
    m_installAction = m_menu->addAction("Install SMAPI…");
    m_repairAction = m_menu->addAction("Repair SMAPI…");
    m_rollbackAction = m_menu->addAction("Roll Back SMAPI…");
    m_uninstallAction = m_menu->addAction("Uninstall SMAPI…");
    m_menu->addSeparator();
    m_checkAction = m_menu->addAction("Check for SMAPI Updates");
    m_menu->setToolTipsVisible(true);
    m_menu->menuAction()->setVisible(false);

    m_pollTimer = new QTimer(this);
    m_pollTimer->setSingleShot(true);
    m_phaseTimer = new QTimer(this);
    m_phaseTimer->setSingleShot(true);
    m_disconnectTimer = new QTimer(this);
    m_disconnectTimer->setSingleShot(true);
    m_disconnectTimer->setInterval(kDisconnectTimeoutMs);

    connect(m_installAction, &QAction::triggered, this, &ModLoaderController::onInstallTriggered);
    connect(m_repairAction, &QAction::triggered, this, &ModLoaderController::onRepairTriggered);
    connect(m_rollbackAction, &QAction::triggered, this, &ModLoaderController::onRollbackTriggered);
    connect(m_uninstallAction, &QAction::triggered, this, &ModLoaderController::onUninstallTriggered);
    connect(m_checkAction, &QAction::triggered, this, &ModLoaderController::onCheckUpdatesTriggered);
    connect(m_pollTimer, &QTimer::timeout, this, &ModLoaderController::onPollTimeout);
    connect(m_phaseTimer, &QTimer::timeout, this, &ModLoaderController::onPhaseTimeout);
    connect(m_disconnectTimer, &QTimer::timeout, this, &ModLoaderController::onDisconnectTimeout);

    connect(m_grpc, &GrpcClient::modLoaderStatusReceived, this, &ModLoaderController::onStatusReceived);
    connect(m_grpc, &GrpcClient::modLoaderStatusFailed, this, &ModLoaderController::onStatusFailed);
    connect(m_grpc, &GrpcClient::modLoaderOperationFinished, this, &ModLoaderController::onOperationFinished);
    connect(m_grpc, &GrpcClient::vfsStatusQueried, this, &ModLoaderController::onVfsStatusQueried);
    connect(m_grpc, &GrpcClient::vfsStatusQueryFailed, this, &ModLoaderController::onVfsStatusQueryFailed);
    connect(m_grpc, &GrpcClient::maintenanceUnmountFinished, this, &ModLoaderController::onMaintenanceUnmountFinished);
    connect(m_grpc, &GrpcClient::daemonInfo, this, &ModLoaderController::onDaemonInfo);
    connect(m_grpc, &GrpcClient::connected, this, &ModLoaderController::onConnected);
    connect(m_grpc, &GrpcClient::disconnected, this, &ModLoaderController::onDisconnected);
}

QString ModLoaderController::interruptibleOperation() const
{
    if (!m_op || m_op->phase == Phase::Capturing || m_op->phase == Phase::Consenting)
        return QString();
    return QStringLiteral("%1 SMAPI for %2").arg(operationProgressive(m_op->kind), m_op->gameName);
}

void ModLoaderController::onActiveGameChanged(const GameInfo& game)
{
    m_game = game;
    const bool supported = managesSmapi(game);
    m_menu->menuAction()->setVisible(supported);
    if (supported) {
        const QString gameId = game.shortName;
        if (const auto it = m_statuses.constFind(gameId); it != m_statuses.constEnd())
            emit modLoaderStatusChanged(gameId, it.value());
        if (m_grpc->isConnected()) {
            if (!m_pendingStatus.contains(gameId))
                requestStatus(gameId, false);
            scheduleLatestCheck(gameId);
        }
    }
    updateMenu();
}

void ModLoaderController::scheduleLatestCheck(const QString& gameId)
{
    if (m_latestChecked.contains(gameId) || !m_grpc->isConnected())
        return;
    m_latestChecked.insert(gameId);
    m_autoCheckIds.insert(requestStatus(gameId, true));
}

void ModLoaderController::startInstall(const QString& gameId, bool confirmed)
{
    const auto it = m_statuses.constFind(gameId);
    const bool update = it != m_statuses.constEnd() && it->updateAvailable;
    beginOperation(update ? OperationKind::Update : OperationKind::Install, gameId, confirmed);
}

void ModLoaderController::startRepair(const QString& gameId, bool confirmed)
{
    beginOperation(OperationKind::Repair, gameId, confirmed);
}

void ModLoaderController::refreshStatus(const QString& gameId)
{
    if (gameId.isEmpty())
        return;
    requestStatus(gameId, false);
}

void ModLoaderController::onInstallTriggered()
{
    startInstall(m_game.shortName);
}

void ModLoaderController::onRepairTriggered()
{
    startRepair(m_game.shortName);
}

void ModLoaderController::onRollbackTriggered()
{
    beginOperation(OperationKind::Rollback, m_game.shortName, false);
}

void ModLoaderController::onUninstallTriggered()
{
    beginOperation(OperationKind::Uninstall, m_game.shortName, false);
}

void ModLoaderController::onCheckUpdatesTriggered()
{
    if (!managesSmapi(m_game))
        return;
    if (!m_grpc->isConnected()) {
        dialogs::warn(m_parentWindow, "Check for SMAPI Updates", "The gorganizer daemon must be running to check for updates.");
        return;
    }
    m_latestChecked.insert(m_game.shortName);
    m_interactiveCheckId = requestStatus(m_game.shortName, true);
    m_statusBar->showMessage("Checking for SMAPI updates…", 5000);
    updateMenu();
}

quint64 ModLoaderController::requestStatus(const QString& gameId, bool checkLatest)
{
    const quint64 requestId = m_grpc->getModLoaderStatus(gameId, checkLatest);
    if (!checkLatest)
        m_pendingStatus.insert(gameId, requestId);
    return requestId;
}

void ModLoaderController::onStatusReceived(quint64 requestId, const QString& gameId, const GrpcModLoaderStatus& status)
{
    if (m_pendingStatus.value(gameId) == requestId)
        m_pendingStatus.remove(gameId);
    m_autoCheckIds.remove(requestId);
    if (!status.latestVersion.isEmpty())
        m_latestVersions.insert(gameId, status.latestVersion);

    if (m_op && m_op->phase == Phase::Reconciling && requestId == m_op->reconcileRequestId) {
        m_op->reconcileRequestId = 0;
        if (status.busy) {
            m_pollTimer->start(kReconcilePollIntervalMs);
            return;
        }
        m_statusFloor.insert(gameId, requestId);
        completeReconciled(status);
        return;
    }

    const bool operationRunning = m_op && m_op->gameId == gameId;
    if (!operationRunning && requestId >= m_statusFloor.value(gameId, 0)) {
        applyStatus(gameId, status);
    } else if (!status.latestVersion.isEmpty()) {
        if (const auto it = m_statuses.constFind(gameId); it != m_statuses.constEnd())
            applyStatus(gameId, it.value());
    }

    if (requestId == m_interactiveCheckId) {
        m_interactiveCheckId = 0;
        updateMenu();
        reportUpdateCheck(status);
        return;
    }
    updateMenu();
}

void ModLoaderController::onStatusFailed(quint64 requestId, const QString& gameId, const QString& error, int grpcCode)
{
    if (m_pendingStatus.value(gameId) == requestId)
        m_pendingStatus.remove(gameId);
    if (m_op && m_op->phase == Phase::Reconciling && requestId == m_op->reconcileRequestId) {
        m_op->reconcileRequestId = 0;
        m_pollTimer->start(kReconcilePollIntervalMs);
        return;
    }
    if (m_autoCheckIds.remove(requestId))
        m_latestChecked.remove(gameId);
    if (requestId == m_interactiveCheckId) {
        m_interactiveCheckId = 0;
        updateMenu();
        presentError(m_parentWindow, "Check for SMAPI Updates", "check for SMAPI updates", GrpcError{grpcCode, QStringLiteral("GetModLoaderStatus"), error}, false);
        return;
    }
    if (gameId == m_game.shortName)
        m_statusBar->showMessage(errorSummary("check SMAPI's status", GrpcError{grpcCode, QStringLiteral("GetModLoaderStatus"), error}, false), 5000);
    updateMenu();
}

void ModLoaderController::applyStatus(const QString& gameId, const GrpcModLoaderStatus& status)
{
    GrpcModLoaderStatus merged = status;
    if (merged.gameId.isEmpty())
        merged.gameId = gameId;
    mergeCachedLatest(gameId, merged);
    m_statuses.insert(gameId, merged);
    emit modLoaderStatusChanged(gameId, merged);
}

void ModLoaderController::mergeCachedLatest(const QString& gameId, GrpcModLoaderStatus& status) const
{
    const QString latest = m_latestVersions.value(gameId);
    if (latest.isEmpty())
        return;
    if (status.latestVersion.isEmpty() || status.latestVersion != latest) {
        status.latestVersion = latest;
        status.updateAvailable = status.managed && versionNewer(latest, status.installedVersion);
    }
}

void ModLoaderController::reportUpdateCheck(const GrpcModLoaderStatus& status)
{
    const QString title = QStringLiteral("Check for SMAPI Updates");
    if (status.latestVersion.isEmpty()) {
        QString text = QStringLiteral("gorganizer could not find out which SMAPI release is the newest.");
        if (!status.detail.isEmpty())
            text += QStringLiteral("\n\n%1").arg(errorSummary("check for SMAPI updates", status.detail));
        dialogs::plainWarn(m_parentWindow, title, text);
        return;
    }
    if (status.updateAvailable) {
        dialogs::plainInfo(m_parentWindow, title,
            QStringLiteral("SMAPI %1 is available (installed: %2).\n\nUse Tools → SMAPI → Update SMAPI to %1… to install it.")
                .arg(status.latestVersion, status.installedVersion));
        return;
    }
    if (status.state == GrpcModLoaderStateNotInstalled) {
        dialogs::plainInfo(m_parentWindow, title,
            QStringLiteral("The newest SMAPI release is %1. SMAPI is not installed for this game yet.")
                .arg(status.latestVersion));
        return;
    }
    if (!status.managed || status.installedVersion.isEmpty()) {
        dialogs::plainInfo(m_parentWindow, title,
            QStringLiteral("The newest SMAPI release is %1. The SMAPI in the game folder was not installed by "
                           "gorganizer (or its files changed since), so its version cannot be compared.")
                .arg(status.latestVersion));
        return;
    }
    dialogs::plainInfo(m_parentWindow, title,
        QStringLiteral("SMAPI %1 is up to date.").arg(status.installedVersion));
}

void ModLoaderController::beginOperation(OperationKind kind, const QString& gameId, bool confirmed)
{
    const QString title = operationTitle(kind);
    if (m_op) {
        dialogs::info(m_parentWindow, title, "Another SMAPI operation is still running. Try again when it finishes.");
        return;
    }
    if (gameId.isEmpty() || !managesSmapi(m_game) || m_game.shortName != gameId) {
        dialogs::info(m_parentWindow, title, "Select a game that uses SMAPI first.");
        return;
    }
    if (!m_grpc->isConnected()) {
        dialogs::warn(m_parentWindow, title, "The gorganizer daemon must be running to change SMAPI.");
        return;
    }

    Operation op;
    op.kind = kind;
    op.gameId = gameId;
    op.gameName = m_game.name;
    if (const auto it = m_statuses.constFind(gameId); it != m_statuses.constEnd()) {
        if (kind == OperationKind::Update)
            op.targetVersion = it->latestVersion;
        else if (kind == OperationKind::Rollback)
            op.targetVersion = it->previousVersion;
    }

    const bool mountedHint = m_session->vfsMounted();
    const bool accepted = confirmed ? confirmUnmount(op, mountedHint) : confirmOperation(op, mountedHint);
    if (!accepted || !canStillStart(gameId))
        return;

    op.serial = ++m_nextOperationSerial;
    op.phase = Phase::Capturing;
    op.unmountConsented = mountedHint;
    m_op = op;
    m_session->suppressAutoMount(gameId);
    m_op->captureRequestId = m_grpc->queryVfsStatus(gameId);
    emit operationActivityChanged(gameId, true);
    m_statusBar->showMessage(QStringLiteral("Checking whether the mods of %1 are mounted…").arg(op.gameName));
    updateMenu();
}

bool ModLoaderController::confirmOperation(const Operation& op, bool mounted)
{
    const QString title = operationTitle(op.kind);
    const QString note = unmountNote(op.kind, op.gameName);
    const QString target = op.targetVersion.isEmpty() ? QString() : QStringLiteral(" to %1").arg(op.targetVersion);
    QString text;
    switch (op.kind) {
    case OperationKind::Install:
        text = QStringLiteral("Install SMAPI for %1?\n\ngorganizer downloads the newest stable SMAPI release from "
                              "GitHub, checks its published SHA-256 checksum, runs its installer on a private copy "
                              "of the game, and then applies the result to the game folder.").arg(op.gameName);
        break;
    case OperationKind::Update:
        text = QStringLiteral("Update SMAPI for %1%2?\n\ngorganizer downloads the newest stable SMAPI release from "
                              "GitHub, checks its published SHA-256 checksum, runs its installer on a private copy "
                              "of the game, and then applies the result to the game folder. The current version is "
                              "kept for a rollback.")
                   .arg(op.gameName, target);
        break;
    case OperationKind::Repair:
        text = QStringLiteral("Repair SMAPI for %1?\n\ngorganizer runs the SMAPI installer it kept from the last "
                              "install or update again, on a private copy of the game, and then applies the result "
                              "to the game folder. Nothing is downloaded; if no installer was kept, install SMAPI "
                              "instead.").arg(op.gameName);
        break;
    case OperationKind::Rollback:
        text = QStringLiteral("Roll SMAPI for %1 back%2?\n\nThe previous SMAPI release that gorganizer kept is "
                              "installed again; the current version becomes the one you can roll back to.")
                   .arg(op.gameName, target);
        break;
    case OperationKind::Uninstall:
        text = QStringLiteral("Uninstall SMAPI from %1?\n\nThe game's original launcher is restored and SMAPI's own "
                              "files are removed. Your mods in the game's Mods folder are kept, and the downloaded "
                              "SMAPI release stays cached so you can install it again later.").arg(op.gameName);
        if (mounted)
            text += QStringLiteral("\n\n") + note;
        return dialogs::plainConfirmDestructive(m_parentWindow, title, text, "Uninstall");
    }
    if (mounted)
        text += QStringLiteral("\n\n") + note;
    return dialogs::plainConfirm(m_parentWindow, title, text);
}

bool ModLoaderController::confirmUnmount(const Operation& op, bool mounted)
{
    if (!mounted)
        return true;
    return dialogs::plainConfirm(m_parentWindow, operationTitle(op.kind),
                                 unmountNote(op.kind, op.gameName) + QStringLiteral("\n\nContinue?"));
}

QString ModLoaderController::unmountNote(OperationKind kind, const QString& gameName)
{
    return QStringLiteral("%1 SMAPI needs the mods unmounted first, so close %2 if it is still running. New files "
                          "written while playing are captured into Overwrite. The mods are mounted again afterwards.")
        .arg(operationProgressive(kind), gameName);
}

bool ModLoaderController::canStillStart(const QString& gameId) const
{
    return !m_op && managesSmapi(m_game) && m_game.shortName == gameId && m_grpc->isConnected();
}

void ModLoaderController::onVfsStatusQueried(quint64 requestId, const GrpcVFSStatus& status)
{
    if (!m_op)
        return;
    if (m_op->phase == Phase::Capturing && requestId == m_op->captureRequestId) {
        onPreStateCaptured(status);
        return;
    }
    if (m_op->phase != Phase::Unmounting || requestId != m_op->vfsCheckRequestId)
        return;
    m_op->vfsCheckRequestId = 0;
    if (!status.mounted) {
        runOperation();
        return;
    }
    const QString message = QStringLiteral("The mods of %1 could not be unmounted, so SMAPI was not changed.\n\n%2")
                                .arg(m_op->gameName, capped(unmountErrorText(m_op->unmountError)));
    abortOperation(message, QString(), false);
}

void ModLoaderController::onVfsStatusQueryFailed(quint64 requestId, const QString& gameId, const QString& error, int grpcCode)
{
    Q_UNUSED(gameId);
    if (!m_op)
        return;
    if (m_op->phase == Phase::Capturing && requestId == m_op->captureRequestId) {
        const QString message = QStringLiteral("gorganizer could not check whether the mods of %1 are mounted, so "
                                               "SMAPI was not changed.\n\n%2").arg(m_op->gameName, errorSummary("check mounted mods", GrpcError{grpcCode, QStringLiteral("GetVFSStatus"), error}, false));
        abortOperation(message, QString(), true);
        return;
    }
    if (m_op->phase == Phase::Unmounting && requestId == m_op->vfsCheckRequestId) {
        m_op->vfsCheckRequestId = 0;
        m_pollTimer->start(kUnmountCheckIntervalMs);
    }
}

void ModLoaderController::onPreStateCaptured(const GrpcVFSStatus& status)
{
    m_op->captureRequestId = 0;
    m_op->wasMounted = status.mounted;
    m_op->mountedProfile = status.profileName;
    if (status.mounted && m_op->mountedProfile.isEmpty())
        m_op->mountedProfile = m_session->currentProfile();
    if (!status.mounted) {
        runOperation();
        return;
    }
    if (!m_op->unmountConsented) {
        const quint64 serial = m_op->serial;
        const Operation asked = *m_op;
        m_op->phase = Phase::Consenting;
        updateMenu();
        const bool accepted = confirmUnmount(asked, true);
        if (!m_op || m_op->serial != serial)
            return;
        if (!accepted) {
            abortOperation(QString(), QString(), false);
            return;
        }
        m_op->unmountConsented = true;
    }
    startUnmount();
}

void ModLoaderController::startUnmount()
{
    const QString notConnected = QStringLiteral("The mods of %1 could not be unmounted because the gorganizer daemon "
                                                "is not connected, so SMAPI was not changed.").arg(m_op->gameName);
    if (!m_grpc->isConnected()) {
        abortOperation(notConnected, QString(), false);
        return;
    }
    m_op->phase = Phase::Unmounting;
    m_op->unmountRequestId = m_session->unmountForMaintenance(m_op->gameId);
    if (m_op->unmountRequestId == 0) {
        abortOperation(notConnected, QString(), false);
        return;
    }
    m_statusBar->showMessage(QString("Unmounting the mods of %1 before %2 SMAPI…")
                                 .arg(m_op->gameName, operationProgressive(m_op->kind).toLower()));
    updateMenu();
}

void ModLoaderController::onMaintenanceUnmountFinished(quint64 requestId, const QString& gameId, bool ok,
                                                       int grpcCode, const QString& error)
{
    Q_UNUSED(grpcCode);
    if (!m_op || m_op->phase != Phase::Unmounting || requestId != m_op->unmountRequestId || gameId != m_op->gameId)
        return;
    m_op->unmountReturned = true;
    if (ok) {
        runOperation();
        return;
    }
    m_op->unmountError = error;
    m_statusBar->showMessage(QStringLiteral("Checking whether the mods of %1 were unmounted…").arg(m_op->gameName));
    m_phaseTimer->start(kUnmountCheckLimitMs);
    sendUnmountCheck();
}

void ModLoaderController::sendUnmountCheck()
{
    if (!m_op || m_op->phase != Phase::Unmounting || !m_op->unmountReturned || m_op->vfsCheckRequestId != 0
        || !m_grpc->isConnected())
        return;
    m_pollTimer->stop();
    m_op->vfsCheckRequestId = m_grpc->queryVfsStatus(m_op->gameId);
}

void ModLoaderController::onPollTimeout()
{
    if (!m_op)
        return;
    if (m_op->phase == Phase::Unmounting)
        sendUnmountCheck();
    else if (m_op->phase == Phase::Reconciling)
        sendReconcilePoll();
}

void ModLoaderController::onPhaseTimeout()
{
    if (!m_op)
        return;
    if (m_op->phase == Phase::Unmounting) {
        QString message = QStringLiteral("gorganizer could not confirm that the mods of %1 were unmounted, so SMAPI "
                                         "was not changed.").arg(m_op->gameName);
        if (!m_op->unmountError.isEmpty())
            message += QStringLiteral("\n\n%1").arg(capped(unmountErrorText(m_op->unmountError)));
        message += QStringLiteral("\n\nIf the mods ended up unmounted, choose Remount Mods to mount them again. "
                                  "Run also mounts them before it starts the game.");
        const QString profile = m_op->mountedProfile;
        const std::optional<Operation> op = releaseOperation();
        m_session->finishMaintenance(op->gameId, QString(), false);
        m_statusBar->clearMessage();
        warnWithRemount(operationTitle(op->kind), message, op->gameId, profile, op->unmountError);
        return;
    }
    if (m_op->phase != Phase::Reconciling)
        return;
    const std::optional<Operation> op = releaseOperation();
    m_session->finishMaintenance(op->gameId, remountProfileFor(*op), true);
    if (m_grpc->isConnected())
        m_statusFloor.insert(op->gameId, requestStatus(op->gameId, false));
    m_statusBar->clearMessage();
    QString message = QStringLiteral("gorganizer waited 30 minutes for the gorganizer daemon to finish %1 SMAPI for "
                                     "%2 without learning the result, so it stopped waiting. Tools → SMAPI shows "
                                     "SMAPI's status once the daemon answers.")
                          .arg(operationProgressive(op->kind).toLower(), op->gameName);
    if (!op->wasMounted) {
        dialogs::plainWarn(m_parentWindow, operationTitle(op->kind), message);
        return;
    }
    message += QStringLiteral("\n\ngorganizer asked to mount the mods again. If the daemon refused because SMAPI "
                              "was still busy, choose Remount Mods once it finishes; Run also mounts them before it "
                              "starts the game.");
    warnWithRemount(operationTitle(op->kind), message, op->gameId, remountProfileFor(*op));
}

void ModLoaderController::warnWithRemount(const QString& title, const QString& message, const QString& gameId,
                                          const QString& profileName, const QString& rawError)
{
    if (profileName.isEmpty() && rawError.isEmpty()) {
        dialogs::plainWarn(m_parentWindow, title, message);
        return;
    }
    QMessageBox box(m_parentWindow);
    box.setWindowTitle(title);
    box.setIcon(QMessageBox::Warning);
    box.setTextFormat(Qt::PlainText);
    box.setText(message);
    QPushButton* remount = nullptr;
    if (!profileName.isEmpty()) {
        remount = box.addButton(QStringLiteral("Remount Mods"), QMessageBox::AcceptRole);
        box.setDefaultButton(remount);
    }
    box.addButton(QMessageBox::Close);
    if (!rawError.isEmpty())
        attachErrorDetails(&box, title, QStringLiteral("unmount mods"), rawError);
    box.exec();
    if (remount && box.clickedButton() == remount)
        m_session->remountAfterMaintenance(gameId, profileName);
}

void ModLoaderController::runOperation()
{
    if (!m_op)
        return;
    m_pollTimer->stop();
    m_phaseTimer->stop();
    if (!m_grpc->isConnected()) {
        QString message = QStringLiteral("The connection to the gorganizer daemon was lost before %1 SMAPI for %2 "
                                         "could start, so SMAPI was not changed.")
                              .arg(operationProgressive(m_op->kind).toLower(), m_op->gameName);
        if (m_op->wasMounted)
            message += QStringLiteral(" The mods are mounted again once the daemon is reachable.");
        abortOperation(message, remountProfileFor(*m_op), true);
        return;
    }
    m_op->phase = Phase::Running;
    const QString gameId = m_op->gameId;
    switch (m_op->kind) {
    case OperationKind::Install:
    case OperationKind::Update:
        m_op->requestId = m_grpc->installModLoader(gameId, false);
        break;
    case OperationKind::Repair:
        m_op->requestId = m_grpc->installModLoader(gameId, true);
        break;
    case OperationKind::Rollback:
        m_op->requestId = m_grpc->rollbackModLoader(gameId);
        break;
    case OperationKind::Uninstall:
        m_op->requestId = m_grpc->uninstallModLoader(gameId);
        break;
    }
    const QString progressive = operationProgressive(m_op->kind);
    progressDialog()->begin(operationTitle(m_op->kind),
                            QStringLiteral("%1 SMAPI for %2…").arg(progressive, m_op->gameName));
    m_statusBar->showMessage(QStringLiteral("%1 SMAPI…").arg(progressive));
    updateMenu();
}

void ModLoaderController::startReconciling(const QString& reason)
{
    m_disconnectTimer->stop();
    m_op->phase = Phase::Reconciling;
    m_op->unknownOutcome = reason;
    m_phaseTimer->start(kReconcileLimitMs);
    m_pollTimer->start(kReconcileFirstPollMs);
    const QString text = QStringLiteral("Waiting for the gorganizer daemon to finish %1 SMAPI for %2…")
                             .arg(operationProgressive(m_op->kind).toLower(), m_op->gameName);
    if (m_progress)
        m_progress->showNote(text);
    m_statusBar->showMessage(text);
    updateMenu();
}

void ModLoaderController::sendReconcilePoll()
{
    if (!m_op || m_op->phase != Phase::Reconciling || m_op->reconcileRequestId != 0 || !m_grpc->isConnected())
        return;
    m_pollTimer->stop();
    m_op->reconcileRequestId = m_grpc->pollModLoaderStatus(m_op->gameId);
}

std::optional<ModLoaderController::Operation> ModLoaderController::releaseOperation()
{
    std::optional<Operation> op = m_op;
    m_op.reset();
    m_pollTimer->stop();
    m_phaseTimer->stop();
    m_disconnectTimer->stop();
    if (m_progress)
        m_progress->finish();
    if (op)
        emit operationActivityChanged(op->gameId, false);
    updateMenu();
    return op;
}

void ModLoaderController::abortOperation(const QString& message, const QString& remountProfile, bool replaySkipped)
{
    const std::optional<Operation> op = releaseOperation();
    if (op)
        m_session->finishMaintenance(op->gameId, remountProfile, replaySkipped);
    m_statusBar->clearMessage();
    if (!message.isEmpty())
        dialogs::plainWarn(m_parentWindow, op ? operationTitle(op->kind) : QStringLiteral("SMAPI"), message);
}

QString ModLoaderController::remountProfileFor(const Operation& op)
{
    return op.wasMounted ? op.mountedProfile : QString();
}

void ModLoaderController::onOperationFinished(quint64 requestId, const QString& gameId, const QString& operation,
                                              bool ok, int grpcCode, const GrpcModLoaderStatus& status,
                                              const QString& error)
{
    Q_UNUSED(operation);
    const bool matches = m_op && m_op->requestId == requestId
        && (m_op->phase == Phase::Running || m_op->phase == Phase::Reconciling);
    if (!matches) {
        if (managesSmapi(m_game) && m_game.shortName == gameId && m_grpc->isConnected())
            refreshStatus(gameId);
        return;
    }
    if (!ok && grpcOutcomeUnknown(grpcCode)) {
        if (m_op->phase == Phase::Running)
            startReconciling(error);
        return;
    }
    completeOperation(ok, status, error);
}

void ModLoaderController::completeOperation(bool ok, const GrpcModLoaderStatus& status, const QString& error)
{
    const QString failureDetail = m_progress ? m_progress->lastFailure() : QString();
    const std::optional<Operation> op = releaseOperation();
    const InstallError busy = parseInstallError(error);
    const bool mountedMeanwhile = !ok && busy.token == QLatin1String("modloader_busy")
        && busy.fields.value(QStringLiteral("operation")) == QLatin1String("mounted");
    m_session->finishMaintenance(op->gameId, mountedMeanwhile ? QString() : remountProfileFor(*op), !mountedMeanwhile);
    if (ok)
        applyStatus(op->gameId, status);
    if (m_grpc->isConnected()) {
        m_statusFloor.insert(op->gameId, requestStatus(op->gameId, false));
        if (mountedMeanwhile)
            m_grpc->getVfsStatus(op->gameId);
    }
    updateMenu();
    reportOutcome(*op, ok, status, error, failureDetail);
}

void ModLoaderController::completeReconciled(const GrpcModLoaderStatus& status)
{
    const std::optional<Operation> op = releaseOperation();
    m_session->finishMaintenance(op->gameId, remountProfileFor(*op), true);
    applyStatus(op->gameId, status);
    updateMenu();
    const GrpcModLoaderStatus shown = m_statuses.value(op->gameId, status);
    QString text = QStringLiteral("gorganizer could not confirm the result of %1 SMAPI for %2, so it waited for the "
                                  "gorganizer daemon to finish.")
                       .arg(operationProgressive(op->kind).toLower(), op->gameName);
    if (!op->unknownOutcome.isEmpty())
        text += QStringLiteral("\n\n%1").arg(errorSummary("change SMAPI", op->unknownOutcome, true));
    if (!op->finalReport.isEmpty())
        text += QStringLiteral("\n\nGorganizer last reported a problem with SMAPI.");
    text += QStringLiteral("\n\nSMAPI now: %1").arg(describeStatus(shown));
    if (!shown.detail.isEmpty())
        text += QStringLiteral("\n\n%1").arg(errorSummary("check SMAPI's status", shown.detail));
    m_statusBar->showMessage(describeStatus(shown), 5000);
    dialogs::plainInfo(m_parentWindow, operationTitle(op->kind), text);
}

void ModLoaderController::reportOutcome(const Operation& op, bool ok, const GrpcModLoaderStatus& status,
                                        const QString& error, const QString& failureDetail)
{
    const QString title = operationTitle(op.kind);
    if (!ok) {
        m_statusBar->showMessage(QStringLiteral("%1 SMAPI failed.").arg(operationProgressive(op.kind)), 5000);
        presentError(m_parentWindow, title, title.toLower(), error, true,
                     failureDetail == error ? QString() : failureDetail);
        return;
    }
    QString text;
    switch (op.kind) {
    case OperationKind::Install:
    case OperationKind::Update:
    case OperationKind::Repair:
        text = status.installedVersion.isEmpty()
            ? QStringLiteral("SMAPI is installed for %1.").arg(op.gameName)
            : QStringLiteral("SMAPI %1 is installed for %2.").arg(status.installedVersion, op.gameName);
        break;
    case OperationKind::Rollback:
        text = status.installedVersion.isEmpty()
            ? QStringLiteral("SMAPI for %1 was rolled back.").arg(op.gameName)
            : QStringLiteral("SMAPI for %1 was rolled back to %2.").arg(op.gameName, status.installedVersion);
        break;
    case OperationKind::Uninstall:
        text = QStringLiteral("SMAPI was uninstalled from %1. The game's original launcher is restored, and your "
                              "mods in the game's Mods folder were kept.").arg(op.gameName);
        break;
    }
    m_statusBar->showMessage(text, 5000);
    if (op.kind != OperationKind::Uninstall && status.state != GrpcModLoaderStateOk && !status.detail.isEmpty())
        text += QStringLiteral("\n\nSMAPI still needs attention. %1")
                    .arg(errorSummary(QStringLiteral("check SMAPI's status"), status.detail));
    dialogs::plainInfo(m_parentWindow, title, text);
}

void ModLoaderController::onDaemonInfo(const QString& info)
{
    static const QRegularExpression finalLine(QStringLiteral("^\\[smapi:(done|failed)\\]\\s*(.*)$"),
                                              QRegularExpression::DotMatchesEverythingOption);
    const auto match = finalLine.match(info);
    if (!match.hasMatch())
        return;
    if (m_op) {
        if (m_op->phase == Phase::Running || m_op->phase == Phase::Reconciling) {
            const QString detail = match.captured(2).trimmed();
            m_op->finalReport = capped(match.captured(1) == QLatin1String("failed")
                                           ? QStringLiteral("failed: %1").arg(detail)
                                           : detail);
        }
        if (m_op->phase == Phase::Reconciling)
            sendReconcilePoll();
        return;
    }
    if (!managesSmapi(m_game) || !m_grpc->isConnected())
        return;
    refreshStatus(m_game.shortName);
}

void ModLoaderController::onConnected()
{
    m_disconnectTimer->stop();
    if (m_op && m_op->phase == Phase::Unmounting)
        sendUnmountCheck();
    else if (m_op && m_op->phase == Phase::Reconciling)
        sendReconcilePoll();
    if (!managesSmapi(m_game)) {
        updateMenu();
        return;
    }
    const QString gameId = m_game.shortName;
    if (!m_pendingStatus.contains(gameId))
        requestStatus(gameId, false);
    scheduleLatestCheck(gameId);
    updateMenu();
}

void ModLoaderController::onDisconnected()
{
    if (m_op && m_op->phase == Phase::Running && !m_disconnectTimer->isActive())
        m_disconnectTimer->start();
    updateMenu();
}

void ModLoaderController::onDisconnectTimeout()
{
    if (!m_op || m_op->phase != Phase::Running || m_grpc->isConnected())
        return;
    startReconciling(QStringLiteral("the connection to the gorganizer daemon was lost"));
}

void ModLoaderController::updateMenu()
{
    const bool supported = managesSmapi(m_game);
    m_menu->menuAction()->setVisible(supported);
    if (!supported)
        return;

    const auto it = m_statuses.constFind(m_game.shortName);
    const bool known = it != m_statuses.constEnd();
    const GrpcModLoaderStatus status = known ? it.value() : GrpcModLoaderStatus{};
    const bool connected = m_grpc->isConnected();
    const bool idle = !m_op && connected && !(known && status.busy);
    const bool unsupportedBuild = known && status.state == GrpcModLoaderStateUnsupportedBuild;

    m_statusAction->setText(statusLine());
    const QString detail = known && !status.detail.isEmpty()
        ? errorSummary(QStringLiteral("check SMAPI's status"), status.detail) : QString();
    m_statusAction->setToolTip(detail.isEmpty() ? QString() : Qt::convertFromPlainText(detail, Qt::WhiteSpaceNormal));

    if (known && status.updateAvailable && !status.latestVersion.isEmpty())
        m_installAction->setText(QStringLiteral("Update SMAPI to %1…").arg(status.latestVersion));
    else if (known && status.state != GrpcModLoaderStateNotInstalled && status.state != GrpcModLoaderStateUnspecified)
        m_installAction->setText(QStringLiteral("Install Latest SMAPI…"));
    else
        m_installAction->setText(QStringLiteral("Install SMAPI…"));
    m_installAction->setEnabled(idle && known && !unsupportedBuild);

    m_repairAction->setEnabled(idle && known && repairable(status.state));

    m_rollbackAction->setText(status.previousVersion.isEmpty()
                                  ? QStringLiteral("Roll Back SMAPI…")
                                  : QStringLiteral("Roll Back SMAPI to %1…").arg(status.previousVersion));
    m_rollbackAction->setEnabled(idle && known && !unsupportedBuild && !status.previousVersion.isEmpty());

    m_uninstallAction->setEnabled(idle && known && !unsupportedBuild
                                  && status.state != GrpcModLoaderStateNotInstalled
                                  && status.state != GrpcModLoaderStateUnspecified);

    m_checkAction->setEnabled(connected && !m_op && m_interactiveCheckId == 0);
}

QString ModLoaderController::statusLine() const
{
    if (m_op && m_op->gameId == m_game.shortName) {
        switch (m_op->phase) {
        case Phase::Capturing:
        case Phase::Consenting:
            return QStringLiteral("SMAPI — checking the mods…");
        case Phase::Unmounting:
            return QStringLiteral("SMAPI — unmounting mods…");
        case Phase::Running:
            return QStringLiteral("SMAPI — %1…").arg(operationProgressive(m_op->kind).toLower());
        case Phase::Reconciling:
            return QStringLiteral("SMAPI — waiting for the daemon to finish…");
        }
    }
    const auto it = m_statuses.constFind(m_game.shortName);
    if (it == m_statuses.constEnd())
        return QStringLiteral("SMAPI — status unknown");
    return describeStatus(it.value());
}

QString ModLoaderController::describeStatus(const GrpcModLoaderStatus& status)
{
    QString line;
    switch (status.state) {
    case GrpcModLoaderStateOk:
        line = status.installedVersion.isEmpty() ? QStringLiteral("SMAPI — OK")
                                                 : QStringLiteral("SMAPI %1 — OK").arg(status.installedVersion);
        if (!status.managed)
            line += QStringLiteral(" (not installed by gorganizer)");
        break;
    case GrpcModLoaderStateNotInstalled:
        line = QStringLiteral("SMAPI — not installed");
        break;
    case GrpcModLoaderStateLauncherReverted:
        line = QStringLiteral("SMAPI — launcher reverted by Steam; repair needed");
        break;
    case GrpcModLoaderStateIncomplete:
        line = QStringLiteral("SMAPI — incomplete; repair needed");
        break;
    case GrpcModLoaderStateUnsupportedBuild:
        line = QStringLiteral("SMAPI — unsupported game build (Windows/Proton)");
        break;
    case GrpcModLoaderStateInterrupted:
        line = QStringLiteral("SMAPI — interrupted operation; repair or restart gorganizer");
        break;
    case GrpcModLoaderStateUnspecified:
        line = QStringLiteral("SMAPI — status unknown");
        break;
    }
    if (status.updateAvailable && !status.latestVersion.isEmpty())
        line += QStringLiteral(" — update %1 available").arg(status.latestVersion);
    if (status.busy)
        line += QStringLiteral(" (busy)");
    return line;
}

ModLoaderProgressDialog* ModLoaderController::progressDialog()
{
    if (!m_progress) {
        m_progress = new ModLoaderProgressDialog(m_parentWindow);
        connect(m_grpc, &GrpcClient::daemonInfo, m_progress, &ModLoaderProgressDialog::onDaemonInfo);
    }
    return m_progress;
}

QString ModLoaderController::operationTitle(OperationKind kind)
{
    switch (kind) {
    case OperationKind::Install:
        return QStringLiteral("Install SMAPI");
    case OperationKind::Update:
        return QStringLiteral("Update SMAPI");
    case OperationKind::Repair:
        return QStringLiteral("Repair SMAPI");
    case OperationKind::Rollback:
        return QStringLiteral("Roll Back SMAPI");
    case OperationKind::Uninstall:
        return QStringLiteral("Uninstall SMAPI");
    }
    return QStringLiteral("SMAPI");
}

QString ModLoaderController::operationProgressive(OperationKind kind)
{
    switch (kind) {
    case OperationKind::Install:
        return QStringLiteral("Installing");
    case OperationKind::Update:
        return QStringLiteral("Updating");
    case OperationKind::Repair:
        return QStringLiteral("Repairing");
    case OperationKind::Rollback:
        return QStringLiteral("Rolling back");
    case OperationKind::Uninstall:
        return QStringLiteral("Uninstalling");
    }
    return QStringLiteral("Changing");
}

}

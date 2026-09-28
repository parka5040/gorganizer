#include "LaunchController.h"
#include "GrpcClient.h"
#include "InstallErrorText.h"
#include "ErrorPresenter.h"
#include "ModLoaderController.h"
#include "SessionController.h"
#include "RunButtonWidget.h"
#include "Dialogs.h"

#include <QMessageBox>
#include <QPushButton>
#include <QStatusBar>

namespace gorganizer {

LaunchController::LaunchController(AppConfig& config, GrpcClient* grpc,
                                   SessionController* session,
                                   ModLoaderController* modLoader,
                                   RunButtonWidget* runButton, QStatusBar* statusBar,
                                   QWidget* parentWindow)
    : QObject(parentWindow)
    , m_config(config)
    , m_grpc(grpc)
    , m_session(session)
    , m_modLoader(modLoader)
    , m_runButton(runButton)
    , m_statusBar(statusBar)
    , m_parentWindow(parentWindow)
{
    connect(m_grpc, &GrpcClient::gameLaunched, this, &LaunchController::onGameLaunched);
    connect(m_grpc, &GrpcClient::gameLaunchFailed, this, &LaunchController::onGameLaunchFailed);
    connect(m_grpc, &GrpcClient::connected, this, &LaunchController::updateRunEnabled);
    connect(m_grpc, &GrpcClient::disconnected, this, &LaunchController::updateRunEnabled);
    connect(m_modLoader, &ModLoaderController::operationActivityChanged, this,
            &LaunchController::onModLoaderActivityChanged);
    connect(m_session, &SessionController::activeGameChanged, this, &LaunchController::onActiveGameChanged);
    connect(m_session, &SessionController::profileSwitchActivityChanged, this, &LaunchController::updateRunEnabled);
    updateRunEnabled();
}

void LaunchController::updateRunEnabled()
{
    const bool loaderBusy = m_modLoader->operationActiveFor(m_session->activeGame().shortName);
    const bool connected = m_grpc->isConnected();
    m_runButton->setEnabled(connected && !m_launchPending && !loaderBusy && !m_session->profileSwitchPending());
    m_runButton->setToolTip(connected ? QString()
        : QStringLiteral("Run is unavailable until Gorganizer's background service reconnects."));
}

void LaunchController::onModLoaderActivityChanged(const QString& gameId, bool active)
{
    Q_UNUSED(gameId);
    Q_UNUSED(active);
    updateRunEnabled();
}

void LaunchController::onActiveGameChanged(const GameInfo& game)
{
    Q_UNUSED(game);
    updateRunEnabled();
}

void LaunchController::onRunGame()
{
    if (m_session->profileSwitchPending())
        return;
    if (!m_grpc->isConnected()) {
        dialogs::warn(m_parentWindow, "Not Connected",
            "Gorganizer's background service is not connected, so it cannot check your mods before starting the game. Wait for the connection indicator to turn green, then press Run again.");
        return;
    }

    if (!m_session->activeGame().detected) {
        dialogs::warn(m_parentWindow, "No Game Selected", "Please select a game first.");
        return;
    }

    if (m_modLoader->operationActiveFor(m_session->activeGame().shortName)) {
        dialogs::info(m_parentWindow, "SMAPI Operation Running",
            "SMAPI is being changed right now. Launch the game when that finishes.");
        return;
    }

    auto target = m_runButton->currentTarget();
    if (target.type == RunButtonWidget::TargetInstallModLoader) {
        m_modLoader->startInstall(m_session->activeGame().shortName);
        return;
    }
    if (target.type == RunButtonWidget::TargetRepairModLoader) {
        m_modLoader->startRepair(m_session->activeGame().shortName);
        return;
    }
    if (target.type == RunButtonWidget::TargetInstallTool) {
        if (target.toolId == "ttw-install") {
            emit ttwInstallRequested();
            return;
        }
        m_statusBar->showMessage(
            QString("Downloading %1 from Nexus...").arg(target.label));
        QString name, err;
        if (!m_grpc->installScriptExtender(m_session->activeGame().shortName, name, err)) {
            presentError(m_parentWindow, "Install Failed", "install the script extender", err, true);
            return;
        }
        m_statusBar->showMessage(QString("%1 installed.").arg(name), 5000);
        m_runButton->setGame(m_session->activeGame(),
            m_config.lastToolFor(m_session->activeGame().shortName));
        return;
    }

    bool useTool = (target.type == RunButtonWidget::TargetTool);
    m_statusBar->showMessage(
        useTool ? QString("Preparing mods and launching %1...").arg(target.label)
                : QString("Preparing mods and launching %1...").arg(m_session->activeGame().name));
    m_launchPending = true;
    updateRunEnabled();
    m_grpc->launchGame(m_session->activeGame().shortName, useTool, m_session->currentProfile());
}

void LaunchController::onTargetChanged(const QString& toolId)
{
    const auto type = m_runButton->currentTarget().type;
    if (type == RunButtonWidget::TargetInstallModLoader || type == RunButtonWidget::TargetRepairModLoader)
        return;
    if (m_session->activeGame().detected)
        m_config.setLastToolFor(m_session->activeGame().shortName, toolId);
}

void LaunchController::onGameLaunched(int pid)
{
    m_launchPending = false;
    updateRunEnabled();
    m_statusBar->showMessage(QString("Game launched (PID %1)").arg(pid), 5000);
}

void LaunchController::onGameLaunchFailed(const QString& error, int grpcCode)
{
    m_launchPending = false;
    updateRunEnabled();
    const InstallError parsed = parseInstallError(error);
    if (parsed.token == QLatin1String("modloader_unavailable")) {
        const QString gameId = parsed.fields.value(QStringLiteral("game"));
        const QString reason = parsed.fields.value(QStringLiteral("reason"));
        m_modLoader->refreshStatus(gameId);
        if (reason == QLatin1String("unsupported_build"))
            presentError(m_parentWindow, "Launch Blocked", "launch this game", GrpcError{grpcCode, QStringLiteral("LaunchGame"), error}, true);
        else
            offerModLoaderFix(gameId, reason);
        m_session->refreshStatusInfo();
        return;
    }
    if (parsed.token == QLatin1String("modloader_busy")) {
        presentError(m_parentWindow, "Launch Blocked", "launch this game", GrpcError{grpcCode, QStringLiteral("LaunchGame"), error}, true);
        m_session->refreshStatusInfo();
        return;
    }
    if (parsed.token == QLatin1String("game_running") || parsed.token == QLatin1String("daemon_shutting_down")) {
        presentError(m_parentWindow, "Launch Blocked", "launch this game", GrpcError{grpcCode, QStringLiteral("LaunchGame"), error}, true);
        const QString gameId = parsed.fields.value(QStringLiteral("game"));
        if (!gameId.isEmpty() && m_grpc->isConnected())
            m_grpc->getVfsStatus(gameId);
        m_session->refreshStatusInfo();
        return;
    }
    if (parsed.token == QLatin1String("loader_missing")) {
        presentError(m_parentWindow, "Script Extender Launch Blocked", "launch this game", GrpcError{grpcCode, QStringLiteral("LaunchGame"), error}, true);
        m_session->refreshStatusInfo();
        return;
    }

    if (parsed.token == QLatin1String("fnv4gb_not_applied_for_ttw")) {
        presentError(m_parentWindow, "Patch FalloutNV.exe to 4GB", "launch this game", GrpcError{grpcCode, QStringLiteral("LaunchGame"), error}, true);
        m_session->refreshStatusInfo();
        return;
    }

    if (parsed.token == QLatin1String("xnvse_missing_for_ttw")) {
        presentError(m_parentWindow, "xNVSE Required", "launch this game", GrpcError{grpcCode, QStringLiteral("LaunchGame"), error}, true);
        m_session->refreshStatusInfo();
        return;
    }

    presentError(m_parentWindow, "Launch Failed", "launch this game", GrpcError{grpcCode, QStringLiteral("LaunchGame"), error}, true);
    m_session->refreshStatusInfo();
}

void LaunchController::offerModLoaderFix(const QString& gameId, const QString& reason)
{
    const QString title = QStringLiteral("SMAPI Required");
    const bool install = reason == QLatin1String("not_installed");
    QMessageBox box(m_parentWindow);
    box.setWindowTitle(title);
    box.setIcon(QMessageBox::Warning);
    box.setTextFormat(Qt::PlainText);
    box.setText(QStringLiteral("SMAPI is %1. Your enabled mods need it.").arg(modLoaderUnavailableReasonText(reason)));
    box.setInformativeText(install
        ? QStringLiteral("Installing downloads the newest stable SMAPI release from GitHub, checks its published "
                         "SHA-256 checksum, and applies it to the game folder.")
        : QStringLiteral("Repairing runs the SMAPI installer gorganizer kept from the last install or update "
                         "again and applies the result to the game folder; nothing is downloaded."));
    QPushButton* fixBtn = box.addButton(install ? QStringLiteral("Install SMAPI") : QStringLiteral("Repair SMAPI"),
                                        QMessageBox::AcceptRole);
    box.addButton(QMessageBox::Cancel);
    box.setDefaultButton(fixBtn);
    box.exec();
    if (box.clickedButton() != fixBtn)
        return;
    if (install)
        m_modLoader->startInstall(gameId, true);
    else
        m_modLoader->startRepair(gameId, true);
}

}

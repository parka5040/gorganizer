#include "SessionController.h"
#include "GrpcClient.h"
#include "GameSelectorWidget.h"
#include "ProfileSelectorWidget.h"
#include "ModListWidget.h"
#include "PluginListWidget.h"
#include "DownloadsLibraryView.h"
#include "RunButtonWidget.h"
#include "GameDetector.h"
#include "Dialogs.h"
#include "InstallErrorText.h"
#include "ErrorPresenter.h"

#include <QAction>
#include <QDir>
#include <QLabel>
#include <QPushButton>
#include <QSet>
#include <QSizePolicy>
#include <QStatusBar>
#include <QToolButton>

#include <algorithm>

namespace gorganizer {

SessionController::SessionController(AppConfig& config, GrpcClient* grpc,
                                     GameSelectorWidget* gameSelector,
                                     ProfileSelectorWidget* profileSelector,
                                     ModListWidget* modList,
                                     PluginListWidget* pluginList,
                                     DownloadsLibraryView* downloadsLibrary,
                                     RunButtonWidget* runButton,
                                     QToolButton* applyButton,
                                     QAction* unmountAction,
                                     QLabel* statusInfo,
                                     QStatusBar* statusBar,
                                     QWidget* parentWindow)
    : QObject(parentWindow)
    , m_config(config)
    , m_grpc(grpc)
    , m_gameSelector(gameSelector)
    , m_profileSelector(profileSelector)
    , m_modList(modList)
    , m_pluginList(pluginList)
    , m_downloadsLibrary(downloadsLibrary)
    , m_runButton(runButton)
    , m_applyButton(applyButton)
    , m_unmountAction(unmountAction)
    , m_statusInfo(statusInfo)
    , m_recoveryLabel(new QLabel(statusBar))
    , m_recoveryButton(new QPushButton("Check Again", statusBar))
    , m_statusBar(statusBar)
    , m_parentWindow(parentWindow)
{
    m_recoveryLabel->setTextFormat(Qt::PlainText);
    m_recoveryLabel->setWordWrap(true);
    m_recoveryLabel->setSizePolicy(QSizePolicy::Ignored, QSizePolicy::Preferred);
    m_statusBar->addPermanentWidget(m_recoveryLabel, 1);
    m_recoveryLabel->hide();
    m_statusBar->addPermanentWidget(m_recoveryButton);
    m_recoveryButton->hide();
    connect(m_recoveryButton, &QPushButton::clicked, this, &SessionController::onRecoveryAction);
    connect(m_grpc, &GrpcClient::gamesDetected, this, &SessionController::onGamesDetected);
    connect(m_grpc, &GrpcClient::vfsStatusChanged, this, &SessionController::onVfsStatusChanged);
    connect(m_grpc, &GrpcClient::vfsStatusReceived, this, &SessionController::onVfsStatusReceived);
    connect(m_grpc, &GrpcClient::vfsStatusQueried, this, [this](quint64 requestId, const GrpcVFSStatus& status) {
        if (requestId != m_autoMountQueryId || !m_activeGame.detected || status.gameId != m_activeGame.shortName)
            return;
        m_autoMountQueryId = 0;
        autoMountActiveProfile();
    });
    connect(m_grpc, &GrpcClient::vfsStatusQueryFailed, this,
            [this](quint64 requestId, const QString& gameId, const QString&) {
        if (requestId != m_autoMountQueryId || gameId != m_activeGame.shortName)
            return;
        m_autoMountQueryId = 0;
        m_recoveryMountSkipped.insert(gameId);
        m_statusBar->showMessage("Gorganizer couldn't check the game's mods. Choose the game again to retry.", 5000);
    });
    connect(m_grpc, &GrpcClient::vfsRecoveryRetried, this, [this](const QString& gameId) {
        if (gameId != m_retryGameId)
            return;
        m_retryGameId.clear();
        refreshRecoveryIndicator();
    });
    connect(m_grpc, &GrpcClient::rpcError, this, &SessionController::onRpcError);
    connect(m_grpc, &GrpcClient::connected, this, &SessionController::onConnected);
    connect(m_grpc, &GrpcClient::disconnected, this, [this] {
        const bool pending = m_lifecycleStates.value(m_activeGame.shortName)
            == GrpcVFSLifecycleState::RecoveryPending;
        m_lifecycleStates.clear();
        m_autoMountQueryId = 0;
        m_retryGameId.clear();
        m_recoveryButton->setEnabled(false);
        m_recoveryButton->setText(pending ? "Review…" : "Check Again");
    });
}

void SessionController::loadManagedGames()
{
    auto managedShortNames = m_config.managedGames();
    auto allDetected = GameDetector::detectAll();

    m_managedGames.clear();
    for (const QString& sn : managedShortNames) {
        auto it = std::find_if(allDetected.begin(), allDetected.end(),
                               [&sn](const GameInfo& g) { return g.shortName == sn; });
        if (it != allDetected.end())
            m_managedGames.push_back(*it);
    }

    m_gameSelector->setGames(m_managedGames);

    QString activeShort = m_config.activeGameShortName();
    m_gameSelector->setActiveGameByShortName(activeShort);

    auto current = m_gameSelector->currentGame();
    if (current.detected)
        switchToGame(current.appId);
}

void SessionController::onGamesDetected(const std::vector<GrpcGame>& detectedGames)
{
    auto managedShortNames = m_config.managedGames();
    QSet<QString> keep(managedShortNames.begin(), managedShortNames.end());

    m_managedGames.clear();
    bool ttwVfsActive = false;
    for (const auto& g : detectedGames) {
        if (g.gameId == "ttw" && g.vfsActive)
            ttwVfsActive = true;
        if (!keep.contains(g.gameId))
            continue;
        m_managedGames.push_back(toGameInfo(g));
    }
    m_gameSelector->setGames(m_managedGames);
    QString activeShort = m_config.activeGameShortName();
    m_gameSelector->setActiveGameByShortName(activeShort);
    m_runButton->setTTWVfsActive(ttwVfsActive);
    auto current = m_gameSelector->currentGame();
    if (current.detected)
        switchToGame(current.appId);
}

void SessionController::switchToGame(uint32_t appId)
{
    auto found = GameInfo::findIn(m_managedGames, appId);
    if (!found) {
        auto current = m_gameSelector->currentGame();
        if (!current.shortName.isEmpty())
            found = current;
    }
    const QString previousGame = m_activeGame.shortName;
    m_activeGame = found.value_or(GameInfo{});
    m_config.setActiveGameShortName(m_activeGame.shortName);
    if (m_activeGame.shortName != previousGame) {
        m_vfsMounted = false;
        m_lifecycleReason.clear();
        setVfsDirty(false);
    }
    m_autoMountQueryId = 0;

    if (m_grpc->isConnected())
        m_grpc->setActiveGame(m_activeGame.detected ? m_activeGame.shortName : QString());

    m_runButton->setGame(m_activeGame, m_config.lastToolFor(m_activeGame.shortName));
    m_pluginList->setSupported(usesPlugins(m_activeGame));
    m_pluginList->setModsDir(GameInfo::modsDirPathFor(m_activeGame.shortName));
    m_pluginList->loadForGame(m_activeGame);
    m_pluginList->setActiveProfile(m_currentProfile);

    emit activeGameChanged(m_activeGame);

    if (m_activeGame.detected) {
        QString modsDir = GameInfo::modsDirPathFor(m_activeGame.shortName);
        QDir().mkpath(modsDir);

        QString preferred = m_config.lastProfileFor(m_activeGame.shortName);
        if (!preferred.isEmpty())
            m_currentProfile = preferred;

        m_profileSelector->loadForGame(m_activeGame.shortName, preferred);
        m_modList->loadForGame(m_activeGame, m_currentProfile);
        if (m_downloadsLibrary)
            m_downloadsLibrary->setGame(m_activeGame);

        m_grpc->subscribeEvents(m_activeGame.shortName);

        if (m_grpc->isConnected())
            m_autoMountQueryId = m_grpc->queryVfsStatus(m_activeGame.shortName);
    }

    if (m_unmountAction)
        m_unmountAction->setEnabled(m_activeGame.detected && !recoveryBlocked(m_activeGame.shortName));
    refreshRecoveryIndicator();
}

void SessionController::onProfileChanged(const QString& profileName)
{
    m_currentProfile = profileName;
    if (m_activeGame.detected)
        m_config.setLastProfileFor(m_activeGame.shortName, profileName);
    m_modList->loadForGame(m_activeGame, profileName);
    m_pluginList->setActiveProfile(profileName);
    refreshStatusInfo();
    emit profileChanged(profileName);
}

void SessionController::onVfsStatusChanged(const GrpcVFSStatus& status)
{
    if (!m_activeGame.detected || status.gameId != m_activeGame.shortName)
        return;
    updateVfsStatus(status);
    m_vfsMounted = status.mounted;
    setVfsDirty(status.dirty);
    m_pluginList->refresh();
}

void SessionController::onVfsStatusReceived(const GrpcVFSStatus& status)
{
    if (!m_activeGame.detected || status.gameId != m_activeGame.shortName)
        return;
    updateVfsStatus(status);
    setVfsDirty(status.mounted && status.dirty);
    if (m_vfsMounted == status.mounted)
        return;
    m_vfsMounted = status.mounted;
    m_pluginList->refresh();
}

bool SessionController::recoveryBlocked(const QString& gameId) const
{
    const auto state = m_lifecycleStates.value(gameId);
    return state == GrpcVFSLifecycleState::RecoveryDeferred
        || state == GrpcVFSLifecycleState::RecoveryPending;
}

void SessionController::updateVfsStatus(const GrpcVFSStatus& status)
{
    const QString gameId = status.gameId;
    const bool wasBlocked = recoveryBlocked(gameId);
    m_lifecycleStates.insert(gameId, status.lifecycleState);
    m_lifecycleReason = status.lifecycleReason;
    if (recoveryBlocked(gameId))
        m_recoveryMountSkipped.insert(gameId);
    if (m_unmountAction)
        m_unmountAction->setEnabled(!recoveryBlocked(gameId));
    refreshRecoveryIndicator();
    if (status.lifecycleState != GrpcVFSLifecycleState::Ready
        || (!wasBlocked && !m_recoveryMountSkipped.contains(gameId) && !m_pendingRemounts.contains(gameId)))
        return;
    if (m_pendingRemounts.contains(gameId) && !m_autoMountSuppressed.contains(gameId)) {
        const QString profile = m_pendingRemounts.value(gameId);
        m_recoveryMountSkipped.remove(gameId);
        m_autoMountQueryId = 0;
        mountForMaintenance(gameId, profile);
    } else if (m_recoveryMountSkipped.remove(gameId)) {
        m_autoMountQueryId = 0;
        autoMountActiveProfile();
    }
}

void SessionController::setVfsDirty(bool dirty)
{
    m_vfsDirty = dirty;
    if (m_applyButton) {
        m_applyButton->setVisible(dirty);
        m_applyButton->setEnabled(dirty && !recoveryBlocked(m_activeGame.shortName));
    }
    if (dirty && !recoveryBlocked(m_activeGame.shortName))
        m_statusBar->showMessage("Mod changes pending — click \"Apply Changes\" or just launch.", 4000);
}

void SessionController::onApplyChanges()
{
    if (!m_activeGame.detected || m_currentProfile.isEmpty() || !m_grpc->isConnected()
        || recoveryBlocked(m_activeGame.shortName))
        return;
    if (m_autoMountSuppressed.contains(m_activeGame.shortName)) {
        m_statusBar->showMessage("Mod changes can't be applied while SMAPI is being changed; try again when it finishes.", 5000);
        return;
    }
    m_applyButton->setEnabled(false);
    m_statusBar->showMessage("Applying mod changes…");
    if (m_vfsMounted)
        m_grpc->rebuildVfs(m_activeGame.shortName);
    else
        m_grpc->mountVfsWithSwap(m_activeGame.shortName, m_currentProfile);
}

void SessionController::onUnmountMods()
{
    if (!m_activeGame.detected || !m_grpc->isConnected()
        || recoveryBlocked(m_activeGame.shortName))
        return;
    const QString gameId = m_activeGame.shortName;
    const auto refusedForLoader = [this, &gameId] {
        if (!m_autoMountSuppressed.contains(gameId))
            return false;
        dialogs::plainInfo(m_parentWindow, "Unmount mods",
            "SMAPI is being changed for this game right now, and gorganizer unmounts and mounts its mods as "
            "part of that. Try again when it finishes.");
        return true;
    };
    if (refusedForLoader())
        return;
    if (!dialogs::confirm(m_parentWindow, "Unmount mods",
            "Restore the game's vanilla Data folder?\n\nAny new writes (saves, tool output) "
            "are captured into Overwrite first. Do this when you've finished playing."))
        return;
    if (!m_activeGame.detected || m_activeGame.shortName != gameId || !m_grpc->isConnected()
        || recoveryBlocked(gameId))
        return;
    if (refusedForLoader())
        return;
    requestUnmount(gameId);
}

void SessionController::requestUnmount(const QString& gameId)
{
    m_grpc->unmountVfs(gameId);
    m_statusBar->showMessage("Unmounting mods…", 4000);
}

quint64 SessionController::unmountForMaintenance(const QString& gameId)
{
    if (gameId.isEmpty() || !m_grpc->isConnected())
        return 0;
    m_statusBar->showMessage("Unmounting mods…", 4000);
    return m_grpc->unmountVfsForMaintenance(gameId);
}

void SessionController::suppressAutoMount(const QString& gameId)
{
    if (gameId.isEmpty())
        return;
    m_autoMountSuppressed.insert(gameId);
    m_autoMountSkipped.remove(gameId);
    m_pendingRemounts.remove(gameId);
}

void SessionController::finishMaintenance(const QString& gameId, const QString& remountProfile, bool replaySkipped)
{
    if (gameId.isEmpty())
        return;
    m_autoMountSuppressed.remove(gameId);
    const bool skipped = m_autoMountSkipped.remove(gameId);
    if (!remountProfile.isEmpty()) {
        mountForMaintenance(gameId, remountProfile);
        return;
    }
    if (skipped && replaySkipped && m_activeGame.detected && m_activeGame.shortName == gameId
        && !m_currentProfile.isEmpty())
        mountForMaintenance(gameId, m_currentProfile);
}

void SessionController::remountAfterMaintenance(const QString& gameId, const QString& profileName)
{
    if (gameId.isEmpty() || profileName.isEmpty())
        return;
    if (m_autoMountSuppressed.contains(gameId)) {
        m_autoMountSkipped.insert(gameId);
        m_statusBar->showMessage("Mods stay unmounted while SMAPI is being changed; they are mounted when it finishes.", 5000);
        return;
    }
    mountForMaintenance(gameId, profileName);
}

void SessionController::mountForMaintenance(const QString& gameId, const QString& profileName)
{
    if (recoveryBlocked(gameId)) {
        m_pendingRemounts.insert(gameId, profileName);
        return;
    }
    if (!m_grpc->isConnected()) {
        m_pendingRemounts.insert(gameId, profileName);
        m_statusBar->showMessage("The mods are mounted again once the gorganizer daemon is reachable.", 5000);
        return;
    }
    m_pendingRemounts.remove(gameId);
    m_grpc->mountVfsWithSwap(gameId, profileName);
    m_statusBar->showMessage("Mounting mods again…", 4000);
}

void SessionController::onConnected()
{
    const QHash<QString, QString> pending = m_pendingRemounts;
    for (auto it = pending.cbegin(); it != pending.cend(); ++it) {
        if (!m_activeGame.detected || it.key() != m_activeGame.shortName || m_autoMountSuppressed.contains(it.key())
            || m_lifecycleStates.value(it.key()) != GrpcVFSLifecycleState::Ready)
            continue;
        mountForMaintenance(it.key(), it.value());
    }
}

void SessionController::autoMountActiveProfile()
{
    if (!m_activeGame.detected || !m_grpc->isConnected() || m_currentProfile.isEmpty())
        return;
    if (m_lifecycleStates.value(m_activeGame.shortName) != GrpcVFSLifecycleState::Ready) {
        m_recoveryMountSkipped.insert(m_activeGame.shortName);
        return;
    }
    m_recoveryMountSkipped.remove(m_activeGame.shortName);
    if (m_autoMountSuppressed.contains(m_activeGame.shortName)) {
        m_autoMountSkipped.insert(m_activeGame.shortName);
        m_statusBar->showMessage("Mods stay unmounted while SMAPI is being changed; they are mounted when it finishes.", 5000);
        return;
    }
    m_grpc->mountVfsWithSwap(m_activeGame.shortName, m_currentProfile);
}

void SessionController::onRpcError(const QString& method, const QString& error)
{
    if (method == QLatin1String("RetryVFSRecovery")) {
        const QString gameId = m_retryGameId;
        m_retryGameId.clear();
        refreshRecoveryIndicator();
        if (parseInstallError(error).token == QLatin1String("farm_recovery_deferred")) {
            if (m_activeGame.shortName == gameId)
                m_statusBar->showMessage("The game still seems to be running. Close it, then choose Check Again.", 5000);
            return;
        }
    }
    if (method == QLatin1String("MountVFS")) {
        const InstallError parsed = parseInstallError(error);
        const QString gameId = parsed.fields.value(QStringLiteral("game"));
        if (parsed.token == QLatin1String("farm_recovery_deferred")) {
            m_recoveryMountSkipped.insert(gameId);
            if (m_grpc->isConnected())
                m_grpc->getVfsStatus(gameId);
            return;
        }
        if (parsed.token == QLatin1String("modloader_busy") && m_autoMountSuppressed.contains(gameId)) {
            m_autoMountSkipped.insert(gameId);
            m_statusBar->showMessage("Mods stay unmounted while SMAPI is being changed; they are mounted when it finishes.", 5000);
            return;
        }
    }
    if (method == "SetModList") {
        if (m_activeGame.detected)
            m_modList->reloadAfterFailedSave(m_activeGame, m_currentProfile);
        presentError(m_parentWindow, "Change not saved", "save mod choices", error, true);
        return;
    }
    if (method == QLatin1String("RebuildVFS") && parseInstallError(error).token == QLatin1String("game_running")) {
        presentError(m_parentWindow, "Apply Changes", "apply mod changes", error, true);
        return;
    }
    const QString operation = method == QLatin1String("MountVFS") ? QStringLiteral("mount mods")
        : method == QLatin1String("UnmountVFS") ? QStringLiteral("unmount mods")
        : method == QLatin1String("RebuildVFS") ? QStringLiteral("apply mod changes")
        : method == QLatin1String("RetryVFSRecovery") ? QStringLiteral("check recovery")
        : method == QLatin1String("RestoreFromBackup") ? QStringLiteral("restore the game files")
        : QStringLiteral("complete this request");
    m_statusBar->showMessage(errorSummary(operation, error, true), 5000);
}

void SessionController::onRecoveryAction()
{
    if (!m_activeGame.detected || !m_grpc->isConnected())
        return;
    const QString gameId = m_activeGame.shortName;
    const auto state = m_lifecycleStates.value(gameId);
    if (state == GrpcVFSLifecycleState::RecoveryPending) {
        emit recoveryReviewRequested(gameId);
        return;
    }
    if (state != GrpcVFSLifecycleState::RecoveryDeferred || !m_retryGameId.isEmpty())
        return;
    m_retryGameId = gameId;
    refreshRecoveryIndicator();
    m_grpc->retryVfsRecovery(gameId);
}

void SessionController::refreshRecoveryIndicator()
{
    const auto state = m_lifecycleStates.value(m_activeGame.shortName);
    const bool deferred = m_activeGame.detected && state == GrpcVFSLifecycleState::RecoveryDeferred;
    const bool pending = m_activeGame.detected && state == GrpcVFSLifecycleState::RecoveryPending;
    m_statusInfo->setVisible(!deferred && !pending);
    m_recoveryLabel->setVisible(deferred || pending);
    m_recoveryButton->setVisible(deferred || pending);
    m_recoveryButton->setEnabled(m_grpc->isConnected() && (pending || m_retryGameId.isEmpty()));
    m_recoveryButton->setText(pending ? "Review…"
        : m_retryGameId == m_activeGame.shortName ? "Checking…" : "Check Again");
    refreshStatusInfo();
}

void SessionController::refreshStatusInfo()
{
    if (!m_activeGame.detected) {
        m_statusInfo->setText("No game selected");
        return;
    }
    const auto state = m_lifecycleStates.value(m_activeGame.shortName);
    m_statusInfo->setText(QString("%1 - %2").arg(m_activeGame.name, m_currentProfile));
    if (state == GrpcVFSLifecycleState::RecoveryPending) {
        m_recoveryLabel->setText("Gorganizer needs your decision to finish an interrupted change.");
        m_recoveryLabel->setToolTip(m_recoveryLabel->text());
        return;
    }
    if (state != GrpcVFSLifecycleState::RecoveryDeferred)
        return;
    if (m_lifecycleReason == QLatin1String("launch_grace"))
        m_recoveryLabel->setText("The game was started moments ago, so your mods were left in place. "
                                 "Gorganizer will check again shortly.");
    else if (m_lifecycleReason == QLatin1String("process_scan_failed"))
        m_recoveryLabel->setText("Gorganizer couldn't check whether the game is running, "
                                 "so your mods were left in place.");
    else if (m_lifecycleReason == QLatin1String("launch_record_invalid")
             || m_lifecycleReason == QLatin1String("launch_record_unreadable"))
        m_recoveryLabel->setText("Gorganizer couldn't read its record of the last game launch, "
                                 "so your mods were left in place.");
    else
        m_recoveryLabel->setText("The game may still be running, so your mods were left in place. "
                                 "Gorganizer will finish once the game closes.");
    m_recoveryLabel->setToolTip(m_recoveryLabel->text());
}

}

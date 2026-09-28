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
#include <QEvent>
#include <QLabel>
#include <QMouseEvent>
#include <QPushButton>
#include <QSet>
#include <QSizePolicy>
#include <QStatusBar>
#include <QToolButton>
#include <QTimer>

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
    , m_modStatusLabel(new QLabel(statusBar))
    , m_recoveryButton(new QPushButton("Check Again", statusBar))
    , m_profileSwitchTimer(new QTimer(this))
    , m_statusBar(statusBar)
    , m_parentWindow(parentWindow)
{
    m_modStatusLabel->setTextFormat(Qt::PlainText);
    m_modStatusLabel->installEventFilter(this);
    m_modStatusLabel->setSizePolicy(QSizePolicy::Minimum, QSizePolicy::Preferred);
    m_statusBar->addPermanentWidget(m_modStatusLabel);
    m_statusBar->addPermanentWidget(m_recoveryButton);
    m_recoveryButton->hide();
    connect(m_statusBar, &QStatusBar::messageChanged, this, [this](const QString& message) {
        if (!profileSwitchPending())
            return;
        const QString switching = m_retargetStatusQueryId && m_requestedProfile.isEmpty()
            ? QStringLiteral("Checking which profile is active…")
            : QString("Switching to profile \"%1\"…").arg(m_requestedProfile);
        if (message != switching)
            m_statusBar->showMessage(switching);
    });
    connect(m_recoveryButton, &QPushButton::clicked, this, &SessionController::onRecoveryAction);
    connect(m_grpc, &GrpcClient::gamesDetected, this, &SessionController::onGamesDetected);
    connect(m_grpc, &GrpcClient::gamesListed, this, &SessionController::onGamesListed);
    connect(m_grpc, &GrpcClient::vfsStatusChanged, this, &SessionController::onVfsStatusChanged);
    connect(m_grpc, &GrpcClient::vfsStatusReceived, this, &SessionController::onVfsStatusReceived);
    connect(m_grpc, &GrpcClient::vfsRetargeted, this, &SessionController::onVfsRetargeted);
    connect(m_grpc, &GrpcClient::vfsRetargetFailed, this, &SessionController::onVfsRetargetFailed);
    m_profileSwitchTimer->setSingleShot(true);
    connect(m_profileSwitchTimer, &QTimer::timeout, this, &SessionController::startProfileSwitch);
    connect(m_modList, &ModListWidget::modListSavesDrained, this, [this] {
        updateProfileSwitchControls();
        startProfileSwitch();
    });
    connect(m_grpc, &GrpcClient::modListSaveFailed, this,
            [this](quint64, const QString& gameId, const QString& profileName, const QString& error) {
        if (!m_waitingForSaves || gameId != m_activeGame.shortName || profileName != m_currentProfile)
            return;
        m_waitingForSaves = false;
        m_profileSwitchTimer->stop();
        m_requestedProfile = m_appliedProfile;
        showProfile(m_appliedProfile);
        updateProfileSwitchControls();
        m_statusBar->showMessage("Couldn't save your mod choices. The active profile hasn't changed.", 5000);
        presentError(m_parentWindow, "Change not saved", "save mod choices", error, true);
    });
    connect(m_grpc, &GrpcClient::vfsStatusQueried, this, &SessionController::onRetargetStatusQueried);
    connect(m_grpc, &GrpcClient::vfsStatusQueryFailed, this, &SessionController::onRetargetStatusQueryFailed);
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
        m_waitingForSaves = false;
        m_profileSwitchTimer->stop();
        m_retargetRequestId = 0;
        m_retargetStatusQueryId = 0;
        m_retargetGameId.clear();
        m_appliedProfile.clear();
        m_vfsMounted = false;
        m_hasModStatus = false;
        m_steamMaintenance = GrpcSteamMaintenanceState::Unspecified;
        m_hasSavedSteamFiles = false;
        updateProfileSwitchControls();
        const bool pending = m_lifecycleStates.value(m_activeGame.shortName)
            == GrpcVFSLifecycleState::RecoveryPending;
        m_lifecycleStates.clear();
        m_autoMountQueryId = 0;
        m_retryGameId.clear();
        m_configuredGames.clear();
        m_detectedGames.clear();
        m_recoveryButton->setEnabled(false);
        m_recoveryButton->setText(pending ? "Review…" : "Check Again");
        refreshRecoveryIndicator();
    });
    refreshRecoveryIndicator();
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
    m_detectedGames = detectedGames;
    m_grpc->listGames();
}

void SessionController::onGamesListed(const std::vector<GrpcGame>& configuredGames)
{
    m_configuredGames = configuredGames;
    refreshManagedGames();
}

void SessionController::refreshManagedGames()
{
    m_managedGames.clear();
    for (const auto& shortName : m_config.managedGames()) {
        auto configured = std::find_if(m_configuredGames.begin(), m_configuredGames.end(),
            [&shortName](const GrpcGame& game) { return game.gameId == shortName; });
        if (configured != m_configuredGames.end()) {
            m_managedGames.push_back(toGameInfo(*configured));
            continue;
        }
        auto detected = std::find_if(m_detectedGames.begin(), m_detectedGames.end(),
            [&shortName](const GrpcGame& game) { return game.gameId == shortName; });
        if (detected != m_detectedGames.end())
            m_managedGames.push_back(toGameInfo(*detected));
    }
    m_gameSelector->setGames(m_managedGames);
    m_gameSelector->setActiveGameByShortName(m_config.activeGameShortName());
    auto ttw = std::find_if(m_configuredGames.begin(), m_configuredGames.end(),
        [](const GrpcGame& game) { return game.gameId == "ttw"; });
    m_runButton->setTTWVfsActive(ttw != m_configuredGames.end() && ttw->vfsActive);
    auto current = m_gameSelector->currentGame();
    if (current.detected)
        switchToGame(current.appId);
}

void SessionController::switchToGame(uint32_t appId)
{
    auto found = GameInfo::findIn(m_managedGames, appId);
    auto current = m_gameSelector->currentGame();
    if (!current.shortName.isEmpty() && (current.appId == appId || !found))
        found = current;
    const QString previousGame = m_activeGame.shortName;
    m_activeGame = found.value_or(GameInfo{});
    m_config.setActiveGameShortName(m_activeGame.shortName);
    if (m_activeGame.shortName != previousGame) {
        const bool wasSwitching = profileSwitchPending();
        m_waitingForSaves = false;
        m_profileSwitchTimer->stop();
        m_retargetRequestId = 0;
        m_retargetStatusQueryId = 0;
        m_retargetGameId.clear();
        m_appliedProfile.clear();
        m_vfsMounted = false;
        m_hasModStatus = false;
        m_steamMaintenance = GrpcSteamMaintenanceState::Unspecified;
        m_hasSavedSteamFiles = false;
        setVfsDirty(false);
        updateProfileSwitchControls();
        if (wasSwitching)
            m_statusBar->clearMessage();
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

        QString preferred = m_vfsMounted && !m_appliedProfile.isEmpty()
            ? m_appliedProfile : m_config.lastProfileFor(m_activeGame.shortName);
        if (previousGame != m_activeGame.shortName && !preferred.isEmpty())
            m_currentProfile = preferred;
        if (!profileSwitchPending())
            m_requestedProfile = m_currentProfile;

        m_profileSelector->loadForGame(m_activeGame.shortName, m_currentProfile);
        m_pluginList->setActiveProfile(m_currentProfile);
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

void SessionController::showProfile(const QString& profileName)
{
    m_profileSelector->selectProfileSilently(profileName);
    if (m_currentProfile == profileName)
        return;
    m_currentProfile = profileName;
    m_modList->loadForGame(m_activeGame, profileName);
    m_pluginList->setActiveProfile(profileName);
    refreshStatusInfo();
    emit profileChanged(profileName);
}

void SessionController::updateProfileSwitchControls()
{
    const bool switching = profileSwitchPending();
    m_profileSelector->setEnabled(!m_retargetRequestId && !m_retargetStatusQueryId
                                  && (!m_waitingForSaves || m_modList->modListSavesIdle()));
    if (m_applyButton)
        m_applyButton->setEnabled(m_vfsDirty && !switching && !recoveryBlocked(m_activeGame.shortName));
    if (m_unmountAction)
        m_unmountAction->setEnabled(m_activeGame.detected && !switching && !recoveryBlocked(m_activeGame.shortName));
    emit profileSwitchActivityChanged();
}

void SessionController::startProfileSwitch()
{
    if (!m_waitingForSaves || m_retargetRequestId || m_retargetStatusQueryId
        || m_profileSwitchTimer->isActive() || !m_modList->modListSavesIdle())
        return;
    if (!m_activeGame.detected || !m_grpc->isConnected() || !m_vfsMounted
        || recoveryBlocked(m_activeGame.shortName)
        || m_autoMountSuppressed.contains(m_activeGame.shortName)) {
        m_waitingForSaves = false;
        m_requestedProfile = m_appliedProfile;
        if (!m_appliedProfile.isEmpty())
            showProfile(m_appliedProfile);
        updateProfileSwitchControls();
        if (recoveryBlocked(m_activeGame.shortName))
            m_statusBar->showMessage("Gorganizer needs to finish recovering this game's mods before switching profiles.", 5000);
        else if (m_autoMountSuppressed.contains(m_activeGame.shortName))
            m_statusBar->showMessage("Mod activation is paused while SMAPI is being changed. Try again when it finishes.", 5000);
        else if (!m_grpc->isConnected())
            m_statusBar->showMessage("The background service disconnected before profiles could be switched.", 5000);
        else
            m_statusBar->showMessage("Mods are inactive for this game. Select a profile to use next time.", 5000);
        return;
    }
    if (m_requestedProfile == m_appliedProfile) {
        m_waitingForSaves = false;
        showProfile(m_appliedProfile);
        updateProfileSwitchControls();
        m_statusBar->clearMessage();
        return;
    }
    m_waitingForSaves = false;
    showProfile(m_requestedProfile);
    m_retargetGameId = m_activeGame.shortName;
    m_retargetRequestId = m_grpc->retargetVfs(m_retargetGameId, m_requestedProfile);
    updateProfileSwitchControls();
}

void SessionController::onProfileChanged(const QString& profileName)
{
    if (!m_activeGame.detected || profileName.isEmpty())
        return;
    if (!profileSwitchPending() && profileName == m_currentProfile)
        emit profileChanged(profileName);
    if (!m_vfsMounted || m_appliedProfile.isEmpty()) {
        m_requestedProfile = profileName;
        showProfile(profileName);
        m_config.setLastProfileFor(m_activeGame.shortName, profileName);
        if (m_autoMountSuppressed.contains(m_activeGame.shortName))
            m_statusBar->showMessage("Mod activation is paused while SMAPI is being changed. Try again when it finishes.", 5000);
        else if (recoveryBlocked(m_activeGame.shortName))
            refreshRecoveryIndicator();
        return;
    }
    if (recoveryBlocked(m_activeGame.shortName)) {
        refreshRecoveryIndicator();
        m_statusBar->showMessage("Gorganizer needs to finish recovering this game's mods before switching profiles.", 5000);
        showProfile(m_appliedProfile);
        return;
    }
    if (m_autoMountSuppressed.contains(m_activeGame.shortName)) {
        m_statusBar->showMessage("Mod activation is paused while SMAPI is being changed. Try again when it finishes.", 5000);
        showProfile(m_appliedProfile);
        return;
    }
    m_requestedProfile = profileName;
    if (m_retargetRequestId || m_retargetStatusQueryId)
        return;
    if (profileName == m_appliedProfile) {
        const bool wasWaiting = m_waitingForSaves;
        m_waitingForSaves = false;
        m_profileSwitchTimer->stop();
        showProfile(profileName);
        updateProfileSwitchControls();
        if (wasWaiting)
            m_statusBar->clearMessage();
        return;
    }
    m_waitingForSaves = true;
    m_profileSwitchTimer->start(150);
    m_statusBar->showMessage(QString("Switching to profile \"%1\"…").arg(profileName));
    updateProfileSwitchControls();
}

void SessionController::onVfsRetargeted(quint64 requestId, const GrpcVFSStatus& status)
{
    if (!m_grpc->isConnected() || requestId != m_retargetRequestId || status.gameId != m_retargetGameId
        || status.gameId != m_activeGame.shortName)
        return;
    m_retargetRequestId = 0;
    m_retargetGameId.clear();
    m_vfsMounted = status.mounted;
    m_appliedProfile = status.mounted ? status.profileName : QString();
    updateVfsStatus(status);
    setVfsDirty(status.mounted && status.dirty);
    m_pluginList->refresh();
    if (!m_appliedProfile.isEmpty())
        m_config.setLastProfileFor(status.gameId, m_appliedProfile);
    if (!m_requestedProfile.isEmpty() && m_requestedProfile != m_appliedProfile) {
        m_waitingForSaves = true;
        m_statusBar->showMessage(QString("Switching to profile \"%1\"…").arg(m_requestedProfile));
        updateProfileSwitchControls();
        startProfileSwitch();
        return;
    }
    m_requestedProfile = m_appliedProfile;
    if (!m_appliedProfile.isEmpty())
        showProfile(m_appliedProfile);
    updateProfileSwitchControls();
    m_statusBar->showMessage(QString("Profile \"%1\" is now active.").arg(m_appliedProfile), 5000);
}

void SessionController::onVfsRetargetFailed(quint64 requestId, const QString& gameId,
                                            const QString& profileName, const QString& error)
{
    if (requestId != m_retargetRequestId || gameId != m_retargetGameId
        || gameId != m_activeGame.shortName)
        return;
    presentError(m_parentWindow, "Switch Profile", "switch profiles", error, true);
    if (requestId != m_retargetRequestId || gameId != m_activeGame.shortName)
        return;
    m_retargetRequestId = 0;
    m_retargetGameId.clear();
    if (m_requestedProfile == profileName)
        m_requestedProfile.clear();
    m_retargetStatusQueryId = m_grpc->queryVfsStatus(gameId);
    updateProfileSwitchControls();
    if (m_requestedProfile.isEmpty())
        m_statusBar->showMessage("Checking which profile is active…");
}

void SessionController::onRetargetStatusQueried(quint64 requestId, const GrpcVFSStatus& status)
{
    if (!m_grpc->isConnected() || requestId != m_retargetStatusQueryId || status.gameId != m_activeGame.shortName)
        return;
    m_retargetStatusQueryId = 0;
    m_vfsMounted = status.mounted;
    m_appliedProfile = status.mounted ? status.profileName : QString();
    if (status.mounted && !m_appliedProfile.isEmpty())
        showProfile(m_appliedProfile);
    m_pluginList->refresh();
    if (m_requestedProfile.isEmpty() || m_requestedProfile == m_appliedProfile || !status.mounted) {
        m_requestedProfile = m_appliedProfile;
        updateProfileSwitchControls();
        if (status.mounted && !m_appliedProfile.isEmpty())
            m_statusBar->showMessage(QString("Couldn't switch profiles. Showing the active profile \"%1\".")
                                         .arg(m_appliedProfile), 5000);
        else
            m_statusBar->showMessage("Couldn't switch profiles. Mods are inactive for this game.", 5000);
        return;
    }
    m_waitingForSaves = true;
    m_statusBar->showMessage(QString("Switching to profile \"%1\"…").arg(m_requestedProfile));
    updateProfileSwitchControls();
    startProfileSwitch();
}

void SessionController::onRetargetStatusQueryFailed(quint64 requestId, const QString& gameId, const QString&)
{
    if (requestId != m_retargetStatusQueryId || gameId != m_activeGame.shortName)
        return;
    m_retargetStatusQueryId = 0;
    m_requestedProfile = m_appliedProfile;
    if (!m_appliedProfile.isEmpty())
        showProfile(m_appliedProfile);
    updateProfileSwitchControls();
    m_statusBar->showMessage("Couldn't check which profile is active. Reconnect to check your mods.", 5000);
}

void SessionController::onVfsStatusChanged(const GrpcVFSStatus& status)
{
    if (!m_grpc->isConnected() || !m_activeGame.detected || status.gameId != m_activeGame.shortName)
        return;
    m_vfsMounted = status.mounted;
    m_appliedProfile = status.mounted ? status.profileName : QString();
    updateVfsStatus(status);
    setVfsDirty(status.mounted && status.dirty);
    if (!profileSwitchPending() && status.mounted && !m_appliedProfile.isEmpty()) {
        m_requestedProfile = m_appliedProfile;
        showProfile(m_appliedProfile);
    }
    m_pluginList->refresh();
}

void SessionController::onVfsStatusReceived(const GrpcVFSStatus& status)
{
    if (!m_grpc->isConnected() || !m_activeGame.detected || status.gameId != m_activeGame.shortName)
        return;
    const bool wasMounted = m_vfsMounted;
    m_vfsMounted = status.mounted;
    m_appliedProfile = status.mounted ? status.profileName : QString();
    updateVfsStatus(status);
    setVfsDirty(status.mounted && status.dirty);
    if (!profileSwitchPending() && status.mounted && !m_appliedProfile.isEmpty()) {
        m_requestedProfile = m_appliedProfile;
        showProfile(m_appliedProfile);
    }
    if (wasMounted != status.mounted)
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
    const auto previousState = m_lifecycleStates.value(gameId);
    const bool firstStatus = !m_hasModStatus;
    const bool wasBlocked = recoveryBlocked(gameId);
    m_lifecycleStates.insert(gameId, status.lifecycleState);
    m_steamMaintenance = status.steamMaintenance;
    m_hasSavedSteamFiles = !status.preservedBatches.empty();
    m_hasModStatus = true;
    if (recoveryBlocked(gameId))
        m_recoveryMountSkipped.insert(gameId);
    if (m_unmountAction)
        m_unmountAction->setEnabled(!profileSwitchPending() && !recoveryBlocked(gameId));
    refreshRecoveryIndicator();
    if (status.lifecycleState == GrpcVFSLifecycleState::RecoveryDeferred
        && (firstStatus || previousState != GrpcVFSLifecycleState::RecoveryDeferred))
        m_statusBar->showMessage("Mods were left active because the game may still be running.", 5000);
    if (status.mounted || status.lifecycleState != GrpcVFSLifecycleState::Ready
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
        m_applyButton->setEnabled(dirty && !profileSwitchPending() && !recoveryBlocked(m_activeGame.shortName));
    }
    if (dirty && !profileSwitchPending() && !recoveryBlocked(m_activeGame.shortName))
        m_statusBar->showMessage("Mod changes pending — click \"Apply Changes\" or just launch.", 4000);
    refreshStatusInfo();
}

void SessionController::onApplyChanges()
{
    if (!m_activeGame.detected || m_currentProfile.isEmpty() || !m_grpc->isConnected()
        || recoveryBlocked(m_activeGame.shortName) || profileSwitchPending())
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
        || recoveryBlocked(m_activeGame.shortName) || profileSwitchPending())
        return;
    const QString gameId = m_activeGame.shortName;
    const auto refusedForLoader = [this, &gameId] {
        if (!m_autoMountSuppressed.contains(gameId))
            return false;
        dialogs::plainInfo(m_parentWindow, "Deactivate Mods",
            "SMAPI is being changed for this game right now. Gorganizer will deactivate and activate "
            "its mods as part of that change. Try again when it finishes.");
        return true;
    };
    if (refusedForLoader())
        return;
    if (!dialogs::confirm(m_parentWindow, "Deactivate Mods",
            "Deactivate mods and restore the original game files? New files created while playing will be kept in Overwrite."))
        return;
    if (!m_activeGame.detected || m_activeGame.shortName != gameId || !m_grpc->isConnected()
        || recoveryBlocked(gameId) || profileSwitchPending())
        return;
    if (refusedForLoader())
        return;
    requestUnmount(gameId);
}

void SessionController::requestUnmount(const QString& gameId)
{
    m_grpc->unmountVfs(gameId);
    m_statusBar->showMessage("Deactivating mods…", 4000);
}

quint64 SessionController::unmountForMaintenance(const QString& gameId)
{
    if (gameId.isEmpty() || !m_grpc->isConnected())
        return 0;
    m_statusBar->showMessage("Deactivating mods…", 4000);
    return m_grpc->unmountVfsForMaintenance(gameId);
}

void SessionController::suppressAutoMount(const QString& gameId)
{
    if (gameId.isEmpty())
        return;
    m_autoMountSuppressed.insert(gameId);
    if (gameId == m_activeGame.shortName && m_waitingForSaves) {
        m_waitingForSaves = false;
        m_profileSwitchTimer->stop();
        m_requestedProfile = m_appliedProfile;
        if (!m_appliedProfile.isEmpty())
            showProfile(m_appliedProfile);
        updateProfileSwitchControls();
        m_statusBar->showMessage("Mod activation is paused while SMAPI is being changed. Try again when it finishes.", 5000);
    }
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
        m_statusBar->showMessage("Mod activation is paused while SMAPI is being changed. Try again when it finishes.", 5000);
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
        m_statusBar->showMessage("Gorganizer will activate the mods again when its background service reconnects.", 5000);
        return;
    }
    m_pendingRemounts.remove(gameId);
    m_grpc->mountVfsWithSwap(gameId, profileName);
    m_statusBar->showMessage("Activating mods again…", 4000);
}

void SessionController::onConnected()
{
    m_grpc->listGames();
    m_hasModStatus = false;
    m_steamMaintenance = GrpcSteamMaintenanceState::Unspecified;
    m_hasSavedSteamFiles = false;
    refreshRecoveryIndicator();
    const QHash<QString, QString> pending = m_pendingRemounts;
    for (auto it = pending.cbegin(); it != pending.cend(); ++it) {
        if (!m_activeGame.detected || it.key() != m_activeGame.shortName || m_autoMountSuppressed.contains(it.key())
            || m_lifecycleStates.value(it.key()) != GrpcVFSLifecycleState::Ready)
            continue;
        mountForMaintenance(it.key(), it.value());
    }
    if (m_activeGame.detected)
        m_autoMountQueryId = m_grpc->queryVfsStatus(m_activeGame.shortName);
}

void SessionController::autoMountActiveProfile()
{
    if (!m_activeGame.detected || !m_grpc->isConnected() || m_currentProfile.isEmpty()
        || profileSwitchPending() || m_vfsMounted)
        return;
    if (m_lifecycleStates.value(m_activeGame.shortName) != GrpcVFSLifecycleState::Ready) {
        m_recoveryMountSkipped.insert(m_activeGame.shortName);
        return;
    }
    m_recoveryMountSkipped.remove(m_activeGame.shortName);
    if (m_autoMountSuppressed.contains(m_activeGame.shortName)) {
        m_autoMountSkipped.insert(m_activeGame.shortName);
        m_statusBar->showMessage("Mod activation is paused while SMAPI is being changed. Try again when it finishes.", 5000);
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
            m_statusBar->showMessage("Mod activation is paused while SMAPI is being changed. Try again when it finishes.", 5000);
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
    const QString operation = method == QLatin1String("MountVFS") ? QStringLiteral("activate mods")
        : method == QLatin1String("UnmountVFS") ? QStringLiteral("deactivate mods")
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
    const bool deferred = m_hasModStatus && m_activeGame.detected
        && state == GrpcVFSLifecycleState::RecoveryDeferred;
    const bool pending = m_hasModStatus && m_activeGame.detected
        && state == GrpcVFSLifecycleState::RecoveryPending;
    m_recoveryButton->setVisible(deferred || pending);
    m_recoveryButton->setEnabled(m_grpc->isConnected() && (pending || m_retryGameId.isEmpty()));
    m_recoveryButton->setText(pending ? "Review…"
        : m_retryGameId == m_activeGame.shortName ? "Checking…" : "Check Again");
    refreshStatusInfo();
}

bool SessionController::eventFilter(QObject* watched, QEvent* event)
{
    if (watched == m_modStatusLabel && event->type() == QEvent::MouseButtonRelease
        && m_grpc->isConnected() && m_hasModStatus && m_activeGame.detected
        && (m_steamMaintenance == GrpcSteamMaintenanceState::VerifyRequired
            || m_steamMaintenance == GrpcSteamMaintenanceState::UserRequested
            || m_steamMaintenance == GrpcSteamMaintenanceState::SteamBusy
            || m_hasSavedSteamFiles)
        && static_cast<QMouseEvent*>(event)->button() == Qt::LeftButton) {
        emit steamHelpRequested();
        return true;
    }
    return QObject::eventFilter(watched, event);
}

void SessionController::refreshStatusInfo()
{
    m_statusInfo->setText(m_activeGame.detected
        ? QString("%1 - %2").arg(m_activeGame.name, m_currentProfile)
        : QStringLiteral("No game selected"));

    QString text;
    QString tip;
    const auto state = m_lifecycleStates.value(m_activeGame.shortName);
    if (!m_grpc->isConnected()) {
        text = QStringLiteral("Connection lost");
        tip = QStringLiteral("Gorganizer's background service is disconnected, so mod status is unknown.");
    } else if (!m_hasModStatus || !m_activeGame.detected) {
        text = QStringLiteral("Checking mod status…");
        tip = QStringLiteral("Gorganizer is checking whether mods are active for this game.");
    } else if (state == GrpcVFSLifecycleState::RecoveryPending) {
        text = QStringLiteral("Needs your decision");
        tip = QStringLiteral("Gorganizer needs your decision to finish an interrupted mod change.");
    } else if (state == GrpcVFSLifecycleState::RecoveryDeferred) {
        text = QStringLiteral("Waiting for the game to close");
        tip = QStringLiteral("Mods were left active because the game may still be running.");
    } else if (m_steamMaintenance == GrpcSteamMaintenanceState::VerifyRequired && m_vfsMounted) {
        text = QStringLiteral("Steam changed the game");
        tip = QStringLiteral("Steam changed game files while mods were active. Open the Steam help to pause mods safely.");
    } else if (m_steamMaintenance == GrpcSteamMaintenanceState::VerifyRequired
               || m_steamMaintenance == GrpcSteamMaintenanceState::UserRequested) {
        text = QStringLiteral("Paused for Steam");
        tip = QStringLiteral("Mods are paused until you verify the game or finish updating it in Steam.");
    } else if (!m_vfsMounted) {
        text = QStringLiteral("Mods inactive");
        tip = QStringLiteral("Mods are not active for this game.");
    } else if (m_vfsDirty) {
        text = QStringLiteral("Changes pending");
        tip = QStringLiteral("Mod changes will be applied when you choose Apply Changes or launch the game.");
    } else {
        text = QStringLiteral("Mods active");
        tip = QStringLiteral("Mods are active for this game.");
    }
    m_modStatusLabel->setText(text);
    m_modStatusLabel->setAccessibleName(text);
    const bool steamHelp = m_grpc->isConnected() && m_hasModStatus && m_activeGame.detected
        && (m_steamMaintenance == GrpcSteamMaintenanceState::VerifyRequired
            || m_steamMaintenance == GrpcSteamMaintenanceState::UserRequested
            || m_steamMaintenance == GrpcSteamMaintenanceState::SteamBusy
            || m_hasSavedSteamFiles);
    const bool verifyMounted = m_steamMaintenance == GrpcSteamMaintenanceState::VerifyRequired && m_vfsMounted;
    m_modStatusLabel->setToolTip(steamHelp && !verifyMounted ? tip + (m_hasSavedSteamFiles
        ? "\nClick for Steam update help and saved files." : "\nClick for Steam update help.") : tip);
    m_modStatusLabel->setCursor(steamHelp ? Qt::PointingHandCursor : Qt::ArrowCursor);
}

}

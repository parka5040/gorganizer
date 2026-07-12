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

#include <QDir>
#include <QLabel>
#include <QSet>
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
    , m_statusInfo(statusInfo)
    , m_statusBar(statusBar)
    , m_parentWindow(parentWindow)
{
    connect(m_grpc, &GrpcClient::gamesDetected, this, &SessionController::onGamesDetected);
    connect(m_grpc, &GrpcClient::vfsStatusChanged, this, &SessionController::onVfsStatusChanged);
    connect(m_grpc, &GrpcClient::rpcError, this, &SessionController::onRpcError);
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
    m_activeGame = found.value_or(GameInfo{});
    m_config.setActiveGameShortName(m_activeGame.shortName);

    if (m_grpc->isConnected())
        m_grpc->setActiveGame(m_activeGame.detected ? m_activeGame.shortName : QString());

    m_runButton->setGame(m_activeGame, m_config.lastToolFor(m_activeGame.shortName));
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

        if (m_grpc->isConnected() && !m_currentProfile.isEmpty())
            m_grpc->mountVfsWithSwap(m_activeGame.shortName, m_currentProfile);
    }

    refreshStatusInfo();
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
    if (!m_activeGame.detected) return;
    if (status.gameId != m_activeGame.shortName) return;
    m_vfsMounted = status.mounted;
    setVfsDirty(status.dirty);
    m_pluginList->refresh();
    emit vfsStateChanged(m_vfsMounted, m_vfsDirty);
}

void SessionController::setVfsDirty(bool dirty)
{
    m_vfsDirty = dirty;
    if (m_applyButton) {
        m_applyButton->setVisible(dirty);
        m_applyButton->setEnabled(dirty);
    }
    if (dirty)
        m_statusBar->showMessage("Mod changes pending — click \"Apply Changes\" or just launch.", 4000);
}

void SessionController::onApplyChanges()
{
    if (!m_activeGame.detected || m_currentProfile.isEmpty() || !m_grpc->isConnected())
        return;
    m_applyButton->setEnabled(false);
    m_statusBar->showMessage("Applying mod changes…");
    if (m_vfsMounted)
        m_grpc->rebuildVfs(m_activeGame.shortName);
    else
        m_grpc->mountVfsWithSwap(m_activeGame.shortName, m_currentProfile);
}

void SessionController::onUnmountMods()
{
    if (!m_activeGame.detected || !m_grpc->isConnected())
        return;
    if (!dialogs::confirm(m_parentWindow, "Unmount mods",
            "Restore the game's vanilla Data folder?\n\nAny new writes (saves, tool output) "
            "are captured into Overwrite first. Do this when you've finished playing."))
        return;
    m_grpc->unmountVfs(m_activeGame.shortName);
    m_statusBar->showMessage("Unmounting mods…", 4000);
}

void SessionController::onRpcError(const QString& method, const QString& error)
{
    if (method == "SetModList") {
        if (m_activeGame.detected)
            m_modList->loadForGame(m_activeGame, m_currentProfile);
        dialogs::warn(m_parentWindow, "Change not saved",
            QString("A mod-list change could not be saved and was reverted:\n\n%1").arg(error));
        return;
    }
    m_statusBar->showMessage(QString("Error (%1): %2").arg(method, error), 5000);
}

void SessionController::refreshStatusInfo()
{
    if (m_activeGame.detected) {
        m_statusInfo->setText(QString("%1 - %2").arg(m_activeGame.name, m_currentProfile));
    } else {
        m_statusInfo->setText("No game selected");
    }
}

}

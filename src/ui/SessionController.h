#pragma once

#include <QObject>
#include <QString>
#include "AppConfig.h"
#include "GameInfo.h"
#include "GrpcTypes.h"
#include <vector>

class QLabel;
class QStatusBar;
class QToolButton;
class QWidget;

namespace gorganizer {

class GrpcClient;
class GameSelectorWidget;
class ProfileSelectorWidget;
class ModListWidget;
class PluginListWidget;
class DownloadsLibraryView;
class RunButtonWidget;

class SessionController : public QObject {
    Q_OBJECT
public:
    SessionController(AppConfig& config, GrpcClient* grpc,
                      GameSelectorWidget* gameSelector,
                      ProfileSelectorWidget* profileSelector,
                      ModListWidget* modList,
                      PluginListWidget* pluginList,
                      DownloadsLibraryView* downloadsLibrary,
                      RunButtonWidget* runButton,
                      QToolButton* applyButton,
                      QLabel* statusInfo,
                      QStatusBar* statusBar,
                      QWidget* parentWindow);

    const GameInfo& activeGame() const { return m_activeGame; }
    QString currentProfile() const { return m_currentProfile; }
    bool vfsMounted() const { return m_vfsMounted; }
    bool vfsDirty() const { return m_vfsDirty; }

    // Seeds the game selector from locally detected games and restores the persisted active game.
    void loadManagedGames();

    // Refreshes the permanent "<game> - <profile>" status-bar label.
    void refreshStatusInfo();

public slots:
    // Switches the active game; synthetic appId==0 games (TTW) fall back to the selector's current entry.
    void switchToGame(uint32_t appId);
    // Applies a profile switch: persists the per-game last profile and reloads mod/plugin views.
    void onProfileChanged(const QString& profileName);
    // U-2: rebuilds the on-disk farm for pending changes, or mount/swaps when not yet mounted.
    void onApplyChanges();
    // C4/C5 (I-23): captures writes into Overwrite and restores vanilla Data/ when the user stops playing.
    void onUnmountMods();

signals:
    void activeGameChanged(const GameInfo& game);
    void profileChanged(const QString& profileName);
    void vfsStateChanged(bool mounted, bool dirty);

private:
    // Rebuilds the managed-game list from a daemon detection pass (authoritative over local detection).
    void onGamesDetected(const std::vector<GrpcGame>& detectedGames);
    // Tracks daemon VFS state for the active game and surfaces the Apply affordance.
    void onVfsStatusChanged(const GrpcVFSStatus& status);
    // U-4: reverts an optimistic SetModList failure to authoritative state with a loud dialog.
    void onRpcError(const QString& method, const QString& error);
    // Shows/enables the Apply button while the daemon reports the VFS dirty (U-2).
    void setVfsDirty(bool dirty);

    AppConfig& m_config;
    GrpcClient* m_grpc;
    GameSelectorWidget* m_gameSelector;
    ProfileSelectorWidget* m_profileSelector;
    ModListWidget* m_modList;
    PluginListWidget* m_pluginList;
    DownloadsLibraryView* m_downloadsLibrary;
    RunButtonWidget* m_runButton;
    QToolButton* m_applyButton;
    QLabel* m_statusInfo;
    QStatusBar* m_statusBar;
    QWidget* m_parentWindow;

    std::vector<GameInfo> m_managedGames;
    GameInfo m_activeGame;
    QString m_currentProfile = "Default";
    bool m_vfsDirty = false;
    bool m_vfsMounted = false;
};

}

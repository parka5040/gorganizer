#pragma once

#include <QHash>
#include <QObject>
#include <QSet>
#include <QString>
#include "AppConfig.h"
#include "GameInfo.h"
#include "GrpcTypes.h"
#include <vector>

class QAction;
class QLabel;
class QPushButton;
class QStatusBar;
class QToolButton;
class QTimer;
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
                      QAction* unmountAction,
                      QLabel* statusInfo,
                      QStatusBar* statusBar,
                      QWidget* parentWindow);

    const GameInfo& activeGame() const { return m_activeGame; }
    QString currentProfile() const { return m_currentProfile; }
    bool vfsMounted() const { return m_vfsMounted; }
    bool vfsDirty() const { return m_vfsDirty; }
    bool profileSwitchPending() const { return m_waitingForSaves || m_retargetRequestId || m_retargetStatusQueryId; }

    // Seeds the game selector from locally detected games and restores the persisted active game.
    void loadManagedGames();

    // Refreshes the permanent "<game> - <profile>" status-bar label.
    void refreshStatusInfo();

    // Unmounts gameId without asking, for maintenance flows that already confirmed with the user, and returns the request id maintenanceUnmountFinished carries, or 0 when nothing was sent.
    quint64 unmountForMaintenance(const QString& gameId);
    // Blocks automatic mounting, Apply and user unmounts of gameId until finishMaintenance lifts the block.
    void suppressAutoMount(const QString& gameId);
    // Lifts the suppression of gameId after a maintenance flow and mounts remountProfile when given, else replays a skipped automatic mount when replaySkipped is set.
    void finishMaintenance(const QString& gameId, const QString& remountProfile, bool replaySkipped);
    // Mounts profileName of gameId at the user's request after a maintenance flow, or defers it while a SMAPI operation suppresses automatic mounting.
    void remountAfterMaintenance(const QString& gameId, const QString& profileName);

public slots:
    // Switches the active game; synthetic appId==0 games (TTW) fall back to the selector's current entry.
    void switchToGame(uint32_t appId);
    // Selects a profile and switches the deployed mods when the game is mounted.
    void onProfileChanged(const QString& profileName);
    // U-2: rebuilds the on-disk farm for pending changes, or mount/swaps when not yet mounted.
    void onApplyChanges();
    // C4/C5 (I-23): captures writes into Overwrite and restores vanilla Data/ when the user stops playing.
    void onUnmountMods();

signals:
    void activeGameChanged(const GameInfo& game);
    void profileChanged(const QString& profileName);
    void recoveryReviewRequested(const QString& gameId);
    void profileSwitchActivityChanged();

private:
    // Rebuilds the managed-game list from a daemon detection pass (authoritative over local detection).
    void onGamesDetected(const std::vector<GrpcGame>& detectedGames);
    // Tracks daemon VFS state for the active game and surfaces the Apply affordance.
    void onVfsStatusChanged(const GrpcVFSStatus& status);
    // Tracks the mount state and pending changes of the active game from a polled VFS status, ignoring other games.
    void onVfsStatusReceived(const GrpcVFSStatus& status);
    // Reverts a failed SetModList loudly (U-4), warns when Apply is refused because the game runs, and shows other RPC errors as readable status text.
    void onRpcError(const QString& method, const QString& error);
    // Shows or disables Apply according to pending changes and recovery state.
    void setVfsDirty(bool dirty);
    // Updates the active game's recovery controls from a daemon status.
    void updateVfsStatus(const GrpcVFSStatus& status);
    // Reports whether recovery blocks changes to the named game.
    bool recoveryBlocked(const QString& gameId) const;
    // Mounts the active game's current profile unless automatic mounting is suppressed or recovery is blocked.
    void autoMountActiveProfile();
    // Rechecks deferred recovery or requests a review of pending recovery for the active game.
    void onRecoveryAction();
    // Updates the active game's mod status and recovery action.
    void refreshRecoveryIndicator();
    // Sends the unmount RPC for gameId and reports it in the status bar.
    void requestUnmount(const QString& gameId);
    // Mounts profileName of gameId with auto-swap and reports it in the status bar, or keeps it pending until the daemon reconnects.
    void mountForMaintenance(const QString& gameId, const QString& profileName);
    // Sends the maintenance remount that waited for the daemon when its game is still active and not suppressed.
    void onConnected();
    void showProfile(const QString& profileName);
    void updateProfileSwitchControls();
    void startProfileSwitch();
    void onVfsRetargeted(quint64 requestId, const GrpcVFSStatus& status);
    void onVfsRetargetFailed(quint64 requestId, const QString& gameId, const QString& profileName,
                             const QString& error);
    void onRetargetStatusQueried(quint64 requestId, const GrpcVFSStatus& status);
    void onRetargetStatusQueryFailed(quint64 requestId, const QString& gameId, const QString& error);

    AppConfig& m_config;
    GrpcClient* m_grpc;
    GameSelectorWidget* m_gameSelector;
    ProfileSelectorWidget* m_profileSelector;
    ModListWidget* m_modList;
    PluginListWidget* m_pluginList;
    DownloadsLibraryView* m_downloadsLibrary;
    RunButtonWidget* m_runButton;
    QToolButton* m_applyButton;
    QAction* m_unmountAction;
    QLabel* m_statusInfo;
    QLabel* m_modStatusLabel;
    QPushButton* m_recoveryButton;
    QTimer* m_profileSwitchTimer;
    QStatusBar* m_statusBar;
    QWidget* m_parentWindow;

    std::vector<GameInfo> m_managedGames;
    GameInfo m_activeGame;
    QString m_currentProfile = "Default";
    QString m_requestedProfile;
    QString m_appliedProfile;
    QString m_retargetGameId;
    quint64 m_retargetRequestId = 0;
    quint64 m_retargetStatusQueryId = 0;
    bool m_waitingForSaves = false;
    bool m_vfsDirty = false;
    bool m_vfsMounted = false;
    bool m_hasModStatus = false;
    GrpcSteamMaintenanceState m_steamMaintenance = GrpcSteamMaintenanceState::Unspecified;
    QHash<QString, GrpcVFSLifecycleState> m_lifecycleStates;
    quint64 m_autoMountQueryId = 0;
    QString m_retryGameId;
    QSet<QString> m_recoveryMountSkipped;
    QSet<QString> m_autoMountSuppressed;
    QSet<QString> m_autoMountSkipped;
    QHash<QString, QString> m_pendingRemounts;
};

}

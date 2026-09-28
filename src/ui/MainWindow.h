#pragma once

#include <QMainWindow>
#include <QTabWidget>
#include <QLabel>
#include <QActionGroup>
#include <optional>
#include "AppConfig.h"
#include "GrpcTypes.h"

class QCloseEvent;
class QDragEnterEvent;
class QDragMoveEvent;
class QDropEvent;
class QToolButton;

namespace gorganizer {

class GrpcClient;
class InstallController;
class GameSelectorWidget;
class ModListWidget;
class PluginListWidget;
class DownloadsLibraryView;
class RunButtonWidget;
class ProfileSelectorWidget;
class ConnectionIndicator;
class ActivityLogPanel;
class SessionController;
class LaunchController;
class FalloutPatchController;
class GameSetupController;
class ModLoaderController;
class ModDependencyController;
class SteamMaintenanceController;
class SmapiModsWidget;
struct ArchiveDrop;

class MainWindow : public QMainWindow {
    Q_OBJECT
public:
    explicit MainWindow(AppConfig& config, GrpcClient* grpc, QWidget* parent = nullptr);

    // Records whether this gorganizer started the daemon, which then stops when gorganizer quits.
    void setDaemonOwned(bool owned) { m_daemonOwned = owned; }

protected:
    // Asks before closing while a SMAPI operation or an asynchronous mod install that quitting could interrupt is still running.
    void closeEvent(QCloseEvent* event) override;
    void dragEnterEvent(QDragEnterEvent* event) override;
    void dragMoveEvent(QDragMoveEvent* event) override;
    void dropEvent(QDropEvent* event) override;

private slots:
    void onInstallMod();
    void onOpenSettings();
    void onOpenIniEditor();
    void onOpenExecutables();
    void onExportMods();
    void onImportMods();
    // Clears the matching pending install and rescans the mod list once no menu or dialog opened from it is running.
    void onInstallRequestCompleted(quint64 requestId, const QString& modFolder, int fileCount);
    // Resolves the matching pending install's failure, or reports any other asynchronous install failure.
    void onInstallRequestFailed(quint64 requestId, const QString& error);
    void onInstallCancelled(quint64 requestId);
    void onInstallUnknown(quint64 requestId);

private:
    enum class ArchiveInstallResult { Started, Succeeded, Failed, Unknown };

    struct DropQueue {
        GameInfo game;
        QStringList remaining;
    };

    struct PendingExternalInstall {
        quint64 requestId = 0;
        QString gameId;
        QString path;
        QString name;
        GrpcInstallMode mode = GrpcInstallAsNewMod;
    };

    void setupUi();
    void createControllers();
    void wireConnections();
    void updateTransferActionsEnabled();
    void refreshAfterImport();
    // Shows or hides game-specific tabs and actions from the active game's daemon capabilities.
    void applyGameCapabilities(const GameInfo& game);
    bool canInstallArchive(const GameInfo& game);
    ArchiveInstallResult installArchiveFromPath(const QString& path, const GameInfo& game);
    void handleArchiveDrop(const ArchiveDrop& drop);
    void startNextDroppedArchive();
    void finishDroppedArchive(bool succeeded);
    // Installs an archive for a manifest-layout game through the daemon, asking only for the mod name.
    bool installThroughDaemonLayout(const QString& gameId, const QString& path);
    // Asks for a mod name until it passes local validation, returning an empty string on cancel.
    QString askModName(const QString& title, const QString& label, const QString& initial);
    // Issues an asynchronous external-archive install and records it as pending under its request id.
    void startExternalInstall(const PendingExternalInstall& request);
    // Reports or resolves a failed pending external install.
    void onExternalInstallFailed(const PendingExternalInstall& request, const QString& error);

    AppConfig& m_config;
    GrpcClient* m_grpc;
    InstallController* m_installs;
    GameSelectorWidget* m_gameSelector = nullptr;
    ModListWidget* m_modList = nullptr;
    PluginListWidget* m_pluginList = nullptr;
    DownloadsLibraryView* m_downloadsLibrary = nullptr;
    ActivityLogPanel* m_activityLog = nullptr;
    QTabWidget* m_rightTabs = nullptr;
    QWidget* m_dataPlaceholder = nullptr;
    SmapiModsWidget* m_smapiMods = nullptr;
    RunButtonWidget* m_runButton = nullptr;
    ProfileSelectorWidget* m_profileSelector = nullptr;
    ConnectionIndicator* m_connectionIndicator = nullptr;
    QLabel* m_statusInfo = nullptr;
    QToolButton* m_cancelInstallButton = nullptr;
    QToolButton* m_applyButton = nullptr;

    SessionController* m_session = nullptr;
    LaunchController* m_launch = nullptr;
    FalloutPatchController* m_falloutPatch = nullptr;
    GameSetupController* m_gameSetup = nullptr;
    ModLoaderController* m_modLoader = nullptr;
    ModDependencyController* m_modDependencies = nullptr;
    SteamMaintenanceController* m_steamMaintenance = nullptr;

    QActionGroup* m_themeActions = nullptr;
    QActionGroup* m_appearanceActions = nullptr;
    QAction* m_addGameAction = nullptr;
    QAction* m_locateGameAction = nullptr;
    QAction* m_exportAction = nullptr;
    QAction* m_importAction = nullptr;
    QAction* m_unmountAction = nullptr;
    QAction* m_steamHelpAction = nullptr;
    QAction* m_pauseForSteamAction = nullptr;
    QAction* m_patch4GBAction = nullptr;
    QAction* m_installTtwAction = nullptr;
    QAction* m_iniEditorAction = nullptr;
    QMenu* m_smapiMenu = nullptr;

    std::optional<PendingExternalInstall> m_pendingExternalInstall;
    std::optional<DropQueue> m_dropQueue;
    bool m_restorePluginsTab = false;
    bool m_daemonOwned = false;
};

}

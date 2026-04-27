#pragma once

#include <QMainWindow>
#include <QTabWidget>
#include <QLabel>
#include <QActionGroup>
#include "AppConfig.h"
#include "GameInfo.h"
#include <vector>

namespace gorganizer {

class GrpcClient;
class GameSelectorWidget;
class ModListWidget;
class PluginListWidget;
class DownloadsLibraryView;
class RunButtonWidget;
class ProfileSelectorWidget;
class ConnectionIndicator;
class ActivityLogPanel;

class MainWindow : public QMainWindow {
    Q_OBJECT
public:
    explicit MainWindow(AppConfig& config, GrpcClient* grpc, QWidget* parent = nullptr);

private slots:
    void onGameChanged(uint32_t appId);
    void onRunGame();
    void onProfileChanged(const QString& profileName);
    void onGameLaunched(int pid);
    void onGameLaunchFailed(const QString& error);
    void onRpcError(const QString& method, const QString& error);
    void onInstallMod();
    void onOpenSettings();
    void onOpenIniEditor();
    void onAddNewGame();
    void onPatchFalloutTo4GB();

private:
    void setupUi();
    void loadManagedGames();
    void updateStatusBarInfo();

    AppConfig& m_config;
    GrpcClient* m_grpc;
    GameSelectorWidget* m_gameSelector = nullptr;
    ModListWidget* m_modList = nullptr;
    PluginListWidget* m_pluginList = nullptr;
    DownloadsLibraryView* m_downloadsLibrary = nullptr;
    ActivityLogPanel* m_activityLog = nullptr;
    QTabWidget* m_rightTabs = nullptr;
    RunButtonWidget* m_runButton = nullptr;
    ProfileSelectorWidget* m_profileSelector = nullptr;
    ConnectionIndicator* m_connectionIndicator = nullptr;
    QLabel* m_statusInfo = nullptr;

    std::vector<GameInfo> m_managedGames;
    GameInfo m_activeGame;
    QString m_currentProfile = "Default";
    QActionGroup* m_themeActions = nullptr;     // legacy: dark variant choice
    QActionGroup* m_appearanceActions = nullptr; // System / Light / Dark
    QAction* m_patch4GBAction = nullptr;         // Tools → Patch Fallout to 4GB
};

} // namespace gorganizer

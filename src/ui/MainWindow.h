#pragma once

#include <QMainWindow>
#include <QTabWidget>
#include <QLabel>
#include <QActionGroup>
#include "AppConfig.h"

class QToolButton;

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
class SessionController;
class LaunchController;
class FalloutPatchController;
class GameSetupController;

class MainWindow : public QMainWindow {
    Q_OBJECT
public:
    explicit MainWindow(AppConfig& config, GrpcClient* grpc, QWidget* parent = nullptr);

private slots:
    void onInstallMod();
    void onOpenSettings();
    void onOpenIniEditor();
    void onOpenExecutables();
    void onExportMods();
    void onImportMods();

private:
    void setupUi();
    void createControllers();
    void wireConnections();
    void updateTransferActionsEnabled();
    void refreshAfterImport();

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
    QToolButton* m_applyButton = nullptr;

    SessionController* m_session = nullptr;
    LaunchController* m_launch = nullptr;
    FalloutPatchController* m_falloutPatch = nullptr;
    GameSetupController* m_gameSetup = nullptr;

    QActionGroup* m_themeActions = nullptr;
    QActionGroup* m_appearanceActions = nullptr;
    QAction* m_addGameAction = nullptr;
    QAction* m_exportAction = nullptr;
    QAction* m_importAction = nullptr;
    QAction* m_unmountAction = nullptr;
    QAction* m_patch4GBAction = nullptr;
    QAction* m_installTtwAction = nullptr;
};

}

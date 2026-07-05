add_library(gorganizer_ui STATIC
    MainWindow.cpp
    SetupWizard.cpp
    GameSelectorWidget.cpp
    ModListWidget.cpp
    PluginListWidget.cpp
    RunButtonWidget.cpp
    ProfileSelectorWidget.cpp
    ConnectionIndicator.cpp
    DownloadsLibraryView.cpp
    DownloadsModel.cpp
    DownloadsRowDelegate.cpp
    SettingsDialog.cpp
    SplashScreen.cpp
    ActivityLogPanel.cpp
    ModInstallDialog.cpp
    InstallWorker.cpp
    FomodPlan.cpp
    FomodInstallerDialog.cpp
    IniEditorDialog.cpp
    TTWInstallDialog.cpp
    ExecutablesDialog.cpp
    ThemeManager.cpp
    ../../resources/themes.qrc
)
target_include_directories(gorganizer_ui PUBLIC ${CMAKE_CURRENT_SOURCE_DIR})
target_link_libraries(gorganizer_ui PUBLIC
    gorganizer_core
    gorganizer_grpc
    gorganizer_util
    Qt6::Core
    Qt6::Gui
    Qt6::Widgets
    Qt6::Network
)

#include "MainWindow.h"
#include "GrpcClient.h"
#include "GameSelectorWidget.h"
#include "ModListWidget.h"
#include "PluginListWidget.h"
#include "RunButtonWidget.h"
#include "ProfileSelectorWidget.h"
#include "ConnectionIndicator.h"
#include "DownloadsLibraryView.h"
#include "ActivityLogPanel.h"
#include "SessionController.h"
#include "LaunchController.h"
#include "FalloutPatchController.h"
#include "GameSetupController.h"
#include "SettingsDialog.h"
#include "ModInstallDialog.h"
#include "IniEditorDialog.h"
#include "ExecutablesDialog.h"
#include "ExportDialog.h"
#include "ImportDialog.h"
#include "ThemeManager.h"
#include "Dialogs.h"

#include <QToolBar>
#include <QToolButton>
#include <QSplitter>
#include <QStatusBar>
#include <QMenuBar>
#include <QFileDialog>
#include <QVBoxLayout>
#include <QActionGroup>

namespace gorganizer {

MainWindow::MainWindow(AppConfig& config, GrpcClient* grpc, QWidget* parent)
    : QMainWindow(parent)
    , m_config(config)
    , m_grpc(grpc)
{
    setWindowTitle("Gorganizer");
    setMinimumSize(900, 600);
    resize(1200, 750);

    setupUi();
    createControllers();
    wireConnections();

    m_session->loadManagedGames();

    if (m_grpc->isConnected()) {
        statusBar()->showMessage("Daemon connected", 3000);
        m_grpc->detectGames();
        m_grpc->startWatching();
    }
}

// Builds menus, toolbar, splitter layout, and status bar; controller wiring happens in wireConnections.
void MainWindow::setupUi()
{
    auto* fileMenu = menuBar()->addMenu("&File");
    fileMenu->addAction("&Install Mod...", this, &MainWindow::onInstallMod);
    fileMenu->addSeparator();
    m_addGameAction = fileMenu->addAction("&Add New Game...");
    fileMenu->addSeparator();
    m_exportAction = fileMenu->addAction("&Export Mods...");
    m_exportAction->setEnabled(false);
    m_importAction = fileMenu->addAction("I&mport Mods...");
    m_importAction->setEnabled(false);
    fileMenu->addSeparator();
    fileMenu->addAction("&Quit", this, &QWidget::close);

    auto* viewMenu = menuBar()->addMenu("&View");

    auto* appearanceMenu = viewMenu->addMenu("&Appearance");
    m_appearanceActions = new QActionGroup(this);
    m_appearanceActions->setExclusive(true);
    const QString currentMode = m_config.appearanceMode();
    const QStringList modes = ThemeManager::availableModes();
    for (const auto& label : modes) {
        auto* action = appearanceMenu->addAction(label);
        action->setCheckable(true);
        const QString modeKey = label.toLower();
        action->setChecked(modeKey == currentMode);
        m_appearanceActions->addAction(action);
        connect(action, &QAction::triggered, this, [this, modeKey] {
            m_config.setAppearanceMode(modeKey);
            ThemeManager::applyMode(modeKey, m_config.preferredStyle());
        });
    }

    auto* darkMenu = viewMenu->addMenu("&Theme");
    m_themeActions = new QActionGroup(this);
    m_themeActions->setExclusive(true);
    QString currentVariant = ThemeManager::canonicalThemeName(m_config.preferredStyle());
    for (const auto& name : ThemeManager::availableThemes()) {
        auto* action = darkMenu->addAction(name);
        action->setCheckable(true);
        action->setChecked(name == currentVariant);
        m_themeActions->addAction(action);
        connect(action, &QAction::triggered, this, [this, name] {
            m_config.setPreferredStyle(name);
            ThemeManager::applyMode(m_config.appearanceMode(), name);
        });
    }

    auto* toolsMenu = menuBar()->addMenu("&Tools");
    toolsMenu->addAction("INI &Editor...", this, &MainWindow::onOpenIniEditor);
    toolsMenu->addAction("E&xternal Tools...", this, &MainWindow::onOpenExecutables);
    m_unmountAction = toolsMenu->addAction("&Unmount Mods (restore vanilla Data)");
    m_patch4GBAction = toolsMenu->addAction("Patch Fallout to &4GB");
    m_patch4GBAction->setVisible(false);
    m_installTtwAction = toolsMenu->addAction("Install Tale of Two &Wastelands...");
    m_installTtwAction->setVisible(false);
    toolsMenu->addSeparator();
    toolsMenu->addAction("&Settings...", this, &MainWindow::onOpenSettings);

    auto* toolbar = addToolBar("Main");
    toolbar->setMovable(false);
    toolbar->setFloatable(false);
    toolbar->setIconSize(QSize(24, 24));

    toolbar->addWidget(new QLabel(" Game: "));
    m_gameSelector = new GameSelectorWidget;
    m_gameSelector->setMinimumWidth(200);
    toolbar->addWidget(m_gameSelector);

    toolbar->addSeparator();

    m_profileSelector = new ProfileSelectorWidget(m_grpc);
    toolbar->addWidget(m_profileSelector);

    toolbar->addSeparator();

    auto* installBtn = new QToolButton;
    installBtn->setText("Install Mod...");
    installBtn->setToolButtonStyle(Qt::ToolButtonTextOnly);
    connect(installBtn, &QToolButton::clicked, this, &MainWindow::onInstallMod);
    toolbar->addWidget(installBtn);

    m_applyButton = new QToolButton;
    m_applyButton->setText("Apply Changes");
    m_applyButton->setToolButtonStyle(Qt::ToolButtonTextOnly);
    m_applyButton->setToolTip("Rebuild the game's mod view to match your pending changes.\n"
                              "Launching the game applies them automatically.");
    m_applyButton->setStyleSheet("QToolButton { font-weight: bold; }");
    m_applyButton->setVisible(false);
    toolbar->addWidget(m_applyButton);

    auto* spacer = new QWidget;
    spacer->setSizePolicy(QSizePolicy::Expanding, QSizePolicy::Preferred);
    toolbar->addWidget(spacer);

    m_runButton = new RunButtonWidget;
    toolbar->addWidget(m_runButton);

    auto* central = new QWidget;
    auto* centralLayout = new QVBoxLayout(central);
    centralLayout->setContentsMargins(0, 0, 0, 0);
    centralLayout->setSpacing(0);

    auto* vsplit = new QSplitter(Qt::Vertical);

    auto* splitter = new QSplitter(Qt::Horizontal);

    m_modList = new ModListWidget(m_grpc);
    m_modList->applyCollapsedSeparatorView(m_config.collapsedSeparatorView());
    splitter->addWidget(m_modList);

    m_rightTabs = new QTabWidget;
    m_pluginList = new PluginListWidget;
    m_pluginList->setGrpcClient(m_grpc);
    m_rightTabs->addTab(m_pluginList, "Plugins");

    auto* dataPlaceholder = new QWidget;
    auto* dpLayout = new QVBoxLayout(dataPlaceholder);
    auto* dpLabel = new QLabel("Virtual data directory.\nShows the merged view of all enabled mods.\n\n(Coming soon)");
    dpLabel->setAlignment(Qt::AlignCenter);
    dpLabel->setObjectName("hintLabel");
    dpLayout->addWidget(dpLabel);
    m_rightTabs->addTab(dataPlaceholder, "Data");

    m_downloadsLibrary = new DownloadsLibraryView(m_grpc);
    m_rightTabs->addTab(m_downloadsLibrary, "Downloads");
    splitter->addWidget(m_rightTabs);

    splitter->setStretchFactor(0, 2);
    splitter->setStretchFactor(1, 1);
    splitter->setSizes({800, 400});

    vsplit->addWidget(splitter);
    m_activityLog = new ActivityLogPanel(m_grpc);
    vsplit->addWidget(m_activityLog);
    vsplit->setStretchFactor(0, 4);
    vsplit->setStretchFactor(1, 1);
    vsplit->setSizes({600, 150});

    centralLayout->addWidget(vsplit, 1);
    setCentralWidget(central);

    m_statusInfo = new QLabel;
    statusBar()->addWidget(m_statusInfo, 1);

    m_connectionIndicator = new ConnectionIndicator(m_grpc);
    statusBar()->addPermanentWidget(m_connectionIndicator);
}

// Instantiates the workflow controllers with the widgets and daemon client they drive.
void MainWindow::createControllers()
{
    m_session = new SessionController(m_config, m_grpc, m_gameSelector, m_profileSelector,
                                      m_modList, m_pluginList, m_downloadsLibrary, m_runButton,
                                      m_applyButton, m_statusInfo, statusBar(), this);
    m_launch = new LaunchController(m_config, m_grpc, m_session, m_runButton, statusBar(), this);
    m_falloutPatch = new FalloutPatchController(m_grpc, m_session, m_runButton, m_patch4GBAction,
                                                statusBar(), this);
    m_gameSetup = new GameSetupController(m_config, m_grpc, m_session, m_modList,
                                          m_installTtwAction, statusBar(), this);
}

// Wires widget signals to controllers, controller cross-links, and window-level daemon status handling.
void MainWindow::wireConnections()
{
    connect(m_gameSelector, &GameSelectorWidget::gameChanged,
            m_session, &SessionController::switchToGame);
    connect(m_profileSelector, &ProfileSelectorWidget::profileChanged,
            m_session, &SessionController::onProfileChanged);
    connect(m_applyButton, &QToolButton::clicked,
            m_session, &SessionController::onApplyChanges);
    connect(m_runButton, &RunButtonWidget::runRequested,
            m_launch, &LaunchController::onRunGame);
    connect(m_runButton, &RunButtonWidget::targetChanged,
            m_launch, &LaunchController::onTargetChanged);
    connect(m_modList, &ModListWidget::modToggled,
            m_pluginList, &PluginListWidget::refresh);
    connect(m_downloadsLibrary, &DownloadsLibraryView::modInstalledFromDownload, this, [this] {
        if (m_session->activeGame().detected)
            m_modList->loadForGame(m_session->activeGame(), m_session->currentProfile());
    });

    connect(m_addGameAction, &QAction::triggered,
            m_gameSetup, &GameSetupController::onAddNewGame);
    connect(m_exportAction, &QAction::triggered, this, &MainWindow::onExportMods);
    connect(m_importAction, &QAction::triggered, this, &MainWindow::onImportMods);
    connect(m_unmountAction, &QAction::triggered,
            m_session, &SessionController::onUnmountMods);
    connect(m_patch4GBAction, &QAction::triggered,
            m_falloutPatch, &FalloutPatchController::onPatchFalloutTo4GB);
    connect(m_installTtwAction, &QAction::triggered,
            m_gameSetup, &GameSetupController::onInstallTTW);

    connect(m_session, &SessionController::activeGameChanged,
            m_falloutPatch, &FalloutPatchController::onActiveGameChanged);
    connect(m_session, &SessionController::activeGameChanged,
            m_gameSetup, &GameSetupController::onActiveGameChanged);
    connect(m_session, &SessionController::activeGameChanged, this,
            [this](const GameInfo&) { updateTransferActionsEnabled(); });
    connect(m_grpc, &GrpcClient::connected, this, &MainWindow::updateTransferActionsEnabled);
    connect(m_grpc, &GrpcClient::disconnected, this, &MainWindow::updateTransferActionsEnabled);
    updateTransferActionsEnabled();
    connect(m_launch, &LaunchController::ttwInstallRequested,
            m_gameSetup, &GameSetupController::onInstallTTW);

    connect(m_grpc, &GrpcClient::connected, this, [this] {
        statusBar()->showMessage("Daemon connected", 3000);
        m_grpc->detectGames();
        m_grpc->startWatching();
    });
    connect(m_grpc, &GrpcClient::disconnected, this, [this] {
        statusBar()->showMessage("Daemon disconnected");
    });

    connect(m_grpc, &GrpcClient::installCompleted, this, [this](const QString& name, int count) {
        statusBar()->showMessage(QString("Installed \"%1\" (%2 files)").arg(name).arg(count), 5000);
        if (m_session->activeGame().detected)
            m_modList->loadForGame(m_session->activeGame(), m_session->currentProfile());
    });
    connect(m_grpc, &GrpcClient::installFailed, this, [this](const QString& err) {
        if (err.contains("fomod_required"))
            return;
        statusBar()->showMessage(QString("Install failed: %1").arg(err), 5000);
    });

    connect(m_grpc, &GrpcClient::daemonInfo, this, [this](const QString& info) {
        statusBar()->showMessage(info, 5000);
    });
    connect(m_grpc, &GrpcClient::daemonError, this, [this](const QString& err) {
        statusBar()->showMessage(err, 10000);
    });
}

void MainWindow::onInstallMod()
{
    if (m_session->activeGame().shortName.isEmpty()) {
        dialogs::warn(this, "No Game Selected", "Select a game first.");
        return;
    }

    QString path = QFileDialog::getOpenFileName(
        this, "Install Mod from Archive", QDir::homePath(),
        "Archives (*.zip *.7z *.rar);;All files (*)");

    if (path.isEmpty())
        return;

    QString modName = QFileInfo(path).completeBaseName();
    QString modsDir = GameInfo::modsDirPathFor(m_session->activeGame().shortName);

    ModInstallDialog dlg(path, modsDir, modName, this);
    dlg.setDaemonContext(m_grpc, m_session->activeGame().shortName);
    if (dlg.exec() == QDialog::Accepted) {
        statusBar()->showMessage(
            QString("Installed \"%1\" (%2 files)")
                .arg(dlg.installedModName())
                .arg(dlg.installedFileCount()),
            5000);
        m_modList->loadForGame(m_session->activeGame(), m_session->currentProfile());
    }
}

void MainWindow::onOpenSettings()
{
    SettingsDialog dlg(m_grpc, &m_config, this);
    connect(&dlg, &SettingsDialog::collapsedSeparatorViewChanged,
            this, [this](bool on) {
                if (m_modList) m_modList->applyCollapsedSeparatorView(on);
            });
    dlg.exec();
    if (m_themeActions) {
        QString current = ThemeManager::canonicalThemeName(m_config.preferredStyle());
        for (auto* a : m_themeActions->actions())
            a->setChecked(a->text() == current);
    }
    if (m_appearanceActions) {
        const QString mode = m_config.appearanceMode();
        for (auto* a : m_appearanceActions->actions())
            a->setChecked(a->text().toLower() == mode);
    }
}

void MainWindow::onOpenIniEditor()
{
    if (!m_session->activeGame().detected) {
        dialogs::info(this, "INI Editor", "Select a game first.");
        return;
    }
    if (m_session->currentProfile().isEmpty()) {
        dialogs::info(this, "INI Editor", "Select a profile first.");
        return;
    }
    IniEditorDialog dlg(m_grpc, m_session->activeGame().shortName, m_session->activeGame().name,
                        m_session->currentProfile(), this);
    dlg.exec();
}

void MainWindow::onOpenExecutables()
{
    if (!m_session->activeGame().detected) {
        dialogs::info(this, "External Tools", "Select a game first.");
        return;
    }
    if (!m_grpc->isConnected()) {
        dialogs::warn(this, "External Tools", "The daemon must be running.");
        return;
    }
    ExecutablesDialog dlg(m_grpc, m_session->activeGame().shortName, m_session->currentProfile(), this);
    dlg.exec();
}

// Enables Export/Import Mods only while the daemon is connected and a game is active.
void MainWindow::updateTransferActionsEnabled()
{
    const bool ready = m_grpc->isConnected() && m_session && m_session->activeGame().detected;
    if (m_exportAction) m_exportAction->setEnabled(ready);
    if (m_importAction) m_importAction->setEnabled(ready);
}

void MainWindow::onExportMods()
{
    if (!m_grpc->isConnected() || !m_session->activeGame().detected)
        return;
    ExportDialog dlg(m_grpc, m_session->activeGame().shortName, this);
    dlg.exec();
}

void MainWindow::onImportMods()
{
    if (!m_grpc->isConnected() || !m_session->activeGame().detected)
        return;
    ImportDialog dlg(m_grpc, m_session->activeGame().shortName, this);
    connect(&dlg, &ImportDialog::importCompleted, this, &MainWindow::refreshAfterImport);
    dlg.exec();
}

// Reloads the profile selector and mod list after an import lands new folders/profiles.
void MainWindow::refreshAfterImport()
{
    if (!m_session->activeGame().detected)
        return;
    m_profileSelector->loadForGame(m_session->activeGame().shortName, m_session->currentProfile());
    m_modList->loadForGame(m_session->activeGame(), m_session->currentProfile());
}

}

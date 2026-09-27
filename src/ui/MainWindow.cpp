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
#include "ModLoaderController.h"
#include "ModDependencyController.h"
#include "SteamMaintenanceController.h"
#include "ModDependencyText.h"
#include "SmapiModsWidget.h"
#include "SettingsDialog.h"
#include "ModInstallDialog.h"
#include "IniEditorDialog.h"
#include "ExecutablesDialog.h"
#include "ExportDialog.h"
#include "ImportDialog.h"
#include "ThemeManager.h"
#include "Dialogs.h"
#include "InstallErrorText.h"
#include "ErrorPresenter.h"

#include <QToolBar>
#include <QToolButton>
#include <QSplitter>
#include <QStatusBar>
#include <QMenuBar>
#include <QFileDialog>
#include <QVBoxLayout>
#include <QActionGroup>
#include <QAbstractButton>
#include <QCloseEvent>
#include <QCheckBox>
#include <QSettings>
#include <QInputDialog>
#include <QMessageBox>
#include <QPushButton>

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
        statusBar()->showMessage("Gorganizer's background service connected", 3000);
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
    m_iniEditorAction = toolsMenu->addAction("INI &Editor...", this, &MainWindow::onOpenIniEditor);
    toolsMenu->addAction("E&xternal Tools...", this, &MainWindow::onOpenExecutables);
    m_unmountAction = toolsMenu->addAction("&Deactivate Mods…");
    m_pauseForSteamAction = toolsMenu->addAction("Pause Mods for a Steam Update…");
    m_steamHelpAction = toolsMenu->addAction("Steam Update Help…");
    m_patch4GBAction = toolsMenu->addAction("Patch Fallout to &4GB");
    m_patch4GBAction->setVisible(false);
    m_installTtwAction = toolsMenu->addAction("Install Tale of Two &Wastelands...");
    m_installTtwAction->setVisible(false);
    m_smapiMenu = toolsMenu->addMenu("S&MAPI");
    m_smapiMenu->menuAction()->setVisible(false);
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

    m_dataPlaceholder = new QWidget;
    auto* dpLayout = new QVBoxLayout(m_dataPlaceholder);
    auto* dpLabel = new QLabel("Virtual data directory.\nShows the merged view of all enabled mods.\n\n(Coming soon)");
    dpLabel->setAlignment(Qt::AlignCenter);
    dpLabel->setObjectName("hintLabel");
    dpLayout->addWidget(dpLabel);
    m_rightTabs->addTab(m_dataPlaceholder, "Data");

    m_smapiMods = new SmapiModsWidget;
    m_rightTabs->setTabVisible(m_rightTabs->addTab(m_smapiMods, "SMAPI"), false);

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
    m_statusInfo->setTextFormat(Qt::PlainText);
    statusBar()->addWidget(m_statusInfo, 1);

    m_connectionIndicator = new ConnectionIndicator(m_grpc);
    statusBar()->addPermanentWidget(m_connectionIndicator);
}

// Instantiates the workflow controllers with the widgets and daemon client they drive.
void MainWindow::createControllers()
{
    m_session = new SessionController(m_config, m_grpc, m_gameSelector, m_profileSelector,
                                      m_modList, m_pluginList, m_downloadsLibrary, m_runButton,
                                      m_applyButton, m_unmountAction, m_statusInfo, statusBar(), this);
    m_modLoader = new ModLoaderController(m_grpc, m_session, m_smapiMenu, statusBar(), this);
    m_steamMaintenance = new SteamMaintenanceController(m_grpc, m_session, m_steamHelpAction,
                                                        m_pauseForSteamAction, statusBar(), this);
    m_launch = new LaunchController(m_config, m_grpc, m_session, m_modLoader, m_runButton, statusBar(), this);
    m_falloutPatch = new FalloutPatchController(m_grpc, m_session, m_runButton, m_patch4GBAction,
                                                statusBar(), this);
    m_gameSetup = new GameSetupController(m_config, m_grpc, m_session, m_modList,
                                          m_installTtwAction, statusBar(), this);
    m_modDependencies = new ModDependencyController(m_grpc, m_session, m_modList, m_smapiMods,
                                                    statusBar(), this);
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
    connect(m_steamMaintenance, &SteamMaintenanceController::modRecovered, this, [this](const QString& gameId) {
        if (m_session->activeGame().detected && m_session->activeGame().shortName == gameId)
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
    connect(m_session, &SessionController::recoveryReviewRequested,
            m_gameSetup, &GameSetupController::reviewRecovery);
    connect(m_session, &SessionController::activeGameChanged, this,
            [this](const GameInfo&) { updateTransferActionsEnabled(); });
    connect(m_session, &SessionController::activeGameChanged,
            this, &MainWindow::applyGameCapabilities);
    connect(m_session, &SessionController::activeGameChanged,
            m_modLoader, &ModLoaderController::onActiveGameChanged);
    connect(m_session, &SessionController::activeGameChanged,
            m_modDependencies, &ModDependencyController::onActiveGameChanged);
    connect(m_session, &SessionController::profileChanged,
            m_modDependencies, &ModDependencyController::onProfileChanged);
    connect(m_modLoader, &ModLoaderController::modLoaderStatusChanged,
            m_modDependencies, &ModDependencyController::onModLoaderStatusChanged);
    connect(m_modLoader, &ModLoaderController::modLoaderStatusChanged, this,
            [this](const QString& gameId, const GrpcModLoaderStatus& status) {
                if (gameId != m_session->activeGame().shortName)
                    return;
                if (status.state == GrpcModLoaderStateUnspecified)
                    m_runButton->clearModLoaderStatus();
                else
                    m_runButton->setModLoaderStatus(status);
            });
    connect(m_grpc, &GrpcClient::connected, this, &MainWindow::updateTransferActionsEnabled);
    connect(m_grpc, &GrpcClient::disconnected, this, &MainWindow::updateTransferActionsEnabled);
    updateTransferActionsEnabled();
    connect(m_launch, &LaunchController::ttwInstallRequested,
            m_gameSetup, &GameSetupController::onInstallTTW);

    connect(m_grpc, &GrpcClient::connected, this, [this] {
        statusBar()->showMessage("Gorganizer's background service connected", 3000);
        m_grpc->detectGames();
        m_grpc->startWatching();
    });
    connect(m_grpc, &GrpcClient::disconnected, this, [this] {
        statusBar()->showMessage("Gorganizer's background service disconnected. Run is unavailable until it reconnects.");
    });

    connect(m_grpc, &GrpcClient::installRequestCompleted, this, &MainWindow::onInstallRequestCompleted);
    connect(m_grpc, &GrpcClient::installRequestFailed, this, &MainWindow::onInstallRequestFailed);

    connect(m_grpc, &GrpcClient::daemonInfo, this, [this](const QString& info) {
        const bool failed = info.startsWith(QLatin1String("[smapi:failed]"));
        const bool warning = info.startsWith(QLatin1String("[smapi:warning]"));
        statusBar()->showMessage(failed || warning
            ? errorSummary("change SMAPI", info.mid(failed ? 14 : 15).trimmed(), true) : info, 5000);
    });
    connect(m_grpc, &GrpcClient::daemonError, this, [this](const QString& err) {
        statusBar()->showMessage(errorSummary("complete this request", err), 10000);
    });
}

void MainWindow::closeEvent(QCloseEvent* event)
{
    const QString loaderOperation = m_modLoader ? m_modLoader->interruptibleOperation() : QString();
    QStringList paragraphs;
    if (!loaderOperation.isEmpty()) {
        const QString consequence = m_daemonOwned
            ? QStringLiteral("Gorganizer also stops the background service it started. The service rolls an "
                             "unfinished SMAPI change back the next time it starts.")
            : QStringLiteral("Gorganizer's background service keeps running and finishes the operation or rolls "
                             "it back, but Gorganizer cannot report the result.");
        paragraphs.append(QStringLiteral("%1 is still in progress.\n\nQuitting may interrupt this operation. "
                                         "Keep Gorganizer open until it finishes.\n\n%2 The mods will not be "
                                         "activated again automatically.").arg(loaderOperation, consequence));
    }
    if (m_pendingExternalInstall) {
        const QString consequence = m_daemonOwned
            ? QStringLiteral("Gorganizer also stops the background service it started, which interrupts the install.")
            : QStringLiteral("Gorganizer's background service keeps running and finishes the install, but "
                             "Gorganizer cannot report the result.");
        paragraphs.append(QStringLiteral("Installing \"%1\" is still in progress.\n\nQuitting may interrupt "
                                         "this operation. Keep Gorganizer open until it finishes.\n\n%2")
                              .arg(m_pendingExternalInstall->name, consequence));
    }
    const bool interrupted = !paragraphs.isEmpty();
    if (m_grpc->isConnected()) {
        std::vector<GrpcShutdownPlanItem> items;
        QString error;
        if (m_grpc->getShutdownPlanSync(2000, items, error)) {
            for (const auto& item : items) {
                if (item.willUnmount) continue;
                QString reason;
                if (item.retainedReason == QLatin1String("game_running"))
                    reason = QStringLiteral("the game is still running");
                else if (item.retainedReason == QLatin1String("launch_recent"))
                    reason = QStringLiteral("the game was started moments ago");
                else if (item.retainedReason == QLatin1String("tool_running"))
                    reason = QStringLiteral("a tool started from Gorganizer is still running");
                else if (item.retainedReason == QLatin1String("recovery_deferred"))
                    reason = QStringLiteral("an earlier change is waiting for the game to close");
                else if (item.retainedReason == QLatin1String("steam_busy"))
                    reason = QStringLiteral("Steam is updating the game");
                else
                    reason = QStringLiteral("another change is still in progress");
                const auto game = GameInfo::findByShortName(item.gameId);
                const QString name = game ? game->name : item.gameId;
                paragraphs.append(QStringLiteral("Mods for %1 will stay active because %2. "
                                                 "Gorganizer turns them off the next time it starts after the game has closed.")
                                      .arg(name, reason));
            }
            if (!m_daemonOwned && !items.empty()) {
                paragraphs.append(QStringLiteral("Gorganizer's background service keeps running after this window closes, "
                                                 "so your mods stay active until you choose Deactivate Mods."));
            }
        }
    }
    if (paragraphs.isEmpty()) {
        QMainWindow::closeEvent(event);
        return;
    }
    if (!interrupted) {
        QSettings settings;
        if (!settings.value(QStringLiteral("shutdown/hideRetentionNotice"), false).toBool()) {
            QMessageBox box(this);
            box.setIcon(QMessageBox::Information);
            box.setWindowTitle(QStringLiteral("Mods Still Active"));
            box.setStandardButtons(QMessageBox::Ok);
            box.setTextFormat(Qt::PlainText);
            box.setText(paragraphs.join(QStringLiteral("\n\n")));
            auto* checkbox = new QCheckBox(QStringLiteral("Don't show again"), &box);
            box.setCheckBox(checkbox);
            box.exec();
            if (checkbox->isChecked())
                settings.setValue(QStringLiteral("shutdown/hideRetentionNotice"), true);
        }
    } else {
        const QString title = loaderOperation.isEmpty() ? QStringLiteral("Mod Install Running")
                                                        : QStringLiteral("SMAPI Operation Running");
        const QString text = paragraphs.join(QStringLiteral("\n\n")) + QStringLiteral("\n\nQuit anyway?");
        if (!dialogs::plainConfirm(this, title, text, QMessageBox::Warning, QMessageBox::No)) {
            event->ignore();
            return;
        }
    }
    QMainWindow::closeEvent(event);
}

void MainWindow::onInstallMod()
{
    const GameInfo game = m_session->activeGame();
    if (game.shortName.isEmpty()) {
        dialogs::warn(this, "No Game Selected", "Select a game first.");
        return;
    }

    const bool localInstall = usesLocalDataRootInstall(game);
    if (!localInstall) {
        if (!game.capabilitiesKnown) {
            dialogs::info(this, "Install Mod", "Waiting for Gorganizer's background service — try again in a moment.");
            return;
        }
        if (m_pendingExternalInstall) {
            dialogs::info(this, "Install Mod", "Another mod install is still running. Try again when it finishes.");
            return;
        }
        if (!m_grpc->isConnected()) {
            dialogs::warn(this, "Install Mod", "Gorganizer's background service must be running to install mods for this game.");
            return;
        }
    }

    QString path = QFileDialog::getOpenFileName(
        this, "Install Mod from Archive", QDir::homePath(),
        "Archives (*.zip *.7z *.rar);;All files (*)");

    if (path.isEmpty())
        return;

    if (!localInstall) {
        installThroughDaemonLayout(game.shortName, path);
        return;
    }

    QString modName = QFileInfo(path).completeBaseName();

    ModInstallDialog dlg(game.shortName, modName, m_grpc,
                         ModInstallDialog::ArchiveSource::fromExternal(path), this);
    if (dlg.exec() == QDialog::Accepted) {
        statusBar()->showMessage(
            QString("Installed \"%1\" (%2 files)")
                .arg(dlg.installedModName())
                .arg(dlg.installedFileCount()),
            5000);
        m_modList->loadForGame(m_session->activeGame(), m_session->currentProfile());
    }
}

void MainWindow::installThroughDaemonLayout(const QString& gameId, const QString& path)
{
    const QString name = askModName("Install Mod", "Mod name:", QFileInfo(path).completeBaseName());
    if (name.isEmpty())
        return;
    PendingExternalInstall request;
    request.gameId = gameId;
    request.path = path;
    request.name = name;
    request.mode = GrpcInstallAsNewMod;
    startExternalInstall(request);
}

QString MainWindow::askModName(const QString& title, const QString& label, const QString& initial)
{
    QString name = initial;
    for (;;) {
        bool ok = false;
        name = QInputDialog::getText(this, title, label, QLineEdit::Normal, name, &ok).trimmed();
        if (!ok)
            return QString();
        const QString problem = modNameProblem(name);
        if (problem.isEmpty())
            return name;
        dialogs::plainWarn(this, title, problem);
    }
}

void MainWindow::startExternalInstall(const PendingExternalInstall& request)
{
    if (m_pendingExternalInstall) {
        dialogs::info(this, "Install Mod", "Another mod install is still running. Try again when it finishes.");
        return;
    }
    if (!m_grpc->isConnected()) {
        dialogs::warn(this, "Install Mod", "Gorganizer's background service must be running to install mods for this game.");
        return;
    }
    PendingExternalInstall pending = request;
    pending.requestId = m_grpc->startInstallExternal(request.gameId, request.path, request.mode, request.name);
    m_pendingExternalInstall = pending;
    statusBar()->showMessage(QString("Installing \"%1\"…").arg(request.name));
}

void MainWindow::onInstallRequestCompleted(quint64 requestId, const QString& modFolder, int fileCount)
{
    if (m_pendingExternalInstall && m_pendingExternalInstall->requestId == requestId)
        m_pendingExternalInstall.reset();
    statusBar()->showMessage(
        QString("Installed \"%1\" (%2 files)").arg(modFolder, QString::number(fileCount)), 5000);
    if (m_session->activeGame().detected)
        m_modList->reloadMods();
}

void MainWindow::onInstallRequestFailed(quint64 requestId, const QString& error)
{
    if (m_pendingExternalInstall && m_pendingExternalInstall->requestId == requestId) {
        const PendingExternalInstall request = *m_pendingExternalInstall;
        m_pendingExternalInstall.reset();
        onExternalInstallFailed(request, error);
        return;
    }
    if (parseInstallError(error).token == QLatin1String("fomod_required"))
        return;
    statusBar()->showMessage(errorSummary("install this mod", error, true), 5000);
}

void MainWindow::onExternalInstallFailed(const PendingExternalInstall& request, const QString& error)
{
    const InstallError parsed = parseInstallError(error);
    statusBar()->clearMessage();
    if (parsed.token == QLatin1String("mod_collision")) {
        QString existing = parsed.fields.value(QStringLiteral("name"));
        if (existing.isEmpty())
            existing = parsed.fields.value(QStringLiteral("existing")).section(QLatin1Char(','), 0, 0);
        if (existing.isEmpty())
            existing = request.name;
        resolveExternalInstallCollision(request, existing);
        return;
    }
    if (parsed.token == QLatin1String("invalid_target_mod") && request.mode == GrpcInstallAsNewMod) {
        presentError(this, "Install Mod", "install this mod", error, true);
        PendingExternalInstall retry = request;
        retry.name = askModName("Install Mod", "Mod name:", request.name);
        if (!retry.name.isEmpty())
            startExternalInstall(retry);
        return;
    }
    if (!parsed.token.isEmpty()) {
        presentError(this, "Install Mod", "install this mod", error, true);
        return;
    }
    presentError(this, "Install Mod", "install this mod", error, true);
}

void MainWindow::resolveExternalInstallCollision(const PendingExternalInstall& request,
                                                 const QString& existingName)
{
    QMessageBox box(this);
    box.setWindowTitle("Mod Already Exists");
    box.setIcon(QMessageBox::Question);
    box.setTextFormat(Qt::PlainText);
    box.setText(QString("A mod named \"%1\" is already installed.").arg(existingName));
    box.setInformativeText(
        "Merge into existing keeps every file already in that mod and adds this archive on top, so files "
        "an update removed or renamed stay behind and can break it.\n\n"
        "Rename… installs this archive as a separate mod, which is safer for updates.");
    QAbstractButton* mergeBtn = box.addButton("Merge into existing (keeps old files)", QMessageBox::AcceptRole);
    QAbstractButton* renameBtn = box.addButton("Rename…", QMessageBox::ActionRole);
    box.addButton(QMessageBox::Cancel);
    box.setDefaultButton(static_cast<QPushButton*>(renameBtn));
    box.exec();

    QAbstractButton* clicked = box.clickedButton();
    PendingExternalInstall retry = request;
    if (clicked == mergeBtn) {
        retry.name = existingName;
        retry.mode = GrpcInstallMergeIntoMod;
        startExternalInstall(retry);
        return;
    }
    if (clicked != renameBtn)
        return;
    retry.name = askModName("Rename Mod", "New mod name:", existingName);
    if (retry.name.isEmpty())
        return;
    retry.mode = GrpcInstallAsNewMod;
    startExternalInstall(retry);
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
        dialogs::warn(this, "External Tools", "Gorganizer's background service must be running.");
        return;
    }
    const GameInfo& game = m_session->activeGame();
    const bool lootAvailable = !game.capabilitiesKnown || game.capabilities.loot;
    ExecutablesDialog dlg(m_grpc, game.shortName, m_session->currentProfile(), this, lootAvailable);
    dlg.exec();
}

void MainWindow::applyGameCapabilities(const GameInfo& game)
{
    const int dataIndex = m_rightTabs->indexOf(m_dataPlaceholder);
    const int smapiIndex = m_rightTabs->indexOf(m_smapiMods);
    if (dataIndex >= 0 && smapiIndex >= 0) {
        const bool showSmapi = showsModDependencies(game);
        const int current = m_rightTabs->currentIndex();
        m_rightTabs->setTabVisible(showSmapi ? smapiIndex : dataIndex, true);
        m_rightTabs->setTabVisible(showSmapi ? dataIndex : smapiIndex, false);
        if (current == (showSmapi ? dataIndex : smapiIndex))
            m_rightTabs->setCurrentIndex(showSmapi ? smapiIndex : dataIndex);
    }

    const int pluginsIndex = m_rightTabs->indexOf(m_pluginList);
    if (pluginsIndex >= 0) {
        const bool showPlugins = usesPlugins(game);
        const bool wasCurrent = m_rightTabs->currentIndex() == pluginsIndex;
        m_rightTabs->setTabVisible(pluginsIndex, showPlugins);
        if (!showPlugins && wasCurrent) {
            m_restorePluginsTab = true;
            for (int i = 0; i < m_rightTabs->count(); ++i) {
                if (m_rightTabs->isTabVisible(i)) {
                    m_rightTabs->setCurrentIndex(i);
                    break;
                }
            }
        } else if (showPlugins && m_restorePluginsTab) {
            m_restorePluginsTab = false;
            m_rightTabs->setCurrentIndex(pluginsIndex);
        }
    }

    if (m_iniEditorAction)
        m_iniEditorAction->setVisible(!game.capabilitiesKnown || game.capabilities.ini);
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

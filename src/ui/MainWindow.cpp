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
#include "SettingsDialog.h"
#include "ModInstallDialog.h"
#include "IniEditorDialog.h"
#include "ExecutablesDialog.h"
#include "GameDetector.h"
#include "TTWInstallDialog.h"
#include "ThemeManager.h"

#include <QToolBar>
#include <QToolButton>
#include <QSplitter>
#include <QStatusBar>
#include <QMessageBox>
#include <QMenuBar>
#include <QProcess>
#include <QFileDialog>
#include <QVBoxLayout>
#include <QActionGroup>
#include <QSet>
#include <QInputDialog>

#include <algorithm>

// Mirror of internal/config/paths.go::gameModsDirNames — keep in sync.
static QString modsDirectoryFor(const QString& gameShortName)
{
    static const QHash<QString, QString> dirNames = {
        {"morrowind", "Morrowind_Mods"}, {"oblivion", "Oblivion_Mods"},
        {"skyrim", "Skyrim_Mods"}, {"skyrimse", "SkyrimSE_Mods"},
        {"fallout3", "Fallout3_Mods"}, {"falloutnv", "FalloutNV_Mods"},
        {"fallout4", "Fallout4_Mods"}, {"starfield", "Starfield_Mods"},
        {"ttw", "TTW_Mods"},
    };
    QByteArray root = qgetenv("GORGANIZER_ROOT");
    if (!root.isEmpty()) {
        QString name = dirNames.value(gameShortName, gameShortName + "_Mods");
        return QString::fromUtf8(root) + "/" + name;
    }
    QString dataHome = qEnvironmentVariable("XDG_DATA_HOME");
    if (dataHome.isEmpty())
        dataHome = QDir::homePath() + "/.local/share";
    return dataHome + "/gorganizer/" + gameShortName + "/mods";
}

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
    loadManagedGames();

    connect(m_grpc, &GrpcClient::gameLaunched, this, &MainWindow::onGameLaunched);
    connect(m_grpc, &GrpcClient::gameLaunchFailed, this, &MainWindow::onGameLaunchFailed);
    connect(m_grpc, &GrpcClient::rpcError, this, &MainWindow::onRpcError);
    connect(m_grpc, &GrpcClient::connected, this, [this] {
        statusBar()->showMessage("Daemon connected", 3000);
        m_grpc->detectGames();
        m_grpc->startWatching();
    });
    connect(m_grpc, &GrpcClient::disconnected, this, [this] {
        statusBar()->showMessage("Daemon disconnected");
    });

    connect(m_grpc, &GrpcClient::gamesDetected, this, [this](const std::vector<GrpcGame>& detectedGames) {
        auto managedShortNames = m_config.managedGames();
        QSet<QString> keep(managedShortNames.begin(), managedShortNames.end());

        m_managedGames.clear();
        bool ttwVfsActive = false;
        for (const auto& g : detectedGames) {
            if (g.gameId == "ttw" && g.vfsActive)
                ttwVfsActive = true;
            if (!keep.contains(g.gameId))
                continue;
            GameInfo info;
            info.appId = g.steamAppId;
            info.name = g.name;
            info.shortName = g.gameId;
            info.installDir = g.installPath.toStdString();
            info.dataDir = g.dataPath.toStdString();
            info.detected = true;
            info.synthetic = g.synthetic;
            info.linkedFromShortName = g.linkedFromGameId;
            info.vfsActive = g.vfsActive;
            m_managedGames.push_back(info);
        }
        m_gameSelector->setGames(m_managedGames);
        QString activeShort = m_config.activeGameShortName();
        m_gameSelector->setActiveGameByShortName(activeShort);
        m_runButton->setTTWVfsActive(ttwVfsActive);
        auto current = m_gameSelector->currentGame();
        if (current.detected)
            onGameChanged(current.appId);
    });

    connect(m_grpc, &GrpcClient::installCompleted, this, [this](const QString& name, int count) {
        statusBar()->showMessage(QString("Installed \"%1\" (%2 files)").arg(name).arg(count), 5000);
        if (m_activeGame.detected)
            m_modList->loadForGame(m_activeGame, m_currentProfile);
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

    connect(m_grpc, &GrpcClient::recoveryPending, this,
        [this](const QString& gameId, const QString& dataPath,
               const QString& backupPath, const QString& reason) {
            QMessageBox box(this);
            box.setWindowTitle("Recovery needed");
            box.setIcon(QMessageBox::Warning);
            box.setTextFormat(Qt::RichText);
            box.setText(
                QString("<b>Game: %1</b><br><br>%2<br><br>"
                        "Data:&nbsp;<code>%3</code><br>"
                        "Backup:&nbsp;<code>%4</code><br><br>"
                        "Restoring will <b>delete the current Data/</b> and "
                        "rename Data.orig/ back. Inspect the directories first "
                        "if you're not sure where they came from.")
                    .arg(gameId.toHtmlEscaped(),
                         reason.toHtmlEscaped(),
                         dataPath.toHtmlEscaped(),
                         backupPath.toHtmlEscaped()));
            auto* restoreBtn = box.addButton("Restore from Data.orig",
                                             QMessageBox::DestructiveRole);
            box.addButton("Cancel", QMessageBox::RejectRole);
            box.exec();
            if (box.clickedButton() == restoreBtn) {
                m_grpc->restoreFromBackup(gameId);
            }
        });

    if (m_grpc->isConnected()) {
        statusBar()->showMessage("Daemon connected", 3000);
        m_grpc->detectGames();
        m_grpc->startWatching();
    }
}

void MainWindow::setupUi()
{
    auto* fileMenu = menuBar()->addMenu("&File");
    fileMenu->addAction("&Install Mod...", this, &MainWindow::onInstallMod);
    fileMenu->addSeparator();
    fileMenu->addAction("&Add New Game...", this, &MainWindow::onAddNewGame);
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

    auto* darkMenu = viewMenu->addMenu("Dark &Variant");
    m_themeActions = new QActionGroup(this);
    m_themeActions->setExclusive(true);
    QString currentVariant = m_config.preferredStyle();
    if (!ThemeManager::isDarkVariant(currentVariant))
        currentVariant = "Dracula";
    for (const auto& name : ThemeManager::availableDarkVariants()) {
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
    toolsMenu->addAction("&Unmount Mods (restore vanilla Data)", this, &MainWindow::onUnmountMods);
    m_patch4GBAction = toolsMenu->addAction("Patch Fallout to &4GB",
                                            this, &MainWindow::onPatchFalloutTo4GB);
    m_patch4GBAction->setVisible(false);
    m_installTtwAction = toolsMenu->addAction("Install Tale of Two &Wastelands...",
                                              this, &MainWindow::onInstallTTW);
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

    // "Apply Changes" — under defer+coalesce, mod-list edits mark the VFS dirty
    // instead of remounting on every click. This button (shown only while dirty)
    // rebuilds the on-disk farm; launching also applies automatically.
    m_applyButton = new QToolButton;
    m_applyButton->setText("Apply Changes");
    m_applyButton->setToolButtonStyle(Qt::ToolButtonTextOnly);
    m_applyButton->setToolTip("Rebuild the game's mod view to match your pending changes.\n"
                              "Launching the game applies them automatically.");
    m_applyButton->setStyleSheet("QToolButton { font-weight: bold; }");
    m_applyButton->setVisible(false);
    connect(m_applyButton, &QToolButton::clicked, this, &MainWindow::onApplyChanges);
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
    dpLabel->setStyleSheet("color: gray;");
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

    connect(m_gameSelector, &GameSelectorWidget::gameChanged,
            this, &MainWindow::onGameChanged);
    connect(m_runButton, &RunButtonWidget::runRequested,
            this, &MainWindow::onRunGame);
    connect(m_runButton, &RunButtonWidget::targetChanged, this,
            [this](const QString& toolId) {
                if (m_activeGame.detected)
                    m_config.setLastToolFor(m_activeGame.shortName, toolId);
            });
    connect(m_profileSelector, &ProfileSelectorWidget::profileChanged,
            this, &MainWindow::onProfileChanged);

    connect(m_modList, &ModListWidget::modToggled,
            m_pluginList, &PluginListWidget::refresh);
    // Defer+coalesce (U-2): a mod toggle/reorder persists via setModList, which
    // marks the VFS dirty on the daemon. We no longer remount on every click;
    // the daemon emits VFSStatus{dirty} and we surface an Apply affordance. The
    // farm is rebuilt on Apply or automatically just before launch.

    connect(m_grpc, &GrpcClient::vfsStatusChanged, this,
            [this](const GrpcVFSStatus& status) {
        if (!m_activeGame.detected) return;
        if (status.gameId != m_activeGame.shortName) return;
        m_vfsMounted = status.mounted;
        setVfsDirty(status.dirty);
        m_pluginList->refresh();
    });

    connect(m_downloadsLibrary, &DownloadsLibraryView::modInstalledFromDownload, this, [this] {
        if (m_activeGame.detected)
            m_modList->loadForGame(m_activeGame, m_currentProfile);
    });
}

void MainWindow::loadManagedGames()
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
        onGameChanged(current.appId);
}

void MainWindow::onGameChanged(uint32_t appId)
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
    m_pluginList->setModsDir(modsDirectoryFor(m_activeGame.shortName));
    m_pluginList->loadForGame(m_activeGame);
    m_pluginList->setActiveProfile(m_currentProfile);

    const bool isFNV = (m_activeGame.shortName == "falloutnv" && m_activeGame.detected);
    const bool isTTW = (m_activeGame.shortName == "ttw" && m_activeGame.detected);
    if (m_patch4GBAction)
        m_patch4GBAction->setVisible(isFNV || isTTW);
    bool patched = false;
    if ((isFNV || isTTW) && m_grpc->isConnected())
        patched = m_grpc->is4GBPatched(m_activeGame.shortName);
    m_runButton->setFourGBPatched(patched);
    if (m_patch4GBAction && patched) {
        m_patch4GBAction->setEnabled(false);
        m_patch4GBAction->setToolTip(
            "FalloutNV.exe is already patched to 4GB. Re-running the patcher is unnecessary.");
    } else if (m_patch4GBAction) {
        m_patch4GBAction->setEnabled(true);
        m_patch4GBAction->setToolTip(QString());
    }

    if (m_installTtwAction) {
        bool ttwInstalled = false;
        if (isTTW && m_grpc->isConnected()) {
            QString verr;
            ttwInstalled = m_grpc->verifyTTWIntegrity(verr);
        }
        m_installTtwAction->setVisible(isTTW && !ttwInstalled);
    }

    if (m_activeGame.detected) {
        QString modsDir = modsDirectoryFor(m_activeGame.shortName);
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

    updateStatusBarInfo();
}

void MainWindow::setVfsDirty(bool dirty)
{
    m_vfsDirty = dirty;
    if (m_applyButton) {
        m_applyButton->setVisible(dirty);
        m_applyButton->setEnabled(dirty);
    }
    if (dirty)
        statusBar()->showMessage("Mod changes pending — click \"Apply Changes\" or just launch.", 4000);
}

void MainWindow::onApplyChanges()
{
    if (!m_activeGame.detected || m_currentProfile.isEmpty() || !m_grpc->isConnected())
        return;
    m_applyButton->setEnabled(false);
    statusBar()->showMessage("Applying mod changes…");
    // When mounted, RebuildVFS re-materializes the on-disk farm in place; if not
    // yet mounted (rare — game-switch mounts), fall back to a mount/swap.
    if (m_vfsMounted)
        m_grpc->rebuildVfs(m_activeGame.shortName);
    else
        m_grpc->mountVfsWithSwap(m_activeGame.shortName, m_currentProfile);
}

void MainWindow::onProfileChanged(const QString& profileName)
{
    m_currentProfile = profileName;
    if (m_activeGame.detected)
        m_config.setLastProfileFor(m_activeGame.shortName, profileName);
    m_modList->loadForGame(m_activeGame, profileName);
    m_pluginList->setActiveProfile(profileName);
    updateStatusBarInfo();
}

void MainWindow::onRunGame()
{
    if (!m_activeGame.detected) {
        QMessageBox::warning(this, "No Game Selected", "Please select a game first.");
        return;
    }

    auto target = m_runButton->currentTarget();
    if (target.type == RunButtonWidget::TargetInstallTool) {
        if (target.toolId == "ttw-install") {
            onInstallTTW();
            return;
        }
        if (!m_grpc->isConnected()) {
            QMessageBox::warning(this, "Not Connected",
                "The daemon must be running to install a script extender.");
            return;
        }
        statusBar()->showMessage(
            QString("Downloading %1 from Nexus...").arg(target.label));
        QString name, err;
        if (!m_grpc->installScriptExtender(m_activeGame.shortName, name, err)) {
            QMessageBox::warning(this, "Install Failed",
                QString("%1\n\nIf you are a non-premium Nexus user, open the "
                        "mod page in a browser and click 'Download with "
                        "Manager' to trigger an NXM download instead.").arg(err));
            return;
        }
        statusBar()->showMessage(QString("%1 installed.").arg(name), 5000);
        m_runButton->setGame(m_activeGame,
            m_config.lastToolFor(m_activeGame.shortName));
        return;
    }

    if (m_grpc->isConnected()) {
        bool useTool = (target.type == RunButtonWidget::TargetTool);
        statusBar()->showMessage(
            useTool ? QString("Preparing mods and launching %1...").arg(target.label)
                    : QString("Preparing mods and launching %1...").arg(m_activeGame.name));
        // U-3: prevent a double-click from firing two launches; re-enabled when
        // the launch resolves (onGameLaunched / onGameLaunchFailed).
        m_runButton->setEnabled(false);
        m_grpc->launchGame(m_activeGame.shortName, useTool, m_currentProfile);
    } else {
        QString steamUrl = QString("steam://rungameid/%1").arg(m_activeGame.appId);
        bool launched = QProcess::startDetached("xdg-open", {steamUrl});
        if (!launched) {
            QMessageBox::warning(this, "Launch Failed",
                "Could not launch Steam. Is Steam installed?");
            return;
        }
        statusBar()->showMessage("Launched " + m_activeGame.name + " (no mods)", 5000);
    }
}

void MainWindow::onInstallMod()
{
    if (m_activeGame.shortName.isEmpty()) {
        QMessageBox::warning(this, "No Game Selected", "Select a game first.");
        return;
    }

    QString path = QFileDialog::getOpenFileName(
        this, "Install Mod from Archive", QDir::homePath(),
        "Archives (*.zip *.7z *.rar);;All files (*)");

    if (path.isEmpty())
        return;

    QString modName = QFileInfo(path).completeBaseName();
    QString modsDir = modsDirectoryFor(m_activeGame.shortName);

    ModInstallDialog dlg(path, modsDir, modName, this);
    dlg.setDaemonContext(m_grpc, m_activeGame.shortName);
    if (dlg.exec() == QDialog::Accepted) {
        statusBar()->showMessage(
            QString("Installed \"%1\" (%2 files)")
                .arg(dlg.installedModName())
                .arg(dlg.installedFileCount()),
            5000);
        m_modList->loadForGame(m_activeGame, m_currentProfile);
    }
}

void MainWindow::onPatchFalloutTo4GB()
{
    const bool isFNV = (m_activeGame.shortName == "falloutnv");
    const bool isTTW = (m_activeGame.shortName == "ttw");
    if ((!isFNV && !isTTW) || !m_activeGame.detected) {
        QMessageBox::information(this, "Patch Fallout to 4GB",
            "This patch is only available for Fallout: New Vegas (or "
            "Tale of Two Wastelands, which shares FNV's install).");
        return;
    }
    if (!m_grpc->isConnected()) {
        QMessageBox::warning(this, "Not Connected",
            "The daemon must be running to download the 4GB patcher.");
        return;
    }

    auto confirm = QMessageBox::question(this, "Patch Fallout to 4GB",
        "<p>The 4GB Patcher modifies FalloutNV.exe in place so the game can "
        "address more than 2 GiB of memory — required for heavy mod load orders.</p>"
        "<p>Requirements:</p>"
        "<ul>"
        "<li>xNVSE must already be installed.</li>"
        "<li>A Nexus API key must be configured in Tools &#x2192; Settings.</li>"
        "</ul>"
        "<p>Continue?</p>",
        QMessageBox::Yes | QMessageBox::No, QMessageBox::Yes);
    if (confirm != QMessageBox::Yes)
        return;

    statusBar()->showMessage("Downloading FNV 4GB patcher from Nexus...");
    QString patcherExePath, version, err;
    if (!m_grpc->install4GBPatcher(m_activeGame.shortName, patcherExePath, version, err)) {
        const QString lower = err.toLower();
        if (lower.contains("xnvse")) {
            QMessageBox::warning(this, "xNVSE Required",
                "<p>xNVSE must be installed before applying the 4GB patch. "
                "The patcher relies on the script extender being in place.</p>"
                "<p>Open the Run combo and choose <b>Install xNVSE...</b>, then try "
                "again.</p>");
        } else if (lower.contains("api key") || lower.contains("apikey")) {
            QMessageBox::warning(this, "Nexus API Key Required",
                "<p>A Nexus Mods API key is required to download the 4GB patcher.</p>"
                "<p>Open <b>Tools &#x2192; Settings</b> and paste a key, then try "
                "again.</p>");
        } else {
            QMessageBox::warning(this, "Download Failed",
                QString("<p>%1</p>"
                        "<p>If you are a non-premium Nexus user, open the mod page "
                        "in a browser and click 'Download with Manager' to trigger "
                        "an NXM download.</p>").arg(err.toHtmlEscaped()));
        }
        statusBar()->clearMessage();
        return;
    }

    statusBar()->showMessage(
        QString("FNV 4GB patcher %1 downloaded.").arg(version), 5000);

    auto apply = QMessageBox::question(this, "Apply 4GB Patch",
        QString("<p>The patcher has been extracted to:</p>"
                "<p><code>%1</code></p>"
                "<p>Apply the patch to <b>FalloutNV.exe</b> now? "
                "This rewrites the game executable in place.</p>")
            .arg(patcherExePath.toHtmlEscaped()),
        QMessageBox::Yes | QMessageBox::No, QMessageBox::Yes);
    if (apply != QMessageBox::Yes) {
        statusBar()->showMessage(
            "Patcher downloaded; apply it later from Tools \xE2\x86\x92 Patch Fallout to 4GB.",
            8000);
        return;
    }

    statusBar()->showMessage("Applying 4GB patch...");
    QString output, applyErr;
    if (!m_grpc->apply4GBPatch(m_activeGame.shortName, patcherExePath, output, applyErr)) {
        QMessageBox::warning(this, "Patch Failed",
            QString("<p>%1</p>"
                    "<p>Patcher output:</p><pre>%2</pre>")
                .arg(applyErr.toHtmlEscaped(), output.toHtmlEscaped()));
        statusBar()->clearMessage();
        return;
    }

    QMessageBox::information(this, "Patch Applied",
        QString("<p>FalloutNV.exe has been patched to 4GB.</p>"
                "<p>Patcher output:</p><pre>%1</pre>")
            .arg(output.isEmpty() ? "(no output)" : output.toHtmlEscaped()));

    if (m_patch4GBAction) {
        m_patch4GBAction->setEnabled(false);
        m_patch4GBAction->setToolTip(
            "FalloutNV.exe is already patched to 4GB. Re-running the patcher is unnecessary.");
    }
    m_runButton->setFourGBPatched(true);
    statusBar()->showMessage("FalloutNV.exe patched to 4GB.", 5000);
}

void MainWindow::onInstallTTW()
{
    if (!m_grpc->isConnected()) {
        QMessageBox::warning(this, "Daemon not connected",
            "The gorganizer daemon is not running. Start it before launching the TTW installer.");
        return;
    }
    TTWInstallDialog dlg(m_grpc, m_activeGame.shortName, m_currentProfile, this);
    dlg.exec();
    const bool installed =
        (dlg.outcome() == TTWInstallDialog::Accepted ||
         dlg.outcome() == TTWInstallDialog::InstalledOnly);

    if (installed) {
        auto managed = m_config.managedGames();
        if (std::find(managed.begin(), managed.end(), QString("ttw")) == managed.end()) {
            managed.push_back("ttw");
            m_config.setManagedGames(managed);
        }
        m_config.setActiveGameShortName("ttw");
    }

    if (m_grpc->isConnected())
        m_grpc->detectGames();
    if (!installed) {
        onGameChanged(m_activeGame.appId);
        if (m_activeGame.detected && m_modList)
            m_modList->loadForGame(m_activeGame, m_currentProfile);
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
        QString current = m_config.preferredStyle();
        if (!ThemeManager::isDarkVariant(current))
            current = "Dracula";
        for (auto* a : m_themeActions->actions())
            a->setChecked(a->text() == current);
    }
    if (m_appearanceActions) {
        const QString mode = m_config.appearanceMode();
        for (auto* a : m_appearanceActions->actions())
            a->setChecked(a->text().toLower() == mode);
    }
}

void MainWindow::onAddNewGame()
{
    auto allDetected = GameDetector::detectAll();
    auto managed = m_config.managedGames();
    QSet<QString> managedSet(managed.begin(), managed.end());

    QStringList labels;
    std::vector<GameInfo> candidates;
    for (const auto& g : allDetected) {
        if (managedSet.contains(g.shortName))
            continue;
        candidates.push_back(g);
        if (g.appId == 0)
            labels.append(g.name);
        else
            labels.append(QString("%1 (App ID %2)").arg(g.name).arg(g.appId));
    }
    if (candidates.empty()) {
        QMessageBox::information(this, "Add New Game",
            "Every Bethesda game Steam can detect is already being managed.\n\n"
            "Install a new supported title in Steam, or use the manual-locate "
            "flow by editing ~/.config/gorganizer/gorganizer.conf.");
        return;
    }

    bool ok = false;
    QString chosenLabel = QInputDialog::getItem(this, "Add New Game",
        "Pick a detected game to start managing:", labels, 0, false, &ok);
    if (!ok) return;
    int idx = labels.indexOf(chosenLabel);
    if (idx < 0) return;
    const auto& chosen = candidates[idx];

    managed.push_back(chosen.shortName);
    m_config.setManagedGames(managed);

    if (m_grpc->isConnected())
        m_grpc->detectGames();
    else
        loadManagedGames();

    m_config.setActiveGameShortName(chosen.shortName);
    statusBar()->showMessage(
        QString("%1 added. Use the Game dropdown to switch.").arg(chosen.name),
        5000);
}

void MainWindow::onOpenIniEditor()
{
    if (!m_activeGame.detected) {
        QMessageBox::information(this, "INI Editor", "Select a game first.");
        return;
    }
    if (m_currentProfile.isEmpty()) {
        QMessageBox::information(this, "INI Editor", "Select a profile first.");
        return;
    }
    IniEditorDialog dlg(m_grpc, m_activeGame.shortName, m_activeGame.name,
                        m_currentProfile, this);
    dlg.exec();
}

void MainWindow::onUnmountMods()
{
    if (!m_activeGame.detected || !m_grpc->isConnected())
        return;
    // C4/C5: explicit "stop playing" — deactivate the farm (capturing writes,
    // restoring vanilla Data/) and clear the steam-launched busy flag so a later
    // Apply/rebuild is allowed.
    if (QMessageBox::question(this, "Unmount mods",
            "Restore the game's vanilla Data folder?\n\nAny new writes (saves, tool output) "
            "are captured into Overwrite first. Do this when you've finished playing.")
        != QMessageBox::Yes)
        return;
    m_grpc->unmountVfs(m_activeGame.shortName);
    statusBar()->showMessage("Unmounting mods…", 4000);
}

void MainWindow::onOpenExecutables()
{
    if (!m_activeGame.detected) {
        QMessageBox::information(this, "External Tools", "Select a game first.");
        return;
    }
    if (!m_grpc->isConnected()) {
        QMessageBox::warning(this, "External Tools", "The daemon must be running.");
        return;
    }
    ExecutablesDialog dlg(m_grpc, m_activeGame.shortName, m_currentProfile, this);
    dlg.exec();
}

void MainWindow::onGameLaunched(int pid)
{
    m_runButton->setEnabled(true); // U-3
    statusBar()->showMessage(QString("Game launched (PID %1)").arg(pid), 5000);
}

void MainWindow::onGameLaunchFailed(const QString& error)
{
    m_runButton->setEnabled(true); // U-3
    if (error.contains("loader_missing:")) {
        const int idx = error.indexOf("loader_missing:");
        const QStringList parts = error.mid(idx + QString("loader_missing:").length())
                                     .split(':', Qt::KeepEmptyParts);
        const QString reason        = parts.value(0);
        const QString configuredExe = parts.value(1);
        const QString installPath   = parts.value(2);

        QString title = "Script extender launch blocked";
        QString body;
        if (reason == "missing") {
            body = QString(
                "Gorganizer can't find the script-extender loader "
                "(<b>%1</b>) in <code>%2</code>.<br><br>"
                "This usually happens after a Steam game update removes or "
                "restores files under the game's install directory. "
                "Reinstall the script extender from <b>Tools &#x2192; Install "
                "script extender</b> to continue."
            ).arg(configuredExe.isEmpty() ? "(none configured)" : configuredExe.toHtmlEscaped(),
                  installPath.toHtmlEscaped());
        } else if (reason == "modified") {
            body = QString(
                "The script-extender files under <code>%1</code> were "
                "modified since they were installed.<br><br>"
                "A Steam game update, manual edit, or anti-cheat tool can "
                "cause this. Reinstall the script extender from "
                "<b>Tools &#x2192; Install script extender</b> so the installed "
                "files match a known-good release."
            ).arg(installPath.toHtmlEscaped());
        } else if (reason == "looks-like-vanilla-launcher") {
            body = QString(
                "The configured loader exe (<b>%1</b>) is larger than any "
                "legitimate script-extender loader &#x2014; it's almost certainly "
                "the vanilla Bethesda launcher a Steam update restored.<br><br>"
                "Reinstall the script extender from <b>Tools &#x2192; Install "
                "script extender</b>, which will replace the file and "
                "re-register the correct launcher."
            ).arg(configuredExe.toHtmlEscaped());
        } else if (reason == "no-loader-configured") {
            body = QString(
                "No script extender is registered for this game yet.<br><br>"
                "Install one from <b>Tools &#x2192; Install script extender</b>, "
                "then try launching with the extender again."
            );
        } else {
            body = QString("Script extender launch failed (%1). Reinstall "
                           "the extender and try again.").arg(reason.toHtmlEscaped());
        }
        QMessageBox box(QMessageBox::Warning, title, body, QMessageBox::Ok, this);
        box.setTextFormat(Qt::RichText);
        box.exec();
        updateStatusBarInfo();
        return;
    }

    if (error.contains("fnv4gb_not_applied_for_ttw")) {
        QMessageBox::warning(this, "Patch FalloutNV.exe to 4GB",
            "<p><b>FalloutNV.exe is not LAA-patched.</b> TTW's merged data "
            "set exceeds FNV's 2&nbsp;GiB memory cap within seconds of the "
            "main menu — that's the \"music plays, then crash\" you just "
            "saw.</p>"
            "<p>Run <b>Tools &#x2192; Patch Fallout to 4GB</b> first, then "
            "try launching again.</p>");
        updateStatusBarInfo();
        return;
    }

    if (error.contains("xnvse_missing_for_ttw")) {
        QMessageBox::warning(this, "xNVSE Required",
            "<p>TTW launches via <b>nvse_loader.exe</b>, but xNVSE's runtime "
            "DLLs are not installed in the FNV directory.</p>"
            "<p>Open the Run combo and choose <b>Install xNVSE...</b>, then "
            "try launching again.</p>");
        updateStatusBarInfo();
        return;
    }

    QMessageBox::warning(this, "Launch Failed", error);
    updateStatusBarInfo();
}

void MainWindow::onRpcError(const QString& method, const QString& error)
{
    // U-4: a mod-list mutation is applied optimistically in the view. If the
    // daemon rejects it, revert to the daemon's authoritative state and surface
    // the failure loudly rather than leaving the UI silently out of sync.
    if (method == "SetModList") {
        if (m_activeGame.detected)
            m_modList->loadForGame(m_activeGame, m_currentProfile);
        QMessageBox::warning(this, "Change not saved",
            QString("A mod-list change could not be saved and was reverted:\n\n%1").arg(error));
        return;
    }
    statusBar()->showMessage(QString("Error (%1): %2").arg(method, error), 5000);
}

void MainWindow::updateStatusBarInfo()
{
    if (m_activeGame.detected) {
        m_statusInfo->setText(QString("%1 - %2").arg(m_activeGame.name, m_currentProfile));
    } else {
        m_statusInfo->setText("No game selected");
    }
}

} // namespace gorganizer

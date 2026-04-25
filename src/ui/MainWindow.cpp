#include "MainWindow.h"
#include "GrpcClient.h"
#include "GameSelectorWidget.h"
#include "ModListWidget.h"
#include "PluginListWidget.h"
#include "RunButtonWidget.h"
#include "ProfileSelectorWidget.h"
#include "ConnectionIndicator.h"
#include "DownloadProgressWidget.h"
#include "DownloadsLibraryView.h"
#include "ActivityLogPanel.h"
#include "SettingsDialog.h"
#include "ModInstallDialog.h"
#include "IniEditorDialog.h"
#include "GameDetector.h"
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

static QString modsDirectoryFor(const QString& gameShortName)
{
    static const QHash<QString, QString> dirNames = {
        {"morrowind", "Morrowind_Mods"}, {"oblivion", "Oblivion_Mods"},
        {"skyrim", "Skyrim_Mods"}, {"skyrimse", "SkyrimSE_Mods"},
        {"fallout3", "Fallout3_Mods"}, {"falloutnv", "FalloutNV_Mods"},
        {"fallout4", "Fallout4_Mods"}, {"starfield", "Starfield_Mods"},
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

    // Connect gRPC signals.
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

    // After daemon detects games, update managed games and trigger load.
    connect(m_grpc, &GrpcClient::gamesDetected, this, [this](const std::vector<GrpcGame>& detectedGames) {
        // The daemon returns everything it can see in Steam. We take that
        // list, intersect it with the user's managed-games preference,
        // and only surface those in the dropdown. Auto-adding every
        // detected game would expand the managed list behind the user's
        // back — what the wizard was meant to prevent. New games arrive
        // via File → Add New Game... only.
        auto managedIds = m_config.managedGames();
        QSet<uint32_t> keep(managedIds.begin(), managedIds.end());

        m_managedGames.clear();
        for (const auto& g : detectedGames) {
            if (!keep.contains(g.steamAppId))
                continue;
            GameInfo info;
            info.appId = g.steamAppId;
            info.name = g.name;
            info.shortName = g.gameId;
            info.installDir = g.installPath.toStdString();
            info.dataDir = g.dataPath.toStdString();
            info.detected = true;
            m_managedGames.push_back(info);
        }
        m_gameSelector->setGames(m_managedGames);
        uint32_t activeId = m_config.activeGameAppId();
        m_gameSelector->setActiveGame(activeId);
        auto current = m_gameSelector->currentGame();
        if (current.detected)
            onGameChanged(current.appId);
    });

    // Mod install result.
    connect(m_grpc, &GrpcClient::installCompleted, this, [this](const QString& name, int count) {
        statusBar()->showMessage(QString("Installed \"%1\" (%2 files)").arg(name).arg(count), 5000);
        if (m_activeGame.detected)
            m_modList->loadForGame(m_activeGame, m_currentProfile);
    });
    connect(m_grpc, &GrpcClient::installFailed, this, [this](const QString& err) {
        // fomod_required is a routing signal (daemon → frontend wizard),
        // not a real failure. The DownloadsLibraryView catches it
        // synchronously and opens the wizard; surfacing it on the status
        // bar would be a misleading "this failed" right before the popup
        // shows up.
        if (err.contains("fomod_required"))
            return;
        statusBar()->showMessage(QString("Install failed: %1").arg(err), 5000);
    });

    // Status stream events.
    connect(m_grpc, &GrpcClient::daemonInfo, this, [this](const QString& info) {
        statusBar()->showMessage(info, 5000);
    });
    // Daemon-side errors broadcast on WatchStatus (e.g. an NXM forwarded
    // by the browser arriving while no API key is set). The persistent
    // ActivityLogPanel renders these in red at the bottom of the window,
    // so a status-bar tick + log entry is enough — no modal interruption.
    connect(m_grpc, &GrpcClient::daemonError, this, [this](const QString& err) {
        statusBar()->showMessage(err, 10000);
    });

    // Recovery-pending modal: the daemon detected an ambiguous Data dir
    // alongside a backup at startup and is refusing to mount/launch
    // until the user confirms a destructive restore. Present a modal
    // that explains the on-disk state and offers two actions:
    //
    //   - Restore from Data.orig: rm -rf Data, mv Data.orig Data
    //     (the daemon clears the pending state on success)
    //   - Cancel: leave the directory alone — user must inspect manually
    //     before the game can be launched again.
    //
    // We show this as a non-blocking dialog so a single Daemon-side
    // event for multiple games doesn't cascade into modal stacking.
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
}

void MainWindow::setupUi()
{
    // --- Menu bar ---
    auto* fileMenu = menuBar()->addMenu("&File");
    fileMenu->addAction("&Install Mod...", this, &MainWindow::onInstallMod);
    fileMenu->addSeparator();
    fileMenu->addAction("&Add New Game...", this, &MainWindow::onAddNewGame);
    fileMenu->addSeparator();
    fileMenu->addAction("&Quit", this, &QWidget::close);

    // View menu with theme selector.
    auto* viewMenu = menuBar()->addMenu("&View");
    auto* themeMenu = viewMenu->addMenu("&Theme");
    m_themeActions = new QActionGroup(this);
    m_themeActions->setExclusive(true);
    QString currentTheme = m_config.preferredStyle();
    for (const auto& name : ThemeManager::availableThemes()) {
        auto* action = themeMenu->addAction(name);
        action->setCheckable(true);
        action->setChecked(name == currentTheme || (currentTheme.isEmpty() && name == "Default"));
        m_themeActions->addAction(action);
        connect(action, &QAction::triggered, this, [this, name] {
            ThemeManager::applyTheme(name);
            m_config.setPreferredStyle(name);
        });
    }

    // Tools menu.
    auto* toolsMenu = menuBar()->addMenu("&Tools");
    toolsMenu->addAction("INI &Editor...", this, &MainWindow::onOpenIniEditor);
    toolsMenu->addSeparator();
    toolsMenu->addAction("&Settings...", this, &MainWindow::onOpenSettings);

    // --- Toolbar (MO2-style layout) ---
    auto* toolbar = addToolBar("Main");
    toolbar->setMovable(false);
    toolbar->setFloatable(false);
    toolbar->setIconSize(QSize(24, 24));

    // Game selector.
    toolbar->addWidget(new QLabel(" Game: "));
    m_gameSelector = new GameSelectorWidget;
    m_gameSelector->setMinimumWidth(200);
    toolbar->addWidget(m_gameSelector);

    toolbar->addSeparator();

    // Profile section: label + selector widget (combo + create/delete/copy buttons).
    m_profileSelector = new ProfileSelectorWidget(m_grpc);
    toolbar->addWidget(m_profileSelector);

    toolbar->addSeparator();

    // Install Mod button.
    auto* installBtn = new QToolButton;
    installBtn->setText("Install Mod...");
    installBtn->setToolButtonStyle(Qt::ToolButtonTextOnly);
    connect(installBtn, &QToolButton::clicked, this, &MainWindow::onInstallMod);
    toolbar->addWidget(installBtn);

    // Spacer pushes Run button to the right.
    auto* spacer = new QWidget;
    spacer->setSizePolicy(QSizePolicy::Expanding, QSizePolicy::Preferred);
    toolbar->addWidget(spacer);

    // Run button.
    m_runButton = new RunButtonWidget;
    toolbar->addWidget(m_runButton);

    // --- Central widget: vertical splitter (workspace | activity log) ---
    //
    // The transient InstallStatusBanner that flashed above the mod list
    // during an install is gone — install/download progress is now
    // narrated in the persistent ActivityLogPanel docked at the bottom,
    // and an in-flight DownloadProgressWidget rides immediately above
    // the activity log inside the left column of the workspace splitter
    // (so its right edge naturally ends at the Downloads box).
    auto* central = new QWidget;
    auto* centralLayout = new QVBoxLayout(central);
    centralLayout->setContentsMargins(0, 0, 0, 0);
    centralLayout->setSpacing(0);

    auto* vsplit = new QSplitter(Qt::Vertical);

    auto* splitter = new QSplitter(Qt::Horizontal);

    // Left column: mod list + download progress strip below it. The
    // strip is hidden when no download is active (DownloadProgressWidget
    // toggles its own visibility on terminal status), so the mod list
    // gets the full height in the common case.
    auto* leftCol = new QWidget;
    auto* leftLayout = new QVBoxLayout(leftCol);
    leftLayout->setContentsMargins(0, 0, 0, 0);
    leftLayout->setSpacing(0);
    m_modList = new ModListWidget(m_grpc);
    leftLayout->addWidget(m_modList, 1);
    m_downloadProgress = new DownloadProgressWidget(m_grpc);
    m_downloadProgress->setMinimumHeight(20);
    leftLayout->addWidget(m_downloadProgress);
    splitter->addWidget(leftCol);

    m_rightTabs = new QTabWidget;
    m_pluginList = new PluginListWidget;
    m_rightTabs->addTab(m_pluginList, "Plugins");

    // Data tab placeholder.
    auto* dataPlaceholder = new QWidget;
    auto* dpLayout = new QVBoxLayout(dataPlaceholder);
    auto* dpLabel = new QLabel("Virtual data directory.\nShows the merged view of all enabled mods.\n\n(Coming soon)");
    dpLabel->setAlignment(Qt::AlignCenter);
    dpLabel->setStyleSheet("color: gray;");
    dpLayout->addWidget(dpLabel);
    m_rightTabs->addTab(dataPlaceholder, "Data");

    // Single Downloads tab. Rows progress in place:
    // Downloading → Waiting → Installing → Installed. No "In Flight" split.
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

    // --- Status bar (MO2-style: game - profile | connection) ---
    // The download progress bar moved out of the status bar and into the
    // workspace's left column (see above) so it sits directly above the
    // activity log and ends at the Downloads box, instead of being a thin
    // strip lost in the status bar.
    m_statusInfo = new QLabel;
    statusBar()->addWidget(m_statusInfo, 1);

    m_connectionIndicator = new ConnectionIndicator(m_grpc);
    statusBar()->addPermanentWidget(m_connectionIndicator);

    // --- Signal connections ---
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

    // When a mod is toggled, refresh the plugin list to show/hide its plugins.
    connect(m_modList, &ModListWidget::modToggled,
            m_pluginList, &PluginListWidget::refresh);

    // When a download is installed via the library view, refresh the mod list.
    connect(m_downloadsLibrary, &DownloadsLibraryView::modInstalledFromDownload, this, [this] {
        if (m_activeGame.detected)
            m_modList->loadForGame(m_activeGame, m_currentProfile);
    });
    // FOMOD-wizard "open/close" notifications used to drive the
    // InstallStatusBanner's bring-to-front affordance. With the banner
    // gone the notifications fall on the floor — the wizard is its own
    // modal window the user can refocus directly.
}

void MainWindow::loadManagedGames()
{
    auto managedIds = m_config.managedGames();
    auto allDetected = GameDetector::detectAll();

    m_managedGames.clear();
    for (uint32_t id : managedIds) {
        auto game = GameInfo::findIn(allDetected, id);
        if (game)
            m_managedGames.push_back(*game);
    }

    m_gameSelector->setGames(m_managedGames);

    uint32_t activeId = m_config.activeGameAppId();
    m_gameSelector->setActiveGame(activeId);

    // Explicitly trigger game load for the restored selection.
    auto current = m_gameSelector->currentGame();
    if (current.detected)
        onGameChanged(current.appId);
}

void MainWindow::onGameChanged(uint32_t appId)
{
    m_config.setActiveGameAppId(appId);

    auto found = GameInfo::findIn(m_managedGames, appId);
    m_activeGame = found.value_or(GameInfo{});

    m_runButton->setGame(m_activeGame, m_config.lastToolFor(m_activeGame.shortName));
    m_pluginList->setModsDir(modsDirectoryFor(m_activeGame.shortName));
    m_pluginList->loadForGame(m_activeGame);

    if (m_activeGame.detected) {
        // Ensure mod directory exists on-demand (only for detected games).
        QString modsDir = modsDirectoryFor(m_activeGame.shortName);
        QDir().mkpath(modsDir);

        // Pre-seed the current profile from AppConfig so everything downstream
        // (mod list, plugin list, status bar) renders on the right profile the
        // first time, instead of showing "Default" for a flash and then
        // redrawing when the profile selector resolves.
        QString preferred = m_config.lastProfileFor(m_activeGame.shortName);
        if (!preferred.isEmpty())
            m_currentProfile = preferred;

        m_profileSelector->loadForGame(m_activeGame.shortName, preferred);
        m_modList->loadForGame(m_activeGame, m_currentProfile);
        if (m_downloadsLibrary)
            m_downloadsLibrary->setGame(m_activeGame);

        // Hook up per-game archive + install streams so the Downloads tab
        // receives progress ticks without a poll loop. The client cancels
        // any prior subscription under the hood.
        m_grpc->subscribeEvents(m_activeGame.shortName);
    }

    updateStatusBarInfo();
}

void MainWindow::onProfileChanged(const QString& profileName)
{
    m_currentProfile = profileName;
    if (m_activeGame.detected)
        m_config.setLastProfileFor(m_activeGame.shortName, profileName);
    m_modList->loadForGame(m_activeGame, profileName);
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
        // Button says "Install xNVSE..." — hand off to the daemon's
        // Nexus-backed installer, then rebuild the combo so the next click
        // runs the newly-installed tool.
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

    // The daemon auto-mounts VFS before launching. The frontend just sends the RPC.
    if (m_grpc->isConnected()) {
        bool useTool = (target.type == RunButtonWidget::TargetTool);
        statusBar()->showMessage(
            useTool ? QString("Preparing mods and launching %1...").arg(target.label)
                    : QString("Preparing mods and launching %1...").arg(m_activeGame.name));
        m_grpc->launchGame(m_activeGame.shortName, useTool, m_currentProfile);
    } else {
        // Fallback: direct Steam launch (no mods).
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
        // Refresh mod list.
        m_modList->loadForGame(m_activeGame, m_currentProfile);
    }
}

void MainWindow::onOpenSettings()
{
    SettingsDialog dlg(m_grpc, &m_config, this);
    dlg.exec();
    // The dialog's theme combo writes directly to m_config; refresh the View
    // → Theme menu's check state so it stays in sync if the user reopens it.
    if (m_themeActions) {
        QString current = m_config.preferredStyle();
        if (current.isEmpty())
            current = "Default";
        for (auto* a : m_themeActions->actions())
            a->setChecked(a->text() == current);
    }
}

void MainWindow::onAddNewGame()
{
    // Walks the set of Steam-detected Bethesda titles the user hasn't
    // already added. Nothing happens to the managed list until the user
    // picks one and confirms — the Games dropdown never side-effects
    // add/remove on its own.
    auto allDetected = GameDetector::detectAll();
    auto managed = m_config.managedGames();
    QSet<uint32_t> managedSet(managed.begin(), managed.end());

    QStringList labels;
    std::vector<GameInfo> candidates;
    for (const auto& g : allDetected) {
        if (managedSet.contains(g.appId))
            continue;
        candidates.push_back(g);
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

    managed.push_back(chosen.appId);
    m_config.setManagedGames(managed);

    // Refresh from the daemon so directories (Downloads/, etc.) get
    // materialized and the combo picks up the new entry.
    if (m_grpc->isConnected())
        m_grpc->detectGames();
    else
        loadManagedGames();

    m_config.setActiveGameAppId(chosen.appId);
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

void MainWindow::onGameLaunched(int pid)
{
    statusBar()->showMessage(QString("Game launched (PID %1)").arg(pid), 5000);
}

void MainWindow::onGameLaunchFailed(const QString& error)
{
    // The daemon emits machine-parseable tokens for surfaceable conditions.
    // Format: "loader_missing:<reason>:<configured-exe>:<install-path>:<gameID>".
    // Break it out into a user-friendly dialog instead of dumping the raw
    // FailedPrecondition string — when the user sees "Bethesda launcher"
    // instead of xNVSE, the actionable next step is a reinstall.
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

    QMessageBox::warning(this, "Launch Failed", error);
    updateStatusBarInfo();
}

void MainWindow::onRpcError(const QString& method, const QString& error)
{
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

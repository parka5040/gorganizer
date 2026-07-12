#include "TTWInstallDialog.h"
#include "Dialogs.h"

#include <QApplication>
#include <QButtonGroup>
#include <QCheckBox>
#include <QClipboard>
#include <QCloseEvent>
#include <QDateTime>
#include <QDir>
#include <QFileDialog>
#include <QFileInfo>
#include <QFontDatabase>
#include <QFormLayout>
#include <QHBoxLayout>
#include <QLabel>
#include <QLineEdit>
#include <QListWidget>
#include <QListWidgetItem>
#include <QMessageBox>
#include <QPlainTextEdit>
#include <QProgressBar>
#include <QPushButton>
#include <QRadioButton>
#include <QRegularExpression>
#include <QScrollArea>
#include <QStackedWidget>
#include <QStandardPaths>
#include <QTimer>
#include <QToolButton>
#include <QVBoxLayout>
#include <QWidget>

namespace gorganizer {

namespace {

constexpr int PAGE_BACKEND   = 0;
constexpr int PAGE_PREREQS   = 1;
constexpr int PAGE_MPI       = 2;
constexpr int PAGE_CONFIGURE = 3;
constexpr int PAGE_RUN       = 4;
constexpr int PAGE_LAUNCHER  = 5;

QString prereqRow(bool ok, const QString& label)
{
    return QString("%1  %2").arg(ok ? QStringLiteral("\xE2\x9C\x85")
                                    : QStringLiteral("\xE2\x9D\x8C"),
                                  label);
}

}

TTWInstallDialog::TTWInstallDialog(GrpcClient* grpc, QString fnvShortName,
                                   QString currentProfile, QWidget* parent)
    : QDialog(parent),
      m_grpc(grpc),
      m_fnvShortName(std::move(fnvShortName)),
      m_currentProfile(std::move(currentProfile))
{
    setWindowTitle("Install Tale of Two Wastelands");
    resize(720, 560);
    setModal(true);

    auto* root = new QVBoxLayout(this);

    m_stack = new QStackedWidget;
    m_stack->addWidget(buildBackendPage());
    m_stack->addWidget(buildPrereqsPage());
    m_stack->addWidget(buildMpiPage());
    m_stack->addWidget(buildConfigurePage());
    m_stack->addWidget(buildRunPage());
    m_stack->addWidget(buildLauncherPage());
    root->addWidget(m_stack, 1);

    auto* nav = new QHBoxLayout;
    m_backBtn = new QPushButton("Back");
    m_nextBtn = new QPushButton("Next");
    m_cancelBtn = new QPushButton("Cancel");
    nav->addWidget(m_cancelBtn);
    nav->addStretch(1);
    nav->addWidget(m_backBtn);
    nav->addWidget(m_nextBtn);
    root->addLayout(nav);

    connect(m_cancelBtn, &QPushButton::clicked, this, &TTWInstallDialog::reject);
    connect(m_backBtn, &QPushButton::clicked, this, [this]() {
        int idx = m_stack->currentIndex();
        if (idx <= 0) return;
        m_stack->setCurrentIndex(idx - 1);
        if (m_stack->currentIndex() == PAGE_PREREQS)
            onRefreshPrereqs();
        setNavButtons(m_stack->currentIndex() > 0, true);
    });
    connect(m_nextBtn, &QPushButton::clicked, this, [this]() {
        int idx = m_stack->currentIndex();
        switch (idx) {
            case PAGE_BACKEND:
                m_stack->setCurrentIndex(PAGE_PREREQS);
                onRefreshPrereqs();
                setNavButtons(true, m_lastPrereqsAllGreen);
                break;
            case PAGE_PREREQS:
                m_stack->setCurrentIndex(PAGE_MPI);
                setNavButtons(true, !m_mpiPath.isEmpty());
                break;
            case PAGE_MPI:
                onConfigure();
                break;
            case PAGE_CONFIGURE:
                m_stack->setCurrentIndex(PAGE_RUN);
                setNavButtons(false, false, "Install");
                m_nextBtn->setVisible(false);
                m_runStartBtn->setEnabled(true);
                break;
            case PAGE_RUN:
                break;
            case PAGE_LAUNCHER:
                onActivate();
                break;
        }
    });

    if (m_grpc) {
        connect(m_grpc, &GrpcClient::daemonInfo,
                this, &TTWInstallDialog::onDaemonInfo);
    }

    m_stack->setCurrentIndex(PAGE_BACKEND);
    setNavButtons(false, true);
}

void TTWInstallDialog::closeEvent(QCloseEvent* ev)
{
    if (m_installRunning) {
        if (!dialogs::confirmWarn(this, "Cancel install?",
            "The TTW installer is still running. Cancelling now will kill the "
            "Wine process tree (Backend A) or the native installer (Backend B).\n\n"
            "Cancel install?",
            QMessageBox::No)) {
            ev->ignore();
            return;
        }
        onCancelInstaller();
    }
    QDialog::closeEvent(ev);
}

void TTWInstallDialog::reject()
{
    if (m_installRunning) {
        if (!dialogs::confirmWarn(this, "Cancel install?",
            "The TTW installer is still running. Cancel?",
            QMessageBox::No)) return;
        onCancelInstaller();
    }
    QDialog::reject();
}

QWidget* TTWInstallDialog::buildBackendPage()
{
    auto* page = new QWidget;
    auto* lay = new QVBoxLayout(page);

    auto* title = new QLabel("<h3>Choose installer</h3>");
    title->setTextFormat(Qt::RichText);
    lay->addWidget(title);

    auto* group = new QButtonGroup(this);

    m_radioNative = new QRadioButton(
        "Native MPI installer (recommended) — ~2 minutes, no Wine setup");
    m_radioNative->setChecked(true);
    m_radioNative->setToolTip(
        "Uses SulfurNitride/TTW_Linux_Installer (a native Rust binary).\n"
        "No .NET, no protontricks, Steam doesn't need to be running.\n"
        "Faster and more reliable for non-technical users.");
    auto* nativeNote = new QLabel(
        "<small>Uses <a href=\"https://github.com/SulfurNitride/TTW_Linux_Installer\">"
        "SulfurNitride/TTW_Linux_Installer</a>. The mpi_installer binary will be "
        "downloaded and verified if not on your system.</small>");
    nativeNote->setTextFormat(Qt::RichText);
    nativeNote->setOpenExternalLinks(true);
    nativeNote->setWordWrap(true);

    m_radioWine = new QRadioButton(
        "Official TTW Install.exe under Wine — 40 min – several hours");
    m_radioWine->setToolTip(
        "Closed-source Windows installer. Requires .NET 4.8, vcrun2022, etc.\n"
        "in your Fallout: New Vegas Proton prefix. Steam must be running.");
    auto* wineNote = new QLabel(
        "<small>Requires .NET Framework 4.8, Visual C++ 2015–2022 redistributables, "
        "and other components in your Fallout: New Vegas Proton prefix. Steam "
        "must be running.</small>");
    wineNote->setTextFormat(Qt::RichText);
    wineNote->setWordWrap(true);

    group->addButton(m_radioNative);
    group->addButton(m_radioWine);

    lay->addWidget(m_radioNative);
    lay->addWidget(nativeNote);
    lay->addSpacing(8);
    lay->addWidget(m_radioWine);
    lay->addWidget(wineNote);
    lay->addStretch(1);

    connect(m_radioNative, &QRadioButton::toggled, this, &TTWInstallDialog::onBackendChanged);
    connect(m_radioWine, &QRadioButton::toggled, this, &TTWInstallDialog::onBackendChanged);

    return page;
}

void TTWInstallDialog::onBackendChanged()
{
}

int TTWInstallDialog::currentBackend() const
{
    return m_radioWine && m_radioWine->isChecked() ? GrpcTTWBackendWine
                                                   : GrpcTTWBackendNative;
}

QWidget* TTWInstallDialog::buildPrereqsPage()
{
    auto* page = new QWidget;
    auto* lay = new QVBoxLayout(page);

    auto* title = new QLabel("<h3>Pre-flight checks</h3>");
    title->setTextFormat(Qt::RichText);
    lay->addWidget(title);

    m_prereqList = new QListWidget;
    lay->addWidget(m_prereqList, 1);

    auto* row = new QHBoxLayout;
    m_refreshPrereqsBtn = new QPushButton("Refresh");
    m_installMissingBtn = new QPushButton("Install missing components");
    m_bootstrapBtn = new QPushButton("Bootstrap FNV prefix");
    m_uninstallMonoBtn = new QPushButton("Uninstall Wine Mono");
    row->addWidget(m_refreshPrereqsBtn);
    row->addStretch(1);
    row->addWidget(m_bootstrapBtn);
    row->addWidget(m_uninstallMonoBtn);
    row->addWidget(m_installMissingBtn);
    lay->addLayout(row);

    connect(m_refreshPrereqsBtn, &QPushButton::clicked,
            this, &TTWInstallDialog::onRefreshPrereqs);
    connect(m_installMissingBtn, &QPushButton::clicked,
            this, &TTWInstallDialog::onInstallMissing);
    connect(m_bootstrapBtn, &QPushButton::clicked,
            this, &TTWInstallDialog::onBootstrapPrefix);
    connect(m_uninstallMonoBtn, &QPushButton::clicked,
            this, &TTWInstallDialog::onUninstallMono);

    m_bootstrapBtn->setVisible(false);
    m_uninstallMonoBtn->setVisible(false);

    return page;
}

void TTWInstallDialog::onRefreshPrereqs()
{
    if (!m_grpc) return;
    GrpcTTWPrereqStatus st;
    QString err;
    if (!m_grpc->checkTTWPrereqs(currentBackend(), st, err)) {
        dialogs::warn(this, "Pre-flight failed",
            QString("CheckTTWPrereqs RPC failed: %1").arg(err));
        return;
    }
    renderPrereqs(st);
}

void TTWInstallDialog::renderPrereqs(const GrpcTTWPrereqStatus& st)
{
    m_prereqList->clear();
    bool allGreen = true;

    auto addRow = [&](bool ok, const QString& label) {
        m_prereqList->addItem(prereqRow(ok, label));
        if (!ok) allGreen = false;
    };

    addRow(st.fnvVanilla, "Fallout: New Vegas Data/ is unmodded (no active VFS mount)");
    addRow(st.gstreamerInstalled,
           QString("GStreamer codecs (in-game music). Hint: %1").arg(st.gstreamerCodecsHint));
    addRow(st.xdeltaInstalled, "xdelta3 on PATH");
    addRow(st.diskSpaceAvailable >= st.diskSpaceRequired,
           QString("Disk space: %1 free of %2 required")
               .arg(humanBytes(st.diskSpaceAvailable))
               .arg(humanBytes(st.diskSpaceRequired)));

    if (st.backend == GrpcTTWBackendNative) {
        addRow(!st.mpiInstallerPath.isEmpty(),
               st.mpiInstallerPath.isEmpty()
                 ? "mpi_installer not found (will download on Install)"
                 : QString("mpi_installer @ %1 (%2)")
                     .arg(st.mpiInstallerPath, st.mpiInstallerVersion));
        m_bootstrapBtn->setVisible(false);
        m_uninstallMonoBtn->setVisible(false);
    } else {
        addRow(st.steamRunning, "Steam is running");
        addRow(st.prefixExists, "FNV Proton prefix exists");
        addRow(!st.monoNeedsRemoval, "Wine Mono uninstalled (must be gone before .NET 4.8)");
        addRow(st.hasDotnet48,
               st.hasDotnet48
                 ? QString(".NET Framework 4.8 (release %1)").arg(st.dotnet48ReleaseRev)
                 : QString(".NET Framework 4.8 (release rev %1)").arg(st.dotnet48ReleaseRev));
        addRow(st.hasVcrun2022, "Visual C++ 2015–2022 redistributables");
        addRow(st.hasMsxml6, "MSXML 6 (defensive)");
        addRow(st.hasCorefonts, "Microsoft Core Fonts (defensive)");
        addRow(st.protontricksAvailable, "protontricks (Flatpak / pipx) available on host");
        addRow(st.winetricksAvailable, "winetricks available on host");

        m_bootstrapBtn->setVisible(!st.prefixExists);
        m_uninstallMonoBtn->setVisible(st.monoNeedsRemoval);
    }

    if (!st.missing.isEmpty()) {
        auto* item = new QListWidgetItem(
            QString("Blocking: %1").arg(st.missing.join(", ")));
        QFont f = item->font();
        f.setBold(true);
        item->setFont(f);
        m_prereqList->addItem(item);
    }

    m_lastPrereqsAllGreen = allGreen;
    m_installMissingBtn->setEnabled(!allGreen);
    setNavButtons(true, allGreen);
}

void TTWInstallDialog::onInstallMissing()
{
    if (!m_grpc) return;
    if (currentBackend() == GrpcTTWBackendNative) {
        QString path, version, err;
        if (!m_grpc->ensureNativeMpiInstaller(path, version, err)) {
            dialogs::warn(this, "Could not install mpi_installer", err);
            return;
        }
        dialogs::info(this, "Installed",
            QString("mpi_installer is now at:\n%1\n\nVersion: %2").arg(path, version));
        onRefreshPrereqs();
        return;
    }
    QString id, err;
    if (!m_grpc->installTTWPrereqs(id, err)) {
        dialogs::warn(this, "Could not install prereqs", err);
        return;
    }
    appendLog(QString("[%1] protontricks started — watch the log tail").arg(id));
    dialogs::info(this, "Install started",
        "Protontricks is installing .NET 4.8 + vcrun2022 + msxml6 + corefonts in "
        "the FNV prefix. Watch the activity log for completion (this can take "
        "5–15 minutes), then click Refresh to re-check.");
}

void TTWInstallDialog::onBootstrapPrefix()
{
    if (!m_grpc) return;
    QString err;
    if (!m_grpc->bootstrapFNVPrefix(err)) {
        dialogs::warn(this, "Bootstrap failed", err);
        return;
    }
    dialogs::info(this, "Prefix bootstrapped",
        "FNV's Proton prefix is now materialized. Refresh to continue.");
    onRefreshPrereqs();
}

void TTWInstallDialog::onUninstallMono()
{
    dialogs::info(this, "Uninstall Wine Mono",
        "Run `protontricks 22380 wine uninstaller --remove` and uninstall any "
        "Wine Mono entry from the Add/Remove dialog, then click Refresh. "
        "Automating this step from gorganizer is unsafe because Wine's uninstall "
        "GUI is the only reliable path.");
}

QWidget* TTWInstallDialog::buildMpiPage()
{
    auto* page = new QWidget;
    auto* lay = new QVBoxLayout(page);

    auto* title = new QLabel("<h3>Locate Tale of Two Wastelands installer</h3>");
    title->setTextFormat(Qt::RichText);
    lay->addWidget(title);

    auto* note = new QLabel(
        "Pick the <code>.mpi</code> file you downloaded from "
        "<a href=\"https://taleoftwowastelands.com/dl_ttw\">taleoftwowastelands.com</a>. "
        "For Backend A (Wine), keep <code>TTW Install.exe</code> in the same folder.");
    note->setTextFormat(Qt::RichText);
    note->setOpenExternalLinks(true);
    note->setWordWrap(true);
    lay->addWidget(note);

    auto* row = new QHBoxLayout;
    m_mpiLineEdit = new QLineEdit;
    m_mpiLineEdit->setPlaceholderText("Path to .mpi file");
    auto* browseBtn = new QPushButton("Browse...");
    row->addWidget(m_mpiLineEdit, 1);
    row->addWidget(browseBtn);
    lay->addLayout(row);

    auto* altLabel = new QLabel(
        "If multiple .mpi files were found, pick one:");
    lay->addWidget(altLabel);

    m_alternateMpiList = new QListWidget;
    m_alternateMpiList->setMaximumHeight(120);
    lay->addWidget(m_alternateMpiList);

    lay->addStretch(1);

    connect(browseBtn, &QPushButton::clicked, this, &TTWInstallDialog::onPickMpi);
    connect(m_alternateMpiList, &QListWidget::itemSelectionChanged, this, [this]() {
        if (auto* item = m_alternateMpiList->currentItem()) {
            m_mpiLineEdit->setText(item->text());
            m_mpiPath = item->text();
            setNavButtons(true, true);
        }
    });
    connect(m_mpiLineEdit, &QLineEdit::textChanged, this, [this](const QString& t) {
        m_mpiPath = t.trimmed();
        setNavButtons(true, !m_mpiPath.isEmpty());
    });

    return page;
}

void TTWInstallDialog::onPickMpi()
{
    QString downloads = QStandardPaths::writableLocation(QStandardPaths::DownloadLocation);
    QString picked = QFileDialog::getOpenFileName(this, "Pick TTW .mpi file",
        downloads, "TTW installer (*.mpi *.exe);;All files (*)");
    if (picked.isEmpty()) return;

    if (!m_grpc) return;
    GrpcTTWInstallerInfo info;
    QString err;
    if (!m_grpc->prepareTTWInstaller(picked, currentBackend(), info, err)) {
        dialogs::warn(this, "Could not resolve .mpi", err);
        return;
    }

    m_mpiPath = info.mpiFile;
    m_installerExe = info.installerExe;
    m_installVersion = info.version;
    m_alternateMpis = info.alternateMpis;

    m_mpiLineEdit->setText(info.mpiFile);
    m_alternateMpiList->clear();
    if (!info.mpiFile.isEmpty())
        m_alternateMpiList->addItem(info.mpiFile);
    for (const QString& alt : info.alternateMpis)
        m_alternateMpiList->addItem(alt);

    setNavButtons(true, true);
}

QWidget* TTWInstallDialog::buildConfigurePage()
{
    auto* page = new QWidget;
    auto* lay = new QVBoxLayout(page);

    auto* title = new QLabel("<h3>Configure destination</h3>");
    title->setTextFormat(Qt::RichText);
    lay->addWidget(title);

    auto* nameRow = new QHBoxLayout;
    nameRow->addWidget(new QLabel("Mod name:"));
    m_modNameEdit = new QLineEdit("Tale of Two Wastelands");
    nameRow->addWidget(m_modNameEdit, 1);
    lay->addLayout(nameRow);

    m_summaryLabel = new QLabel;
    m_summaryLabel->setWordWrap(true);
    m_summaryLabel->setTextInteractionFlags(Qt::TextSelectableByMouse);
    lay->addWidget(m_summaryLabel);

    auto* form = new QFormLayout;
    m_pathsForm = form;
    lay->addLayout(form);

    lay->addStretch(1);

    connect(m_modNameEdit, &QLineEdit::textChanged, this, [this](const QString& t) {
        m_modName = t.trimmed();
        setNavButtons(true, !m_modName.isEmpty());
    });

    return page;
}

void TTWInstallDialog::onConfigure()
{
    if (!m_grpc) return;
    if (m_modName.isEmpty()) {
        dialogs::info(this, "Mod name required",
            "Please enter a name for the TTW mod folder.");
        return;
    }

    QString modDir, err;
    if (!m_grpc->createBlankTTWMod(m_modName, modDir, err)) {
        if (err.contains("mod_collision", Qt::CaseInsensitive)) {
            if (!dialogs::confirm(this, "Replace existing TTW mod?",
                QString("A mod folder named %1 already exists. Replace it?").arg(m_modName),
                QMessageBox::No)) return;
            QString uerr;
            std::vector<QString> flagged;
            if (!m_grpc->uninstallMod("ttw", m_modName, true, flagged, uerr)) {
                dialogs::warn(this, "Could not remove existing mod", uerr);
                return;
            }
            if (!m_grpc->createBlankTTWMod(m_modName, modDir, err)) {
                dialogs::warn(this, "Could not create TTW mod folder", err);
                return;
            }
        } else {
            dialogs::warn(this, "Could not create TTW mod folder", err);
            return;
        }
    }

    while (m_pathsForm->rowCount() > 0)
        m_pathsForm->removeRow(0);

    auto addCopyRow = [this](const QString& label, const QString& path) {
        auto* lineEdit = new QLineEdit(path);
        lineEdit->setReadOnly(true);
        auto* copyBtn = new QPushButton("Copy");
        connect(copyBtn, &QPushButton::clicked, [path]() {
            QApplication::clipboard()->setText(path);
        });
        QPushButton* winePathBtn = nullptr;
        if (currentBackend() == GrpcTTWBackendWine) {
            winePathBtn = new QPushButton("Copy (Wine path)");
            connect(winePathBtn, &QPushButton::clicked, [this, path]() {
                if (!m_grpc) return;
                QString winePath, err;
                if (m_grpc->translateWinePath(m_fnvShortName, path, winePath, err) && !winePath.isEmpty())
                    QApplication::clipboard()->setText(winePath);
                else
                    QApplication::clipboard()->setText(path);
            });
        }
        auto* row = new QHBoxLayout;
        row->addWidget(lineEdit, 1);
        if (winePathBtn) row->addWidget(winePathBtn);
        row->addWidget(copyBtn);
        auto* container = new QWidget;
        container->setLayout(row);
        m_pathsForm->addRow(label, container);
    };

    addCopyRow("TTW destination:", modDir);

    if (currentBackend() == GrpcTTWBackendNative) {
        m_summaryLabel->setText(
            QString("<p>The native installer will run with these paths. "
                    "Backend B doesn't need any pasting — the daemon hands "
                    "the paths to mpi_installer directly.</p>"));
    } else {
        m_summaryLabel->setText(
            "<p><b>Backend A — Wine.</b> The TTW installer GUI will open in a "
            "separate window. When it asks for paths, click <b>Copy (Wine path)</b> "
            "next to each row, then paste (Ctrl+V) into the installer's path field.</p>");
    }

    m_stack->setCurrentIndex(PAGE_CONFIGURE);
    setNavButtons(true, true, "Install");
}

QWidget* TTWInstallDialog::buildRunPage()
{
    auto* page = new QWidget;
    auto* lay = new QVBoxLayout(page);

    auto* title = new QLabel("<h3>Running installer</h3>");
    title->setTextFormat(Qt::RichText);
    lay->addWidget(title);

    m_runElapsedLabel = new QLabel("Click <b>Install</b> to begin.");
    m_runElapsedLabel->setTextFormat(Qt::RichText);
    lay->addWidget(m_runElapsedLabel);

    m_runProgress = new QProgressBar;
    m_runProgress->setRange(0, 0);
    m_runProgress->setTextVisible(true);
    lay->addWidget(m_runProgress);

    m_runStatusLine = new QLabel(QStringLiteral("…waiting for installer to start"));
    m_runStatusLine->setWordWrap(true);
    m_runStatusLine->setStyleSheet("font-weight: bold;");
    lay->addWidget(m_runStatusLine);

    m_runLog = new QPlainTextEdit;
    m_runLog->setReadOnly(true);
    m_runLog->setMaximumBlockCount(2000);
    m_runLog->setLineWrapMode(QPlainTextEdit::NoWrap);
    {
        QFont mono = QFontDatabase::systemFont(QFontDatabase::FixedFont);
        m_runLog->setFont(mono);
    }
    lay->addWidget(m_runLog, 1);

    auto* row = new QHBoxLayout;
    m_runStartBtn = new QPushButton("Start");
    m_runCancelBtn = new QPushButton("Cancel installer");
    m_runCancelBtn->setEnabled(false);
    row->addWidget(m_runStartBtn);
    row->addStretch(1);
    row->addWidget(m_runCancelBtn);
    lay->addLayout(row);

    connect(m_runStartBtn, &QPushButton::clicked, this, &TTWInstallDialog::onRunInstaller);
    connect(m_runCancelBtn, &QPushButton::clicked, this, &TTWInstallDialog::onCancelInstaller);

    m_runTicker = new QTimer(this);
    m_runTicker->setInterval(1000);
    connect(m_runTicker, &QTimer::timeout, this, [this]() {
        if (m_runStartedMsecs == 0) return;
        qint64 elapsedMs = QDateTime::currentMSecsSinceEpoch() - m_runStartedMsecs;
        int s = int(elapsedMs / 1000);
        int mm = s / 60;
        int ss = s % 60;
        m_runElapsedLabel->setText(
            QString("<b>Elapsed:</b> %1:%2 — installer is running. "
                    "Backend B (native) typically takes ~2 minutes; "
                    "Backend A (Wine) can take 40+ minutes.")
                .arg(mm, 2, 10, QChar('0')).arg(ss, 2, 10, QChar('0')));
    });

    return page;
}

void TTWInstallDialog::onRunInstaller()
{
    if (!m_grpc) return;
    if (m_installRunning) return;

    GrpcTTWInstallerInfo info;
    info.backend = currentBackend();
    info.mpiFile = m_mpiPath;
    info.installerExe = m_installerExe;
    info.version = m_installVersion;

    QString id, err;
    if (!m_grpc->launchTTWInstaller(info, m_modName, id, err)) {
        dialogs::warn(this, "Could not launch installer", err);
        return;
    }
    m_inFlightInstallId = id;
    m_installRunning = true;
    m_runStartedMsecs = QDateTime::currentMSecsSinceEpoch();
    m_runStartBtn->setEnabled(false);
    m_runCancelBtn->setEnabled(true);
    m_runProgress->setRange(0, 0);
    m_runStatusLine->setText("Starting installer...");
    m_runElapsedLabel->setText(
        QString("<b>Elapsed:</b> 00:00 — installer is starting (id <code>%1</code>).")
            .arg(id));
    appendLog(QString("[%1] installer started").arg(id));
    if (m_runTicker) m_runTicker->start();
}

void TTWInstallDialog::onCancelInstaller()
{
    if (!m_grpc || m_inFlightInstallId.isEmpty()) {
        m_installRunning = false;
        if (m_runTicker) m_runTicker->stop();
        return;
    }
    QString err;
    if (!m_grpc->cancelTTWInstaller(m_inFlightInstallId, err)) {
        dialogs::warn(this, "Cancel failed", err);
        return;
    }
    appendLog(QString("[%1] cancel issued").arg(m_inFlightInstallId));
    m_runCancelBtn->setEnabled(false);
    m_runStatusLine->setText("Cancelling...");
}

void TTWInstallDialog::onDaemonInfo(const QString& info)
{
    if (m_stack->currentIndex() != PAGE_RUN && m_stack->currentIndex() != PAGE_PREREQS)
        return;

    if (m_installRunning && !m_inFlightInstallId.isEmpty()
        && !info.contains(m_inFlightInstallId)) {
        if (m_stack->currentIndex() == PAGE_PREREQS)
            appendLog(info);
        return;
    }

    appendLog(info);

    static const QRegularExpression tagRe(
        QStringLiteral("^\\[([^:\\]]+):([^\\]]+)\\]\\s*(.*)$"));
    QString kind, payload;
    auto m = tagRe.match(info);
    if (m.hasMatch()) {
        kind = m.captured(2);
        payload = m.captured(3);
    } else {
        payload = info;
    }

    if (!payload.isEmpty()
        && kind != "tick" && kind != "start" && kind != "exit") {
        m_runStatusLine->setText(payload);
    }

    if (kind == "start") {
        m_runStatusLine->setText(QString("Started: %1").arg(payload));
        return;
    }

    if (kind == "tick") {
        return;
    }

    if (kind == "exit") {
        if (m_runTicker) m_runTicker->stop();
        m_installRunning = false;
        m_lastFinishedInstallId = m_inFlightInstallId;
        m_inFlightInstallId.clear();

        bool ok = payload.contains(QStringLiteral("code=0"));
        if (ok) {
            m_runProgress->setRange(0, 100);
            m_runProgress->setValue(100);
            m_runStatusLine->setText(QString("Done. %1").arg(payload));
            m_runElapsedLabel->setText(
                QString("<b>Install complete.</b> %1").arg(payload));
        } else {
            m_runProgress->setRange(0, 100);
            m_runProgress->setValue(0);
            m_runStatusLine->setText(QString("Failed. %1").arg(payload));
            m_runElapsedLabel->setText(
                QString("<b>Install failed.</b> %1 — see log above.").arg(payload));
            m_runStartBtn->setVisible(true);
            m_runStartBtn->setEnabled(true);
            m_runCancelBtn->setEnabled(false);
            return;
        }

        m_runStartBtn->setVisible(false);
        m_runCancelBtn->setEnabled(false);

        m_stack->setCurrentIndex(PAGE_LAUNCHER);
        m_nextBtn->setVisible(true);
        setNavButtons(false, true, "Activate");

        populateLauncherCandidates();
        return;
    }

    static const QRegularExpression pctRe(
        QStringLiteral("(\\d{1,3})\\s*%"));
    auto pm = pctRe.match(payload);
    if (pm.hasMatch()) {
        bool conv = false;
        int pct = pm.captured(1).toInt(&conv);
        if (conv && pct >= 0 && pct <= 100) {
            if (m_runProgress->maximum() != 100)
                m_runProgress->setRange(0, 100);
            m_runProgress->setValue(pct);
        }
    }
}

void TTWInstallDialog::appendLog(const QString& line)
{
    if (m_runLog)
        m_runLog->appendPlainText(line);
}

QWidget* TTWInstallDialog::buildLauncherPage()
{
    auto* page = new QWidget;
    auto* lay = new QVBoxLayout(page);

    auto* title = new QLabel("<h3>Pick launcher</h3>");
    title->setTextFormat(Qt::RichText);
    lay->addWidget(title);

    auto* note = new QLabel(
        "<p>TTW on Linux launches through <b>nvse_loader.exe</b>, the same "
        "loader you use for vanilla FNV with mods. There is <b>no separate "
        "<code>Tale of Two Wastelands.exe</code></b> on Linux — the Windows "
        "TTW installer writes one (a FOMM fork), but the native MPI "
        "installer used here doesn't, since you're managing mods through "
        "gorganizer rather than FOMM.</p>"
        "<p>If TTW Install.exe (Wine backend) wrote a custom launcher into "
        "your Fallout: New Vegas install dir, it'll show up in the list "
        "below — pick it instead of nvse_loader.exe in that case.</p>");
    note->setTextFormat(Qt::RichText);
    note->setWordWrap(true);
    lay->addWidget(note);

    m_launcherList = new QListWidget;
    lay->addWidget(m_launcherList, 1);

    auto* row = new QHBoxLayout;
    auto* browseBtn = new QPushButton("Browse...");
    row->addStretch(1);
    row->addWidget(browseBtn);
    lay->addLayout(row);

    connect(browseBtn, &QPushButton::clicked, this, &TTWInstallDialog::onPickLauncher);
    connect(m_launcherList, &QListWidget::itemSelectionChanged, this, [this]() {
        auto* item = m_launcherList->currentItem();
        if (!item) return;
        QVariant rel = item->data(Qt::UserRole);
        m_chosenLauncherRel = rel.isValid() ? rel.toString() : item->text();
    });

    return page;
}

void TTWInstallDialog::onPickLauncher()
{
    QString picked = QFileDialog::getOpenFileName(this, "Pick TTW launcher exe",
        QString(), "Loader (*.exe)");
    if (picked.isEmpty()) return;
    QFileInfo fi(picked);
    QString rel = fi.fileName();
    m_chosenLauncherRel = rel;
    auto* item = new QListWidgetItem(QString("FNV/  %1   (browsed)").arg(rel));
    item->setData(Qt::UserRole, rel);
    m_launcherList->insertItem(0, item);
    m_launcherList->setCurrentRow(0);
}

void TTWInstallDialog::onActivate()
{
    if (!m_grpc) {
        accept();
        return;
    }

    QString err;
    if (!m_grpc->setTTWLauncherExe(m_chosenLauncherRel, err)) {
        dialogs::warn(this, "Could not set launcher", err);
        return;
    }

    if (!dialogs::confirm(this, "Activate Tale of Two Wastelands now?",
        "Switch active game to Tale of Two Wastelands and mount its VFS?\n\n"
        "If FNV's VFS is currently mounted, it will be unmounted first. "
        "Saves made in vanilla FNV cannot be loaded after switching.",
        QMessageBox::Yes)) {
        m_outcome = InstalledOnly;
        accept();
        return;
    }

    m_grpc->mountVfsWithSwap("ttw", m_currentProfile);
    m_outcome = Accepted;
    accept();
}

void TTWInstallDialog::setNavButtons(bool backEnabled, bool nextEnabled,
                                     const QString& nextText)
{
    if (m_backBtn) m_backBtn->setEnabled(backEnabled);
    if (m_nextBtn) {
        m_nextBtn->setEnabled(nextEnabled);
        m_nextBtn->setText(nextText);
    }
}

QString TTWInstallDialog::humanBytes(int64_t b) const
{
    if (b < 0) return "?";
    static const char* units[] = {"B", "KB", "MB", "GB", "TB"};
    int u = 0;
    double v = static_cast<double>(b);
    while (v >= 1024.0 && u + 1 < int(sizeof(units) / sizeof(units[0]))) {
        v /= 1024.0;
        ++u;
    }
    return QString("%1 %2").arg(v, 0, 'f', 1).arg(units[u]);
}

void TTWInstallDialog::populateLauncherCandidates()
{
    m_launcherList->clear();

    QString id = m_lastFinishedInstallId.isEmpty() ? m_inFlightInstallId
                                                   : m_lastFinishedInstallId;

    GrpcTTWInstallResult result;
    QString err;
    bool fetched = false;
    if (!id.isEmpty() && m_grpc) {
        fetched = m_grpc->getTTWInstallResult(id, false, result, err);
        if (!fetched)
            appendLog(QString("[dialog] could not fetch install result: %1").arg(err));
    }

    auto isRecommendedName = [](const QString& base) {
        QString low = base.toLower();
        return low.contains("ttw") || low.contains("tale of two")
            || low == "nvse_loader.exe";
    };

    int rowsAdded = 0;
    auto addCandidate = [&](const QString& display, const QString& storeAsRel,
                            const QString& tooltip, bool dest) {
        auto* item = new QListWidgetItem(display);
        item->setData(Qt::UserRole, storeAsRel);
        item->setData(Qt::UserRole + 1, dest);
        QString tip = tooltip;
        if (isRecommendedName(QFileInfo(storeAsRel).fileName())) {
            QFont f = item->font();
            f.setBold(true);
            item->setFont(f);
            tip = "Recommended.\n" + tip;
        }
        item->setToolTip(tip);
        m_launcherList->addItem(item);
        ++rowsAdded;
    };

    if (fetched) {
        std::vector<GrpcTTWExeDelta> rooted = result.changedExesInRoot;
        std::sort(rooted.begin(), rooted.end(),
            [&](const GrpcTTWExeDelta& a, const GrpcTTWExeDelta& b) {
                bool ar = isRecommendedName(QFileInfo(a.relPath).fileName());
                bool br = isRecommendedName(QFileInfo(b.relPath).fileName());
                if (ar != br) return ar;
                return a.relPath < b.relPath;
            });
        for (const auto& d : rooted) {
            addCandidate(QString("FNV/  %1   (%2)").arg(d.relPath, d.kind),
                         d.relPath,
                         QString("Lives in the Fallout: New Vegas install dir (%1).")
                             .arg(d.kind),
                         false);
        }

        if (!result.dataModExes.empty()) {
            auto* sep = new QListWidgetItem(
                "—— TTW mod folder (advanced) ——");
            sep->setFlags(sep->flags() & ~Qt::ItemIsSelectable
                                       & ~Qt::ItemIsEnabled);
            m_launcherList->addItem(sep);
            for (const auto& d : result.dataModExes) {
                addCandidate(QString("TTW_Mods/  %1").arg(d.relPath),
                             d.relPath,
                             "Lives inside the TTW data mod folder. Pick this only "
                             "if your TTW manifest places the launcher there "
                             "rather than in FNV's install dir.",
                             true);
            }
        }
    }

    if (rowsAdded == 0) {
        addCandidate("FNV/  nvse_loader.exe",
                     "nvse_loader.exe",
                     "xNVSE's loader. This is the correct launcher for TTW "
                     "on Linux when the native MPI installer (Backend B) "
                     "was used — no separate TTW.exe is created on Linux.",
                     false);
    }

    for (int i = 0; i < m_launcherList->count(); ++i) {
        auto* it = m_launcherList->item(i);
        if (it && (it->flags() & Qt::ItemIsSelectable)) {
            m_launcherList->setCurrentRow(i);
            m_chosenLauncherRel = it->data(Qt::UserRole).toString();
            break;
        }
    }
}

}

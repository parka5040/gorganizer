#include "ModInstallDialog.h"
#include "FomodInstallerDialog.h"
#include "GrpcClient.h"
#include "InstallWorker.h"

#include <QVBoxLayout>
#include <QHBoxLayout>
#include <QProcess>
#include <QDir>
#include <QDirIterator>
#include <QFileInfo>
#include <QTemporaryDir>
#include <QMessageBox>
#include <QCoreApplication>
#include <QLineEdit>
#include <QFormLayout>
#include <QRegularExpression>
#include <QTextStream>
#include <QDateTime>
#include <QFile>
#include <QCloseEvent>

namespace gorganizer {

ModInstallDialog::ModInstallDialog(const QString& archivePath,
                                   const QString& modsDir,
                                   const QString& defaultModName,
                                   QWidget* parent)
    : QDialog(parent)
    , m_archivePath(archivePath)
    , m_modsDir(modsDir)
    , m_modName(defaultModName)
{
    setWindowTitle("Install Mod: " + defaultModName);
    setMinimumSize(500, 400);
    resize(600, 500);

    auto* layout = new QVBoxLayout(this);

    // Status area
    m_statusLabel = new QLabel("Extracting archive...");
    m_statusLabel->setStyleSheet("font-weight: bold;");
    layout->addWidget(m_statusLabel);

    m_progressBar = new QProgressBar;
    m_progressBar->setRange(0, 0); // indeterminate
    layout->addWidget(m_progressBar);

    // Tree view for choosing data root (hidden initially)
    m_treeLabel = new QLabel(
        "No \"Data\" folder detected in the archive.\n"
        "Select the folder that contains the mod's game files\n"
        "(e.g., meshes/, textures/, .esp files):");
    m_treeLabel->setWordWrap(true);
    m_treeLabel->hide();
    layout->addWidget(m_treeLabel);

    m_treeWidget = new QTreeWidget;
    m_treeWidget->setHeaderLabels({"Folder / File"});
    m_treeWidget->hide();
    layout->addWidget(m_treeWidget, 1);

    // Buttons
    m_buttons = new QDialogButtonBox;
    m_installBtn = m_buttons->addButton("Install", QDialogButtonBox::AcceptRole);
    m_installBtn->setEnabled(false);
    m_cancelBtn = m_buttons->addButton(QDialogButtonBox::Cancel);
    connect(m_installBtn, &QPushButton::clicked, this, &ModInstallDialog::onInstallClicked);
    connect(m_cancelBtn, &QPushButton::clicked, this, &ModInstallDialog::onCancelClicked);
    layout->addWidget(m_buttons);

    // Start extraction
    startExtraction();
}

ModInstallDialog::~ModInstallDialog()
{
    // Last-resort cleanup. Normal exit paths handle this — but if the
    // dialog is destroyed mid-extract or mid-install (parent torn
    // down, app quit, exception), Qt's parent-child cleanup deletes
    // the worker QProcess and QThread without first asking them to
    // stop. A QThread destroyed while still running fatally aborts
    // the process; an unkilled QProcess can be left running as a
    // detached child.

    // 1. Kill the extraction subprocess if it's still running. The
    // QProcess is parented to `this`, so Qt would delete it after this
    // dtor returns — but ~QProcess only calls terminate(), not kill(),
    // and waits a short time. Be explicit.
    if (m_extractProc && m_extractProc->state() != QProcess::NotRunning) {
        m_extractProc->kill();
        m_extractProc->waitForFinished(2000);
    }

    // 2. Stop the install worker thread cleanly. cancel() sets the
    // worker's atomic flag; the thread should exit shortly. If it
    // doesn't (rare — usually means the worker is wedged on a slow
    // filesystem op), proceed anyway and rely on the OS to reap the
    // thread when the process exits. Better than crashing.
    if (m_workerThread) {
        if (m_worker) m_worker->cancel();
        m_workerThread->quit();
        if (!m_workerThread->wait(3000)) {
            qWarning("ModInstallDialog: install worker did not stop within 3s");
        }
    }

    // 3. Best-effort temp-dir cleanup. Normal flow already does this in
    // onWorkerFinished or scanExtractedTree's FOMOD-cancel path; this
    // catches the case where the dialog dies before either runs.
    if (!m_extractDir.isEmpty()) {
        QDir(m_extractDir).removeRecursively();
    }
}

void ModInstallDialog::startExtraction()
{
    m_phase = Extracting;

    // Create temp directory for extraction.
    m_extractDir = QDir::tempPath() + "/gorganizer-extract-" +
                   QString::number(QCoreApplication::applicationPid());
    QDir().mkpath(m_extractDir);

    // Use 7z to extract (handles zip, 7z, rar uniformly).
    auto* proc = new QProcess(this);
    m_extractProc = proc;
    connect(proc, QOverload<int, QProcess::ExitStatus>::of(&QProcess::finished),
            this, &ModInstallDialog::onExtractFinished);

    // Try 7z first, fall back to unzip for basic zip files.
    QString sevenz = "7z";
    QStringList args = {"x", "-o" + m_extractDir, "-y", m_archivePath};

    m_statusLabel->setText("Extracting: " + QFileInfo(m_archivePath).fileName());

    proc->start(sevenz, args);
    if (!proc->waitForStarted(2000)) {
        // 7z not found, try bsdtar (available on most systems).
        proc->start("bsdtar", {"xf", m_archivePath, "-C", m_extractDir});
        if (!proc->waitForStarted(2000)) {
            // Last resort: unzip for .zip files
            proc->start("unzip", {"-o", m_archivePath, "-d", m_extractDir});
            if (!proc->waitForStarted(2000)) {
                m_statusLabel->setText("Error: No extraction tool found (7z, bsdtar, or unzip).");
                m_progressBar->hide();
                return;
            }
        }
    }
}

void ModInstallDialog::onExtractFinished(int exitCode)
{
    if (exitCode != 0) {
        m_statusLabel->setText("Extraction failed (exit code " + QString::number(exitCode) + ").");
        m_progressBar->hide();
        return;
    }

    m_progressBar->hide();
    scanExtractedTree();
}

void ModInstallDialog::scanExtractedTree()
{
    // FOMOD installer? If the extracted tree contains a fomod/ModuleConfig.xml,
    // run the wizard and use the user's selections to drive install instead
    // of the generic "find Data/ root" heuristic below.
    if (auto plan = FomodParser::parse(m_extractDir); plan.has_value() && !plan->isEmpty()) {
        emit fomodWizardOpened(m_archivePath, m_modName);
        FomodInstallerDialog wizard(*plan, this);
        int code = wizard.exec();
        emit fomodWizardClosed(m_archivePath);
        if (code != QDialog::Accepted) {
            // User cancelled — close the whole install dialog.
            QDir(m_extractDir).removeRecursively();
            reject();
            return;
        }
        m_fomodModulePath = plan->modulePath;
        m_detectedDataRoot = plan->modulePath; // for writeMetadata's source hint
        if (plan->legacyInfoOnly) {
            m_legacyFomodFlatCopy = true;
            m_statusLabel->setText("Legacy FOMOD: flat-copying mod files (script.cs ignored).");
        } else {
            m_fomodSelections = wizard.selectedFiles();
            m_statusLabel->setText(
                QString("FOMOD installer: %1 file/folder operation(s) selected.")
                    .arg(m_fomodSelections.size()));
        }
        // Drive the install immediately. Without this, the user has to click
        // an additional "Install" on the parent dialog after they've already
        // confirmed in the wizard — they routinely close the parent thinking
        // install completed and end up with no mod on disk.
        m_phase = Installing;
        m_installBtn->setEnabled(false);
        m_treeLabel->hide();
        m_treeWidget->hide();
        m_progressBar->setRange(0, 0);
        m_progressBar->show();
        m_statusLabel->setText("Installing " + m_modName + "… please wait");
        installFrom(m_detectedDataRoot);
        return;
    }

    // Look for a Data/ folder in the extracted tree.
    // Check common layouts:
    //   1. extractDir/Data/          — Data at root
    //   2. extractDir/ModName/Data/  — single wrapper dir with Data inside
    //   3. extractDir/ModName/       — single wrapper, no Data (content IS the data)
    //   4. extractDir/ has .esp/textures/meshes directly — root IS the data

    QDir root(m_extractDir);
    auto entries = root.entryList(QDir::Dirs | QDir::NoDotAndDotDot);

    // Check for Data/ at root.
    if (entries.contains("Data", Qt::CaseInsensitive)) {
        for (const auto& e : entries) {
            if (e.compare("Data", Qt::CaseInsensitive) == 0) {
                m_detectedDataRoot = m_extractDir + "/" + e;
                break;
            }
        }
        m_statusLabel->setText("Found Data/ folder. Ready to install.");
        m_installBtn->setEnabled(true);
        m_phase = Choosing;
        return;
    }

    // Single wrapper directory — check inside it.
    if (entries.size() == 1) {
        QString wrapper = m_extractDir + "/" + entries.first();
        QDir wrapperDir(wrapper);
        auto wrapperEntries = wrapperDir.entryList(QDir::Dirs | QDir::NoDotAndDotDot);
        if (wrapperEntries.contains("Data", Qt::CaseInsensitive)) {
            for (const auto& e : wrapperEntries) {
                if (e.compare("Data", Qt::CaseInsensitive) == 0) {
                    m_detectedDataRoot = wrapper + "/" + e;
                    break;
                }
            }
            m_statusLabel->setText("Found Data/ folder inside " + entries.first() + "/. Ready to install.");
            m_installBtn->setEnabled(true);
            m_phase = Choosing;
            return;
        }
    }

    // Check if root has game-recognizable files (.esp, .esm, meshes/, textures/, etc.)
    auto rootFiles = root.entryList(QDir::Files);
    bool hasGameFiles = false;
    for (const auto& f : rootFiles) {
        QString lower = f.toLower();
        if (lower.endsWith(".esp") || lower.endsWith(".esm") || lower.endsWith(".esl") ||
            lower.endsWith(".bsa") || lower.endsWith(".ba2")) {
            hasGameFiles = true;
            break;
        }
    }
    for (const auto& d : entries) {
        QString lower = d.toLower();
        if (lower == "textures" || lower == "meshes" || lower == "scripts" ||
            lower == "sound" || lower == "interface" || lower == "skse" ||
            lower == "nvse" || lower == "fose" || lower == "f4se") {
            hasGameFiles = true;
            break;
        }
    }

    if (hasGameFiles) {
        m_detectedDataRoot = m_extractDir;
        m_statusLabel->setText("Archive root contains game files. Ready to install.");
        m_installBtn->setEnabled(true);
        m_phase = Choosing;
        return;
    }

    // Single wrapper with game files inside.
    if (entries.size() == 1) {
        QString wrapper = m_extractDir + "/" + entries.first();
        m_detectedDataRoot = wrapper;
        m_statusLabel->setText("Using " + entries.first() + "/ as data root. Ready to install.");
        m_installBtn->setEnabled(true);
        m_phase = Choosing;
        return;
    }

    // Ambiguous — show tree and let user pick.
    m_statusLabel->setText("Could not auto-detect the data root. Please select it below:");
    m_treeLabel->show();
    m_treeWidget->show();

    // Populate tree.
    populateTree(m_extractDir, nullptr);
    m_treeWidget->expandToDepth(1);

    // Add "(Archive Root)" as a selectable option.
    auto* rootItem = new QTreeWidgetItem({"(Archive Root — use if files are directly here)"});
    rootItem->setData(0, Qt::UserRole, m_extractDir);
    rootItem->setForeground(0, QBrush(QColor(100, 149, 237)));
    m_treeWidget->insertTopLevelItem(0, rootItem);

    connect(m_treeWidget, &QTreeWidget::currentItemChanged, this, [this](QTreeWidgetItem* item) {
        if (item && !item->data(0, Qt::UserRole).toString().isEmpty()) {
            m_detectedDataRoot = item->data(0, Qt::UserRole).toString();
            m_installBtn->setEnabled(true);
        }
    });

    m_phase = Choosing;
}

void ModInstallDialog::populateTree(const QString& dir, QTreeWidgetItem* parent)
{
    QDir d(dir);
    auto entries = d.entryInfoList(QDir::Dirs | QDir::Files | QDir::NoDotAndDotDot, QDir::DirsFirst | QDir::Name);

    for (const auto& entry : entries) {
        auto* item = new QTreeWidgetItem({entry.fileName()});

        if (entry.isDir()) {
            item->setData(0, Qt::UserRole, entry.absoluteFilePath());
            // Recurse only 2 levels deep to keep it manageable.
            if (!parent || !parent->parent()) {
                populateTree(entry.absoluteFilePath(), item);
            }
        } else {
            item->setData(0, Qt::UserRole, QString()); // files not selectable as root
            item->setForeground(0, QBrush(QColor(180, 180, 180)));
        }

        if (parent)
            parent->addChild(item);
        else
            m_treeWidget->addTopLevelItem(item);
    }
}

void ModInstallDialog::onInstallClicked()
{
    if (m_detectedDataRoot.isEmpty())
        return;

    m_phase = Installing;
    m_installBtn->setEnabled(false);
    m_treeLabel->hide();
    m_treeWidget->hide();
    m_progressBar->setRange(0, 0);
    m_progressBar->show();
    m_statusLabel->setText("Installing to " + m_modName + "/...");

    installFrom(m_detectedDataRoot);
}

void ModInstallDialog::installFrom(const QString& sourceDir)
{
    m_installDestDir = m_modsDir + "/" + m_modName;
    QDir().mkpath(m_installDestDir);

    // Spin up the worker on a dedicated thread so the file-copy loop
    // doesn't pin the UI. The dialog's Cancel button can interrupt
    // mid-copy via the worker's atomic flag; partial output is wiped in
    // onWorkerFinished.
    m_worker = new InstallWorker;
    if (m_legacyFomodFlatCopy) {
        m_worker->configureLegacyFomod(m_fomodModulePath, m_installDestDir);
    } else if (!m_fomodSelections.isEmpty()) {
        m_worker->configureFomodSelections(m_fomodModulePath, m_fomodSelections,
                                           m_installDestDir);
    } else {
        m_worker->configureRecursive(sourceDir, m_installDestDir);
    }

    m_workerThread = new QThread(this);
    m_worker->moveToThread(m_workerThread);
    connect(m_workerThread, &QThread::started, m_worker, &InstallWorker::run);
    connect(m_worker, &InstallWorker::finished,
            this, &ModInstallDialog::onWorkerFinished);
    connect(m_worker, &InstallWorker::finished,
            m_workerThread, &QThread::quit);
    connect(m_workerThread, &QThread::finished,
            m_worker, &QObject::deleteLater);
    connect(m_workerThread, &QThread::finished,
            m_workerThread, &QObject::deleteLater);

    m_workerThread->start();
}

void ModInstallDialog::onCancelClicked()
{
    // During install, Cancel asks the worker to stop and removes the
    // partial mod folder in onWorkerFinished. Outside install we just
    // reject the dialog like a normal cancel.
    if (m_phase == Installing && m_worker) {
        m_phase = Cancelling;
        m_cancelBtn->setEnabled(false);
        m_statusLabel->setText("Cancelling…");
        m_worker->cancel();
        return;
    }
    QDialog::reject();
}

void ModInstallDialog::closeEvent(QCloseEvent* event)
{
    if (m_phase == Installing && m_worker) {
        m_phase = Cancelling;
        if (m_cancelBtn)
            m_cancelBtn->setEnabled(false);
        m_statusLabel->setText("Cancelling…");
        m_worker->cancel();
        event->ignore(); // wait for the worker — onWorkerFinished closes us
        return;
    }
    QDialog::closeEvent(event);
}

void ModInstallDialog::reject()
{
    if (m_phase == Installing && m_worker) {
        m_phase = Cancelling;
        if (m_cancelBtn)
            m_cancelBtn->setEnabled(false);
        m_statusLabel->setText("Cancelling…");
        m_worker->cancel();
        return; // don't actually reject — onWorkerFinished does it
    }
    QDialog::reject();
}

void ModInstallDialog::onWorkerFinished(bool ok, bool cancelled, int fileCount,
                                       const QString& err)
{
    m_worker = nullptr;       // owned by its thread; deleteLater wired up
    m_workerThread = nullptr; // ditto
    m_fileCount = fileCount;

    // Always nuke the temp extraction tree.
    QDir(m_extractDir).removeRecursively();

    if (cancelled || !ok) {
        // Wipe the half-populated destination so the user doesn't end up
        // with a broken mod folder the daemon would happily list. Bail out
        // only if the path is unmistakably under the user's mods dir, to
        // avoid a worst-case rmtree on a typo'd target.
        if (!m_installDestDir.isEmpty()) {
            QString destAbs = QDir(m_installDestDir).absolutePath();
            QString modsAbs = QDir(m_modsDir).absolutePath();
            if (destAbs.startsWith(modsAbs + "/"))
                QDir(destAbs).removeRecursively();
        }
        m_progressBar->hide();
        m_phase = Done;
        if (cancelled) {
            m_statusLabel->setText("Install cancelled.");
            reject();
        } else {
            m_statusLabel->setText(QString("Install failed: %1").arg(err));
        }
        return;
    }

    if (fileCount > 0)
        writeMetadata(m_installDestDir);

    m_progressBar->hide();
    m_phase = Done;

    if (fileCount > 0) {
        m_statusLabel->setText(
            QString("Installed %1 files to %2/").arg(fileCount).arg(m_modName));
        // Notify the daemon: register the new mod folder so every profile's
        // modlist.txt gets a (disabled) entry, the installed-archive cache
        // is dropped, and the Downloads tab flips to INSTALLED. Without this
        // the mod folder exists on disk but the daemon never sees it — so
        // plugins.txt at launch never includes the mod's ESPs.
        if (m_grpc && !m_gameId.isEmpty()) {
            QString archiveRel;
            QString modsDirNorm = QDir(m_modsDir).absolutePath();
            QString archAbs = QFileInfo(m_archivePath).absoluteFilePath();
            if (archAbs.startsWith(modsDirNorm + "/"))
                archiveRel = archAbs.mid(modsDirNorm.length() + 1);
            QString rpcErr;
            if (!m_grpc->registerManualInstall(m_gameId, m_modName, archiveRel, rpcErr)) {
                m_statusLabel->setText(
                    QString("Installed %1 files. (Daemon notify failed: %2)")
                        .arg(fileCount).arg(rpcErr));
            }
        }
        accept();
    } else {
        m_statusLabel->setText("No files were installed. The selected folder may be empty.");
    }
}

void ModInstallDialog::setDaemonContext(GrpcClient* grpc, const QString& gameId)
{
    m_grpc = grpc;
    m_gameId = gameId;
}

void ModInstallDialog::writeMetadata(const QString& modDir)
{
    // If the archive has a Nexus sidecar next to it, prefer those fields
    // over filename-regex guessing. Sidecar is a flat key: value YAML.
    QString sidecarName, sidecarVersion, sidecarCategory, sidecarModId, sidecarFileId,
            sidecarGameDomain;
    {
        QFile sidecar(m_archivePath + ".meta.yaml");
        if (sidecar.open(QIODevice::ReadOnly | QIODevice::Text)) {
            QTextStream in(&sidecar);
            while (!in.atEnd()) {
                QString line = in.readLine().trimmed();
                if (line.isEmpty() || line.startsWith('#')) continue;
                int colon = line.indexOf(':');
                if (colon < 0) continue;
                QString k = line.left(colon).trimmed();
                QString v = line.mid(colon + 1).trimmed();
                if (v.startsWith('"') && v.endsWith('"'))
                    v = v.mid(1, v.length() - 2);
                if (k == "mod_name")        sidecarName = v;
                else if (k == "version")    sidecarVersion = v;
                else if (k == "category")   sidecarCategory = v;
                else if (k == "mod_id")     sidecarModId = v;
                else if (k == "file_id")    sidecarFileId = v;
                else if (k == "game_domain") sidecarGameDomain = v;
            }
        }
    }

    // The Nexus mod page URL is only derivable when the sidecar has both
    // game_domain and mod_id. Absence → no mod_page key written → UI hides
    // the "Visit Mod Page" action.
    QString modPageUrl;
    if (!sidecarGameDomain.isEmpty() && !sidecarModId.isEmpty() && sidecarModId != "0") {
        modPageUrl = QString("https://www.nexusmods.com/%1/mods/%2")
                         .arg(sidecarGameDomain, sidecarModId);
    }

    QString displayName = sidecarName;
    QString version = sidecarVersion;
    QString category = sidecarCategory;

    // Fallback: derive from filename when the sidecar didn't provide values.
    if (version.isEmpty()) {
        QRegularExpression versionRe(R"([-_\s]v?(\d+(?:\.\d+)+)(?:[-_\s]|$))");
        auto vMatch = versionRe.match(m_modName);
        if (vMatch.hasMatch())
            version = vMatch.captured(1);
    }
    if (displayName.isEmpty()) {
        displayName = m_modName;
        displayName.remove(QRegularExpression(R"([-_\s]*v?\d+\.\d+.*)"));
        displayName.replace('_', ' ');
        displayName.replace('-', ' ');
        displayName = displayName.simplified();
        displayName.replace(QRegularExpression(R"(([a-z])([A-Z]))"), R"(\1 \2)");
        if (displayName.isEmpty())
            displayName = m_modName;
    }

    // Collect the data file listing.
    QStringList fileList;
    QDirIterator it(modDir, QDir::Files | QDir::NoDotAndDotDot, QDirIterator::Subdirectories);
    while (it.hasNext()) {
        it.next();
        QString rel = QDir(modDir).relativeFilePath(it.filePath());
        if (rel != "metadata.yaml")
            fileList.append(rel);
    }

    QFile meta(modDir + "/metadata.yaml");
    if (!meta.open(QIODevice::WriteOnly | QIODevice::Text))
        return;

    // The archive path is stored relative to {ModsDir}: if the archive lives
    // under {ModsDir}/Downloads/... use that relative form, otherwise store
    // the absolute path (for local-file installs outside the managed tree).
    QString archiveRel = m_archivePath;
    {
        QString modsDirNorm = QDir(m_modsDir).absolutePath();
        QString archAbs = QFileInfo(m_archivePath).absoluteFilePath();
        if (archAbs.startsWith(modsDirNorm + "/"))
            archiveRel = archAbs.mid(modsDirNorm.length() + 1);
    }

    QTextStream out(&meta);
    out << "# Gorganizer mod metadata — auto-generated\n";
    out << "name: \"" << displayName << "\"\n";
    out << "folder: \"" << m_modName << "\"\n";
    out << "installed: \"" << QDateTime::currentDateTime().toString(Qt::ISODate) << "\"\n";
    out << "category: \"" << category << "\"\n";
    out << "version: \"" << version << "\"\n";
    out << "enabled: false\n";
    out << "file_count: " << fileList.size() << "\n";
    if (!modPageUrl.isEmpty())
        out << "mod_page: \"" << modPageUrl << "\"\n";
    out << "source_archives:\n";
    out << "  - path: \"" << archiveRel << "\"\n";
    out << "    mod_id: " << (sidecarModId.isEmpty() ? "0" : sidecarModId) << "\n";
    out << "    file_id: " << (sidecarFileId.isEmpty() ? "0" : sidecarFileId) << "\n";
    out << "    installed_at: \"" << QDateTime::currentDateTime().toUTC().toString(Qt::ISODate) << "\"\n";
    out << "files:\n";
    for (const auto& f : fileList)
        out << "  - \"" << f << "\"\n";

    meta.close();
}

} // namespace gorganizer

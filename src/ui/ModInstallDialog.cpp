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

    m_statusLabel = new QLabel("Extracting archive...");
    m_statusLabel->setStyleSheet("font-weight: bold;");
    layout->addWidget(m_statusLabel);

    m_progressBar = new QProgressBar;
    m_progressBar->setRange(0, 0);
    layout->addWidget(m_progressBar);

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

    m_buttons = new QDialogButtonBox;
    m_installBtn = m_buttons->addButton("Install", QDialogButtonBox::AcceptRole);
    m_installBtn->setEnabled(false);
    m_cancelBtn = m_buttons->addButton(QDialogButtonBox::Cancel);
    connect(m_installBtn, &QPushButton::clicked, this, &ModInstallDialog::onInstallClicked);
    connect(m_cancelBtn, &QPushButton::clicked, this, &ModInstallDialog::onCancelClicked);
    layout->addWidget(m_buttons);

    startExtraction();
}

// Last-resort cleanup if the dialog dies mid-extract/mid-install before normal exit paths run.
ModInstallDialog::~ModInstallDialog()
{
    if (m_extractProc && m_extractProc->state() != QProcess::NotRunning) {
        m_extractProc->kill();
        m_extractProc->waitForFinished(2000);
    }

    if (m_workerThread) {
        if (m_worker) m_worker->cancel();
        m_workerThread->quit();
        if (!m_workerThread->wait(3000)) {
            qWarning("ModInstallDialog: install worker did not stop within 3s");
        }
    }

    if (!m_extractDir.isEmpty()) {
        QDir(m_extractDir).removeRecursively();
    }
}

void ModInstallDialog::startExtraction()
{
    m_phase = Extracting;

    m_extractDir = QDir::tempPath() + "/gorganizer-extract-" +
                   QString::number(QCoreApplication::applicationPid());
    QDir().mkpath(m_extractDir);

    auto* proc = new QProcess(this);
    m_extractProc = proc;
    // Merge stderr into stdout so we capture both with one read on finish; the
    // old dialog discarded 7z's output entirely, which made "exit code N"
    // failures impossible to diagnose.
    proc->setProcessChannelMode(QProcess::MergedChannels);
    connect(proc, &QProcess::finished,
            this, &ModInstallDialog::onExtractFinished);

    QStringList sevenZArgs = {"x", "-o" + m_extractDir, "-y", m_archivePath};
    m_extractToolUsed = "7z";
    m_extractArgsUsed = sevenZArgs;
    m_statusLabel->setText("Extracting: " + QFileInfo(m_archivePath).fileName());

    proc->start("7z", sevenZArgs);
    if (proc->waitForStarted(2000)) return;

    QStringList bsdtarArgs = {"xf", m_archivePath, "-C", m_extractDir};
    m_extractToolUsed = "bsdtar";
    m_extractArgsUsed = bsdtarArgs;
    proc->start("bsdtar", bsdtarArgs);
    if (proc->waitForStarted(2000)) return;

    QStringList unzipArgs = {"-o", m_archivePath, "-d", m_extractDir};
    m_extractToolUsed = "unzip";
    m_extractArgsUsed = unzipArgs;
    proc->start("unzip", unzipArgs);
    if (proc->waitForStarted(2000)) return;

    m_statusLabel->setText("Error: No extraction tool found (7z, bsdtar, or unzip).");
    m_progressBar->hide();
}

void ModInstallDialog::onExtractFinished(int exitCode, QProcess::ExitStatus status)
{
    QString output;
    if (m_extractProc)
        output = QString::fromLocal8Bit(m_extractProc->readAllStandardOutput()).trimmed();

    if (status == QProcess::CrashExit || exitCode != 0) {
        QString reason = (status == QProcess::CrashExit)
            ? QString("crashed (signal %1)").arg(exitCode)
            : QString("exit code %1").arg(exitCode);

        // Log the full failure so it's recoverable from a terminal-launched
        // GUI. Status label is space-constrained, so it gets a short summary
        // plus the last line of tool output.
        QString cmdline = m_extractToolUsed + " " + m_extractArgsUsed.join(" ");
        qWarning().noquote() << "Extraction failed:" << cmdline;
        qWarning().noquote() << "  reason:" << reason;
        if (!output.isEmpty())
            qWarning().noquote() << "  output:" << output;

        QString tail = output.section('\n', -1).trimmed();
        QString shown = QString("Extraction failed (%1, %2).").arg(m_extractToolUsed, reason);
        if (!tail.isEmpty())
            shown += "\n" + tail;
        m_statusLabel->setText(shown);
        m_progressBar->hide();
        return;
    }

    m_progressBar->hide();
    scanExtractedTree();
}

void ModInstallDialog::scanExtractedTree()
{
    if (auto plan = FomodParser::parse(m_extractDir); plan.has_value() && !plan->isEmpty()) {
        emit fomodWizardOpened(m_archivePath, m_modName);
        FomodInstallerDialog wizard(*plan, this);
        int code = wizard.exec();
        emit fomodWizardClosed(m_archivePath);
        if (code != QDialog::Accepted) {
            QDir(m_extractDir).removeRecursively();
            reject();
            return;
        }
        m_fomodModulePath = plan->modulePath;
        m_detectedDataRoot = plan->modulePath;
        if (plan->legacyInfoOnly) {
            m_legacyFomodFlatCopy = true;
            m_statusLabel->setText("Legacy FOMOD: flat-copying mod files (script.cs ignored).");
        } else {
            m_fomodSelections = wizard.selectedFiles();
            m_statusLabel->setText(
                QString("FOMOD installer: %1 file/folder operation(s) selected.")
                    .arg(m_fomodSelections.size()));
        }
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

    QDir root(m_extractDir);
    auto entries = root.entryList(QDir::Dirs | QDir::NoDotAndDotDot);

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

    if (entries.size() == 1) {
        QString wrapper = m_extractDir + "/" + entries.first();
        m_detectedDataRoot = wrapper;
        m_statusLabel->setText("Using " + entries.first() + "/ as data root. Ready to install.");
        m_installBtn->setEnabled(true);
        m_phase = Choosing;
        return;
    }

    m_statusLabel->setText("Could not auto-detect the data root. Please select it below:");
    m_treeLabel->show();
    m_treeWidget->show();

    populateTree(m_extractDir, nullptr);
    m_treeWidget->expandToDepth(1);

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
            if (!parent || !parent->parent()) {
                populateTree(entry.absoluteFilePath(), item);
            }
        } else {
            item->setData(0, Qt::UserRole, QString());
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
        event->ignore();
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
        return;
    }
    QDialog::reject();
}

void ModInstallDialog::onWorkerFinished(bool ok, bool cancelled, int fileCount,
                                       const QString& err)
{
    m_worker = nullptr;
    m_workerThread = nullptr;
    m_fileCount = fileCount;

    QDir(m_extractDir).removeRecursively();

    if (cancelled || !ok) {
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
        if (m_grpc && !m_gameId.isEmpty()) {
            QString archiveRel;
            QString modsDirNorm = QDir(m_modsDir).absolutePath();
            QString archAbs = QFileInfo(m_archivePath).absoluteFilePath();
            if (archAbs.startsWith(modsDirNorm + "/"))
                archiveRel = archAbs.mid(modsDirNorm.length() + 1);
            QString rpcErr;
            if (!m_grpc->registerManualInstall(m_gameId, m_modName, archiveRel, rpcErr)) {
                // U-5: the files are on disk, but the daemon never learned about
                // this mod — do NOT accept() (which reads as full success). Make
                // the failure loud and leave the dialog open with a clear exit so
                // the user knows to rescan/restart rather than assume it worked.
                m_statusLabel->setText(
                    QString("Copied %1 files, but notifying the daemon failed: %2")
                        .arg(fileCount).arg(rpcErr));
                QMessageBox::warning(this, "Install incomplete",
                    QString("The mod files were copied to disk, but the daemon "
                            "could not be notified:\n\n%1\n\nThe mod may not appear "
                            "or be enabled until you rescan mods or restart Gorganizer.")
                        .arg(rpcErr));
                m_cancelBtn->setEnabled(true);
                m_cancelBtn->setText("Close");
                return;
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

    QString modPageUrl;
    if (!sidecarGameDomain.isEmpty() && !sidecarModId.isEmpty() && sidecarModId != "0") {
        modPageUrl = QString("https://www.nexusmods.com/%1/mods/%2")
                         .arg(sidecarGameDomain, sidecarModId);
    }

    QString displayName = sidecarName;
    QString version = sidecarVersion;
    QString category = sidecarCategory;

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

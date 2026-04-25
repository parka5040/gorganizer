#include "ModInstallDialog.h"
#include "FomodInstallerDialog.h"
#include "GrpcClient.h"

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
    m_buttons->addButton(QDialogButtonBox::Cancel);
    connect(m_installBtn, &QPushButton::clicked, this, &ModInstallDialog::onInstallClicked);
    connect(m_buttons, &QDialogButtonBox::rejected, this, &QDialog::reject);
    layout->addWidget(m_buttons);

    // Start extraction
    startExtraction();
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
        m_statusLabel->setText("Installing to " + m_modName + "/...");
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
    QString destDir = m_modsDir + "/" + m_modName;
    QDir().mkpath(destDir);

    if (m_legacyFomodFlatCopy) {
        m_fileCount = copyLegacyFomod(m_fomodModulePath, destDir);
    } else if (!m_fomodSelections.isEmpty()) {
        m_fileCount = copyFomodSelections(m_fomodModulePath, destDir);
    } else {
        m_fileCount = copyRecursive(sourceDir, destDir);
    }

    // Generate metadata.yaml manifest.
    if (m_fileCount > 0)
        writeMetadata(destDir);

    // Clean up temp extraction dir.
    QDir(m_extractDir).removeRecursively();

    m_progressBar->hide();
    m_phase = Done;

    if (m_fileCount > 0) {
        m_statusLabel->setText(QString("Installed %1 files to %2/").arg(m_fileCount).arg(m_modName));
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
            QString err;
            if (!m_grpc->registerManualInstall(m_gameId, m_modName, archiveRel, err)) {
                // Non-fatal — surface a warning but still accept(): the user
                // already has a working mod folder; the worst case is they
                // need to enable it manually.
                m_statusLabel->setText(
                    QString("Installed %1 files. (Daemon notify failed: %2)")
                        .arg(m_fileCount).arg(err));
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

int ModInstallDialog::copyFomodSelections(const QString& modulePath, const QString& destDir)
{
    // FOMOD selections are ordered source→destination operations. Higher
    // `priority` wins on conflicts per the FOMOD spec; stable-sort so the
    // later writes land last.
    auto ops = m_fomodSelections;
    std::stable_sort(ops.begin(), ops.end(),
        [](const FomodFile& a, const FomodFile& b) { return a.priority < b.priority; });

    auto normalizeDest = [&](const FomodFile& f) -> QString {
        QString dest = f.destination;
        dest.replace('\\', '/');
        // Empty destination on a folder means "mirror folder contents at mod root".
        // Empty destination on a file means "put file at mod root under its basename".
        if (dest.isEmpty()) {
            if (f.isFolder)
                return QString();
            return QFileInfo(f.source).fileName();
        }
        // Strip leading/trailing slashes.
        while (dest.startsWith('/')) dest.remove(0, 1);
        while (dest.endsWith('/'))   dest.chop(1);
        return dest;
    };

    int count = 0;
    for (const auto& f : ops) {
        QString normSource = f.source;
        normSource.replace('\\', '/');
        QString absSource = modulePath + "/" + normSource;

        if (f.isFolder) {
            QDir srcDir(absSource);
            if (!srcDir.exists()) continue;
            QString destRoot = destDir;
            QString destSub = normalizeDest(f);
            if (!destSub.isEmpty())
                destRoot = destDir + "/" + destSub;
            QDir().mkpath(destRoot);

            QDirIterator it(absSource, QDir::Files | QDir::Dirs | QDir::NoDotAndDotDot,
                            QDirIterator::Subdirectories);
            while (it.hasNext()) {
                it.next();
                QString rel = srcDir.relativeFilePath(it.filePath());
                QString target = destRoot + "/" + rel;
                if (it.fileInfo().isDir()) {
                    QDir().mkpath(target);
                } else {
                    QDir().mkpath(QFileInfo(target).absolutePath());
                    QFile::remove(target); // allow overwrite
                    if (QFile::copy(it.filePath(), target))
                        ++count;
                }
            }
        } else {
            if (!QFile::exists(absSource)) continue;
            QString destSub = normalizeDest(f);
            QString target = destSub.isEmpty() ? destDir + "/" + QFileInfo(absSource).fileName()
                                               : destDir + "/" + destSub;
            QDir().mkpath(QFileInfo(target).absolutePath());
            QFile::remove(target);
            if (QFile::copy(absSource, target))
                ++count;
        }
    }
    return count;
}

int ModInstallDialog::copyLegacyFomod(const QString& modulePath, const QString& destDir)
{
    // Legacy NMM FOMOD: copy every file under modulePath EXCEPT the fomod/
    // directory and any *.cs install script. We deliberately skip the script
    // — executing untrusted C# is out of scope and the bundled mod files
    // alone are typically functional for these older mods.
    int count = 0;
    QDir base(modulePath);
    QDirIterator it(modulePath, QDir::Files | QDir::Dirs | QDir::NoDotAndDotDot,
                    QDirIterator::Subdirectories);
    while (it.hasNext()) {
        it.next();
        QString rel = base.relativeFilePath(it.filePath());
        QString lowerRel = rel.toLower();
        if (lowerRel == "fomod" || lowerRel.startsWith("fomod/"))
            continue;
        if (lowerRel.endsWith(".cs"))
            continue;

        QString target = destDir + "/" + rel;
        if (it.fileInfo().isDir()) {
            QDir().mkpath(target);
        } else {
            QDir().mkpath(QFileInfo(target).absolutePath());
            QFile::remove(target);
            if (QFile::copy(it.filePath(), target))
                ++count;
        }
    }
    return count;
}

int ModInstallDialog::copyRecursive(const QString& src, const QString& dst)
{
    int count = 0;
    QDirIterator it(src, QDir::Files | QDir::Dirs | QDir::NoDotAndDotDot, QDirIterator::Subdirectories);
    while (it.hasNext()) {
        it.next();
        QString rel = QDir(src).relativeFilePath(it.filePath());
        QString destPath = dst + "/" + rel;

        if (it.fileInfo().isDir()) {
            QDir().mkpath(destPath);
        } else {
            QDir().mkpath(QFileInfo(destPath).path());
            QFile::copy(it.filePath(), destPath);
            count++;
        }
    }
    return count;
}

} // namespace gorganizer

#pragma once

#include <QDialog>
#include <QTreeWidget>
#include <QLabel>
#include <QProgressBar>
#include <QPushButton>
#include <QDialogButtonBox>
#include <QString>
#include <QList>
#include "FomodPlan.h"

namespace gorganizer {

class GrpcClient;

// Dialog that handles the full mod archive install flow:
// 1. Extract archive to temp dir (with progress)
// 2. Detect Data/ folder inside
// 3. If ambiguous, let user pick the data root
// 4. Copy contents to the game's mod folder
class ModInstallDialog : public QDialog {
    Q_OBJECT
public:
    explicit ModInstallDialog(const QString& archivePath,
                              const QString& modsDir,
                              const QString& defaultModName,
                              QWidget* parent = nullptr);

    // Optional daemon callback. When set, a successful install ends with a
    // RegisterManualInstall RPC so the daemon updates each profile's
    // modlist.txt and the Downloads tab flips to INSTALLED. Without this,
    // the mod folder ends up on disk but the daemon never learns about it,
    // so its plugins.txt and source_archives caches stay stale.
    void setDaemonContext(GrpcClient* grpc, const QString& gameId);

    QString installedModName() const { return m_modName; }
    int installedFileCount() const { return m_fileCount; }

signals:
    // Emitted when the client-side FOMOD wizard opens/closes. MainWindow
    // forwards these to the InstallStatusBanner so the user has a visible
    // "Waiting on FOMOD" signal even when the modal is covering other windows.
    void fomodWizardOpened(const QString& archivePath, const QString& modName);
    void fomodWizardClosed(const QString& archivePath);

private slots:
    void onExtractFinished(int exitCode);
    void onInstallClicked();

private:
    void startExtraction();
    void scanExtractedTree();
    void populateTree(const QString& dir, QTreeWidgetItem* parent);
    void installFrom(const QString& sourceDir);
    int copyRecursive(const QString& src, const QString& dst);
    int copyFomodSelections(const QString& modulePath, const QString& destDir);
    int copyLegacyFomod(const QString& modulePath, const QString& destDir);
    void writeMetadata(const QString& modDir);

    QString m_archivePath;
    QString m_modsDir;
    QString m_modName;
    QString m_extractDir;
    QString m_detectedDataRoot;
    int m_fileCount = 0;

    GrpcClient* m_grpc = nullptr;
    QString m_gameId;

    // When non-empty, install uses the FOMOD installer's selections instead
    // of the auto-detected data root.
    QList<FomodFile> m_fomodSelections;
    QString m_fomodModulePath;
    // Legacy NMM-style FOMOD: flat-copy everything outside fomod/ from this
    // module path. Mutually exclusive with m_fomodSelections.
    bool m_legacyFomodFlatCopy = false;

    // UI
    QLabel* m_statusLabel;
    QProgressBar* m_progressBar;
    QLabel* m_treeLabel;
    QTreeWidget* m_treeWidget;
    QDialogButtonBox* m_buttons;
    QPushButton* m_installBtn;

    enum Phase { Extracting, Choosing, Installing, Done };
    Phase m_phase = Extracting;
};

} // namespace gorganizer

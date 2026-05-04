#pragma once

#include <QDialog>
#include <QTreeWidget>
#include <QLabel>
#include <QPointer>
#include <QProgressBar>
#include <QPushButton>
#include <QDialogButtonBox>
#include <QString>
#include <QList>
#include <QThread>
#include "FomodPlan.h"

#include <QProcess>

class QCloseEvent;

namespace gorganizer {

class GrpcClient;
class InstallWorker;

// Handles the full mod archive install flow: extract, detect Data/ root, copy to mod folder.
class ModInstallDialog : public QDialog {
    Q_OBJECT
public:
    explicit ModInstallDialog(const QString& archivePath,
                              const QString& modsDir,
                              const QString& defaultModName,
                              QWidget* parent = nullptr);
    ~ModInstallDialog() override;

    // When set, a successful install fires RegisterManualInstall so the daemon updates modlists.
    void setDaemonContext(GrpcClient* grpc, const QString& gameId);

    QString installedModName() const { return m_modName; }
    int installedFileCount() const { return m_fileCount; }

protected:
    void closeEvent(QCloseEvent* event) override;
    void reject() override;

signals:
    void fomodWizardOpened(const QString& archivePath, const QString& modName);
    void fomodWizardClosed(const QString& archivePath);

private slots:
    void onExtractFinished(int exitCode, QProcess::ExitStatus status);
    void onInstallClicked();
    void onCancelClicked();
    void onWorkerFinished(bool ok, bool cancelled, int fileCount, const QString& err);

private:
    void startExtraction();
    void scanExtractedTree();
    void populateTree(const QString& dir, QTreeWidgetItem* parent);
    void installFrom(const QString& sourceDir);
    void writeMetadata(const QString& modDir);

    QString m_archivePath;
    QString m_modsDir;
    QString m_modName;
    QString m_extractDir;
    QString m_detectedDataRoot;
    int m_fileCount = 0;

    GrpcClient* m_grpc = nullptr;
    QString m_gameId;

    QList<FomodFile> m_fomodSelections;
    QString m_fomodModulePath;
    bool m_legacyFomodFlatCopy = false;

    QLabel* m_statusLabel;
    QProgressBar* m_progressBar;
    QLabel* m_treeLabel;
    QTreeWidget* m_treeWidget;
    QDialogButtonBox* m_buttons;
    QPushButton* m_installBtn;
    QPushButton* m_cancelBtn = nullptr;

    QThread* m_workerThread = nullptr;
    InstallWorker* m_worker = nullptr;
    QString m_installDestDir;

    QPointer<QProcess> m_extractProc;
    QString m_extractToolUsed;
    QStringList m_extractArgsUsed;

    enum Phase { Extracting, Choosing, Installing, Cancelling, Done };
    Phase m_phase = Extracting;
};

} // namespace gorganizer

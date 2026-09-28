#pragma once

#include <QDialog>
#include <QString>
#include <QStringList>
#include <QtGlobal>
#include <vector>
#include "GrpcTypes.h"

class QCloseEvent;
class QDialogButtonBox;
class QLabel;
class QProgressBar;
class QPushButton;
class QTreeWidget;

namespace gorganizer {

class GrpcClient;
class InstallController;

class ModInstallDialog : public QDialog {
    Q_OBJECT
public:
    struct ArchiveSource {
        QString archiveRelPath;
        QString externalArchivePath;

        static ArchiveSource fromLibrary(const QString& path) { return {path, {}}; }
        static ArchiveSource fromExternal(const QString& path) { return {{}, path}; }
    };

    struct InstallTarget {
        GrpcInstallMode mode;
        QString targetMod;
    };

    explicit ModInstallDialog(const QString& gameId, const QString& modName,
                              GrpcClient* grpc, InstallController* installs, ArchiveSource source,
                              QWidget* parent = nullptr,
                              InstallTarget target = {GrpcInstallAsNewMod, {}});

    QString installedModName() const { return m_modName; }
    int installedFileCount() const { return m_fileCount; }
    bool installUnconfirmed() const { return m_installUnconfirmed; }

protected:
    void closeEvent(QCloseEvent* event) override;
    void reject() override;

signals:
    void fomodWizardOpened(const QString& archivePath, const QString& modName);
    void fomodWizardClosed(const QString& archivePath);
    void installDetached(quint64 requestId, const QString& gameId, const QString& modName);

private slots:
    void onPreviewCompleted(quint64 requestId, const GrpcPreviewInstallResult& result);
    void onPreviewFailed(quint64 requestId, const QString& error, int grpcCode);
    void onInstallCompleted(quint64 requestId, const QString& modFolder, int fileCount);
    void onInstallFailed(quint64 requestId, const QString& error);
    void onInstallCancelled(quint64 requestId);
    void onInstallUnknown(quint64 requestId);
    void onInstallClicked();

private:
    void showRoots(const QStringList& selectableRoots);
    void beginInstall(bool fomodConfirmed, const std::vector<GrpcFomodFile>& files = {},
                      const QString& selectedRoot = QString());
    void showFailure(const QString& message);
    void discardPreview();
    QString archivePath() const;

    QString m_gameId;
    QString m_modName;
    GrpcClient* m_grpc;
    InstallController* m_installs;
    ArchiveSource m_source;
    InstallTarget m_target;
    QString m_previewId;
    QString m_selectedRoot;
    QString m_installRoot;
    std::vector<GrpcFomodFile> m_selectedFiles;
    bool m_fomodConfirmed = false;
    bool m_cancelRequested = false;
    bool m_reconciling = false;
    bool m_installUnconfirmed = false;
    QStringList m_selectableRoots;
    quint64 m_previewRequestId = 0;
    quint64 m_installRequestId = 0;
    int m_fileCount = 0;
    bool m_rootChosen = false;

    QLabel* m_statusLabel;
    QProgressBar* m_progressBar;
    QLabel* m_treeLabel;
    QTreeWidget* m_treeWidget;
    QDialogButtonBox* m_buttons;
    QPushButton* m_installBtn;
    QPushButton* m_cancelBtn;

    enum Phase { Previewing, CancellingPreview, Choosing, Installing, Done };
    Phase m_phase = Previewing;
};

}

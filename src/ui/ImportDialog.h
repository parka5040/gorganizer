#pragma once

#include <QDialog>
#include <QString>
#include "GrpcTypes.h"

class QLabel;
class QLineEdit;
class QProgressBar;
class QPushButton;
class QRadioButton;
class QStackedWidget;
class QTreeWidget;
class QTreeWidgetItem;

namespace gorganizer {

class GrpcClient;

class ImportDialog : public QDialog {
    Q_OBJECT
public:
    ImportDialog(GrpcClient* grpc, const QString& gameId, QWidget* parent = nullptr);

signals:
    void importCompleted();

protected:
    void closeEvent(QCloseEvent* ev) override;
    void reject() override;

private slots:
    void onBrowseArchive();
    void onPreview();
    void onStartImport();
    void onCancelTransfer();
    void onBackToSelection();
    void onTransferProgress(const GrpcTransferProgress& progress);
    void onTransferCompleted(const GrpcTransferSummary& summary);
    void onTransferFailed(const QString& error);

private:
    QWidget* buildArchivePage();
    QWidget* buildSelectionPage();
    QWidget* buildProgressPage();
    void populatePreview();
    void updateStartEnabled();
    GrpcTransferPolicy selectedPolicy() const;
    QStringList checkedChildren(const QTreeWidgetItem* root) const;
    bool confirmAbortWhileRunning();
    static QString friendlyTransferError(const QString& error);

    GrpcClient* m_grpc;
    QString m_gameId;
    GrpcImportPreview m_preview;
    bool m_running = false;
    bool m_cancelRequested = false;

    QStackedWidget* m_stack = nullptr;

    QLineEdit* m_archiveEdit = nullptr;
    QLabel* m_archiveErrorLabel = nullptr;
    QPushButton* m_previewBtn = nullptr;

    QLabel* m_manifestLabel = nullptr;
    QTreeWidget* m_tree = nullptr;
    QTreeWidgetItem* m_modsRoot = nullptr;
    QTreeWidgetItem* m_profilesRoot = nullptr;
    QRadioButton* m_renameRadio = nullptr;
    QRadioButton* m_skipRadio = nullptr;
    QRadioButton* m_overwriteRadio = nullptr;
    QLabel* m_selectionErrorLabel = nullptr;
    QPushButton* m_startBtn = nullptr;

    QLabel* m_stepLabel = nullptr;
    QLabel* m_itemLabel = nullptr;
    QLabel* m_bytesLabel = nullptr;
    QProgressBar* m_progressBar = nullptr;
    QLabel* m_resultLabel = nullptr;
    QPushButton* m_cancelBtn = nullptr;
    QPushButton* m_backBtn = nullptr;
    QPushButton* m_closeBtn = nullptr;
};

}

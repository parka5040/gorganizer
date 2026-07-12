#pragma once

#include <QDialog>
#include <QString>
#include <vector>
#include "GrpcTypes.h"

class QCheckBox;
class QLabel;
class QLineEdit;
class QListWidget;
class QProgressBar;
class QPushButton;
class QStackedWidget;

namespace gorganizer {

class GrpcClient;

class ExportDialog : public QDialog {
    Q_OBJECT
public:
    ExportDialog(GrpcClient* grpc, const QString& gameId, QWidget* parent = nullptr);

protected:
    void closeEvent(QCloseEvent* ev) override;
    void reject() override;

private slots:
    void onBrowseDestination();
    void onStartExport();
    void onCancelTransfer();
    void onBackToConfig();
    void onTransferProgress(const GrpcTransferProgress& progress);
    void onTransferCompleted(const GrpcTransferSummary& summary);
    void onTransferFailed(const QString& error);

private:
    QWidget* buildConfigPage();
    QWidget* buildProgressPage();
    void loadSelections();
    void setAllMods(bool checked);
    void updateStartEnabled();
    QStringList checkedItems(const QListWidget* list) const;
    bool confirmAbortWhileRunning();

    GrpcClient* m_grpc;
    QString m_gameId;
    QString m_loadError;
    bool m_running = false;
    bool m_cancelRequested = false;

    QStackedWidget* m_stack = nullptr;

    QListWidget* m_profileList = nullptr;
    QListWidget* m_modList = nullptr;
    QLabel* m_selectionLabel = nullptr;
    QCheckBox* m_overwriteCheck = nullptr;
    QCheckBox* m_settingsCheck = nullptr;
    QLineEdit* m_destinationEdit = nullptr;
    QLabel* m_configErrorLabel = nullptr;
    QPushButton* m_startBtn = nullptr;
    QPushButton* m_closeConfigBtn = nullptr;

    QLabel* m_stepLabel = nullptr;
    QLabel* m_itemLabel = nullptr;
    QLabel* m_bytesLabel = nullptr;
    QProgressBar* m_progressBar = nullptr;
    QLabel* m_resultLabel = nullptr;
    QPushButton* m_cancelBtn = nullptr;
    QPushButton* m_backBtn = nullptr;
    QPushButton* m_closeBtn = nullptr;

    std::vector<GrpcModInfo> m_mods;
};

}

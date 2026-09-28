#pragma once

#include <QObject>
#include <QSet>
#include <QString>
#include "GrpcTypes.h"

class QAction;
class QDialog;
class QLabel;
class QListWidget;
class QPushButton;
class QStatusBar;
class QWidget;

namespace gorganizer {

class GrpcClient;
class SessionController;

class SteamMaintenanceController : public QObject {
    Q_OBJECT
public:
    SteamMaintenanceController(GrpcClient* grpc, SessionController* session,
                               QAction* helpAction, QAction* pauseAction,
                               QStatusBar* statusBar, QWidget* parentWindow);

public slots:
    void showHelp();
    void pauseMods();

signals:
    void modRecovered(const QString& gameId);

private:
    void onActiveGameChanged();
    void onStatus(const GrpcVFSStatus& status);
    void updateActions();
    void showSavedFiles();
    void refreshSavedFiles();
    void showBatchFiles();
    void openBatchFolder();
    void recoverFiles();
    void deleteBatch();
    void finishSteam();
    void onMaintenanceSet(quint64 requestId, const GrpcVFSStatus& status);
    void onMaintenanceSetFailed(quint64 requestId, const QString& gameId, const QString& error, int grpcCode);
    void onFilesImported(quint64 requestId, const QString& gameId, const QString& modName, int fileCount);
    void onFilesImportFailed(quint64 requestId, const QString& gameId, const QString& error, int grpcCode);
    void onBatchDeleted(quint64 requestId, const GrpcVFSStatus& status);
    void onBatchDeleteFailed(quint64 requestId, const QString& gameId, const QString& error, int grpcCode);
    const GrpcPreservedBatch* selectedBatch() const;
    void requestRefresh(const QString& gameId);

    GrpcClient* m_grpc;
    SessionController* m_session;
    QAction* m_helpAction;
    QAction* m_pauseAction;
    QStatusBar* m_statusBar;
    QWidget* m_parentWindow;
    QDialog* m_panel = nullptr;
    QLabel* m_explanation = nullptr;
    QPushButton* m_pauseButton = nullptr;
    QPushButton* m_finishButton = nullptr;
    QPushButton* m_showFilesButton = nullptr;
    QDialog* m_savedDialog = nullptr;
    QListWidget* m_batches = nullptr;
    QListWidget* m_files = nullptr;
    QLabel* m_fileMessage = nullptr;
    QPushButton* m_openButton = nullptr;
    QPushButton* m_recoverButton = nullptr;
    QPushButton* m_deleteButton = nullptr;
    QString m_gameId;
    QString m_operationGameId;
    GrpcVFSStatus m_status;
    QSet<QString> m_promptedGames;
    quint64 m_maintenanceRequestId = 0;
    quint64 m_importRequestId = 0;
    quint64 m_deleteRequestId = 0;
    quint64 m_refreshRequestId = 0;
    bool m_haveStatus = false;
    bool m_finishing = false;
};

}

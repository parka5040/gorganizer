#pragma once

#include <QObject>
#include <QHash>
#include <QString>
#include <QTimer>
#include <vector>
#include "GrpcTypes.h"

namespace gorganizer {

class GrpcClient;

class InstallController : public QObject {
    Q_OBJECT
public:
    struct InstallRequest {
        QString gameId;
        QString archiveRelPath;
        QString externalArchivePath;
        GrpcInstallMode mode = GrpcInstallAsNewMod;
        QString targetMod;
        QString previewId;
        std::vector<GrpcFomodFile> selectedFiles;
        bool fomodConfirmed = false;
        QString selectedRoot;
    };

    explicit InstallController(GrpcClient* grpc, QObject* parent = nullptr);
    quint64 install(const InstallRequest& request);
    quint64 reinstall(const QString& gameId, const QString& modName);
    void cancel(quint64 requestId);
    bool hasPendingOperations() const { return !m_pending.isEmpty(); }
    int pendingCount() const { return m_pending.size(); }

signals:
    void installSucceeded(quint64 requestId, const QString& modFolder, int fileCount);
    void installFailed(quint64 requestId, const QString& error);
    void reinstallSucceeded(quint64 requestId, const GrpcReinstallResult& result);
    void reinstallFailed(quint64 requestId, const QString& error);
    void cancelled(quint64 requestId);
    void reconciling(quint64 requestId);
    void outcomeUnknown(quint64 requestId);

private:
    struct Pending {
        QString gameId;
        QString clientRequestId;
        QString modName;
        bool reinstall = false;
        bool reconciling = false;
        bool sawRunning = false;
        int polls = 0;
        quint64 queryId = 0;
    };
    void onFailed(quint64 requestId, int grpcCode, const QString& error, bool sent);
    void beginReconciliation(quint64 requestId);
    void query(quint64 requestId);
    void pollAgain(quint64 requestId);
    void finishFailed(quint64 requestId, const QString& error);
    void finishSucceeded(quint64 requestId, const QString& modFolder, int fileCount);
    void finishCancelled(quint64 requestId);
    void finishUnknown(quint64 requestId);
    void forget(quint64 requestId);

    GrpcClient* m_grpc;
    QHash<quint64, Pending> m_pending;
    QHash<quint64, quint64> m_queries;
};

}

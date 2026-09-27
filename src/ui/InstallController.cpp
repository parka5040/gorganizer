#include "InstallController.h"
#include "GrpcClient.h"

#include <QUuid>

namespace gorganizer {

InstallController::InstallController(GrpcClient* grpc, QObject* parent)
    : QObject(parent)
    , m_grpc(grpc)
{
    connect(m_grpc, &GrpcClient::installRequestCompleted, this,
            [this](quint64 id, const QString& modFolder, int fileCount) {
        if (m_pending.contains(id) && !m_pending.value(id).reinstall)
            finishSucceeded(id, modFolder, fileCount);
    });
    connect(m_grpc, &GrpcClient::modReinstalled, this,
            [this](quint64 id, const QString&, const QString&, const GrpcReinstallResult& result) {
        if (!m_pending.contains(id) || !m_pending.value(id).reinstall) return;
        forget(id);
        emit reinstallSucceeded(id, result);
    });
    connect(m_grpc, &GrpcClient::installRequestFailed, this,
            [this](quint64 id, int code, const QString& error, bool sent) {
        if (m_pending.contains(id) && !m_pending.value(id).reinstall)
            onFailed(id, code, error, sent);
    });
    connect(m_grpc, &GrpcClient::reinstallRequestFailed, this,
            [this](quint64 id, int code, const QString& error, bool sent) {
        if (m_pending.contains(id) && m_pending.value(id).reinstall)
            onFailed(id, code, error, sent);
    });
    connect(m_grpc, &GrpcClient::installOutcomeReceived, this,
            [this](quint64 queryId, const GrpcInstallOutcome& outcome) {
        const quint64 id = m_queries.take(queryId);
        if (!m_pending.contains(id) || m_pending.value(id).queryId != queryId) return;
        m_pending[id].queryId = 0;
        switch (outcome.state) {
        case GrpcInstallOutcomeState::Succeeded:
            finishSucceeded(id, outcome.modFolder, outcome.fileCount);
            break;
        case GrpcInstallOutcomeState::Failed:
            finishFailed(id, outcome.error);
            break;
        case GrpcInstallOutcomeState::Cancelled:
            finishCancelled(id);
            break;
        case GrpcInstallOutcomeState::Running:
            m_pending[id].sawRunning = true;
            m_pending[id].polls = 0;
            QTimer::singleShot(1500, this, [this, id] { query(id); });
            break;
        case GrpcInstallOutcomeState::Unknown:
            if (m_pending[id].sawRunning) finishUnknown(id);
            else pollAgain(id);
            break;
        }
    });
    connect(m_grpc, &GrpcClient::installOutcomeFailed, this,
            [this](quint64 queryId, int, const QString&) {
        const quint64 id = m_queries.take(queryId);
        if (!m_pending.contains(id) || m_pending.value(id).queryId != queryId) return;
        m_pending[id].queryId = 0;
        pollAgain(id);
    });
    connect(m_grpc, &GrpcClient::workersStopped, this, [this] {
        const auto ids = m_pending.keys();
        for (quint64 id : ids) beginReconciliation(id);
    });
    connect(m_grpc, &GrpcClient::connected, this, [this] {
        const auto ids = m_pending.keys();
        for (quint64 id : ids)
            if (m_pending.value(id).reconciling && m_pending.value(id).queryId == 0)
                query(id);
    });
    connect(m_grpc, &GrpcClient::installCompletedHintReceived, this,
            [this](const GrpcInstallCompleted& hint) {
        for (auto it = m_pending.cbegin(); it != m_pending.cend(); ++it) {
            if (it.value().reconciling && !it.value().reinstall &&
                it.value().clientRequestId == hint.clientRequestId &&
                it.value().gameId == hint.gameId && it.value().queryId == 0) {
                query(it.key());
                break;
            }
        }
    });
}

quint64 InstallController::install(const InstallRequest& request)
{
    const QString clientId = QUuid::createUuid().toString(QUuid::WithoutBraces);
    quint64 id;
    if (request.archiveRelPath.isEmpty()) {
        id = m_grpc->startInstallExternal(request.gameId, request.externalArchivePath,
            request.mode, request.targetMod, request.fomodConfirmed, request.selectedRoot,
            request.previewId, request.selectedFiles, clientId);
    } else {
        id = m_grpc->startInstall(request.gameId, request.archiveRelPath, request.mode,
            request.targetMod, request.previewId, request.selectedFiles, request.fomodConfirmed,
            request.selectedRoot, clientId);
    }
    m_pending.insert(id, Pending{request.gameId, clientId, request.targetMod});
    return id;
}

quint64 InstallController::reinstall(const QString& gameId, const QString& modName)
{
    const QString clientId = QUuid::createUuid().toString(QUuid::WithoutBraces);
    const quint64 id = m_grpc->reinstallModAsync(gameId, modName, clientId);
    Pending pending{gameId, clientId, modName};
    pending.reinstall = true;
    m_pending.insert(id, pending);
    return id;
}

void InstallController::cancel(quint64 requestId)
{
    if (m_pending.contains(requestId) && !m_pending.value(requestId).reconciling)
        m_grpc->cancelInstallRequest(requestId);
}

void InstallController::onFailed(quint64 requestId, int grpcCode, const QString& error, bool sent)
{
    if (!sent) {
        if (grpcCode == GrpcStatusCancelled)
            finishCancelled(requestId);
        else
            finishFailed(requestId, error);
        return;
    }
    if (grpcCode == GrpcStatusCancelled || grpcCode == GrpcStatusDeadlineExceeded ||
        grpcCode == GrpcStatusUnavailable || grpcCode == GrpcStatusUnknown) {
        beginReconciliation(requestId);
        return;
    }
    finishFailed(requestId, error);
}

void InstallController::beginReconciliation(quint64 requestId)
{
    auto it = m_pending.find(requestId);
    if (it == m_pending.end() || it->reconciling) return;
    it->reconciling = true;
    emit reconciling(requestId);
    query(requestId);
}

void InstallController::query(quint64 requestId)
{
    auto it = m_pending.find(requestId);
    if (it == m_pending.end() || !it->reconciling || it->queryId) return;
    if (!m_grpc->isConnected()) {
        QTimer::singleShot(1500, this, [this, requestId] { query(requestId); });
        return;
    }
    const quint64 queryId = m_grpc->getInstallOutcome(it->gameId, it->clientRequestId);
    it->queryId = queryId;
    m_queries.insert(queryId, requestId);
}

void InstallController::pollAgain(quint64 requestId)
{
    auto it = m_pending.find(requestId);
    if (it == m_pending.end()) return;
    if (++it->polls >= 80) {
        finishUnknown(requestId);
        return;
    }
    QTimer::singleShot(1500, this, [this, requestId] { query(requestId); });
}

void InstallController::forget(quint64 requestId)
{
    auto it = m_pending.find(requestId);
    if (it == m_pending.end()) return;
    m_queries.remove(it->queryId);
    m_pending.erase(it);
}

void InstallController::finishSucceeded(quint64 requestId, const QString& modFolder, int fileCount)
{
    const Pending pending = m_pending.value(requestId);
    forget(requestId);
    if (pending.reinstall) {
        GrpcReinstallResult result;
        result.fileCount = fileCount;
        emit reinstallSucceeded(requestId, result);
    } else {
        emit installSucceeded(requestId, modFolder, fileCount);
    }
}

void InstallController::finishFailed(quint64 requestId, const QString& error)
{
    const bool reinstall = m_pending.value(requestId).reinstall;
    forget(requestId);
    if (reinstall) emit reinstallFailed(requestId, error);
    else emit installFailed(requestId, error);
}

void InstallController::finishCancelled(quint64 requestId)
{
    forget(requestId);
    emit cancelled(requestId);
}

void InstallController::finishUnknown(quint64 requestId)
{
    forget(requestId);
    emit outcomeUnknown(requestId);
}

}

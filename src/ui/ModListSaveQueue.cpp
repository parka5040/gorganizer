#include "ModListSaveQueue.h"
#include "GrpcClient.h"

#include <utility>

namespace gorganizer {

ModListSaveQueue::ModListSaveQueue(GrpcClient* grpc, QObject* parent)
    : QObject(parent)
    , m_grpc(grpc)
{
    connect(m_grpc, &GrpcClient::modListSaved, this, &ModListSaveQueue::onSaved);
    connect(m_grpc, &GrpcClient::modListSaveFailed, this, &ModListSaveQueue::onFailed);
    connect(m_grpc, &GrpcClient::workersStopped, this, &ModListSaveQueue::onWorkersStopped);
}

void ModListSaveQueue::setContext(const QString& gameId, const QString& profileName, const QString& modsDir)
{
    const Key next{gameId, profileName, modsDir};
    if (next == m_key)
        return;
    for (auto it = m_flights.begin(); it != m_flights.end(); ++it) {
        if (it->key == m_key)
            it->abandoned = true;
    }
    m_pending.reset();
    m_key = next;
}

bool ModListSaveQueue::hasFlight() const
{
    for (auto it = m_flights.cbegin(); it != m_flights.cend(); ++it) {
        if (it->key == m_key)
            return true;
    }
    return false;
}

bool ModListSaveQueue::isIdle() const
{
    return !hasFlight() && !m_pending;
}

quint64 ModListSaveQueue::send(const std::vector<GrpcModListEntry>& entries)
{
    const quint64 id = m_grpc->setModListTracked(m_key.gameId, m_key.profileName, entries);
    m_flights.insert(id, Flight{m_key});
    return id;
}

quint64 ModListSaveQueue::submit(const std::vector<GrpcModListEntry>& entries)
{
    if (m_key.gameId.isEmpty() || m_key.profileName.isEmpty() || m_key.modsDir.isEmpty())
        return 0;
    if (hasFlight()) {
        m_pending = entries;
        return 0;
    }
    return send(entries);
}

void ModListSaveQueue::onSaved(quint64 requestId, const QString& gameId, const QString& profileName)
{
    const auto it = m_flights.constFind(requestId);
    if (it == m_flights.constEnd() || it->key.gameId != gameId || it->key.profileName != profileName)
        return;
    const Flight flight = it.value();
    m_flights.erase(it);
    if (flight.key != m_key)
        return;
    if (!flight.abandoned)
        emit saveSucceeded(requestId);
    if (m_pending) {
        const std::vector<GrpcModListEntry> entries = std::move(*m_pending);
        m_pending.reset();
        send(entries);
    } else if (isIdle()) {
        emit drained();
    }
}

void ModListSaveQueue::onFailed(quint64 requestId, const QString& gameId, const QString& profileName, const QString&)
{
    const auto it = m_flights.constFind(requestId);
    if (it == m_flights.constEnd() || it->key.gameId != gameId || it->key.profileName != profileName)
        return;
    const Flight flight = it.value();
    m_flights.erase(it);
    if (flight.key != m_key)
        return;
    const bool pending = m_pending.has_value();
    m_pending.reset();
    if (!flight.abandoned || pending)
        emit saveFailed(requestId);
    else if (isIdle())
        emit drained();
}

void ModListSaveQueue::onWorkersStopped()
{
    const bool unsaved = !isIdle();
    m_flights.clear();
    m_pending.reset();
    if (unsaved)
        emit saveFailed(0);
}

}

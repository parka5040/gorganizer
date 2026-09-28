#pragma once

#include "GrpcTypes.h"

#include <QHash>
#include <QObject>
#include <QString>
#include <optional>
#include <vector>

namespace gorganizer {

class GrpcClient;

class ModListSaveQueue : public QObject {
    Q_OBJECT
public:
    explicit ModListSaveQueue(GrpcClient* grpc, QObject* parent = nullptr);

    void setContext(const QString& gameId, const QString& profileName, const QString& modsDir);
    quint64 submit(const std::vector<GrpcModListEntry>& entries);
    bool isIdle() const;

signals:
    void drained();
    void saveSucceeded(quint64 requestId);
    void saveFailed(quint64 requestId);

private slots:
    void onSaved(quint64 requestId, const QString& gameId, const QString& profileName);
    void onFailed(quint64 requestId, const QString& gameId, const QString& profileName, const QString& error);
    void onWorkersStopped();

private:
    struct Key {
        QString gameId;
        QString profileName;
        QString modsDir;

        bool operator==(const Key& other) const = default;
    };
    struct Flight {
        Key key;
        bool abandoned = false;
    };

    quint64 send(const std::vector<GrpcModListEntry>& entries);
    bool hasFlight() const;

    GrpcClient* m_grpc;
    Key m_key;
    QHash<quint64, Flight> m_flights;
    std::optional<std::vector<GrpcModListEntry>> m_pending;
};

}

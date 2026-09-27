#pragma once

#include <QHash>
#include <QObject>
#include <QSet>
#include <QString>
#include "AppConfig.h"
#include "GameInfo.h"
#include "GrpcTypes.h"

class QAction;
class QStatusBar;
class QWidget;

namespace gorganizer {

class GrpcClient;
class ModListWidget;
class SessionController;

class GameSetupController : public QObject {
    Q_OBJECT
public:
    GameSetupController(AppConfig& config, GrpcClient* grpc, SessionController* session,
                        ModListWidget* modList, QAction* installTtwAction,
                        QStatusBar* statusBar, QWidget* parentWindow);

public slots:
    // Shows the Install-TTW action only while TTW is active but not yet verified installed.
    void onActiveGameChanged(const GameInfo& game);
    // Lets the user pick an unmanaged detected game and adds it to the managed set.
    void onAddNewGame();
    void onLocateGame();
    // Runs the TTW installer dialog; on success marks ttw managed and active before re-detection.
    void onInstallTTW();
    // Reopens the latest recovery prompt for a game or requests its status while details are unavailable.
    void reviewRecovery(const QString& gameId);

private slots:
    // Shows a recovery prompt once for each pending item identity.
    void onRecoveryPending(const GrpcRecoveryPending& recovery);
    void onVfsStatusReceived(const GrpcVFSStatus& status);
    void onVfsStatusQueried(quint64 requestId, const GrpcVFSStatus& status);
    void onVfsStatusQueryFailed(quint64 requestId, const QString& gameId, const QString& error);
    // Explains that a recovery confirmation went stale without changing the game.
    void onRpcError(const QString& method, const QString& error);

private:
    // Queues a recovery prompt without repeating the currently open dialog.
    void queueRecovery(const GrpcRecoveryPending& recovery);
    void configureNewGame(const GameInfo& game);

    AppConfig& m_config;
    GrpcClient* m_grpc;
    SessionController* m_session;
    ModListWidget* m_modList;
    QAction* m_installTtwAction;
    QStatusBar* m_statusBar;
    QWidget* m_parentWindow;
    QHash<quint64, GameInfo> m_pendingGames;
    QHash<QString, QSet<QString>> m_seenRecoveryIds;
    QSet<QString> m_shownRecoveryIds;
    QHash<QString, GrpcRecoveryPending> m_latestRecoveries;
    QHash<QString, GrpcRecoveryPending> m_queuedRecoveries;
    QHash<QString, quint64> m_pendingReviewQueries;
    QHash<QString, quint64> m_lastRecoveryEventSeq;
    QHash<QString, quint64> m_lastRestoreAttemptSeq;
    quint64 m_recoveryEventSeq = 0;
    QString m_showingRecoveryGameId;
    QString m_showingRecoveryId;
    bool m_showingRecovery = false;
};

}

#pragma once

#include <QObject>
#include <QString>
#include "AppConfig.h"
#include "GameInfo.h"

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
    // Runs the TTW installer dialog; on success marks ttw managed and active before re-detection.
    void onInstallTTW();

private slots:
    // Modal prompt for ambiguous Data/ state at daemon startup; restore is destructive and user-confirmed.
    void onRecoveryPending(const QString& gameId, const QString& dataPath,
                           const QString& backupPath, const QString& reason);

private:
    AppConfig& m_config;
    GrpcClient* m_grpc;
    SessionController* m_session;
    ModListWidget* m_modList;
    QAction* m_installTtwAction;
    QStatusBar* m_statusBar;
    QWidget* m_parentWindow;
};

}

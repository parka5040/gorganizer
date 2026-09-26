#pragma once

#include <QObject>
#include <QString>
#include "AppConfig.h"
#include "GameInfo.h"

class QStatusBar;
class QWidget;

namespace gorganizer {

class GrpcClient;
class ModLoaderController;
class RunButtonWidget;
class SessionController;

class LaunchController : public QObject {
    Q_OBJECT
public:
    LaunchController(AppConfig& config, GrpcClient* grpc, SessionController* session,
                     ModLoaderController* modLoader, RunButtonWidget* runButton,
                     QStatusBar* statusBar, QWidget* parentWindow);

signals:
    void ttwInstallRequested();

public slots:
    // U-3: disables Run between the launch request and gameLaunched/gameLaunchFailed to block double launches.
    void onRunGame();
    // Persists the per-game last-selected Run target, never an install or repair action.
    void onTargetChanged(const QString& toolId);

private slots:
    // U-3 re-enable point: launch resolved successfully.
    void onGameLaunched(int pid);
    // U-3 re-enable point on failure; translates machine error strings into actionable dialogs.
    void onGameLaunchFailed(const QString& error);
    // Re-evaluates Run when a SMAPI operation starts or ends.
    void onModLoaderActivityChanged(const QString& gameId, bool active);
    // Re-evaluates Run for the newly active game.
    void onActiveGameChanged(const GameInfo& game);

private:
    // Enables Run only while no launch is pending and no SMAPI operation runs for the active game.
    void updateRunEnabled();
    // Explains a launch refused because SMAPI is unusable and offers to install or repair it.
    void offerModLoaderFix(const QString& gameId, const QString& reason);

    AppConfig& m_config;
    GrpcClient* m_grpc;
    SessionController* m_session;
    ModLoaderController* m_modLoader;
    RunButtonWidget* m_runButton;
    QStatusBar* m_statusBar;
    QWidget* m_parentWindow;
    bool m_launchPending = false;
};

}

#pragma once

#include <QObject>
#include <QString>
#include "AppConfig.h"

class QStatusBar;
class QWidget;

namespace gorganizer {

class GrpcClient;
class RunButtonWidget;
class SessionController;

class LaunchController : public QObject {
    Q_OBJECT
public:
    LaunchController(AppConfig& config, GrpcClient* grpc, SessionController* session,
                     RunButtonWidget* runButton, QStatusBar* statusBar,
                     QWidget* parentWindow);

signals:
    void ttwInstallRequested();

public slots:
    // U-3: disables Run between the launch request and gameLaunched/gameLaunchFailed to block double launches.
    void onRunGame();
    // Persists the per-game last-selected Run target.
    void onTargetChanged(const QString& toolId);

private slots:
    // U-3 re-enable point: launch resolved successfully.
    void onGameLaunched(int pid);
    // U-3 re-enable point on failure; translates machine error strings into actionable dialogs.
    void onGameLaunchFailed(const QString& error);

private:
    AppConfig& m_config;
    GrpcClient* m_grpc;
    SessionController* m_session;
    RunButtonWidget* m_runButton;
    QStatusBar* m_statusBar;
    QWidget* m_parentWindow;
};

}

#pragma once

#include <QObject>
#include "GameInfo.h"

class QAction;
class QStatusBar;
class QWidget;

namespace gorganizer {

class GrpcClient;
class RunButtonWidget;
class SessionController;

class FalloutPatchController : public QObject {
    Q_OBJECT
public:
    FalloutPatchController(GrpcClient* grpc, SessionController* session,
                           RunButtonWidget* runButton, QAction* patchAction,
                           QStatusBar* statusBar, QWidget* parentWindow);

public slots:
    // Refreshes patch-action visibility and the run button's 4GB flag from the active game's patch status.
    void onActiveGameChanged(const GameInfo& game);
    // Two-step 4GB patch flow: download the patcher, then confirm and apply it to FalloutNV.exe.
    void onPatchFalloutTo4GB();

private:
    GrpcClient* m_grpc;
    SessionController* m_session;
    RunButtonWidget* m_runButton;
    QAction* m_patchAction;
    QStatusBar* m_statusBar;
    QWidget* m_parentWindow;
};

}

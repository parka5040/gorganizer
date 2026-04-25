#pragma once

#include <QWidget>

class QLabel;
class QTimer;
class QProgressBar;

namespace gorganizer {

class GrpcClient;

// SplashScreen blocks the user from interacting with MainWindow during the
// daemon's cold-start warmup. Shown immediately at app launch, it polls the
// daemon's Health RPC and updates a status label until games_warmed flips
// true, then emits ready() and self-closes.
//
// The window is frameless and stays on top so a tester staring at "nothing
// happening" sees the current init step ("detecting games", "warming
// falloutnv", ...) instead of an empty MainWindow.
class SplashScreen : public QWidget {
    Q_OBJECT
public:
    explicit SplashScreen(GrpcClient* grpc, QWidget* parent = nullptr);

    // Bound on the splash's progress wait. After this elapses without
    // games_warmed=true we emit failed() with the last init step so the
    // caller can decide what to show (error dialog + log tail hint).
    void setTimeoutMs(int ms) { m_timeoutMs = ms; }

    // Begins polling. Call after the GrpcClient has connectToDaemon()'d.
    void startPolling();

signals:
    // Emitted exactly once when the daemon reports games_warmed=true.
    void ready();
    // Emitted exactly once if polling exceeds the timeout. Carries the
    // last observed init step so the caller can build a useful error.
    void failed(const QString& lastStep);

private slots:
    void poll();

private:
    GrpcClient* m_grpc;
    QLabel* m_titleLabel = nullptr;
    QLabel* m_stepLabel = nullptr;
    QProgressBar* m_bar = nullptr;
    QTimer* m_timer = nullptr;
    int m_timeoutMs = 30000;
    int m_elapsedMs = 0;
    bool m_done = false;
};

} // namespace gorganizer

#pragma once

#include <QWidget>

class QLabel;
class QTimer;
class QProgressBar;

namespace gorganizer {

class GrpcClient;

class SplashScreen : public QWidget {
    Q_OBJECT
public:
    explicit SplashScreen(GrpcClient* grpc, QWidget* parent = nullptr);

    void setTimeoutMs(int ms) { m_timeoutMs = ms; }

    void startPolling();

signals:
    void ready();
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

}

#pragma once

#include <QWidget>
#include <QLabel>

namespace gorganizer {

class GrpcClient;

class ConnectionIndicator : public QWidget {
    Q_OBJECT
public:
    explicit ConnectionIndicator(GrpcClient* grpc, QWidget* parent = nullptr);

private slots:
    void onConnected();
    void onDisconnected();

private:
    QLabel* m_dot;
    QLabel* m_text;
};

}

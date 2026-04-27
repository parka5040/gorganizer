#include "ConnectionIndicator.h"
#include "GrpcClient.h"

#include <QHBoxLayout>

namespace gorganizer {

ConnectionIndicator::ConnectionIndicator(GrpcClient* grpc, QWidget* parent)
    : QWidget(parent)
{
    auto* layout = new QHBoxLayout(this);
    layout->setContentsMargins(0, 0, 0, 0);
    layout->setSpacing(4);

    m_dot = new QLabel;
    m_dot->setFixedSize(10, 10);
    m_text = new QLabel("Disconnected");

    layout->addWidget(m_dot);
    layout->addWidget(m_text);

    connect(grpc, &GrpcClient::connected, this, &ConnectionIndicator::onConnected);
    connect(grpc, &GrpcClient::disconnected, this, &ConnectionIndicator::onDisconnected);

    // The connect()s above only react to *transitions*. If the GrpcClient
    // already considers itself connected at our construction time (the
    // splash flipped m_connected during Health() polling), no further
    // signal is coming until the channel actually drops. Seed our visual
    // state from the current value so we don't sit at "Disconnected" while
    // every gate-checking RPC actually works.
    if (grpc->isConnected())
        onConnected();
    else
        onDisconnected();
}

void ConnectionIndicator::onConnected()
{
    m_dot->setStyleSheet("background-color: #4CAF50; border-radius: 5px;");
    m_text->setText("Connected");
}

void ConnectionIndicator::onDisconnected()
{
    m_dot->setStyleSheet("background-color: #F44336; border-radius: 5px;");
    m_text->setText("Disconnected");
}

} // namespace gorganizer

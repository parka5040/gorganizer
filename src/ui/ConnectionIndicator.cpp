#include "ConnectionIndicator.h"
#include "GrpcClient.h"

#include <QHBoxLayout>
#include <QStyle>

namespace gorganizer {

ConnectionIndicator::ConnectionIndicator(GrpcClient* grpc, QWidget* parent)
    : QWidget(parent)
{
    auto* layout = new QHBoxLayout(this);
    layout->setContentsMargins(0, 0, 0, 0);
    layout->setSpacing(4);

    m_dot = new QLabel;
    m_dot->setObjectName("connectionDot");
    m_dot->setFixedSize(10, 10);
    m_text = new QLabel("Disconnected");

    layout->addWidget(m_dot);
    layout->addWidget(m_text);

    connect(grpc, &GrpcClient::connected, this, &ConnectionIndicator::onConnected);
    connect(grpc, &GrpcClient::disconnected, this, &ConnectionIndicator::onDisconnected);

    if (grpc->isConnected())
        onConnected();
    else
        onDisconnected();
}

// The dot's fill is driven by the themed QSS rule
// QLabel#connectionDot[connected="true"/"false"], so it tracks the active theme
// (success/error tokens) without a hardcoded color here.
static void repolish(QLabel* dot, bool connected)
{
    dot->setProperty("connected", connected);
    dot->style()->unpolish(dot);
    dot->style()->polish(dot);
}

void ConnectionIndicator::onConnected()
{
    repolish(m_dot, true);
    m_text->setText("Connected");
}

void ConnectionIndicator::onDisconnected()
{
    repolish(m_dot, false);
    m_text->setText("Disconnected");
}

} // namespace gorganizer

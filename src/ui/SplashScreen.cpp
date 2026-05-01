#include "SplashScreen.h"
#include "GrpcClient.h"

#include <QVBoxLayout>
#include <QLabel>
#include <QProgressBar>
#include <QTimer>
#include <QApplication>
#include <QScreen>

namespace gorganizer {

namespace {
constexpr int kPollIntervalMs = 150;
} // namespace

SplashScreen::SplashScreen(GrpcClient* grpc, QWidget* parent)
    : QWidget(parent, Qt::FramelessWindowHint | Qt::SplashScreen | Qt::WindowStaysOnTopHint)
    , m_grpc(grpc)
{
    setAttribute(Qt::WA_DeleteOnClose, false);
    setFixedSize(480, 240);

    auto* layout = new QVBoxLayout(this);
    layout->setContentsMargins(40, 40, 40, 40);
    layout->setSpacing(16);

    m_titleLabel = new QLabel("Initializing Gorganizer");
    QFont titleFont = m_titleLabel->font();
    titleFont.setPointSize(titleFont.pointSize() + 6);
    titleFont.setBold(true);
    m_titleLabel->setFont(titleFont);
    m_titleLabel->setAlignment(Qt::AlignCenter);
    layout->addWidget(m_titleLabel);

    m_stepLabel = new QLabel("Connecting to daemon...");
    m_stepLabel->setAlignment(Qt::AlignCenter);
    m_stepLabel->setWordWrap(true);
    layout->addWidget(m_stepLabel);

    m_bar = new QProgressBar;
    m_bar->setRange(0, 0);
    m_bar->setTextVisible(false);
    layout->addWidget(m_bar);

    layout->addStretch();

    auto* hint = new QLabel("This usually takes a few seconds.");
    hint->setAlignment(Qt::AlignCenter);
    hint->setStyleSheet("color: gray; font-size: 10pt;");
    layout->addWidget(hint);

    if (auto* screen = QApplication::primaryScreen()) {
        QRect g = screen->geometry();
        move(g.center().x() - width() / 2, g.center().y() - height() / 2);
    }

    m_timer = new QTimer(this);
    m_timer->setInterval(kPollIntervalMs);
    connect(m_timer, &QTimer::timeout, this, &SplashScreen::poll);
}

void SplashScreen::startPolling()
{
    m_done = false;
    m_elapsedMs = 0;
    m_timer->start();
}

void SplashScreen::poll()
{
    if (m_done) return;

    GrpcReadiness r;
    QString err;
    if (m_grpc->health(r, err)) {
        if (!r.lastInitStep.isEmpty())
            m_stepLabel->setText(r.lastInitStep);
        if (r.gamesWarmed) {
            m_done = true;
            m_timer->stop();
            emit ready();
            return;
        }
    } else {
        m_stepLabel->setText("Waiting for daemon...");
    }

    m_elapsedMs += kPollIntervalMs;
    if (m_elapsedMs >= m_timeoutMs) {
        m_done = true;
        m_timer->stop();
        emit failed(m_stepLabel->text());
    }
}

} // namespace gorganizer

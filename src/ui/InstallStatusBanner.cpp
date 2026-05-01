#include "InstallStatusBanner.h"

#include <QHBoxLayout>
#include <QLabel>
#include <QProgressBar>
#include <QPushButton>

namespace gorganizer {

static QString stepLabelFor(int step)
{
    switch (step) {
        case GrpcInstallStepExtracting: return "Extracting";
        case GrpcInstallStepCopying: return "Installing";
        case GrpcInstallStepFinalizing: return "Finalizing";
        case GrpcInstallStepComplete: return "Installed";
        case GrpcInstallStepFailed: return "Install failed";
        default: return "Working";
    }
}

InstallStatusBanner::InstallStatusBanner(GrpcClient* grpc, QWidget* parent)
    : QWidget(parent)
    , m_grpc(grpc)
{
    setAutoFillBackground(true);

    auto* row = new QHBoxLayout(this);
    row->setContentsMargins(12, 6, 12, 6);
    row->setSpacing(12);

    m_titleLabel = new QLabel;
    m_titleLabel->setStyleSheet("font-weight: bold;");
    row->addWidget(m_titleLabel);

    m_detailLabel = new QLabel;
    m_detailLabel->setStyleSheet("color: palette(mid);");
    row->addWidget(m_detailLabel, 1);

    m_bar = new QProgressBar;
    m_bar->setFixedWidth(220);
    m_bar->setTextVisible(true);
    row->addWidget(m_bar);

    m_extraBadge = new QLabel;
    m_extraBadge->setStyleSheet("color: palette(mid);");
    row->addWidget(m_extraBadge);

    m_fomodBtn = new QPushButton("Bring wizard to front");
    connect(m_fomodBtn, &QPushButton::clicked, this, &InstallStatusBanner::bringFomodToFront);
    row->addWidget(m_fomodBtn);

    m_autoHide = new QTimer(this);
    m_autoHide->setSingleShot(true);
    m_autoHide->setInterval(2000);
    connect(m_autoHide, &QTimer::timeout, this, [this] {
        bool anyActive = false;
        for (const auto& a : m_active) {
            if (a.step != GrpcInstallStepComplete && a.step != GrpcInstallStepFailed) {
                anyActive = true;
                break;
            }
        }
        if (!anyActive) {
            m_active.clear();
            m_focusedKey.clear();
            setActive(false);
        }
    });

    setActive(false);
    connect(m_grpc, &GrpcClient::installProgressEvent,
            this, &InstallStatusBanner::onInstallProgress);
}

void InstallStatusBanner::setActive(bool visible)
{
    setVisible(visible);
    if (!visible) {
        m_titleLabel->clear();
        m_detailLabel->clear();
        m_bar->reset();
        m_extraBadge->clear();
        m_fomodBtn->hide();
    }
}

void InstallStatusBanner::onInstallProgress(const GrpcInstallProgress& p)
{
    if (p.archiveRelPath.isEmpty())
        return;
    ActiveInstall& a = m_active[p.archiveRelPath];
    a.archiveRelPath = p.archiveRelPath;
    if (!p.modName.isEmpty())
        a.modName = p.modName;
    a.step = p.step;
    a.pct = p.pct;
    a.filesDone = p.filesDone;
    a.filesTotal = p.filesTotal;
    a.currentFile = p.currentFile;
    a.error = p.error;
    m_focusedKey = p.archiveRelPath;

    if (p.step == GrpcInstallStepComplete || p.step == GrpcInstallStepFailed) {
        m_autoHide->start();
    }
    redraw();
}

void InstallStatusBanner::showFomodPending(const QString& archiveRelPath, const QString& modName)
{
    if (archiveRelPath.isEmpty())
        return;
    ActiveInstall& a = m_active[archiveRelPath];
    a.archiveRelPath = archiveRelPath;
    a.modName = modName;
    a.step = GrpcInstallStepExtracting;
    a.pct = -1;
    m_focusedKey = archiveRelPath;
    redraw();
}

void InstallStatusBanner::clearUiNotice(const QString& archiveRelPath)
{
    if (!m_active.contains(archiveRelPath))
        return;
    m_active.remove(archiveRelPath);
    if (m_focusedKey == archiveRelPath)
        m_focusedKey.clear();
    if (m_active.isEmpty())
        setActive(false);
    else {
        if (m_focusedKey.isEmpty())
            m_focusedKey = m_active.begin().key();
        redraw();
    }
}

void InstallStatusBanner::redraw()
{
    if (m_active.isEmpty()) {
        setActive(false);
        return;
    }
    setActive(true);

    const ActiveInstall& a = m_active.value(m_focusedKey);
    QString title = a.modName.isEmpty() ? a.archiveRelPath : a.modName;
    m_titleLabel->setText(stepLabelFor(a.step) + ": " + title);

    QString detail;
    if (a.step == GrpcInstallStepCopying && a.filesTotal > 0) {
        detail = QString("%1 / %2 files").arg(a.filesDone).arg(a.filesTotal);
        if (!a.currentFile.isEmpty())
            detail += " — " + a.currentFile;
    } else if (a.step == GrpcInstallStepFailed && !a.error.isEmpty()) {
        detail = a.error;
    } else if (!a.currentFile.isEmpty()) {
        detail = a.currentFile;
    }
    m_detailLabel->setText(detail);

    if (a.pct < 0) {
        m_bar->setRange(0, 0);
    } else {
        m_bar->setRange(0, 100);
        m_bar->setValue(a.pct);
    }
    m_bar->setFormat("%p%");

    m_fomodBtn->setVisible(false);

    int more = 0;
    for (auto it = m_active.constBegin(); it != m_active.constEnd(); ++it) {
        if (it.key() == m_focusedKey)
            continue;
        if (it.value().step != GrpcInstallStepComplete
         && it.value().step != GrpcInstallStepFailed) {
            more++;
        }
    }
    m_extraBadge->setText(more > 0 ? QString("+%1 more").arg(more) : QString());
}

} // namespace gorganizer

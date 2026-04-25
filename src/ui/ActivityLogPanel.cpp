#include "ActivityLogPanel.h"
#include "GrpcClient.h"

#include <QVBoxLayout>
#include <QHBoxLayout>
#include <QPlainTextEdit>
#include <QToolButton>
#include <QCheckBox>
#include <QLabel>
#include <QDateTime>
#include <QFontDatabase>
#include <QFileInfo>

namespace gorganizer {

namespace {

QString humanBytes(qint64 b)
{
    if (b < 1024) return QString::number(b) + " B";
    double v = static_cast<double>(b);
    static const char* units[] = {"KB", "MB", "GB", "TB"};
    int i = -1;
    do {
        v /= 1024.0;
        ++i;
    } while (v >= 1024.0 && i < 3);
    return QString::number(v, 'f', v < 10.0 ? 1 : 0) + " " + units[i];
}

QString shortName(const QString& archiveRelPath, const QString& fallback)
{
    if (!archiveRelPath.isEmpty())
        return QFileInfo(archiveRelPath).fileName();
    return fallback.isEmpty() ? QString("(unnamed)") : fallback;
}

QString sevColor(int sevInt)
{
    switch (sevInt) {
    case 1: return "#080";  // Success — green
    case 2: return "#a60";  // Warning — amber
    case 3: return "#c00";  // Error — red
    default: return QString();
    }
}

} // namespace

ActivityLogPanel::ActivityLogPanel(GrpcClient* grpc, QWidget* parent)
    : QWidget(parent)
    , m_grpc(grpc)
{
    auto* outer = new QVBoxLayout(this);
    outer->setContentsMargins(4, 4, 4, 4);
    outer->setSpacing(2);

    auto* header = new QHBoxLayout;
    header->setContentsMargins(0, 0, 0, 0);
    header->setSpacing(6);
    m_titleLabel = new QLabel("Activity");
    QFont f = m_titleLabel->font();
    f.setBold(true);
    m_titleLabel->setFont(f);
    header->addWidget(m_titleLabel);
    header->addStretch();
    m_verboseCheck = new QCheckBox("Verbose");
    m_verboseCheck->setToolTip(
        "Show every progress tick (downloads, install steps, daemon info messages). "
        "Off by default — only outcomes are shown.");
    connect(m_verboseCheck, &QCheckBox::toggled, this, [this](bool on) { m_verbose = on; });
    header->addWidget(m_verboseCheck);
    m_clearBtn = new QToolButton;
    m_clearBtn->setText("Clear");
    connect(m_clearBtn, &QToolButton::clicked, this, [this] { m_log->clear(); });
    header->addWidget(m_clearBtn);
    outer->addLayout(header);

    m_log = new QPlainTextEdit;
    m_log->setReadOnly(true);
    m_log->setMaximumBlockCount(500);  // ring-buffer the last 500 lines
    m_log->setFont(QFontDatabase::systemFont(QFontDatabase::FixedFont));
    // Compact line height — this is a status surface, not a focus surface.
    QFont lf = m_log->font();
    lf.setPointSize(qMax(8, lf.pointSize() - 1));
    m_log->setFont(lf);
    outer->addWidget(m_log, 1);

    setMinimumHeight(120);

    log(Severity::Info, "Gorganizer ready.");

    // Wire signals.
    connect(m_grpc, &GrpcClient::installProgressEvent, this, &ActivityLogPanel::onInstallProgress);
    connect(m_grpc, &GrpcClient::archiveEventReceived, this, &ActivityLogPanel::onArchiveEvent);
    connect(m_grpc, &GrpcClient::daemonInfo, this, &ActivityLogPanel::onDaemonInfo);
    connect(m_grpc, &GrpcClient::daemonError, this, &ActivityLogPanel::onDaemonError);
}

void ActivityLogPanel::log(Severity sev, const QString& message)
{
    QString ts = QDateTime::currentDateTime().toString("HH:mm:ss");
    QString color = sevColor(static_cast<int>(sev));
    QString line;
    if (color.isEmpty()) {
        line = QString("<span style='color:#888;'>[%1]</span> %2")
                   .arg(ts.toHtmlEscaped(), message.toHtmlEscaped());
    } else {
        line = QString("<span style='color:#888;'>[%1]</span> <span style='color:%2;'>%3</span>")
                   .arg(ts.toHtmlEscaped(), color, message.toHtmlEscaped());
    }
    m_log->appendHtml(line);
}

void ActivityLogPanel::onInstallProgress(const GrpcInstallProgress& p)
{
    // Map proto step → label. 0=idle, 1=extracting, 2=copying, 3=finalizing,
    // 4=complete, 5=failed.
    int prev = m_lastInstallStep.value(p.installId, -1);
    bool stepChanged = (prev != p.step);
    m_lastInstallStep[p.installId] = p.step;

    const QString name = p.modName.isEmpty() ? shortName(p.archiveRelPath, p.modName) : p.modName;
    switch (p.step) {
    case 1: // extracting
    case 2: // copying
    case 3: // finalizing
        if (stepChanged)
            log(Severity::Info, QString("Installing %1...").arg(name));
        else if (m_verbose)
            log(Severity::Info, QString("  %1: %2% (%3/%4 files)")
                                    .arg(name).arg(p.pct).arg(p.filesDone).arg(p.filesTotal));
        break;
    case 4: // complete
        if (stepChanged)
            log(Severity::Success, QString("Installed %1 (%2 files).").arg(name).arg(p.filesTotal));
        m_lastInstallStep.remove(p.installId);
        break;
    case 5: // failed
        if (stepChanged) {
            QString msg = p.error.isEmpty() ? QString("(unknown error)") : p.error;
            // fomod_required isn't a real failure — the daemon uses it to
            // hand control back to the frontend, which opens the FOMOD
            // wizard. Logging it as Error misled users into thinking the
            // install died right as the popup was about to render. Skip
            // it; if anything goes wrong inside the wizard, that path
            // surfaces its own messages.
            if (msg.contains("fomod_required")) {
                log(Severity::Info, QString("%1 needs the FOMOD installer wizard...").arg(name));
            } else {
                log(Severity::Error, QString("Install of %1 failed: %2").arg(name, msg));
            }
        }
        m_lastInstallStep.remove(p.installId);
        break;
    default:
        break;
    }
}

void ActivityLogPanel::onArchiveEvent(const GrpcArchiveEvent& evt)
{
    if (evt.kind == GrpcArchiveEvent::KindDownloadProgress) {
        const auto& d = evt.progress;
        // Status enum mirrors proto DownloadStatus: 1=queued, 2=downloading,
        // 3=downloaded, 4=installing, 5=installed, 6=uninstalled, 7=cancelled, 8=failed.
        const QString key = d.downloadId.isEmpty() ? d.modName : d.downloadId;
        int prev = m_lastDownloadStatus.value(key, -1);
        bool changed = (prev != d.status);
        m_lastDownloadStatus[key] = d.status;
        const QString name = d.modName.isEmpty() ? QString("download") : d.modName;
        if (!d.modName.isEmpty())
            m_lastArchiveName[d.downloadId] = d.modName;

        switch (d.status) {
        case 1: // queued
            if (changed)
                log(Severity::Info, QString("Queued %1 (position %2).").arg(name).arg(d.queuedAhead + 1));
            break;
        case 2: // downloading
            if (changed)
                log(Severity::Info, QString("Downloading %1...").arg(name));
            else if (m_verbose && d.bytesTotal > 0)
                log(Severity::Info, QString("  %1: %2 / %3").arg(
                    name, humanBytes(d.bytesDownloaded), humanBytes(d.bytesTotal)));
            break;
        case 3: // downloaded
            if (changed)
                log(Severity::Success, QString("Downloaded %1 (%2).").arg(name, humanBytes(d.bytesTotal)));
            m_lastDownloadStatus.remove(key);
            break;
        case 7: // cancelled
            if (changed)
                log(Severity::Warning, QString("Cancelled download of %1.").arg(name));
            m_lastDownloadStatus.remove(key);
            break;
        case 8: // failed
            if (changed) {
                QString msg = d.error.isEmpty() ? QString("(unknown error)") : d.error;
                log(Severity::Error, QString("Download of %1 failed: %2").arg(name, msg));
            }
            m_lastDownloadStatus.remove(key);
            break;
        default:
            break;
        }
    } else if (evt.kind == GrpcArchiveEvent::KindRowChanged && m_verbose) {
        // Verbose-only: the row metadata changed (hidden flag, refresh, etc.).
        log(Severity::Info, QString("Updated %1.").arg(shortName(evt.row.archiveRelPath, evt.row.modName)));
    } else if (evt.kind == GrpcArchiveEvent::KindArchiveRemoved && m_verbose) {
        log(Severity::Info, QString("Removed archive %1.").arg(QFileInfo(evt.archiveRemoved).fileName()));
    }
}

void ActivityLogPanel::onDaemonInfo(const QString& info)
{
    // Filter the noisy ones unless verbose. "ready" fires once per cold
    // start and is interesting; recovery resolution messages too.
    if (!m_verbose) {
        if (info == "ready" || info.startsWith("recovery"))
            log(Severity::Info, info);
        return;
    }
    log(Severity::Info, info);
}

void ActivityLogPanel::onDaemonError(const QString& err)
{
    log(Severity::Error, err);
}

} // namespace gorganizer

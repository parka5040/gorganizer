#include "ActivityLogPanel.h"
#include "GrpcClient.h"
#include "ThemeManager.h"

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
    connect(m_clearBtn, &QToolButton::clicked, this, [this] {
        m_entries.clear();
        m_log->clear();
    });
    header->addWidget(m_clearBtn);
    outer->addLayout(header);

    m_log = new QPlainTextEdit;
    m_log->setReadOnly(true);
    m_log->setMaximumBlockCount(500);
    m_log->setFont(QFontDatabase::systemFont(QFontDatabase::FixedFont));
    QFont lf = m_log->font();
    lf.setPointSize(qMax(8, lf.pointSize() - 1));
    m_log->setFont(lf);
    outer->addWidget(m_log, 1);

    setMinimumHeight(120);

    log(Severity::Info, "Gorganizer ready.");

    connect(m_grpc, &GrpcClient::installProgressEvent, this, &ActivityLogPanel::onInstallProgress);
    connect(m_grpc, &GrpcClient::archiveEventReceived, this, &ActivityLogPanel::onArchiveEvent);
    connect(m_grpc, &GrpcClient::daemonInfo, this, &ActivityLogPanel::onDaemonInfo);
    connect(m_grpc, &GrpcClient::daemonError, this, &ActivityLogPanel::onDaemonError);
    connect(m_grpc, &GrpcClient::dependencyWarning, this, &ActivityLogPanel::onDependencyWarning);

    // Recolor the whole scrollback when the theme changes so severity/timestamp
    // hues stay legible in both light and dark.
    connect(ThemeManager::instance(), &ThemeManager::themeChanged,
            this, [this](const Palette&) { rerenderLog(); });
}

void ActivityLogPanel::onDependencyWarning(const GrpcDependencyWarning& warning)
{
    Severity sev = Severity::Warning;
    switch (warning.kind) {
    case GrpcDepMasterAbsent:
    case GrpcDepMasterOutOfOrder:
        sev = Severity::Error;
        break;
    case GrpcDepMasterDisabled:
    case GrpcDepSoftMissing:
        sev = Severity::Warning;
        break;
    default:
        return;
    }
    QString line = QString("[%1] %2").arg(warning.pluginFilename, warning.detail);
    log(sev, line);
}

QString ActivityLogPanel::renderEntry(const LogEntry& e) const
{
    const Palette& p = ThemeManager::currentPalette();
    const QString tsHex = p.textMuted.name();
    QString color;
    switch (e.sev) {
    case Severity::Success: color = p.successFg.name(); break;
    case Severity::Warning: color = p.warningFg.name(); break;
    case Severity::Error:   color = p.errorFg.name(); break;
    case Severity::Info:    break; // default text color
    }
    if (color.isEmpty()) {
        return QString("<span style='color:%1;'>[%2]</span> %3")
            .arg(tsHex, e.ts.toHtmlEscaped(), e.message.toHtmlEscaped());
    }
    return QString("<span style='color:%1;'>[%2]</span> <span style='color:%3;'>%4</span>")
        .arg(tsHex, e.ts.toHtmlEscaped(), color, e.message.toHtmlEscaped());
}

void ActivityLogPanel::rerenderLog()
{
    m_log->clear();
    for (const auto& e : m_entries)
        m_log->appendHtml(renderEntry(e));
}

void ActivityLogPanel::log(Severity sev, const QString& message)
{
    LogEntry e{sev, QDateTime::currentDateTime().toString("HH:mm:ss"), message};
    m_entries.push_back(e);
    if (m_entries.size() > kMaxLog)
        m_entries.remove(0, m_entries.size() - kMaxLog);
    m_log->appendHtml(renderEntry(e));
}

void ActivityLogPanel::onInstallProgress(const GrpcInstallProgress& p)
{
    int prev = m_lastInstallStep.value(p.installId, -1);
    bool stepChanged = (prev != p.step);
    m_lastInstallStep[p.installId] = p.step;

    const QString name = p.modName.isEmpty() ? shortName(p.archiveRelPath, p.modName) : p.modName;
    switch (p.step) {
    case 1:
    case 2:
    case 3:
        if (stepChanged)
            log(Severity::Info, QString("Installing %1...").arg(name));
        else if (m_verbose)
            log(Severity::Info, QString("  %1: %2% (%3/%4 files)")
                                    .arg(name).arg(p.pct).arg(p.filesDone).arg(p.filesTotal));
        break;
    case 4:
        if (stepChanged)
            log(Severity::Success, QString("Installed %1 (%2 files).").arg(name).arg(p.filesTotal));
        m_lastInstallStep.remove(p.installId);
        break;
    case 5:
        if (stepChanged) {
            QString msg = p.error.isEmpty() ? QString("(unknown error)") : p.error;
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
        const QString key = d.downloadId.isEmpty() ? d.modName : d.downloadId;
        int prev = m_lastDownloadStatus.value(key, -1);
        bool changed = (prev != d.status);
        m_lastDownloadStatus[key] = d.status;
        const QString name = d.modName.isEmpty() ? QString("download") : d.modName;
        if (!d.modName.isEmpty())
            m_lastArchiveName[d.downloadId] = d.modName;

        switch (d.status) {
        case 1:
            if (changed)
                log(Severity::Info, QString("Queued %1 (position %2).").arg(name).arg(d.queuedAhead + 1));
            break;
        case 2:
            if (changed)
                log(Severity::Info, QString("Downloading %1...").arg(name));
            else if (m_verbose && d.bytesTotal > 0)
                log(Severity::Info, QString("  %1: %2 / %3").arg(
                    name, humanBytes(d.bytesDownloaded), humanBytes(d.bytesTotal)));
            break;
        case 3:
            if (changed)
                log(Severity::Success, QString("Downloaded %1 (%2).").arg(name, humanBytes(d.bytesTotal)));
            m_lastDownloadStatus.remove(key);
            break;
        case 7:
            if (changed)
                log(Severity::Warning, QString("Cancelled download of %1.").arg(name));
            m_lastDownloadStatus.remove(key);
            break;
        case 8:
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
        log(Severity::Info, QString("Updated %1.").arg(shortName(evt.row.archiveRelPath, evt.row.modName)));
    } else if (evt.kind == GrpcArchiveEvent::KindArchiveRemoved && m_verbose) {
        log(Severity::Info, QString("Removed archive %1.").arg(QFileInfo(evt.archiveRemoved).fileName()));
    }
}

void ActivityLogPanel::onDaemonInfo(const QString& info)
{
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

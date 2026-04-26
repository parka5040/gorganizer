#pragma once

#include <QWidget>
#include <QHash>

#include "GrpcClient.h"

class QPlainTextEdit;
class QToolButton;
class QCheckBox;
class QLabel;

namespace gorganizer {

// ActivityLogPanel is the persistent console-style strip docked at the
// bottom of the MainWindow. It replaces the old transient
// InstallStatusBanner — instead of a thin bar that flashes during an
// install and disappears, the user sees a continuous timestamped log of
// daemon activity (downloads landing, installs finishing, mods being
// uninstalled, network errors).
//
// Default mode is "minimal" — only outcomes a non-technical user cares
// about (started/finished/failed). A verbose toggle pipes through the
// per-progress-tick events for power users who want to watch a download
// crawl byte-by-byte.
class ActivityLogPanel : public QWidget {
    Q_OBJECT
public:
    explicit ActivityLogPanel(GrpcClient* grpc, QWidget* parent = nullptr);

private slots:
    void onInstallProgress(const GrpcInstallProgress& p);
    void onArchiveEvent(const GrpcArchiveEvent& evt);
    void onDaemonInfo(const QString& info);
    void onDaemonError(const QString& err);
    void onDependencyWarning(const GrpcDependencyWarning& warning);

private:
    enum class Severity { Info, Success, Warning, Error };
    void log(Severity sev, const QString& message);

    GrpcClient* m_grpc;
    QPlainTextEdit* m_log = nullptr;
    QCheckBox* m_verboseCheck = nullptr;
    QToolButton* m_clearBtn = nullptr;
    QLabel* m_titleLabel = nullptr;
    bool m_verbose = false;

    // Per-archive de-dup state so we only emit one "Started" / "Finished"
    // line per download instead of one per byte-progress tick.
    QHash<QString, int> m_lastDownloadStatus;   // archiveRelPath/downloadId → status
    QHash<QString, int> m_lastInstallStep;      // installId → step
    QHash<QString, QString> m_lastArchiveName;  // downloadId → archive name shown to user
};

} // namespace gorganizer

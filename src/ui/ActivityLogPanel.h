#pragma once

#include <QWidget>
#include <QHash>

#include "GrpcClient.h"

class QPlainTextEdit;
class QToolButton;
class QCheckBox;
class QLabel;

namespace gorganizer {

// Persistent timestamped log of daemon activity docked at the bottom of MainWindow.
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

    QHash<QString, int> m_lastDownloadStatus;
    QHash<QString, int> m_lastInstallStep;
    QHash<QString, QString> m_lastArchiveName;
};

} // namespace gorganizer

#pragma once

#include <QObject>
#include <QPointer>
#include <QProcess>
#include <QString>
#include <functional>

#include "GrpcTypes.h"
#include "InstallLayout.h"
#include "NoticeBar.h"

class QTimer;
class QWidget;

namespace gorganizer {

class AppConfig;
class GrpcClient;

class UpdateController : public QObject {
    Q_OBJECT
public:
    UpdateController(GrpcClient* grpc, AppConfig& config, NoticeBar* bar,
                     QWidget* parentWindow, bool daemonStopsOnExit);
    // Evaluates the startup update policy once per window launch.
    void start();
    // Makes an explicit, one-time update check without changing the saved preference.
    void checkNow();
    // Reconciles pending automatic checks with the saved setting.
    void preferenceChanged();
    // Records whether closing the window also stops its daemon.
    void setDaemonStopsOnExit(bool stops);
    // Reports whether an in-place update process is still active.
    bool updating() const;
    // Returns the warning to show when closing during an update.
    QString updateCloseParagraph() const;
    // Stops an update and calls done after it exits or the close timeout elapses.
    void stopUpdateThen(std::function<void()> done);

signals:
    void updatingChanged(bool updating);

private:
    enum class Notice { None, Consent, Installed, OlderDaemon, CheckResult, Updating, UpdateResult };

    // Checks the daemon's version on its first connection and runs a queued check.
    void onConnected();
    // Starts the single pending check when the daemon is reachable.
    void sendCheck();
    // Disconnects the one-shot connection trigger after its check is resolved.
    void releaseConnection();
    // Reports a matching update-check response.
    void onCheckFinished(quint64 requestId, const GrpcUpdateCheck& result);
    // Reports a matching update-check failure.
    void onCheckFailed(quint64 requestId, const QString& error, int grpcCode);
    // Displays an update result according to the installation type.
    void showResult(const GrpcUpdateCheck& result, bool interactive);
    // Displays the one-time startup consent question.
    void showConsent();
    // Displays the selected-release notice.
    void showInstalled(const QString& version);
    // Restores a displaced startup notice after a manual result is dismissed.
    void restoreNotice();
    // Handles a notice dismissed by the user.
    void onDismissed();
    // Displays a dismissible check result when session policy permits it.
    void showCheckNotice(NoticeBar::Kind kind, const QString& text,
                         const QList<NoticeBar::Action>& actions, bool interactive);
    // Validates the bundle and starts an update to version.
    void updateNow(const QString& version, const QString& bundleDir);
    // Requests termination and starts the escalation timer for the current process.
    void cancelUpdate();
    // Captures the newest part of the process output.
    void collectOutput(QProcess* process);
    // Reconciles the selected release after a process exits or fails to start.
    void reconcile(QProcess* process, int exitCode, QProcess::ExitStatus exitStatus);
    // Displays the captured process output and exit details.
    void showDetails(const QString& raw);

    GrpcClient* m_grpc;
    QMetaObject::Connection m_connectedHandler;
    AppConfig& m_config;
    NoticeBar* m_bar;
    QWidget* m_parentWindow;
    AppInstallLayout m_layout;
    Notice m_notice = Notice::None;
    bool m_started = false;
    bool m_readinessChecked = false;
    bool m_skipAutomatic = false;
    bool m_autoQueued = false;
    bool m_interactive = false;
    bool m_autoDismissed = false;
    bool m_discardAutomatic = false;
    bool m_resultInteractive = false;
    bool m_daemonStopsOnExit = false;
    bool m_cancelRequested = false;
    quint64 m_checkId = 0;
    QString m_installedDifferent;
    QString m_olderDaemon;
    QString m_target;
    QString m_selectedBefore;
    QString m_releasesRoot;
    QByteArray m_output;
    QPointer<QProcess> m_process;
    QPointer<QTimer> m_killTimer;
    std::function<void()> m_closeDone;
};

}

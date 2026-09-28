#pragma once

#include <QHash>
#include <QObject>
#include <QSet>
#include <QString>
#include <optional>
#include "GameInfo.h"
#include "GrpcTypes.h"

class QAction;
class QMenu;
class QStatusBar;
class QTimer;
class QWidget;

namespace gorganizer {

class GrpcClient;
class ModLoaderProgressDialog;
class SessionController;

class ModLoaderController : public QObject {
    Q_OBJECT
public:
    ModLoaderController(GrpcClient* grpc, SessionController* session, QMenu* menu,
                        QStatusBar* statusBar, QWidget* parentWindow);

    // Reports whether a SMAPI operation for gameId, including its mount handling, is in progress.
    bool operationActiveFor(const QString& gameId) const { return m_op && m_op->gameId == gameId; }
    // Describes the SMAPI operation that quitting could interrupt, or returns an empty string when none reached the daemon.
    QString interruptibleOperation() const;
    // Starts the install workflow for gameId when it is the active SMAPI game, skipping the intro question when already confirmed.
    void startInstall(const QString& gameId, bool confirmed = false);
    // Starts the repair workflow for gameId when it is the active SMAPI game, skipping the intro question when already confirmed.
    void startRepair(const QString& gameId, bool confirmed = false);
    // Re-reads the mod-loader status of gameId without network access.
    void refreshStatus(const QString& gameId);

public slots:
    // Tracks the active game, showing the SMAPI menu and requesting status for SMAPI-capable games.
    void onActiveGameChanged(const GameInfo& game);

signals:
    void modLoaderStatusChanged(const QString& gameId, const GrpcModLoaderStatus& status);
    void operationActivityChanged(const QString& gameId, bool active);

private slots:
    void onInstallTriggered();
    void onRepairTriggered();
    void onRollbackTriggered();
    void onUninstallTriggered();
    void onCheckUpdatesTriggered();
    // Applies the newest status answer for a game, completes a reconciling operation, and reports an interactive update check.
    void onStatusReceived(quint64 requestId, const QString& gameId, const GrpcModLoaderStatus& status);
    // Retries a reconciling poll or reports a failed interactive update check; other status failures only reach the status bar.
    void onStatusFailed(quint64 requestId, const QString& gameId, const QString& error, int grpcCode);
    // Completes the matching operation, or starts reconciling it when its outcome is unknown.
    void onOperationFinished(quint64 requestId, const QString& gameId, const QString& operation,
                             bool ok, int grpcCode, const GrpcModLoaderStatus& status, const QString& error);
    // Advances the pre-state capture or the unmount check from the answer to one of this controller's VFS queries.
    void onVfsStatusQueried(quint64 requestId, const GrpcVFSStatus& status);
    // Aborts a failed pre-state capture or retries a failed unmount check.
    void onVfsStatusQueryFailed(quint64 requestId, const QString& gameId, const QString& error, int grpcCode);
    // Runs the operation after the maintenance unmount, or checks the mount state when the unmount RPC failed.
    void onMaintenanceUnmountFinished(quint64 requestId, const QString& gameId, bool ok, int grpcCode,
                                      const QString& error);
    // Sends the next unmount check or reconciling poll.
    void onPollTimeout();
    // Gives up on an unmount check or a reconciling operation that exceeded its time limit.
    void onPhaseTimeout();
    // Re-reads the active game's status after a SMAPI operation completes, or polls at once while reconciling.
    void onDaemonInfo(const QString& info);
    // Refreshes the active game's status and resumes pending checks once the daemon is reachable again.
    void onConnected();
    // Starts the disconnect grace period for a running operation.
    void onDisconnected();
    // Starts reconciling a running operation whose result never arrived because the daemon stayed unreachable.
    void onDisconnectTimeout();

private:
    enum class OperationKind { Install, Update, Repair, Rollback, Uninstall };
    enum class Phase { Capturing, Consenting, Unmounting, Running, Reconciling };

    struct Operation {
        quint64 serial = 0;
        OperationKind kind = OperationKind::Install;
        Phase phase = Phase::Capturing;
        QString gameId;
        QString gameName;
        QString targetVersion;
        bool unmountConsented = false;
        bool wasMounted = false;
        QString mountedProfile;
        quint64 captureRequestId = 0;
        quint64 unmountRequestId = 0;
        bool unmountReturned = false;
        QString unmountError;
        quint64 vfsCheckRequestId = 0;
        quint64 requestId = 0;
        quint64 reconcileRequestId = 0;
        QString unknownOutcome;
        QString finalReport;
    };

    // Confirms, captures the mount state, and starts the unmount and the mutating operation for gameId.
    void beginOperation(OperationKind kind, const QString& gameId, bool confirmed);
    // Asks the user to confirm an operation, including the unmount step when the mods are mounted.
    bool confirmOperation(const Operation& op, bool mounted);
    // Asks the user to confirm only the unmount step, answering yes when the mods are not mounted.
    bool confirmUnmount(const Operation& op, bool mounted);
    // Reports whether an operation that was confirmed for gameId may still start.
    bool canStillStart(const QString& gameId) const;
    // Records the mount state captured before the operation and unmounts with consent, or runs the operation when unmounted.
    void onPreStateCaptured(const GrpcVFSStatus& status);
    // Sends the maintenance unmount of the operation's game.
    void startUnmount();
    // Queries the operation's game mount state after a failed unmount RPC when the daemon is connected.
    void sendUnmountCheck();
    // Sends the operation's RPC and shows the progress dialog.
    void runOperation();
    // Keeps suppression and polls the daemon until it no longer holds the operation's game.
    void startReconciling(const QString& reason);
    // Sends a reconciling status poll off the unary worker when the daemon is connected.
    void sendReconcilePoll();
    // Ends the operation before its RPC changed anything, lifting suppression, mounting as requested, and showing message.
    void abortOperation(const QString& message, const QString& remountProfile, bool replaySkipped);
    // Clears the current operation, stops its timers and progress dialog, and announces the end of its activity.
    std::optional<Operation> releaseOperation();
    // Shows a warning whose Remount Mods button mounts profileName of gameId again, or a plain warning when no profile is known.
    void warnWithRemount(const QString& title, const QString& message, const QString& gameId,
                         const QString& profileName, const QString& rawError = QString());
    // Returns the profile to mount after the operation, which is the captured one when the mods were mounted at its start.
    static QString remountProfileFor(const Operation& op);
    // Ends the operation with its definitive RPC result, restoring the mount and showing the outcome.
    void completeOperation(bool ok, const GrpcModLoaderStatus& status, const QString& error);
    // Ends a reconciled operation from the status the daemon reported once idle, restoring the mount and showing it.
    void completeReconciled(const GrpcModLoaderStatus& status);
    // Sends a status query for gameId, remembering plain queries so activation does not repeat them.
    quint64 requestStatus(const QString& gameId, bool checkLatest);
    // Sends this session's one automatic update check for gameId.
    void scheduleLatestCheck(const QString& gameId);
    // Stores and publishes a status after merging the cached latest release into it.
    void applyStatus(const QString& gameId, const GrpcModLoaderStatus& status);
    // Fills the latest release and update flag from this session's cached update check.
    void mergeCachedLatest(const QString& gameId, GrpcModLoaderStatus& status) const;
    // Shows the result of an interactive update check.
    void reportUpdateCheck(const GrpcModLoaderStatus& status);
    // Shows the outcome of a finished operation.
    void reportOutcome(const Operation& op, bool ok, const GrpcModLoaderStatus& status,
                       const QString& error, const QString& failureDetail);
    // Refreshes the SMAPI menu's labels, visibility and enabled states.
    void updateMenu();
    // Returns the menu's status line for the active game.
    QString statusLine() const;
    // Returns the progress dialog, creating it on first use.
    ModLoaderProgressDialog* progressDialog();
    // Describes a mod-loader status as a one-line summary.
    static QString describeStatus(const GrpcModLoaderStatus& status);
    // Returns the confirmation paragraph that explains the unmount step of an operation.
    static QString unmountNote(OperationKind kind, const QString& gameName);
    // Returns the title-case name of an operation kind.
    static QString operationTitle(OperationKind kind);
    // Returns the "-ing" form of an operation kind for progress text.
    static QString operationProgressive(OperationKind kind);

    GrpcClient* m_grpc;
    SessionController* m_session;
    QMenu* m_menu;
    QStatusBar* m_statusBar;
    QWidget* m_parentWindow;

    QAction* m_statusAction = nullptr;
    QAction* m_installAction = nullptr;
    QAction* m_repairAction = nullptr;
    QAction* m_rollbackAction = nullptr;
    QAction* m_uninstallAction = nullptr;
    QAction* m_checkAction = nullptr;
    QTimer* m_pollTimer = nullptr;
    QTimer* m_phaseTimer = nullptr;
    QTimer* m_disconnectTimer = nullptr;
    ModLoaderProgressDialog* m_progress = nullptr;

    GameInfo m_game;
    std::optional<Operation> m_op;
    quint64 m_nextOperationSerial = 0;
    QHash<QString, GrpcModLoaderStatus> m_statuses;
    QHash<QString, QString> m_latestVersions;
    QHash<QString, quint64> m_statusFloor;
    QHash<QString, quint64> m_pendingStatus;
    QSet<QString> m_latestChecked;
    QSet<quint64> m_autoCheckIds;
    quint64 m_interactiveCheckId = 0;
};

}

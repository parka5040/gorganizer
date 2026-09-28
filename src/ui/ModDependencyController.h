#pragma once

#include <QDateTime>
#include <QHash>
#include <QObject>
#include <QSet>
#include <QString>
#include <QStringList>
#include <optional>
#include <vector>
#include "GameInfo.h"
#include "GrpcTypes.h"

class QStatusBar;
class QTimer;
class QWidget;

namespace gorganizer {

class GrpcClient;
class ModListWidget;
class SessionController;
class SmapiModsWidget;

class ModDependencyController : public QObject {
    Q_OBJECT
public:
    ModDependencyController(GrpcClient* grpc, SessionController* session, ModListWidget* modList,
                            SmapiModsWidget* smapiMods, QStatusBar* statusBar, QWidget* parentWindow);

public slots:
    // Tracks the active game and schedules a report, going online when this game's last smapi.io check is stale.
    void onActiveGameChanged(const GameInfo& game);
    // Tracks the active profile and schedules a report for it.
    void onProfileChanged(const QString& profileName);
    // Schedules an offline report after the active game's mod-loader status changed.
    void onModLoaderStatusChanged(const QString& gameId, const GrpcModLoaderStatus& status);

private slots:
    void onRefreshTimeout();
    // Schedules the offline report that retries an automatic enable whose mod-list save failed.
    void onRetryTimeout();
    // Applies the newest report of the current game and profile, dropping stale or superseded answers.
    void onReportReceived(quint64 requestId, const GrpcModDependencyReport& report);
    void onReportFailed(quint64 requestId, const QString& gameId, const QString& profileName, const QString& error);
    // Continues the enable job with the authoritative modlist it requested.
    void onModListReceived(quint64 requestId, const QString& gameId, const QString& profileName,
                           const std::vector<GrpcModListEntry>& entries);
    void onModListRequestFailed(quint64 requestId, const QString& gameId, const QString& profileName,
                                const QString& error);
    // Acknowledges the job's pending enables once its SetModList succeeded.
    void onModListSaved(quint64 requestId, const QString& gameId, const QString& profileName);
    // Ends the enable job without acknowledging its pending enables, retrying automatic jobs later.
    void onModListSaveFailed(quint64 requestId, const QString& gameId, const QString& profileName,
                             const QString& error);
    // Records the acknowledged pending enables so a stale report can never enable them again.
    void onEnableAcknowledged(quint64 requestId, const QString& gameId, const QString& batchId, int acknowledged);
    void onEnableAckFailed(quint64 requestId, const QString& gameId, const QString& batchId, const QString& error);
    // Reports what a dependency fetch did: queued downloads, pages to open, and IDs it could not fetch.
    void onFetchFinished(quint64 requestId, const QString& gameId, const QString& profileName,
                         const std::vector<GrpcDependencyFetchResult>& results);
    void onFetchFailed(quint64 requestId, const QString& gameId, const QString& profileName, const QString& error);
    // Reloads the mod list and schedules a report when a mod finished installing for the active game.
    void onInstallCompletedHint(const GrpcInstallCompleted& event);
    // Schedules an offline report after any mod-list change was persisted.
    void onModListPersisted();
    // Restarts an enable job that waited for the mod list's context menu or dialog to close.
    void onListInteractionFinished();
    // Restarts an enable job after the mod list finishes saving or reloads its saved state.
    void onModListReadyForEnable();
    void onModListAdopted(quint64 adoptionId);
    void onModListAdoptionDeferred(quint64 adoptionId);
    void onConnected();
    // Forgets every in-flight request once the client stopped its workers, since no answer can arrive any more.
    void onWorkersStopped();
    void onRefreshRequested();
    void onCheckUpdatesRequested();
    void onFetchMissingRequested();
    void onEnableRequiredRequested();
    // Opens the fetch dialog for the missing dependencies one mod needs.
    void onModFetchRequested(const QStringList& uniqueIds);
    // Asks to enable the disabled mods that satisfy one mod's dependencies.
    void onModEnableRequested(const QStringList& modNames);
    // Enables the downloaded dependencies whose automatic enable gave up, at the user's request.
    void onWaitingEnablesRequested();
    // Opens the fetch dialog again for a dependency whose download failed or expired.
    void onRetryFetchRequested(const QString& uniqueId);

private:
    enum class RefreshMode { None = -1, Offline = 0, RemoteIfStale = 1, Remote = 2, Force = 3 };

    struct ReportRequest {
        QString gameId;
        QString profileName;
        quint64 generation = 0;
        bool remote = false;
        QDateTime previousAttempt;
    };

    struct FetchRequest {
        quint64 requestId = 0;
        QString gameId;
        QString profileName;
        quint64 generation = 0;
    };

    struct EnableJob {
        enum class Stage { Loading, WaitingForList, WaitingForSaves, WaitingForAdoption, Saving, Acknowledging };
        Stage stage = Stage::Loading;
        quint64 generation = 0;
        QString gameId;
        QString profileName;
        QStringList modNames;
        QStringList enabledNames;
        std::vector<GrpcPendingEnable> pending;
        std::vector<GrpcPendingEnable> toAcknowledge;
        bool interactive = false;
        bool applied = false;
        bool saved = false;
        quint64 listRequestId = 0;
        quint64 listEditSerial = 0;
        std::vector<GrpcModListEntry> adoptionEntries;
        quint64 adoptionId = 0;
        quint64 saveRequestId = 0;
        QHash<quint64, QStringList> ackRequests;
        int acknowledged = 0;
    };

    // Reports whether the active game shows dependency features and a profile is selected.
    bool active() const;
    QString activeGameId() const;
    QString activeProfile() const;
    // Bumps the generation and drops the report, job and waiting answers when the game or profile changed.
    void syncContext();
    // Debounces a report request, keeping the strongest mode asked for meanwhile.
    void scheduleRefresh(RefreshMode mode, int delayMs = -1);
    // Sends one report request for the active game and profile, holding it back while this context's online report runs.
    void requestReport(RefreshMode mode);
    // Schedules the report held back while gameId's online report ran, asking again whether to go online when that answer was dropped.
    void releaseHeldRefresh(const QString& gameId, bool dropped);
    // Reports whether the active game's last smapi.io attempt is old enough to try again.
    bool remoteStale(const QString& gameId) const;
    void applyReport(const GrpcModDependencyReport& report);
    // Pushes the active game's online-check state to the SMAPI tab.
    void publishRemoteState();
    void updateActionsBusy();
    // Returns the report's pending enables of the current profile that no acknowledgement of this session covers.
    std::vector<GrpcPendingEnable> openPendingEnables() const;
    // Pushes the downloaded dependencies whose automatic enable gave up to the SMAPI tab.
    void publishWaitingEnables();
    // Notes failed or expired dependency downloads of the report that this session has not mentioned yet.
    void noteRecentFailures(const GrpcModDependencyReport& report);
    // Starts the level-triggered enable job for the report's pending enables of the current profile (I-46).
    void maybeAutoEnable();
    void startEnableJob(const QStringList& modNames, const std::vector<GrpcPendingEnable>& pending, bool interactive);
    // Requests the authoritative modlist the job adopts before enabling.
    void requestJobModList();
    // Reports whether the job still targets the active game and profile and the loaded mod list.
    bool jobCurrent() const;
    void applyJob(const std::vector<GrpcModListEntry>& entries);
    void finishApplyingJob(const std::vector<GrpcModListEntry>& entries);
    // Acknowledges the satisfied pending enables, one request per batch with at least one UniqueID.
    void acknowledgeJob();
    // Ends the enable job, reporting failure when given and either refreshing now or, with retryLater, only after the retry delay.
    void finishJob(const QString& failure = QString(), bool retryLater = false);
    // Shows the fetch dialog for candidates and sends the chosen IDs.
    void openFetchDialog(const std::vector<GrpcMissingDependency>& candidates);
    // Asks the user to confirm enabling modNames, then starts an enable job without acknowledgement.
    void confirmAndEnable(const QStringList& modNames);
    // Returns the disabled provider the current report lists for a dependency a fetch found present but disabled.
    QStringList disabledProvidersOf(const GrpcDependencyFetchResult& result) const;

    GrpcClient* m_grpc;
    SessionController* m_session;
    ModListWidget* m_modList;
    SmapiModsWidget* m_smapiMods;
    QStatusBar* m_statusBar;
    QWidget* m_parentWindow;
    QTimer* m_refreshTimer = nullptr;
    QTimer* m_retryTimer = nullptr;

    QString m_contextKey;
    quint64 m_generation = 0;
    RefreshMode m_pendingMode = RefreshMode::None;
    QHash<quint64, ReportRequest> m_reportRequests;
    quint64 m_appliedReportId = 0;
    std::optional<GrpcModDependencyReport> m_report;
    bool m_reportDuringJob = false;

    QHash<QString, QDateTime> m_remoteAttemptAt;
    QHash<QString, QDateTime> m_remoteCheckedAt;
    QHash<QString, QString> m_remoteError;
    QHash<QString, quint64> m_remoteInFlight;
    QHash<QString, RefreshMode> m_heldModes;

    std::optional<EnableJob> m_job;
    QHash<QString, int> m_autoEnableAttempts;
    QSet<QString> m_acknowledged;
    QSet<QString> m_notedFailures;
    std::optional<FetchRequest> m_fetch;
};

}

#include "ModDependencyController.h"
#include "DependencyFetchDialog.h"
#include "Dialogs.h"
#include "GrpcClient.h"
#include "InstallErrorText.h"
#include "ErrorPresenter.h"
#include "ModCatalog.h"
#include "ModDependencyText.h"
#include "ModListWidget.h"
#include "SessionController.h"
#include "SmapiModsWidget.h"
#include "SafeLinks.h"

#include <QMap>
#include <QMessageBox>
#include <QPushButton>
#include <QStatusBar>
#include <QTimer>
#include <algorithm>

namespace gorganizer {

namespace {

constexpr int kRefreshDebounceMs = 300;
constexpr qint64 kRemoteMaxAgeSecs = 60 * 60;
constexpr qint64 kRemoteRetrySecs = 5 * 60;
constexpr int kMaxAutoEnableAttempts = 3;
constexpr int kSaveRetryDelayMs = 10 * 1000;

QString pendingKeyOf(const QString& batchId, const QString& uniqueId)
{
    return batchId + QLatin1Char('\n') + uniqueId.trimmed().toCaseFolded();
}

QString pendingKey(const GrpcPendingEnable& pending)
{
    return pendingKeyOf(pending.batchId, pending.uniqueId);
}

QString bulletList(const QStringList& lines)
{
    QStringList out;
    out.reserve(lines.size());
    for (const auto& line : lines)
        out.append(QStringLiteral("• %1").arg(line));
    return out.join(QLatin1Char('\n'));
}

bool askPlain(QWidget* parent, const QString& title, const QString& text, const QString& acceptLabel)
{
    QMessageBox box(parent);
    box.setWindowTitle(title);
    box.setIcon(QMessageBox::Question);
    box.setTextFormat(Qt::PlainText);
    box.setText(text);
    QPushButton* accept = box.addButton(acceptLabel, QMessageBox::AcceptRole);
    box.addButton(QMessageBox::Cancel);
    box.setDefaultButton(accept);
    box.exec();
    return box.clickedButton() == accept;
}

}

ModDependencyController::ModDependencyController(GrpcClient* grpc, SessionController* session,
                                                 ModListWidget* modList, SmapiModsWidget* smapiMods,
                                                 QStatusBar* statusBar, QWidget* parentWindow)
    : QObject(parentWindow)
    , m_grpc(grpc)
    , m_session(session)
    , m_modList(modList)
    , m_smapiMods(smapiMods)
    , m_statusBar(statusBar)
    , m_parentWindow(parentWindow)
{
    m_refreshTimer = new QTimer(this);
    m_refreshTimer->setSingleShot(true);
    m_refreshTimer->setInterval(kRefreshDebounceMs);
    connect(m_refreshTimer, &QTimer::timeout, this, &ModDependencyController::onRefreshTimeout);
    m_retryTimer = new QTimer(this);
    m_retryTimer->setSingleShot(true);
    m_retryTimer->setInterval(kSaveRetryDelayMs);
    connect(m_retryTimer, &QTimer::timeout, this, &ModDependencyController::onRetryTimeout);

    connect(m_grpc, &GrpcClient::modDependencyReportReceived, this, &ModDependencyController::onReportReceived);
    connect(m_grpc, &GrpcClient::modDependencyReportFailed, this, &ModDependencyController::onReportFailed);
    connect(m_grpc, &GrpcClient::modListRequestReceived, this, &ModDependencyController::onModListReceived);
    connect(m_grpc, &GrpcClient::modListRequestFailed, this, &ModDependencyController::onModListRequestFailed);
    connect(m_grpc, &GrpcClient::modListSaved, this, &ModDependencyController::onModListSaved);
    connect(m_grpc, &GrpcClient::modListSaveFailed, this, &ModDependencyController::onModListSaveFailed);
    connect(m_grpc, &GrpcClient::dependencyEnableAcknowledged, this, &ModDependencyController::onEnableAcknowledged);
    connect(m_grpc, &GrpcClient::dependencyEnableAckFailed, this, &ModDependencyController::onEnableAckFailed);
    connect(m_grpc, &GrpcClient::modDependenciesFetched, this, &ModDependencyController::onFetchFinished);
    connect(m_grpc, &GrpcClient::modDependencyFetchFailed, this, &ModDependencyController::onFetchFailed);
    connect(m_grpc, &GrpcClient::installCompletedHintReceived, this, &ModDependencyController::onInstallCompletedHint);
    connect(m_grpc, &GrpcClient::modListUpdated, this, &ModDependencyController::onModListPersisted);
    connect(m_grpc, &GrpcClient::connected, this, &ModDependencyController::onConnected);
    connect(m_grpc, &GrpcClient::workersStopped, this, &ModDependencyController::onWorkersStopped);

    connect(m_modList, &ModListWidget::interactionFinished, this, &ModDependencyController::onListInteractionFinished);
    connect(m_modList, &ModListWidget::modListReadyForEnable, this,
            &ModDependencyController::onModListReadyForEnable);
    connect(m_modList, &ModListWidget::modsEdited, this, &ModDependencyController::onModListPersisted);
    connect(m_modList, &ModListWidget::dependencyFetchRequested, this,
            &ModDependencyController::onModFetchRequested, Qt::QueuedConnection);
    connect(m_modList, &ModListWidget::dependencyEnableRequested, this,
            &ModDependencyController::onModEnableRequested, Qt::QueuedConnection);

    connect(m_smapiMods, &SmapiModsWidget::refreshRequested, this, &ModDependencyController::onRefreshRequested);
    connect(m_smapiMods, &SmapiModsWidget::checkUpdatesRequested, this,
            &ModDependencyController::onCheckUpdatesRequested);
    connect(m_smapiMods, &SmapiModsWidget::fetchMissingRequested, this,
            &ModDependencyController::onFetchMissingRequested);
    connect(m_smapiMods, &SmapiModsWidget::enableRequiredRequested, this,
            &ModDependencyController::onEnableRequiredRequested);
    connect(m_smapiMods, &SmapiModsWidget::waitingEnablesRequested, this,
            &ModDependencyController::onWaitingEnablesRequested);
    connect(m_smapiMods, &SmapiModsWidget::retryFetchRequested, this,
            &ModDependencyController::onRetryFetchRequested, Qt::QueuedConnection);
}

bool ModDependencyController::active() const
{
    return showsModDependencies(m_session->activeGame()) && !m_session->currentProfile().isEmpty();
}

QString ModDependencyController::activeGameId() const
{
    return m_session->activeGame().shortName;
}

QString ModDependencyController::activeProfile() const
{
    return m_session->currentProfile();
}

void ModDependencyController::syncContext()
{
    const QString key = active() ? activeGameId() + QLatin1Char('\n') + activeProfile() : QString();
    if (key == m_contextKey)
        return;
    m_contextKey = key;
    ++m_generation;
    m_report.reset();
    m_job.reset();
    m_reportDuringJob = false;
    m_autoEnableAttempts.clear();
    m_retryTimer->stop();
    m_smapiMods->clearReport();
    m_modList->clearDependencyReport();
    updateActionsBusy();
    publishRemoteState();
}

void ModDependencyController::onActiveGameChanged(const GameInfo&)
{
    syncContext();
    scheduleRefresh(RefreshMode::RemoteIfStale);
}

void ModDependencyController::onProfileChanged(const QString&)
{
    syncContext();
    scheduleRefresh(RefreshMode::RemoteIfStale);
}

void ModDependencyController::onModLoaderStatusChanged(const QString& gameId, const GrpcModLoaderStatus&)
{
    if (gameId == activeGameId())
        scheduleRefresh(RefreshMode::Offline);
}

void ModDependencyController::scheduleRefresh(RefreshMode mode, int delayMs)
{
    if (!active())
        return;
    m_pendingMode = std::max(m_pendingMode, mode);
    m_refreshTimer->start(delayMs < 0 ? kRefreshDebounceMs : delayMs);
}

void ModDependencyController::onRefreshTimeout()
{
    syncContext();
    const RefreshMode mode = m_pendingMode;
    m_pendingMode = RefreshMode::None;
    if (mode == RefreshMode::None || !active() || !m_grpc->isConnected())
        return;
    requestReport(mode);
}

void ModDependencyController::onRetryTimeout()
{
    scheduleRefresh(RefreshMode::Offline, 0);
}

bool ModDependencyController::remoteStale(const QString& gameId) const
{
    const QDateTime attempt = m_remoteAttemptAt.value(gameId);
    if (!attempt.isValid())
        return true;
    const qint64 maxAge = m_remoteError.contains(gameId) ? kRemoteRetrySecs : kRemoteMaxAgeSecs;
    return attempt.secsTo(QDateTime::currentDateTimeUtc()) >= maxAge;
}

void ModDependencyController::requestReport(RefreshMode mode)
{
    const QString gameId = activeGameId();
    const QString profile = activeProfile();
    const auto flight = m_remoteInFlight.constFind(gameId);
    const bool inFlight = flight != m_remoteInFlight.constEnd();
    if (inFlight && (flight.value() == m_generation || mode >= RefreshMode::Remote))
        m_heldModes.insert(gameId, std::max(m_heldModes.value(gameId, RefreshMode::None), mode));
    if (inFlight && flight.value() == m_generation)
        return;
    const bool remote = !inFlight
        && (mode == RefreshMode::Remote || mode == RefreshMode::Force
            || (mode == RefreshMode::RemoteIfStale && remoteStale(gameId)));
    const bool force = remote && mode == RefreshMode::Force;

    const quint64 requestId = m_grpc->getModDependencyReport(gameId, profile, remote, force);
    m_reportRequests.insert(requestId,
                            ReportRequest{gameId, profile, m_generation, remote, m_remoteAttemptAt.value(gameId)});
    if (remote) {
        m_remoteInFlight.insert(gameId, m_generation);
        m_remoteAttemptAt.insert(gameId, QDateTime::currentDateTimeUtc());
        publishRemoteState();
    }
}

void ModDependencyController::releaseHeldRefresh(const QString& gameId, bool dropped)
{
    RefreshMode mode = m_heldModes.value(gameId, RefreshMode::None);
    m_heldModes.remove(gameId);
    if (!active() || activeGameId() != gameId)
        return;
    if (dropped)
        mode = std::max(mode, RefreshMode::RemoteIfStale);
    if (mode != RefreshMode::None)
        scheduleRefresh(mode, 0);
}

void ModDependencyController::onReportReceived(quint64 requestId, const GrpcModDependencyReport& report)
{
    const auto it = m_reportRequests.constFind(requestId);
    if (it == m_reportRequests.constEnd())
        return;
    const ReportRequest request = it.value();
    m_reportRequests.erase(it);
    const bool applicable = request.generation == m_generation && requestId >= m_appliedReportId
        && report.gameId == request.gameId && report.profileName == request.profileName;

    bool dropped = false;
    if (request.remote) {
        m_remoteInFlight.remove(request.gameId);
        if (report.remoteChecked && applicable) {
            m_remoteCheckedAt.insert(request.gameId, QDateTime::currentDateTimeUtc());
            m_remoteError.remove(request.gameId);
        } else if (report.remoteChecked) {
            dropped = true;
            if (request.previousAttempt.isValid())
                m_remoteAttemptAt.insert(request.gameId, request.previousAttempt);
            else
                m_remoteAttemptAt.remove(request.gameId);
        } else if (!report.remoteError.isEmpty()) {
            m_remoteError.insert(request.gameId, errorSummary("check SMAPI mod requirements", report.remoteError));
        }
        publishRemoteState();
    }
    if (applicable) {
        m_appliedReportId = requestId;
        applyReport(report);
    }
    if (request.remote)
        releaseHeldRefresh(request.gameId, dropped);
}

void ModDependencyController::onReportFailed(quint64 requestId, const QString&, const QString&, const QString& error)
{
    const auto it = m_reportRequests.constFind(requestId);
    if (it == m_reportRequests.constEnd())
        return;
    const ReportRequest request = it.value();
    m_reportRequests.erase(it);
    const QString message = errorSummary("check SMAPI mod requirements", error);
    if (request.remote) {
        m_remoteInFlight.remove(request.gameId);
        m_remoteError.insert(request.gameId, message);
        publishRemoteState();
    }
    if (request.generation == m_generation && requestId >= m_appliedReportId)
        m_smapiMods->setReportError(message);
    if (request.remote)
        releaseHeldRefresh(request.gameId, false);
}

void ModDependencyController::applyReport(const GrpcModDependencyReport& report)
{
    m_report = report;
    m_smapiMods->setReportError(QString());
    m_smapiMods->setReport(report);
    if (m_modList->loadedGameId() == report.gameId && m_modList->loadedProfileName() == report.profileName)
        m_modList->setDependencyReport(report);
    noteRecentFailures(report);
    if (m_job) {
        m_reportDuringJob = true;
        publishWaitingEnables();
        return;
    }
    maybeAutoEnable();
    publishWaitingEnables();
}

void ModDependencyController::publishRemoteState()
{
    const QString gameId = active() ? activeGameId() : QString();
    m_smapiMods->setRemoteState(m_remoteCheckedAt.value(gameId).toLocalTime(), m_remoteError.value(gameId),
                                m_remoteInFlight.contains(gameId));
}

void ModDependencyController::updateActionsBusy()
{
    m_smapiMods->setActionsBusy(m_job.has_value() || m_fetch.has_value());
}

std::vector<GrpcPendingEnable> ModDependencyController::openPendingEnables() const
{
    std::vector<GrpcPendingEnable> open;
    if (!m_report || !active())
        return open;
    const QString profile = activeProfile();
    if (m_report->gameId != activeGameId() || m_report->profileName != profile)
        return open;
    for (const auto& entry : m_report->pendingEnables) {
        if (entry.profileName != profile || entry.modName.isEmpty() || entry.uniqueId.trimmed().isEmpty()
            || entry.batchId.isEmpty() || m_acknowledged.contains(pendingKey(entry)))
            continue;
        open.push_back(entry);
    }
    return open;
}

void ModDependencyController::publishWaitingEnables()
{
    QStringList lines;
    if (m_report) {
        const QHash<QString, QString> names = dependencyNames(*m_report);
        for (const auto& entry : openPendingEnables()) {
            if (m_autoEnableAttempts.value(pendingKey(entry)) < kMaxAutoEnableAttempts)
                continue;
            lines.append(QStringLiteral("%1 — %2").arg(entry.modName, dependencyDisplayName(entry.uniqueId, names)));
        }
    }
    lines.removeDuplicates();
    m_smapiMods->setWaitingEnables(lines);
}

void ModDependencyController::noteRecentFailures(const GrpcModDependencyReport& report)
{
    int fresh = 0;
    for (const auto& issue : relevantRecentFailures(report)) {
        const QString key = pendingKeyOf(issue.batchId, issue.uniqueId) + QLatin1Char('\n')
            + issue.updatedAt.toString(Qt::ISODate);
        if (m_notedFailures.contains(key))
            continue;
        m_notedFailures.insert(key);
        ++fresh;
    }
    if (fresh > 0)
        m_statusBar->showMessage(QStringLiteral("%1 dependency download(s) failed or expired; see the SMAPI tab to "
                                                "retry.").arg(fresh),
                                 8000);
}

void ModDependencyController::maybeAutoEnable()
{
    if (!m_report || m_job || !active() || !m_grpc->isConnected())
        return;
    const QString profile = activeProfile();
    if (m_modList->loadedGameId() != activeGameId() || m_modList->loadedProfileName() != profile)
        return;
    std::vector<GrpcPendingEnable> pending;
    QStringList names;
    for (const auto& entry : openPendingEnables()) {
        if (m_autoEnableAttempts.value(pendingKey(entry)) >= kMaxAutoEnableAttempts)
            continue;
        pending.push_back(entry);
        if (!names.contains(entry.modName))
            names.append(entry.modName);
    }
    if (pending.empty())
        return;
    startEnableJob(names, pending, false);
}

void ModDependencyController::startEnableJob(const QStringList& modNames, const std::vector<GrpcPendingEnable>& pending,
                                             bool interactive)
{
    EnableJob job;
    job.generation = m_generation;
    job.gameId = activeGameId();
    job.profileName = activeProfile();
    job.modNames = modNames;
    job.pending = pending;
    job.interactive = interactive;
    m_job = job;
    m_reportDuringJob = false;
    updateActionsBusy();
    requestJobModList();
}

void ModDependencyController::requestJobModList()
{
    if (!m_job)
        return;
    if (!m_modList->readyForDependencyEnable()) {
        m_job->stage = EnableJob::Stage::WaitingForSaves;
        return;
    }
    m_job->stage = EnableJob::Stage::Loading;
    m_job->listEditSerial = m_modList->editSerial();
    m_job->listRequestId = m_grpc->getModListTracked(m_job->gameId, m_job->profileName);
}

bool ModDependencyController::jobCurrent() const
{
    return m_job && m_job->generation == m_generation && active() && activeGameId() == m_job->gameId
        && activeProfile() == m_job->profileName && m_modList->loadedGameId() == m_job->gameId
        && m_modList->loadedProfileName() == m_job->profileName;
}

void ModDependencyController::onModListReceived(quint64 requestId, const QString&, const QString&,
                                                const std::vector<GrpcModListEntry>& entries)
{
    if (!m_job || m_job->stage != EnableJob::Stage::Loading || requestId != m_job->listRequestId)
        return;
    syncContext();
    if (!jobCurrent()) {
        finishJob();
        return;
    }
    if (m_modList->isInteracting()) {
        m_job->stage = EnableJob::Stage::WaitingForList;
        return;
    }
    if (m_modList->editSerial() != m_job->listEditSerial || !m_modList->readyForDependencyEnable()) {
        requestJobModList();
        return;
    }
    applyJob(entries);
}

void ModDependencyController::onModListRequestFailed(quint64 requestId, const QString&, const QString&,
                                                     const QString& error)
{
    if (!m_job || m_job->stage != EnableJob::Stage::Loading || requestId != m_job->listRequestId)
        return;
    finishJob(errorSummary("read the mod list to enable required mods", error));
}

void ModDependencyController::onListInteractionFinished()
{
    if (m_job && (m_job->stage == EnableJob::Stage::WaitingForList
                  || m_job->stage == EnableJob::Stage::WaitingForSaves))
        requestJobModList();
}

void ModDependencyController::onModListReadyForEnable()
{
    if (m_job && m_job->stage == EnableJob::Stage::WaitingForSaves) {
        syncContext();
        if (jobCurrent())
            requestJobModList();
        else
            finishJob();
    }
}

void ModDependencyController::applyJob(const std::vector<GrpcModListEntry>& entries)
{
    QHash<QString, bool> authoritative;
    for (const auto& entry : entries) {
        if (!authoritative.contains(entry.modName))
            authoritative.insert(entry.modName, entry.enabled);
    }
    if (!m_modList->adoptModList(entries)) {
        finishJob(QStringLiteral("The mod list is not loaded, so no dependency was enabled."));
        return;
    }

    QStringList toEnable;
    for (const auto& name : m_job->modNames) {
        if (m_modList->containsMod(name) && !toEnable.contains(name))
            toEnable.append(name);
    }
    QStringList changed;
    const quint64 saveId = m_modList->enableModsInProfile(entries, toEnable, &changed);
    m_job->applied = true;
    m_job->enabledNames = changed;
    for (const auto& entry : m_job->pending) {
        m_autoEnableAttempts[pendingKey(entry)] += 1;
        if (!toEnable.contains(entry.modName))
            continue;
        if (saveId == 0 && !authoritative.value(entry.modName, false))
            continue;
        m_job->toAcknowledge.push_back(entry);
    }
    if (saveId != 0) {
        m_job->stage = EnableJob::Stage::Saving;
        m_job->saveRequestId = saveId;
        return;
    }
    acknowledgeJob();
}

void ModDependencyController::onModListSaved(quint64 requestId, const QString&, const QString&)
{
    if (!m_job || m_job->stage != EnableJob::Stage::Saving || requestId != m_job->saveRequestId)
        return;
    syncContext();
    if (!jobCurrent()) {
        finishJob();
        return;
    }
    m_job->saved = true;
    acknowledgeJob();
}

void ModDependencyController::onModListSaveFailed(quint64 requestId, const QString&, const QString&,
                                                  const QString& error)
{
    if (!m_job || m_job->stage != EnableJob::Stage::Saving || requestId != m_job->saveRequestId)
        return;
    syncContext();
    if (!jobCurrent()) {
        finishJob();
        return;
    }
    if (!m_job->interactive) {
        finishJob(errorSummary("enable downloaded dependencies", error, true)
                      + QStringLiteral(" Gorganizer will try again shortly."), true);
        return;
    }
    finishJob(errorSummary("enable required mods", error, true));
    presentError(m_parentWindow, QStringLiteral("Enable Required Dependencies"),
                 QStringLiteral("enable required mods"), error, true);
}

void ModDependencyController::acknowledgeJob()
{
    if (!m_job)
        return;
    QMap<QString, QStringList> byBatch;
    for (const auto& entry : m_job->toAcknowledge) {
        const QString id = entry.uniqueId.trimmed();
        if (entry.batchId.isEmpty() || id.isEmpty())
            continue;
        QStringList& ids = byBatch[entry.batchId];
        if (!ids.contains(id, Qt::CaseInsensitive))
            ids.append(id);
    }
    if (byBatch.isEmpty()) {
        finishJob();
        return;
    }
    m_job->stage = EnableJob::Stage::Acknowledging;
    for (auto it = byBatch.cbegin(); it != byBatch.cend(); ++it) {
        QStringList keys;
        for (const auto& id : it.value())
            keys.append(pendingKeyOf(it.key(), id));
        m_job->ackRequests.insert(m_grpc->ackDependencyEnable(m_job->gameId, it.key(), it.value()), keys);
    }
}

void ModDependencyController::onEnableAcknowledged(quint64 requestId, const QString&, const QString&, int acknowledged)
{
    if (!m_job || m_job->stage != EnableJob::Stage::Acknowledging)
        return;
    const auto it = m_job->ackRequests.constFind(requestId);
    if (it == m_job->ackRequests.constEnd())
        return;
    for (const auto& key : it.value())
        m_acknowledged.insert(key);
    m_job->ackRequests.erase(it);
    m_job->acknowledged += acknowledged;
    if (m_job->ackRequests.isEmpty())
        finishJob();
}

void ModDependencyController::onEnableAckFailed(quint64 requestId, const QString&, const QString&, const QString& error)
{
    if (!m_job || m_job->stage != EnableJob::Stage::Acknowledging || !m_job->ackRequests.remove(requestId))
        return;
    m_statusBar->showMessage(errorSummary("record enabled dependencies", error, true), 6000);
    if (m_job->ackRequests.isEmpty())
        finishJob();
}

void ModDependencyController::finishJob(const QString& failure, bool retryLater)
{
    if (!m_job)
        return;
    const EnableJob job = *m_job;
    m_job.reset();
    const bool reportArrived = m_reportDuringJob;
    m_reportDuringJob = false;
    updateActionsBusy();

    if (!failure.isEmpty()) {
        m_statusBar->showMessage(failure, 8000);
    } else if (job.saved) {
        m_statusBar->showMessage(QStringLiteral("Enabled %1 in profile \"%2\".")
                                     .arg(job.enabledNames.join(QStringLiteral(", ")), job.profileName),
                                 6000);
    } else if (job.interactive && job.applied) {
        m_statusBar->showMessage(QStringLiteral("The required mods are already enabled."), 5000);
    }
    publishWaitingEnables();
    if (retryLater)
        m_retryTimer->start();
    else if (job.acknowledged > 0 || reportArrived)
        scheduleRefresh(RefreshMode::Offline);
}

void ModDependencyController::onInstallCompletedHint(const GrpcInstallCompleted& event)
{
    if (!active() || event.gameId != activeGameId())
        return;
    QSet<QString> batches(event.batchIds.begin(), event.batchIds.end());
    if (!event.batchId.isEmpty())
        batches.insert(event.batchId);
    batches.remove(QString());
    for (auto it = m_autoEnableAttempts.begin(); it != m_autoEnableAttempts.end();) {
        if (batches.contains(it.key().section(QLatin1Char('\n'), 0, 0)))
            it = m_autoEnableAttempts.erase(it);
        else
            ++it;
    }
    m_modList->reloadMods();
    scheduleRefresh(RefreshMode::Offline, batches.isEmpty() ? -1 : 0);
}

void ModDependencyController::onModListPersisted()
{
    scheduleRefresh(RefreshMode::Offline);
}

void ModDependencyController::onConnected()
{
    scheduleRefresh(RefreshMode::RemoteIfStale);
}

void ModDependencyController::onWorkersStopped()
{
    m_refreshTimer->stop();
    m_retryTimer->stop();
    m_pendingMode = RefreshMode::None;
    m_reportRequests.clear();
    m_remoteInFlight.clear();
    m_heldModes.clear();
    m_job.reset();
    m_reportDuringJob = false;
    m_fetch.reset();
    updateActionsBusy();
    publishRemoteState();
}

void ModDependencyController::onRefreshRequested()
{
    m_autoEnableAttempts.clear();
    publishWaitingEnables();
    scheduleRefresh(RefreshMode::Offline, 0);
}

void ModDependencyController::onCheckUpdatesRequested()
{
    m_autoEnableAttempts.clear();
    publishWaitingEnables();
    scheduleRefresh(RefreshMode::Force, 0);
}

void ModDependencyController::onFetchMissingRequested()
{
    if (!m_report)
        return;
    std::vector<GrpcMissingDependency> candidates;
    for (const auto& dep : m_report->missing) {
        if (dep.disabledProviders.isEmpty())
            candidates.push_back(dep);
    }
    openFetchDialog(candidates);
}

void ModDependencyController::onModFetchRequested(const QStringList& uniqueIds)
{
    if (!m_report)
        return;
    QSet<QString> wanted;
    for (const auto& id : uniqueIds)
        wanted.insert(id.toCaseFolded());
    std::vector<GrpcMissingDependency> candidates;
    for (const auto& dep : m_report->missing) {
        if (dep.disabledProviders.isEmpty() && wanted.contains(dep.uniqueId.toCaseFolded()))
            candidates.push_back(dep);
    }
    openFetchDialog(candidates);
}

void ModDependencyController::onRetryFetchRequested(const QString& uniqueId)
{
    onModFetchRequested(QStringList{uniqueId});
}

void ModDependencyController::openFetchDialog(const std::vector<GrpcMissingDependency>& candidates)
{
    if (!active() || !m_report)
        return;
    if (candidates.empty()) {
        dialogs::plainInfo(m_parentWindow, QStringLiteral("Fetch Missing Dependencies"),
                           QStringLiteral("No missing dependency needs to be downloaded."));
        return;
    }
    if (m_fetch) {
        m_statusBar->showMessage(QStringLiteral("A dependency fetch is still running; try again when it finishes."), 5000);
        return;
    }
    if (!m_grpc->isConnected()) {
        dialogs::plainWarn(m_parentWindow, QStringLiteral("Fetch Missing Dependencies"),
                           QStringLiteral("The gorganizer daemon must be running to fetch dependencies."));
        return;
    }

    const quint64 generation = m_generation;
    const QString gameId = activeGameId();
    const QString profile = activeProfile();
    DependencyFetchDialog dlg(candidates, dependencyNames(*m_report), profile, m_parentWindow);
    if (dlg.exec() != QDialog::Accepted)
        return;
    const QStringList ids = dlg.selectedIds();
    if (ids.isEmpty())
        return;
    syncContext();
    if (generation != m_generation || !active() || activeGameId() != gameId || activeProfile() != profile) {
        dialogs::plainInfo(m_parentWindow, QStringLiteral("Fetch Missing Dependencies"),
                           QStringLiteral("The game or profile changed while the dialog was open, so nothing was "
                                          "fetched."));
        return;
    }
    if (m_fetch || !m_grpc->isConnected())
        return;
    m_fetch = FetchRequest{m_grpc->fetchModDependencies(gameId, profile, ids), gameId, profile, generation};
    updateActionsBusy();
    m_statusBar->showMessage(QStringLiteral("Requesting %1 dependencies…").arg(ids.size()));
}

QStringList ModDependencyController::disabledProvidersOf(const GrpcDependencyFetchResult& result) const
{
    if (!m_report)
        return {};
    for (const auto& dep : m_report->missing) {
        if (dep.uniqueId.compare(result.uniqueId, Qt::CaseInsensitive) != 0 || dep.disabledProviders.isEmpty())
            continue;
        const QString& provider = dep.disabledProviders.front();
        if (provider.isEmpty() || provider == QLatin1String(kOverwriteModName))
            return {};
        return QStringList{provider};
    }
    return {};
}

void ModDependencyController::onFetchFinished(quint64 requestId, const QString&, const QString&,
                                              const std::vector<GrpcDependencyFetchResult>& results)
{
    if (!m_fetch || requestId != m_fetch->requestId)
        return;
    const FetchRequest fetch = *m_fetch;
    m_fetch.reset();
    updateActionsBusy();
    scheduleRefresh(RefreshMode::Offline);

    const QHash<QString, QString> names = m_report ? dependencyNames(*m_report) : QHash<QString, QString>{};
    int queued = 0;
    int alreadyRunning = 0;
    QStringList urls;
    QStringList openReasons;
    QStringList notFetched;
    QStringList disabledProviders;
    for (const auto& result : results) {
        const QString label = dependencyDisplayName(result.uniqueId, names);
        const QString code = dependencyFetchReasonCode(result.reason);
        switch (result.outcome) {
        case GrpcFetchOutcomeQueued:
            ++queued;
            if (code == QLatin1String("in_flight") || code == QLatin1String("installing"))
                ++alreadyRunning;
            break;
        case GrpcFetchOutcomeOpenUrl:
            if (result.url.startsWith(QLatin1String("https://")) && !urls.contains(result.url))
                urls.append(result.url);
            else if (!result.url.startsWith(QLatin1String("https://")))
                notFetched.append(QStringLiteral("%1: no Nexus page to open.").arg(label));
            if (const QString why = dependencyFetchReasonText(result.reason);
                !why.isEmpty() && !openReasons.contains(why))
                openReasons.append(why);
            break;
        case GrpcFetchOutcomeAlreadyPresent:
            if (code == QLatin1String("disabled")) {
                const QStringList providers = disabledProvidersOf(result);
                if (providers.isEmpty()) {
                    notFetched.append(QStringLiteral("%1: %2 Use \"Enable Required\" once the SMAPI tab refreshed.")
                                          .arg(label, dependencyFetchReasonText(result.reason)));
                    break;
                }
                for (const auto& provider : providers) {
                    if (!disabledProviders.contains(provider))
                        disabledProviders.append(provider);
                }
                break;
            }
            if (code == QLatin1String("pending_enable"))
                m_autoEnableAttempts.remove(pendingKeyOf(result.batchId, result.uniqueId));
            notFetched.append(QStringLiteral("%1: %2").arg(label, dependencyFetchReasonText(result.reason)));
            break;
        default:
            notFetched.append(QStringLiteral("%1: %2").arg(
                label, result.reason.isEmpty() ? QStringLiteral("It could not be fetched.")
                                               : dependencyFetchReasonText(result.reason)));
            break;
        }
    }

    if (queued > 0) {
        QString note = QStringLiteral("Downloading %1 dependencies; they are installed and enabled when the "
                                      "downloads finish.").arg(queued);
        if (alreadyRunning > 0)
            note += QStringLiteral(" %1 of them were already downloading or installing.").arg(alreadyRunning);
        m_statusBar->showMessage(note, 10000);
    } else {
        m_statusBar->clearMessage();
    }

    if (!urls.isEmpty()) {
        QString text = QStringLiteral("Open %1 Nexus Mods page(s) in your browser?\n\nUse \"Mod Manager Download\" on "
                                      "each; gorganizer installs the downloads and enables them in profile \"%2\".")
                           .arg(urls.size())
                           .arg(fetch.profileName);
        if (!openReasons.isEmpty())
            text += QStringLiteral("\n\nWhy not automatically: %1").arg(openReasons.join(QLatin1Char(' ')));
        if (askPlain(m_parentWindow, QStringLiteral("Open Nexus Mods Pages"), text, QStringLiteral("Open Pages"))) {
            for (const auto& url : urls)
                openWebLink(m_parentWindow, url);
        }
    }

    if (!notFetched.isEmpty())
        dialogs::plainInfo(m_parentWindow, QStringLiteral("Dependencies Not Fetched"),
                           QStringLiteral("These dependencies were not fetched:\n\n%1").arg(bulletList(notFetched)));

    if (!disabledProviders.isEmpty()) {
        syncContext();
        if (fetch.generation != m_generation || !active())
            return;
        if (!askPlain(m_parentWindow, QStringLiteral("Enable Required Dependencies"),
                      QStringLiteral("These required mods are already installed but disabled:\n\n%1\n\nEnable them "
                                     "in profile \"%2\" instead of downloading them again?")
                          .arg(bulletList(disabledProviders), fetch.profileName),
                      QStringLiteral("Enable")))
            return;
        syncContext();
        if (fetch.generation != m_generation || !active())
            return;
        if (m_job) {
            m_statusBar->showMessage(QStringLiteral("Mods are already being enabled; try again in a moment."), 5000);
            return;
        }
        startEnableJob(disabledProviders, {}, true);
    }
}

void ModDependencyController::onFetchFailed(quint64 requestId, const QString&, const QString&, const QString& error)
{
    if (!m_fetch || requestId != m_fetch->requestId)
        return;
    m_fetch.reset();
    updateActionsBusy();
    m_statusBar->clearMessage();
    presentError(m_parentWindow, QStringLiteral("Fetch Missing Dependencies"),
                 QStringLiteral("fetch missing dependencies"), error, true);
}

void ModDependencyController::onEnableRequiredRequested()
{
    if (!m_report)
        return;
    confirmAndEnable(disabledProvidersToEnable(m_report->missing));
}

void ModDependencyController::onModEnableRequested(const QStringList& modNames)
{
    confirmAndEnable(modNames);
}

void ModDependencyController::onWaitingEnablesRequested()
{
    if (!active() || !m_report)
        return;
    if (m_job) {
        m_statusBar->showMessage(QStringLiteral("Mods are already being enabled; try again in a moment."), 5000);
        return;
    }
    if (!m_grpc->isConnected()) {
        dialogs::plainWarn(m_parentWindow, QStringLiteral("Enable Downloaded Dependencies"),
                           QStringLiteral("The gorganizer daemon must be running to change the mod list."));
        return;
    }
    std::vector<GrpcPendingEnable> pending;
    QStringList names;
    for (const auto& entry : openPendingEnables()) {
        m_autoEnableAttempts.remove(pendingKey(entry));
        pending.push_back(entry);
        if (!names.contains(entry.modName))
            names.append(entry.modName);
    }
    publishWaitingEnables();
    if (pending.empty())
        return;
    startEnableJob(names, pending, true);
}

void ModDependencyController::confirmAndEnable(const QStringList& modNames)
{
    if (!active() || modNames.isEmpty())
        return;
    if (m_job) {
        m_statusBar->showMessage(QStringLiteral("Mods are already being enabled; try again in a moment."), 5000);
        return;
    }
    if (!m_grpc->isConnected()) {
        dialogs::plainWarn(m_parentWindow, QStringLiteral("Enable Required Dependencies"),
                           QStringLiteral("The gorganizer daemon must be running to change the mod list."));
        return;
    }
    const quint64 generation = m_generation;
    const QString profile = activeProfile();
    if (!askPlain(m_parentWindow, QStringLiteral("Enable Required Dependencies"),
                  QStringLiteral("Enable these installed mods in profile \"%1\"? Other mods need them.\n\n%2")
                      .arg(profile, bulletList(modNames)),
                  QStringLiteral("Enable")))
        return;
    syncContext();
    if (generation != m_generation || !active() || m_job)
        return;
    startEnableJob(modNames, {}, true);
}

}

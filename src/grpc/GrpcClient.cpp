#include "GrpcClient.h"
#include "GrpcDeadline.h"
#include "GrpcInstallPreview.h"
#include "GrpcStatus.h"
#include "GrpcWorker.h"
#include "gorganizer.grpc.pb.h"

#include <grpcpp/grpcpp.h>
#include <QDir>
#include <QElapsedTimer>
#include <QFileInfo>
#include <QMetaType>
#include <QThread>
#include <algorithm>
#include <chrono>
#include <cstdlib>
#include <unistd.h>

namespace gorganizer {

using Stub = gorganizer::v1::Gorganizer::Stub;

struct GrpcSyncStub {
    explicit GrpcSyncStub(const std::shared_ptr<grpc::Channel>& channel)
        : stub(gorganizer::v1::Gorganizer::NewStub(channel))
    {
    }

    std::unique_ptr<Stub> stub;
};

namespace {

// socketPath returns the daemon socket path for the current GUI session.
QString socketPath()
{
    if (qgetenv("GORGANIZER_SUPERVISED") == "1") {
        QByteArray configured = qgetenv("GORGANIZER_SOCKET");
        if (!configured.isEmpty())
            return QString::fromUtf8(configured);
    }
    const char* xdg = std::getenv("XDG_RUNTIME_DIR");
    if (xdg && xdg[0])
        return QString::fromUtf8(xdg) + "/gorganizer/gorganizer.sock";
    return QDir::tempPath() + "/gorganizer-" + QString::number(getuid()) + "/gorganizer.sock";
}

template <typename Req, typename Resp, typename Method>
grpc::Status invokeUnary(GrpcSyncStub* stub, Method method, const Req& req, Resp& resp,
                         std::chrono::milliseconds deadline = kDefaultUnaryTimeout)
{
    if (!stub) return grpc::Status(grpc::StatusCode::UNAVAILABLE, "not connected");
    grpc::ClientContext ctx;
    setUnaryDeadline(ctx, deadline);
    return ((*stub->stub).*method)(&ctx, req, &resp);
}

bool mapError(const grpc::Status& status, const QString& method, GrpcError& errorOut)
{
    errorOut = grpcErrorFromStatus(status, method);
    return status.ok();
}

GrpcArchiveRow archiveRowFromProto(const gorganizer::v1::ArchiveRow& r)
{
    GrpcArchiveRow row;
    row.archiveRelPath = QString::fromStdString(r.archive_rel_path());
    row.modId = r.mod_id();
    row.fileId = r.file_id();
    row.modName = QString::fromStdString(r.mod_name());
    row.fileName = QString::fromStdString(r.file_name());
    row.fileArchiveName = QString::fromStdString(r.file_archive_name());
    row.version = QString::fromStdString(r.version());
    row.category = QString::fromStdString(r.category());
    row.sizeBytes = r.size_bytes();
    row.uploadedAt = QString::fromStdString(r.uploaded_at());
    row.downloadedAt = QString::fromStdString(r.downloaded_at());
    row.hidden = r.hidden();
    row.gameDomain = QString::fromStdString(r.game_domain());
    row.thumbnailUrl = QString::fromStdString(r.thumbnail_url());
    row.adultContent = r.adult_content();
    row.status = static_cast<int>(r.status());
    row.installedModFolder = QString::fromStdString(r.installed_mod_folder());
    row.downloadId = QString::fromStdString(r.download_id());
    row.bytesDownloaded = r.bytes_downloaded();
    row.queuedAhead = r.queued_ahead();
    row.merged = r.merged();
    return row;
}

GrpcExecutable execFromProto(const gorganizer::v1::Executable& e)
{
    GrpcExecutable g;
    g.id = QString::fromStdString(e.id());
    g.title = QString::fromStdString(e.title());
    g.exePath = QString::fromStdString(e.exe_path());
    g.toolId = QString::fromStdString(e.tool_id());
    g.runner = QString::fromStdString(e.runner());
    for (const auto& a : e.args()) g.args << QString::fromStdString(a);
    for (const auto& [key, value] : e.environment())
        g.environment.insert(QString::fromStdString(key), QString::fromStdString(value));
    g.workingDir = QString::fromStdString(e.working_dir());
    g.prefixAppId = e.prefix_app_id();
    g.outputPolicy = QString::fromStdString(e.output_policy());
    g.selectedInput = QString::fromStdString(e.selected_input());
    g.needsVfsMounted = e.needs_vfs_mounted();
    g.captureOutputToMod = QString::fromStdString(e.capture_output_to_mod());
    g.sanitizeEnv = e.sanitize_env();
    for (const auto& p : e.extra_rw_paths()) g.extraRwPaths << QString::fromStdString(p);
    g.autoDetected = e.auto_detected();
    return g;
}

void execToProto(const GrpcExecutable& g, gorganizer::v1::Executable* e)
{
    e->set_id(g.id.toStdString());
    e->set_title(g.title.toStdString());
    e->set_exe_path(g.exePath.toStdString());
    e->set_tool_id(g.toolId.toStdString());
    e->set_runner(g.runner.toStdString());
    for (const auto& a : g.args) e->add_args(a.toStdString());
    for (auto it = g.environment.cbegin(); it != g.environment.cend(); ++it)
        (*e->mutable_environment())[it.key().toStdString()] = it.value().toStdString();
    e->set_working_dir(g.workingDir.toStdString());
    e->set_prefix_app_id(g.prefixAppId);
    e->set_output_policy(g.outputPolicy.toStdString());
    e->set_selected_input(g.selectedInput.toStdString());
    e->set_needs_vfs_mounted(g.needsVfsMounted);
    e->set_capture_output_to_mod(g.captureOutputToMod.toStdString());
    e->set_sanitize_env(g.sanitizeEnv);
    for (const auto& p : g.extraRwPaths) e->add_extra_rw_paths(p.toStdString());
    e->set_auto_detected(g.autoDetected);
}

GrpcProfileIniStatus iniStatusFromProto(const gorganizer::v1::ProfileIniStatus& s)
{
    GrpcProfileIniStatus out;
    out.gameId = QString::fromStdString(s.game_id());
    out.profileName = QString::fromStdString(s.profile_name());
    out.useCustomIni = s.use_custom_ini();
    out.myGamesDir = QString::fromStdString(s.my_games_dir());
    out.gameSupportsIni = s.game_supports_ini();
    return out;
}

GrpcIniTweakState iniTweakFromProto(const gorganizer::v1::IniTweakState& t)
{
    GrpcIniTweakState out;
    out.id = QString::fromStdString(t.id());
    out.name = QString::fromStdString(t.name());
    out.description = QString::fromStdString(t.description());
    out.targetFile = QString::fromStdString(t.target_file());
    out.enabled = t.enabled();
    return out;
}

GrpcImportPreview importPreviewFromProto(const gorganizer::v1::PreviewImportResponse& r)
{
    GrpcImportPreview out;
    out.schemaVersion = r.schema_version();
    out.gorganizerVersion = QString::fromStdString(r.gorganizer_version());
    out.gameId = QString::fromStdString(r.game_id());
    out.exportedAt = QString::fromStdString(r.exported_at());
    for (const auto& m : r.mods()) {
        GrpcTransferModEntry e;
        e.folder = QString::fromStdString(m.folder());
        e.name = QString::fromStdString(m.name());
        e.fileCount = m.file_count();
        e.totalBytes = m.total_bytes();
        e.nexusModId = m.nexus_mod_id();
        e.nexusFileId = m.nexus_file_id();
        e.collision = m.collision();
        out.mods.push_back(std::move(e));
    }
    for (const auto& p : r.profiles()) {
        out.profiles.push_back(GrpcTransferProfileEntry{
            QString::fromStdString(p.name()),
            p.collision(),
        });
    }
    out.includesOverwrite = r.includes_overwrite();
    out.includesGameSettings = r.includes_game_settings();
    out.archiveIdentity = QString::fromStdString(r.archive_identity());
    return out;
}

}

GrpcClient::GrpcClient(QObject* parent)
    : QObject(parent)
{
    qRegisterMetaType<GrpcPreviewInstallResult>();
    qRegisterMetaType<GrpcReinstallResult>();
    qRegisterMetaType<GrpcInstallOutcome>();
    qRegisterMetaType<GrpcIniSaveResult>();
    qRegisterMetaType<QStringList>();
    qRegisterMetaType<GrpcRecoveryPending>();
    qRegisterMetaType<GrpcUpdateCheck>();
    qRegisterMetaType<quint64>();
    m_connectionTimer = new QTimer(this);
    m_connectionTimer->setInterval(5000);
    connect(m_connectionTimer, &QTimer::timeout, this, &GrpcClient::onCheckConnection);
}

GrpcClient::~GrpcClient()
{
    disconnectFromDaemon();
}

std::string GrpcClient::socketTarget() const
{
    return "unix://" + socketPath().toStdString();
}

void GrpcClient::connectWorkerSignals(GrpcWorker* worker)
{
    const quint64 generation = m_connectionGeneration;
    connect(worker, &GrpcWorker::gamesListed, this, &GrpcClient::gamesListed);
    connect(worker, &GrpcWorker::gamesDetected, this, &GrpcClient::gamesDetected);
    connect(worker, &GrpcWorker::gameConfigured, this, &GrpcClient::gameConfigured);
    connect(worker, &GrpcWorker::gameConfigurationFinished, this, &GrpcClient::gameConfigurationFinished);
    connect(worker, &GrpcWorker::modsListed, this, &GrpcClient::modsListed);
    connect(worker, &GrpcWorker::modInfoReceived, this, &GrpcClient::modInfoReceived);
    connect(worker, &GrpcWorker::profilesListed, this, &GrpcClient::profilesListed);
    connect(worker, &GrpcWorker::profileCreated, this, &GrpcClient::profileCreated);
    connect(worker, &GrpcWorker::profileCopied, this, &GrpcClient::profileCopied);
    connect(worker, &GrpcWorker::profileDeleted, this, &GrpcClient::profileDeleted);
    connect(worker, &GrpcWorker::modListReceived, this, &GrpcClient::modListReceived);
    connect(worker, &GrpcWorker::modListUpdated, this, &GrpcClient::modListUpdated);
    connect(worker, &GrpcWorker::vfsMounted, this, &GrpcClient::vfsMounted);
    connect(worker, &GrpcWorker::vfsRetargeted, this, &GrpcClient::vfsRetargeted);
    connect(worker, &GrpcWorker::vfsRetargetFailed, this, &GrpcClient::vfsRetargetFailed);
    connect(worker, &GrpcWorker::vfsUnmounted, this, &GrpcClient::vfsUnmounted);
    connect(worker, &GrpcWorker::vfsStatusReceived, this, &GrpcClient::vfsStatusReceived);
    connect(worker, &GrpcWorker::vfsRecoveryRetried, this, &GrpcClient::vfsRecoveryRetried);
    connect(worker, &GrpcWorker::vfsStatusQueried, this, &GrpcClient::vfsStatusQueried);
    connect(worker, &GrpcWorker::vfsStatusQueryFailed, this, &GrpcClient::vfsStatusQueryFailed);
    connect(worker, &GrpcWorker::steamMaintenanceSet, this, &GrpcClient::steamMaintenanceSet);
    connect(worker, &GrpcWorker::steamMaintenanceSetFailed, this, &GrpcClient::steamMaintenanceSetFailed);
    connect(worker, &GrpcWorker::preservedFilesImported, this, &GrpcClient::preservedFilesImported);
    connect(worker, &GrpcWorker::preservedFilesImportFailed, this, &GrpcClient::preservedFilesImportFailed);
    connect(worker, &GrpcWorker::preservedBatchDeleted, this, &GrpcClient::preservedBatchDeleted);
    connect(worker, &GrpcWorker::preservedBatchDeleteFailed, this, &GrpcClient::preservedBatchDeleteFailed);
    connect(worker, &GrpcWorker::maintenanceUnmountFinished, this, &GrpcClient::maintenanceUnmountFinished);
    connect(worker, &GrpcWorker::vfsRebuilt, this, &GrpcClient::vfsRebuilt);
    connect(worker, &GrpcWorker::conflictsReceived, this, &GrpcClient::conflictsReceived);
    connect(worker, &GrpcWorker::gameLaunched, this, &GrpcClient::gameLaunched);
    connect(worker, &GrpcWorker::gameLaunchFailed, this, &GrpcClient::gameLaunchFailed);
    connect(worker, &GrpcWorker::downloadStarted, this, &GrpcClient::downloadStarted);
    connect(worker, &GrpcWorker::downloadCancelled, this, &GrpcClient::downloadCancelled);
    connect(worker, &GrpcWorker::downloadRetried, this, &GrpcClient::downloadRetried);
    connect(worker, &GrpcWorker::previewInstallCompleted, this, &GrpcClient::previewInstallCompleted);
    connect(worker, &GrpcWorker::previewInstallFailed, this, &GrpcClient::previewInstallFailed);
    connect(worker, &GrpcWorker::installRequestCompleted, this, &GrpcClient::installRequestCompleted);
    connect(worker, &GrpcWorker::installRequestFailed, this, &GrpcClient::installRequestFailed);
    connect(worker, &GrpcWorker::reinstallRequestFailed, this, &GrpcClient::reinstallRequestFailed);
    connect(worker, &GrpcWorker::installOutcomeReceived, this, &GrpcClient::installOutcomeReceived);
    connect(worker, &GrpcWorker::installOutcomeFailed, this, &GrpcClient::installOutcomeFailed);
    connect(worker, &GrpcWorker::nexusAPIKeySet, this, &GrpcClient::nexusAPIKeySet);
    connect(worker, &GrpcWorker::nexusKeySaveFinished, this, &GrpcClient::nexusKeySaveFinished);
    connect(worker, &GrpcWorker::vfsStatusChanged, this, &GrpcClient::vfsStatusChanged);
    connect(worker, &GrpcWorker::archiveEventReceived, this, [this, generation](quint64 streamGeneration, const GrpcArchiveEvent& event) {
        if (generation == m_connectionGeneration && streamGeneration == m_streamStates[GrpcWorker::StreamArchive].generation)
            emit archiveEventReceived(event);
    });
    connect(worker, &GrpcWorker::installProgressEvent, this, [this, generation](quint64 streamGeneration, const GrpcInstallProgress& progress) {
        if (generation == m_connectionGeneration && streamGeneration == m_streamStates[GrpcWorker::StreamInstall].generation)
            emit installProgressEvent(progress);
    });
    connect(worker, &GrpcWorker::pluginStatusSnapshot, this, [this, generation](quint64 streamGeneration, const std::vector<GrpcPluginStatus>& plugins) {
        if (generation == m_connectionGeneration && streamGeneration == m_streamStates[GrpcWorker::StreamPluginStatus].generation)
            emit pluginStatusSnapshot(plugins);
    });
    connect(worker, &GrpcWorker::pluginStatusUpdate, this, [this, generation](quint64 streamGeneration, const GrpcPluginStatus& plugin) {
        if (generation == m_connectionGeneration && streamGeneration == m_streamStates[GrpcWorker::StreamPluginStatus].generation)
            emit pluginStatusUpdate(plugin);
    });
    connect(worker, &GrpcWorker::streamEventReceived, this, [this, generation](int kind, quint64 streamGeneration) {
        if (generation != m_connectionGeneration || kind < GrpcWorker::StreamArchive || kind > GrpcWorker::StreamPluginStatus)
            return;
        auto& state = m_streamStates[kind];
        if (state.generation != streamGeneration || state.receivedEvent) return;
        state.receivedEvent = true;
        state.retryMs = 1000;
    });
    connect(worker, &GrpcWorker::streamEnded, this, [this, generation](int kind, quint64 streamGeneration, int) {
        if (generation != m_connectionGeneration || kind < GrpcWorker::StreamArchive || kind > GrpcWorker::StreamPluginStatus)
            return;
        auto& state = m_streamStates[kind];
        if (state.generation != streamGeneration || !state.active) return;
        state.active = false;
        if (!isConnected()) return;
        const int delay = state.retryMs;
        state.retryMs = std::min(delay * 2, 30000);
        QTimer::singleShot(delay, this, [this, generation, kind, streamGeneration] {
            if (generation != m_connectionGeneration || !isConnected() ||
                m_streamStates[kind].generation != streamGeneration || m_streamStates[kind].active)
                return;
            startStream(kind);
        });
    });
    connect(worker, &GrpcWorker::dependencyWarning, this, &GrpcClient::dependencyWarning);
    connect(worker, &GrpcWorker::daemonError, this, &GrpcClient::daemonError);
    connect(worker, &GrpcWorker::daemonInfo, this, &GrpcClient::daemonInfo);
    connect(worker, &GrpcWorker::recoveryPending, this, &GrpcClient::recoveryPending);
    connect(worker, &GrpcWorker::transferProgress, this, &GrpcClient::transferProgress);
    connect(worker, &GrpcWorker::transferCompleted, this, [this, generation](const GrpcTransferSummary& summary) {
        if (generation != m_connectionGeneration) return;
        m_transferActive = false;
        emit transferCompleted(summary);
    });
    connect(worker, &GrpcWorker::transferFailed, this, [this, generation](const QString& error, int grpcCode) {
        if (generation != m_connectionGeneration) return;
        m_transferActive = false;
        emit transferFailed(error, grpcCode);
    });
    connect(worker, &GrpcWorker::updateCheckFinished, this, &GrpcClient::updateCheckFinished);
    connect(worker, &GrpcWorker::updateCheckFailed, this, &GrpcClient::updateCheckFailed);
    connect(worker, &GrpcWorker::modLoaderStatusReceived, this, &GrpcClient::modLoaderStatusReceived);
    connect(worker, &GrpcWorker::modLoaderStatusFailed, this, &GrpcClient::modLoaderStatusFailed);
    connect(worker, &GrpcWorker::modLoaderOperationFinished, this, &GrpcClient::modLoaderOperationFinished);
    connect(worker, &GrpcWorker::modListRequestReceived, this, &GrpcClient::modListRequestReceived);
    connect(worker, &GrpcWorker::modListRequestFailed, this, &GrpcClient::modListRequestFailed);
    connect(worker, &GrpcWorker::modListSaved, this, &GrpcClient::modListSaved);
    connect(worker, &GrpcWorker::modListSaveFailed, this, &GrpcClient::modListSaveFailed);
    connect(worker, &GrpcWorker::modReinstalled, this, &GrpcClient::modReinstalled);
    connect(worker, &GrpcWorker::modUninstalled, this, &GrpcClient::modUninstalled);
    connect(worker, &GrpcWorker::modRenamed, this, &GrpcClient::modRenamed);
    connect(worker, &GrpcWorker::modActionFailed, this, &GrpcClient::modActionFailed);
    connect(worker, &GrpcWorker::modDependencyReportReceived, this, &GrpcClient::modDependencyReportReceived);
    connect(worker, &GrpcWorker::modDependencyReportFailed, this, &GrpcClient::modDependencyReportFailed);
    connect(worker, &GrpcWorker::modDependenciesFetched, this, &GrpcClient::modDependenciesFetched);
    connect(worker, &GrpcWorker::modDependencyFetchFailed, this, &GrpcClient::modDependencyFetchFailed);
    connect(worker, &GrpcWorker::dependencyEnableAcknowledged, this, &GrpcClient::dependencyEnableAcknowledged);
    connect(worker, &GrpcWorker::dependencyEnableAckFailed, this, &GrpcClient::dependencyEnableAckFailed);
    connect(worker, &GrpcWorker::profileIniSaved, this, &GrpcClient::profileIniSaved);
    connect(worker, &GrpcWorker::profileIniSaveFailed, this, &GrpcClient::profileIniSaveFailed);
    connect(worker, &GrpcWorker::profileIniFilesApplied, this, &GrpcClient::profileIniFilesApplied);
    connect(worker, &GrpcWorker::profileIniFilesApplyFailed, this, &GrpcClient::profileIniFilesApplyFailed);
    connect(worker, &GrpcWorker::installCompletedHintReceived, this, [this, generation](quint64 streamGeneration, const GrpcInstallCompleted& event) {
        if (generation == m_connectionGeneration && streamGeneration == m_streamStates[GrpcWorker::StreamInstall].generation)
            emit installCompletedHintReceived(event);
    });
    connect(worker, &GrpcWorker::rpcError, this, &GrpcClient::rpcError);
}

void GrpcClient::connectToDaemon()
{
    if (m_workers[RoleUnary].thread) disconnectFromDaemon();
    ++m_connectionGeneration;

    grpc::ChannelArguments args;
    args.SetMaxReceiveMessageSize(64 * 1024 * 1024);
    m_channel = grpc::CreateCustomChannel(socketTarget(), grpc::InsecureChannelCredentials(), args);
    m_syncStub = std::make_unique<GrpcSyncStub>(m_channel);

    for (auto& handle : m_workers) {
        handle.worker = new GrpcWorker(m_channel);
        handle.thread = new QThread(this);
        handle.worker->moveToThread(handle.thread);
        connectWorkerSignals(handle.worker);
    }
    for (auto& handle : m_workers)
        handle.thread->start();
    m_connectionTimer->start();
    QTimer::singleShot(0, this, &GrpcClient::onCheckConnection);
}

// Stops workers, joins threads, and leaks any thread still alive after 3s to avoid stub use-after-free.
void GrpcClient::disconnectFromDaemon()
{
    ++m_connectionGeneration;
    m_connectionTimer->stop();
    for (auto& handle : m_workers)
        if (handle.worker) handle.worker->stop();

    for (auto& handle : m_workers) {
        if (!handle.thread) continue;
        handle.thread->quit();
        if (!handle.thread->wait(3000)) {
            QObject::disconnect(handle.worker, nullptr, this, nullptr);
            handle.thread->setParent(nullptr);
            qWarning("GrpcClient: %s thread did not exit within 3s; "
                     "leaking it to avoid use-after-free on the gRPC stub",
                     handle.tag);
            handle.thread = nullptr;
            handle.worker = nullptr;
            continue;
        }
        delete handle.worker;
        delete handle.thread;
        handle.worker = nullptr;
        handle.thread = nullptr;
    }

    m_syncStub.reset();
    m_channel.reset();
    m_transferActive = false;
    m_watchStarted = false;
    for (auto& state : m_streamStates) state.active = false;

    if (m_connected) {
        m_connected = false;
        emit disconnected();
    }
    emit workersStopped();
}

bool GrpcClient::isConnected() const { return m_connected; }

void GrpcClient::onCheckConnection()
{
    if (!m_channel) return;
    auto state = m_channel->GetState(true);
    bool nowConnected = (state == GRPC_CHANNEL_READY);
    if (nowConnected && !m_connected) {
        m_connected = true;
        resumeSubscriptions();
        emit connected();
        emit resubscribed();
    } else if (!nowConnected && m_connected) {
        m_connected = false;
        m_watchStarted = false;
        emit disconnected();
    }
}

template <typename Method, typename... Args>
void GrpcClient::postTo(GrpcWorker* worker, Method method, Args... args)
{
    QMetaObject::invokeMethod(worker, [worker, method, args...] { (worker->*method)(args...); });
}

template <typename Method, typename... Args>
void GrpcClient::post(Method method, Args... args)
{
    postTo(unaryWorker(), method, args...);
}

void GrpcClient::listGames() { post(&GrpcWorker::doListGames); }
void GrpcClient::detectGames() { post(&GrpcWorker::doDetectGames); }

void GrpcClient::configureGame(const QString& gameId, const QString& name,
                               uint32_t steamAppId, const QString& installPath,
                               const QString& dataSubpath)
{
    post(&GrpcWorker::doConfigureGame, gameId, name, steamAppId, installPath, dataSubpath);
}

quint64 GrpcClient::configureGameTracked(const QString& gameId, const QString& name,
                                         uint32_t steamAppId, const QString& installPath,
                                         const QString& dataSubpath)
{
    const quint64 id = ++m_nextSetupRequestId;
    post(&GrpcWorker::doConfigureGameTracked, id, gameId, name, steamAppId, installPath, dataSubpath);
    return id;
}

void GrpcClient::listMods(const QString& gameId) { post(&GrpcWorker::doListMods, gameId); }

bool GrpcClient::listModsSync(const QString& gameId, std::vector<GrpcModInfo>& out, GrpcError& errorOut)
{
    gorganizer::v1::ListModsRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::ListModsResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::ListMods, req, resp), "ListMods", errorOut)) return false;
    out.clear();
    out.reserve(resp.mods_size());
    for (const auto& m : resp.mods()) {
        GrpcModInfo info;
        info.name = QString::fromStdString(m.name());
        info.gameId = QString::fromStdString(m.game_id());
        info.basePath = QString::fromStdString(m.base_path());
        info.dataPath = QString::fromStdString(m.data_path());
        info.fileCount = m.file_count();
        info.totalSize = m.total_size();
        out.push_back(std::move(info));
    }
    return true;
}

void GrpcClient::getMod(const QString& gameId, const QString& modName)
{
    post(&GrpcWorker::doGetMod, gameId, modName);
}

void GrpcClient::rescanMod(const QString& gameId, const QString& modName)
{
    post(&GrpcWorker::doRescanMod, gameId, modName);
}

void GrpcClient::listProfiles(const QString& gameId) { post(&GrpcWorker::doListProfiles, gameId); }

bool GrpcClient::listProfilesSync(const QString& gameId, std::vector<GrpcProfile>& out, GrpcError& errorOut)
{
    gorganizer::v1::ListProfilesRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::ListProfilesResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::ListProfiles, req, resp), "ListProfiles", errorOut)) return false;
    out.clear();
    out.reserve(resp.profiles_size());
    for (const auto& p : resp.profiles()) {
        out.push_back(GrpcProfile{
            QString::fromStdString(p.name()),
            QString::fromStdString(p.game_id()),
            QString::fromStdString(p.created_at()),
        });
    }
    return true;
}

void GrpcClient::createProfile(const QString& gameId, const QString& name)
{
    post(&GrpcWorker::doCreateProfile, gameId, name);
}

void GrpcClient::copyProfile(const QString& gameId, const QString& source, const QString& name)
{
    post(&GrpcWorker::doCopyProfile, gameId, source, name);
}

void GrpcClient::deleteProfile(const QString& gameId, const QString& name)
{
    post(&GrpcWorker::doDeleteProfile, gameId, name);
}

void GrpcClient::getModList(const QString& gameId, const QString& profileName)
{
    post(&GrpcWorker::doGetModList, gameId, profileName);
}

void GrpcClient::setModList(const QString& gameId, const QString& profileName,
                            const std::vector<GrpcModListEntry>& entries)
{
    post(&GrpcWorker::doSetModList, gameId, profileName, entries);
}

void GrpcClient::mountVfs(const QString& gameId, const QString& profileName)
{
    post(&GrpcWorker::doMountVfs, gameId, profileName);
}

void GrpcClient::mountVfsWithSwap(const QString& gameId, const QString& profileName)
{
    post(&GrpcWorker::doMountVfsWithSwap, gameId, profileName);
}

quint64 GrpcClient::retargetVfs(const QString& gameId, const QString& profileName)
{
    const quint64 requestId = ++m_nextVfsRequestId;
    if (!installRpcWorker() || !isConnected()) {
        QMetaObject::invokeMethod(this, [this, requestId, gameId, profileName] {
            emit vfsRetargetFailed(requestId, gameId, profileName, QStringLiteral("not connected"), GrpcStatusUnavailable);
        }, Qt::QueuedConnection);
        return requestId;
    }
    postTo(installRpcWorker(), &GrpcWorker::doRetargetVfs, requestId, gameId, profileName);
    return requestId;
}

void GrpcClient::unmountVfs(const QString& gameId) { post(&GrpcWorker::doUnmountVfs, gameId); }

void GrpcClient::restoreFromBackup(const QString& gameId, GrpcRecoveryKind kind, const QString& recoveryId)
{
    post(&GrpcWorker::doRestoreFromBackup, gameId, kind, recoveryId);
}

void GrpcClient::retryVfsRecovery(const QString& gameId)
{
    post(&GrpcWorker::doRetryVfsRecovery, gameId);
}

void GrpcClient::getVfsStatus(const QString& gameId) { post(&GrpcWorker::doGetVfsStatus, gameId); }

quint64 GrpcClient::queryVfsStatus(const QString& gameId)
{
    const quint64 requestId = ++m_nextVfsRequestId;
    if (!unaryWorker()) {
        QMetaObject::invokeMethod(this, [this, requestId, gameId] {
            emit vfsStatusQueryFailed(requestId, gameId, QStringLiteral("not connected"), GrpcStatusUnavailable);
        }, Qt::QueuedConnection);
        return requestId;
    }
    post(&GrpcWorker::doQueryVfsStatus, requestId, gameId);
    return requestId;
}

quint64 GrpcClient::setSteamMaintenance(const QString& gameId, bool enabled, bool verificationConfirmed)
{
    const quint64 requestId = ++m_nextSteamRequestId;
    if (!installRpcWorker() || !isConnected()) {
        QMetaObject::invokeMethod(this, [this, requestId, gameId] {
            emit steamMaintenanceSetFailed(requestId, gameId, QStringLiteral("not connected"), GrpcStatusUnavailable);
        }, Qt::QueuedConnection);
        return requestId;
    }
    postTo(installRpcWorker(), &GrpcWorker::doSetSteamMaintenance, requestId, gameId, enabled, verificationConfirmed);
    return requestId;
}

quint64 GrpcClient::importPreservedFiles(const QString& gameId, const QString& batchId,
                                         const QString& modName, const QStringList& relativePaths)
{
    const quint64 requestId = ++m_nextSteamRequestId;
    if (!unaryWorker() || !isConnected()) {
        QMetaObject::invokeMethod(this, [this, requestId, gameId] {
            emit preservedFilesImportFailed(requestId, gameId, QStringLiteral("not connected"), GrpcStatusUnavailable);
        }, Qt::QueuedConnection);
        return requestId;
    }
    post(&GrpcWorker::doImportPreservedFiles, requestId, gameId, batchId, modName, relativePaths);
    return requestId;
}

quint64 GrpcClient::deletePreservedBatch(const QString& gameId, const QString& batchId)
{
    const quint64 requestId = ++m_nextSteamRequestId;
    if (!unaryWorker() || !isConnected()) {
        QMetaObject::invokeMethod(this, [this, requestId, gameId] {
            emit preservedBatchDeleteFailed(requestId, gameId, QStringLiteral("not connected"), GrpcStatusUnavailable);
        }, Qt::QueuedConnection);
        return requestId;
    }
    post(&GrpcWorker::doDeletePreservedBatch, requestId, gameId, batchId);
    return requestId;
}

quint64 GrpcClient::unmountVfsForMaintenance(const QString& gameId)
{
    const quint64 requestId = ++m_nextVfsRequestId;
    if (!unaryWorker()) {
        QMetaObject::invokeMethod(this, [this, requestId, gameId] {
            emit maintenanceUnmountFinished(requestId, gameId, false, GrpcStatusUnavailable,
                                            QStringLiteral("not connected"));
        }, Qt::QueuedConnection);
        return requestId;
    }
    post(&GrpcWorker::doUnmountVfsForMaintenance, requestId, gameId);
    return requestId;
}

void GrpcClient::rebuildVfs(const QString& gameId) { post(&GrpcWorker::doRebuildVfs, gameId); }

void GrpcClient::getConflicts(const QString& gameId, const QString& profileName)
{
    post(&GrpcWorker::doGetConflicts, gameId, profileName);
}

void GrpcClient::launchGame(const QString& gameId, bool useTool, const QString& profileName)
{
    post(&GrpcWorker::doLaunchGame, gameId, useTool, profileName);
}

void GrpcClient::startDownload(const QString& nxmUri) { post(&GrpcWorker::doStartDownload, nxmUri); }

void GrpcClient::cancelDownload(const QString& downloadId)
{
    post(&GrpcWorker::doCancelDownload, downloadId);
}

void GrpcClient::retryDownload(const QString& downloadId)
{
    post(&GrpcWorker::doRetryDownload, downloadId);
}

quint64 GrpcClient::startInstall(const QString& gameId, const QString& archiveRelPath,
                                 GrpcInstallMode mode, const QString& targetMod,
                                 const QString& previewId,
                                 const std::vector<GrpcFomodFile>& fomodSelectedFiles,
                                 bool fomodConfirmed, const QString& selectedRoot,
                                 const QString& clientRequestId)
{
    return postInstall(gameId, archiveRelPath, QString(), mode, targetMod, previewId,
                       fomodSelectedFiles, fomodConfirmed, selectedRoot, clientRequestId);
}

quint64 GrpcClient::startInstallExternal(const QString& gameId, const QString& externalArchivePath,
                                         GrpcInstallMode mode, const QString& targetMod,
                                         bool fomodConfirmed, const QString& selectedRoot,
                                         const QString& previewId,
                                         const std::vector<GrpcFomodFile>& fomodSelectedFiles,
                                         const QString& clientRequestId)
{
    return postInstall(gameId, QString(), externalArchivePath, mode, targetMod, previewId,
                       fomodSelectedFiles, fomodConfirmed, selectedRoot, clientRequestId);
}

// Assigns a request id and queues StartInstall on the install RPC worker, failing asynchronously when not connected.
quint64 GrpcClient::postInstall(const QString& gameId, const QString& archiveRelPath,
                                const QString& externalArchivePath, GrpcInstallMode mode,
                                const QString& targetMod, const QString& previewId,
                                const std::vector<GrpcFomodFile>& fomodSelectedFiles,
                                bool fomodConfirmed, const QString& selectedRoot,
                                const QString& clientRequestId)
{
    const quint64 requestId = ++m_nextInstallRequestId;
    if (!installRpcWorker() || !isConnected()) {
        QMetaObject::invokeMethod(this, [this, requestId] {
            emit installRequestFailed(requestId, GrpcStatusUnavailable, QStringLiteral("not connected"), false);
        }, Qt::QueuedConnection);
        return requestId;
    }
    postTo(installRpcWorker(), &GrpcWorker::doStartInstall, requestId, gameId, archiveRelPath,
           externalArchivePath, static_cast<int>(mode), targetMod, previewId, fomodSelectedFiles,
           fomodConfirmed, selectedRoot, clientRequestId);
    return requestId;
}

void GrpcClient::cancelInstallRequest(quint64 requestId)
{
    if (installRpcWorker()) installRpcWorker()->cancelInstall(requestId);
}

quint64 GrpcClient::getInstallOutcome(const QString& gameId, const QString& clientRequestId)
{
    const quint64 requestId = ++m_nextInstallStatusRequestId;
    if (!installStatusWorker() || !isConnected()) {
        QMetaObject::invokeMethod(this, [this, requestId] {
            emit installOutcomeFailed(requestId, GrpcStatusUnavailable, QStringLiteral("not connected"));
        }, Qt::QueuedConnection);
        return requestId;
    }
    postTo(installStatusWorker(), &GrpcWorker::doGetInstallOutcome, requestId, gameId, clientRequestId);
    return requestId;
}

template <typename Method, typename... Args>
quint64 GrpcClient::postModLoaderOperation(const QString& gameId, const QString& operation, Method method, Args... args)
{
    const quint64 requestId = ++m_nextModLoaderRequestId;
    if (!modLoaderRpcWorker()) {
        QMetaObject::invokeMethod(this, [this, requestId, gameId, operation] {
            emit modLoaderOperationFinished(requestId, gameId, operation, false, GrpcStatusFailedPrecondition,
                                            GrpcModLoaderStatus{}, QStringLiteral("not connected"));
        }, Qt::QueuedConnection);
        return requestId;
    }
    postTo(modLoaderRpcWorker(), method, requestId, args...);
    return requestId;
}

quint64 GrpcClient::postModLoaderStatus(GrpcWorker* worker, const QString& gameId, bool checkLatest)
{
    const quint64 requestId = ++m_nextModLoaderRequestId;
    if (!worker) {
        QMetaObject::invokeMethod(this, [this, requestId, gameId] {
            emit modLoaderStatusFailed(requestId, gameId, QStringLiteral("not connected"), GrpcStatusUnavailable);
        }, Qt::QueuedConnection);
        return requestId;
    }
    postTo(worker, &GrpcWorker::doGetModLoaderStatus, requestId, gameId, checkLatest);
    return requestId;
}

quint64 GrpcClient::checkForUpdate(const QString& runningVersion)
{
    const quint64 requestId = ++m_nextUpdateRequestId;
    if (!updateRpcWorker()) {
        QMetaObject::invokeMethod(this, [this, requestId] {
            emit updateCheckFailed(requestId, QStringLiteral("not connected"), GrpcStatusUnavailable);
        }, Qt::QueuedConnection);
        return requestId;
    }
    postTo(updateRpcWorker(), &GrpcWorker::doCheckForUpdate, requestId, runningVersion);
    return requestId;
}

quint64 GrpcClient::getModLoaderStatus(const QString& gameId, bool checkLatest)
{
    return postModLoaderStatus(checkLatest ? modLoaderStatusWorker() : unaryWorker(), gameId, checkLatest);
}

quint64 GrpcClient::pollModLoaderStatus(const QString& gameId)
{
    return postModLoaderStatus(modLoaderStatusWorker(), gameId, false);
}

quint64 GrpcClient::installModLoader(const QString& gameId, bool repairOnly)
{
    return postModLoaderOperation(gameId, repairOnly ? QStringLiteral("repair") : QStringLiteral("install"),
                                  &GrpcWorker::doInstallModLoader, gameId, repairOnly);
}

quint64 GrpcClient::uninstallModLoader(const QString& gameId)
{
    return postModLoaderOperation(gameId, QStringLiteral("uninstall"), &GrpcWorker::doUninstallModLoader, gameId);
}

quint64 GrpcClient::rollbackModLoader(const QString& gameId)
{
    return postModLoaderOperation(gameId, QStringLiteral("rollback"), &GrpcWorker::doRollbackModLoader, gameId);
}

quint64 GrpcClient::getModListTracked(const QString& gameId, const QString& profileName)
{
    const quint64 requestId = ++m_nextModListRequestId;
    if (!unaryWorker()) {
        QMetaObject::invokeMethod(this, [this, requestId, gameId, profileName] {
            emit modListRequestFailed(requestId, gameId, profileName, QStringLiteral("not connected"));
        }, Qt::QueuedConnection);
        return requestId;
    }
    post(&GrpcWorker::doGetModListRequest, requestId, gameId, profileName);
    return requestId;
}

quint64 GrpcClient::setModListTracked(const QString& gameId, const QString& profileName,
                                      const std::vector<GrpcModListEntry>& entries)
{
    const quint64 requestId = ++m_nextModListRequestId;
    if (!unaryWorker()) {
        QMetaObject::invokeMethod(this, [this, requestId, gameId, profileName] {
            emit modListSaveFailed(requestId, gameId, profileName, QStringLiteral("not connected"));
        }, Qt::QueuedConnection);
        return requestId;
    }
    post(&GrpcWorker::doSetModListRequest, requestId, gameId, profileName, entries);
    return requestId;
}

// Queues a dependency report on the dependency worker when it consults smapi.io and on the unary worker otherwise.
quint64 GrpcClient::getModDependencyReport(const QString& gameId, const QString& profileName,
                                           bool refreshRemote, bool forceRemote)
{
    const quint64 requestId = ++m_nextDependencyRequestId;
    GrpcWorker* worker = (refreshRemote || forceRemote) ? dependencyRpcWorker() : unaryWorker();
    if (!worker) {
        QMetaObject::invokeMethod(this, [this, requestId, gameId, profileName] {
            emit modDependencyReportFailed(requestId, gameId, profileName, QStringLiteral("not connected"));
        }, Qt::QueuedConnection);
        return requestId;
    }
    postTo(worker, &GrpcWorker::doGetModDependencyReport, requestId, gameId, profileName,
           refreshRemote, forceRemote);
    return requestId;
}

quint64 GrpcClient::fetchModDependencies(const QString& gameId, const QString& profileName,
                                         const QStringList& uniqueIds)
{
    const quint64 requestId = ++m_nextDependencyRequestId;
    if (!dependencyRpcWorker()) {
        QMetaObject::invokeMethod(this, [this, requestId, gameId, profileName] {
            emit modDependencyFetchFailed(requestId, gameId, profileName, QStringLiteral("not connected"));
        }, Qt::QueuedConnection);
        return requestId;
    }
    postTo(dependencyRpcWorker(), &GrpcWorker::doFetchModDependencies, requestId, gameId, profileName, uniqueIds);
    return requestId;
}

quint64 GrpcClient::ackDependencyEnable(const QString& gameId, const QString& batchId, const QStringList& uniqueIds)
{
    const quint64 requestId = ++m_nextDependencyRequestId;
    if (!unaryWorker()) {
        QMetaObject::invokeMethod(this, [this, requestId, gameId, batchId] {
            emit dependencyEnableAckFailed(requestId, gameId, batchId, QStringLiteral("not connected"));
        }, Qt::QueuedConnection);
        return requestId;
    }
    post(&GrpcWorker::doAckDependencyEnable, requestId, gameId, batchId, uniqueIds);
    return requestId;
}

void GrpcClient::startWatching()
{
    if (!watchWorker() || m_watchStarted) return;
    m_watchStarted = true;
    watchWorker()->setStreamGeneration(++m_watchGeneration);
    postTo(watchWorker(), &GrpcWorker::doStartWatching, m_watchGeneration);
}

void GrpcClient::stopWatching()
{
    m_watchStarted = false;
    if (watchWorker()) watchWorker()->setStreamGeneration(++m_watchGeneration);
}

void GrpcClient::cancelStream(int kind)
{
    static_assert(RoleInstall == RoleArchive + GrpcWorker::StreamInstall);
    static_assert(RolePluginStatus == RoleArchive + GrpcWorker::StreamPluginStatus);
    auto& state = m_streamStates[kind];
    ++state.generation;
    state.active = false;
    state.retryMs = 1000;
    state.receivedEvent = false;
    auto* worker = m_workers[RoleArchive + kind].worker;
    if (worker) worker->setStreamGeneration(state.generation);
}

void GrpcClient::startStream(int kind)
{
    auto* worker = m_workers[RoleArchive + kind].worker;
    if (!worker || !isConnected()) return;
    if (kind == GrpcWorker::StreamPluginStatus) {
        if (m_pluginGame.isEmpty() || m_pluginProfile.isEmpty()) return;
    } else if (m_subscribedGame.isEmpty()) {
        return;
    }
    auto& state = m_streamStates[kind];
    state.active = true;
    state.receivedEvent = false;
    worker->setStreamGeneration(++state.generation);
    if (kind == GrpcWorker::StreamArchive)
        postTo(worker, &GrpcWorker::doStreamArchiveEvents, m_subscribedGame, state.generation);
    else if (kind == GrpcWorker::StreamInstall)
        postTo(worker, &GrpcWorker::doStreamInstallEvents, m_subscribedGame, state.generation);
    else
        postTo(worker, &GrpcWorker::doStreamPluginStatus, m_pluginGame, m_pluginProfile, state.generation);
}

void GrpcClient::resumeSubscriptions()
{
    for (int kind = GrpcWorker::StreamArchive; kind <= GrpcWorker::StreamPluginStatus; ++kind) {
        cancelStream(kind);
        startStream(kind);
    }
}

void GrpcClient::subscribeEvents(const QString& gameId)
{
    if (gameId == m_subscribedGame && m_streamStates[GrpcWorker::StreamArchive].active &&
        m_streamStates[GrpcWorker::StreamInstall].active) return;
    m_subscribedGame = gameId;
    cancelStream(GrpcWorker::StreamArchive);
    cancelStream(GrpcWorker::StreamInstall);
    startStream(GrpcWorker::StreamArchive);
    startStream(GrpcWorker::StreamInstall);
}

void GrpcClient::unsubscribeEvents()
{
    m_subscribedGame.clear();
    cancelStream(GrpcWorker::StreamArchive);
    cancelStream(GrpcWorker::StreamInstall);
}

void GrpcClient::subscribePluginStatus(const QString& gameId, const QString& profileName)
{
    m_pluginGame = gameId;
    m_pluginProfile = profileName;
    cancelStream(GrpcWorker::StreamPluginStatus);
    startStream(GrpcWorker::StreamPluginStatus);
}

void GrpcClient::unsubscribePluginStatus()
{
    m_pluginGame.clear();
    m_pluginProfile.clear();
    cancelStream(GrpcWorker::StreamPluginStatus);
}

void GrpcClient::setNexusAPIKey(const QString& apiKey)
{
    post(&GrpcWorker::doSetNexusAPIKey, apiKey);
}

quint64 GrpcClient::saveNexusAPIKey(const QString& apiKey)
{
    const quint64 id = ++m_nextSetupRequestId;
    post(&GrpcWorker::doSetNexusAPIKeyTracked, id, apiKey);
    return id;
}

void GrpcClient::shutdownDaemon()
{
    post(&GrpcWorker::doShutdownDaemon);
}

bool GrpcClient::getShutdownPlanSync(int timeoutMs, std::vector<GrpcShutdownPlanItem>& items, GrpcError& errorOut)
{
    gorganizer::v1::GetShutdownPlanRequest req;
    gorganizer::v1::ShutdownPlan resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::GetShutdownPlan, req, resp,
                              std::chrono::milliseconds(timeoutMs)), "GetShutdownPlan", errorOut)) return false;
    items.clear();
    for (const auto& item : resp.items()) {
        items.push_back({QString::fromStdString(item.game_id()),
                         QString::fromStdString(item.profile_name()),
                         item.will_unmount(), QString::fromStdString(item.retained_reason())});
    }
    return true;
}

bool GrpcClient::shutdownDaemonSync(int rpcTimeoutMs, int pollTimeoutMs, GrpcError& errorOut)
{
    if (!m_syncStub) {
        errorOut = {GrpcStatusUnavailable, QStringLiteral("Shutdown"), QStringLiteral("not connected")};
        return false;
    }
    gorganizer::v1::ShutdownRequest req;
    gorganizer::v1::ShutdownResponse resp;
    auto s = invokeUnary(m_syncStub.get(), &Stub::Shutdown, req, resp,
                         std::chrono::milliseconds(rpcTimeoutMs));
    if (!s.ok())
        errorOut = grpcErrorFromStatus(s, QStringLiteral("Shutdown"));

    QString sockPath = socketPath();
    QElapsedTimer t;
    t.start();
    while (t.elapsed() < pollTimeoutMs) {
        if (!QFileInfo::exists(sockPath)) return true;
        QThread::msleep(50);
    }
    if (errorOut.message.isEmpty())
        errorOut = {GrpcStatusDeadlineExceeded, QStringLiteral("Shutdown"),
                    QStringLiteral("daemon did not exit within timeout")};
    return false;
}

bool GrpcClient::listArchives(const QString& gameId, std::vector<GrpcArchiveRow>& rowsOut, GrpcError& errorOut)
{
    gorganizer::v1::ListArchivesRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::ListArchivesResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::ListArchives, req, resp), "ListArchives", errorOut)) return false;
    rowsOut.clear();
    for (const auto& r : resp.rows()) rowsOut.push_back(archiveRowFromProto(r));
    return true;
}

bool GrpcClient::setArchiveHidden(const QString& gameId, const QString& archiveRelPath, bool hidden, GrpcError& errorOut)
{
    gorganizer::v1::SetArchiveHiddenRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_archive_rel_path(archiveRelPath.toStdString());
    req.set_hidden(hidden);
    gorganizer::v1::SetArchiveHiddenResponse resp;
    return mapError(invokeUnary(m_syncStub.get(), &Stub::SetArchiveHidden, req, resp), "SetArchiveHidden", errorOut);
}

bool GrpcClient::setArchivesHiddenBulk(const QString& gameId, bool hidden, GrpcBulkHideScope scope,
                                        int& affectedOut, GrpcError& errorOut)
{
    gorganizer::v1::SetArchivesHiddenBulkRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_hidden(hidden);
    req.set_scope(static_cast<gorganizer::v1::SetArchivesHiddenBulkRequest_Scope>(scope));
    gorganizer::v1::SetArchivesHiddenBulkResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::SetArchivesHiddenBulk, req, resp), "SetArchivesHiddenBulk", errorOut)) return false;
    affectedOut = resp.affected();
    return true;
}

bool GrpcClient::removeArchive(const QString& gameId, const QString& archiveRelPath, const QString& downloadId, GrpcError& errorOut)
{
    gorganizer::v1::RemoveArchiveRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_archive_rel_path(archiveRelPath.toStdString());
    req.set_download_id(downloadId.toStdString());
    gorganizer::v1::RemoveArchiveResponse resp;
    return mapError(invokeUnary(m_syncStub.get(), &Stub::RemoveArchive, req, resp), "RemoveArchive", errorOut);
}

bool GrpcClient::refreshArchiveMetadata(const QString& gameId, const QString& archiveRelPath,
                                         GrpcArchiveRow& rowOut, GrpcError& errorOut)
{
    gorganizer::v1::RefreshArchiveMetadataRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_archive_rel_path(archiveRelPath.toStdString());
    gorganizer::v1::RefreshArchiveMetadataResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::RefreshArchiveMetadata, req, resp), "RefreshArchiveMetadata", errorOut)) return false;
    rowOut = archiveRowFromProto(resp.row());
    return true;
}

bool GrpcClient::previewInstall(const QString& gameId, const QString& archiveRelPath,
                                 GrpcPreviewInstallResult& out, GrpcError& errorOut,
                                 const QString& externalArchivePath)
{
    auto req = previewInstallRequest(gameId, archiveRelPath, externalArchivePath);
    gorganizer::v1::PreviewInstallResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::PreviewInstall, req, resp,
                              std::chrono::minutes(10)), "PreviewInstall", errorOut)) return false;
    out = previewInstallResultFromProto(resp);
    return true;
}

quint64 GrpcClient::previewInstallAsync(const QString& gameId, const QString& archiveRelPath,
                                        const QString& externalArchivePath)
{
    const quint64 requestId = ++m_nextPreviewRequestId;
    if (!installRpcWorker()) {
        QMetaObject::invokeMethod(this, [this, requestId] {
            emit previewInstallFailed(requestId, QStringLiteral("not connected"), GrpcStatusUnavailable);
        }, Qt::QueuedConnection);
        return requestId;
    }
    postTo(installRpcWorker(), &GrpcWorker::doPreviewInstall, requestId, gameId, archiveRelPath,
           externalArchivePath);
    return requestId;
}

bool GrpcClient::discardPreview(const QString& previewId, GrpcError& errorOut)
{
    gorganizer::v1::DiscardPreviewRequest req;
    req.set_preview_id(previewId.toStdString());
    gorganizer::v1::DiscardPreviewResponse resp;
    return mapError(invokeUnary(m_syncStub.get(), &Stub::DiscardPreview, req, resp), "DiscardPreview", errorOut);
}

void GrpcClient::discardPreviewAsync(const QString& previewId)
{
    if (!unaryWorker()) {
        qWarning("GrpcClient: DiscardPreview failed: not connected");
        return;
    }
    post(&GrpcWorker::doDiscardPreview, previewId);
}

quint64 GrpcClient::renameModAsync(const QString& gameId, const QString& oldName, const QString& newName)
{
    const quint64 requestId = ++m_nextModActionRequestId;
    if (!installRpcWorker() || !isConnected()) {
        QMetaObject::invokeMethod(this, [this, requestId, gameId, oldName] {
            emit modActionFailed(requestId, gameId, oldName, QStringLiteral("RenameMod"),
                                 QStringLiteral("not connected"), GrpcStatusUnavailable);
        }, Qt::QueuedConnection);
        return requestId;
    }
    postTo(installRpcWorker(), &GrpcWorker::doRenameMod, requestId, gameId, oldName, newName);
    return requestId;
}

bool GrpcClient::uninstallMod(const QString& gameId, const QString& modName, bool force,
                               std::vector<QString>& archivesFlaggedOut, GrpcError& errorOut)
{
    gorganizer::v1::UninstallModRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_mod_name(modName.toStdString());
    req.set_force(force);
    gorganizer::v1::UninstallModResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::UninstallMod, req, resp,
                              std::chrono::minutes(5)), "UninstallMod", errorOut)) return false;
    archivesFlaggedOut.clear();
    for (const auto& p : resp.archives_flagged_uninstalled())
        archivesFlaggedOut.push_back(QString::fromStdString(p));
    return true;
}

quint64 GrpcClient::reinstallModAsync(const QString& gameId, const QString& modName,
                                       const QString& clientRequestId)
{
    const quint64 requestId = ++m_nextInstallRequestId;
    if (!installRpcWorker() || !isConnected()) {
        QMetaObject::invokeMethod(this, [this, requestId] {
            emit reinstallRequestFailed(requestId, GrpcStatusUnavailable, QStringLiteral("not connected"), false);
        }, Qt::QueuedConnection);
        return requestId;
    }
    postTo(installRpcWorker(), &GrpcWorker::doReinstallMod, requestId, gameId, modName, clientRequestId);
    return requestId;
}

quint64 GrpcClient::uninstallModAsync(const QString& gameId, const QString& modName, bool force)
{
    const quint64 requestId = ++m_nextModActionRequestId;
    if (!installRpcWorker() || !isConnected()) {
        QMetaObject::invokeMethod(this, [this, requestId, gameId, modName] {
            emit modActionFailed(requestId, gameId, modName, QStringLiteral("UninstallMod"),
                                 QStringLiteral("not connected"), GrpcStatusUnavailable);
        }, Qt::QueuedConnection);
        return requestId;
    }
    postTo(installRpcWorker(), &GrpcWorker::doUninstallMod, requestId, gameId, modName, force);
    return requestId;
}

bool GrpcClient::registerManualInstall(const QString& gameId, const QString& modName,
                                        const QString& archiveRelPath, GrpcError& errorOut)
{
    gorganizer::v1::RegisterManualInstallRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_mod_name(modName.toStdString());
    req.set_archive_rel_path(archiveRelPath.toStdString());
    gorganizer::v1::RegisterManualInstallResponse resp;
    return mapError(invokeUnary(m_syncStub.get(), &Stub::RegisterManualInstall, req, resp), "RegisterManualInstall", errorOut);
}

bool GrpcClient::listOverwriteFiles(const QString& gameId,
                                     std::vector<GrpcOverwriteEntry>& filesOut,
                                     QString& overwriteDirOut, GrpcError& errorOut)
{
    gorganizer::v1::ListOverwriteFilesRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::ListOverwriteFilesResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::ListOverwriteFiles, req, resp), "ListOverwriteFiles", errorOut)) return false;
    filesOut.clear();
    filesOut.reserve(resp.files_size());
    for (const auto& f : resp.files()) {
        GrpcOverwriteEntry e;
        e.relPath = QString::fromStdString(f.rel_path());
        e.sizeBytes = f.size_bytes();
        e.modifiedAt = QString::fromStdString(f.modified_at());
        e.isDir = f.is_dir();
        filesOut.push_back(std::move(e));
    }
    overwriteDirOut = QString::fromStdString(resp.overwrite_dir());
    return true;
}

bool GrpcClient::extractOverwriteToMod(const QString& gameId, const QString& modName,
                                        const QStringList& files, bool keepInOverwrite,
                                        int& fileCountOut, GrpcError& errorOut)
{
    gorganizer::v1::ExtractOverwriteToModRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_mod_name(modName.toStdString());
    req.set_keep_in_overwrite(keepInOverwrite);
    for (const auto& f : files)
        req.add_files(f.toStdString());
    gorganizer::v1::ExtractOverwriteToModResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::ExtractOverwriteToMod, req, resp,
                              std::chrono::minutes(2)), "ExtractOverwriteToMod", errorOut)) return false;
    fileCountOut = resp.file_count();
    return true;
}

bool GrpcClient::listSeparators(const QString& gameId, const QString& profileName,
                                 std::vector<GrpcSeparator>& out, bool& viewEnabledOut,
                                 GrpcError& errorOut)
{
    gorganizer::v1::ListSeparatorsRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    gorganizer::v1::ListSeparatorsResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::ListSeparators, req, resp), "ListSeparators", errorOut)) return false;
    out.clear();
    out.reserve(resp.separators_size());
    for (const auto& sp : resp.separators()) {
        out.push_back(GrpcSeparator{
            QString::fromStdString(sp.name()),
            QString::fromStdString(sp.visual_index()),
            sp.collapsed(),
        });
    }
    viewEnabledOut = resp.view_enabled();
    return true;
}

bool GrpcClient::setSeparators(const QString& gameId, const QString& profileName,
                                const std::vector<GrpcSeparator>& seps, bool viewEnabled,
                                GrpcError& errorOut)
{
    gorganizer::v1::SetSeparatorsRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    for (const auto& g : seps) {
        auto* sp = req.add_separators();
        sp->set_name(g.name.toStdString());
        sp->set_visual_index(g.visualIndex.toStdString());
        sp->set_collapsed(g.collapsed);
    }
    req.set_view_enabled(viewEnabled);
    gorganizer::v1::SetSeparatorsResponse resp;
    return mapError(invokeUnary(m_syncStub.get(), &Stub::SetSeparators, req, resp), "SetSeparators", errorOut);
}

bool GrpcClient::setPluginOrder(const QString& gameId, const QString& profileName,
                                 const QStringList& filenames, GrpcError& errorOut)
{
    gorganizer::v1::SetPluginOrderRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    for (const auto& f : filenames)
        req.add_filenames(f.toStdString());
    gorganizer::v1::SetPluginOrderResponse resp;
    return mapError(invokeUnary(m_syncStub.get(), &Stub::SetPluginOrder, req, resp), "SetPluginOrder", errorOut);
}

bool GrpcClient::setPluginLoadout(const QString& gameId, const QString& profileName,
                                  const std::vector<GrpcPluginLoadoutEntry>& plugins,
                                  GrpcError& errorOut)
{
    gorganizer::v1::SetPluginLoadoutRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    for (const auto& plugin : plugins) {
        auto* entry = req.add_plugins();
        entry->set_filename(plugin.filename.toStdString());
        entry->set_enabled(plugin.enabled);
    }
    gorganizer::v1::SetPluginLoadoutResponse resp;
    return mapError(invokeUnary(m_syncStub.get(), &Stub::SetPluginLoadout, req, resp), "SetPluginLoadout", errorOut);
}

bool GrpcClient::detectProtonVersions(std::vector<GrpcProtonVersion>& out, GrpcError& errorOut)
{
    gorganizer::v1::DetectProtonRequest req;
    gorganizer::v1::DetectProtonResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::DetectProton, req, resp), "DetectProton", errorOut)) return false;
    out.clear();
    out.reserve(resp.versions_size());
    for (const auto& v : resp.versions()) {
        out.push_back(GrpcProtonVersion{
            QString::fromStdString(v.name()),
            QString::fromStdString(v.path()),
        });
    }
    return true;
}

bool GrpcClient::getPreferredProton(QString& pathOut, GrpcError& errorOut)
{
    gorganizer::v1::GetPreferredProtonRequest req;
    gorganizer::v1::GetPreferredProtonResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::GetPreferredProton, req, resp), "GetPreferredProton", errorOut)) return false;
    pathOut = QString::fromStdString(resp.path());
    return true;
}

bool GrpcClient::setPreferredProton(const QString& path, GrpcError& errorOut)
{
    gorganizer::v1::SetPreferredProtonRequest req;
    req.set_path(path.toStdString());
    gorganizer::v1::SetPreferredProtonResponse resp;
    return mapError(invokeUnary(m_syncStub.get(), &Stub::SetPreferredProton, req, resp), "SetPreferredProton", errorOut);
}

void GrpcClient::setActiveGame(const QString& gameId)
{
    gorganizer::v1::SetActiveGameRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::SetActiveGameResponse resp;
    invokeUnary(m_syncStub.get(), &Stub::SetActiveGame, req, resp);
}

bool GrpcClient::installScriptExtender(const QString& gameId, QString& nameOut, GrpcError& errorOut)
{
    gorganizer::v1::InstallScriptExtenderRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::InstallScriptExtenderResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::InstallScriptExtender, req, resp,
                              std::chrono::minutes(5)), "InstallScriptExtender", errorOut)) return false;
    nameOut = QString::fromStdString(resp.name());
    return true;
}

bool GrpcClient::listExecutables(const QString& gameId, QList<GrpcExecutable>& out, GrpcError& errorOut)
{
    gorganizer::v1::ListExecutablesRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::ListExecutablesResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::ListExecutables, req, resp), "ListExecutables", errorOut)) return false;
    out.clear();
    for (const auto& e : resp.executables()) out << execFromProto(e);
    return true;
}

bool GrpcClient::upsertExecutable(const QString& gameId, const GrpcExecutable& exe,
                                  GrpcExecutable& savedOut, GrpcError& errorOut)
{
    gorganizer::v1::UpsertExecutableRequest req;
    req.set_game_id(gameId.toStdString());
    execToProto(exe, req.mutable_executable());
    gorganizer::v1::Executable resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::UpsertExecutable, req, resp), "UpsertExecutable", errorOut)) return false;
    savedOut = execFromProto(resp);
    return true;
}

bool GrpcClient::removeExecutable(const QString& gameId, const QString& id, GrpcError& errorOut)
{
    gorganizer::v1::RemoveExecutableRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_id(id.toStdString());
    gorganizer::v1::RemoveExecutableResponse resp;
    return mapError(invokeUnary(m_syncStub.get(), &Stub::RemoveExecutable, req, resp), "RemoveExecutable", errorOut);
}

bool GrpcClient::detectExecutables(const QString& gameId, QList<GrpcDetectedExecutable>& out, GrpcError& errorOut)
{
    gorganizer::v1::DetectExecutablesRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::DetectExecutablesResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::DetectExecutables, req, resp,
                              std::chrono::seconds(60)), "DetectExecutables", errorOut)) return false;
    out.clear();
    for (const auto& d : resp.detected()) {
        GrpcDetectedExecutable g;
        g.toolId = QString::fromStdString(d.tool_id());
        g.title = QString::fromStdString(d.title());
        g.exePath = QString::fromStdString(d.exe_path());
        g.runner = QString::fromStdString(d.runner());
        g.prefixAppId = d.prefix_app_id();
        g.outputPolicy = QString::fromStdString(d.output_policy());
        g.needsVfsMounted = d.needs_vfs_mounted();
        g.captureOutputToMod = QString::fromStdString(d.capture_output_to_mod());
        g.extraRwScratch = d.extra_rw_scratch();
        for (const auto& arg : d.default_args()) g.defaultArgs << QString::fromStdString(arg);
        out << g;
    }
    return true;
}

bool GrpcClient::launchExecutable(const QString& gameId, const QString& execId, const QString& profileName,
                                  int& pidOut, QString& runIdOut, GrpcError& errorOut, bool autoSort)
{
    gorganizer::v1::LaunchExecutableRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_exec_id(execId.toStdString());
    req.set_profile_name(profileName.toStdString());
    req.set_auto_sort(autoSort);
    gorganizer::v1::LaunchExecutableResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::LaunchExecutable, req, resp,
                              std::chrono::minutes(2)), "LaunchExecutable", errorOut)) return false;
    pidOut = resp.pid();
    runIdOut = QString::fromStdString(resp.run_id());
    return true;
}

namespace {
GrpcManagedToolStatus managedToolStatusFromProto(const gorganizer::v1::ManagedToolStatus& status)
{
    GrpcManagedToolStatus out;
    out.toolId = QString::fromStdString(status.tool_id());
    out.installed = status.installed();
    out.activeVersion = QString::fromStdString(status.active_version());
    out.previousVersion = QString::fromStdString(status.previous_version());
    out.executablePath = QString::fromStdString(status.executable_path());
    out.updateAvailable = QString::fromStdString(status.update_available());
    return out;
}
}

bool GrpcClient::getManagedToolStatus(const QString& toolId, GrpcManagedToolStatus& statusOut, GrpcError& errorOut)
{
    gorganizer::v1::GetManagedToolStatusRequest req;
    req.set_tool_id(toolId.toStdString());
    gorganizer::v1::ManagedToolStatus resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::GetManagedToolStatus, req, resp), "GetManagedToolStatus", errorOut)) return false;
    statusOut = managedToolStatusFromProto(resp);
    return true;
}

bool GrpcClient::installManagedTool(const QString& toolId, GrpcManagedToolStatus& statusOut, GrpcError& errorOut)
{
    gorganizer::v1::InstallManagedToolRequest req;
    req.set_tool_id(toolId.toStdString());
    gorganizer::v1::ManagedToolStatus resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::InstallManagedTool, req, resp,
                              std::chrono::minutes(10)), "InstallManagedTool", errorOut)) return false;
    statusOut = managedToolStatusFromProto(resp);
    return true;
}

bool GrpcClient::rollbackManagedTool(const QString& toolId, GrpcManagedToolStatus& statusOut, GrpcError& errorOut)
{
    gorganizer::v1::RollbackManagedToolRequest req;
    req.set_tool_id(toolId.toStdString());
    gorganizer::v1::ManagedToolStatus resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::RollbackManagedTool, req, resp), "RollbackManagedTool", errorOut)) return false;
    statusOut = managedToolStatusFromProto(resp);
    return true;
}

bool GrpcClient::cancelExecutable(const QString& runId, GrpcError& errorOut)
{
    gorganizer::v1::CancelExecutableRequest req;
    req.set_run_id(runId.toStdString());
    gorganizer::v1::CancelExecutableResponse resp;
    return mapError(invokeUnary(m_syncStub.get(), &Stub::CancelExecutable, req, resp), "CancelExecutable", errorOut);
}

bool GrpcClient::install4GBPatcher(const QString& gameId, QString& patcherExePathOut,
                                    QString& versionOut, GrpcError& errorOut)
{
    gorganizer::v1::Install4GBPatcherRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::Install4GBPatcherResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::Install4GBPatcher, req, resp,
                              std::chrono::minutes(5)), "Install4GBPatcher", errorOut)) return false;
    patcherExePathOut = QString::fromStdString(resp.patcher_exe_path());
    versionOut = QString::fromStdString(resp.version());
    return true;
}

bool GrpcClient::apply4GBPatch(const QString& gameId, const QString& patcherExePath,
                                QString& outputOut, GrpcError& errorOut)
{
    gorganizer::v1::Apply4GBPatchRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_patcher_exe_path(patcherExePath.toStdString());
    gorganizer::v1::Apply4GBPatchResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::Apply4GBPatch, req, resp,
                              std::chrono::minutes(2)), "Apply4GBPatch", errorOut)) return false;
    outputOut = QString::fromStdString(resp.output());
    return true;
}

bool GrpcClient::is4GBPatched(const QString& gameId)
{
    gorganizer::v1::Get4GBPatchStatusRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::Get4GBPatchStatusResponse resp;
    auto s = invokeUnary(m_syncStub.get(), &Stub::Get4GBPatchStatus, req, resp,
                         std::chrono::seconds(3));
    if (!s.ok()) return false;
    return resp.patched();
}

bool GrpcClient::checkTTWPrereqs(int backend, GrpcTTWPrereqStatus& out, GrpcError& errorOut)
{
    gorganizer::v1::CheckTTWPrereqsRequest req;
    req.set_backend(static_cast<gorganizer::v1::TTWBackend>(backend));
    gorganizer::v1::CheckTTWPrereqsResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::CheckTTWPrereqs, req, resp,
                              std::chrono::seconds(15)), "CheckTTWPrereqs", errorOut)) return false;
    out.backend = static_cast<int>(resp.backend());
    out.gstreamerInstalled = resp.gstreamer_installed();
    out.gstreamerCodecsHint = QString::fromStdString(resp.gstreamer_codecs_hint());
    out.xdeltaInstalled = resp.xdelta_installed();
    out.diskSpaceAvailable = resp.disk_space_available();
    out.diskSpaceRequired = resp.disk_space_required();
    out.fnvVanilla = resp.fnv_vanilla();
    out.mpiInstallerPath = QString::fromStdString(resp.mpi_installer_path());
    out.mpiInstallerVersion = QString::fromStdString(resp.mpi_installer_version());
    out.prefixExists = resp.prefix_exists();
    out.hasDotnet48 = resp.has_dotnet48();
    out.dotnet48ReleaseRev = resp.dotnet48_release_rev();
    out.hasMsxml6 = resp.has_msxml6();
    out.hasVcrun2022 = resp.has_vcrun2022();
    out.hasCorefonts = resp.has_corefonts();
    out.monoNeedsRemoval = resp.mono_needs_removal();
    out.steamRunning = resp.steam_running();
    out.protontricksAvailable = resp.protontricks_available();
    out.winetricksAvailable = resp.winetricks_available();
    out.missing.clear();
    for (const auto& m : resp.missing())
        out.missing.append(QString::fromStdString(m));
    return true;
}

bool GrpcClient::checkTTWDiskSpace(int64_t& availableOut, int64_t& requiredOut, GrpcError& errorOut)
{
    gorganizer::v1::CheckTTWDiskSpaceRequest req;
    gorganizer::v1::CheckTTWDiskSpaceResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::CheckTTWDiskSpace, req, resp,
                              std::chrono::seconds(5)), "CheckTTWDiskSpace", errorOut)) return false;
    availableOut = resp.available();
    requiredOut = resp.required();
    return true;
}

bool GrpcClient::checkFNVNotMounted(GrpcError& errorOut)
{
    gorganizer::v1::CheckFNVNotMountedRequest req;
    gorganizer::v1::CheckFNVNotMountedResponse resp;
    return mapError(invokeUnary(m_syncStub.get(), &Stub::CheckFNVNotMounted, req, resp,
                                std::chrono::seconds(5)), "CheckFNVNotMounted", errorOut);
}

bool GrpcClient::prepareTTWInstaller(const QString& userPath, int backend,
                                     GrpcTTWInstallerInfo& out, GrpcError& errorOut)
{
    gorganizer::v1::PrepareTTWInstallerRequest req;
    req.set_user_path(userPath.toStdString());
    req.set_backend(static_cast<gorganizer::v1::TTWBackend>(backend));
    gorganizer::v1::PrepareTTWInstallerResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::PrepareTTWInstaller, req, resp,
                              std::chrono::seconds(15)), "PrepareTTWInstaller", errorOut)) return false;
    out.backend = static_cast<int>(resp.backend());
    out.mpiFile = QString::fromStdString(resp.mpi_file());
    out.installerExe = QString::fromStdString(resp.installer_exe());
    out.version = QString::fromStdString(resp.version());
    out.alternateMpis.clear();
    for (const auto& m : resp.alternate_mpis())
        out.alternateMpis.append(QString::fromStdString(m));
    return true;
}

bool GrpcClient::createBlankTTWMod(const QString& modName, QString& modDirOut, GrpcError& errorOut)
{
    gorganizer::v1::CreateBlankTTWModRequest req;
    req.set_mod_name(modName.toStdString());
    gorganizer::v1::CreateBlankTTWModResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::CreateBlankTTWMod, req, resp,
                              std::chrono::seconds(10)), "CreateBlankTTWMod", errorOut)) return false;
    modDirOut = QString::fromStdString(resp.mod_dir());
    return true;
}

bool GrpcClient::ensureNativeMpiInstaller(QString& pathOut, QString& versionOut, GrpcError& errorOut)
{
    gorganizer::v1::EnsureNativeMpiInstallerRequest req;
    gorganizer::v1::EnsureNativeMpiInstallerResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::EnsureNativeMpiInstaller, req, resp,
                              std::chrono::minutes(2)), "EnsureNativeMpiInstaller", errorOut)) return false;
    pathOut = QString::fromStdString(resp.path());
    versionOut = QString::fromStdString(resp.version());
    return true;
}

bool GrpcClient::bootstrapFNVPrefix(GrpcError& errorOut)
{
    gorganizer::v1::BootstrapFNVPrefixRequest req;
    gorganizer::v1::BootstrapFNVPrefixResponse resp;
    return mapError(invokeUnary(m_syncStub.get(), &Stub::BootstrapFNVPrefix, req, resp,
                                std::chrono::minutes(2)), "BootstrapFNVPrefix", errorOut);
}

bool GrpcClient::installTTWPrereqs(QString& installIdOut, GrpcError& errorOut)
{
    gorganizer::v1::InstallTTWPrereqsRequest req;
    gorganizer::v1::InstallTTWPrereqsResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::InstallTTWPrereqs, req, resp,
                              std::chrono::seconds(10)), "InstallTTWPrereqs", errorOut)) return false;
    installIdOut = QString::fromStdString(resp.install_id());
    return true;
}

bool GrpcClient::launchTTWInstaller(const GrpcTTWInstallerInfo& info, const QString& dataModName,
                                    QString& installIdOut, GrpcError& errorOut)
{
    gorganizer::v1::LaunchTTWInstallerRequest req;
    auto* infoMsg = req.mutable_info();
    infoMsg->set_backend(static_cast<gorganizer::v1::TTWBackend>(info.backend));
    infoMsg->set_mpi_file(info.mpiFile.toStdString());
    infoMsg->set_installer_exe(info.installerExe.toStdString());
    infoMsg->set_version(info.version.toStdString());
    for (const auto& alt : info.alternateMpis)
        infoMsg->add_alternate_mpis(alt.toStdString());
    req.set_data_mod_name(dataModName.toStdString());
    gorganizer::v1::LaunchTTWInstallerResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::LaunchTTWInstaller, req, resp,
                              std::chrono::seconds(15)), "LaunchTTWInstaller", errorOut)) return false;
    installIdOut = QString::fromStdString(resp.install_id());
    return true;
}

bool GrpcClient::cancelTTWInstaller(const QString& installId, GrpcError& errorOut)
{
    gorganizer::v1::CancelTTWInstallerRequest req;
    req.set_install_id(installId.toStdString());
    gorganizer::v1::CancelTTWInstallerResponse resp;
    return mapError(invokeUnary(m_syncStub.get(), &Stub::CancelTTWInstaller, req, resp), "CancelTTWInstaller", errorOut);
}

bool GrpcClient::getTTWInstallResult(const QString& installId, bool block,
                                     GrpcTTWInstallResult& out, GrpcError& errorOut)
{
    gorganizer::v1::GetTTWInstallResultRequest req;
    req.set_install_id(installId.toStdString());
    req.set_block(block);
    gorganizer::v1::GetTTWInstallResultResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::GetTTWInstallResult, req, resp,
                              std::chrono::seconds(block ? 3600 : 5)), "GetTTWInstallResult", errorOut)) return false;
    out.installerExitCode = resp.installer_exit_code();
    out.layoutFixed = resp.layout_fixed();
    out.dataModFileCount = resp.data_mod_file_count();
    out.dataModBytes = resp.data_mod_bytes();
    out.changedExesInRoot.clear();
    for (const auto& d : resp.changed_exes_in_root()) {
        GrpcTTWExeDelta dd;
        dd.relPath = QString::fromStdString(d.rel_path());
        dd.kind = QString::fromStdString(d.kind());
        dd.size = d.size();
        dd.mtime = QString::fromStdString(d.mtime());
        dd.sha256 = QString::fromStdString(d.sha256());
        out.changedExesInRoot.push_back(std::move(dd));
    }
    out.dataModExes.clear();
    for (const auto& d : resp.data_mod_exes()) {
        GrpcTTWExeDelta dd;
        dd.relPath = QString::fromStdString(d.rel_path());
        dd.kind = QString::fromStdString(d.kind());
        dd.size = d.size();
        dd.mtime = QString::fromStdString(d.mtime());
        dd.sha256 = QString::fromStdString(d.sha256());
        out.dataModExes.push_back(std::move(dd));
    }
    return true;
}

bool GrpcClient::setTTWLauncherExe(const QString& relPath, GrpcError& errorOut)
{
    gorganizer::v1::SetTTWLauncherExeRequest req;
    req.set_rel_path(relPath.toStdString());
    gorganizer::v1::SetTTWLauncherExeResponse resp;
    return mapError(invokeUnary(m_syncStub.get(), &Stub::SetTTWLauncherExe, req, resp,
                                std::chrono::seconds(5)), "SetTTWLauncherExe", errorOut);
}

bool GrpcClient::verifyTTWIntegrity(GrpcError& errorOut)
{
    gorganizer::v1::VerifyTTWIntegrityRequest req;
    gorganizer::v1::VerifyTTWIntegrityResponse resp;
    return mapError(invokeUnary(m_syncStub.get(), &Stub::VerifyTTWIntegrity, req, resp,
                                std::chrono::seconds(5)), "VerifyTTWIntegrity", errorOut);
}

bool GrpcClient::translateWinePath(const QString& gameId, const QString& unixPath,
                                   QString& winePathOut, GrpcError& errorOut)
{
    gorganizer::v1::TranslateWinePathRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_unix_path(unixPath.toStdString());
    gorganizer::v1::TranslateWinePathResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::TranslateWinePath, req, resp,
                              std::chrono::seconds(5)), "TranslateWinePath", errorOut)) return false;
    winePathOut = QString::fromStdString(resp.wine_path());
    return true;
}

bool GrpcClient::previewImport(const QString& gameId, const QString& archivePath,
                               GrpcImportPreview& out, GrpcError& errorOut)
{
    gorganizer::v1::PreviewImportRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_archive_path(archivePath.toStdString());
    gorganizer::v1::PreviewImportResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::PreviewImport, req, resp,
                              std::chrono::seconds(60)), "PreviewImport", errorOut)) return false;
    out = importPreviewFromProto(resp);
    return true;
}

void GrpcClient::startExport(const QString& gameId, const QString& outputPath,
                             const QStringList& modFolders, const QStringList& profileNames,
                             bool includeOverwrite, bool includeGameSettings)
{
    if (!transferWorker()) {
        emit transferFailed(QStringLiteral("not connected"), GrpcStatusUnavailable);
        return;
    }
    if (m_transferActive) {
        emit transferFailed(QStringLiteral("transfer already running"), GrpcStatusFailedPrecondition);
        return;
    }
    m_transferActive = true;
    postTo(transferWorker(), &GrpcWorker::doExportInstance, gameId, outputPath,
           modFolders, profileNames, includeOverwrite, includeGameSettings);
}

void GrpcClient::startImport(const QString& gameId, const QString& archivePath,
                             GrpcTransferPolicy policy, const QMap<QString, int>& modPolicyOverrides,
                             const QStringList& modFolders, const QStringList& profileNames,
                             const QString& expectedArchiveIdentity)
{
    if (!transferWorker()) {
        emit transferFailed(QStringLiteral("not connected"), GrpcStatusUnavailable);
        return;
    }
    if (m_transferActive) {
        emit transferFailed(QStringLiteral("transfer already running"), GrpcStatusFailedPrecondition);
        return;
    }
    m_transferActive = true;
    postTo(transferWorker(), &GrpcWorker::doImportInstance, gameId, archivePath,
           static_cast<int>(policy), modPolicyOverrides, modFolders, profileNames,
           expectedArchiveIdentity);
}

void GrpcClient::cancelTransfer()
{
    if (transferWorker()) transferWorker()->cancelActiveStream();
}

bool GrpcClient::getGameSettings(const QString& gameId, GrpcGameSettings& settingsOut, GrpcError& errorOut)
{
    gorganizer::v1::GetGameSettingsRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::GameSettings resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::GetGameSettings, req, resp), "GetGameSettings", errorOut)) return false;
    settingsOut.gameId = QString::fromStdString(resp.game_id());
    settingsOut.autoInstall = resp.auto_install();
    return true;
}

bool GrpcClient::setGameSettings(const QString& gameId, bool autoInstall, GrpcGameSettings& settingsOut, GrpcError& errorOut)
{
    gorganizer::v1::SetGameSettingsRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_auto_install(autoInstall);
    gorganizer::v1::GameSettings resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::SetGameSettings, req, resp), "SetGameSettings", errorOut)) return false;
    settingsOut.gameId = QString::fromStdString(resp.game_id());
    settingsOut.autoInstall = resp.auto_install();
    return true;
}

bool GrpcClient::listProfileIniFiles(const QString& gameId, const QString& profileName,
                                     std::vector<GrpcProfileIniFile>& filesOut,
                                     GrpcProfileIniStatus& statusOut, GrpcError& errorOut)
{
    gorganizer::v1::ListProfileIniFilesRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    gorganizer::v1::ListProfileIniFilesResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::ListProfileIniFiles, req, resp), "ListProfileIniFiles", errorOut)) return false;
    filesOut.clear();
    for (const auto& f : resp.files()) {
        filesOut.push_back(GrpcProfileIniFile{
            QString::fromStdString(f.filename()),
            QString::fromStdString(f.content()),
            QString::fromStdString(f.disk_path()),
        });
    }
    statusOut.gameId = gameId;
    statusOut.profileName = profileName;
    statusOut.useCustomIni = resp.use_custom_ini();
    statusOut.myGamesDir = QString::fromStdString(resp.my_games_dir());
    statusOut.gameSupportsIni = !resp.files().empty();
    return true;
}

quint64 GrpcClient::saveProfileIniFile(const QString& gameId, const QString& profileName,
                                       const QString& filename, const QString& content)
{
    const quint64 requestId = ++m_nextIniRequestId;
    if (!unaryWorker()) {
        QMetaObject::invokeMethod(this, [this, requestId] {
            emit profileIniSaveFailed(requestId, QStringLiteral("not connected"), GrpcStatusUnavailable);
        }, Qt::QueuedConnection);
        return requestId;
    }
    post(&GrpcWorker::doSaveProfileIniFile, requestId, gameId, profileName, filename, content);
    return requestId;
}

quint64 GrpcClient::applyProfileIniFiles(const QString& gameId, const QString& profileName)
{
    const quint64 requestId = ++m_nextIniRequestId;
    if (!unaryWorker()) {
        QMetaObject::invokeMethod(this, [this, requestId] {
            emit profileIniFilesApplyFailed(requestId, QStringLiteral("not connected"), GrpcStatusUnavailable);
        }, Qt::QueuedConnection);
        return requestId;
    }
    post(&GrpcWorker::doApplyProfileIniFiles, requestId, gameId, profileName);
    return requestId;
}

bool GrpcClient::setProfileIniEnabled(const QString& gameId, const QString& profileName,
                                      bool enabled, GrpcProfileIniStatus& statusOut, GrpcError& errorOut)
{
    gorganizer::v1::SetProfileIniEnabledRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    req.set_enabled(enabled);
    gorganizer::v1::ProfileIniStatus resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::SetProfileIniEnabled, req, resp), "SetProfileIniEnabled", errorOut)) return false;
    statusOut = iniStatusFromProto(resp);
    return true;
}

bool GrpcClient::getProfileIniStatus(const QString& gameId, const QString& profileName,
                                     GrpcProfileIniStatus& statusOut, GrpcError& errorOut)
{
    gorganizer::v1::GetProfileIniStatusRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    gorganizer::v1::ProfileIniStatus resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::GetProfileIniStatus, req, resp), "GetProfileIniStatus", errorOut)) return false;
    statusOut = iniStatusFromProto(resp);
    return true;
}

bool GrpcClient::listIniTweaks(const QString& gameId, const QString& profileName,
                               std::vector<GrpcIniTweakState>& tweaksOut, GrpcError& errorOut)
{
    gorganizer::v1::ListIniTweaksRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    gorganizer::v1::ListIniTweaksResponse resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::ListIniTweaks, req, resp), "ListIniTweaks", errorOut)) return false;
    tweaksOut.clear();
    for (const auto& t : resp.tweaks()) tweaksOut.push_back(iniTweakFromProto(t));
    return true;
}

bool GrpcClient::setIniTweak(const QString& gameId, const QString& profileName,
                             const QString& tweakId, bool enabled,
                             GrpcIniTweakState& stateOut, GrpcError& errorOut)
{
    gorganizer::v1::SetIniTweakRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    req.set_tweak_id(tweakId.toStdString());
    req.set_enabled(enabled);
    gorganizer::v1::IniTweakState resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::SetIniTweak, req, resp), "SetIniTweak", errorOut)) return false;
    stateOut = iniTweakFromProto(resp);
    return true;
}

bool GrpcClient::health(GrpcReadiness& out, GrpcError& errorOut)
{
    gorganizer::v1::HealthRequest req;
    gorganizer::v1::Readiness resp;
    if (!mapError(invokeUnary(m_syncStub.get(), &Stub::Health, req, resp,
                              std::chrono::seconds(2)), "Health", errorOut)) return false;
    out.socketReady = resp.socket_ready();
    out.recoveryDone = resp.recovery_done();
    out.gamesWarmed = resp.games_warmed();
    out.lastInitStep = QString::fromStdString(resp.last_init_step());
    out.version = QString::fromStdString(resp.version());
    out.apiEpoch = resp.api_epoch();
    if (!m_connected) {
        m_connected = true;
        resumeSubscriptions();
        emit connected();
        emit resubscribed();
    }
    return true;
}

}

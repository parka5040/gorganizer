#include "GrpcWorker.h"
#include "GrpcInstallPreview.h"

#include <grpcpp/grpcpp.h>
#include <chrono>
#include <mutex>
#include <utility>

namespace gorganizer {

namespace {
class ScopedCtxRegistration {
public:
    ScopedCtxRegistration(std::mutex& mu, grpc::ClientContext*& slot, grpc::ClientContext& ctx)
        : m_mu(mu)
        , m_slot(slot)
    {
        std::lock_guard<std::mutex> lk(m_mu);
        m_slot = &ctx;
    }

    ~ScopedCtxRegistration()
    {
        std::lock_guard<std::mutex> lk(m_mu);
        m_slot = nullptr;
    }

    ScopedCtxRegistration(const ScopedCtxRegistration&) = delete;
    ScopedCtxRegistration& operator=(const ScopedCtxRegistration&) = delete;

private:
    std::mutex& m_mu;
    grpc::ClientContext*& m_slot;
};

ModLoaderKind modLoaderKindFromProto(gorganizer::v1::ModLoaderKind k)
{
    switch (k) {
    case gorganizer::v1::MOD_LOADER_KIND_SMAPI:
        return ModLoaderKind::Smapi;
    default:
        return ModLoaderKind::None;
    }
}

InstallLayout installLayoutFromProto(gorganizer::v1::InstallLayout l)
{
    switch (l) {
    case gorganizer::v1::INSTALL_LAYOUT_DATA_ROOT:
        return InstallLayout::DataRoot;
    case gorganizer::v1::INSTALL_LAYOUT_SMAPI_MANIFEST:
        return InstallLayout::SmapiManifest;
    default:
        return InstallLayout::Unspecified;
    }
}

GameCapabilities capabilitiesFromProto(const gorganizer::v1::GameCapabilities& c)
{
    GameCapabilities out;
    out.plugins = c.plugins();
    out.ini = c.ini();
    out.loot = c.loot();
    out.modLoader = modLoaderKindFromProto(c.mod_loader());
    out.installLayout = installLayoutFromProto(c.install_layout());
    out.manifestDependencies = c.manifest_dependencies();
    return out;
}

GrpcModLoaderState modLoaderStateFromProto(gorganizer::v1::ModLoaderState s)
{
    switch (s) {
    case gorganizer::v1::MOD_LOADER_STATE_NOT_INSTALLED:
        return GrpcModLoaderStateNotInstalled;
    case gorganizer::v1::MOD_LOADER_STATE_OK:
        return GrpcModLoaderStateOk;
    case gorganizer::v1::MOD_LOADER_STATE_LAUNCHER_REVERTED:
        return GrpcModLoaderStateLauncherReverted;
    case gorganizer::v1::MOD_LOADER_STATE_INCOMPLETE:
        return GrpcModLoaderStateIncomplete;
    case gorganizer::v1::MOD_LOADER_STATE_UNSUPPORTED_BUILD:
        return GrpcModLoaderStateUnsupportedBuild;
    case gorganizer::v1::MOD_LOADER_STATE_INTERRUPTED:
        return GrpcModLoaderStateInterrupted;
    default:
        return GrpcModLoaderStateUnspecified;
    }
}

GrpcVFSLifecycleState vfsLifecycleFromProto(gorganizer::v1::VFSLifecycleState state)
{
    switch (state) {
    case gorganizer::v1::VFS_LIFECYCLE_STATE_READY:
        return GrpcVFSLifecycleState::Ready;
    case gorganizer::v1::VFS_LIFECYCLE_STATE_RECOVERY_DEFERRED:
        return GrpcVFSLifecycleState::RecoveryDeferred;
    case gorganizer::v1::VFS_LIFECYCLE_STATE_RECOVERY_PENDING:
        return GrpcVFSLifecycleState::RecoveryPending;
    default:
        return GrpcVFSLifecycleState::Unspecified;
    }
}

GrpcSteamMaintenanceState steamMaintenanceFromProto(gorganizer::v1::SteamMaintenanceState state)
{
    switch (state) {
    case gorganizer::v1::STEAM_MAINTENANCE_STATE_NONE:
        return GrpcSteamMaintenanceState::None;
    case gorganizer::v1::STEAM_MAINTENANCE_STATE_STEAM_BUSY:
        return GrpcSteamMaintenanceState::SteamBusy;
    case gorganizer::v1::STEAM_MAINTENANCE_STATE_VERIFY_REQUIRED:
        return GrpcSteamMaintenanceState::VerifyRequired;
    case gorganizer::v1::STEAM_MAINTENANCE_STATE_USER_REQUESTED:
        return GrpcSteamMaintenanceState::UserRequested;
    default:
        return GrpcSteamMaintenanceState::Unspecified;
    }
}

GrpcRecoveryKind recoveryKindFromProto(gorganizer::v1::RecoveryKind kind)
{
    switch (kind) {
    case gorganizer::v1::RECOVERY_KIND_DATA:
        return GrpcRecoveryKind::Data;
    case gorganizer::v1::RECOVERY_KIND_MOD_LOADER:
        return GrpcRecoveryKind::ModLoader;
    case gorganizer::v1::RECOVERY_KIND_GAME_ROOT:
        return GrpcRecoveryKind::GameRoot;
    default:
        return GrpcRecoveryKind::Unspecified;
    }
}

gorganizer::v1::RecoveryKind recoveryKindToProto(GrpcRecoveryKind kind)
{
    switch (kind) {
    case GrpcRecoveryKind::Data:
        return gorganizer::v1::RECOVERY_KIND_DATA;
    case GrpcRecoveryKind::ModLoader:
        return gorganizer::v1::RECOVERY_KIND_MOD_LOADER;
    case GrpcRecoveryKind::GameRoot:
        return gorganizer::v1::RECOVERY_KIND_GAME_ROOT;
    default:
        return gorganizer::v1::RECOVERY_KIND_UNSPECIFIED;
    }
}

GrpcModLoaderStatus modLoaderStatusFromProto(const gorganizer::v1::ModLoaderStatus& s)
{
    GrpcModLoaderStatus out;
    out.gameId = QString::fromStdString(s.game_id());
    out.kind = modLoaderKindFromProto(s.kind());
    out.state = modLoaderStateFromProto(s.state());
    out.managed = s.managed();
    out.installedVersion = QString::fromStdString(s.installed_version());
    out.activeVersion = QString::fromStdString(s.active_version());
    out.previousVersion = QString::fromStdString(s.previous_version());
    out.latestVersion = QString::fromStdString(s.latest_version());
    out.updateAvailable = s.update_available();
    out.busy = s.busy();
    out.detail = QString::fromStdString(s.detail());
    return out;
}

GrpcPluginStatus pluginStatusFromProto(const gorganizer::v1::PluginStatusItem& p)
{
    GrpcPluginStatus out;
    out.filename = QString::fromStdString(p.filename());
    out.ext = QString::fromStdString(p.ext());
    out.isLight = p.is_light();
    out.enabled = p.enabled();
    out.fromMod = QString::fromStdString(p.from_mod());
    out.softPending = p.soft_pending();
    for (const auto& iss : p.issues()) {
        GrpcDepIssue issue;
        issue.kind = static_cast<int>(iss.kind());
        issue.master = QString::fromStdString(iss.master());
        issue.softModName = QString::fromStdString(iss.soft_mod_name());
        issue.softModId = iss.soft_mod_id();
        issue.softModUrl = QString::fromStdString(iss.soft_mod_url());
        out.issues.push_back(std::move(issue));
    }
    return out;
}

QStringList stringListFromProto(const google::protobuf::RepeatedPtrField<std::string>& values)
{
    QStringList out;
    out.reserve(values.size());
    for (const auto& v : values)
        out.append(QString::fromStdString(v));
    return out;
}

GrpcModIssueKind modIssueKindFromProto(gorganizer::v1::ModIssueKind k)
{
    switch (k) {
    case gorganizer::v1::MOD_ISSUE_KIND_MISSING:
        return GrpcModIssueMissing;
    case gorganizer::v1::MOD_ISSUE_KIND_DISABLED:
        return GrpcModIssueDisabled;
    case gorganizer::v1::MOD_ISSUE_KIND_VERSION_TOO_LOW:
        return GrpcModIssueVersionTooLow;
    case gorganizer::v1::MOD_ISSUE_KIND_DUPLICATE_ID:
        return GrpcModIssueDuplicateId;
    case gorganizer::v1::MOD_ISSUE_KIND_INVALID_MANIFEST:
        return GrpcModIssueInvalidManifest;
    case gorganizer::v1::MOD_ISSUE_KIND_NEEDS_NEWER_LOADER:
        return GrpcModIssueNeedsNewerLoader;
    case gorganizer::v1::MOD_ISSUE_KIND_NEEDS_NEWER_GAME:
        return GrpcModIssueNeedsNewerGame;
    case gorganizer::v1::MOD_ISSUE_KIND_CIRCULAR:
        return GrpcModIssueCircular;
    case gorganizer::v1::MOD_ISSUE_KIND_FOLDER_COLLISION:
        return GrpcModIssueFolderCollision;
    case gorganizer::v1::MOD_ISSUE_KIND_DEPENDENCY_FAILED:
        return GrpcModIssueDependencyFailed;
    default:
        return GrpcModIssueUnspecified;
    }
}

GrpcModComponentKind modComponentKindFromProto(gorganizer::v1::ModComponentKind k)
{
    switch (k) {
    case gorganizer::v1::MOD_COMPONENT_KIND_CODE:
        return GrpcModComponentCode;
    case gorganizer::v1::MOD_COMPONENT_KIND_CONTENT_PACK:
        return GrpcModComponentContentPack;
    case gorganizer::v1::MOD_COMPONENT_KIND_INVALID:
        return GrpcModComponentInvalid;
    default:
        return GrpcModComponentUnspecified;
    }
}

GrpcFetchOutcome fetchOutcomeFromProto(gorganizer::v1::FetchOutcome o)
{
    switch (o) {
    case gorganizer::v1::FETCH_OUTCOME_QUEUED:
        return GrpcFetchOutcomeQueued;
    case gorganizer::v1::FETCH_OUTCOME_OPEN_URL:
        return GrpcFetchOutcomeOpenUrl;
    case gorganizer::v1::FETCH_OUTCOME_UNRESOLVED:
        return GrpcFetchOutcomeUnresolved;
    case gorganizer::v1::FETCH_OUTCOME_ALREADY_PRESENT:
        return GrpcFetchOutcomeAlreadyPresent;
    default:
        return GrpcFetchOutcomeUnspecified;
    }
}

GrpcModComponent modComponentFromProto(const gorganizer::v1::ModComponent& c)
{
    GrpcModComponent out;
    out.folder = QString::fromStdString(c.folder());
    out.providerMod = QString::fromStdString(c.provider_mod());
    out.uniqueId = QString::fromStdString(c.unique_id());
    out.name = QString::fromStdString(c.name());
    out.version = QString::fromStdString(c.version());
    out.kind = modComponentKindFromProto(c.kind());
    out.bundled = c.bundled();
    out.failed = c.failed();
    for (const auto& i : c.issues()) {
        GrpcModIssue issue;
        issue.kind = modIssueKindFromProto(i.kind());
        issue.targetId = QString::fromStdString(i.target_id());
        issue.requiredVersion = QString::fromStdString(i.required_version());
        issue.foundVersion = QString::fromStdString(i.found_version());
        issue.providers = stringListFromProto(i.providers());
        issue.detail = QString::fromStdString(i.detail());
        out.issues.push_back(std::move(issue));
    }
    out.updateVersion = QString::fromStdString(c.update_version());
    out.updateUrl = QString::fromStdString(c.update_url());
    out.nexusId = c.nexus_id();
    out.updateStale = c.update_stale();
    return out;
}

GrpcModDependencyReport modDependencyReportFromProto(const gorganizer::v1::ModDependencyReport& r)
{
    GrpcModDependencyReport out;
    out.gameId = QString::fromStdString(r.game_id());
    out.profileName = QString::fromStdString(r.profile_name());
    out.components.reserve(r.components_size());
    for (const auto& c : r.components())
        out.components.push_back(modComponentFromProto(c));
    out.missing.reserve(r.missing_size());
    for (const auto& m : r.missing()) {
        GrpcMissingDependency dep;
        dep.uniqueId = QString::fromStdString(m.unique_id());
        dep.minimumVersion = QString::fromStdString(m.minimum_version());
        dep.requiredBy = stringListFromProto(m.required_by());
        dep.disabledProviders = stringListFromProto(m.disabled_providers());
        dep.name = QString::fromStdString(m.name());
        dep.nexusId = m.nexus_id();
        dep.url = QString::fromStdString(m.url());
        dep.resolvable = m.resolvable();
        dep.stale = m.stale();
        out.missing.push_back(std::move(dep));
    }
    out.pendingEnables.reserve(r.pending_enables_size());
    for (const auto& p : r.pending_enables()) {
        out.pendingEnables.push_back(GrpcPendingEnable{
            QString::fromStdString(p.batch_id()),
            QString::fromStdString(p.profile_name()),
            QString::fromStdString(p.mod_name()),
            QString::fromStdString(p.unique_id()),
        });
    }
    out.rootManifestMods = stringListFromProto(r.root_manifest_mods());
    out.remoteChecked = r.remote_checked();
    out.remoteError = QString::fromStdString(r.remote_error());
    out.loaderVersion = QString::fromStdString(r.loader_version());
    out.gameVersion = QString::fromStdString(r.game_version());
    out.recentFailures.reserve(r.recent_failures_size());
    for (const auto& f : r.recent_failures()) {
        GrpcDependencyRequestIssue issue;
        issue.uniqueId = QString::fromStdString(f.unique_id());
        issue.batchId = QString::fromStdString(f.batch_id());
        issue.state = QString::fromStdString(f.state());
        issue.detail = QString::fromStdString(f.detail());
        issue.updatedAt = QDateTime::fromString(QString::fromStdString(f.updated_at()), Qt::ISODate);
        out.recentFailures.push_back(std::move(issue));
    }
    return out;
}

GrpcDependencyFetchResult dependencyFetchResultFromProto(const gorganizer::v1::DependencyFetchResult& r)
{
    GrpcDependencyFetchResult out;
    out.uniqueId = QString::fromStdString(r.unique_id());
    out.outcome = fetchOutcomeFromProto(r.outcome());
    out.downloadId = QString::fromStdString(r.download_id());
    out.url = QString::fromStdString(r.url());
    out.reason = QString::fromStdString(r.reason());
    out.batchId = QString::fromStdString(r.batch_id());
    return out;
}

GrpcInstallCompleted installCompletedFromProto(const gorganizer::v1::InstallCompleted& c)
{
    GrpcInstallCompleted out;
    out.gameId = QString::fromStdString(c.game_id());
    out.modName = QString::fromStdString(c.mod_name());
    out.archiveRelPath = QString::fromStdString(c.archive_rel_path());
    out.batchId = QString::fromStdString(c.batch_id());
    out.batchIds = stringListFromProto(c.batch_ids());
    return out;
}
}

GrpcWorker::GrpcWorker(std::shared_ptr<grpc::Channel> channel)
    : m_channel(std::move(channel))
    , m_stub(gorganizer::v1::Gorganizer::NewStub(m_channel))
{
}

// Cancels the active stream and any in-flight unary RPC so the thread can wind down promptly.
void GrpcWorker::stop()
{
    m_stopped.store(true);
    std::lock_guard<std::mutex> lk(m_streamMu);
    if (m_streamCtx) m_streamCtx->TryCancel();
    if (m_unaryCtx) m_unaryCtx->TryCancel();
}

void GrpcWorker::cancelActiveStream()
{
    std::lock_guard<std::mutex> lk(m_streamMu);
    if (m_streamCtx) m_streamCtx->TryCancel();
}

void GrpcWorker::setStreamGeneration(quint64 generation)
{
    std::lock_guard<std::mutex> lk(m_streamMu);
    m_streamGeneration.store(generation);
    if (m_streamCtx) m_streamCtx->TryCancel();
}

template <typename Req, typename Resp, typename Method>
grpc::Status GrpcWorker::invoke(Method method, const Req& req, Resp& resp,
                                std::chrono::milliseconds deadline)
{
    grpc::ClientContext ctx;
    setUnaryDeadline(ctx, deadline);
    ScopedCtxRegistration reg(m_streamMu, m_unaryCtx, ctx);
    return ((*m_stub).*method)(&ctx, req, &resp);
}

template <typename Req, typename Resp, typename Method>
bool GrpcWorker::call(const char* rpcName, Method method, const Req& req, Resp& resp,
                      std::chrono::milliseconds deadline)
{
    auto status = invoke(method, req, resp, deadline);
    if (!status.ok()) {
        emit rpcError(rpcName, QString::fromStdString(status.error_message()));
        return false;
    }
    return true;
}

template <typename Req, typename Ev, typename Dispatch>
grpc::Status GrpcWorker::runStream(std::unique_ptr<grpc::ClientReader<Ev>> (Stub::*method)(grpc::ClientContext*, const Req&),
                                   const Req& req, Dispatch dispatch, quint64 generation)
{
    grpc::ClientContext ctx;
    ScopedCtxRegistration reg(m_streamMu, m_streamCtx, ctx);
    if (m_stopped.load() || (generation && m_streamGeneration.load() != generation))
        return grpc::Status(grpc::StatusCode::CANCELLED, "stream cancelled");
    auto reader = ((*m_stub).*method)(&ctx, req);
    Ev event;
    while (!m_stopped.load() && reader->Read(&event)) {
        if (generation && m_streamGeneration.load() != generation) {
            ctx.TryCancel();
            break;
        }
        dispatch(event);
    }
    if (m_stopped.load()) ctx.TryCancel();
    return reader->Finish();
}

template <typename Method>
void GrpcWorker::runModLoaderOperation(quint64 requestId, const QString& gameId, const QString& operation,
                                       Method method, const gorganizer::v1::ModLoaderRequest& req,
                                       std::chrono::milliseconds deadline)
{
    gorganizer::v1::ModLoaderStatus resp;
    auto status = invoke(method, req, resp, deadline);
    if (!status.ok()) {
        emit modLoaderOperationFinished(requestId, gameId, operation, false, static_cast<int>(status.error_code()),
                                        GrpcModLoaderStatus{}, QString::fromStdString(status.error_message()));
        return;
    }
    emit modLoaderOperationFinished(requestId, gameId, operation, true, GrpcStatusOk,
                                    modLoaderStatusFromProto(resp), QString());
}

template <typename Req>
void GrpcWorker::runTransferStream(std::unique_ptr<grpc::ClientReader<gorganizer::v1::TransferEvent>> (Stub::*method)(grpc::ClientContext*, const Req&),
                                   const Req& req)
{
    bool haveSummary = false;
    GrpcTransferSummary summary;
    auto status = runStream(method, req, [this, &haveSummary, &summary](const gorganizer::v1::TransferEvent& event) {
        switch (event.event_case()) {
        case gorganizer::v1::TransferEvent::kProgress:
            emit transferProgress(transferProgressFromProto(event.progress()));
            break;
        case gorganizer::v1::TransferEvent::kSummary:
            haveSummary = true;
            summary = transferSummaryFromProto(event.summary());
            break;
        default:
            break;
        }
    });
    if (!status.ok()) {
        emit transferFailed(QString::fromStdString(status.error_message()));
        return;
    }
    if (!haveSummary) {
        emit transferFailed(QStringLiteral("transfer stream ended without a summary"));
        return;
    }
    emit transferCompleted(summary);
}

GrpcGame GrpcWorker::gameFromProto(const gorganizer::v1::Game& g)
{
    GrpcGame out{
        .gameId = QString::fromStdString(g.game_id()),
        .name = QString::fromStdString(g.name()),
        .steamAppId = g.steam_app_id(),
        .installPath = QString::fromStdString(g.install_path()),
        .dataPath = QString::fromStdString(g.data_path()),
        .synthetic = g.synthetic(),
        .linkedFromGameId = QString::fromStdString(g.linked_from_game_id()),
        .vfsActive = g.vfs_active(),
    };
    if (g.has_capabilities()) {
        out.capabilities = capabilitiesFromProto(g.capabilities());
        out.capabilitiesKnown = true;
    }
    return out;
}

GrpcModInfo GrpcWorker::modFromProto(const gorganizer::v1::ModInfo& m)
{
    return {
        .name = QString::fromStdString(m.name()),
        .gameId = QString::fromStdString(m.game_id()),
        .basePath = QString::fromStdString(m.base_path()),
        .dataPath = QString::fromStdString(m.data_path()),
        .fileCount = m.file_count(),
        .totalSize = m.total_size(),
    };
}

GrpcModListEntry GrpcWorker::modListEntryFromProto(const gorganizer::v1::ModListEntry& e)
{
    return {
        .modName = QString::fromStdString(e.mod_name()),
        .enabled = e.enabled(),
        .priority = e.priority(),
    };
}

GrpcProfile GrpcWorker::profileFromProto(const gorganizer::v1::Profile& p)
{
    return {
        .name = QString::fromStdString(p.name()),
        .gameId = QString::fromStdString(p.game_id()),
        .createdAt = QString::fromStdString(p.created_at()),
    };
}

GrpcVFSStatus GrpcWorker::vfsStatusFromProto(const gorganizer::v1::VFSStatus& s)
{
    GrpcVFSStatus out{
        .mounted = s.mounted(),
        .gameId = QString::fromStdString(s.game_id()),
        .profileName = QString::fromStdString(s.profile_name()),
        .mountPoint = QString::fromStdString(s.mount_point()),
        .enabledModCount = s.enabled_mod_count(),
        .totalFileCount = s.total_file_count(),
        .dirty = s.dirty(),
        .desiredGen = s.desired_gen(),
        .appliedGen = s.applied_gen(),
        .lifecycleState = vfsLifecycleFromProto(s.lifecycle_state()),
        .lifecycleReason = QString::fromStdString(s.lifecycle_reason()),
        .steamMaintenance = steamMaintenanceFromProto(s.steam_maintenance()),
    };
    for (const auto& batch : s.preserved_batches()) {
        out.preservedBatches.push_back({
            QString::fromStdString(batch.batch_id()),
            QString::fromStdString(batch.created_at()),
            batch.file_count(),
            QString::fromStdString(batch.reason()),
            QString::fromStdString(batch.path()),
        });
    }
    if (s.has_pending_recovery()) {
        const auto& pending = s.pending_recovery();
        out.hasPendingRecovery = true;
        out.pendingRecovery = {
            QString::fromStdString(pending.game_id()),
            QString::fromStdString(pending.data_path()),
            QString::fromStdString(pending.backup_path()),
            QString::fromStdString(pending.reason()),
            recoveryKindFromProto(pending.kind()),
            QString::fromStdString(pending.recovery_id()),
        };
    }
    return out;
}

GrpcFileConflict GrpcWorker::conflictFromProto(const gorganizer::v1::FileConflict& c)
{
    QStringList losers;
    for (const auto& l : c.losing_mods())
        losers.append(QString::fromStdString(l));
    return {
        .virtualPath = QString::fromStdString(c.virtual_path()),
        .winningMod = QString::fromStdString(c.winning_mod()),
        .losingMods = losers,
    };
}

GrpcDownloadProgress GrpcWorker::downloadProgressFromProto(const gorganizer::v1::DownloadProgress& d)
{
    return {
        .downloadId = QString::fromStdString(d.download_id()),
        .modName = QString::fromStdString(d.mod_name()),
        .bytesDownloaded = d.bytes_downloaded(),
        .bytesTotal = d.bytes_total(),
        .status = static_cast<int>(d.status()),
        .error = QString::fromStdString(d.error()),
        .queuedAhead = d.queued_ahead(),
    };
}

GrpcInstallProgress GrpcWorker::installProgressFromProto(const gorganizer::v1::InstallProgress& p)
{
    return {
        .installId = QString::fromStdString(p.install_id()),
        .archiveRelPath = QString::fromStdString(p.archive_rel_path()),
        .modName = QString::fromStdString(p.mod_name()),
        .step = static_cast<int>(p.step()),
        .pct = p.pct(),
        .currentFile = QString::fromStdString(p.current_file()),
        .filesDone = p.files_done(),
        .filesTotal = p.files_total(),
        .error = QString::fromStdString(p.error()),
    };
}

GrpcArchiveRow GrpcWorker::archiveRowFromProto(const gorganizer::v1::ArchiveRow& r)
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

GrpcTransferProgress GrpcWorker::transferProgressFromProto(const gorganizer::v1::TransferProgress& p)
{
    return {
        .step = QString::fromStdString(p.step()),
        .currentItem = QString::fromStdString(p.current_item()),
        .itemsDone = p.items_done(),
        .itemsTotal = p.items_total(),
        .bytesDone = p.bytes_done(),
        .bytesTotal = p.bytes_total(),
    };
}

GrpcTransferSummary GrpcWorker::transferSummaryFromProto(const gorganizer::v1::TransferSummary& s)
{
    GrpcTransferSummary out;
    out.modsExported = s.mods_exported();
    out.modsImported = s.mods_imported();
    out.profilesTransferred = s.profiles_transferred();
    for (const auto& sk : s.skipped())
        out.skipped.append(QString::fromStdString(sk));
    for (const auto& [from, to] : s.renamed())
        out.renamed.insert(QString::fromStdString(from), QString::fromStdString(to));
    out.outputPath = QString::fromStdString(s.output_path());
    return out;
}

void GrpcWorker::doListGames()
{
    gorganizer::v1::ListGamesRequest req;
    gorganizer::v1::ListGamesResponse resp;
    if (!call("ListGames", &Stub::ListGames, req, resp)) return;
    std::vector<GrpcGame> games;
    for (const auto& g : resp.games()) games.push_back(gameFromProto(g));
    emit gamesListed(games);
}

void GrpcWorker::doDetectGames()
{
    gorganizer::v1::DetectGamesRequest req;
    gorganizer::v1::DetectGamesResponse resp;
    if (!call("DetectGames", &Stub::DetectGames, req, resp, std::chrono::seconds(60))) return;
    std::vector<GrpcGame> games;
    for (const auto& g : resp.games()) games.push_back(gameFromProto(g));
    emit gamesDetected(games);
}

void GrpcWorker::doConfigureGame(const QString& gameId, const QString& name,
                                  uint32_t steamAppId, const QString& installPath,
                                  const QString& dataSubpath)
{
    gorganizer::v1::ConfigureGameRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_name(name.toStdString());
    req.set_steam_app_id(steamAppId);
    req.set_install_path(installPath.toStdString());
    req.set_data_subpath(dataSubpath.toStdString());
    gorganizer::v1::ConfigureGameResponse resp;
    if (!call("ConfigureGame", &Stub::ConfigureGame, req, resp)) return;
    emit gameConfigured();
}

void GrpcWorker::doListMods(const QString& gameId)
{
    gorganizer::v1::ListModsRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::ListModsResponse resp;
    if (!call("ListMods", &Stub::ListMods, req, resp)) return;
    std::vector<GrpcModInfo> mods;
    for (const auto& m : resp.mods()) mods.push_back(modFromProto(m));
    emit modsListed(mods);
}

void GrpcWorker::doGetMod(const QString& gameId, const QString& modName)
{
    gorganizer::v1::GetModRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_mod_name(modName.toStdString());
    gorganizer::v1::ModInfo resp;
    if (!call("GetMod", &Stub::GetMod, req, resp)) return;
    emit modInfoReceived(modFromProto(resp));
}

void GrpcWorker::doRescanMod(const QString& gameId, const QString& modName)
{
    gorganizer::v1::RescanModRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_mod_name(modName.toStdString());
    gorganizer::v1::ModInfo resp;
    if (!call("RescanMod", &Stub::RescanMod, req, resp, std::chrono::minutes(5))) return;
    emit modInfoReceived(modFromProto(resp));
}

void GrpcWorker::doListProfiles(const QString& gameId)
{
    gorganizer::v1::ListProfilesRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::ListProfilesResponse resp;
    if (!call("ListProfiles", &Stub::ListProfiles, req, resp)) return;
    std::vector<GrpcProfile> profiles;
    for (const auto& p : resp.profiles()) profiles.push_back(profileFromProto(p));
    emit profilesListed(profiles);
}

void GrpcWorker::doCreateProfile(const QString& gameId, const QString& name)
{
    gorganizer::v1::CreateProfileRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_name(name.toStdString());
    gorganizer::v1::Profile resp;
    if (!call("CreateProfile", &Stub::CreateProfile, req, resp)) return;
    emit profileCreated(profileFromProto(resp));
}

void GrpcWorker::doCopyProfile(const QString& gameId, const QString& source, const QString& name)
{
    gorganizer::v1::CopyProfileRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_source_name(source.toStdString());
    req.set_name(name.toStdString());
    gorganizer::v1::Profile resp;
    if (!call("CopyProfile", &Stub::CopyProfile, req, resp, std::chrono::minutes(5))) return;
    emit profileCopied(gameId, profileFromProto(resp));
}

void GrpcWorker::doDeleteProfile(const QString& gameId, const QString& name)
{
    gorganizer::v1::DeleteProfileRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_name(name.toStdString());
    gorganizer::v1::DeleteProfileResponse resp;
    if (!call("DeleteProfile", &Stub::DeleteProfile, req, resp)) return;
    emit profileDeleted();
}

void GrpcWorker::doGetModList(const QString& gameId, const QString& profileName)
{
    gorganizer::v1::GetModListRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    gorganizer::v1::ModListResponse resp;
    if (!call("GetModList", &Stub::GetModList, req, resp)) return;
    std::vector<GrpcModListEntry> entries;
    for (const auto& e : resp.entries()) entries.push_back(modListEntryFromProto(e));
    emit modListReceived(entries);
}

void GrpcWorker::doSetModList(const QString& gameId, const QString& profileName,
                              const std::vector<GrpcModListEntry>& entries)
{
    gorganizer::v1::SetModListRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    for (const auto& e : entries) {
        auto* entry = req.add_entries();
        entry->set_mod_name(e.modName.toStdString());
        entry->set_enabled(e.enabled);
        entry->set_priority(e.priority);
    }
    gorganizer::v1::SetModListResponse resp;
    if (!call("SetModList", &Stub::SetModList, req, resp)) return;
    emit modListUpdated();
}

void GrpcWorker::doMountVfs(const QString& gameId, const QString& profileName)
{
    gorganizer::v1::MountVFSRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    gorganizer::v1::MountVFSResponse resp;
    if (!call("MountVFS", &Stub::MountVFS, req, resp, std::chrono::minutes(5))) return;
    emit vfsMounted(vfsStatusFromProto(resp.status()));
}

void GrpcWorker::doMountVfsWithSwap(const QString& gameId, const QString& profileName)
{
    gorganizer::v1::MountVFSRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    req.set_auto_swap(true);
    gorganizer::v1::MountVFSResponse resp;
    if (!call("MountVFS", &Stub::MountVFS, req, resp, std::chrono::minutes(10))) return;
    emit vfsMounted(vfsStatusFromProto(resp.status()));
}

void GrpcWorker::doRetargetVfs(quint64 requestId, const QString& gameId, const QString& profileName)
{
    gorganizer::v1::MountVFSRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    req.set_auto_swap(true);
    req.set_retarget_if_mounted(true);
    gorganizer::v1::MountVFSResponse resp;
    auto status = invoke(&Stub::MountVFS, req, resp, std::chrono::minutes(10));
    if (!status.ok()) {
        emit vfsRetargetFailed(requestId, gameId, profileName, QString::fromStdString(status.error_message()));
        return;
    }
    emit vfsRetargeted(requestId, vfsStatusFromProto(resp.status()));
}

void GrpcWorker::doUnmountVfs(const QString& gameId)
{
    gorganizer::v1::UnmountVFSRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::UnmountVFSResponse resp;
    if (!call("UnmountVFS", &Stub::UnmountVFS, req, resp)) return;
    emit vfsUnmounted();
}

void GrpcWorker::doUnmountVfsForMaintenance(quint64 requestId, const QString& gameId)
{
    gorganizer::v1::UnmountVFSRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::UnmountVFSResponse resp;
    auto status = invoke(&Stub::UnmountVFS, req, resp, std::chrono::minutes(10));
    if (!status.ok()) {
        emit maintenanceUnmountFinished(requestId, gameId, false, static_cast<int>(status.error_code()),
                                        QString::fromStdString(status.error_message()));
        return;
    }
    emit vfsUnmounted();
    emit maintenanceUnmountFinished(requestId, gameId, true, GrpcStatusOk, QString());
}

void GrpcWorker::doRestoreFromBackup(const QString& gameId, GrpcRecoveryKind kind, const QString& recoveryId)
{
    gorganizer::v1::RestoreFromBackupRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_expected_kind(recoveryKindToProto(kind));
    req.set_recovery_id(recoveryId.toStdString());
    gorganizer::v1::RestoreFromBackupResponse resp;
    if (!call("RestoreFromBackup", &Stub::RestoreFromBackup, req, resp)) return;
    emit daemonInfo(QString("Recovery resolved for %1.").arg(gameId));
    doGetVfsStatus(gameId);
}

void GrpcWorker::doRetryVfsRecovery(const QString& gameId)
{
    gorganizer::v1::RetryVFSRecoveryRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::VFSStatus resp;
    if (!call("RetryVFSRecovery", &Stub::RetryVFSRecovery, req, resp, std::chrono::seconds(30))) return;
    emit vfsStatusReceived(vfsStatusFromProto(resp));
    emit vfsRecoveryRetried(gameId);
}

void GrpcWorker::doGetVfsStatus(const QString& gameId)
{
    gorganizer::v1::GetVFSStatusRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::VFSStatus resp;
    if (!call("GetVFSStatus", &Stub::GetVFSStatus, req, resp)) return;
    emit vfsStatusReceived(vfsStatusFromProto(resp));
}

void GrpcWorker::doQueryVfsStatus(quint64 requestId, const QString& gameId)
{
    gorganizer::v1::GetVFSStatusRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::VFSStatus resp;
    auto status = invoke(&Stub::GetVFSStatus, req, resp);
    if (!status.ok()) {
        emit vfsStatusQueryFailed(requestId, gameId, QString::fromStdString(status.error_message()));
        return;
    }
    GrpcVFSStatus out = vfsStatusFromProto(resp);
    if (out.gameId.isEmpty())
        out.gameId = gameId;
    emit vfsStatusReceived(out);
    emit vfsStatusQueried(requestId, out);
}

void GrpcWorker::doSetSteamMaintenance(quint64 requestId, const QString& gameId, bool enabled,
                                       bool verificationConfirmed)
{
    gorganizer::v1::SetSteamMaintenanceRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_enabled(enabled);
    req.set_verification_confirmed(verificationConfirmed);
    gorganizer::v1::VFSStatus resp;
    auto status = invoke(&Stub::SetSteamMaintenance, req, resp, std::chrono::minutes(10));
    if (!status.ok()) {
        emit steamMaintenanceSetFailed(requestId, gameId, QString::fromStdString(status.error_message()));
        return;
    }
    auto result = vfsStatusFromProto(resp);
    if (result.gameId.isEmpty())
        result.gameId = gameId;
    emit vfsStatusReceived(result);
    emit steamMaintenanceSet(requestId, result);
}

void GrpcWorker::doImportPreservedFiles(quint64 requestId, const QString& gameId, const QString& batchId,
                                        const QString& modName, const QStringList& relativePaths)
{
    gorganizer::v1::ImportPreservedFilesRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_batch_id(batchId.toStdString());
    req.set_mod_name(modName.toStdString());
    for (const auto& path : relativePaths)
        req.add_relative_paths(path.toStdString());
    gorganizer::v1::ImportPreservedFilesResponse resp;
    auto status = invoke(&Stub::ImportPreservedFiles, req, resp, std::chrono::minutes(10));
    if (!status.ok()) {
        emit preservedFilesImportFailed(requestId, gameId, QString::fromStdString(status.error_message()));
        return;
    }
    emit preservedFilesImported(requestId, gameId, QString::fromStdString(resp.mod_name()), resp.file_count());
}

void GrpcWorker::doDeletePreservedBatch(quint64 requestId, const QString& gameId, const QString& batchId)
{
    gorganizer::v1::DeletePreservedBatchRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_batch_id(batchId.toStdString());
    gorganizer::v1::VFSStatus resp;
    auto status = invoke(&Stub::DeletePreservedBatch, req, resp);
    if (!status.ok()) {
        emit preservedBatchDeleteFailed(requestId, gameId, QString::fromStdString(status.error_message()));
        return;
    }
    auto result = vfsStatusFromProto(resp);
    if (result.gameId.isEmpty())
        result.gameId = gameId;
    emit vfsStatusReceived(result);
    emit preservedBatchDeleted(requestId, result);
}

void GrpcWorker::doRebuildVfs(const QString& gameId)
{
    gorganizer::v1::RebuildVFSRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::RebuildVFSResponse resp;
    if (!call("RebuildVFS", &Stub::RebuildVFS, req, resp, std::chrono::minutes(5))) return;
    emit vfsRebuilt();
}

void GrpcWorker::doGetConflicts(const QString& gameId, const QString& profileName)
{
    gorganizer::v1::GetConflictsRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    gorganizer::v1::ConflictsResponse resp;
    if (!call("GetConflicts", &Stub::GetConflicts, req, resp, std::chrono::minutes(2))) return;
    std::vector<GrpcFileConflict> conflicts;
    for (const auto& c : resp.conflicts()) conflicts.push_back(conflictFromProto(c));
    emit conflictsReceived(conflicts);
}

void GrpcWorker::doLaunchGame(const QString& gameId, bool useTool, const QString& profileName)
{
    gorganizer::v1::LaunchGameRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_use_tool(useTool);
    req.set_profile_name(profileName.toStdString());
    gorganizer::v1::LaunchGameResponse resp;
    auto status = invoke(&Stub::LaunchGame, req, resp);
    if (!status.ok()) { emit gameLaunchFailed(QString::fromStdString(status.error_message())); return; }
    emit gameLaunched(resp.pid());
}

void GrpcWorker::doStartDownload(const QString& nxmUri)
{
    gorganizer::v1::StartDownloadRequest req;
    req.set_nxm_uri(nxmUri.toStdString());
    gorganizer::v1::StartDownloadResponse resp;
    if (!call("StartDownload", &Stub::StartDownload, req, resp)) return;
    emit downloadStarted(QString::fromStdString(resp.download_id()), resp.queued_ahead());
}

void GrpcWorker::doCancelDownload(const QString& downloadId)
{
    gorganizer::v1::CancelDownloadRequest req;
    req.set_download_id(downloadId.toStdString());
    gorganizer::v1::CancelDownloadResponse resp;
    if (!call("CancelDownload", &Stub::CancelDownload, req, resp)) return;
    emit downloadCancelled(downloadId);
}

void GrpcWorker::doRetryDownload(const QString& downloadId)
{
    gorganizer::v1::RetryDownloadRequest req;
    req.set_download_id(downloadId.toStdString());
    gorganizer::v1::RetryDownloadResponse resp;
    if (!call("RetryDownload", &Stub::RetryDownload, req, resp)) return;
    emit downloadRetried(downloadId, resp.queued_ahead());
}

void GrpcWorker::doPreviewInstall(quint64 requestId, const QString& gameId,
                                  const QString& archiveRelPath, const QString& externalArchivePath)
{
    auto req = previewInstallRequest(gameId, archiveRelPath, externalArchivePath);
    gorganizer::v1::PreviewInstallResponse resp;
    auto status = invoke(&Stub::PreviewInstall, req, resp, std::chrono::minutes(10));
    if (!status.ok()) {
        emit previewInstallFailed(requestId, QString::fromStdString(status.error_message()));
        return;
    }
    emit previewInstallCompleted(requestId, previewInstallResultFromProto(resp));
}

void GrpcWorker::doDiscardPreview(const QString& previewId)
{
    gorganizer::v1::DiscardPreviewRequest req;
    req.set_preview_id(previewId.toStdString());
    gorganizer::v1::DiscardPreviewResponse resp;
    auto status = invoke(&Stub::DiscardPreview, req, resp, std::chrono::seconds(30));
    if (!status.ok()) qWarning("GrpcWorker: DiscardPreview failed: %s", status.error_message().c_str());
}

void GrpcWorker::doStartInstall(quint64 requestId, const QString& gameId,
                                 const QString& archiveRelPath,
                                 const QString& externalArchivePath, int mode,
                                 const QString& targetMod, const QString& previewId,
                                 const std::vector<GrpcFomodFile>& fomodSelectedFiles,
                                 bool fomodConfirmed, const QString& selectedRoot)
{
    gorganizer::v1::StartInstallRequest req;
    req.set_game_id(gameId.toStdString());
    if (!archiveRelPath.isEmpty()) req.set_archive_rel_path(archiveRelPath.toStdString());
    if (!externalArchivePath.isEmpty()) req.set_external_archive_path(externalArchivePath.toStdString());
    req.set_mode(static_cast<gorganizer::v1::InstallMode>(mode));
    req.set_target_mod(targetMod.toStdString());
    req.set_preview_id(previewId.toStdString());
    req.set_fomod_confirmed(fomodConfirmed);
    req.set_selected_root(selectedRoot.toStdString());
    for (const auto& f : fomodSelectedFiles) {
        auto* pb = req.add_fomod_selected_files();
        pb->set_source(f.source.toStdString());
        pb->set_destination(f.destination.toStdString());
        pb->set_is_folder(f.isFolder);
        pb->set_priority(f.priority);
    }
    gorganizer::v1::StartInstallResponse resp;
    auto status = invoke(&Stub::StartInstall, req, resp, std::chrono::minutes(10));
    if (!status.ok()) {
        emit installRequestFailed(requestId, QString::fromStdString(status.error_message()));
        return;
    }
    emit installRequestCompleted(requestId, QString::fromStdString(resp.mod_folder()), resp.file_count());
}

void GrpcWorker::doSetNexusAPIKey(const QString& apiKey)
{
    gorganizer::v1::SetNexusAPIKeyRequest req;
    req.set_api_key(apiKey.toStdString());
    gorganizer::v1::SetNexusAPIKeyResponse resp;
    if (!call("SetNexusAPIKey", &Stub::SetNexusAPIKey, req, resp, std::chrono::seconds(12))) return;
    emit nexusAPIKeySet(resp.valid(), QString::fromStdString(resp.error_message()));
}

void GrpcWorker::doShutdownDaemon()
{
    gorganizer::v1::ShutdownRequest req;
    gorganizer::v1::ShutdownResponse resp;
    invoke(&Stub::Shutdown, req, resp, std::chrono::seconds(3));
}

void GrpcWorker::doStartWatching(quint64 generation)
{
    gorganizer::v1::WatchStatusRequest req;
    runStream(&Stub::WatchStatus, req, [this](const gorganizer::v1::StatusEvent& event) {
        switch (event.event_case()) {
        case gorganizer::v1::StatusEvent::kVfsStatus:
            emit vfsStatusChanged(vfsStatusFromProto(event.vfs_status()));
            break;
        case gorganizer::v1::StatusEvent::kError:
            emit daemonError(QString::fromStdString(event.error()));
            break;
        case gorganizer::v1::StatusEvent::kInfo:
            emit daemonInfo(QString::fromStdString(event.info()));
            break;
        case gorganizer::v1::StatusEvent::kRecoveryPending: {
            const auto& rp = event.recovery_pending();
            emit recoveryPending(GrpcRecoveryPending{
                QString::fromStdString(rp.game_id()),
                QString::fromStdString(rp.data_path()),
                QString::fromStdString(rp.backup_path()),
                QString::fromStdString(rp.reason()),
                recoveryKindFromProto(rp.kind()),
                QString::fromStdString(rp.recovery_id()),
            });
            break;
        }
        case gorganizer::v1::StatusEvent::kDependencyWarning: {
            const auto& dw = event.dependency_warning();
            GrpcDependencyWarning out;
            out.pluginFilename = QString::fromStdString(dw.plugin_filename());
            out.detail = QString::fromStdString(dw.detail());
            out.kind = static_cast<int>(dw.kind());
            emit dependencyWarning(out);
            break;
        }
        default:
            break;
        }
    }, generation);
}

void GrpcWorker::doStreamArchiveEvents(const QString& gameId, quint64 generation)
{
    gorganizer::v1::StreamArchiveEventsRequest req;
    req.set_game_id(gameId.toStdString());
    auto status = runStream(&Stub::StreamArchiveEvents, req, [this, generation](const gorganizer::v1::ArchiveEvent& event) {
        GrpcArchiveEvent out;
        switch (event.event_case()) {
        case gorganizer::v1::ArchiveEvent::kDownloadProgress:
            out.kind = GrpcArchiveEvent::KindDownloadProgress;
            out.progress = downloadProgressFromProto(event.download_progress());
            break;
        case gorganizer::v1::ArchiveEvent::kRowChanged:
            out.kind = GrpcArchiveEvent::KindRowChanged;
            out.row = archiveRowFromProto(event.row_changed());
            break;
        case gorganizer::v1::ArchiveEvent::kArchiveRemoved:
            out.kind = GrpcArchiveEvent::KindArchiveRemoved;
            out.archiveRemoved = QString::fromStdString(event.archive_removed());
            break;
        default:
            return;
        }
        emit streamEventReceived(StreamArchive, generation);
        emit archiveEventReceived(generation, out);
    }, generation);
    if (!m_stopped.load() && m_streamGeneration.load() == generation)
        emit streamEnded(StreamArchive, generation, static_cast<int>(status.error_code()));
}

void GrpcWorker::doStreamInstallEvents(const QString& gameId, quint64 generation)
{
    gorganizer::v1::StreamInstallEventsRequest req;
    req.set_game_id(gameId.toStdString());
    auto status = runStream(&Stub::StreamInstallEvents, req, [this, generation](const gorganizer::v1::InstallEvent& event) {
        switch (event.event_case()) {
        case gorganizer::v1::InstallEvent::kInstallProgress:
            emit streamEventReceived(StreamInstall, generation);
            emit installProgressEvent(generation, installProgressFromProto(event.install_progress()));
            break;
        case gorganizer::v1::InstallEvent::kInstallCompleted:
            emit streamEventReceived(StreamInstall, generation);
            emit installCompletedHintReceived(generation, installCompletedFromProto(event.install_completed()));
            break;
        default:
            break;
        }
    }, generation);
    if (!m_stopped.load() && m_streamGeneration.load() == generation)
        emit streamEnded(StreamInstall, generation, static_cast<int>(status.error_code()));
}

void GrpcWorker::doExportInstance(const QString& gameId, const QString& outputPath,
                                  const QStringList& modFolders, const QStringList& profileNames,
                                  bool includeOverwrite, bool includeGameSettings)
{
    gorganizer::v1::ExportInstanceRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_output_path(outputPath.toStdString());
    for (const auto& f : modFolders)
        req.add_mod_folders(f.toStdString());
    for (const auto& p : profileNames)
        req.add_profile_names(p.toStdString());
    req.set_include_overwrite(includeOverwrite);
    req.set_include_game_settings(includeGameSettings);
    runTransferStream(&Stub::ExportInstance, req);
}

void GrpcWorker::doImportInstance(const QString& gameId, const QString& archivePath,
                                  int policy, const QMap<QString, int>& modPolicyOverrides,
                                  const QStringList& modFolders, const QStringList& profileNames)
{
    gorganizer::v1::ImportInstanceRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_archive_path(archivePath.toStdString());
    req.set_policy(static_cast<gorganizer::v1::TransferCollisionPolicy>(policy));
    for (auto it = modPolicyOverrides.constBegin(); it != modPolicyOverrides.constEnd(); ++it)
        (*req.mutable_mod_policy_overrides())[it.key().toStdString()] =
            static_cast<gorganizer::v1::TransferCollisionPolicy>(it.value());
    for (const auto& f : modFolders)
        req.add_mod_folders(f.toStdString());
    for (const auto& p : profileNames)
        req.add_profile_names(p.toStdString());
    runTransferStream(&Stub::ImportInstance, req);
}

void GrpcWorker::doStreamPluginStatus(const QString& gameId, const QString& profileName, quint64 generation)
{
    gorganizer::v1::StreamPluginStatusRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    auto status = runStream(&Stub::StreamPluginStatus, req, [this, generation](const gorganizer::v1::PluginStatusEvent& event) {
        switch (event.event_case()) {
        case gorganizer::v1::PluginStatusEvent::kSnapshot: {
            std::vector<GrpcPluginStatus> items;
            items.reserve(event.snapshot().plugins_size());
            for (const auto& p : event.snapshot().plugins()) {
                items.push_back(pluginStatusFromProto(p));
            }
            emit streamEventReceived(StreamPluginStatus, generation);
            emit pluginStatusSnapshot(generation, items);
            break;
        }
        case gorganizer::v1::PluginStatusEvent::kUpdate:
            emit streamEventReceived(StreamPluginStatus, generation);
            emit pluginStatusUpdate(generation, pluginStatusFromProto(event.update().plugin()));
            break;
        default:
            break;
        }
    }, generation);
    if (!m_stopped.load() && m_streamGeneration.load() == generation)
        emit streamEnded(StreamPluginStatus, generation, static_cast<int>(status.error_code()));
}

// Queries the game's mod-loader status, allowing the longer network deadline when the latest release is resolved too.
void GrpcWorker::doGetModLoaderStatus(quint64 requestId, const QString& gameId, bool checkLatest)
{
    gorganizer::v1::ModLoaderRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_check_latest(checkLatest);
    gorganizer::v1::ModLoaderStatus resp;
    const std::chrono::milliseconds deadline = checkLatest ? std::chrono::seconds(60) : std::chrono::seconds(30);
    auto status = invoke(&Stub::GetModLoaderStatus, req, resp, deadline);
    if (!status.ok()) {
        emit modLoaderStatusFailed(requestId, gameId, QString::fromStdString(status.error_message()));
        return;
    }
    emit modLoaderStatusReceived(requestId, gameId, modLoaderStatusFromProto(resp));
}

void GrpcWorker::doInstallModLoader(quint64 requestId, const QString& gameId, bool repairOnly)
{
    gorganizer::v1::ModLoaderRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_repair_only(repairOnly);
    runModLoaderOperation(requestId, gameId, repairOnly ? QStringLiteral("repair") : QStringLiteral("install"),
                          &Stub::InstallModLoader, req, std::chrono::minutes(20));
}

void GrpcWorker::doUninstallModLoader(quint64 requestId, const QString& gameId)
{
    gorganizer::v1::ModLoaderRequest req;
    req.set_game_id(gameId.toStdString());
    runModLoaderOperation(requestId, gameId, QStringLiteral("uninstall"),
                          &Stub::UninstallModLoader, req, std::chrono::minutes(5));
}

void GrpcWorker::doRollbackModLoader(quint64 requestId, const QString& gameId)
{
    gorganizer::v1::ModLoaderRequest req;
    req.set_game_id(gameId.toStdString());
    runModLoaderOperation(requestId, gameId, QStringLiteral("rollback"),
                          &Stub::RollbackModLoader, req, std::chrono::minutes(20));
}

// Reads a profile's modlist for a caller that correlates the answer by request id.
void GrpcWorker::doGetModListRequest(quint64 requestId, const QString& gameId, const QString& profileName)
{
    gorganizer::v1::GetModListRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    gorganizer::v1::ModListResponse resp;
    auto status = invoke(&Stub::GetModList, req, resp);
    if (!status.ok()) {
        emit modListRequestFailed(requestId, gameId, profileName, QString::fromStdString(status.error_message()));
        return;
    }
    std::vector<GrpcModListEntry> entries;
    entries.reserve(resp.entries_size());
    for (const auto& e : resp.entries()) entries.push_back(modListEntryFromProto(e));
    emit modListRequestReceived(requestId, gameId, profileName, entries);
}

// Persists a modlist and reports the outcome only through the request-id signals, never the generic rpcError.
void GrpcWorker::doSetModListRequest(quint64 requestId, const QString& gameId, const QString& profileName,
                                     const std::vector<GrpcModListEntry>& entries)
{
    gorganizer::v1::SetModListRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    for (const auto& e : entries) {
        auto* entry = req.add_entries();
        entry->set_mod_name(e.modName.toStdString());
        entry->set_enabled(e.enabled);
        entry->set_priority(e.priority);
    }
    gorganizer::v1::SetModListResponse resp;
    auto status = invoke(&Stub::SetModList, req, resp);
    if (!status.ok()) {
        emit modListSaveFailed(requestId, gameId, profileName, QString::fromStdString(status.error_message()));
        return;
    }
    emit modListUpdated();
    emit modListSaved(requestId, gameId, profileName);
}

void GrpcWorker::doReinstallMod(quint64 requestId, const QString& gameId, const QString& modName)
{
    gorganizer::v1::ReinstallModRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_mod_name(modName.toStdString());
    gorganizer::v1::ReinstallModResponse resp;
    const auto status = invoke(&Stub::ReinstallMod, req, resp, std::chrono::minutes(30));
    if (!status.ok()) {
        emit modActionFailed(requestId, gameId, modName, QStringLiteral("ReinstallMod"),
                             QString::fromStdString(status.error_message()));
        return;
    }
    GrpcReinstallResult result;
    result.archivesReplayed = resp.archives_replayed();
    result.archivesSkipped = resp.archives_skipped();
    result.fileCount = resp.file_count();
    emit modReinstalled(requestId, gameId, modName, result);
}

void GrpcWorker::doUninstallMod(quint64 requestId, const QString& gameId, const QString& modName, bool force)
{
    gorganizer::v1::UninstallModRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_mod_name(modName.toStdString());
    req.set_force(force);
    gorganizer::v1::UninstallModResponse resp;
    const auto status = invoke(&Stub::UninstallMod, req, resp, std::chrono::minutes(10));
    if (!status.ok()) {
        emit modActionFailed(requestId, gameId, modName, QStringLiteral("UninstallMod"),
                             QString::fromStdString(status.error_message()));
        return;
    }
    QStringList flaggedArchives;
    for (const auto& archive : resp.archives_flagged_uninstalled())
        flaggedArchives.append(QString::fromStdString(archive));
    emit modUninstalled(requestId, gameId, modName, flaggedArchives);
}

void GrpcWorker::doRenameMod(quint64 requestId, const QString& gameId, const QString& oldName, const QString& newName)
{
    gorganizer::v1::RenameModRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_old_name(oldName.toStdString());
    req.set_new_name(newName.toStdString());
    gorganizer::v1::RenameModResponse resp;
    const auto status = invoke(&Stub::RenameMod, req, resp, std::chrono::minutes(10));
    if (!status.ok()) {
        emit modActionFailed(requestId, gameId, oldName, QStringLiteral("RenameMod"),
                             QString::fromStdString(status.error_message()));
        return;
    }
    emit modRenamed(requestId, gameId, oldName, newName);
}

// Requests a SMAPI dependency report, allowing a longer deadline when smapi.io is consulted.
void GrpcWorker::doGetModDependencyReport(quint64 requestId, const QString& gameId, const QString& profileName,
                                          bool refreshRemote, bool forceRemote)
{
    gorganizer::v1::ModDependencyReportRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    req.set_refresh_remote(refreshRemote || forceRemote);
    req.set_force_remote(forceRemote);
    gorganizer::v1::ModDependencyReport resp;
    const std::chrono::milliseconds deadline =
        (refreshRemote || forceRemote) ? std::chrono::seconds(90) : std::chrono::seconds(30);
    auto status = invoke(&Stub::GetModDependencyReport, req, resp, deadline);
    if (!status.ok()) {
        emit modDependencyReportFailed(requestId, gameId, profileName,
                                       QString::fromStdString(status.error_message()));
        return;
    }
    emit modDependencyReportReceived(requestId, modDependencyReportFromProto(resp));
}

// Registers dependency requests for the given UniqueIDs and reports each ID's outcome.
void GrpcWorker::doFetchModDependencies(quint64 requestId, const QString& gameId, const QString& profileName,
                                        const QStringList& uniqueIds)
{
    gorganizer::v1::FetchModDependenciesRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    for (const auto& id : uniqueIds)
        req.add_unique_ids(id.toStdString());
    gorganizer::v1::FetchModDependenciesResponse resp;
    auto status = invoke(&Stub::FetchModDependencies, req, resp, std::chrono::seconds(120));
    if (!status.ok()) {
        emit modDependencyFetchFailed(requestId, gameId, profileName,
                                      QString::fromStdString(status.error_message()));
        return;
    }
    std::vector<GrpcDependencyFetchResult> results;
    results.reserve(resp.results_size());
    for (const auto& r : resp.results())
        results.push_back(dependencyFetchResultFromProto(r));
    emit modDependenciesFetched(requestId, gameId, profileName, results);
}

// Marks a batch's pending dependency enables done after the GUI enabled them.
void GrpcWorker::doAckDependencyEnable(quint64 requestId, const QString& gameId, const QString& batchId,
                                       const QStringList& uniqueIds)
{
    gorganizer::v1::AckDependencyEnableRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_batch_id(batchId.toStdString());
    for (const auto& id : uniqueIds)
        req.add_unique_ids(id.toStdString());
    gorganizer::v1::AckDependencyEnableResponse resp;
    auto status = invoke(&Stub::AckDependencyEnable, req, resp, std::chrono::seconds(15));
    if (!status.ok()) {
        emit dependencyEnableAckFailed(requestId, gameId, batchId, QString::fromStdString(status.error_message()));
        return;
    }
    emit dependencyEnableAcknowledged(requestId, gameId, batchId, resp.acknowledged());
}

}

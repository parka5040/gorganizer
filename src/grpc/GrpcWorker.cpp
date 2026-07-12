#include "GrpcWorker.h"

#include <grpcpp/grpcpp.h>
#include <chrono>
#include <mutex>

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
}

GrpcWorker::GrpcWorker(std::shared_ptr<grpc::Channel> channel)
    : m_stub(gorganizer::v1::Gorganizer::NewStub(channel))
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
                                   const Req& req, Dispatch dispatch)
{
    grpc::ClientContext ctx;
    ScopedCtxRegistration reg(m_streamMu, m_streamCtx, ctx);
    auto reader = ((*m_stub).*method)(&ctx, req);
    Ev event;
    while (!m_stopped.load() && reader->Read(&event))
        dispatch(event);
    if (m_stopped.load()) ctx.TryCancel();
    return reader->Finish();
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
    return {
        .gameId = QString::fromStdString(g.game_id()),
        .name = QString::fromStdString(g.name()),
        .steamAppId = g.steam_app_id(),
        .installPath = QString::fromStdString(g.install_path()),
        .dataPath = QString::fromStdString(g.data_path()),
        .synthetic = g.synthetic(),
        .linkedFromGameId = QString::fromStdString(g.linked_from_game_id()),
        .vfsActive = g.vfs_active(),
    };
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
    return {
        .mounted = s.mounted(),
        .gameId = QString::fromStdString(s.game_id()),
        .profileName = QString::fromStdString(s.profile_name()),
        .mountPoint = QString::fromStdString(s.mount_point()),
        .enabledModCount = s.enabled_mod_count(),
        .totalFileCount = s.total_file_count(),
        .dirty = s.dirty(),
    };
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

void GrpcWorker::doUnmountVfs(const QString& gameId)
{
    gorganizer::v1::UnmountVFSRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::UnmountVFSResponse resp;
    if (!call("UnmountVFS", &Stub::UnmountVFS, req, resp)) return;
    emit vfsUnmounted();
}

void GrpcWorker::doRestoreFromBackup(const QString& gameId)
{
    gorganizer::v1::RestoreFromBackupRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::RestoreFromBackupResponse resp;
    if (!call("RestoreFromBackup", &Stub::RestoreFromBackup, req, resp)) return;
    emit daemonInfo(QString("Recovery resolved for %1.").arg(gameId));
}

void GrpcWorker::doGetVfsStatus(const QString& gameId)
{
    gorganizer::v1::GetVFSStatusRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::VFSStatus resp;
    if (!call("GetVFSStatus", &Stub::GetVFSStatus, req, resp)) return;
    emit vfsStatusReceived(vfsStatusFromProto(resp));
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

void GrpcWorker::doStartInstall(const QString& gameId, const QString& archiveRelPath,
                                 const QString& externalArchivePath, int mode,
                                 const QString& targetMod, const QString& previewId,
                                 const std::vector<GrpcFomodFile>& fomodSelectedFiles)
{
    gorganizer::v1::StartInstallRequest req;
    req.set_game_id(gameId.toStdString());
    if (!archiveRelPath.isEmpty()) req.set_archive_rel_path(archiveRelPath.toStdString());
    if (!externalArchivePath.isEmpty()) req.set_external_archive_path(externalArchivePath.toStdString());
    req.set_mode(static_cast<gorganizer::v1::InstallMode>(mode));
    req.set_target_mod(targetMod.toStdString());
    req.set_preview_id(previewId.toStdString());
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
        emit installFailed(QString::fromStdString(status.error_message()));
        return;
    }
    emit installCompleted(QString::fromStdString(resp.mod_folder()), resp.file_count());
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

void GrpcWorker::doStartWatching()
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
            emit recoveryPending(
                QString::fromStdString(rp.game_id()),
                QString::fromStdString(rp.data_path()),
                QString::fromStdString(rp.backup_path()),
                QString::fromStdString(rp.reason()));
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
    });
}

void GrpcWorker::doStreamArchiveEvents(const QString& gameId)
{
    gorganizer::v1::StreamArchiveEventsRequest req;
    req.set_game_id(gameId.toStdString());
    runStream(&Stub::StreamArchiveEvents, req, [this](const gorganizer::v1::ArchiveEvent& event) {
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
        emit archiveEventReceived(out);
    });
}

void GrpcWorker::doStreamInstallEvents(const QString& gameId)
{
    gorganizer::v1::StreamInstallEventsRequest req;
    req.set_game_id(gameId.toStdString());
    runStream(&Stub::StreamInstallEvents, req, [this](const gorganizer::v1::InstallEvent& event) {
        switch (event.event_case()) {
        case gorganizer::v1::InstallEvent::kInstallProgress:
            emit installProgressEvent(installProgressFromProto(event.install_progress()));
            break;
        default:
            break;
        }
    });
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

void GrpcWorker::doStreamPluginStatus(const QString& gameId, const QString& profileName)
{
    gorganizer::v1::StreamPluginStatusRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    runStream(&Stub::StreamPluginStatus, req, [this](const gorganizer::v1::PluginStatusEvent& event) {
        switch (event.event_case()) {
        case gorganizer::v1::PluginStatusEvent::kSnapshot: {
            std::vector<GrpcPluginStatus> items;
            items.reserve(event.snapshot().plugins_size());
            for (const auto& p : event.snapshot().plugins()) {
                items.push_back(pluginStatusFromProto(p));
            }
            emit pluginStatusSnapshot(items);
            break;
        }
        case gorganizer::v1::PluginStatusEvent::kUpdate:
            emit pluginStatusUpdate(pluginStatusFromProto(event.update().plugin()));
            break;
        default:
            break;
        }
    });
}

}

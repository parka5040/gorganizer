#include "GrpcWorker.h"

#include <grpcpp/grpcpp.h>
#include <chrono>
#include <mutex>

namespace gorganizer {

namespace {
// Default deadline for unary RPCs. Chosen generously enough that even
// expensive ops (Steam scan, hardlink-farm mount, full conflict walk)
// fit comfortably; tight enough that a stalled daemon can't wedge a
// worker thread indefinitely. Without these, GrpcClient::disconnect's
// QThread::wait(3000) leaves zombie threads when the daemon dies
// mid-RPC. Streaming RPCs are deliberately NOT clamped — they're
// long-lived by design.
constexpr auto kDefaultUnaryTimeout = std::chrono::seconds(30);
inline void setUnaryDeadline(
    grpc::ClientContext& ctx,
    std::chrono::milliseconds timeout = kDefaultUnaryTimeout)
{
    ctx.set_deadline(std::chrono::system_clock::now() + timeout);
}
} // namespace

GrpcWorker::GrpcWorker(std::shared_ptr<grpc::Channel> channel)
    : m_stub(gorganizer::v1::Gorganizer::NewStub(channel))
{
}

void GrpcWorker::stop()
{
    m_stopped.store(true);
    cancelActiveStream();
}

void GrpcWorker::cancelActiveStream()
{
    std::lock_guard<std::mutex> lk(m_streamMu);
    if (m_streamCtx) m_streamCtx->TryCancel();
}

// --- Conversion helpers ---

GrpcGame GrpcWorker::gameFromProto(const gorganizer::v1::Game& g)
{
    return {
        .gameId = QString::fromStdString(g.game_id()),
        .name = QString::fromStdString(g.name()),
        .steamAppId = g.steam_app_id(),
        .installPath = QString::fromStdString(g.install_path()),
        .dataPath = QString::fromStdString(g.data_path()),
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

// --- Unary RPCs ---

void GrpcWorker::doListGames()
{
    grpc::ClientContext ctx;
    setUnaryDeadline(ctx);
    gorganizer::v1::ListGamesRequest req;
    gorganizer::v1::ListGamesResponse resp;
    auto status = m_stub->ListGames(&ctx, req, &resp);
    if (!status.ok()) { emit rpcError("ListGames", QString::fromStdString(status.error_message())); return; }
    std::vector<GrpcGame> games;
    for (const auto& g : resp.games()) games.push_back(gameFromProto(g));
    emit gamesListed(games);
}

void GrpcWorker::doDetectGames()
{
    grpc::ClientContext ctx;
    setUnaryDeadline(ctx, std::chrono::seconds(60)); // Steam scan across libraries
    gorganizer::v1::DetectGamesRequest req;
    gorganizer::v1::DetectGamesResponse resp;
    auto status = m_stub->DetectGames(&ctx, req, &resp);
    if (!status.ok()) { emit rpcError("DetectGames", QString::fromStdString(status.error_message())); return; }
    std::vector<GrpcGame> games;
    for (const auto& g : resp.games()) games.push_back(gameFromProto(g));
    emit gamesDetected(games);
}

void GrpcWorker::doConfigureGame(const QString& gameId, const QString& name,
                                  uint32_t steamAppId, const QString& installPath,
                                  const QString& dataSubpath)
{
    grpc::ClientContext ctx;
    setUnaryDeadline(ctx);
    gorganizer::v1::ConfigureGameRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_name(name.toStdString());
    req.set_steam_app_id(steamAppId);
    req.set_install_path(installPath.toStdString());
    req.set_data_subpath(dataSubpath.toStdString());
    gorganizer::v1::ConfigureGameResponse resp;
    auto status = m_stub->ConfigureGame(&ctx, req, &resp);
    if (!status.ok()) { emit rpcError("ConfigureGame", QString::fromStdString(status.error_message())); return; }
    emit gameConfigured();
}

void GrpcWorker::doListMods(const QString& gameId)
{
    grpc::ClientContext ctx;
    setUnaryDeadline(ctx);
    gorganizer::v1::ListModsRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::ListModsResponse resp;
    auto status = m_stub->ListMods(&ctx, req, &resp);
    if (!status.ok()) { emit rpcError("ListMods", QString::fromStdString(status.error_message())); return; }
    std::vector<GrpcModInfo> mods;
    for (const auto& m : resp.mods()) mods.push_back(modFromProto(m));
    emit modsListed(mods);
}

void GrpcWorker::doGetMod(const QString& gameId, const QString& modName)
{
    grpc::ClientContext ctx;
    setUnaryDeadline(ctx);
    gorganizer::v1::GetModRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_mod_name(modName.toStdString());
    gorganizer::v1::ModInfo resp;
    auto status = m_stub->GetMod(&ctx, req, &resp);
    if (!status.ok()) { emit rpcError("GetMod", QString::fromStdString(status.error_message())); return; }
    emit modInfoReceived(modFromProto(resp));
}

void GrpcWorker::doRescanMod(const QString& gameId, const QString& modName)
{
    grpc::ClientContext ctx;
    setUnaryDeadline(ctx, std::chrono::minutes(5)); // file walk + hashing
    gorganizer::v1::RescanModRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_mod_name(modName.toStdString());
    gorganizer::v1::ModInfo resp;
    auto status = m_stub->RescanMod(&ctx, req, &resp);
    if (!status.ok()) { emit rpcError("RescanMod", QString::fromStdString(status.error_message())); return; }
    emit modInfoReceived(modFromProto(resp));
}

void GrpcWorker::doListProfiles(const QString& gameId)
{
    grpc::ClientContext ctx;
    setUnaryDeadline(ctx);
    gorganizer::v1::ListProfilesRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::ListProfilesResponse resp;
    auto status = m_stub->ListProfiles(&ctx, req, &resp);
    if (!status.ok()) { emit rpcError("ListProfiles", QString::fromStdString(status.error_message())); return; }
    std::vector<GrpcProfile> profiles;
    for (const auto& p : resp.profiles()) profiles.push_back(profileFromProto(p));
    emit profilesListed(profiles);
}

void GrpcWorker::doCreateProfile(const QString& gameId, const QString& name)
{
    grpc::ClientContext ctx;
    setUnaryDeadline(ctx);
    gorganizer::v1::CreateProfileRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_name(name.toStdString());
    gorganizer::v1::Profile resp;
    auto status = m_stub->CreateProfile(&ctx, req, &resp);
    if (!status.ok()) { emit rpcError("CreateProfile", QString::fromStdString(status.error_message())); return; }
    emit profileCreated(profileFromProto(resp));
}

void GrpcWorker::doDeleteProfile(const QString& gameId, const QString& name)
{
    grpc::ClientContext ctx;
    setUnaryDeadline(ctx);
    gorganizer::v1::DeleteProfileRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_name(name.toStdString());
    gorganizer::v1::DeleteProfileResponse resp;
    auto status = m_stub->DeleteProfile(&ctx, req, &resp);
    if (!status.ok()) { emit rpcError("DeleteProfile", QString::fromStdString(status.error_message())); return; }
    emit profileDeleted();
}

void GrpcWorker::doGetModList(const QString& gameId, const QString& profileName)
{
    grpc::ClientContext ctx;
    setUnaryDeadline(ctx);
    gorganizer::v1::GetModListRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    gorganizer::v1::ModListResponse resp;
    auto status = m_stub->GetModList(&ctx, req, &resp);
    if (!status.ok()) { emit rpcError("GetModList", QString::fromStdString(status.error_message())); return; }
    std::vector<GrpcModListEntry> entries;
    for (const auto& e : resp.entries()) entries.push_back(modListEntryFromProto(e));
    emit modListReceived(entries);
}

void GrpcWorker::doSetModList(const QString& gameId, const QString& profileName,
                              const std::vector<GrpcModListEntry>& entries)
{
    grpc::ClientContext ctx;
    setUnaryDeadline(ctx);
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
    auto status = m_stub->SetModList(&ctx, req, &resp);
    if (!status.ok()) { emit rpcError("SetModList", QString::fromStdString(status.error_message())); return; }
    emit modListUpdated();
}

void GrpcWorker::doMountVfs(const QString& gameId, const QString& profileName)
{
    grpc::ClientContext ctx;
    setUnaryDeadline(ctx, std::chrono::minutes(5)); // hardlink farm build for large modlists
    gorganizer::v1::MountVFSRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    gorganizer::v1::MountVFSResponse resp;
    auto status = m_stub->MountVFS(&ctx, req, &resp);
    if (!status.ok()) { emit rpcError("MountVFS", QString::fromStdString(status.error_message())); return; }
    emit vfsMounted(vfsStatusFromProto(resp.status()));
}

void GrpcWorker::doUnmountVfs(const QString& gameId)
{
    grpc::ClientContext ctx;
    setUnaryDeadline(ctx);
    gorganizer::v1::UnmountVFSRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::UnmountVFSResponse resp;
    auto status = m_stub->UnmountVFS(&ctx, req, &resp);
    if (!status.ok()) { emit rpcError("UnmountVFS", QString::fromStdString(status.error_message())); return; }
    emit vfsUnmounted();
}

void GrpcWorker::doRestoreFromBackup(const QString& gameId)
{
    grpc::ClientContext ctx;
    setUnaryDeadline(ctx);
    gorganizer::v1::RestoreFromBackupRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::RestoreFromBackupResponse resp;
    auto status = m_stub->RestoreFromBackup(&ctx, req, &resp);
    if (!status.ok()) {
        emit rpcError("RestoreFromBackup", QString::fromStdString(status.error_message()));
        return;
    }
    emit daemonInfo(QString("Recovery resolved for %1.").arg(gameId));
}

void GrpcWorker::doGetVfsStatus(const QString& gameId)
{
    grpc::ClientContext ctx;
    setUnaryDeadline(ctx);
    gorganizer::v1::GetVFSStatusRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::VFSStatus resp;
    auto status = m_stub->GetVFSStatus(&ctx, req, &resp);
    if (!status.ok()) { emit rpcError("GetVFSStatus", QString::fromStdString(status.error_message())); return; }
    emit vfsStatusReceived(vfsStatusFromProto(resp));
}

void GrpcWorker::doRebuildVfs(const QString& gameId)
{
    grpc::ClientContext ctx;
    setUnaryDeadline(ctx, std::chrono::minutes(5)); // hardlink farm rebuild
    gorganizer::v1::RebuildVFSRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::RebuildVFSResponse resp;
    auto status = m_stub->RebuildVFS(&ctx, req, &resp);
    if (!status.ok()) { emit rpcError("RebuildVFS", QString::fromStdString(status.error_message())); return; }
    emit vfsRebuilt();
}

void GrpcWorker::doGetConflicts(const QString& gameId, const QString& profileName)
{
    grpc::ClientContext ctx;
    setUnaryDeadline(ctx, std::chrono::minutes(2)); // full mod walk
    gorganizer::v1::GetConflictsRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    gorganizer::v1::ConflictsResponse resp;
    auto status = m_stub->GetConflicts(&ctx, req, &resp);
    if (!status.ok()) { emit rpcError("GetConflicts", QString::fromStdString(status.error_message())); return; }
    std::vector<GrpcFileConflict> conflicts;
    for (const auto& c : resp.conflicts()) conflicts.push_back(conflictFromProto(c));
    emit conflictsReceived(conflicts);
}

void GrpcWorker::doLaunchGame(const QString& gameId, bool useTool, const QString& profileName)
{
    grpc::ClientContext ctx;
    setUnaryDeadline(ctx);
    gorganizer::v1::LaunchGameRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_use_tool(useTool);
    req.set_profile_name(profileName.toStdString());
    gorganizer::v1::LaunchGameResponse resp;
    auto status = m_stub->LaunchGame(&ctx, req, &resp);
    if (!status.ok()) { emit gameLaunchFailed(QString::fromStdString(status.error_message())); return; }
    emit gameLaunched(resp.pid());
}

void GrpcWorker::doStartDownload(const QString& nxmUri)
{
    grpc::ClientContext ctx;
    setUnaryDeadline(ctx);
    gorganizer::v1::StartDownloadRequest req;
    req.set_nxm_uri(nxmUri.toStdString());
    gorganizer::v1::StartDownloadResponse resp;
    auto status = m_stub->StartDownload(&ctx, req, &resp);
    if (!status.ok()) { emit rpcError("StartDownload", QString::fromStdString(status.error_message())); return; }
    emit downloadStarted(QString::fromStdString(resp.download_id()), resp.queued_ahead());
}

void GrpcWorker::doCancelDownload(const QString& downloadId)
{
    grpc::ClientContext ctx;
    setUnaryDeadline(ctx);
    gorganizer::v1::CancelDownloadRequest req;
    req.set_download_id(downloadId.toStdString());
    gorganizer::v1::CancelDownloadResponse resp;
    auto status = m_stub->CancelDownload(&ctx, req, &resp);
    if (!status.ok()) { emit rpcError("CancelDownload", QString::fromStdString(status.error_message())); return; }
    emit downloadCancelled(downloadId);
}

void GrpcWorker::doRetryDownload(const QString& downloadId)
{
    grpc::ClientContext ctx;
    setUnaryDeadline(ctx);
    gorganizer::v1::RetryDownloadRequest req;
    req.set_download_id(downloadId.toStdString());
    gorganizer::v1::RetryDownloadResponse resp;
    auto status = m_stub->RetryDownload(&ctx, req, &resp);
    if (!status.ok()) { emit rpcError("RetryDownload", QString::fromStdString(status.error_message())); return; }
    emit downloadRetried(downloadId, resp.queued_ahead());
}

void GrpcWorker::doStartInstall(const QString& gameId, const QString& archiveRelPath,
                                 const QString& externalArchivePath, int mode,
                                 const QString& targetMod, const QString& previewId,
                                 const std::vector<GrpcFomodFile>& fomodSelectedFiles)
{
    grpc::ClientContext ctx;
    ctx.set_deadline(std::chrono::system_clock::now() + std::chrono::minutes(10));
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
    auto status = m_stub->StartInstall(&ctx, req, &resp);
    if (!status.ok()) {
        emit installFailed(QString::fromStdString(status.error_message()));
        return;
    }
    emit installCompleted(QString::fromStdString(resp.mod_folder()), resp.file_count());
}

void GrpcWorker::doSetNexusAPIKey(const QString& apiKey)
{
    grpc::ClientContext ctx;
    ctx.set_deadline(std::chrono::system_clock::now() + std::chrono::seconds(12));
    gorganizer::v1::SetNexusAPIKeyRequest req;
    req.set_api_key(apiKey.toStdString());
    gorganizer::v1::SetNexusAPIKeyResponse resp;
    auto status = m_stub->SetNexusAPIKey(&ctx, req, &resp);
    if (!status.ok()) { emit rpcError("SetNexusAPIKey", QString::fromStdString(status.error_message())); return; }
    emit nexusAPIKeySet(resp.valid(), QString::fromStdString(resp.error_message()));
}

void GrpcWorker::doShutdownDaemon()
{
    // Tight deadline: the daemon should ack the signal almost
    // instantly. The actual shutdown work happens after the RPC
    // returns; we don't wait for the daemon to fully exit here.
    // GrpcClient::shutdownDaemonSync (used at app exit) is the
    // synchronous path with its own deadline + post-RPC poll.
    grpc::ClientContext ctx;
    setUnaryDeadline(ctx, std::chrono::seconds(3));
    gorganizer::v1::ShutdownRequest req;
    gorganizer::v1::ShutdownResponse resp;
    m_stub->Shutdown(&ctx, req, &resp);
}

void GrpcWorker::doStartWatching()
{
    grpc::ClientContext ctx;
    {
        std::lock_guard<std::mutex> lk(m_streamMu);
        m_streamCtx = &ctx;
    }
    gorganizer::v1::WatchStatusRequest req;
    auto reader = m_stub->WatchStatus(&ctx, req);

    gorganizer::v1::StatusEvent event;
    while (!m_stopped.load() && reader->Read(&event)) {
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
        default:
            break;
        }
    }
    {
        std::lock_guard<std::mutex> lk(m_streamMu);
        m_streamCtx = nullptr;
    }
}

void GrpcWorker::doStreamArchiveEvents(const QString& gameId)
{
    grpc::ClientContext ctx;
    {
        std::lock_guard<std::mutex> lk(m_streamMu);
        m_streamCtx = &ctx;
    }
    gorganizer::v1::StreamArchiveEventsRequest req;
    req.set_game_id(gameId.toStdString());
    auto reader = m_stub->StreamArchiveEvents(&ctx, req);

    gorganizer::v1::ArchiveEvent event;
    while (!m_stopped.load() && reader->Read(&event)) {
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
            continue;
        }
        emit archiveEventReceived(out);
    }
    {
        std::lock_guard<std::mutex> lk(m_streamMu);
        m_streamCtx = nullptr;
    }
}

void GrpcWorker::doStreamInstallEvents(const QString& gameId)
{
    grpc::ClientContext ctx;
    {
        std::lock_guard<std::mutex> lk(m_streamMu);
        m_streamCtx = &ctx;
    }
    gorganizer::v1::StreamInstallEventsRequest req;
    req.set_game_id(gameId.toStdString());
    auto reader = m_stub->StreamInstallEvents(&ctx, req);

    gorganizer::v1::InstallEvent event;
    while (!m_stopped.load() && reader->Read(&event)) {
        switch (event.event_case()) {
        case gorganizer::v1::InstallEvent::kInstallProgress:
            emit installProgressEvent(installProgressFromProto(event.install_progress()));
            break;
        default:
            break;
        }
    }
    {
        std::lock_guard<std::mutex> lk(m_streamMu);
        m_streamCtx = nullptr;
    }
}

} // namespace gorganizer

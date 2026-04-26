#include "GrpcClient.h"
#include "GrpcWorker.h"
#include "gorganizer.grpc.pb.h"

#include <grpcpp/grpcpp.h>
#include <QElapsedTimer>
#include <QFileInfo>
#include <QThread>
#include <chrono>
#include <cstdlib>

namespace gorganizer {

GrpcClient::GrpcClient(QObject* parent)
    : QObject(parent)
{
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
    const char* xdgRuntime = std::getenv("XDG_RUNTIME_DIR");
    std::string dir = xdgRuntime ? xdgRuntime : "/tmp";
    return "unix://" + dir + "/gorganizer/gorganizer.sock";
}

void GrpcClient::connectWorkerSignals(GrpcWorker* worker)
{
    connect(worker, &GrpcWorker::gamesListed, this, &GrpcClient::gamesListed);
    connect(worker, &GrpcWorker::gamesDetected, this, &GrpcClient::gamesDetected);
    connect(worker, &GrpcWorker::gameConfigured, this, &GrpcClient::gameConfigured);
    connect(worker, &GrpcWorker::modsListed, this, &GrpcClient::modsListed);
    connect(worker, &GrpcWorker::modInfoReceived, this, &GrpcClient::modInfoReceived);
    connect(worker, &GrpcWorker::profilesListed, this, &GrpcClient::profilesListed);
    connect(worker, &GrpcWorker::profileCreated, this, &GrpcClient::profileCreated);
    connect(worker, &GrpcWorker::profileDeleted, this, &GrpcClient::profileDeleted);
    connect(worker, &GrpcWorker::modListReceived, this, &GrpcClient::modListReceived);
    connect(worker, &GrpcWorker::modListUpdated, this, &GrpcClient::modListUpdated);
    connect(worker, &GrpcWorker::vfsMounted, this, &GrpcClient::vfsMounted);
    connect(worker, &GrpcWorker::vfsUnmounted, this, &GrpcClient::vfsUnmounted);
    connect(worker, &GrpcWorker::vfsStatusReceived, this, &GrpcClient::vfsStatusReceived);
    connect(worker, &GrpcWorker::vfsRebuilt, this, &GrpcClient::vfsRebuilt);
    connect(worker, &GrpcWorker::conflictsReceived, this, &GrpcClient::conflictsReceived);
    connect(worker, &GrpcWorker::gameLaunched, this, &GrpcClient::gameLaunched);
    connect(worker, &GrpcWorker::gameLaunchFailed, this, &GrpcClient::gameLaunchFailed);
    connect(worker, &GrpcWorker::downloadStarted, this, &GrpcClient::downloadStarted);
    connect(worker, &GrpcWorker::downloadCancelled, this, &GrpcClient::downloadCancelled);
    connect(worker, &GrpcWorker::downloadRetried, this, &GrpcClient::downloadRetried);
    connect(worker, &GrpcWorker::installStarted, this, &GrpcClient::installStarted);
    connect(worker, &GrpcWorker::installCompleted, this, &GrpcClient::installCompleted);
    connect(worker, &GrpcWorker::installFailed, this, &GrpcClient::installFailed);
    connect(worker, &GrpcWorker::nexusAPIKeySet, this, &GrpcClient::nexusAPIKeySet);
    connect(worker, &GrpcWorker::vfsStatusChanged, this, &GrpcClient::vfsStatusChanged);
    connect(worker, &GrpcWorker::archiveEventReceived, this, &GrpcClient::archiveEventReceived);
    connect(worker, &GrpcWorker::installProgressEvent, this, &GrpcClient::installProgressEvent);
    connect(worker, &GrpcWorker::daemonError, this, &GrpcClient::daemonError);
    connect(worker, &GrpcWorker::daemonInfo, this, &GrpcClient::daemonInfo);
    connect(worker, &GrpcWorker::recoveryPending, this, &GrpcClient::recoveryPending);
    connect(worker, &GrpcWorker::rpcError, this, &GrpcClient::rpcError);
}

void GrpcClient::connectToDaemon()
{
    if (m_workerThread) disconnectFromDaemon();

    m_channel = grpc::CreateChannel(socketTarget(), grpc::InsecureChannelCredentials());

    // Unary RPCs on one worker. Streams each get their own so a blocking
    // Read() can't starve a queued unary call.
    m_worker = new GrpcWorker(m_channel);
    m_workerThread = new QThread(this);
    m_worker->moveToThread(m_workerThread);
    connectWorkerSignals(m_worker);

    m_streamWorker = new GrpcWorker(m_channel);
    m_streamThread = new QThread(this);
    m_streamWorker->moveToThread(m_streamThread);
    connectWorkerSignals(m_streamWorker);

    m_archiveWorker = new GrpcWorker(m_channel);
    m_archiveThread = new QThread(this);
    m_archiveWorker->moveToThread(m_archiveThread);
    connectWorkerSignals(m_archiveWorker);

    m_installWorker = new GrpcWorker(m_channel);
    m_installThread = new QThread(this);
    m_installWorker->moveToThread(m_installThread);
    connectWorkerSignals(m_installWorker);

    m_workerThread->start();
    m_streamThread->start();
    m_archiveThread->start();
    m_installThread->start();
    m_connectionTimer->start();
}

void GrpcClient::disconnectFromDaemon()
{
    m_connectionTimer->stop();
    if (m_worker) m_worker->stop();
    if (m_streamWorker) m_streamWorker->stop();
    if (m_archiveWorker) m_archiveWorker->stop();
    if (m_installWorker) m_installWorker->stop();

    // wait(3000) per thread is normally plenty: stop() fires TryCancel
    // on streams, and Tier 1.7's per-RPC deadlines bound any unary call
    // in flight. If a thread *still* hasn't finished, deleting the
    // worker out from under a live stub is the documented Qt path to a
    // crash on app teardown — so leak the thread instead and let Qt's
    // parent-child cleanup reap it on ~GrpcClient (which itself happens
    // after all threads have been signaled stopped). Better a one-time
    // cleanup leak than a heisenbug crash.
    auto shutdown = [](QThread*& t, GrpcWorker*& w, const char* tag) {
        if (!t) return;
        t->quit();
        if (!t->wait(3000)) {
            qWarning("GrpcClient: %s thread did not exit within 3s; "
                     "leaking it to avoid use-after-free on the gRPC stub",
                     tag);
            // Detach: don't delete worker or thread; ~QObject will
            // tear them down when GrpcClient itself goes away.
            t = nullptr;
            w = nullptr;
            return;
        }
        delete w; w = nullptr;
        t = nullptr;
    };
    shutdown(m_workerThread,  m_worker,        "unary");
    shutdown(m_streamThread,  m_streamWorker,  "watch-status");
    shutdown(m_archiveThread, m_archiveWorker, "archive-stream");
    shutdown(m_installThread, m_installWorker, "install-stream");

    // Reset the channel only after every thread has confirmed exit. A
    // worker that's still inside m_stub->XYZ(...) holds a stub backed
    // by this channel; resetting it under their feet is UAF.
    m_channel.reset();

    if (m_connected) {
        m_connected = false;
        emit disconnected();
    }
}

bool GrpcClient::isConnected() const { return m_connected; }

void GrpcClient::onCheckConnection()
{
    if (!m_channel) return;
    auto state = m_channel->GetState(true);
    bool nowConnected = (state == GRPC_CHANNEL_READY);
    if (nowConnected && !m_connected) {
        m_connected = true;
        emit connected();
    } else if (!nowConnected && m_connected) {
        m_connected = false;
        emit disconnected();
    }
}

// --- Delegating async methods ---

void GrpcClient::listGames() { QMetaObject::invokeMethod(m_worker, &GrpcWorker::doListGames); }
void GrpcClient::detectGames() { QMetaObject::invokeMethod(m_worker, &GrpcWorker::doDetectGames); }

void GrpcClient::configureGame(const QString& gameId, const QString& name,
                               uint32_t steamAppId, const QString& installPath,
                               const QString& dataSubpath)
{
    QMetaObject::invokeMethod(m_worker, [this, gameId, name, steamAppId, installPath, dataSubpath] {
        m_worker->doConfigureGame(gameId, name, steamAppId, installPath, dataSubpath);
    });
}

void GrpcClient::listMods(const QString& gameId)
{
    QMetaObject::invokeMethod(m_worker, [this, gameId] { m_worker->doListMods(gameId); });
}

void GrpcClient::getMod(const QString& gameId, const QString& modName)
{
    QMetaObject::invokeMethod(m_worker, [this, gameId, modName] { m_worker->doGetMod(gameId, modName); });
}

void GrpcClient::rescanMod(const QString& gameId, const QString& modName)
{
    QMetaObject::invokeMethod(m_worker, [this, gameId, modName] { m_worker->doRescanMod(gameId, modName); });
}

void GrpcClient::listProfiles(const QString& gameId)
{
    QMetaObject::invokeMethod(m_worker, [this, gameId] { m_worker->doListProfiles(gameId); });
}

void GrpcClient::createProfile(const QString& gameId, const QString& name)
{
    QMetaObject::invokeMethod(m_worker, [this, gameId, name] { m_worker->doCreateProfile(gameId, name); });
}

void GrpcClient::deleteProfile(const QString& gameId, const QString& name)
{
    QMetaObject::invokeMethod(m_worker, [this, gameId, name] { m_worker->doDeleteProfile(gameId, name); });
}

void GrpcClient::getModList(const QString& gameId, const QString& profileName)
{
    QMetaObject::invokeMethod(m_worker, [this, gameId, profileName] { m_worker->doGetModList(gameId, profileName); });
}

void GrpcClient::setModList(const QString& gameId, const QString& profileName,
                            const std::vector<GrpcModListEntry>& entries)
{
    QMetaObject::invokeMethod(m_worker, [this, gameId, profileName, entries] {
        m_worker->doSetModList(gameId, profileName, entries);
    });
}

void GrpcClient::mountVfs(const QString& gameId, const QString& profileName)
{
    QMetaObject::invokeMethod(m_worker, [this, gameId, profileName] { m_worker->doMountVfs(gameId, profileName); });
}

void GrpcClient::unmountVfs(const QString& gameId)
{
    QMetaObject::invokeMethod(m_worker, [this, gameId] { m_worker->doUnmountVfs(gameId); });
}

void GrpcClient::restoreFromBackup(const QString& gameId)
{
    QMetaObject::invokeMethod(m_worker, [this, gameId] { m_worker->doRestoreFromBackup(gameId); });
}

void GrpcClient::getVfsStatus(const QString& gameId)
{
    QMetaObject::invokeMethod(m_worker, [this, gameId] { m_worker->doGetVfsStatus(gameId); });
}

void GrpcClient::rebuildVfs(const QString& gameId)
{
    QMetaObject::invokeMethod(m_worker, [this, gameId] { m_worker->doRebuildVfs(gameId); });
}

void GrpcClient::getConflicts(const QString& gameId, const QString& profileName)
{
    QMetaObject::invokeMethod(m_worker, [this, gameId, profileName] { m_worker->doGetConflicts(gameId, profileName); });
}

void GrpcClient::launchGame(const QString& gameId, bool useTool, const QString& profileName)
{
    QMetaObject::invokeMethod(m_worker, [this, gameId, useTool, profileName] {
        m_worker->doLaunchGame(gameId, useTool, profileName);
    });
}

void GrpcClient::startDownload(const QString& nxmUri)
{
    QMetaObject::invokeMethod(m_worker, [this, nxmUri] { m_worker->doStartDownload(nxmUri); });
}

void GrpcClient::cancelDownload(const QString& downloadId)
{
    QMetaObject::invokeMethod(m_worker, [this, downloadId] { m_worker->doCancelDownload(downloadId); });
}

void GrpcClient::retryDownload(const QString& downloadId)
{
    QMetaObject::invokeMethod(m_worker, [this, downloadId] { m_worker->doRetryDownload(downloadId); });
}

void GrpcClient::startInstall(const QString& gameId, const QString& archiveRelPath,
                              GrpcInstallMode mode, const QString& targetMod,
                              const QString& previewId,
                              const std::vector<GrpcFomodFile>& fomodSelectedFiles)
{
    QMetaObject::invokeMethod(m_worker, [this, gameId, archiveRelPath, mode, targetMod, previewId, fomodSelectedFiles] {
        m_worker->doStartInstall(gameId, archiveRelPath, QString(), static_cast<int>(mode),
                                 targetMod, previewId, fomodSelectedFiles);
    });
}

void GrpcClient::startInstallExternal(const QString& gameId, const QString& externalArchivePath,
                                      GrpcInstallMode mode, const QString& targetMod)
{
    QMetaObject::invokeMethod(m_worker, [this, gameId, externalArchivePath, mode, targetMod] {
        m_worker->doStartInstall(gameId, QString(), externalArchivePath, static_cast<int>(mode),
                                 targetMod, QString(), std::vector<GrpcFomodFile>{});
    });
}

void GrpcClient::startWatching()
{
    QMetaObject::invokeMethod(m_streamWorker, &GrpcWorker::doStartWatching);
}

void GrpcClient::stopWatching()
{
    if (m_streamWorker) m_streamWorker->stop();
}

void GrpcClient::subscribeEvents(const QString& gameId)
{
    if (gameId == m_subscribedGame && m_archiveWorker && m_installWorker) return;

    // Cancel the running streams (if any) so the worker loops exit, then
    // re-invoke with the new gameId.
    if (m_archiveWorker) m_archiveWorker->cancelActiveStream();
    if (m_installWorker) m_installWorker->cancelActiveStream();
    m_subscribedGame = gameId;
    if (gameId.isEmpty()) return;

    QMetaObject::invokeMethod(m_archiveWorker, [this, gameId] {
        m_archiveWorker->doStreamArchiveEvents(gameId);
    });
    QMetaObject::invokeMethod(m_installWorker, [this, gameId] {
        m_installWorker->doStreamInstallEvents(gameId);
    });
}

void GrpcClient::unsubscribeEvents()
{
    if (m_archiveWorker) m_archiveWorker->cancelActiveStream();
    if (m_installWorker) m_installWorker->cancelActiveStream();
    m_subscribedGame.clear();
}

void GrpcClient::setNexusAPIKey(const QString& apiKey)
{
    QMetaObject::invokeMethod(m_worker, [this, apiKey] { m_worker->doSetNexusAPIKey(apiKey); });
}

void GrpcClient::shutdownDaemon()
{
    QMetaObject::invokeMethod(m_worker, &GrpcWorker::doShutdownDaemon);
}

bool GrpcClient::shutdownDaemonSync(int rpcTimeoutMs, int pollTimeoutMs, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    // Inline stub creation: makeStub() lives in an anonymous namespace
    // declared lower in this file, so we can't call it from up here.
    auto stub = gorganizer::v1::Gorganizer::NewStub(m_channel);
    grpc::ClientContext ctx;
    ctx.set_deadline(std::chrono::system_clock::now() + std::chrono::milliseconds(rpcTimeoutMs));
    gorganizer::v1::ShutdownRequest req;
    gorganizer::v1::ShutdownResponse resp;
    auto s = stub->Shutdown(&ctx, req, &resp);
    if (!s.ok()) {
        errorOut = QString::fromStdString(s.error_message());
        // Don't return early — the daemon may already be on its way
        // out (stream cancellation can race the RPC return). Fall
        // through to the socket-poll which is the authoritative
        // signal: ipc.Server.Stop() removes the socket on graceful
        // shutdown.
    }

    // Poll for socket disappearance. The daemon's graceful Stop()
    // removes the socket file as its last act, so its absence is a
    // strong signal that the daemon's gRPC server has fully exited.
    const char* xdgRuntime = std::getenv("XDG_RUNTIME_DIR");
    QString sockPath = QString::fromUtf8(xdgRuntime ? xdgRuntime : "/tmp")
                       + "/gorganizer/gorganizer.sock";
    QElapsedTimer t;
    t.start();
    while (t.elapsed() < pollTimeoutMs) {
        if (!QFileInfo::exists(sockPath)) return true;
        QThread::msleep(50);
    }
    if (errorOut.isEmpty()) errorOut = "daemon did not exit within timeout";
    return false;
}

// --- Synchronous helpers ---

namespace {
std::unique_ptr<gorganizer::v1::Gorganizer::Stub> makeStub(std::shared_ptr<grpc::Channel> channel)
{
    return gorganizer::v1::Gorganizer::NewStub(channel);
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

GrpcFomodPlan fomodPlanFromProto(const gorganizer::v1::FomodPlan& p)
{
    GrpcFomodPlan out;
    out.moduleName = QString::fromStdString(p.module_name());
    out.modulePath = QString::fromStdString(p.module_path());
    for (const auto& f : p.required_files()) {
        out.requiredFiles.push_back(GrpcFomodFile{
            QString::fromStdString(f.source()),
            QString::fromStdString(f.destination()),
            f.is_folder(),
            f.priority(),
        });
    }
    for (const auto& step : p.steps()) {
        GrpcFomodStep s;
        s.name = QString::fromStdString(step.name());
        for (const auto& g : step.groups()) {
            GrpcFomodGroup gg;
            gg.name = QString::fromStdString(g.name());
            gg.type = static_cast<int>(g.type());
            for (const auto& plugin : g.plugins()) {
                GrpcFomodPlugin pp;
                pp.name = QString::fromStdString(plugin.name());
                pp.description = QString::fromStdString(plugin.description());
                pp.imagePath = QString::fromStdString(plugin.image_path());
                pp.defaultState = static_cast<int>(plugin.default_state());
                for (const auto& f : plugin.files()) {
                    pp.files.push_back(GrpcFomodFile{
                        QString::fromStdString(f.source()),
                        QString::fromStdString(f.destination()),
                        f.is_folder(),
                        f.priority(),
                    });
                }
                gg.plugins.push_back(std::move(pp));
            }
            s.groups.push_back(std::move(gg));
        }
        out.steps.push_back(std::move(s));
    }
    return out;
}
} // namespace

bool GrpcClient::listArchives(const QString& gameId, std::vector<GrpcArchiveRow>& rowsOut, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    gorganizer::v1::ListArchivesRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::ListArchivesResponse resp;
    auto s = stub->ListArchives(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
    rowsOut.clear();
    for (const auto& r : resp.rows()) rowsOut.push_back(archiveRowFromProto(r));
    return true;
}

bool GrpcClient::setArchiveHidden(const QString& gameId, const QString& archiveRelPath, bool hidden, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    gorganizer::v1::SetArchiveHiddenRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_archive_rel_path(archiveRelPath.toStdString());
    req.set_hidden(hidden);
    gorganizer::v1::SetArchiveHiddenResponse resp;
    auto s = stub->SetArchiveHidden(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
    return true;
}

bool GrpcClient::setArchivesHiddenBulk(const QString& gameId, bool hidden, GrpcBulkHideScope scope,
                                        int& affectedOut, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    gorganizer::v1::SetArchivesHiddenBulkRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_hidden(hidden);
    req.set_scope(static_cast<gorganizer::v1::SetArchivesHiddenBulkRequest_Scope>(scope));
    gorganizer::v1::SetArchivesHiddenBulkResponse resp;
    auto s = stub->SetArchivesHiddenBulk(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
    affectedOut = resp.affected();
    return true;
}

bool GrpcClient::removeArchive(const QString& gameId, const QString& archiveRelPath, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    gorganizer::v1::RemoveArchiveRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_archive_rel_path(archiveRelPath.toStdString());
    gorganizer::v1::RemoveArchiveResponse resp;
    auto s = stub->RemoveArchive(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
    return true;
}

bool GrpcClient::refreshArchiveMetadata(const QString& gameId, const QString& archiveRelPath,
                                         GrpcArchiveRow& rowOut, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    ctx.set_deadline(std::chrono::system_clock::now() + std::chrono::seconds(30));
    gorganizer::v1::RefreshArchiveMetadataRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_archive_rel_path(archiveRelPath.toStdString());
    gorganizer::v1::RefreshArchiveMetadataResponse resp;
    auto s = stub->RefreshArchiveMetadata(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
    rowOut = archiveRowFromProto(resp.row());
    return true;
}

bool GrpcClient::previewInstall(const QString& gameId, const QString& archiveRelPath,
                                 GrpcPreviewInstallResult& out, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    ctx.set_deadline(std::chrono::system_clock::now() + std::chrono::minutes(5));
    gorganizer::v1::PreviewInstallRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_archive_rel_path(archiveRelPath.toStdString());
    gorganizer::v1::PreviewInstallResponse resp;
    auto s = stub->PreviewInstall(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
    out.previewId = QString::fromStdString(resp.preview_id());
    out.hasFomod = resp.has_fomod();
    if (resp.has_plan()) out.plan = fomodPlanFromProto(resp.plan());
    out.flatFileList.clear();
    for (const auto& f : resp.flat_file_list()) out.flatFileList.append(QString::fromStdString(f));
    return true;
}

bool GrpcClient::startInstallSync(const QString& gameId, const QString& archiveRelPath,
                                   const QString& externalArchivePath,
                                   GrpcInstallMode mode, const QString& targetMod,
                                   const QString& previewId,
                                   const std::vector<GrpcFomodFile>& fomodSelectedFiles,
                                   QString& modFolderOut, int& fileCountOut, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
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
    auto s = stub->StartInstall(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
    modFolderOut = QString::fromStdString(resp.mod_folder());
    fileCountOut = resp.file_count();
    return true;
}

bool GrpcClient::discardPreview(const QString& previewId, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    gorganizer::v1::DiscardPreviewRequest req;
    req.set_preview_id(previewId.toStdString());
    gorganizer::v1::DiscardPreviewResponse resp;
    auto s = stub->DiscardPreview(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
    return true;
}

bool GrpcClient::renameMod(const QString& gameId, const QString& oldName,
                            const QString& newName, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    gorganizer::v1::RenameModRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_old_name(oldName.toStdString());
    req.set_new_name(newName.toStdString());
    gorganizer::v1::RenameModResponse resp;
    auto s = stub->RenameMod(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
    return true;
}

bool GrpcClient::uninstallMod(const QString& gameId, const QString& modName, bool force,
                               std::vector<QString>& archivesFlaggedOut, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    ctx.set_deadline(std::chrono::system_clock::now() + std::chrono::minutes(5));
    gorganizer::v1::UninstallModRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_mod_name(modName.toStdString());
    req.set_force(force);
    gorganizer::v1::UninstallModResponse resp;
    auto s = stub->UninstallMod(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
    archivesFlaggedOut.clear();
    for (const auto& p : resp.archives_flagged_uninstalled())
        archivesFlaggedOut.push_back(QString::fromStdString(p));
    return true;
}

bool GrpcClient::reinstallMod(const QString& gameId, const QString& modName,
                               GrpcReinstallResult& resultOut, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    ctx.set_deadline(std::chrono::system_clock::now() + std::chrono::minutes(10));
    gorganizer::v1::ReinstallModRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_mod_name(modName.toStdString());
    gorganizer::v1::ReinstallModResponse resp;
    auto s = stub->ReinstallMod(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
    resultOut.archivesReplayed = resp.archives_replayed();
    resultOut.archivesSkipped = resp.archives_skipped();
    resultOut.fileCount = resp.file_count();
    return true;
}

bool GrpcClient::registerManualInstall(const QString& gameId, const QString& modName,
                                        const QString& archiveRelPath, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    ctx.set_deadline(std::chrono::system_clock::now() + std::chrono::seconds(30));
    gorganizer::v1::RegisterManualInstallRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_mod_name(modName.toStdString());
    req.set_archive_rel_path(archiveRelPath.toStdString());
    gorganizer::v1::RegisterManualInstallResponse resp;
    auto s = stub->RegisterManualInstall(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
    return true;
}

bool GrpcClient::listOverwriteFiles(const QString& gameId,
                                     std::vector<GrpcOverwriteEntry>& filesOut,
                                     QString& overwriteDirOut, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    ctx.set_deadline(std::chrono::system_clock::now() + std::chrono::seconds(30));
    gorganizer::v1::ListOverwriteFilesRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::ListOverwriteFilesResponse resp;
    auto s = stub->ListOverwriteFiles(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
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
                                        int& fileCountOut, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    ctx.set_deadline(std::chrono::system_clock::now() + std::chrono::minutes(2));
    gorganizer::v1::ExtractOverwriteToModRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_mod_name(modName.toStdString());
    req.set_keep_in_overwrite(keepInOverwrite);
    for (const auto& f : files)
        req.add_files(f.toStdString());
    gorganizer::v1::ExtractOverwriteToModResponse resp;
    auto s = stub->ExtractOverwriteToMod(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
    fileCountOut = resp.file_count();
    return true;
}

bool GrpcClient::listSeparators(const QString& gameId, const QString& profileName,
                                 std::vector<GrpcSeparator>& out, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    gorganizer::v1::ListSeparatorsRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    gorganizer::v1::ListSeparatorsResponse resp;
    auto s = stub->ListSeparators(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
    out.clear();
    out.reserve(resp.separators_size());
    for (const auto& sp : resp.separators()) {
        out.push_back(GrpcSeparator{
            QString::fromStdString(sp.name()),
            QString::fromStdString(sp.visual_index()),
            sp.collapsed(),
        });
    }
    return true;
}

bool GrpcClient::setSeparators(const QString& gameId, const QString& profileName,
                                const std::vector<GrpcSeparator>& seps, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    gorganizer::v1::SetSeparatorsRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    for (const auto& g : seps) {
        auto* sp = req.add_separators();
        sp->set_name(g.name.toStdString());
        sp->set_visual_index(g.visualIndex.toStdString());
        sp->set_collapsed(g.collapsed);
    }
    gorganizer::v1::SetSeparatorsResponse resp;
    auto s = stub->SetSeparators(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
    return true;
}

bool GrpcClient::detectProtonVersions(std::vector<GrpcProtonVersion>& out, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    gorganizer::v1::DetectProtonRequest req;
    gorganizer::v1::DetectProtonResponse resp;
    auto s = stub->DetectProton(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
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

bool GrpcClient::getPreferredProton(QString& pathOut, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    gorganizer::v1::GetPreferredProtonRequest req;
    gorganizer::v1::GetPreferredProtonResponse resp;
    auto s = stub->GetPreferredProton(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
    pathOut = QString::fromStdString(resp.path());
    return true;
}

bool GrpcClient::setPreferredProton(const QString& path, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    gorganizer::v1::SetPreferredProtonRequest req;
    req.set_path(path.toStdString());
    gorganizer::v1::SetPreferredProtonResponse resp;
    auto s = stub->SetPreferredProton(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
    return true;
}

bool GrpcClient::installScriptExtender(const QString& gameId, QString& nameOut, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    ctx.set_deadline(std::chrono::system_clock::now() + std::chrono::minutes(5));
    gorganizer::v1::InstallScriptExtenderRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::InstallScriptExtenderResponse resp;
    auto s = stub->InstallScriptExtender(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
    nameOut = QString::fromStdString(resp.name());
    return true;
}

bool GrpcClient::getGameSettings(const QString& gameId, GrpcGameSettings& settingsOut, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    gorganizer::v1::GetGameSettingsRequest req;
    req.set_game_id(gameId.toStdString());
    gorganizer::v1::GameSettings resp;
    auto s = stub->GetGameSettings(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
    settingsOut.gameId = QString::fromStdString(resp.game_id());
    settingsOut.autoInstall = resp.auto_install();
    return true;
}

bool GrpcClient::setGameSettings(const QString& gameId, bool autoInstall, GrpcGameSettings& settingsOut, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    gorganizer::v1::SetGameSettingsRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_auto_install(autoInstall);
    gorganizer::v1::GameSettings resp;
    auto s = stub->SetGameSettings(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
    settingsOut.gameId = QString::fromStdString(resp.game_id());
    settingsOut.autoInstall = resp.auto_install();
    return true;
}

static GrpcProfileIniStatus iniStatusFromProto(const gorganizer::v1::ProfileIniStatus& s)
{
    GrpcProfileIniStatus out;
    out.gameId = QString::fromStdString(s.game_id());
    out.profileName = QString::fromStdString(s.profile_name());
    out.useCustomIni = s.use_custom_ini();
    out.myGamesDir = QString::fromStdString(s.my_games_dir());
    out.gameSupportsIni = s.game_supports_ini();
    return out;
}

bool GrpcClient::listProfileIniFiles(const QString& gameId, const QString& profileName,
                                     std::vector<GrpcProfileIniFile>& filesOut,
                                     GrpcProfileIniStatus& statusOut, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    gorganizer::v1::ListProfileIniFilesRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    gorganizer::v1::ListProfileIniFilesResponse resp;
    auto s = stub->ListProfileIniFiles(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
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

bool GrpcClient::saveProfileIniFile(const QString& gameId, const QString& profileName,
                                    const QString& filename, const QString& content, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    gorganizer::v1::SaveProfileIniFileRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    req.set_filename(filename.toStdString());
    req.set_content(content.toStdString());
    gorganizer::v1::SaveProfileIniFileResponse resp;
    auto s = stub->SaveProfileIniFile(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
    return true;
}

bool GrpcClient::setProfileIniEnabled(const QString& gameId, const QString& profileName,
                                      bool enabled, GrpcProfileIniStatus& statusOut, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    gorganizer::v1::SetProfileIniEnabledRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    req.set_enabled(enabled);
    gorganizer::v1::ProfileIniStatus resp;
    auto s = stub->SetProfileIniEnabled(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
    statusOut = iniStatusFromProto(resp);
    return true;
}

bool GrpcClient::getProfileIniStatus(const QString& gameId, const QString& profileName,
                                     GrpcProfileIniStatus& statusOut, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    gorganizer::v1::GetProfileIniStatusRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    gorganizer::v1::ProfileIniStatus resp;
    auto s = stub->GetProfileIniStatus(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
    statusOut = iniStatusFromProto(resp);
    return true;
}

static GrpcIniTweakState iniTweakFromProto(const gorganizer::v1::IniTweakState& t)
{
    GrpcIniTweakState out;
    out.id = QString::fromStdString(t.id());
    out.name = QString::fromStdString(t.name());
    out.description = QString::fromStdString(t.description());
    out.targetFile = QString::fromStdString(t.target_file());
    out.enabled = t.enabled();
    return out;
}

bool GrpcClient::listIniTweaks(const QString& gameId, const QString& profileName,
                               std::vector<GrpcIniTweakState>& tweaksOut, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    gorganizer::v1::ListIniTweaksRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    gorganizer::v1::ListIniTweaksResponse resp;
    auto s = stub->ListIniTweaks(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
    tweaksOut.clear();
    for (const auto& t : resp.tweaks()) tweaksOut.push_back(iniTweakFromProto(t));
    return true;
}

bool GrpcClient::health(GrpcReadiness& out, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    // Tight deadline: the splash polls this on a short cadence and a
    // hung daemon must not stall the GUI thread.
    ctx.set_deadline(std::chrono::system_clock::now() + std::chrono::seconds(2));
    gorganizer::v1::HealthRequest req;
    gorganizer::v1::Readiness resp;
    auto s = stub->Health(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
    out.socketReady = resp.socket_ready();
    out.recoveryDone = resp.recovery_done();
    out.gamesWarmed = resp.games_warmed();
    out.lastInitStep = QString::fromStdString(resp.last_init_step());
    return true;
}

bool GrpcClient::setIniTweak(const QString& gameId, const QString& profileName,
                             const QString& tweakId, bool enabled,
                             GrpcIniTweakState& stateOut, QString& errorOut)
{
    if (!m_channel) { errorOut = "not connected"; return false; }
    auto stub = makeStub(m_channel);
    grpc::ClientContext ctx;
    gorganizer::v1::SetIniTweakRequest req;
    req.set_game_id(gameId.toStdString());
    req.set_profile_name(profileName.toStdString());
    req.set_tweak_id(tweakId.toStdString());
    req.set_enabled(enabled);
    gorganizer::v1::IniTweakState resp;
    auto s = stub->SetIniTweak(&ctx, req, &resp);
    if (!s.ok()) { errorOut = QString::fromStdString(s.error_message()); return false; }
    stateOut = iniTweakFromProto(resp);
    return true;
}

} // namespace gorganizer

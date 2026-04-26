#pragma once

#include <QObject>
#include <memory>
#include <string>
#include <vector>
#include <atomic>

#include "GrpcClient.h"
#include "gorganizer.grpc.pb.h"

namespace grpc {
class ClientContext;
}

namespace gorganizer {

// GrpcWorker runs on a dedicated QThread and performs blocking gRPC calls.
// Results are emitted as signals which cross thread boundaries via Qt's
// queued connection mechanism.
class GrpcWorker : public QObject {
    Q_OBJECT
public:
    explicit GrpcWorker(std::shared_ptr<grpc::Channel> channel);

    void stop();
    // For the two per-game stream workers: lets the client cancel the
    // current RPC so a new game's stream can start cleanly.
    void cancelActiveStream();

public slots:
    // Game RPCs
    void doListGames();
    void doDetectGames();
    void doConfigureGame(const QString& gameId, const QString& name,
                         uint32_t steamAppId, const QString& installPath,
                         const QString& dataSubpath);

    // Mod RPCs (v2)
    void doListMods(const QString& gameId);
    void doGetMod(const QString& gameId, const QString& modName);
    void doRescanMod(const QString& gameId, const QString& modName);

    // Profile RPCs
    void doListProfiles(const QString& gameId);
    void doCreateProfile(const QString& gameId, const QString& name);
    void doDeleteProfile(const QString& gameId, const QString& name);
    void doGetModList(const QString& gameId, const QString& profileName);
    void doSetModList(const QString& gameId, const QString& profileName,
                      const std::vector<GrpcModListEntry>& entries);

    // VFS RPCs
    void doMountVfs(const QString& gameId, const QString& profileName);
    void doUnmountVfs(const QString& gameId);
    void doGetVfsStatus(const QString& gameId);
    void doRebuildVfs(const QString& gameId);
    // Destructive restore confirmed via the recovery-pending modal.
    void doRestoreFromBackup(const QString& gameId);

    // Conflict RPC
    void doGetConflicts(const QString& gameId, const QString& profileName);

    // Launch RPCs
    void doLaunchGame(const QString& gameId, bool useTool, const QString& profileName);

    // Archive lifecycle
    void doStartDownload(const QString& nxmUri);
    void doCancelDownload(const QString& downloadId);
    void doRetryDownload(const QString& downloadId);

    // Install lifecycle
    void doStartInstall(const QString& gameId, const QString& archiveRelPath,
                        const QString& externalArchivePath, int mode,
                        const QString& targetMod, const QString& previewId,
                        const std::vector<GrpcFomodFile>& fomodSelectedFiles);

    // Settings
    void doSetNexusAPIKey(const QString& apiKey);

    // Lifecycle
    void doShutdownDaemon();

    // Streaming
    void doStartWatching();                   // WatchStatus (global)
    void doStreamArchiveEvents(const QString& gameId);
    void doStreamInstallEvents(const QString& gameId);
    void doStreamPluginStatus(const QString& gameId, const QString& profileName);

signals:
    // Game + mod + profile results.
    void gamesListed(const std::vector<GrpcGame>& games);
    void gamesDetected(const std::vector<GrpcGame>& games);
    void gameConfigured();
    void modsListed(const std::vector<GrpcModInfo>& mods);
    void modInfoReceived(const GrpcModInfo& info);
    void profilesListed(const std::vector<GrpcProfile>& profiles);
    void profileCreated(const GrpcProfile& profile);
    void profileDeleted();
    void modListReceived(const std::vector<GrpcModListEntry>& entries);
    void modListUpdated();
    void vfsMounted(const GrpcVFSStatus& status);
    void vfsUnmounted();
    void vfsStatusReceived(const GrpcVFSStatus& status);
    void vfsRebuilt();
    void conflictsReceived(const std::vector<GrpcFileConflict>& conflicts);
    void gameLaunched(int pid);
    void gameLaunchFailed(const QString& error);

    // Download + install async results
    void downloadStarted(const QString& downloadId, int queuedAhead);
    void downloadCancelled(const QString& downloadId);
    void downloadRetried(const QString& downloadId, int queuedAhead);
    void installStarted(const QString& installId);
    void installCompleted(const QString& modFolder, int fileCount);
    void installFailed(const QString& error);

    void nexusAPIKeySet(bool valid, const QString& errorMessage);

    // Stream events
    void vfsStatusChanged(const GrpcVFSStatus& status);
    void archiveEventReceived(const GrpcArchiveEvent& evt);
    void installProgressEvent(const GrpcInstallProgress& progress);
    void daemonError(const QString& error);
    void daemonInfo(const QString& info);
    // Emitted when the daemon detects an ambiguous on-disk state for a
    // game's Data dir at startup (unrecognized Data alongside intact
    // Data.orig). The MainWindow shows a confirmation modal and, on
    // user consent, calls restoreFromBackup.
    void recoveryPending(const QString& gameId, const QString& dataPath,
                         const QString& backupPath, const QString& reason);

    // Plugin dependency status stream
    void pluginStatusSnapshot(const std::vector<GrpcPluginStatus>& plugins);
    void pluginStatusUpdate(const GrpcPluginStatus& plugin);

    // Activity-log dep warning routed through WatchStatus
    void dependencyWarning(const GrpcDependencyWarning& warning);

    void rpcError(const QString& method, const QString& error);

private:
    std::unique_ptr<gorganizer::v1::Gorganizer::Stub> m_stub;
    std::atomic<bool> m_stopped{false};

    // Held when a stream is active so cancelActiveStream() can cancel it.
    // Guarded by m_streamMu.
    std::mutex m_streamMu;
    grpc::ClientContext* m_streamCtx = nullptr;

    // Conversion helpers.
    static GrpcGame gameFromProto(const gorganizer::v1::Game& g);
    static GrpcModInfo modFromProto(const gorganizer::v1::ModInfo& m);
    static GrpcModListEntry modListEntryFromProto(const gorganizer::v1::ModListEntry& e);
    static GrpcProfile profileFromProto(const gorganizer::v1::Profile& p);
    static GrpcVFSStatus vfsStatusFromProto(const gorganizer::v1::VFSStatus& s);
    static GrpcFileConflict conflictFromProto(const gorganizer::v1::FileConflict& c);
    static GrpcDownloadProgress downloadProgressFromProto(const gorganizer::v1::DownloadProgress& d);
    static GrpcInstallProgress installProgressFromProto(const gorganizer::v1::InstallProgress& p);
    static GrpcArchiveRow archiveRowFromProto(const gorganizer::v1::ArchiveRow& r);
};

} // namespace gorganizer

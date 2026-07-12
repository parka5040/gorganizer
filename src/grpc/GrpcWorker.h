#pragma once

#include <QObject>
#include <atomic>
#include <chrono>
#include <memory>
#include <mutex>
#include <vector>

#include "GrpcDeadline.h"
#include "GrpcTypes.h"
#include "gorganizer.grpc.pb.h"

namespace gorganizer {

class GrpcWorker : public QObject {
    Q_OBJECT
public:
    explicit GrpcWorker(std::shared_ptr<grpc::Channel> channel);

    void stop();
    void cancelActiveStream();

public slots:
    void doListGames();
    void doDetectGames();
    void doConfigureGame(const QString& gameId, const QString& name,
                         uint32_t steamAppId, const QString& installPath,
                         const QString& dataSubpath);

    void doListMods(const QString& gameId);
    void doGetMod(const QString& gameId, const QString& modName);
    void doRescanMod(const QString& gameId, const QString& modName);

    void doListProfiles(const QString& gameId);
    void doCreateProfile(const QString& gameId, const QString& name);
    void doDeleteProfile(const QString& gameId, const QString& name);
    void doGetModList(const QString& gameId, const QString& profileName);
    void doSetModList(const QString& gameId, const QString& profileName,
                      const std::vector<GrpcModListEntry>& entries);

    void doMountVfs(const QString& gameId, const QString& profileName);
    void doMountVfsWithSwap(const QString& gameId, const QString& profileName);
    void doUnmountVfs(const QString& gameId);
    void doGetVfsStatus(const QString& gameId);
    void doRebuildVfs(const QString& gameId);
    void doRestoreFromBackup(const QString& gameId);

    void doGetConflicts(const QString& gameId, const QString& profileName);

    void doLaunchGame(const QString& gameId, bool useTool, const QString& profileName);

    void doStartDownload(const QString& nxmUri);
    void doCancelDownload(const QString& downloadId);
    void doRetryDownload(const QString& downloadId);

    void doStartInstall(const QString& gameId, const QString& archiveRelPath,
                        const QString& externalArchivePath, int mode,
                        const QString& targetMod, const QString& previewId,
                        const std::vector<GrpcFomodFile>& fomodSelectedFiles);

    void doSetNexusAPIKey(const QString& apiKey);

    void doShutdownDaemon();

    void doStartWatching();
    void doStreamArchiveEvents(const QString& gameId);
    void doStreamInstallEvents(const QString& gameId);
    void doStreamPluginStatus(const QString& gameId, const QString& profileName);

    void doExportInstance(const QString& gameId, const QString& outputPath,
                          const QStringList& modFolders, const QStringList& profileNames,
                          bool includeOverwrite, bool includeGameSettings);
    void doImportInstance(const QString& gameId, const QString& archivePath,
                          int policy, const QMap<QString, int>& modPolicyOverrides,
                          const QStringList& modFolders, const QStringList& profileNames);

signals:
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

    void downloadStarted(const QString& downloadId, int queuedAhead);
    void downloadCancelled(const QString& downloadId);
    void downloadRetried(const QString& downloadId, int queuedAhead);
    void installStarted(const QString& installId);
    void installCompleted(const QString& modFolder, int fileCount);
    void installFailed(const QString& error);

    void nexusAPIKeySet(bool valid, const QString& errorMessage);

    void vfsStatusChanged(const GrpcVFSStatus& status);
    void archiveEventReceived(const GrpcArchiveEvent& evt);
    void installProgressEvent(const GrpcInstallProgress& progress);
    void daemonError(const QString& error);
    void daemonInfo(const QString& info);
    void recoveryPending(const QString& gameId, const QString& dataPath,
                         const QString& backupPath, const QString& reason);

    void pluginStatusSnapshot(const std::vector<GrpcPluginStatus>& plugins);
    void pluginStatusUpdate(const GrpcPluginStatus& plugin);

    void dependencyWarning(const GrpcDependencyWarning& warning);

    void transferProgress(const GrpcTransferProgress& progress);
    void transferCompleted(const GrpcTransferSummary& summary);
    void transferFailed(const QString& error);

    void rpcError(const QString& method, const QString& error);

private:
    using Stub = gorganizer::v1::Gorganizer::Stub;

    std::unique_ptr<Stub> m_stub;
    std::atomic<bool> m_stopped{false};

    std::mutex m_streamMu;
    grpc::ClientContext* m_streamCtx = nullptr;
    grpc::ClientContext* m_unaryCtx = nullptr;

    template <typename Req, typename Resp, typename Method>
    grpc::Status invoke(Method method, const Req& req, Resp& resp,
                        std::chrono::milliseconds deadline = kDefaultUnaryTimeout);

    template <typename Req, typename Resp, typename Method>
    bool call(const char* rpcName, Method method, const Req& req, Resp& resp,
              std::chrono::milliseconds deadline = kDefaultUnaryTimeout);

    template <typename Req, typename Ev, typename Dispatch>
    grpc::Status runStream(std::unique_ptr<grpc::ClientReader<Ev>> (Stub::*method)(grpc::ClientContext*, const Req&),
                           const Req& req, Dispatch dispatch);

    template <typename Req>
    void runTransferStream(std::unique_ptr<grpc::ClientReader<gorganizer::v1::TransferEvent>> (Stub::*method)(grpc::ClientContext*, const Req&),
                           const Req& req);

    static GrpcGame gameFromProto(const gorganizer::v1::Game& g);
    static GrpcModInfo modFromProto(const gorganizer::v1::ModInfo& m);
    static GrpcModListEntry modListEntryFromProto(const gorganizer::v1::ModListEntry& e);
    static GrpcProfile profileFromProto(const gorganizer::v1::Profile& p);
    static GrpcVFSStatus vfsStatusFromProto(const gorganizer::v1::VFSStatus& s);
    static GrpcFileConflict conflictFromProto(const gorganizer::v1::FileConflict& c);
    static GrpcDownloadProgress downloadProgressFromProto(const gorganizer::v1::DownloadProgress& d);
    static GrpcInstallProgress installProgressFromProto(const gorganizer::v1::InstallProgress& p);
    static GrpcArchiveRow archiveRowFromProto(const gorganizer::v1::ArchiveRow& r);
    static GrpcTransferProgress transferProgressFromProto(const gorganizer::v1::TransferProgress& p);
    static GrpcTransferSummary transferSummaryFromProto(const gorganizer::v1::TransferSummary& s);
};

}

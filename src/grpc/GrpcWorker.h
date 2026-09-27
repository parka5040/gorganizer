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

    enum StreamKind { StreamArchive, StreamInstall, StreamPluginStatus };

    void stop();
    void cancelActiveStream();
    void setStreamGeneration(quint64 generation);

public slots:
    void doListGames();
    void doDetectGames();
    void doConfigureGame(const QString& gameId, const QString& name,
                         uint32_t steamAppId, const QString& installPath,
                         const QString& dataSubpath);
    void doConfigureGameTracked(quint64 requestId, const QString& gameId, const QString& name,
                                uint32_t steamAppId, const QString& installPath,
                                const QString& dataSubpath);

    void doListMods(const QString& gameId);
    void doGetMod(const QString& gameId, const QString& modName);
    void doRescanMod(const QString& gameId, const QString& modName);

    void doListProfiles(const QString& gameId);
    void doCreateProfile(const QString& gameId, const QString& name);
    void doCopyProfile(const QString& gameId, const QString& source, const QString& name);
    void doDeleteProfile(const QString& gameId, const QString& name);
    void doGetModList(const QString& gameId, const QString& profileName);
    void doSetModList(const QString& gameId, const QString& profileName,
                      const std::vector<GrpcModListEntry>& entries);

    void doMountVfs(const QString& gameId, const QString& profileName);
    void doMountVfsWithSwap(const QString& gameId, const QString& profileName);
    void doRetargetVfs(quint64 requestId, const QString& gameId, const QString& profileName);
    void doUnmountVfs(const QString& gameId);
    // Unmounts gameId for a maintenance flow with the long mount deadline and reports the outcome under requestId.
    void doUnmountVfsForMaintenance(quint64 requestId, const QString& gameId);
    void doGetVfsStatus(const QString& gameId);
    // Reads gameId's VFS status and reports it, or the failure, under requestId.
    void doQueryVfsStatus(quint64 requestId, const QString& gameId);
    void doSetSteamMaintenance(quint64 requestId, const QString& gameId, bool enabled, bool verificationConfirmed);
    void doImportPreservedFiles(quint64 requestId, const QString& gameId, const QString& batchId,
                                const QString& modName, const QStringList& relativePaths);
    void doDeletePreservedBatch(quint64 requestId, const QString& gameId, const QString& batchId);
    void doRebuildVfs(const QString& gameId);
    void doRestoreFromBackup(const QString& gameId, GrpcRecoveryKind kind, const QString& recoveryId);
    void doRetryVfsRecovery(const QString& gameId);

    void doGetConflicts(const QString& gameId, const QString& profileName);

    void doLaunchGame(const QString& gameId, bool useTool, const QString& profileName);

    void doStartDownload(const QString& nxmUri);
    void doCancelDownload(const QString& downloadId);
    void doRetryDownload(const QString& downloadId);

    void doPreviewInstall(quint64 requestId, const QString& gameId, const QString& archiveRelPath,
                          const QString& externalArchivePath);
    void doDiscardPreview(const QString& previewId);
    void doStartInstall(quint64 requestId, const QString& gameId, const QString& archiveRelPath,
                        const QString& externalArchivePath, int mode,
                        const QString& targetMod, const QString& previewId,
                        const std::vector<GrpcFomodFile>& fomodSelectedFiles,
                        bool fomodConfirmed, const QString& selectedRoot);

    void doSetNexusAPIKey(const QString& apiKey);
    void doSetNexusAPIKeyTracked(quint64 requestId, const QString& apiKey);

    void doShutdownDaemon();

    void doStartWatching(quint64 generation);
    void doStreamArchiveEvents(const QString& gameId, quint64 generation);
    void doStreamInstallEvents(const QString& gameId, quint64 generation);
    void doStreamPluginStatus(const QString& gameId, const QString& profileName, quint64 generation);

    void doExportInstance(const QString& gameId, const QString& outputPath,
                          const QStringList& modFolders, const QStringList& profileNames,
                          bool includeOverwrite, bool includeGameSettings);
    void doImportInstance(const QString& gameId, const QString& archivePath,
                          int policy, const QMap<QString, int>& modPolicyOverrides,
                          const QStringList& modFolders, const QStringList& profileNames);

    void doGetModLoaderStatus(quint64 requestId, const QString& gameId, bool checkLatest);
    void doInstallModLoader(quint64 requestId, const QString& gameId, bool repairOnly);
    void doUninstallModLoader(quint64 requestId, const QString& gameId);
    void doRollbackModLoader(quint64 requestId, const QString& gameId);

    void doGetModListRequest(quint64 requestId, const QString& gameId, const QString& profileName);
    void doSetModListRequest(quint64 requestId, const QString& gameId, const QString& profileName,
                             const std::vector<GrpcModListEntry>& entries);
    void doReinstallMod(quint64 requestId, const QString& gameId, const QString& modName);
    void doUninstallMod(quint64 requestId, const QString& gameId, const QString& modName, bool force);
    void doRenameMod(quint64 requestId, const QString& gameId, const QString& oldName, const QString& newName);
    void doGetModDependencyReport(quint64 requestId, const QString& gameId, const QString& profileName,
                                  bool refreshRemote, bool forceRemote);
    void doFetchModDependencies(quint64 requestId, const QString& gameId, const QString& profileName,
                                const QStringList& uniqueIds);
    void doAckDependencyEnable(quint64 requestId, const QString& gameId, const QString& batchId,
                               const QStringList& uniqueIds);
    void doSaveProfileIniFile(quint64 requestId, const QString& gameId, const QString& profileName,
                              const QString& filename, const QString& content);
    void doApplyProfileIniFiles(quint64 requestId, const QString& gameId, const QString& profileName);

signals:
    void gamesListed(const std::vector<GrpcGame>& games);
    void gamesDetected(const std::vector<GrpcGame>& games);
    void gameConfigured();
    void gameConfigurationFinished(quint64 requestId, const QString& gameId, bool ok, const QString& error);
    void modsListed(const std::vector<GrpcModInfo>& mods);
    void modInfoReceived(const GrpcModInfo& info);
    void profilesListed(const std::vector<GrpcProfile>& profiles);
    void profileCreated(const GrpcProfile& profile);
    void profileCopied(const QString& gameId, const GrpcProfile& profile);
    void profileDeleted();
    void modListReceived(const std::vector<GrpcModListEntry>& entries);
    void modListUpdated();
    void vfsMounted(const GrpcVFSStatus& status);
    void vfsRetargeted(quint64 requestId, const GrpcVFSStatus& status);
    void vfsRetargetFailed(quint64 requestId, const QString& gameId, const QString& profileName, const QString& error);
    void vfsUnmounted();
    void vfsStatusReceived(const GrpcVFSStatus& status);
    void vfsRecoveryRetried(const QString& gameId);
    void vfsStatusQueried(quint64 requestId, const GrpcVFSStatus& status);
    void vfsStatusQueryFailed(quint64 requestId, const QString& gameId, const QString& error);
    void steamMaintenanceSet(quint64 requestId, const GrpcVFSStatus& status);
    void steamMaintenanceSetFailed(quint64 requestId, const QString& gameId, const QString& error);
    void preservedFilesImported(quint64 requestId, const QString& gameId, const QString& modName, int fileCount);
    void preservedFilesImportFailed(quint64 requestId, const QString& gameId, const QString& error);
    void preservedBatchDeleted(quint64 requestId, const GrpcVFSStatus& status);
    void preservedBatchDeleteFailed(quint64 requestId, const QString& gameId, const QString& error);
    void maintenanceUnmountFinished(quint64 requestId, const QString& gameId, bool ok, int grpcCode, const QString& error);
    void vfsRebuilt();
    void conflictsReceived(const std::vector<GrpcFileConflict>& conflicts);
    void gameLaunched(int pid);
    void gameLaunchFailed(const QString& error);

    void downloadStarted(const QString& downloadId, int queuedAhead);
    void downloadCancelled(const QString& downloadId);
    void downloadRetried(const QString& downloadId, int queuedAhead);
    void previewInstallCompleted(quint64 requestId, const GrpcPreviewInstallResult& result);
    void previewInstallFailed(quint64 requestId, const QString& error);
    void installRequestCompleted(quint64 requestId, const QString& modFolder, int fileCount);
    void installRequestFailed(quint64 requestId, const QString& error);

    void nexusAPIKeySet(bool valid, const QString& errorMessage);
    void nexusKeySaveFinished(quint64 requestId, bool saved, const QString& error);

    void vfsStatusChanged(const GrpcVFSStatus& status);
    void archiveEventReceived(quint64 generation, const GrpcArchiveEvent& evt);
    void installProgressEvent(quint64 generation, const GrpcInstallProgress& progress);
    void streamEventReceived(int kind, quint64 generation);
    void streamEnded(int kind, quint64 generation, int statusCode);
    void daemonError(const QString& error);
    void daemonInfo(const QString& info);
    void recoveryPending(const GrpcRecoveryPending& recovery);

    void pluginStatusSnapshot(quint64 generation, const std::vector<GrpcPluginStatus>& plugins);
    void pluginStatusUpdate(quint64 generation, const GrpcPluginStatus& plugin);

    void dependencyWarning(const GrpcDependencyWarning& warning);

    void transferProgress(const GrpcTransferProgress& progress);
    void transferCompleted(const GrpcTransferSummary& summary);
    void transferFailed(const QString& error);

    void modLoaderStatusReceived(quint64 requestId, const QString& gameId, const GrpcModLoaderStatus& status);
    void modLoaderStatusFailed(quint64 requestId, const QString& gameId, const QString& error);
    void modLoaderOperationFinished(quint64 requestId, const QString& gameId, const QString& operation,
                                    bool ok, int grpcCode, const GrpcModLoaderStatus& status, const QString& error);

    void modListRequestReceived(quint64 requestId, const QString& gameId, const QString& profileName,
                                const std::vector<GrpcModListEntry>& entries);
    void modListRequestFailed(quint64 requestId, const QString& gameId, const QString& profileName,
                              const QString& error);
    void modListSaved(quint64 requestId, const QString& gameId, const QString& profileName);
    void modListSaveFailed(quint64 requestId, const QString& gameId, const QString& profileName,
                           const QString& error);
    void modReinstalled(quint64 requestId, const QString& gameId, const QString& modName,
                        const GrpcReinstallResult& result);
    void modUninstalled(quint64 requestId, const QString& gameId, const QString& modName,
                        const QStringList& flaggedArchives);
    void modRenamed(quint64 requestId, const QString& gameId, const QString& oldName, const QString& newName);
    void modActionFailed(quint64 requestId, const QString& gameId, const QString& modName,
                         const QString& method, const QString& error);
    void modDependencyReportReceived(quint64 requestId, const GrpcModDependencyReport& report);
    void modDependencyReportFailed(quint64 requestId, const QString& gameId, const QString& profileName,
                                   const QString& error);
    void modDependenciesFetched(quint64 requestId, const QString& gameId, const QString& profileName,
                                const std::vector<GrpcDependencyFetchResult>& results);
    void modDependencyFetchFailed(quint64 requestId, const QString& gameId, const QString& profileName,
                                  const QString& error);
    void dependencyEnableAcknowledged(quint64 requestId, const QString& gameId, const QString& batchId,
                                      int acknowledged);
    void dependencyEnableAckFailed(quint64 requestId, const QString& gameId, const QString& batchId,
                                   const QString& error);
    void profileIniSaved(quint64 requestId, const GrpcIniSaveResult& result);
    void profileIniSaveFailed(quint64 requestId, const QString& error);
    void profileIniFilesApplied(quint64 requestId, int appliedFileCount);
    void profileIniFilesApplyFailed(quint64 requestId, const QString& error);
    void installCompletedHintReceived(quint64 generation, const GrpcInstallCompleted& event);

    void rpcError(const QString& method, const QString& error);

private:
    using Stub = gorganizer::v1::Gorganizer::Stub;

    std::shared_ptr<grpc::Channel> m_channel;
    std::unique_ptr<Stub> m_stub;
    std::atomic<bool> m_stopped{false};
    std::atomic<quint64> m_streamGeneration{0};

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
                           const Req& req, Dispatch dispatch, quint64 generation = 0);

    template <typename Method>
    void runModLoaderOperation(quint64 requestId, const QString& gameId, const QString& operation,
                               Method method, const gorganizer::v1::ModLoaderRequest& req,
                               std::chrono::milliseconds deadline);

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

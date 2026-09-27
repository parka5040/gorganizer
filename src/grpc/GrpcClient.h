#pragma once

#include <QObject>
#include <QString>
#include <QThread>
#include <QTimer>
#include <array>
#include <memory>
#include <string>
#include <vector>

#include "GrpcTypes.h"

namespace grpc {
class Channel;
}

namespace gorganizer {

class GrpcWorker;
struct GrpcSyncStub;

class GrpcClient : public QObject {
    Q_OBJECT
public:
    explicit GrpcClient(QObject* parent = nullptr);
    ~GrpcClient() override;

    void connectToDaemon();
    void disconnectFromDaemon();
    bool isConnected() const;

    void listGames();
    void detectGames();
    void configureGame(const QString& gameId, const QString& name,
                       uint32_t steamAppId, const QString& installPath,
                       const QString& dataSubpath);

    void listMods(const QString& gameId);
    // Synchronous ListMods for modal flows (export mod checklist).
    bool listModsSync(const QString& gameId, std::vector<GrpcModInfo>& out, QString& errorOut);
    void getMod(const QString& gameId, const QString& modName);
    void rescanMod(const QString& gameId, const QString& modName);
    bool uninstallMod(const QString& gameId, const QString& modName, bool force,
                      std::vector<QString>& archivesFlaggedOut, QString& errorOut);
    quint64 reinstallModAsync(const QString& gameId, const QString& modName);
    quint64 uninstallModAsync(const QString& gameId, const QString& modName, bool force);
    quint64 renameModAsync(const QString& gameId, const QString& oldName, const QString& newName);
    // Registers a mod folder created outside StartInstall.
    bool registerManualInstall(const QString& gameId, const QString& modName,
                               const QString& archiveRelPath, QString& errorOut);

    bool listOverwriteFiles(const QString& gameId,
                            std::vector<GrpcOverwriteEntry>& filesOut,
                            QString& overwriteDirOut, QString& errorOut);
    // Empty files extracts everything; collisions reported as ALREADY_EXISTS.
    bool extractOverwriteToMod(const QString& gameId, const QString& modName,
                               const QStringList& files, bool keepInOverwrite,
                               int& fileCountOut, QString& errorOut);

    void listProfiles(const QString& gameId);
    // Synchronous ListProfiles for modal flows (export profile checklist).
    bool listProfilesSync(const QString& gameId, std::vector<GrpcProfile>& out, QString& errorOut);
    void createProfile(const QString& gameId, const QString& name);
    void deleteProfile(const QString& gameId, const QString& name);
    void getModList(const QString& gameId, const QString& profileName);
    void setModList(const QString& gameId, const QString& profileName,
                    const std::vector<GrpcModListEntry>& entries);
    bool listSeparators(const QString& gameId, const QString& profileName,
                        std::vector<GrpcSeparator>& out, bool& viewEnabledOut,
                        QString& errorOut);
    bool setSeparators(const QString& gameId, const QString& profileName,
                       const std::vector<GrpcSeparator>& seps, bool viewEnabled,
                       QString& errorOut);

    void mountVfs(const QString& gameId, const QString& profileName);
    // Auto-swap unmounts the conflicting game in the same mutex group (FNV/TTW).
    void mountVfsWithSwap(const QString& gameId, const QString& profileName);
    quint64 retargetVfs(const QString& gameId, const QString& profileName);
    void unmountVfs(const QString& gameId);
    // Queues a maintenance unmount with the long mount deadline on the unary worker and returns the id maintenanceUnmountFinished carries.
    quint64 unmountVfsForMaintenance(const QString& gameId);
    void getVfsStatus(const QString& gameId);
    // Queues a VFS status query on the unary worker and returns the id vfsStatusQueried or vfsStatusQueryFailed carries.
    quint64 queryVfsStatus(const QString& gameId);
    void rebuildVfs(const QString& gameId);
    // Restores the pending recovery only when its kind and identity still match the confirmed item.
    void restoreFromBackup(const QString& gameId, GrpcRecoveryKind kind, const QString& recoveryId);
    void retryVfsRecovery(const QString& gameId);

    void getConflicts(const QString& gameId, const QString& profileName);

    void launchGame(const QString& gameId, bool useTool, const QString& profileName);
    bool installScriptExtender(const QString& gameId, QString& nameOut, QString& errorOut);

    // External executables (MO2-style tools). Synchronous, deadline-bounded.
    bool listExecutables(const QString& gameId, QList<GrpcExecutable>& out, QString& errorOut);
    bool upsertExecutable(const QString& gameId, const GrpcExecutable& exe,
                          GrpcExecutable& savedOut, QString& errorOut);
    bool removeExecutable(const QString& gameId, const QString& id, QString& errorOut);
    bool detectExecutables(const QString& gameId, QList<GrpcDetectedExecutable>& out, QString& errorOut);
    bool launchExecutable(const QString& gameId, const QString& execId, const QString& profileName,
                          int& pidOut, QString& runIdOut, QString& errorOut, bool autoSort = false);
    bool cancelExecutable(const QString& runId, QString& errorOut);
    bool getManagedToolStatus(const QString& toolId, GrpcManagedToolStatus& statusOut, QString& errorOut);
    bool installManagedTool(const QString& toolId, GrpcManagedToolStatus& statusOut, QString& errorOut);
    bool rollbackManagedTool(const QString& toolId, GrpcManagedToolStatus& statusOut, QString& errorOut);

    // FNV 4GB patcher (FNV only): two-step install + apply, plus marker-file probe.
    bool install4GBPatcher(const QString& gameId, QString& patcherExePathOut,
                           QString& versionOut, QString& errorOut);
    bool apply4GBPatch(const QString& gameId, const QString& patcherExePath,
                       QString& outputOut, QString& errorOut);
    bool is4GBPatched(const QString& gameId);
    bool detectProtonVersions(std::vector<GrpcProtonVersion>& out, QString& errorOut);
    bool getPreferredProton(QString& pathOut, QString& errorOut);
    bool setPreferredProton(const QString& path, QString& errorOut);
    // Tells daemon which game the UI is showing for NXM download routing. Fire-and-forget.
    void setActiveGame(const QString& gameId);

    bool checkTTWPrereqs(int backend, GrpcTTWPrereqStatus& out, QString& errorOut);
    bool checkTTWDiskSpace(int64_t& availableOut, int64_t& requiredOut, QString& errorOut);
    bool checkFNVNotMounted(QString& errorOut);
    bool prepareTTWInstaller(const QString& userPath, int backend,
                             GrpcTTWInstallerInfo& out, QString& errorOut);
    bool createBlankTTWMod(const QString& modName, QString& modDirOut, QString& errorOut);
    bool ensureNativeMpiInstaller(QString& pathOut, QString& versionOut, QString& errorOut);
    bool bootstrapFNVPrefix(QString& errorOut);
    bool installTTWPrereqs(QString& installIdOut, QString& errorOut);
    bool launchTTWInstaller(const GrpcTTWInstallerInfo& info, const QString& dataModName,
                            QString& installIdOut, QString& errorOut);
    bool cancelTTWInstaller(const QString& installId, QString& errorOut);
    bool getTTWInstallResult(const QString& installId, bool block,
                             GrpcTTWInstallResult& out, QString& errorOut);
    bool setTTWLauncherExe(const QString& relPath, QString& errorOut);
    bool verifyTTWIntegrity(QString& errorOut);
    bool translateWinePath(const QString& gameId, const QString& unixPath,
                           QString& winePathOut, QString& errorOut);

    bool listArchives(const QString& gameId, std::vector<GrpcArchiveRow>& rowsOut, QString& errorOut);
    bool setArchiveHidden(const QString& gameId, const QString& archiveRelPath, bool hidden, QString& errorOut);
    bool setArchivesHiddenBulk(const QString& gameId, bool hidden, GrpcBulkHideScope scope, int& affectedOut, QString& errorOut);
    bool removeArchive(const QString& gameId, const QString& archiveRelPath, const QString& downloadId, QString& errorOut);
    bool refreshArchiveMetadata(const QString& gameId, const QString& archiveRelPath,
                                GrpcArchiveRow& rowOut, QString& errorOut);
    void startDownload(const QString& nxmUri);
    void cancelDownload(const QString& downloadId);
    void retryDownload(const QString& downloadId);

    bool previewInstall(const QString& gameId, const QString& archiveRelPath,
                        GrpcPreviewInstallResult& out, QString& errorOut,
                        const QString& externalArchivePath = QString());
    quint64 previewInstallAsync(const QString& gameId, const QString& archiveRelPath,
                                const QString& externalArchivePath);
    bool discardPreview(const QString& previewId, QString& errorOut);
    void discardPreviewAsync(const QString& previewId);
    // Queues an install on the install RPC worker and returns the id its installRequest* signals carry.
    quint64 startInstall(const QString& gameId, const QString& archiveRelPath,
                         GrpcInstallMode mode, const QString& targetMod,
                         const QString& previewId,
                         const std::vector<GrpcFomodFile>& fomodSelectedFiles,
                         bool fomodConfirmed = false, const QString& selectedRoot = QString());
    // Queues an install from an archive outside the Downloads index and returns its request id.
    quint64 startInstallExternal(const QString& gameId, const QString& externalArchivePath,
                                 GrpcInstallMode mode, const QString& targetMod,
                                 bool fomodConfirmed = false, const QString& selectedRoot = QString(),
                                 const QString& previewId = QString(),
                                 const std::vector<GrpcFomodFile>& fomodSelectedFiles = {});
    // Synchronous StartInstall for modal flows.
    bool startInstallSync(const QString& gameId, const QString& archiveRelPath,
                          const QString& externalArchivePath,
                          GrpcInstallMode mode, const QString& targetMod,
                          const QString& previewId,
                          const std::vector<GrpcFomodFile>& fomodSelectedFiles,
                          QString& modFolderOut, int& fileCountOut, QString& errorOut);

    // Reads an export archive's manifest and per-item collision flags without writing anything.
    bool previewImport(const QString& gameId, const QString& archivePath,
                       GrpcImportPreview& out, QString& errorOut);
    // Starts a streaming instance export on the transfer worker; progress arrives via transfer* signals.
    void startExport(const QString& gameId, const QString& outputPath,
                     const QStringList& modFolders, const QStringList& profileNames,
                     bool includeOverwrite, bool includeGameSettings);
    // Starts a streaming instance import on the transfer worker; progress arrives via transfer* signals.
    void startImport(const QString& gameId, const QString& archivePath,
                     GrpcTransferPolicy policy, const QMap<QString, int>& modPolicyOverrides,
                     const QStringList& modFolders, const QStringList& profileNames);
    // Cancels the in-flight export/import stream; the transfer then reports transferFailed.
    void cancelTransfer();
    bool transferActive() const { return m_transferActive; }

    // Queues a mod-loader status query and returns the id its modLoaderStatus* signals carry; latest-release checks run on the mod-loader status worker, others on the unary worker.
    quint64 getModLoaderStatus(const QString& gameId, bool checkLatest);
    // Queues a mod-loader status query without a latest-release check on the mod-loader status worker and returns the id its modLoaderStatus* signals carry.
    quint64 pollModLoaderStatus(const QString& gameId);
    // Queues a mod-loader install, or a repair from the retained release, and returns the id modLoaderOperationFinished carries.
    quint64 installModLoader(const QString& gameId, bool repairOnly);
    // Queues a mod-loader uninstall and returns the id modLoaderOperationFinished carries.
    quint64 uninstallModLoader(const QString& gameId);
    // Queues a rollback to the retained previous mod-loader release and returns the id modLoaderOperationFinished carries.
    quint64 rollbackModLoader(const QString& gameId);

    // Queues GetModList on the unary worker and returns the id its modListRequest* signals carry.
    quint64 getModListTracked(const QString& gameId, const QString& profileName);
    // Queues SetModList on the unary worker behind earlier mod-list saves and returns the id its modListSave* signals carry, reporting failures only through modListSaveFailed.
    quint64 setModListTracked(const QString& gameId, const QString& profileName,
                              const std::vector<GrpcModListEntry>& entries);
    // Queues a SMAPI dependency report, on the dependency worker when smapi.io is consulted, and returns the id its modDependencyReport* signals carry.
    quint64 getModDependencyReport(const QString& gameId, const QString& profileName,
                                   bool refreshRemote, bool forceRemote);
    // Queues a dependency fetch on the dependency worker and returns the id modDependenciesFetched or modDependencyFetchFailed carries.
    quint64 fetchModDependencies(const QString& gameId, const QString& profileName, const QStringList& uniqueIds);
    // Queues a pending-enable acknowledgement on the unary worker and returns the id its dependencyEnableAck* signals carry.
    quint64 ackDependencyEnable(const QString& gameId, const QString& batchId, const QStringList& uniqueIds);

    bool getGameSettings(const QString& gameId, GrpcGameSettings& settingsOut, QString& errorOut);
    bool setGameSettings(const QString& gameId, bool autoInstall, GrpcGameSettings& settingsOut, QString& errorOut);

    bool listProfileIniFiles(const QString& gameId, const QString& profileName,
                             std::vector<GrpcProfileIniFile>& filesOut,
                             GrpcProfileIniStatus& statusOut, QString& errorOut);
    bool saveProfileIniFile(const QString& gameId, const QString& profileName,
                            const QString& filename, const QString& content, QString& errorOut);
    bool setProfileIniEnabled(const QString& gameId, const QString& profileName,
                              bool enabled, GrpcProfileIniStatus& statusOut, QString& errorOut);
    bool getProfileIniStatus(const QString& gameId, const QString& profileName,
                             GrpcProfileIniStatus& statusOut, QString& errorOut);
    bool listIniTweaks(const QString& gameId, const QString& profileName,
                       std::vector<GrpcIniTweakState>& tweaksOut, QString& errorOut);
    bool setIniTweak(const QString& gameId, const QString& profileName,
                     const QString& tweakId, bool enabled,
                     GrpcIniTweakState& stateOut, QString& errorOut);

    void startWatching();
    void stopWatching();
    void subscribeEvents(const QString& gameId);
    void unsubscribeEvents();

    void subscribePluginStatus(const QString& gameId, const QString& profileName);
    void unsubscribePluginStatus();

    // Persist a user-set plugin load order; synchronous, false on RPC failure with errorOut set.
    bool setPluginOrder(const QString& gameId, const QString& profileName,
                        const QStringList& filenames, QString& errorOut);
    // Persist the complete ordered activation state for the profile.
    bool setPluginLoadout(const QString& gameId, const QString& profileName,
                          const std::vector<GrpcPluginLoadoutEntry>& plugins,
                          QString& errorOut);

    void setNexusAPIKey(const QString& apiKey);

    void shutdownDaemon();
    // Synchronous shutdown for app exit; polls socket file for graceful daemon exit.
    bool shutdownDaemonSync(int rpcTimeoutMs, int pollTimeoutMs, QString& errorOut);

    // Cold-start readiness probe used by the splash screen.
    bool health(GrpcReadiness& out, QString& errorOut);

signals:
    void connected();
    void disconnected();
    void resubscribed();
    // Reports that disconnectFromDaemon stopped every worker, so no queued request will be answered any more.
    void workersStopped();
    void connectionError(const QString& error);

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
    void vfsRetargeted(quint64 requestId, const GrpcVFSStatus& status);
    void vfsRetargetFailed(quint64 requestId, const QString& gameId, const QString& profileName, const QString& error);
    void vfsUnmounted();
    void vfsStatusReceived(const GrpcVFSStatus& status);
    void vfsRecoveryRetried(const QString& gameId);
    void vfsStatusQueried(quint64 requestId, const GrpcVFSStatus& status);
    void vfsStatusQueryFailed(quint64 requestId, const QString& gameId, const QString& error);
    void maintenanceUnmountFinished(quint64 requestId, const QString& gameId, bool ok, int grpcCode, const QString& error);
    void vfsRebuilt();

    void conflictsReceived(const std::vector<GrpcFileConflict>& conflicts);

    void gameLaunched(int pid);
    void gameLaunchFailed(const QString& error);

    void previewInstallCompleted(quint64 requestId, const GrpcPreviewInstallResult& result);
    void previewInstallFailed(quint64 requestId, const QString& error);
    void installRequestCompleted(quint64 requestId, const QString& modFolder, int fileCount);
    void installRequestFailed(quint64 requestId, const QString& error);

    void downloadStarted(const QString& downloadId, int queuedAhead);
    void downloadCancelled(const QString& downloadId);
    void downloadRetried(const QString& downloadId, int queuedAhead);

    void archiveEventReceived(const GrpcArchiveEvent& evt);
    void installProgressEvent(const GrpcInstallProgress& progress);

    void pluginStatusSnapshot(const std::vector<GrpcPluginStatus>& plugins);
    void pluginStatusUpdate(const GrpcPluginStatus& plugin);

    void vfsStatusChanged(const GrpcVFSStatus& status);
    void daemonError(const QString& error);
    void daemonInfo(const QString& info);
    void dependencyWarning(const GrpcDependencyWarning& warning);
    void recoveryPending(const GrpcRecoveryPending& recovery);

    void nexusAPIKeySet(bool valid, const QString& errorMessage);

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
    // Reports that a mod finished installing for the subscribed game, as a refresh hint.
    void installCompletedHintReceived(const GrpcInstallCompleted& event);

    void rpcError(const QString& method, const QString& error);

private slots:
    void onCheckConnection();

private:
    struct WorkerHandle {
        QThread* thread = nullptr;
        GrpcWorker* worker = nullptr;
        const char* tag = "";
    };

    enum WorkerRole {
        RoleUnary = 0,
        RoleWatch,
        RoleArchive,
        RoleInstall,
        RolePluginStatus,
        RoleTransfer,
        RoleInstallRpc,
        RoleDependencyRpc,
        RoleModLoaderRpc,
        RoleModLoaderStatus,
        RoleCount,
    };

    std::shared_ptr<grpc::Channel> m_channel;
    std::unique_ptr<GrpcSyncStub> m_syncStub;
    std::array<WorkerHandle, RoleCount> m_workers{{
        {nullptr, nullptr, "unary"},
        {nullptr, nullptr, "watch-status"},
        {nullptr, nullptr, "archive-stream"},
        {nullptr, nullptr, "install-stream"},
        {nullptr, nullptr, "plugin-status-stream"},
        {nullptr, nullptr, "transfer-stream"},
        {nullptr, nullptr, "install-rpc"},
        {nullptr, nullptr, "dependency-rpc"},
        {nullptr, nullptr, "modloader-rpc"},
        {nullptr, nullptr, "modloader-status"},
    }};
    QTimer* m_connectionTimer = nullptr;
    bool m_connected = false;
    bool m_transferActive = false;
    quint64 m_connectionGeneration = 0;
    quint64 m_nextPreviewRequestId = 0;
    quint64 m_nextInstallRequestId = 0;
    quint64 m_nextModActionRequestId = 0;
    quint64 m_nextModLoaderRequestId = 0;
    quint64 m_nextModListRequestId = 0;
    quint64 m_nextDependencyRequestId = 0;
    quint64 m_nextVfsRequestId = 0;
    QString m_subscribedGame;
    QString m_pluginGame;
    QString m_pluginProfile;
    struct StreamState {
        quint64 generation = 0;
        bool active = false;
        int retryMs = 1000;
        bool receivedEvent = false;
    };
    std::array<StreamState, 3> m_streamStates{};
    quint64 m_watchGeneration = 0;
    bool m_watchStarted = false;

    GrpcWorker* unaryWorker() const { return m_workers[RoleUnary].worker; }
    GrpcWorker* watchWorker() const { return m_workers[RoleWatch].worker; }
    GrpcWorker* archiveWorker() const { return m_workers[RoleArchive].worker; }
    GrpcWorker* installWorker() const { return m_workers[RoleInstall].worker; }
    GrpcWorker* pluginStatusWorker() const { return m_workers[RolePluginStatus].worker; }
    GrpcWorker* transferWorker() const { return m_workers[RoleTransfer].worker; }
    GrpcWorker* installRpcWorker() const { return m_workers[RoleInstallRpc].worker; }
    GrpcWorker* dependencyRpcWorker() const { return m_workers[RoleDependencyRpc].worker; }
    GrpcWorker* modLoaderRpcWorker() const { return m_workers[RoleModLoaderRpc].worker; }
    GrpcWorker* modLoaderStatusWorker() const { return m_workers[RoleModLoaderStatus].worker; }

    std::string socketTarget() const;
    void connectWorkerSignals(GrpcWorker* worker);
    void startStream(int kind);
    void cancelStream(int kind);
    void resumeSubscriptions();
    quint64 postInstall(const QString& gameId, const QString& archiveRelPath,
                        const QString& externalArchivePath, GrpcInstallMode mode,
                        const QString& targetMod, const QString& previewId,
                        const std::vector<GrpcFomodFile>& fomodSelectedFiles,
                        bool fomodConfirmed, const QString& selectedRoot);

    template <typename Method, typename... Args>
    quint64 postModLoaderOperation(const QString& gameId, const QString& operation, Method method, Args... args);
    // Assigns a request id and queues a mod-loader status query on worker, failing asynchronously when there is none.
    quint64 postModLoaderStatus(GrpcWorker* worker, const QString& gameId, bool checkLatest);

    template <typename Method, typename... Args>
    void postTo(GrpcWorker* worker, Method method, Args... args);

    template <typename Method, typename... Args>
    void post(Method method, Args... args);
};

}

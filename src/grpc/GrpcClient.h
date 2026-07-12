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
    bool renameMod(const QString& gameId, const QString& oldName,
                   const QString& newName, QString& errorOut);
    bool uninstallMod(const QString& gameId, const QString& modName, bool force,
                      std::vector<QString>& archivesFlaggedOut, QString& errorOut);
    bool reinstallMod(const QString& gameId, const QString& modName,
                      GrpcReinstallResult& resultOut, QString& errorOut);
    // Register a mod folder produced outside StartInstall (FOMOD wizard's local-extract path).
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
    void unmountVfs(const QString& gameId);
    void getVfsStatus(const QString& gameId);
    void rebuildVfs(const QString& gameId);
    // Destructive recovery; only call after user confirms via recovery-pending modal.
    void restoreFromBackup(const QString& gameId);

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
    bool removeArchive(const QString& gameId, const QString& archiveRelPath, QString& errorOut);
    bool refreshArchiveMetadata(const QString& gameId, const QString& archiveRelPath,
                                GrpcArchiveRow& rowOut, QString& errorOut);
    void startDownload(const QString& nxmUri);
    void cancelDownload(const QString& downloadId);
    void retryDownload(const QString& downloadId);

    bool previewInstall(const QString& gameId, const QString& archiveRelPath,
                        GrpcPreviewInstallResult& out, QString& errorOut);
    bool discardPreview(const QString& previewId, QString& errorOut);
    void startInstall(const QString& gameId, const QString& archiveRelPath,
                      GrpcInstallMode mode, const QString& targetMod,
                      const QString& previewId,
                      const std::vector<GrpcFomodFile>& fomodSelectedFiles);
    // Drag-drop entry: installs from an archive not in the Downloads index.
    void startInstallExternal(const QString& gameId, const QString& externalArchivePath,
                              GrpcInstallMode mode, const QString& targetMod);
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
    void vfsUnmounted();
    void vfsStatusReceived(const GrpcVFSStatus& status);
    void vfsRebuilt();

    void conflictsReceived(const std::vector<GrpcFileConflict>& conflicts);

    void gameLaunched(int pid);
    void gameLaunchFailed(const QString& error);

    void installStarted(const QString& installId);
    void installCompleted(const QString& modFolder, int fileCount);
    void installFailed(const QString& error);

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
    // Daemon found ambiguous Data state at startup; UI shows a modal and may call restoreFromBackup.
    void recoveryPending(const QString& gameId, const QString& dataPath,
                         const QString& backupPath, const QString& reason);

    void nexusAPIKeySet(bool valid, const QString& errorMessage);

    void transferProgress(const GrpcTransferProgress& progress);
    void transferCompleted(const GrpcTransferSummary& summary);
    void transferFailed(const QString& error);

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
    }};
    QTimer* m_connectionTimer = nullptr;
    bool m_connected = false;
    bool m_transferActive = false;
    QString m_subscribedGame;

    GrpcWorker* unaryWorker() const { return m_workers[RoleUnary].worker; }
    GrpcWorker* watchWorker() const { return m_workers[RoleWatch].worker; }
    GrpcWorker* archiveWorker() const { return m_workers[RoleArchive].worker; }
    GrpcWorker* installWorker() const { return m_workers[RoleInstall].worker; }
    GrpcWorker* pluginStatusWorker() const { return m_workers[RolePluginStatus].worker; }
    GrpcWorker* transferWorker() const { return m_workers[RoleTransfer].worker; }

    std::string socketTarget() const;
    void connectWorkerSignals(GrpcWorker* worker);

    template <typename Method, typename... Args>
    void postTo(GrpcWorker* worker, Method method, Args... args);

    template <typename Method, typename... Args>
    void post(Method method, Args... args);
};

}

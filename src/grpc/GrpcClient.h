#pragma once

#include <QObject>
#include <QString>
#include <QThread>
#include <QTimer>
#include <memory>
#include <string>
#include <vector>

namespace grpc {
class Channel;
}

namespace gorganizer {

class GrpcWorker;

struct GrpcGame {
    QString gameId;
    QString name;
    uint32_t steamAppId = 0;
    QString installPath;
    QString dataPath;
    bool synthetic = false;
    QString linkedFromGameId;
    bool vfsActive = false;
};

enum GrpcTTWBackend {
    GrpcTTWBackendNative = 0,
    GrpcTTWBackendWine = 1,
};

struct GrpcTTWPrereqStatus {
    int backend = 0;

    bool gstreamerInstalled = false;
    QString gstreamerCodecsHint;
    bool xdeltaInstalled = false;
    int64_t diskSpaceAvailable = 0;
    int64_t diskSpaceRequired = 0;
    bool fnvVanilla = false;

    QString mpiInstallerPath;
    QString mpiInstallerVersion;

    bool prefixExists = false;
    bool hasDotnet48 = false;
    uint32_t dotnet48ReleaseRev = 0;
    bool hasMsxml6 = false;
    bool hasVcrun2022 = false;
    bool hasCorefonts = false;
    bool monoNeedsRemoval = false;
    bool steamRunning = false;
    bool protontricksAvailable = false;
    bool winetricksAvailable = false;

    QStringList missing;
};

struct GrpcTTWInstallerInfo {
    int backend = 0;
    QString mpiFile;
    QString installerExe;
    QString version;
    QStringList alternateMpis;
};

struct GrpcTTWExeDelta {
    QString relPath;
    QString kind;
    int64_t size = 0;
    QString mtime;
    QString sha256;
};

struct GrpcTTWInstallResult {
    int installerExitCode = 0;
    bool layoutFixed = false;
    int dataModFileCount = 0;
    int64_t dataModBytes = 0;
    std::vector<GrpcTTWExeDelta> changedExesInRoot;
    std::vector<GrpcTTWExeDelta> dataModExes;
};

struct GrpcModInfo {
    QString name;
    QString gameId;
    QString basePath;
    QString dataPath;
    int fileCount = 0;
    int64_t totalSize = 0;
};

struct GrpcModListEntry {
    QString modName;
    bool enabled = false;
    int priority = 0;
};

struct GrpcSeparator {
    QString name;
    QString visualIndex;
    bool collapsed = false;
};

struct GrpcProfile {
    QString name;
    QString gameId;
    QString createdAt;
};

struct GrpcVFSStatus {
    bool mounted = false;
    QString gameId;
    QString profileName;
    QString mountPoint;
    int enabledModCount = 0;
    int totalFileCount = 0;
    bool dirty = false; // pending mod edits not yet applied to the on-disk farm
};

struct GrpcFileConflict {
    QString virtualPath;
    QString winningMod;
    QStringList losingMods;
};

struct GrpcOverwriteEntry {
    QString relPath;
    int64_t sizeBytes = 0;
    QString modifiedAt;
    bool isDir = false;
};

struct GrpcDownloadProgress {
    QString downloadId;
    QString modName;
    int64_t bytesDownloaded = 0;
    int64_t bytesTotal = 0;
    int status = 0;
    QString error;
    int32_t queuedAhead = 0;
};

enum GrpcInstallStep {
    GrpcInstallStepIdle = 0,
    GrpcInstallStepExtracting = 1,
    GrpcInstallStepCopying = 2,
    GrpcInstallStepFinalizing = 3,
    GrpcInstallStepComplete = 4,
    GrpcInstallStepFailed = 5,
};

struct GrpcInstallProgress {
    QString installId;
    QString archiveRelPath;
    QString modName;
    int step = 0;
    int pct = -1;
    QString currentFile;
    int64_t filesDone = 0;
    int64_t filesTotal = 0;
    QString error;
};

enum GrpcDepKind {
    GrpcDepOK = 0,
    GrpcDepMasterAbsent = 1,
    GrpcDepMasterDisabled = 2,
    GrpcDepMasterOutOfOrder = 3,
    GrpcDepSoftMissing = 4,
};

struct GrpcDepIssue {
    int kind = 0;
    QString master;
    QString softModName;
    int softModId = 0;
    QString softModUrl;
};

struct GrpcPluginStatus {
    QString filename;
    QString ext;
    bool isLight = false;
    bool enabled = false;
    QString fromMod;
    bool softPending = false;
    std::vector<GrpcDepIssue> issues;
};

struct GrpcDependencyWarning {
    QString pluginFilename;
    QString detail;
    int kind = 0;
};

struct GrpcArchiveRow {
    QString archiveRelPath;
    int modId = 0;
    int fileId = 0;
    QString modName;
    QString fileName;
    QString fileArchiveName;
    QString version;
    QString category;
    int64_t sizeBytes = 0;
    QString uploadedAt;
    QString downloadedAt;
    bool hidden = false;
    QString gameDomain;
    QString thumbnailUrl;
    bool adultContent = false;
    int status = 0;
    QString installedModFolder;
    QString downloadId;
    int64_t bytesDownloaded = 0;
    int32_t queuedAhead = 0;
    bool merged = false;
};

struct GrpcArchiveEvent {
    enum Kind { KindUnknown, KindDownloadProgress, KindRowChanged, KindArchiveRemoved };
    Kind kind = KindUnknown;
    GrpcDownloadProgress progress;
    GrpcArchiveRow row;
    QString archiveRemoved;
};

using GrpcDownloadRow = GrpcArchiveRow;

struct GrpcGameSettings {
    QString gameId;
    bool autoInstall = false;
};

struct GrpcExecutable {
    QString id;
    QString title;
    QString exePath;
    QStringList args;
    QString workingDir;
    bool needsVfsMounted = true;
    QString captureOutputToMod;
    bool sanitizeEnv = true;
    QStringList extraRwPaths;
    bool autoDetected = false;
};

struct GrpcDetectedExecutable {
    QString title;
    QString exePath;
    bool needsVfsMounted = true;
    QString captureOutputToMod;
};

struct GrpcReinstallResult {
    int archivesReplayed = 0;
    int archivesSkipped = 0;
    int fileCount = 0;
};

struct GrpcProfileIniFile {
    QString filename;
    QString content;
    QString diskPath;
};

struct GrpcProfileIniStatus {
    QString gameId;
    QString profileName;
    bool useCustomIni = false;
    QString myGamesDir;
    bool gameSupportsIni = false;
};

struct GrpcIniTweakState {
    QString id;
    QString name;
    QString description;
    QString targetFile;
    bool enabled = false;
};

enum GrpcInstallMode { GrpcInstallAsNewMod = 0, GrpcInstallMergeIntoMod = 1 };

enum GrpcBulkHideScope { GrpcBulkHideAll = 0, GrpcBulkHideInstalled = 1, GrpcBulkHideUninstalled = 2 };

struct GrpcProtonVersion {
    QString name;
    QString path;
};

// Daemon cold-start state used by the splash screen.
struct GrpcReadiness {
    bool socketReady = false;
    bool recoveryDone = false;
    bool gamesWarmed = false;
    QString lastInitStep;
};

struct GrpcFomodFile {
    QString source;
    QString destination;
    bool isFolder = false;
    int32_t priority = 0;
};

struct GrpcFomodPlugin {
    QString name;
    QString description;
    QString imagePath;
    std::vector<GrpcFomodFile> files;
    int32_t defaultState = 0;
};

struct GrpcFomodGroup {
    QString name;
    int32_t type = 0;
    std::vector<GrpcFomodPlugin> plugins;
};

struct GrpcFomodStep {
    QString name;
    std::vector<GrpcFomodGroup> groups;
};

struct GrpcFomodPlan {
    QString moduleName;
    QString modulePath;
    std::vector<GrpcFomodFile> requiredFiles;
    std::vector<GrpcFomodStep> steps;
};

struct GrpcPreviewInstallResult {
    QString previewId;
    bool hasFomod = false;
    GrpcFomodPlan plan;
    QStringList flatFileList;
};

// Manages a gRPC connection to the gorganizerd daemon.
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
                          int& pidOut, QString& runIdOut, QString& errorOut);
    bool cancelExecutable(const QString& runId, QString& errorOut);

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

    // Persist a user-set plugin load order. Synchronous; returns false on
    // RPC failure with a message in errorOut.
    bool setPluginOrder(const QString& gameId, const QString& profileName,
                        const QStringList& filenames, QString& errorOut);

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

    void rpcError(const QString& method, const QString& error);

private slots:
    void onCheckConnection();

private:
    std::shared_ptr<grpc::Channel> m_channel;
    QThread* m_workerThread = nullptr;
    GrpcWorker* m_worker = nullptr;
    QThread* m_streamThread = nullptr;
    GrpcWorker* m_streamWorker = nullptr;
    QThread* m_archiveThread = nullptr;
    GrpcWorker* m_archiveWorker = nullptr;
    QThread* m_installThread = nullptr;
    GrpcWorker* m_installWorker = nullptr;
    QThread* m_pluginStatusThread = nullptr;
    GrpcWorker* m_pluginStatusWorker = nullptr;
    QTimer* m_connectionTimer = nullptr;
    bool m_connected = false;
    QString m_subscribedGame;

    std::string socketTarget() const;
    void connectWorkerSignals(GrpcWorker* worker);
};

} // namespace gorganizer

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

// Qt-friendly result types (decoupled from protobuf).

struct GrpcGame {
    QString gameId;
    QString name;
    uint32_t steamAppId = 0;
    QString installPath;
    QString dataPath;
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
    QString visualIndex; // 16-char hex, see internal/separators.FormatIndex
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
    // Mirrors proto DownloadStatus: 0=unknown, 1=queued, 2=downloading,
    // 3=downloaded, 4=installing, 5=installed, 6=uninstalled, 7=cancelled,
    // 8=failed.
    int status = 0;
    QString error;
    int32_t queuedAhead = 0;
};

// InstallStep mirrors the unified proto InstallProgress.Step. The legacy
// FomodPending step is gone — FOMOD detection is an explicit RPC now
// (PreviewInstall), not a mid-install state.
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

// Plugin dependency analysis (StreamPluginStatus). Mirrors proto DepKind /
// DepIssue / PluginStatusItem.
enum GrpcDepKind {
    GrpcDepOK = 0,
    GrpcDepMasterAbsent = 1,       // red
    GrpcDepMasterDisabled = 2,     // orange
    GrpcDepMasterOutOfOrder = 3,   // red
    GrpcDepSoftMissing = 4,        // yellow
};

struct GrpcDepIssue {
    int kind = 0;                  // GrpcDepKind
    QString master;                // hard kinds
    QString softModName;           // soft kind
    int softModId = 0;
    QString softModUrl;
};

struct GrpcPluginStatus {
    QString filename;
    QString ext;                   // ".esp" / ".esm" / ".esl"
    bool isLight = false;          // ESL flag from header — overrides ext
    bool enabled = false;
    QString fromMod;
    bool softPending = false;
    std::vector<GrpcDepIssue> issues;
};

// One activity-log warning routed via WatchStatus.
struct GrpcDependencyWarning {
    QString pluginFilename;
    QString detail;
    int kind = 0;                  // GrpcDepKind
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
    int status = 0;                 // DownloadStatus, see above
    QString installedModFolder;
    QString downloadId;             // set while a download is still racing this row
    int64_t bytesDownloaded = 0;
    int32_t queuedAhead = 0;
    bool merged = false;            // archive was merged into a pre-existing mod
};

// ArchiveEvent is one message on StreamArchiveEvents. Exactly one of the
// three discriminant fields is populated; the others are ignored.
struct GrpcArchiveEvent {
    enum Kind { KindUnknown, KindDownloadProgress, KindRowChanged, KindArchiveRemoved };
    Kind kind = KindUnknown;
    GrpcDownloadProgress progress;
    GrpcArchiveRow row;
    QString archiveRemoved; // archive_rel_path
};

// Legacy alias so existing UI code keeps compiling after the v2 rename.
// New code should use GrpcArchiveRow directly.
using GrpcDownloadRow = GrpcArchiveRow;

struct GrpcGameSettings {
    QString gameId;
    bool autoInstall = false;
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

// InstallMode mirrors proto enum.
enum GrpcInstallMode { GrpcInstallAsNewMod = 0, GrpcInstallMergeIntoMod = 1 };

// Bulk-hide scopes mirror proto enum.
enum GrpcBulkHideScope { GrpcBulkHideAll = 0, GrpcBulkHideInstalled = 1, GrpcBulkHideUninstalled = 2 };

struct GrpcProtonVersion {
    QString name;
    QString path;
};

// GrpcReadiness mirrors proto Readiness — daemon cold-start state used by
// the splash screen.
struct GrpcReadiness {
    bool socketReady = false;
    bool recoveryDone = false;
    bool gamesWarmed = false;
    QString lastInitStep;
};

// FOMOD structures mirror proto for the PreviewInstall response.
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

// GrpcClient manages a gRPC connection to the gorganizerd daemon.
class GrpcClient : public QObject {
    Q_OBJECT
public:
    explicit GrpcClient(QObject* parent = nullptr);
    ~GrpcClient() override;

    // Connection management
    void connectToDaemon();
    void disconnectFromDaemon();
    bool isConnected() const;

    // Game management
    void listGames();
    void detectGames();
    void configureGame(const QString& gameId, const QString& name,
                       uint32_t steamAppId, const QString& installPath,
                       const QString& dataSubpath);

    // Mod management (v2)
    void listMods(const QString& gameId);
    void getMod(const QString& gameId, const QString& modName);
    void rescanMod(const QString& gameId, const QString& modName);
    bool renameMod(const QString& gameId, const QString& oldName,
                   const QString& newName, QString& errorOut);
    bool uninstallMod(const QString& gameId, const QString& modName, bool force,
                      std::vector<QString>& archivesFlaggedOut, QString& errorOut);
    bool reinstallMod(const QString& gameId, const QString& modName,
                      GrpcReinstallResult& resultOut, QString& errorOut);
    // Notify the daemon about a mod folder produced outside StartInstall
    // (the C++ FOMOD wizard's local-extract path). Daemon adds the mod to
    // every profile's modlist.txt (disabled by default) and invalidates
    // the installed-archive cache so the Downloads tab reflects the install.
    bool registerManualInstall(const QString& gameId, const QString& modName,
                               const QString& archiveRelPath, QString& errorOut);

    // Overwrite layer access. Files are returned with forward-slashed
    // paths relative to overwriteDirOut; the UI uses overwriteDirOut to
    // implement "Open Folder".
    bool listOverwriteFiles(const QString& gameId,
                            std::vector<GrpcOverwriteEntry>& filesOut,
                            QString& overwriteDirOut, QString& errorOut);
    // files empty → extract everything. Files are forward-slashed paths
    // returned by listOverwriteFiles. The new mod folder name MUST NOT
    // already exist; collision is reported as ALREADY_EXISTS.
    bool extractOverwriteToMod(const QString& gameId, const QString& modName,
                               const QStringList& files, bool keepInOverwrite,
                               int& fileCountOut, QString& errorOut);

    // Profile management
    void listProfiles(const QString& gameId);
    void createProfile(const QString& gameId, const QString& name);
    void deleteProfile(const QString& gameId, const QString& name);
    void getModList(const QString& gameId, const QString& profileName);
    void setModList(const QString& gameId, const QString& profileName,
                    const std::vector<GrpcModListEntry>& entries);
    bool listSeparators(const QString& gameId, const QString& profileName,
                        std::vector<GrpcSeparator>& out, QString& errorOut);
    bool setSeparators(const QString& gameId, const QString& profileName,
                       const std::vector<GrpcSeparator>& seps, QString& errorOut);

    // VFS control
    void mountVfs(const QString& gameId, const QString& profileName);
    void unmountVfs(const QString& gameId);
    void getVfsStatus(const QString& gameId);
    void rebuildVfs(const QString& gameId);
    // Destructive recovery: rm -rf Data, mv Data.orig → Data. Only call
    // after the user has confirmed via the recovery-pending modal.
    void restoreFromBackup(const QString& gameId);

    // Conflict analysis
    void getConflicts(const QString& gameId, const QString& profileName);

    // Game launch
    void launchGame(const QString& gameId, bool useTool, const QString& profileName);
    bool installScriptExtender(const QString& gameId, QString& nameOut, QString& errorOut);
    bool detectProtonVersions(std::vector<GrpcProtonVersion>& out, QString& errorOut);
    bool getPreferredProton(QString& pathOut, QString& errorOut);
    bool setPreferredProton(const QString& path, QString& errorOut);

    // Archives (v2 Downloads tab surface). Synchronous for list/set; async
    // for the long-running operations.
    bool listArchives(const QString& gameId, std::vector<GrpcArchiveRow>& rowsOut, QString& errorOut);
    bool setArchiveHidden(const QString& gameId, const QString& archiveRelPath, bool hidden, QString& errorOut);
    bool setArchivesHiddenBulk(const QString& gameId, bool hidden, GrpcBulkHideScope scope, int& affectedOut, QString& errorOut);
    bool removeArchive(const QString& gameId, const QString& archiveRelPath, QString& errorOut);
    bool refreshArchiveMetadata(const QString& gameId, const QString& archiveRelPath,
                                GrpcArchiveRow& rowOut, QString& errorOut);
    // Async download lifecycle.
    void startDownload(const QString& nxmUri);
    void cancelDownload(const QString& downloadId);
    void retryDownload(const QString& downloadId);

    // Install lifecycle. PreviewInstall + DiscardPreview are synchronous
    // because they feed a modal dialog; StartInstall is async because it
    // can take seconds for large archives.
    bool previewInstall(const QString& gameId, const QString& archiveRelPath,
                        GrpcPreviewInstallResult& out, QString& errorOut);
    bool discardPreview(const QString& previewId, QString& errorOut);
    void startInstall(const QString& gameId, const QString& archiveRelPath,
                      GrpcInstallMode mode, const QString& targetMod,
                      const QString& previewId,
                      const std::vector<GrpcFomodFile>& fomodSelectedFiles);
    // Drag-drop entry point: installs from an archive that isn't already in
    // the Downloads index.
    void startInstallExternal(const QString& gameId, const QString& externalArchivePath,
                              GrpcInstallMode mode, const QString& targetMod);
    // Synchronous StartInstall for modal flows (the Downloads tab context
    // menu, ModInstallDialog). Blocks on the unix socket — fine since the
    // caller has a dialog up anyway.
    bool startInstallSync(const QString& gameId, const QString& archiveRelPath,
                          const QString& externalArchivePath,
                          GrpcInstallMode mode, const QString& targetMod,
                          const QString& previewId,
                          const std::vector<GrpcFomodFile>& fomodSelectedFiles,
                          QString& modFolderOut, int& fileCountOut, QString& errorOut);

    // Per-game settings (auto-install toggle).
    bool getGameSettings(const QString& gameId, GrpcGameSettings& settingsOut, QString& errorOut);
    bool setGameSettings(const QString& gameId, bool autoInstall, GrpcGameSettings& settingsOut, QString& errorOut);

    // Per-profile INI management.
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

    // Status streaming. WatchStatus is global (VFS + Info/Error); the two
    // per-game streams start when subscribeEvents(gameId) is called and
    // restart on game switch.
    void startWatching();
    void stopWatching();
    void subscribeEvents(const QString& gameId);
    void unsubscribeEvents();

    // Plugin dependency status stream. The first event is a full snapshot;
    // subsequent events are deltas as soft-dep checks complete on the
    // daemon's background workers. Subscribing while a previous stream is
    // active cancels the prior one (game/profile switch).
    void subscribePluginStatus(const QString& gameId, const QString& profileName);
    void unsubscribePluginStatus();

    // Settings
    void setNexusAPIKey(const QString& apiKey);

    // Daemon lifecycle
    void shutdownDaemon();
    // Synchronous variant used at app exit. Sends Shutdown with a
    // bounded deadline and then polls the socket file (the daemon
    // removes it on graceful stop) so the GUI doesn't return control
    // to the shell wrapper while the daemon is still alive. Returns
    // true if the daemon shut down within totalTimeoutMs.
    bool shutdownDaemonSync(int rpcTimeoutMs, int pollTimeoutMs, QString& errorOut);

    // Cold-start readiness probe. Synchronous because the splash polls it
    // every ~150 ms with a short deadline; goes through its own stub so it
    // doesn't queue behind anything on the unary worker.
    bool health(GrpcReadiness& out, QString& errorOut);

signals:
    // Connection
    void connected();
    void disconnected();
    void connectionError(const QString& error);

    // Game results
    void gamesListed(const std::vector<GrpcGame>& games);
    void gamesDetected(const std::vector<GrpcGame>& games);
    void gameConfigured();

    // Mod results
    void modsListed(const std::vector<GrpcModInfo>& mods);
    void modInfoReceived(const GrpcModInfo& info);

    // Profile results
    void profilesListed(const std::vector<GrpcProfile>& profiles);
    void profileCreated(const GrpcProfile& profile);
    void profileDeleted();

    // Mod list results
    void modListReceived(const std::vector<GrpcModListEntry>& entries);
    void modListUpdated();

    // VFS results
    void vfsMounted(const GrpcVFSStatus& status);
    void vfsUnmounted();
    void vfsStatusReceived(const GrpcVFSStatus& status);
    void vfsRebuilt();

    // Conflict results
    void conflictsReceived(const std::vector<GrpcFileConflict>& conflicts);

    // Game launch results
    void gameLaunched(int pid);
    void gameLaunchFailed(const QString& error);

    // Install results
    void installStarted(const QString& installId);
    void installCompleted(const QString& modFolder, int fileCount);
    void installFailed(const QString& error);

    // Download results
    void downloadStarted(const QString& downloadId, int queuedAhead);
    void downloadCancelled(const QString& downloadId);
    void downloadRetried(const QString& downloadId, int queuedAhead);

    // Per-game archive + install stream events
    void archiveEventReceived(const GrpcArchiveEvent& evt);
    void installProgressEvent(const GrpcInstallProgress& progress);

    // Plugin dependency status stream
    void pluginStatusSnapshot(const std::vector<GrpcPluginStatus>& plugins);
    void pluginStatusUpdate(const GrpcPluginStatus& plugin);

    // Global status events
    void vfsStatusChanged(const GrpcVFSStatus& status);
    void daemonError(const QString& error);
    void daemonInfo(const QString& info);
    // Activity-log entry produced by the daemon's plugin dep analyzer.
    void dependencyWarning(const GrpcDependencyWarning& warning);
    // Daemon detected an ambiguous on-disk Data state at startup. The
    // MainWindow shows a confirmation modal; user consent fires
    // restoreFromBackup.
    void recoveryPending(const QString& gameId, const QString& dataPath,
                         const QString& backupPath, const QString& reason);

    // Settings results
    void nexusAPIKeySet(bool valid, const QString& errorMessage);

    // Generic error for any RPC failure
    void rpcError(const QString& method, const QString& error);

private slots:
    void onCheckConnection();

private:
    std::shared_ptr<grpc::Channel> m_channel;
    QThread* m_workerThread = nullptr;
    GrpcWorker* m_worker = nullptr;
    QThread* m_streamThread = nullptr;   // WatchStatus (global VFS + Info/Error)
    GrpcWorker* m_streamWorker = nullptr;
    QThread* m_archiveThread = nullptr;  // StreamArchiveEvents
    GrpcWorker* m_archiveWorker = nullptr;
    QThread* m_installThread = nullptr;  // StreamInstallEvents
    GrpcWorker* m_installWorker = nullptr;
    QThread* m_pluginStatusThread = nullptr;  // StreamPluginStatus
    GrpcWorker* m_pluginStatusWorker = nullptr;
    QTimer* m_connectionTimer = nullptr;
    bool m_connected = false;
    QString m_subscribedGame;

    std::string socketTarget() const;
    void connectWorkerSignals(GrpcWorker* worker);
};

} // namespace gorganizer

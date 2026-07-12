#pragma once

#include <QMap>
#include <QString>
#include <QStringList>
#include <cstdint>
#include <vector>

namespace gorganizer {

struct GameInfo;

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
    bool dirty = false;
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

struct GrpcPluginLoadoutEntry {
    QString filename;
    bool enabled = true;
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
    QString toolId;
    QString runner;
    QStringList args;
    QMap<QString, QString> environment;
    QString workingDir;
    int prefixAppId = 0;
    QString outputPolicy;
    QString selectedInput;
    bool needsVfsMounted = true;
    QString captureOutputToMod;
    bool sanitizeEnv = true;
    QStringList extraRwPaths;
    bool autoDetected = false;
};

struct GrpcDetectedExecutable {
    QString toolId;
    QString title;
    QString exePath;
    QString runner;
    int prefixAppId = 0;
    QString outputPolicy;
    bool needsVfsMounted = true;
    QString captureOutputToMod;
    bool extraRwScratch = false;
    QStringList defaultArgs;
};

struct GrpcManagedToolStatus {
    QString toolId;
    bool installed = false;
    QString activeVersion;
    QString previousVersion;
    QString executablePath;
    QString updateAvailable;
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

enum GrpcTransferPolicy {
    GrpcTransferPolicyAbort = 0,
    GrpcTransferPolicySkip = 1,
    GrpcTransferPolicyRename = 2,
    GrpcTransferPolicyOverwrite = 3,
};

struct GrpcTransferModEntry {
    QString folder;
    QString name;
    int fileCount = 0;
    int64_t totalBytes = 0;
    int nexusModId = 0;
    int nexusFileId = 0;
    bool collision = false;
};

struct GrpcTransferProfileEntry {
    QString name;
    bool collision = false;
};

struct GrpcImportPreview {
    int schemaVersion = 0;
    QString gorganizerVersion;
    QString gameId;
    QString exportedAt;
    std::vector<GrpcTransferModEntry> mods;
    std::vector<GrpcTransferProfileEntry> profiles;
    bool includesOverwrite = false;
    bool includesGameSettings = false;
};

struct GrpcTransferProgress {
    QString step;
    QString currentItem;
    int itemsDone = 0;
    int itemsTotal = 0;
    int64_t bytesDone = 0;
    int64_t bytesTotal = 0;
};

struct GrpcTransferSummary {
    int modsExported = 0;
    int modsImported = 0;
    int profilesTransferred = 0;
    QStringList skipped;
    QMap<QString, QString> renamed;
    QString outputPath;
};

// Single conversion point from the gRPC game DTO to the core game model; always marks detected.
GameInfo toGameInfo(const GrpcGame& game);

// Inverse of toGameInfo for the same field mapping.
GrpcGame toGrpcGame(const GameInfo& info);

}

package dto

type GameInfo struct {
	GameID           string
	Name             string
	SteamAppID       uint32
	InstallPath      string
	DataPath         string
	Synthetic        bool
	LinkedFromGameID string
	VFSActive        bool
}

type ModInfoResult struct {
	Name      string
	GameID    string
	BasePath  string
	FileCount int
	TotalSize int64
	Files     []string
}

type ProfileResult struct {
	Name      string
	GameID    string
	CreatedAt string
}

type ModListEntryResult struct {
	ModName  string
	Enabled  bool
	Priority int
}

type SeparatorResult struct {
	Name        string
	VisualIndex string
	Collapsed   bool
}

type VFSStatusResult struct {
	Mounted         bool
	GameID          string
	ProfileName     string
	MountPoint      string
	EnabledModCount int
	TotalFileCount  int
	Dirty           bool
	DesiredGen      uint64
	AppliedGen      uint64
}

type FileConflictResult struct {
	VirtualPath string
	WinningMod  string
	LosingMods  []string
}

type ExecutableSpec struct {
	ID                 string
	Title              string
	ExePath            string
	ToolID             string
	Runner             string
	Args               []string
	Environment        map[string]string
	WorkingDir         string
	PrefixAppID        int
	OutputPolicy       string
	SelectedInput      string
	NeedsVFSMounted    bool
	CaptureOutputToMod string
	SanitizeEnv        bool
	ExtraRWPaths       []string
	AutoDetected       bool
}

type DetectedExecutable struct {
	ToolID             string
	Title              string
	ExePath            string
	Runner             string
	PrefixAppID        int
	OutputPolicy       string
	NeedsVFSMounted    bool
	CaptureOutputToMod string
	ExtraRWScratch     bool
	DefaultArgs        []string
}

type ManagedToolStatusResult struct {
	ID              string
	Installed       bool
	ActiveVersion   string
	PreviousVersion string
	ExecutablePath  string
	UpdateAvailable string
}

type OverwriteEntryResult struct {
	RelPath    string
	SizeBytes  int64
	ModifiedAt string
	IsDir      bool
}

type DownloadStatus int

const (
	DownloadStatusUnknown     DownloadStatus = 0
	DownloadStatusQueued      DownloadStatus = 1
	DownloadStatusDownloading DownloadStatus = 2
	DownloadStatusDownloaded  DownloadStatus = 3
	DownloadStatusInstalling  DownloadStatus = 4
	DownloadStatusInstalled   DownloadStatus = 5
	DownloadStatusUninstalled DownloadStatus = 6
	DownloadStatusCancelled   DownloadStatus = 7
	DownloadStatusFailed      DownloadStatus = 8
)

type DownloadProgressResult struct {
	DownloadID      string
	ModName         string
	BytesDownloaded int64
	BytesTotal      int64
	Status          DownloadStatus
	Error           string
	QueuedAhead     int32
	GameID          string
}

type ArchiveRowResult struct {
	ArchiveRelPath     string
	ModID              int
	FileID             int
	ModName            string
	FileName           string
	FileArchiveName    string
	Version            string
	Category           string
	SizeBytes          int64
	UploadedAt         string
	DownloadedAt       string
	Hidden             bool
	GameDomain         string
	ThumbnailURL       string
	AdultContent       bool
	Status             DownloadStatus
	InstalledModFolder string
	DownloadID         string
	BytesDownloaded    int64
	QueuedAhead        int32
	Merged             bool
}

type ArchiveEventResult struct {
	GameID         string
	Progress       *DownloadProgressResult
	RowChanged     *ArchiveRowResult
	ArchiveRemoved string
}

type BulkHideScope int

const (
	BulkHideAll         BulkHideScope = 0
	BulkHideInstalled   BulkHideScope = 1
	BulkHideUninstalled BulkHideScope = 2
)

type InstallMode int

const (
	InstallAsNewMod     InstallMode = 0
	InstallMergeIntoMod InstallMode = 1
)

type InstallStep int

const (
	InstallStepIdle       InstallStep = 0
	InstallStepExtracting InstallStep = 1
	InstallStepCopying    InstallStep = 2
	InstallStepFinalizing InstallStep = 3
	InstallStepComplete   InstallStep = 4
	InstallStepFailed     InstallStep = 5
)

type InstallProgressResult struct {
	InstallID      string
	ArchiveRelPath string
	ModName        string
	Step           InstallStep
	Pct            int32
	CurrentFile    string
	FilesDone      int64
	FilesTotal     int64
	Error          string
	GameID         string
}

type InstallEventResult struct {
	GameID   string
	Progress *InstallProgressResult
}

type FomodFileResult struct {
	Source      string
	Destination string
	IsFolder    bool
	Priority    int32
}

type FomodPluginResult struct {
	Name         string
	Description  string
	ImagePath    string
	Files        []FomodFileResult
	DefaultState int32
}

type FomodGroupResult struct {
	Name    string
	Type    int32
	Plugins []FomodPluginResult
}

type FomodStepResult struct {
	Name   string
	Groups []FomodGroupResult
}

type FomodPlanResult struct {
	ModuleName    string
	ModulePath    string
	RequiredFiles []FomodFileResult
	Steps         []FomodStepResult

	LegacyInfoOnly bool
	Description    string
	ScreenshotPath string
	Version        string
	Author         string
}

type PreviewResult struct {
	PreviewID    string
	HasFomod     bool
	Plan         *FomodPlanResult
	FlatFileList []string
}

type StartInstallRequest struct {
	GameID              string
	ArchiveRelPath      string
	ExternalArchivePath string
	Mode                InstallMode
	TargetMod           string
	PreviewID           string
	FomodSelectedFiles  []FomodFileResult
}

type GameSettingsResult struct {
	GameID      string
	AutoInstall bool
}

type ProtonVersionResult struct {
	Name string
	Path string
}

type NexusAPIKeyResult struct {
	Valid        bool
	ErrorMessage string
}

type StatusEventResult struct {
	VFSStatus         *VFSStatusResult
	Error             string
	Info              string
	RecoveryPending   *RecoveryPendingResult
	DependencyWarning *DependencyWarningResult
}

type DependencyWarningResult struct {
	PluginFilename string
	Detail         string
	Kind           DepKindResult
}

type DepKindResult int

const (
	DepKindOK               DepKindResult = 0
	DepKindMasterAbsent     DepKindResult = 1
	DepKindMasterDisabled   DepKindResult = 2
	DepKindMasterOutOfOrder DepKindResult = 3
	DepKindSoftMissing      DepKindResult = 4
)

type DepIssueResult struct {
	Kind        DepKindResult
	Master      string
	SoftModName string
	SoftModID   int32
	SoftModURL  string
}

type PluginStatusItemResult struct {
	Filename    string
	Ext         string
	IsLight     bool
	Enabled     bool
	FromMod     string
	SoftPending bool
	Issues      []DepIssueResult
}

type PluginStatusEventResult struct {
	Snapshot []PluginStatusItemResult
	Update   *PluginStatusItemResult
}

type PluginLoadoutEntryResult struct {
	Filename string
	Enabled  bool
}

type RecoveryPendingResult struct {
	GameID     string
	DataPath   string
	BackupPath string
	Reason     string
}

type ReadinessResult struct {
	SocketReady  bool
	RecoveryDone bool
	GamesWarmed  bool
	LastInitStep string
}

type ProfileIniFileResult struct {
	Filename string
	Content  string
	DiskPath string
}

type ProfileIniListResult struct {
	Files        []ProfileIniFileResult
	MyGamesDir   string
	UseCustomIni bool
}

type ProfileIniStatusResult struct {
	GameID          string
	ProfileName     string
	UseCustomIni    bool
	MyGamesDir      string
	GameSupportsIni bool
}

type IniTweakStateResult struct {
	ID          string
	Name        string
	Description string
	TargetFile  string
	Enabled     bool
}

type FNV4GBInstallResult struct {
	PatcherExePath string
	Version        string
}

type TTWPrereqResult struct {
	Backend int

	GstreamerInstalled  bool
	GstreamerCodecsHint string
	XdeltaInstalled     bool
	DiskSpaceAvailable  int64
	DiskSpaceRequired   int64
	FNVVanilla          bool

	MpiInstallerPath    string
	MpiInstallerVersion string

	PrefixExists          bool
	HasDotnet48           bool
	DotNet48ReleaseRev    uint32
	HasMsxml6             bool
	HasVcrun2022          bool
	HasCorefonts          bool
	MonoNeedsRemoval      bool
	SteamRunning          bool
	ProtontricksAvailable bool
	WinetricksAvailable   bool

	Missing []string
}

type TTWInstallerInfoResult struct {
	Backend       int
	MpiFile       string
	InstallerExe  string
	Version       string
	AlternateMpis []string
}

type TTWInstallResultData struct {
	InstallerExitCode int
	LayoutFixed       bool
	DataModFileCount  int
	DataModBytes      int64
	ChangedExesInRoot []TTWExeDeltaResult
	DataModExes       []TTWExeDeltaResult
}

type TTWExeDeltaResult struct {
	RelPath string
	Kind    string
	Size    int64
	MTime   string
	SHA256  string
}

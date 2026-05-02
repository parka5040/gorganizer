package ipc

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"

	pb "github.com/parka/gorganizer/api/proto"
	"google.golang.org/grpc"
)

// LoaderMissingError is returned by LaunchGame when a useTool=true launch
type LoaderMissingError struct {
	GameID        string
	ConfiguredExe string
	InstallPath   string
	Reason        string
}

func (e *LoaderMissingError) Error() string {
	if e.ConfiguredExe == "" {
		return fmt.Sprintf("script extender loader not found for %s in %s (%s)",
			e.GameID, e.InstallPath, e.Reason)
	}
	return fmt.Sprintf("script extender loader %q not found in %s (%s)",
		e.ConfiguredExe, e.InstallPath, e.Reason)
}

// DaemonController is the interface the IPC layer uses to call into the daemon.
// Dependency inversion: ipc depends on this interface, not the concrete daemon type.
type DaemonController interface {
	GameController
	ModController
	ProfileController
	VFSController
	ConflictController
	PluginStatusController
	ArchiveController
	InstallController
	LaunchController
	FNV4GBController
	SettingsController
	IniController
	LifecycleController
	TTWController
}

// TTWController exposes the synthetic-game install + lifecycle for Tale
// of Two Wastelands. The two backends share most of the surface; only
type TTWController interface {
	CheckTTWPrereqs(backend int) (TTWPrereqResult, error)
	CheckTTWDiskSpace() (available, required int64, err error)
	CheckFNVNotMounted() error
	PrepareTTWInstaller(userPath string, backend int) (TTWInstallerInfoResult, error)
	CreateBlankTTWMod(modName string) (string, error)
	EnsureNativeMpiInstaller() (path, version string, err error)
	BootstrapFNVPrefix() error
	InstallTTWPrereqs() (string, error)
	LaunchTTWInstaller(info TTWInstallerInfoResult, dataModName string) (string, error)
	CancelTTWInstaller(installID string) error
	GetTTWInstallResult(installID string, block bool) (TTWInstallResultData, error)
	SetTTWLauncherExe(relPath string) error
	VerifyTTWIntegrity() error
	TranslateWinePath(gameID, unixPath string) (string, error)
	MountVFSWithSwap(gameID, profileName string) (*VFSStatusResult, error)
}

// TTWPrereqResult mirrors the TTW pre-flight status. Backend-tagged via
// the int field; values match daemon.TTWBackend.
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

// TTWInstallerInfoResult is the resolved-input record returned by
// PrepareTTWInstaller.
type TTWInstallerInfoResult struct {
	Backend       int
	MpiFile       string
	InstallerExe  string
	Version       string
	AlternateMpis []string
}

// TTWInstallResultData mirrors daemon.TTWInstallResult through the IPC
// boundary without requiring ipc to import daemon.
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

// GameController handles game-related operations.
type GameController interface {
	ListConfiguredGames() ([]GameInfo, error)
	DetectInstalledGames() ([]GameInfo, error)
	ConfigureGame(gameID, name string, steamAppID uint32, installPath, dataSubpath string) error
}

// ModController — installed-mod lifecycle.
type ModController interface {
	ListMods(gameID string) ([]ModInfoResult, error)
	GetMod(gameID, modName string) (*ModInfoResult, error)
	RescanMod(gameID, modName string) (*ModInfoResult, error)
	RenameMod(gameID, oldName, newName string) error
	UninstallMod(gameID, modName string, force bool) ([]string, error)
	ReinstallMod(gameID, modName string) (replayed, skipped, fileCount int, err error)
	RegisterManualInstall(gameID, modName, archiveRelPath string) (profilesUpdated int, err error)
	ListOverwriteFiles(gameID string) (entries []OverwriteEntryResult, dir string, err error)
	ExtractOverwriteToMod(gameID, modName string, files []string, keep bool) (fileCount int, err error)
}

// ProfileController handles profile-related operations.
type ProfileController interface {
	ListProfiles(gameID string) ([]ProfileResult, error)
	CreateProfile(gameID, name string) (*ProfileResult, error)
	DeleteProfile(gameID, name string) error
	GetModList(gameID, profileName string) ([]ModListEntryResult, error)
	SetModList(gameID, profileName string, entries []ModListEntryResult) error
	ListSeparators(gameID, profileName string) ([]SeparatorResult, bool, error)
	SetSeparators(gameID, profileName string, seps []SeparatorResult, viewEnabled bool) error
}

// SeparatorResult is one MO2-style visual separator row in the mod list.
type SeparatorResult struct {
	Name        string
	VisualIndex string
	Collapsed   bool
}

// VFSController handles VFS mount/unmount operations.
type VFSController interface {
	MountVFS(gameID, profileName string) (*VFSStatusResult, error)
	UnmountVFS(gameID string) error
	GetVFSStatus(gameID string) (*VFSStatusResult, error)
	RebuildVFS(gameID string) error
	RestoreFromBackup(gameID string) error
}

// ConflictController handles conflict analysis.
type ConflictController interface {
	GetConflicts(gameID, profileName string) ([]FileConflictResult, error)
}

// PluginStatusController streams plugin dependency-analysis results.
type PluginStatusController interface {
	StreamPluginStatus(ctx context.Context, gameID, profileName string) (<-chan PluginStatusEventResult, error)
}

// ArchiveController — the Downloads tab lifecycle. Every archive-scope verb
// lives here; the old DownloadController is gone.
type ArchiveController interface {
	StartDownload(nxmURI string) (id string, queuedAhead int, err error)
	CancelDownload(id string) error
	RetryDownload(id string) (queuedAhead int, err error)
	ListArchives(gameID string) ([]ArchiveRowResult, error)
	RemoveArchive(gameID, archiveRelPath string) error
	SetArchiveHidden(gameID, archiveRelPath string, hidden bool) error
	SetArchivesHiddenBulk(gameID string, hidden bool, scope BulkHideScope) (int, error)
	RefreshArchiveMetadata(gameID, archiveRelPath string) (*ArchiveRowResult, error)
	StreamArchiveEvents(ctx context.Context, gameID string) (<-chan ArchiveEventResult, error)
}

// InstallController — archive → mod installation.
type InstallController interface {
	PreviewInstall(gameID, archiveRelPath string) (*PreviewResult, error)
	StartInstall(req StartInstallRequest) (modFolder string, fileCount int, err error)
	DiscardPreview(previewID string) error
	StreamInstallEvents(ctx context.Context, gameID string) (<-chan InstallEventResult, error)
}

// ProfileIniFileResult is one per-profile INI file.
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

// IniTweakStateResult is one INI preset plus whether it's currently applied.
type IniTweakStateResult struct {
	ID          string
	Name        string
	Description string
	TargetFile  string
	Enabled     bool
}

// LaunchController handles game launching.
type LaunchController interface {
	LaunchGame(gameID string, useTool bool, profileName string) (int, error)
	DetectProton() ([]ProtonVersionResult, error)
	InstallScriptExtender(gameID string) (string, error)
	GetPreferredProton() (string, error)
	SetPreferredProton(path string) error
}

// FNV4GBController handles the Fallout: New Vegas 4GB patcher flow. Split
type FNV4GBController interface {
	Install4GBPatcher(gameID string) (FNV4GBInstallResult, error)
	Apply4GBPatch(gameID, patcherExePath string) (output string, err error)
	Get4GBPatchStatus(gameID string) (bool, error)
}

// FNV4GBInstallResult mirrors proto Install4GBPatcherResponse.
type FNV4GBInstallResult struct {
	PatcherExePath string
	Version        string
}

// SettingsController handles daemon settings + per-game settings.
type SettingsController interface {
	SetNexusAPIKey(ctx context.Context, apiKey string) (*NexusAPIKeyResult, error)
	GetGameSettings(gameID string) (*GameSettingsResult, error)
	SetGameSettings(gameID string, autoInstall bool) (*GameSettingsResult, error)
	SetActiveGame(gameID string) error
}

// IniController handles per-profile INI file management for Bethesda games.
type IniController interface {
	ListProfileIniFiles(gameID, profileName string) (*ProfileIniListResult, error)
	SaveProfileIniFile(gameID, profileName, filename, content string) error
	SetProfileIniEnabled(gameID, profileName string, enabled bool) (*ProfileIniStatusResult, error)
	GetProfileIniStatus(gameID, profileName string) (*ProfileIniStatusResult, error)
	ListIniTweaks(gameID, profileName string) ([]IniTweakStateResult, error)
	SetIniTweak(gameID, profileName, tweakID string, enabled bool) (*IniTweakStateResult, error)
}

// LifecycleController handles daemon lifecycle.
type LifecycleController interface {
	Shutdown()
	WatchStatus() <-chan StatusEventResult
	Health() ReadinessResult
}

// ReadinessResult mirrors proto Readiness — cold-start state visible to
// the splash screen.
type ReadinessResult struct {
	SocketReady  bool
	RecoveryDone bool
	GamesWarmed  bool
	LastInitStep string
}

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

type VFSStatusResult struct {
	Mounted         bool
	GameID          string
	ProfileName     string
	MountPoint      string
	EnabledModCount int
	TotalFileCount  int
}

type FileConflictResult struct {
	VirtualPath string
	WinningMod  string
	LosingMods  []string
}

// OverwriteEntryResult mirrors proto OverwriteEntry. One row per file (and
// per intermediate directory) under the always-on Overwrite layer.
type OverwriteEntryResult struct {
	RelPath    string
	SizeBytes  int64
	ModifiedAt string
	IsDir      bool
}

// DownloadStatus mirrors proto DownloadStatus. Aligned numerically with the
// proto so a direct cast works.
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

// DownloadProgressResult is the streaming payload for
// StreamArchiveEvents.download_progress.
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

// ArchiveRowResult is the snapshot-row payload (ListArchives +
// ArchiveEvent.row_changed).
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

// ArchiveEventResult is one event on the per-game archive stream.
type ArchiveEventResult struct {
	GameID         string
	Progress       *DownloadProgressResult
	RowChanged     *ArchiveRowResult
	ArchiveRemoved string
}

// BulkHideScope mirrors proto SetArchivesHiddenBulkRequest.Scope.
type BulkHideScope int

const (
	BulkHideAll         BulkHideScope = 0
	BulkHideInstalled   BulkHideScope = 1
	BulkHideUninstalled BulkHideScope = 2
)

// InstallMode mirrors proto StartInstallRequest.InstallMode.
type InstallMode int

const (
	InstallAsNewMod     InstallMode = 0
	InstallMergeIntoMod InstallMode = 1
)

// InstallStep mirrors proto InstallProgress.Step.
type InstallStep int

const (
	InstallStepIdle       InstallStep = 0
	InstallStepExtracting InstallStep = 1
	InstallStepCopying    InstallStep = 2
	InstallStepFinalizing InstallStep = 3
	InstallStepComplete   InstallStep = 4
	InstallStepFailed     InstallStep = 5
)

// InstallProgressResult is the streaming payload for
// StreamInstallEvents.install_progress.
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

// InstallEventResult wraps per-game install-stream events.
type InstallEventResult struct {
	GameID   string
	Progress *InstallProgressResult
}

// FomodFileResult mirrors the FomodFile proto — used for both requests
// (user-selected files in StartInstall) and responses (PreviewInstall plan).
type FomodFileResult struct {
	Source      string
	Destination string
	IsFolder    bool
	Priority    int32
}

// FomodPluginResult — one selectable plugin inside a step.
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

// FomodPlanResult mirrors proto FomodPlan.
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

// PreviewResult is returned by PreviewInstall — carries a cache handle
// plus either a FOMOD plan or a flat file list.
type PreviewResult struct {
	PreviewID    string
	HasFomod     bool
	Plan         *FomodPlanResult
	FlatFileList []string
}

// StartInstallRequest packages the StartInstall RPC payload so the daemon
// interface doesn't sprawl into 7 positional params.
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

// DependencyWarningResult is the daemon-side struct that gets serialized
type DependencyWarningResult struct {
	PluginFilename string
	Detail         string
	Kind           DepKindResult
}

// DepKindResult mirrors proto DepKind — values aligned numerically with
// internal/plugins.DepKind so a direct cast works.
type DepKindResult int

const (
	DepKindOK               DepKindResult = 0
	DepKindMasterAbsent     DepKindResult = 1
	DepKindMasterDisabled   DepKindResult = 2
	DepKindMasterOutOfOrder DepKindResult = 3
	DepKindSoftMissing      DepKindResult = 4
)

// DepIssueResult mirrors proto DepIssue.
type DepIssueResult struct {
	Kind        DepKindResult
	Master      string
	SoftModName string
	SoftModID   int32
	SoftModURL  string
}

// PluginStatusItemResult mirrors proto PluginStatusItem.
type PluginStatusItemResult struct {
	Filename    string
	Ext         string
	IsLight     bool
	Enabled     bool
	FromMod     string
	SoftPending bool
	Issues      []DepIssueResult
}

// PluginStatusEventResult is one event on the StreamPluginStatus stream.
// Exactly one of Snapshot / Update is set.
type PluginStatusEventResult struct {
	Snapshot []PluginStatusItemResult
	Update   *PluginStatusItemResult
}

// RecoveryPendingResult is the daemon-side struct that gets serialized
// to the proto RecoveryPending event. Mirrors vfs.RecoveryPending plus
type RecoveryPendingResult struct {
	GameID     string
	DataPath   string
	BackupPath string
	Reason     string
}

// Server wraps a gRPC server listening on a Unix domain socket.
type Server struct {
	socketPath string
	grpcServer *grpc.Server
	ctrl       DaemonController
}

// NewServer creates a new IPC server.
func NewServer(socketPath string, ctrl DaemonController) *Server {
	return &Server{
		socketPath: socketPath,
		ctrl:       ctrl,
	}
}

// Start creates the socket directory, listens, and serves gRPC.
func (s *Server) Start() error {
	dir := filepath.Dir(s.socketPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating socket directory %s: %w", dir, err)
	}

	os.Remove(s.socketPath)

	lis, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", s.socketPath, err)
	}

	s.grpcServer = grpc.NewServer()
	pb.RegisterGorganizerServer(s.grpcServer, &gorganizerServer{ctrl: s.ctrl})

	slog.Info("gRPC server listening", "socket", s.socketPath)

	go func() {
		if err := s.grpcServer.Serve(lis); err != nil {
			slog.Error("gRPC server error", "err", err)
		}
	}()
	return nil
}

func (s *Server) Stop() {
	if s.grpcServer != nil {
		slog.Info("stopping gRPC server")
		s.grpcServer.GracefulStop()
	}
	if s.socketPath != "" {
		_ = os.Remove(s.socketPath)
	}
}

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
// cannot find a valid script-extender loader exe in the game dir. Lives in
// ipc (not tools) so the IPC layer can grpc-status-map it without creating
// an import cycle — tools already depends on ipc.
//
// This error is load-bearing: without it, the launcher either runs a
// non-existent path or, worse, executes the vanilla Bethesda launcher a
// Steam update restored over a renamed loader. The frontend should show
// an actionable "reinstall xNVSE" dialog, not a generic error toast.
type LoaderMissingError struct {
	GameID        string
	ConfiguredExe string
	InstallPath   string
	Reason        string // "missing" | "modified" | "looks-like-vanilla-launcher" | "no-loader-configured"
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
	ArchiveController
	InstallController
	LaunchController
	SettingsController
	IniController
	LifecycleController
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
	ListSeparators(gameID, profileName string) ([]SeparatorResult, error)
	SetSeparators(gameID, profileName string, seps []SeparatorResult) error
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
	// RestoreFromBackup performs the destructive Data → Data.orig restore
	// the user explicitly confirmed via the recovery-pending modal.
	// Returns an error if no recovery is pending for that game (so a
	// stale frontend can't accidentally trigger destruction).
	RestoreFromBackup(gameID string) error
}

// ConflictController handles conflict analysis.
type ConflictController interface {
	GetConflicts(gameID, profileName string) ([]FileConflictResult, error)
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

// SettingsController handles daemon settings + per-game settings.
type SettingsController interface {
	SetNexusAPIKey(ctx context.Context, apiKey string) (*NexusAPIKeyResult, error)
	GetGameSettings(gameID string) (*GameSettingsResult, error)
	SetGameSettings(gameID string, autoInstall bool) (*GameSettingsResult, error)
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
	// Health returns the current cold-start readiness snapshot. The splash
	// screen polls this until GamesWarmed is true.
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

// Result types used by DaemonController to decouple from protobuf types.

type GameInfo struct {
	GameID      string
	Name        string
	SteamAppID  uint32
	InstallPath string
	DataPath    string
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
	ModifiedAt string // RFC3339
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
	// GameID is used internally to route the event to the right per-game
	// stream; not present on the wire.
	GameID string
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
	// Merged is true when this archive was installed via "Merge Into Existing
	// Mod..." into a pre-existing target. Drives the "Merged" Status display
	// and "Show Containing Mod" right-click action in the Downloads view.
	Merged bool
}

// ArchiveEventResult is one event on the per-game archive stream.
type ArchiveEventResult struct {
	GameID string
	// Exactly one of the following is set.
	Progress        *DownloadProgressResult
	RowChanged      *ArchiveRowResult
	ArchiveRemoved  string // archive_rel_path of a removed archive
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
	InstallAsNewMod    InstallMode = 0
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
	GameID         string // routing only; not on the wire
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

	// Legacy NMM-style FOMOD (info.xml only, no ModuleConfig.xml).
	// Frontend renders an info-only popup and the install path falls back
	// to a flat copy. The C# script.cs that legacy FOMODs sometimes carry
	// is intentionally NOT executed.
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
	VFSStatus       *VFSStatusResult
	Error           string
	Info            string
	RecoveryPending *RecoveryPendingResult
}

// RecoveryPendingResult is the daemon-side struct that gets serialized
// to the proto RecoveryPending event. Mirrors vfs.RecoveryPending plus
// game_id (the IPC layer consumer needs to know which game).
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
	// Ensure socket directory exists.
	dir := filepath.Dir(s.socketPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating socket directory %s: %w", dir, err)
	}

	// Remove stale socket file if present.
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

// Stop performs a graceful stop of the gRPC server and removes the
// socket file. Without the os.Remove the socket inode persists between
// daemon sessions, so `gorganizerd --handle-nxm` connects to a stale
// path and gets ECONNREFUSED instead of "no daemon running".
func (s *Server) Stop() {
	if s.grpcServer != nil {
		slog.Info("stopping gRPC server")
		s.grpcServer.GracefulStop()
	}
	if s.socketPath != "" {
		_ = os.Remove(s.socketPath)
	}
}

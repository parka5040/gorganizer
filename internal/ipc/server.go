package ipc

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	pb "github.com/parka/gorganizer/api/proto"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/fsutil"
	"google.golang.org/grpc"
)

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
	ExecutableController
	TransferController
	ModLoaderController
	ModDependencyController
}

type ModDependencyController interface {
	GetModDependencyReport(ctx context.Context, gameID, profileName string, refreshRemote, forceRemote bool) (dto.ModDependencyReportResult, error)
	FetchModDependencies(ctx context.Context, gameID, profileName string, uniqueIDs []string) ([]dto.DependencyFetchResult, error)
	AckDependencyEnable(ctx context.Context, gameID, batchID string, uniqueIDs []string) (int, error)
}

type ModLoaderController interface {
	GetModLoaderStatus(ctx context.Context, gameID string, checkLatest bool) (dto.ModLoaderStatusResult, error)
	InstallModLoader(ctx context.Context, gameID string, repairOnly bool) (dto.ModLoaderStatusResult, error)
	UninstallModLoader(ctx context.Context, gameID string) (dto.ModLoaderStatusResult, error)
	RollbackModLoader(ctx context.Context, gameID string) (dto.ModLoaderStatusResult, error)
}

type TransferController interface {
	ExportInstance(ctx context.Context, req dto.ExportRequest, emit func(dto.TransferProgress)) (dto.TransferSummary, error)
	PreviewImport(ctx context.Context, gameID, archivePath string) (dto.ImportPreview, error)
	ImportInstance(ctx context.Context, req dto.ImportRequest, emit func(dto.TransferProgress)) (dto.TransferSummary, error)
}

type ExecutableController interface {
	ListExecutables(gameID string) ([]dto.ExecutableSpec, error)
	UpsertExecutable(gameID string, spec dto.ExecutableSpec) (dto.ExecutableSpec, error)
	RemoveExecutable(gameID, id string) error
	DetectExecutables(gameID string) ([]dto.DetectedExecutable, error)
	LaunchExecutable(gameID, execID, profileName string, autoSort ...bool) (int, string, error)
	CancelExecutable(runID string) error
	GetManagedToolStatus(toolID string) (dto.ManagedToolStatusResult, error)
	InstallManagedTool(ctx context.Context, toolID string) (dto.ManagedToolStatusResult, error)
	RollbackManagedTool(toolID string) (dto.ManagedToolStatusResult, error)
}

type TTWController interface {
	CheckTTWPrereqs(backend int) (dto.TTWPrereqResult, error)
	CheckTTWDiskSpace() (available, required int64, err error)
	CheckFNVNotMounted() error
	PrepareTTWInstaller(userPath string, backend int) (dto.TTWInstallerInfoResult, error)
	CreateBlankTTWMod(modName string) (string, error)
	EnsureNativeMpiInstaller() (path, version string, err error)
	BootstrapFNVPrefix() error
	InstallTTWPrereqs() (string, error)
	LaunchTTWInstaller(info dto.TTWInstallerInfoResult, dataModName string) (string, error)
	CancelTTWInstaller(installID string) error
	GetTTWInstallResult(installID string, block bool) (dto.TTWInstallResultData, error)
	SetTTWLauncherExe(relPath string) error
	VerifyTTWIntegrity() error
	TranslateWinePath(gameID, unixPath string) (string, error)
	MountVFSWithSwap(gameID, profileName string) (*dto.VFSStatusResult, error)
}

type GameController interface {
	ListConfiguredGames() ([]dto.GameInfo, error)
	DetectInstalledGames() ([]dto.GameInfo, error)
	ConfigureGame(gameID, name string, steamAppID uint32, installPath, dataSubpath string) error
}

type ModController interface {
	ListMods(gameID string) ([]dto.ModInfoResult, error)
	GetMod(gameID, modName string) (*dto.ModInfoResult, error)
	RescanMod(gameID, modName string) (*dto.ModInfoResult, error)
	RenameMod(gameID, oldName, newName string) error
	UninstallMod(gameID, modName string, force bool) ([]string, error)
	ReinstallMod(gameID, modName string) (replayed, skipped, fileCount int, err error)
	RegisterManualInstall(gameID, modName, archiveRelPath string) (profilesUpdated int, err error)
	ListOverwriteFiles(gameID string) (entries []dto.OverwriteEntryResult, dir string, err error)
	ExtractOverwriteToMod(gameID, modName string, files []string, keep bool) (fileCount int, err error)
}

type ProfileController interface {
	ListProfiles(gameID string) ([]dto.ProfileResult, error)
	CreateProfile(gameID, name string) (*dto.ProfileResult, error)
	DeleteProfile(gameID, name string) error
	GetModList(gameID, profileName string) ([]dto.ModListEntryResult, error)
	SetModList(gameID, profileName string, entries []dto.ModListEntryResult) error
	ListSeparators(gameID, profileName string) ([]dto.SeparatorResult, bool, error)
	SetSeparators(gameID, profileName string, seps []dto.SeparatorResult, viewEnabled bool) error
}

type VFSController interface {
	MountVFS(gameID, profileName string) (*dto.VFSStatusResult, error)
	UnmountVFS(gameID string) error
	GetVFSStatus(gameID string) (*dto.VFSStatusResult, error)
	RebuildVFS(gameID string) error
	RestoreFromBackup(gameID string, expectedKind dto.RecoveryKind, recoveryID string) error
	RetryDeferredRecovery(gameID string) error
}

type ConflictController interface {
	GetConflicts(gameID, profileName string) ([]dto.FileConflictResult, error)
}

type PluginStatusController interface {
	StreamPluginStatus(ctx context.Context, gameID, profileName string) (<-chan dto.PluginStatusEventResult, error)
	SetPluginOrder(gameID, profileName string, filenames []string) error
	SetPluginLoadout(gameID, profileName string, entries []dto.PluginLoadoutEntryResult) error
}

type ArchiveController interface {
	StartDownload(nxmURI string) (id string, queuedAhead int, err error)
	CancelDownload(id string) error
	RetryDownload(id string) (queuedAhead int, err error)
	ListArchives(gameID string) ([]dto.ArchiveRowResult, error)
	RemoveArchive(gameID, archiveRelPath, downloadID string) error
	SetArchiveHidden(gameID, archiveRelPath string, hidden bool) error
	SetArchivesHiddenBulk(gameID string, hidden bool, scope dto.BulkHideScope) (int, error)
	RefreshArchiveMetadata(gameID, archiveRelPath string) (*dto.ArchiveRowResult, error)
	StreamArchiveEvents(ctx context.Context, gameID string) (<-chan dto.ArchiveEventResult, error)
}

type InstallController interface {
	PreviewInstall(req dto.PreviewInstallRequest) (*dto.PreviewResult, error)
	StartInstall(req dto.StartInstallRequest) (modFolder string, fileCount int, err error)
	DiscardPreview(previewID string) error
	StreamInstallEvents(ctx context.Context, gameID string) (<-chan dto.InstallEventResult, error)
}

type LaunchController interface {
	LaunchGame(gameID string, useTool bool, profileName string) (int, error)
	DetectProton() ([]dto.ProtonVersionResult, error)
	InstallScriptExtender(gameID string) (string, error)
	GetPreferredProton() (string, error)
	SetPreferredProton(path string) error
}

type FNV4GBController interface {
	Install4GBPatcher(gameID string) (dto.FNV4GBInstallResult, error)
	Apply4GBPatch(gameID, patcherExePath string) (output string, err error)
	Get4GBPatchStatus(gameID string) (bool, error)
}

type SettingsController interface {
	SetNexusAPIKey(ctx context.Context, apiKey string) (*dto.NexusAPIKeyResult, error)
	GetGameSettings(gameID string) (*dto.GameSettingsResult, error)
	SetGameSettings(gameID string, autoInstall bool) (*dto.GameSettingsResult, error)
	SetActiveGame(gameID string) error
}

type IniController interface {
	ListProfileIniFiles(gameID, profileName string) (*dto.ProfileIniListResult, error)
	SaveProfileIniFile(gameID, profileName, filename, content string) error
	SetProfileIniEnabled(gameID, profileName string, enabled bool) (*dto.ProfileIniStatusResult, error)
	GetProfileIniStatus(gameID, profileName string) (*dto.ProfileIniStatusResult, error)
	ListIniTweaks(gameID, profileName string) ([]dto.IniTweakStateResult, error)
	SetIniTweak(gameID, profileName, tweakID string, enabled bool) (*dto.IniTweakStateResult, error)
}

type LifecycleController interface {
	Shutdown()
	WatchStatus() <-chan dto.StatusEventResult
	Health() dto.ReadinessResult
}

var (
	gracefulStopTimeout = 3 * time.Second
	forcedStopWait      = 2 * time.Second
)

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

// Start checks the socket directory and path, listens privately, and serves gRPC.
func (s *Server) Start() error {
	dir := filepath.Dir(s.socketPath)
	if err := fsutil.EnsurePrivateDir(dir); err != nil {
		return fmt.Errorf("checking socket directory %s: %w", dir, err)
	}

	info, err := os.Lstat(s.socketPath)
	if err == nil {
		st, ok := info.Sys().(*syscall.Stat_t)
		if info.Mode()&os.ModeSocket == 0 || !ok || int(st.Uid) != os.Getuid() {
			return fmt.Errorf("socket path %s is not a socket owned by this user", s.socketPath)
		}
		if err := os.Remove(s.socketPath); err != nil {
			return fmt.Errorf("removing stale socket %s: %w", s.socketPath, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("checking socket %s: %w", s.socketPath, err)
	}

	lis, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", s.socketPath, err)
	}
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		_ = lis.Close()
		return fmt.Errorf("setting socket permissions %s: %w", s.socketPath, err)
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

// Stop stops serving within gracefulStopTimeout plus forcedStopWait, force-closing connections in the background and then abandoning handlers that still do not return, and removes the socket.
func (s *Server) Stop() {
	if s.grpcServer != nil {
		slog.Info("stopping gRPC server")
		done := make(chan struct{})
		go func() {
			s.grpcServer.GracefulStop()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(gracefulStopTimeout):
			slog.Warn("gRPC graceful stop timed out; cancelling open streams", "timeout", gracefulStopTimeout)
			go s.grpcServer.Stop()
			select {
			case <-done:
			case <-time.After(forcedStopWait):
				slog.Warn("gRPC handlers still running after the forced stop; abandoning them", "waited", forcedStopWait)
			}
		}
	}
	if s.socketPath != "" {
		_ = os.Remove(s.socketPath)
	}
}

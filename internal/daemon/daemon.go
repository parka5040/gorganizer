package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/game"
	inipkg "github.com/parka/gorganizer/internal/ini"
	"github.com/parka/gorganizer/internal/ipc"
	"github.com/parka/gorganizer/internal/mod"
	"github.com/parka/gorganizer/internal/plugins"
	"github.com/parka/gorganizer/internal/profile"
	"github.com/parka/gorganizer/internal/tools"
	"github.com/parka/gorganizer/internal/vfs"
)

// managerHooks wires the download manager's progress + archive-landed
// callbacks to the daemon's per-game stream bus and post-install plumbing.
func (d *Daemon) managerHooks() download.ManagerHooks {
	return download.ManagerHooks{
		OnDownloadProgress: func(snap download.DownloadSnapshot) {
			d.archiveBus.Publish(snap.GameID, ipc.ArchiveEventResult{
				GameID: snap.GameID,
				Progress: &ipc.DownloadProgressResult{
					DownloadID: snap.ID, ModName: snap.ModName,
					BytesDownloaded: snap.BytesDownloaded, BytesTotal: snap.BytesTotal,
					Status: snap.Status, Error: snap.Error,
					QueuedAhead: snap.QueuedAhead, GameID: snap.GameID,
				},
			})
		},
		OnArchiveLanded: func(snap download.DownloadSnapshot, archivePath string, sidecar download.ArchiveSidecar) {
			if row, err := d.buildArchiveRow(snap.GameID, relFromDownloads(snap.GameID, archivePath)); err == nil {
				row.DownloadID = snap.ID
				d.archiveBus.Publish(snap.GameID, ipc.ArchiveEventResult{
					GameID: snap.GameID, RowChanged: row,
				})
			}
			d.invalidateInstalledArchiveCache(snap.GameID)

			settings, _ := config.LoadGameSettings(snap.GameID)
			if !settings.AutoInstall {
				return
			}
			go d.autoInstallAfterDownload(snap.GameID, archivePath, sidecar)
		},
	}
}

// relFromDownloads converts an absolute archive path under DownloadsDir
// into the index-relative form.
func relFromDownloads(gameID, absArchive string) string {
	rel, err := filepath.Rel(config.DownloadsDir(gameID), absArchive)
	if err != nil {
		return absArchive
	}
	return rel
}

// autoInstallAfterDownload is the daemon-side companion to the download
func (d *Daemon) autoInstallAfterDownload(gameID, archivePath string, sidecar download.ArchiveSidecar) {
	rel := relFromDownloads(gameID, archivePath)
	modName := sidecar.ModName
	if modName == "" {
		base := filepath.Base(archivePath)
		modName = strings.TrimSuffix(base, filepath.Ext(base))
	}
	if _, _, err := d.StartInstall(ipc.StartInstallRequest{
		GameID: gameID, ArchiveRelPath: rel,
		Mode: ipc.InstallAsNewMod, TargetMod: modName,
	}); err != nil {
		slog.Info("auto-install skipped", "archive", rel, "reason", err)
	}
}

// Daemon is the central coordinator that owns all subsystems.
// It implements ipc.DaemonController.
type Daemon struct {
	config      *config.Config
	profileMgr  *profile.Manager
	iniMgr      *inipkg.Manager
	mountMgrs   map[string]*vfs.MountManager
	mountStates map[string]mountState
	downloadMgr *download.Manager
	toolMgr     *tools.Manager
	ipcServer   *ipc.Server

	launched   map[int]*launchedGame
	launchedMu sync.Mutex

	pendingRecoveries   map[string]*ipc.RecoveryPendingResult
	gamesAtPath         map[string][]string
	pendingRecoveriesMu sync.Mutex

	activeGameID   string
	activeGameIDMu sync.RWMutex

	statusCh      chan ipc.StatusEventResult
	coalescer     *statusCoalescer
	coalescedCh   chan ipc.StatusEventResult
	coalescerDone chan struct{}
	ingesterDone  chan struct{}

	archiveBus *streamBus[ipc.ArchiveEventResult]
	installBus *streamBus[ipc.InstallEventResult]

	previews *previewCache

	shutdownCh   chan struct{}
	shutdownOnce sync.Once
	mu           sync.RWMutex

	installedArchiveCache   map[string]map[string]archiveInstall
	installedArchiveCacheMu sync.RWMutex

	readiness   ipc.ReadinessResult
	readinessMu sync.RWMutex

	pluginHeaderCache     *plugins.HeaderCache
	pluginHeaderCacheOnce sync.Once

	softDepFetcher   *plugins.SoftDepFetcher
	softDepFetcherMu sync.Mutex
}

type mountState struct {
	profileName string
}

type archiveInstall struct {
	Folder string
	Merged bool
}

// launchedGame captures everything we need to know about an in-flight
type launchedGame struct {
	gameID string
	done   <-chan struct{}
}

// New creates a Daemon from configuration with all subsystems initialized.
func New(cfg *config.Config) (*Daemon, error) {
	profileMgr := profile.NewManager(config.DataDir())
	d := &Daemon{
		config:                cfg,
		profileMgr:            profileMgr,
		iniMgr:                inipkg.NewManager(profileMgr.ProfileDir),
		mountMgrs:             make(map[string]*vfs.MountManager),
		mountStates:           make(map[string]mountState),
		toolMgr:               tools.NewManager(cfg),
		statusCh:              make(chan ipc.StatusEventResult, 64),
		coalescer:             newStatusCoalescer(),
		coalescedCh:           make(chan ipc.StatusEventResult, 16),
		coalescerDone:         make(chan struct{}),
		ingesterDone:          make(chan struct{}),
		shutdownCh:            make(chan struct{}),
		installedArchiveCache: make(map[string]map[string]archiveInstall),
		launched:              make(map[int]*launchedGame),
		pendingRecoveries:     make(map[string]*ipc.RecoveryPendingResult),
		gamesAtPath:           make(map[string][]string),
	}
	go d.runStatusIngest()
	go d.runStatusDrain()

	d.archiveBus = newStreamBus[ipc.ArchiveEventResult](64)
	d.installBus = newStreamBus[ipc.InstallEventResult](64)
	d.previews = newPreviewCache(15*time.Minute, 5)
	go d.runPreviewSweeper()

	download.SetModsDirResolver(config.ModsDir)

	if cfg.NexusAPIKey != "" {
		nexus := download.NewNexusClient(cfg.NexusAPIKey)
		d.downloadMgr = download.NewManager(nexus, cfg, 3, d.managerHooks())
		d.downloadMgr.SetPostInstallHook(d.ensureInModList)
		d.downloadMgr.RehydrateLedger()
	}

	for gameID, gc := range cfg.Games {
		d.ensureMountManager(gameID, gc)
	}

	return d, nil
}

// ensureMountManager creates a MountManager for a game if one doesn't exist.
func (d *Daemon) ensureMountManager(gameID string, gc config.GameConfig) *vfs.MountManager {
	if mm, ok := d.mountMgrs[gameID]; ok {
		return mm
	}
	installPath := gc.InstallPath
	subpath := gc.DataSubpath
	if subpath == "" {
		subpath = "Data"
	}
	if gc.LinkedFromGameID != "" {
		if parent, ok := d.config.Games[gc.LinkedFromGameID]; ok && parent.InstallPath != "" {
			installPath = parent.InstallPath
			if subpath == "" {
				subpath = parent.DataSubpath
				if subpath == "" {
					subpath = "Data"
				}
			}
		}
	}
	mm := vfs.NewMountManager(
		filepath.Join(installPath, subpath),
		filepath.Join(config.ModsDir(gameID), "Overwrite"),
	)
	d.mountMgrs[gameID] = mm
	return mm
}

func (d *Daemon) RecoverAll() {
	d.setReadinessStep("checking crash recovery", nil)
	defer d.setReadinessStep("recovery complete", func(r *ipc.ReadinessResult) { r.RecoveryDone = true })

	pathToGames := map[string][]string{}
	pathOrder := []string{}
	for gameID, mm := range d.mountMgrs {
		dataPath := mm.DataPath()
		resolved, err := filepath.Abs(dataPath)
		if err != nil {
			resolved = dataPath
		}
		if _, seen := pathToGames[resolved]; !seen {
			pathOrder = append(pathOrder, resolved)
		}
		pathToGames[resolved] = append(pathToGames[resolved], gameID)
	}

	for _, dataPath := range pathOrder {
		gameIDs := pathToGames[dataPath]
		mm := d.mountMgrs[gameIDs[0]]
		outcome, err := mm.RecoverIfNeeded()
		if err != nil {
			slog.Error("crash recovery failed", "data_path", dataPath, "games", gameIDs, "err", err)
			continue
		}
		if outcome.Pending == nil {
			continue
		}
		pending := &ipc.RecoveryPendingResult{
			GameID:     gameIDs[0],
			DataPath:   outcome.Pending.DataPath,
			BackupPath: outcome.Pending.BackupPath,
			Reason:     outcome.Pending.Reason,
		}
		d.pendingRecoveriesMu.Lock()
		d.pendingRecoveries[dataPath] = pending
		d.gamesAtPath[dataPath] = append([]string{}, gameIDs...)
		d.pendingRecoveriesMu.Unlock()
		slog.Warn("recovery pending — refusing to mount/launch until user confirms",
			"data_path", dataPath, "games", gameIDs, "reason", pending.Reason)
		select {
		case d.statusCh <- ipc.StatusEventResult{RecoveryPending: pending}:
		default:
		}
	}
}

const shutdownTimeout = 30 * time.Second

var mutexGroups = map[string]string{
	"falloutnv": "fnv-data",
	"ttw":       "fnv-data",
}

// mutexGroupOf returns the mutex group for a gameID, "" if none.
func mutexGroupOf(gameID string) string {
	return mutexGroups[gameID]
}

// isSynthetic returns true if the gameID corresponds to a synthetic game
// definition (currently only TTW). Daemon paths that need to behave
func isSynthetic(gameID string) bool {
	def, ok := game.FindByID(gameID)
	return ok && def.Synthetic
}

// findMutexConflict returns the gameID of the currently-mounted sibling
// in gameID's mutex group, or "" if no conflict exists. Caller must hold
func (d *Daemon) findMutexConflict(gameID string) string {
	group := mutexGroupOf(gameID)
	if group == "" {
		return ""
	}
	for other, otherGroup := range mutexGroups {
		if other == gameID || otherGroup != group {
			continue
		}
		mm, ok := d.mountMgrs[other]
		if !ok || !mm.IsMounted() {
			continue
		}
		return other
	}
	return ""
}

// Run starts the gRPC server and blocks until shutdown.
func (d *Daemon) Run(socketPath string) error {
	d.ipcServer = ipc.NewServer(socketPath, d)
	if err := d.ipcServer.Start(); err != nil {
		return fmt.Errorf("starting IPC server: %w", err)
	}
	d.setReadinessStep("socket bound", func(r *ipc.ReadinessResult) { r.SocketReady = true })
	go d.warmupAsync()

	<-d.shutdownCh

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	d.shutdownAll(ctx)
	return nil
}

func (d *Daemon) Health() ipc.ReadinessResult {
	d.readinessMu.RLock()
	defer d.readinessMu.RUnlock()
	return d.readiness
}

func (d *Daemon) setReadinessStep(step string, mutate func(*ipc.ReadinessResult)) {
	d.readinessMu.Lock()
	d.readiness.LastInitStep = step
	if mutate != nil {
		mutate(&d.readiness)
	}
	d.readinessMu.Unlock()
}

// warmupAsync runs the slow cold-start tasks (crash recovery, Steam scan,
// per-game metadata caching) in the background so the gRPC server doesn't
func (d *Daemon) warmupAsync() {
	d.RecoverAll()

	d.setReadinessStep("detecting games", nil)
	if _, err := d.DetectInstalledGames(); err != nil {
		slog.Warn("warmup: DetectInstalledGames failed", "err", err)
	}

	d.mu.RLock()
	gameIDs := make([]string, 0, len(d.config.Games))
	for id := range d.config.Games {
		gameIDs = append(gameIDs, id)
	}
	d.mu.RUnlock()
	for _, id := range gameIDs {
		d.setReadinessStep("warming "+id, nil)
		d.sweepOrphanStageDirs(id)
		_ = d.installedArchiveMap(id)
	}

	d.setReadinessStep("ready", func(r *ipc.ReadinessResult) { r.GamesWarmed = true })
	select {
	case d.statusCh <- ipc.StatusEventResult{Info: "ready"}:
	default:
	}
}

func (d *Daemon) shutdownAll(ctx context.Context) {
	d.waitForLaunchedExit(ctx)

	d.mu.Lock()
	defer d.mu.Unlock()

	for gameID, mm := range d.mountMgrs {
		if mm.IsMounted() {
			slog.Info("deactivating VFS on shutdown", "game", gameID)
			if err := mm.Deactivate(); err != nil {
				slog.Error("deactivation failed on shutdown", "game", gameID, "err", err)
			}
		}
	}

	if d.ipcServer != nil {
		d.ipcServer.Stop()
	}

	close(d.statusCh)
	<-d.ingesterDone
	<-d.coalescerDone
}

func (d *Daemon) trackLaunched(gameID string, h *tools.LaunchHandle) {
	if h == nil {
		return
	}
	d.launchedMu.Lock()
	d.launched[h.PID] = &launchedGame{gameID: gameID, done: h.Done}
	d.launchedMu.Unlock()
	go func() {
		<-h.Done
		d.launchedMu.Lock()
		delete(d.launched, h.PID)
		remaining := len(d.launched)
		d.launchedMu.Unlock()
		slog.Info("launched game exited", "game", gameID, "pid", h.PID, "still_running", remaining)
	}()
}

// waitForLaunchedExit blocks until every registered Proton launch has
// signaled Done, or until ctx is cancelled (the watchdog in Run). Emits
func (d *Daemon) waitForLaunchedExit(ctx context.Context) {
	d.launchedMu.Lock()
	dones := make([]<-chan struct{}, 0, len(d.launched))
	ids := make([]string, 0, len(d.launched))
	for _, lg := range d.launched {
		dones = append(dones, lg.done)
		ids = append(ids, lg.gameID)
	}
	d.launchedMu.Unlock()

	if len(dones) == 0 {
		return
	}
	slog.Info("waiting for launched games to exit before unmounting VFS",
		"count", len(dones), "games", ids)
	for _, done := range dones {
		select {
		case <-done:
		case <-ctx.Done():
			slog.Warn("shutdown timed out waiting for launched games — proceeding anyway",
				"remaining", len(dones), "games", ids,
				"reason", "VFS may not unmount cleanly; user must verify Data/ on next launch")
			return
		}
	}
	slog.Info("all launched games have exited — proceeding with shutdown")
}

func (d *Daemon) ListConfiguredGames() ([]ipc.GameInfo, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var games []ipc.GameInfo
	for gameID, gc := range d.config.Games {
		subpath := gc.DataSubpath
		if subpath == "" {
			subpath = "Data"
		}
		installPath := gc.InstallPath
		appID := uint32(gc.SteamAppID)
		if gc.LinkedFromGameID != "" {
			if parent, ok := d.config.Games[gc.LinkedFromGameID]; ok {
				installPath = parent.InstallPath
				appID = uint32(parent.SteamAppID)
			}
		}
		vfsActive := false
		if mm, ok := d.mountMgrs[gameID]; ok {
			vfsActive = mm.IsMounted()
		}
		games = append(games, ipc.GameInfo{
			GameID:           gameID,
			Name:             gc.Name,
			SteamAppID:       appID,
			InstallPath:      installPath,
			DataPath:         filepath.Join(installPath, subpath),
			Synthetic:        isSynthetic(gameID),
			LinkedFromGameID: gc.LinkedFromGameID,
			VFSActive:        vfsActive,
		})
	}
	return games, nil
}

func (d *Daemon) DetectInstalledGames() ([]ipc.GameInfo, error) {
	detected, err := game.DetectInstalledGames()
	if err != nil {
		return nil, err
	}

	detected = d.applyTTWPlayableProbe(detected)

	d.mu.Lock()
	for _, g := range detected {
		if _, exists := d.config.Games[g.ID]; !exists {
			gc := config.GameConfig{
				Name:        g.Name,
				InstallPath: g.InstallPath,
				DataSubpath: g.DataSubpath,
				SteamAppID:  int(g.SteamAppID),
			}
			if g.Synthetic && g.ParentGameID != "" {
				gc.LinkedFromGameID = g.ParentGameID
				gc.SteamAppID = 0
			}
			d.config.Games[g.ID] = gc
			d.ensureMountManager(g.ID, gc)
			slog.Info("auto-configured detected game", "id", g.ID, "path", g.InstallPath, "synthetic", g.Synthetic)
		}
	}
	d.config.Save()
	d.mu.Unlock()

	var games []ipc.GameInfo
	for _, g := range detected {
		vfsActive := false
		if mm, ok := d.mountMgrs[g.ID]; ok {
			vfsActive = mm.IsMounted()
		}
		games = append(games, ipc.GameInfo{
			GameID:           g.ID,
			Name:             g.Name,
			SteamAppID:       g.SteamAppID,
			InstallPath:      g.InstallPath,
			DataPath:         g.DataPath,
			Synthetic:        g.Synthetic,
			LinkedFromGameID: g.ParentGameID,
			VFSActive:        vfsActive,
		})
	}
	return games, nil
}

// applyTTWPlayableProbe is the daemon-side TTWPlayableProbe wired into
// game.AppendSyntheticGames so a TTW install marker (after FO3 refund)
func (d *Daemon) applyTTWPlayableProbe(detected []game.DetectedGame) []game.DetectedGame {
	probe := func() (string, bool) {
		var fnvInstall string
		for _, g := range detected {
			if g.ID == "falloutnv" {
				fnvInstall = g.InstallPath
				break
			}
		}
		if fnvInstall == "" {
			return "", false
		}
		if !game.HasTTWMarker(fnvInstall) {
			return "", false
		}
		entries, err := os.ReadDir(config.ModsDir("ttw"))
		if err != nil {
			return "", false
		}
		for _, e := range entries {
			if e.IsDir() && e.Name() != "Downloads" && e.Name() != "Overwrite" {
				return fnvInstall, true
			}
		}
		return "", false
	}
	return game.AppendSyntheticGames(detected, probe)
}

// ConfigureGame persists a game to the daemon's config and creates its
// mount manager. Called by the frontend after setup wizard detects games.
func (d *Daemon) ConfigureGame(gameID, name string, steamAppID uint32, installPath, dataSubpath string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if dataSubpath == "" {
		dataSubpath = "Data"
	}

	gc := config.GameConfig{
		Name:        name,
		InstallPath: installPath,
		DataSubpath: dataSubpath,
		SteamAppID:  int(steamAppID),
	}
	d.config.Games[gameID] = gc
	d.ensureMountManager(gameID, gc)

	if err := d.config.Save(); err != nil {
		return fmt.Errorf("saving config after configuring game %s: %w", gameID, err)
	}

	slog.Info("game configured", "id", gameID, "path", installPath)
	return nil
}

func (d *Daemon) ListProfiles(gameID string) ([]ipc.ProfileResult, error) {
	profiles, err := d.profileMgr.List(gameID)
	if err != nil {
		return nil, err
	}

	var results []ipc.ProfileResult
	for _, p := range profiles {
		results = append(results, ipc.ProfileResult{
			Name:      p.Name,
			GameID:    p.GameID,
			CreatedAt: p.CreatedAt.Format("2006-01-02T15:04:05Z"),
		})
	}
	return results, nil
}

func (d *Daemon) CreateProfile(gameID, name string) (*ipc.ProfileResult, error) {
	p, err := d.profileMgr.Create(gameID, name)
	if err != nil {
		return nil, err
	}
	return &ipc.ProfileResult{
		Name:      p.Name,
		GameID:    p.GameID,
		CreatedAt: p.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}, nil
}

func (d *Daemon) DeleteProfile(gameID, name string) error {
	return d.profileMgr.Delete(gameID, name)
}

func (d *Daemon) GetModList(gameID, profileName string) ([]ipc.ModListEntryResult, error) {
	_, entries, err := d.profileMgr.Load(gameID, profileName)
	if err != nil {
		return nil, err
	}

	var results []ipc.ModListEntryResult
	for i, e := range entries {
		results = append(results, ipc.ModListEntryResult{
			ModName:  e.Name,
			Enabled:  e.Enabled,
			Priority: i,
		})
	}
	return results, nil
}

func (d *Daemon) SetModList(gameID, profileName string, entries []ipc.ModListEntryResult) error {
	p, _, err := d.profileMgr.Load(gameID, profileName)
	if err != nil {
		return err
	}

	var modEntries []mod.ModListEntry
	for _, e := range entries {
		modEntries = append(modEntries, mod.ModListEntry{
			Name:    e.ModName,
			Enabled: e.Enabled,
		})
	}
	if err := d.profileMgr.Save(p, modEntries); err != nil {
		return err
	}

	d.writeTrueIndexes(gameID, modEntries)

	d.mu.RLock()
	mm, mmOk := d.mountMgrs[gameID]
	ms, msOk := d.mountStates[gameID]
	gc, gcOk := d.config.Games[gameID]
	d.mu.RUnlock()
	if mmOk && msOk && gcOk && ms.profileName == profileName && mm.IsMounted() {
		layers := d.buildLayers(gameID, gc, modEntries)
		if err := mm.Tree().Rebuild(layers); err != nil {
			slog.Warn("VFS rebuild after modlist change failed", "game", gameID, "err", err)
		} else {
			enabled := 0
			for _, e := range modEntries {
				if e.Enabled {
					enabled++
				}
			}
			fileCount, _ := mm.Tree().Stats()
			subpath := gc.DataSubpath
			if subpath == "" {
				subpath = "Data"
			}
			select {
			case d.statusCh <- ipc.StatusEventResult{VFSStatus: &ipc.VFSStatusResult{
				Mounted:         true,
				GameID:          gameID,
				ProfileName:     profileName,
				MountPoint:      filepath.Join(gc.InstallPath, subpath),
				EnabledModCount: enabled,
				TotalFileCount:  fileCount,
			}}:
			default:
			}
		}
	}
	return nil
}

func (d *Daemon) MountVFS(gameID, profileName string) (*ipc.VFSStatusResult, error) {
	return d.mountVFSWithSwap(gameID, profileName, false)
}

// MountVFSWithSwap is the auto-swap variant: when gameID's mutex group
func (d *Daemon) MountVFSWithSwap(gameID, profileName string) (*ipc.VFSStatusResult, error) {
	return d.mountVFSWithSwap(gameID, profileName, true)
}

func (d *Daemon) mountVFSWithSwap(gameID, profileName string, autoSwap bool) (*ipc.VFSStatusResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if pending := d.recoveryPendingFor(gameID); pending != nil {
		return nil, fmt.Errorf("recovery pending for %s: %s — confirm via the GUI prompt or `gorganizerctl recover-confirm` first",
			gameID, pending.Reason)
	}

	if conflict := d.findMutexConflict(gameID); conflict != "" {
		if !autoSwap {
			return nil, &ipc.VFSMutexError{
				GameID:      gameID,
				Conflicting: conflict,
				Group:       mutexGroupOf(gameID),
			}
		}
		if conflictMM, ok := d.mountMgrs[conflict]; ok && conflictMM.IsMounted() {
			if err := conflictMM.Deactivate(); err != nil {
				return nil, fmt.Errorf("auto-swap deactivate of %s failed: %w", conflict, err)
			}
			delete(d.mountStates, conflict)
			select {
			case d.statusCh <- ipc.StatusEventResult{VFSStatus: &ipc.VFSStatusResult{GameID: conflict}}:
			default:
			}
			slog.Info("auto-swap: deactivated conflicting VFS", "deactivated", conflict, "now_activating", gameID)
		}
	}

	gc, ok := d.config.Games[gameID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	if gc.LinkedFromGameID != "" {
		if _, parentOk := d.config.Games[gc.LinkedFromGameID]; !parentOk {
			return nil, &ipc.ErrLinkedParentMissing{
				GameID:       gameID,
				ParentGameID: gc.LinkedFromGameID,
			}
		}
	}

	mm := d.ensureMountManager(gameID, gc)

	_, entries, err := d.profileMgr.Load(gameID, profileName)
	if err != nil {
		return nil, fmt.Errorf("loading profile %q: %w", profileName, err)
	}

	layers := d.buildLayers(gameID, gc, entries)

	if err := mm.Activate(layers); err != nil {
		return nil, err
	}

	d.mountStates[gameID] = mountState{profileName: profileName}

	enabledCount := 0
	for _, e := range entries {
		if e.Enabled {
			enabledCount++
		}
	}

	fileCount, _ := mm.Tree().Stats()
	subpath := gc.DataSubpath
	if subpath == "" {
		subpath = "Data"
	}
	st := &ipc.VFSStatusResult{
		Mounted:         true,
		GameID:          gameID,
		ProfileName:     profileName,
		MountPoint:      filepath.Join(gc.InstallPath, subpath),
		EnabledModCount: enabledCount,
		TotalFileCount:  fileCount,
	}

	select {
	case d.statusCh <- ipc.StatusEventResult{VFSStatus: st}:
	default:
	}
	return st, nil
}

func (d *Daemon) UnmountVFS(gameID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	mm, ok := d.mountMgrs[gameID]
	if !ok {
		return fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	if err := mm.Deactivate(); err != nil {
		return err
	}
	delete(d.mountStates, gameID)

	select {
	case d.statusCh <- ipc.StatusEventResult{VFSStatus: &ipc.VFSStatusResult{GameID: gameID}}:
	default:
	}
	return nil
}

func (d *Daemon) GetVFSStatus(gameID string) (*ipc.VFSStatusResult, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	mm, ok := d.mountMgrs[gameID]
	if !ok {
		return &ipc.VFSStatusResult{GameID: gameID}, nil
	}

	st := &ipc.VFSStatusResult{
		Mounted: mm.IsMounted(),
		GameID:  gameID,
	}

	if ms, ok := d.mountStates[gameID]; ok {
		st.ProfileName = ms.profileName
	}
	if mm.IsMounted() && mm.Tree() != nil {
		fileCount, _ := mm.Tree().Stats()
		st.TotalFileCount = fileCount
	}
	return st, nil
}

// recoveryPendingFor returns the pending recovery record for gameID, or
// nil when none is registered. Looks up via the gameID's resolved Data/
func (d *Daemon) recoveryPendingFor(gameID string) *ipc.RecoveryPendingResult {
	mm, ok := d.mountMgrs[gameID]
	if !ok {
		return nil
	}
	resolved, err := filepath.Abs(mm.DataPath())
	if err != nil {
		resolved = mm.DataPath()
	}
	d.pendingRecoveriesMu.Lock()
	defer d.pendingRecoveriesMu.Unlock()
	return d.pendingRecoveries[resolved]
}

func (d *Daemon) RestoreFromBackup(gameID string) error {
	mm, ok := d.mountMgrs[gameID]
	if !ok {
		return fmt.Errorf("no mount manager for %s", gameID)
	}
	resolved, err := filepath.Abs(mm.DataPath())
	if err != nil {
		resolved = mm.DataPath()
	}

	d.pendingRecoveriesMu.Lock()
	pending, exists := d.pendingRecoveries[resolved]
	siblings := append([]string{}, d.gamesAtPath[resolved]...)
	d.pendingRecoveriesMu.Unlock()
	if !exists {
		return fmt.Errorf("no recovery pending for %s (path %s)", gameID, resolved)
	}

	if err := vfs.RestoreFromBackup(pending.DataPath); err != nil {
		return fmt.Errorf("restoring %s: %w", pending.DataPath, err)
	}

	d.pendingRecoveriesMu.Lock()
	delete(d.pendingRecoveries, resolved)
	delete(d.gamesAtPath, resolved)
	d.pendingRecoveriesMu.Unlock()

	slog.Info("restore from backup completed via user consent",
		"game", gameID, "path", pending.DataPath, "siblings", siblings)
	for _, sibling := range siblings {
		select {
		case d.statusCh <- ipc.StatusEventResult{Info: fmt.Sprintf("recovery resolved for %s", sibling)}:
		default:
		}
	}
	return nil
}

func (d *Daemon) RebuildVFS(gameID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	mm, ok := d.mountMgrs[gameID]
	if !ok || !mm.IsMounted() {
		return fmt.Errorf("%w for %s", vfs.ErrNotMounted, gameID)
	}

	gc := d.config.Games[gameID]
	ms := d.mountStates[gameID]

	_, entries, err := d.profileMgr.Load(gameID, ms.profileName)
	if err != nil {
		return err
	}

	layers := d.buildLayers(gameID, gc, entries)
	return mm.Tree().Rebuild(layers)
}

func (d *Daemon) buildLayers(gameID string, gc config.GameConfig, entries []mod.ModListEntry) []vfs.Layer {
	subpath := gc.DataSubpath
	if subpath == "" {
		subpath = "Data"
	}

	layers := []vfs.Layer{
		{Name: "__base__", RootPath: filepath.Join(gc.InstallPath, subpath), Enabled: true},
	}

	modsDir := config.ModsDir(gameID)
	for _, e := range entries {
		if !e.Enabled {
			continue
		}
		if e.Name == profile.OverwriteModName {
			continue
		}
		m := mod.NewMod(e.Name, gameID, filepath.Join(modsDir, e.Name))
		layers = append(layers, vfs.Layer{
			Name:     e.Name,
			RootPath: m.BasePath,
			Enabled:  true,
		})
	}

	owDir := filepath.Join(modsDir, profile.OverwriteModName)
	layers = append(layers, vfs.Layer{
		Name:     profile.OverwriteModName,
		RootPath: owDir,
		Enabled:  true,
	})
	return layers
}

func (d *Daemon) GetConflicts(gameID, profileName string) ([]ipc.FileConflictResult, error) {
	gc, ok := d.config.Games[gameID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}

	_, entries, err := d.profileMgr.Load(gameID, profileName)
	if err != nil {
		return nil, err
	}

	layers := d.buildLayers(gameID, gc, entries)
	cm, err := mod.BuildConflictMap(layers)
	if err != nil {
		return nil, err
	}

	var results []ipc.FileConflictResult
	for _, c := range cm.Conflicts {
		results = append(results, ipc.FileConflictResult{
			VirtualPath: c.VirtualPath,
			WinningMod:  c.Winner,
			LosingMods:  c.Losers,
		})
	}
	return results, nil
}

// installedArchiveMap builds archive-rel-path → archiveInstall for every mod
// under ModsDir(gameID) whose metadata.yaml lists source_archives. Memoized
func (d *Daemon) installedArchiveMap(gameID string) map[string]archiveInstall {
	d.installedArchiveCacheMu.RLock()
	if cached, ok := d.installedArchiveCache[gameID]; ok {
		d.installedArchiveCacheMu.RUnlock()
		return cached
	}
	d.installedArchiveCacheMu.RUnlock()

	modsDir := config.ModsDir(gameID)
	out := map[string]archiveInstall{}
	entries, err := os.ReadDir(modsDir)
	if err != nil {
		d.installedArchiveCacheMu.Lock()
		d.installedArchiveCache[gameID] = out
		d.installedArchiveCacheMu.Unlock()
		return out
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "Downloads" {
			continue
		}
		modDir := filepath.Join(modsDir, entry.Name())
		meta, err := download.LoadModMetadata(modDir)
		if err != nil {
			continue
		}
		for _, sa := range meta.SourceArchives {
			out[sa.Path] = archiveInstall{Folder: entry.Name(), Merged: sa.Merged}
		}
	}
	d.installedArchiveCacheMu.Lock()
	d.installedArchiveCache[gameID] = out
	d.installedArchiveCacheMu.Unlock()
	return out
}

func (d *Daemon) invalidateInstalledArchiveCache(gameID string) {
	d.installedArchiveCacheMu.Lock()
	defer d.installedArchiveCacheMu.Unlock()
	if gameID == "" {
		d.installedArchiveCache = make(map[string]map[string]archiveInstall)
		return
	}
	delete(d.installedArchiveCache, gameID)
}

func (d *Daemon) ensureInModList(gameID, modName string) {
	d.invalidateInstalledArchiveCache(gameID)
	profiles, err := d.profileMgr.List(gameID)
	if err != nil || len(profiles) == 0 {
		profiles = []*profile.Profile{{Name: "Default", GameID: gameID}}
	}
	for _, p := range profiles {
		_, entries, err := d.profileMgr.Load(gameID, p.Name)
		if err != nil {
			entries = nil
		}
		present := false
		for _, e := range entries {
			if e.Name == modName {
				present = true
				break
			}
		}
		if present {
			continue
		}
		entries = append(entries, mod.ModListEntry{Name: modName, Enabled: false})
		if err := d.profileMgr.Save(p, entries); err != nil {
			slog.Warn("could not update modlist.txt", "game", gameID, "profile", p.Name, "err", err)
		}
	}
}

// RegisterManualInstall is the post-install hook for paths that produce a
// mod folder without going through StartInstall (e.g., the C++ FOMOD wizard
func (d *Daemon) RegisterManualInstall(gameID, modName, archiveRelPath string) (int, error) {
	if _, ok := d.config.Games[gameID]; !ok {
		return 0, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	if modName == "" {
		return 0, fmt.Errorf("mod_name required")
	}
	modDir := filepath.Join(config.ModsDir(gameID), modName)
	if _, err := os.Stat(modDir); err != nil {
		if os.IsNotExist(err) {
			return 0, &ipc.ModNotFoundError{GameID: gameID, Name: modName}
		}
		return 0, err
	}

	d.invalidateInstalledArchiveCache(gameID)

	profiles, err := d.profileMgr.List(gameID)
	if err != nil || len(profiles) == 0 {
		profiles = []*profile.Profile{{Name: "Default", GameID: gameID}}
	}
	updated := 0
	for _, p := range profiles {
		_, entries, err := d.profileMgr.Load(gameID, p.Name)
		if err != nil {
			entries = nil
		}
		present := false
		for _, e := range entries {
			if e.Name == modName {
				present = true
				break
			}
		}
		if present {
			continue
		}
		entries = append(entries, mod.ModListEntry{Name: modName, Enabled: false})
		if err := d.profileMgr.Save(p, entries); err != nil {
			slog.Warn("RegisterManualInstall: could not update modlist.txt",
				"game", gameID, "profile", p.Name, "err", err)
			continue
		}
		updated++
	}

	if archiveRelPath != "" {
		if row, err := d.buildArchiveRow(gameID, archiveRelPath); err == nil {
			d.archiveBus.Publish(gameID, ipc.ArchiveEventResult{
				GameID: gameID, RowChanged: row,
			})
		}
	}

	fileCount := 0
	if meta, err := download.LoadModMetadata(modDir); err == nil && meta != nil {
		fileCount = meta.FileCount
	}
	d.installBus.Publish(gameID, ipc.InstallEventResult{
		GameID: gameID,
		Progress: &ipc.InstallProgressResult{
			InstallID:      "manual-" + modName,
			ArchiveRelPath: archiveRelPath,
			ModName:        modName,
			Step:           ipc.InstallStepComplete,
			Pct:            100,
			FilesDone:      int64(fileCount),
			FilesTotal:     int64(fileCount),
			GameID:         gameID,
		},
	})

	return updated, nil
}

func (d *Daemon) ListOverwriteFiles(gameID string) ([]ipc.OverwriteEntryResult, string, error) {
	if _, ok := d.config.Games[gameID]; !ok {
		return nil, "", fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	owDir := filepath.Join(config.ModsDir(gameID), profile.OverwriteModName)
	var out []ipc.OverwriteEntryResult
	err := filepath.WalkDir(owDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) && path == owDir {
				return filepath.SkipAll
			}
			return walkErr
		}
		if path == owDir {
			return nil
		}
		rel, _ := filepath.Rel(owDir, path)
		entry := ipc.OverwriteEntryResult{
			RelPath: filepath.ToSlash(rel),
			IsDir:   d.IsDir(),
		}
		if info, ierr := d.Info(); ierr == nil {
			if !entry.IsDir {
				entry.SizeBytes = info.Size()
			}
			entry.ModifiedAt = info.ModTime().UTC().Format(time.RFC3339)
		}
		out = append(out, entry)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, owDir, err
	}
	return out, owDir, nil
}

// ExtractOverwriteToMod graduates a subset of loose files from Overwrite
func (d *Daemon) ExtractOverwriteToMod(gameID, modName string, files []string, keep bool) (int, error) {
	if _, ok := d.config.Games[gameID]; !ok {
		return 0, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	if modName == "" {
		return 0, fmt.Errorf("mod_name required")
	}
	if modName == profile.OverwriteModName {
		return 0, fmt.Errorf("mod_name %q is reserved", modName)
	}

	modsDir := config.ModsDir(gameID)
	owDir := filepath.Join(modsDir, profile.OverwriteModName)
	destDir := filepath.Join(modsDir, modName)
	if _, err := os.Stat(destDir); err == nil {
		return 0, &ipc.ModCollisionError{Name: modName}
	}

	var paths []string
	if len(files) == 0 {
		_ = filepath.WalkDir(owDir, func(path string, de fs.DirEntry, err error) error {
			if err != nil || de.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(owDir, path)
			paths = append(paths, filepath.ToSlash(rel))
			return nil
		})
	} else {
		for _, f := range files {
			full := filepath.Join(owDir, filepath.FromSlash(f))
			info, err := os.Stat(full)
			if err != nil {
				continue
			}
			if !info.IsDir() {
				paths = append(paths, f)
				continue
			}
			_ = filepath.WalkDir(full, func(path string, de fs.DirEntry, werr error) error {
				if werr != nil || de.IsDir() {
					return werr
				}
				rel, _ := filepath.Rel(owDir, path)
				paths = append(paths, filepath.ToSlash(rel))
				return nil
			})
		}
	}
	if len(paths) == 0 {
		return 0, fmt.Errorf("no files to extract")
	}

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return 0, fmt.Errorf("creating mod dir: %w", err)
	}

	count := 0
	for _, rel := range paths {
		src := filepath.Join(owDir, filepath.FromSlash(rel))
		dst := filepath.Join(destDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			slog.Warn("mkdir for extract failed", "path", dst, "err", err)
			continue
		}
		if keep {
			if err := copyFileForExtract(src, dst); err != nil {
				slog.Warn("copy for extract failed", "src", src, "dst", dst, "err", err)
				continue
			}
		} else {
			if err := os.Rename(src, dst); err != nil {
				if err := copyFileForExtract(src, dst); err != nil {
					slog.Warn("move-via-copy for extract failed", "src", src, "err", err)
					continue
				}
				_ = os.Remove(src)
			}
		}
		count++
	}

	if !keep {
		var dirs []string
		_ = filepath.WalkDir(owDir, func(path string, de fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if de.IsDir() && path != owDir {
				dirs = append(dirs, path)
			}
			return nil
		})
		sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
		for _, d := range dirs {
			_ = os.Remove(d)
		}
	}

	if err := download.AppendSourceArchive(
		destDir, modName,
		download.SourceArchiveRef{},
		modName /*displayName*/, "" /*category*/, "" /*version*/, "", /*modPage*/
		paths,
	); err != nil {
		slog.Warn("ExtractOverwriteToMod: metadata write failed", "err", err)
	}

	if _, err := d.RegisterManualInstall(gameID, modName, ""); err != nil {
		slog.Warn("ExtractOverwriteToMod: RegisterManualInstall failed", "err", err)
	}

	return count, nil
}

func copyFileForExtract(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}

// sweepOrphanStageDirs removes any `.stage-<rand>/` directories sitting
func (d *Daemon) sweepOrphanStageDirs(gameID string) {
	modsDir := config.ModsDir(gameID)
	entries, err := os.ReadDir(modsDir)
	if err != nil {
		return
	}
	removed := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !strings.HasPrefix(e.Name(), ".stage-") {
			continue
		}
		path := filepath.Join(modsDir, e.Name())
		if err := os.RemoveAll(path); err != nil {
			slog.Warn("sweepOrphanStageDirs: remove failed", "path", path, "err", err)
			continue
		}
		removed++
	}
	if removed > 0 {
		slog.Info("removed orphan install stage dirs", "game", gameID, "count", removed)
	}
}

func (d *Daemon) runStatusIngest() {
	defer close(d.ingesterDone)
	for evt := range d.statusCh {
		d.coalescer.Push(evt)
	}
	d.coalescer.Close()
}

func (d *Daemon) runStatusDrain() {
	defer close(d.coalescerDone)
	defer close(d.coalescedCh)
	for {
		evt, ok := d.coalescer.Drain()
		if !ok {
			return
		}
		d.coalescedCh <- evt
	}
}

// GetGameSettings returns the per-game settings (auto_install toggle).
// A game with no settings file yields defaults — not an error.
func (d *Daemon) GetGameSettings(gameID string) (*ipc.GameSettingsResult, error) {
	if _, ok := d.config.Games[gameID]; !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	s, err := config.LoadGameSettings(gameID)
	if err != nil {
		return nil, err
	}
	return &ipc.GameSettingsResult{GameID: gameID, AutoInstall: s.AutoInstall}, nil
}

func (d *Daemon) SetGameSettings(gameID string, autoInstall bool) (*ipc.GameSettingsResult, error) {
	if _, ok := d.config.Games[gameID]; !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	s := config.GameSettings{AutoInstall: autoInstall}
	if err := config.SaveGameSettings(gameID, s); err != nil {
		return nil, err
	}
	return &ipc.GameSettingsResult{GameID: gameID, AutoInstall: s.AutoInstall}, nil
}

func (d *Daemon) LaunchGame(gameID string, useTool bool, profileName string) (int, error) {
	if pending := d.recoveryPendingFor(gameID); pending != nil {
		return 0, fmt.Errorf("recovery pending for %s: %s — confirm via the GUI prompt or `gorganizerctl recover-confirm` first",
			gameID, pending.Reason)
	}
	if conflict := d.findMutexConflict(gameID); conflict != "" {
		return 0, &ipc.VFSMutexError{
			GameID:      gameID,
			Conflicting: conflict,
			Group:       mutexGroupOf(gameID),
		}
	}
	gc, ok := d.config.Games[gameID]
	if !ok {
		return 0, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	if gc.LinkedFromGameID != "" {
		if _, parentOk := d.config.Games[gc.LinkedFromGameID]; !parentOk {
			return 0, &ipc.ErrLinkedParentMissing{
				GameID:       gameID,
				ParentGameID: gc.LinkedFromGameID,
			}
		}
	}
	if gc.LinkedFromGameID != "" {
		eff, err := d.config.EffectiveGameConfig(gameID)
		if err != nil {
			return 0, err
		}
		gc = eff
	}

	if isSynthetic(gameID) {
		if err := d.VerifyTTWIntegrity(); err != nil {
			return 0, err
		}
	}

	mm := d.ensureMountManager(gameID, gc)
	if !mm.IsMounted() && profileName != "" {
		slog.Info("auto-mounting VFS before launch", "game", gameID, "profile", profileName)
		if _, err := d.MountVFS(gameID, profileName); err != nil {
			if isSynthetic(gameID) {
				return 0, fmt.Errorf("auto-mount of %s VFS failed: %w", gameID, err)
			}
			slog.Warn("VFS auto-mount failed, launching without mods", "game", gameID, "err", err)
			select {
			case d.statusCh <- ipc.StatusEventResult{Info: fmt.Sprintf("VFS mount skipped: %v", err)}:
			default:
			}
		}
	}

	if profileName == "" {
		slog.Warn("no profile selected — skipping INI push (tweaks like disable-intro will NOT apply)",
			"game", gameID)
	} else {
		p, _, err := d.profileMgr.Load(gameID, profileName)
		switch {
		case err != nil:
			slog.Warn("profile load failed — skipping INI push", "game", gameID, "profile", profileName, "err", err)
		case !p.UseCustomIni:
			slog.Warn("profile has UseCustomIni=false — skipping INI push (tweaks will NOT apply; enable 'Use custom INIs' in profile settings)",
				"game", gameID, "profile", profileName)
		default:
			if _, ok := inipkg.SpecFor(gameID); !ok {
				slog.Info("no INI spec for game — skipping INI push", "game", gameID)
			} else {
				reports, err := d.iniMgr.PushToDocuments(gameID, profileName, gc.SteamAppID)
				if err != nil {
					if useTool {
						return 0, fmt.Errorf("pushing profile INIs failed: %w", err)
					}
					slog.Warn("pushing profile INIs failed", "game", gameID, "profile", profileName, "err", err)
				}
				var unverified []string
				for _, r := range reports {
					if r.Skipped {
						slog.Info("INI push skipped",
							"name", r.Filename, "target", r.TargetPath, "reason", r.Note)
						continue
					}
					slog.Info("INI pushed",
						"name", r.Filename, "target", r.TargetPath,
						"bytes", r.Bytes, "sha256", r.SHA256,
						"mtime", r.ModTime, "verified", r.Verified, "note", r.Note)
					if !r.Verified {
						unverified = append(unverified, r.Filename+": "+r.Note)
					}
				}
				if len(unverified) > 0 && useTool {
					return 0, fmt.Errorf("INI push verification failed for: %s", strings.Join(unverified, "; "))
				}
			}
		}
	}

	if profileName != "" {
		if err := d.writePluginsTxt(gameID, gc, profileName); err != nil {
			slog.Warn("writing plugins.txt failed", "game", gameID, "err", err)
		}
	}

	if useTool {
		if d.toolMgr == nil {
			return 0, fmt.Errorf("tool launch requested but tool manager is not initialized")
		}
		if drifted, verr := VerifyScriptExtenderManifest(gc.InstallPath); verr == nil && len(drifted) > 0 {
			slog.Warn("script extender manifest drift — refusing to launch",
				"game", gameID, "drifted_files", drifted)
			return 0, &ipc.LoaderMissingError{
				GameID:        gameID,
				ConfiguredExe: gc.ToolExe,
				InstallPath:   gc.InstallPath,
				Reason:        "modified",
			}
		} else if verr != nil {
			slog.Warn("could not verify script extender manifest", "err", verr)
		}
		handle, err := d.toolMgr.LaunchGame(gameID, true, &gc, d.config.PreferredProton)
		if err != nil {
			return 0, fmt.Errorf("launching via script extender: %w", err)
		}
		d.trackLaunched(gameID, handle)
		return handle.PID, nil
	}

	steamURL := fmt.Sprintf("steam://rungameid/%d", gc.SteamAppID)
	cmd := exec.Command("xdg-open", steamURL)
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("launching via Steam: %w", err)
	}
	go cmd.Wait()
	return cmd.Process.Pid, nil
}

// writePluginsTxt materializes the engine-readable plugins.txt (and
// loadorder.txt, when the engine uses one) into AppData/Local/{GameSubdir}/
func (d *Daemon) writePluginsTxt(gameID string, gc config.GameConfig, profileName string) error {
	spec, ok := plugins.SpecFor(gameID)
	if !ok {
		return nil
	}

	_, entries, err := d.profileMgr.Load(gameID, profileName)
	if err != nil {
		return fmt.Errorf("loading profile %q: %w", profileName, err)
	}

	modsDir := config.ModsDir(gameID)
	var enabled []plugins.ModEntry
	for _, e := range entries {
		if !e.Enabled {
			continue
		}
		enabled = append(enabled, plugins.ModEntry{
			Name: e.Name,
			Path: filepath.Join(modsDir, e.Name),
		})
	}

	subpath := gc.DataSubpath
	if subpath == "" {
		subpath = "Data"
	}
	baseData := filepath.Join(gc.InstallPath, subpath)
	if _, err := os.Stat(baseData + ".orig"); err == nil {
		baseData = baseData + ".orig"
	}

	list, err := plugins.DiscoverPlugins(baseData, enabled)
	if err != nil {
		return fmt.Errorf("discovering plugins: %w", err)
	}
	plugins.ApplyCanonicalOrder(list, spec)
	if userOrder, oerr := d.profileMgr.LoadPluginOrder(gameID, profileName); oerr == nil && len(userOrder) > 0 {
		plugins.ApplyUserOrder(list, spec, userOrder)
	} else if oerr != nil {
		slog.Warn("loading plugin order failed", "game", gameID, "profile", profileName, "err", oerr)
	}

	destDir, err := inipkg.AppDataLocalPath(gc.SteamAppID, spec.AppDataSubdir)
	if err != nil {
		return fmt.Errorf("resolving AppData path: %w", err)
	}

	if err := plugins.Write(spec, destDir, list); err != nil {
		return fmt.Errorf("writing plugins.txt: %w", err)
	}
	slog.Info("plugins.txt deployed",
		"game", gameID, "profile", profileName,
		"count", len(list), "dest", destDir)
	return nil
}

// GetPreferredProton returns the global Proton preference or "" for
// auto-pick. Read-only, no lock needed beyond what config already uses.
func (d *Daemon) GetPreferredProton() (string, error) {
	return d.config.PreferredProton, nil
}

// SetPreferredProton stores a global Proton path override. Empty string
// clears the preference so the daemon falls back to detection ranking.
func (d *Daemon) SetPreferredProton(path string) error {
	d.config.PreferredProton = path
	return d.config.Save()
}

func (d *Daemon) DetectProton() ([]ipc.ProtonVersionResult, error) {
	if d.toolMgr == nil {
		return nil, nil
	}
	return d.toolMgr.DetectProton()
}

func (d *Daemon) SetNexusAPIKey(ctx context.Context, apiKey string) (*ipc.NexusAPIKeyResult, error) {
	nexus := download.NewNexusClient(apiKey)
	if err := nexus.ValidateAPIKey(ctx); err != nil {
		slog.Warn("nexus API key validation failed", "err", err)
		if errors.Is(err, download.ErrInvalidKey) {
			return &ipc.NexusAPIKeyResult{Valid: false, ErrorMessage: "invalid API key"}, nil
		}
		return &ipc.NexusAPIKeyResult{Valid: false, ErrorMessage: err.Error()}, nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	d.config.NexusAPIKey = apiKey
	if err := d.config.Save(); err != nil {
		return nil, fmt.Errorf("saving config: %w", err)
	}

	if d.downloadMgr != nil {
		d.downloadMgr.Stop()
	}
	d.downloadMgr = download.NewManager(nexus, d.config, 3, d.managerHooks())
	d.downloadMgr.SetPostInstallHook(d.ensureInModList)
	d.downloadMgr.RehydrateLedger()

	slog.Info("nexus API key set and validated")
	return &ipc.NexusAPIKeyResult{Valid: true}, nil
}

// ListProfileIniFiles seeds the profile's ini directory from the game's
// current Documents/My Games INIs (only on the first call — existing profile
func (d *Daemon) ListProfileIniFiles(gameID, profileName string) (*ipc.ProfileIniListResult, error) {
	gc, err := d.config.EffectiveGameConfig(gameID)
	if err != nil {
		return nil, err
	}
	spec, hasSpec := inipkg.SpecFor(gameID)
	if !hasSpec {
		return &ipc.ProfileIniListResult{}, nil
	}
	p, _, err := d.profileMgr.Load(gameID, profileName)
	if err != nil {
		return nil, fmt.Errorf("loading profile: %w", err)
	}
	if err := d.iniMgr.SeedFromDocuments(gameID, profileName, gc.SteamAppID); err != nil {
		slog.Warn("seeding profile INIs failed", "err", err)
	}
	docs, _ := inipkg.DocumentsPath(gc.SteamAppID, spec.MyGamesSubdir)

	result := &ipc.ProfileIniListResult{
		MyGamesDir:   docs,
		UseCustomIni: p.UseCustomIni,
	}
	for _, name := range spec.Files {
		content, err := d.iniMgr.Read(gameID, profileName, name)
		if err != nil {
			slog.Warn("reading profile INI failed", "file", name, "err", err)
			continue
		}
		result.Files = append(result.Files, ipc.ProfileIniFileResult{
			Filename: name,
			Content:  content,
			DiskPath: d.iniMgr.IniPath(gameID, profileName, name),
		})
	}
	return result, nil
}

func (d *Daemon) SaveProfileIniFile(gameID, profileName, filename, content string) error {
	if _, ok := d.config.Games[gameID]; !ok {
		return fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	if err := d.iniMgr.Write(gameID, profileName, filename, content); err != nil {
		return err
	}
	p, _, err := d.profileMgr.Load(gameID, profileName)
	if err == nil && p.UseCustomIni {
		gc, gcErr := d.config.EffectiveGameConfig(gameID)
		if gcErr == nil {
			if _, pushErr := d.iniMgr.PushToDocuments(gameID, profileName, gc.SteamAppID); pushErr != nil {
				slog.Warn("pushing INI after save failed", "err", pushErr)
			}
		}
	}
	return nil
}

func (d *Daemon) SetProfileIniEnabled(gameID, profileName string, enabled bool) (*ipc.ProfileIniStatusResult, error) {
	if _, ok := d.config.Games[gameID]; !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	p, entries, err := d.profileMgr.Load(gameID, profileName)
	if err != nil {
		return nil, fmt.Errorf("loading profile: %w", err)
	}
	p.UseCustomIni = enabled
	if err := d.profileMgr.Save(p, entries); err != nil {
		return nil, fmt.Errorf("saving profile: %w", err)
	}
	return d.GetProfileIniStatus(gameID, profileName)
}

// ListIniTweaks returns the named INI presets available for the game paired
// with their current applied state against the profile's Custom.ini.
func (d *Daemon) ListIniTweaks(gameID, profileName string) ([]ipc.IniTweakStateResult, error) {
	if _, ok := d.config.Games[gameID]; !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	states, err := d.iniMgr.ListTweaks(gameID, profileName)
	if err != nil {
		return nil, err
	}
	out := make([]ipc.IniTweakStateResult, 0, len(states))
	for _, s := range states {
		out = append(out, ipc.IniTweakStateResult{
			ID:          s.ID,
			Name:        s.Name,
			Description: s.Description,
			TargetFile:  s.TargetFile,
			Enabled:     s.Enabled,
		})
	}
	return out, nil
}

// SetIniTweak toggles an INI preset on or off in the profile's Custom.ini.
func (d *Daemon) SetIniTweak(gameID, profileName, tweakID string, enabled bool) (*ipc.IniTweakStateResult, error) {
	if _, ok := d.config.Games[gameID]; !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	state, err := d.iniMgr.SetTweak(gameID, profileName, tweakID, enabled)
	if err != nil {
		return nil, err
	}
	p, _, perr := d.profileMgr.Load(gameID, profileName)
	if perr == nil && p.UseCustomIni {
		gc, gcErr := d.config.EffectiveGameConfig(gameID)
		if gcErr == nil {
			if _, err := d.iniMgr.PushToDocuments(gameID, profileName, gc.SteamAppID); err != nil {
				slog.Warn("pushing INI after tweak toggle failed", "err", err)
			}
		}
	}
	return &ipc.IniTweakStateResult{
		ID:          state.ID,
		Name:        state.Name,
		Description: state.Description,
		TargetFile:  state.TargetFile,
		Enabled:     state.Enabled,
	}, nil
}

func (d *Daemon) GetProfileIniStatus(gameID, profileName string) (*ipc.ProfileIniStatusResult, error) {
	gc, err := d.config.EffectiveGameConfig(gameID)
	if err != nil {
		return nil, err
	}
	spec, hasSpec := inipkg.SpecFor(gameID)
	result := &ipc.ProfileIniStatusResult{
		GameID:          gameID,
		ProfileName:     profileName,
		GameSupportsIni: hasSpec,
	}
	if hasSpec {
		docs, _ := inipkg.DocumentsPath(gc.SteamAppID, spec.MyGamesSubdir)
		result.MyGamesDir = docs
	}
	p, _, err := d.profileMgr.Load(gameID, profileName)
	if err == nil {
		result.UseCustomIni = p.UseCustomIni
	}
	return result, nil
}

// Shutdown signals every consumer of d.shutdownCh by closing it. Idempotent
// via sync.Once: repeated calls (e.g. SIGTERM after a frontend RPC already
func (d *Daemon) Shutdown() {
	d.shutdownOnce.Do(func() { close(d.shutdownCh) })
}

func (d *Daemon) WatchStatus() <-chan ipc.StatusEventResult {
	return d.coalescedCh
}

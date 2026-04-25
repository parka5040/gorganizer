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
// Broken out so both the startup constructor and the SetNexusAPIKey
// reinitialization share one definition.
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
			// Best-effort: post-download, emit a RowChanged so the UI
			// flips the transient row to its archive-row form. Carry the
			// snap.ID across as DownloadID — without it the frontend's
			// promotion logic in DownloadsModel::replaceFromDaemon can't
			// match the new archive row to the in-flight transient row,
			// and the user ends up with two duplicate entries (one with
			// full metadata, one with just bytes/size).
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
			// Auto-install through the canonical StartInstall path — same
			// invariants (metadata.yaml written, structured errors, etc.)
			// as a user-initiated install.
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
// manager's OnArchiveLanded hook: when per-game auto-install is on, run the
// archive through StartInstall so source_archives gets written and the
// mod appears in the tab without user input.
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

	// launched tracks every Proton process the daemon has started but not
	// yet seen exit. shutdownAll waits on each Done channel before
	// unmounting VFS — tearing a FUSE mount out from under a running
	// Bethesda engine manifests as "main menu stalls and nothing loads"
	// because mid-game asset reads silently fail. Keyed by PID; entries
	// are removed by the launch goroutine in launchAndTrack when the
	// process tree exits.
	launched   map[int]*launchedGame
	launchedMu sync.Mutex

	// pendingRecoveries records games whose Data/ is in an ambiguous
	// post-crash state (CleanupStale refused to auto-restore). The
	// frontend reads this via RestoreFromBackup-or-not flow: it learns
	// from the streamed RecoveryPending event, prompts the user, and
	// invokes RestoreFromBackup on consent. Mount/launch RPCs refuse
	// for any game with a pending entry until the user resolves it.
	pendingRecoveries   map[string]*ipc.RecoveryPendingResult
	pendingRecoveriesMu sync.Mutex

	// statusCh / coalescedCh carry VFSStatus + Info/Error events to the
	// generic WatchStatus stream. Download and install progress moved to
	// per-game buses (archiveBus, installBus) in the v2 surface.
	statusCh      chan ipc.StatusEventResult
	coalescer     *statusCoalescer
	coalescedCh   chan ipc.StatusEventResult
	coalescerDone chan struct{}
	ingesterDone  chan struct{}

	// archiveBus and installBus fan per-game progress events out to the
	// StreamArchiveEvents / StreamInstallEvents RPCs. Non-blocking publish
	// semantics (see streams.go) keep the download pipeline from stalling
	// on slow consumers.
	archiveBus *streamBus[ipc.ArchiveEventResult]
	installBus *streamBus[ipc.InstallEventResult]

	// previews caches FOMOD-aware install extractions so
	// PreviewInstall → StartInstall reuses the same unpacked tree. See
	// archives.go for the eviction policy.
	previews *previewCache

	shutdownCh chan struct{}
	mu         sync.RWMutex

	// installedArchiveCache memoizes installedArchiveMap per gameID so
	// ListArchives / BulkHide don't re-walk every mod's metadata.yaml on
	// every call. Invalidated on install, uninstall, reinstall. The value
	// carries both the owning mod folder and whether the archive was a
	// merge into a pre-existing mod (drives the "Merged" status display).
	installedArchiveCache   map[string]map[string]archiveInstall
	installedArchiveCacheMu sync.RWMutex

	// readiness tracks cold-start progress for the splash screen. The
	// frontend polls Health() while showing the splash and unlocks
	// MainWindow when GamesWarmed flips true.
	readiness   ipc.ReadinessResult
	readinessMu sync.RWMutex
}

type mountState struct {
	profileName string
}

// archiveInstall captures everything ListArchives needs to know about an
// already-installed archive: which mod folder owns it and whether the
// install was a merge into a pre-existing mod (vs. a fresh new-mod install).
type archiveInstall struct {
	Folder string
	Merged bool
}

// launchedGame captures everything we need to know about an in-flight
// Proton launch: which game it's for (so shutdown logs are specific) and
// a Done channel that closes when the Proton tree exits.
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
	}
	go d.runStatusIngest()
	go d.runStatusDrain()

	// Per-game fanout buses for the v2 per-game streaming RPCs. Decoupled
	// from the WatchStatus channel so a slow download consumer can't stall
	// install or VFS events.
	d.archiveBus = newStreamBus[ipc.ArchiveEventResult](64)
	d.installBus = newStreamBus[ipc.InstallEventResult](64)
	d.previews = newPreviewCache(15*time.Minute, 5)
	go d.runPreviewSweeper()

	// Wire the canonical install path's mods-dir lookup so the download
	// package doesn't need to import config.
	download.SetModsDirResolver(config.ModsDir)

	// Initialize download manager if API key is configured.
	if cfg.NexusAPIKey != "" {
		nexus := download.NewNexusClient(cfg.NexusAPIKey)
		d.downloadMgr = download.NewManager(nexus, cfg, 3, d.managerHooks())
		d.downloadMgr.SetPostInstallHook(d.ensureInModList)
		d.downloadMgr.RehydrateLedger()
	}

	// Create mount managers for all configured games.
	for gameID, gc := range cfg.Games {
		d.ensureMountManager(gameID, gc)
	}

	return d, nil
}

// ensureMountManager creates a MountManager for a game if one doesn't exist.
// The Overwrite mod path is computed eagerly so the mount manager can
// capture writes on Deactivate without needing a separate setter call. We
// don't materialize the directory here — Activate handles that via the
// materializer when an overwrite layer is actually present.
func (d *Daemon) ensureMountManager(gameID string, gc config.GameConfig) *vfs.MountManager {
	if mm, ok := d.mountMgrs[gameID]; ok {
		return mm
	}
	subpath := gc.DataSubpath
	if subpath == "" {
		subpath = "Data"
	}
	mm := vfs.NewMountManager(
		filepath.Join(gc.InstallPath, subpath),
		filepath.Join(config.ModsDir(gameID), "Overwrite"),
	)
	d.mountMgrs[gameID] = mm
	return mm
}

// RecoverAll runs crash recovery for all configured games. When recovery
// detects an ambiguous on-disk state (unrecognized Data alongside intact
// Data.orig), the game is recorded in pendingRecoveries and a
// RecoveryPending event is queued so the GUI can prompt the user. Mount
// and launch RPCs for that game refuse until the user responds via
// RestoreFromBackup.
func (d *Daemon) RecoverAll() {
	d.setReadinessStep("checking crash recovery", nil)
	defer d.setReadinessStep("recovery complete", func(r *ipc.ReadinessResult) { r.RecoveryDone = true })
	for gameID, mm := range d.mountMgrs {
		outcome, err := mm.RecoverIfNeeded()
		if err != nil {
			slog.Error("crash recovery failed", "game", gameID, "err", err)
			continue
		}
		if outcome.Pending == nil {
			continue
		}
		pending := &ipc.RecoveryPendingResult{
			GameID:     gameID,
			DataPath:   outcome.Pending.DataPath,
			BackupPath: outcome.Pending.BackupPath,
			Reason:     outcome.Pending.Reason,
		}
		d.pendingRecoveriesMu.Lock()
		d.pendingRecoveries[gameID] = pending
		d.pendingRecoveriesMu.Unlock()
		slog.Warn("recovery pending — refusing to mount/launch until user confirms",
			"game", gameID, "reason", pending.Reason)
		// Best-effort: queue an event for any WatchStatus subscriber that
		// is already connected at recover time. The GUI also re-checks
		// pendingRecoveries on connect, so a subscriber that hasn't
		// reached this point yet still gets the state.
		select {
		case d.statusCh <- ipc.StatusEventResult{RecoveryPending: pending}:
		default:
		}
	}
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
	d.shutdownAll()
	return nil
}

// Health returns the current cold-start readiness snapshot under the
// readiness mutex. Used by the splash screen via the Health RPC.
func (d *Daemon) Health() ipc.ReadinessResult {
	d.readinessMu.RLock()
	defer d.readinessMu.RUnlock()
	return d.readiness
}

// setReadinessStep updates the readiness state under the mutex and
// records a human-readable step string for the splash subtitle. The
// mutate function may be nil for step-only updates.
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
// have to block on them. The frontend's splash polls Health() and stays
// up until GamesWarmed flips true. A single Info("ready") event also fires
// on WatchStatus so any already-connected client wakes immediately.
func (d *Daemon) warmupAsync() {
	d.RecoverAll()

	d.setReadinessStep("detecting games", nil)
	if _, err := d.DetectInstalledGames(); err != nil {
		slog.Warn("warmup: DetectInstalledGames failed", "err", err)
	}

	// Pre-walk the configured games' mod directories so the first
	// ListMods / ListArchives RPC the frontend issues hits a warm cache.
	d.mu.RLock()
	gameIDs := make([]string, 0, len(d.config.Games))
	for id := range d.config.Games {
		gameIDs = append(gameIDs, id)
	}
	d.mu.RUnlock()
	for _, id := range gameIDs {
		d.setReadinessStep("warming "+id, nil)
		// Sweep orphan .stage-* directories under ModsDir before any other
		// pass touches the filesystem. Stages are short-lived staging dirs
		// created by download.Install while extracting → finalizing into a
		// real mod folder; if the daemon was killed mid-install (or ran
		// against a buggy build that didn't wipe them on success) they
		// persist and waste disk. Safe to remove unconditionally — they
		// are dot-prefixed (excluded from scanModsFolder) and not
		// referenced by anything except the in-flight install.
		d.sweepOrphanStageDirs(id)
		_ = d.installedArchiveMap(id)
	}

	d.setReadinessStep("ready", func(r *ipc.ReadinessResult) { r.GamesWarmed = true })
	select {
	case d.statusCh <- ipc.StatusEventResult{Info: "ready"}:
	default:
	}
}

func (d *Daemon) shutdownAll() {
	// Block unmount until every in-flight Proton launch has exited.
	// The FUSE-mounted Data/ dir is the live file source for the engine;
	// unmounting while the game still holds asset handles fails every
	// subsequent read and the user sees "splash plays, menu stalls". We
	// have to outlive the game even when the frontend has already quit.
	d.waitForLaunchedExit()

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

	// Close statusCh → ingester exits → coalescer closes → drain exits
	// → coalescedCh closes → WatchStatus gRPC handlers return.
	close(d.statusCh)
	<-d.ingesterDone
	<-d.coalescerDone
}

// trackLaunched registers a Proton process so shutdownAll can wait on it,
// and spawns the bookkeeping goroutine that removes the entry when the
// process tree exits.
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
// signaled Done. Emits a log at entry so a user staring at a journal can
// tell *why* daemon shutdown is pausing, and emits nothing on the fast
// path (no games running) so a clean quit stays terse.
func (d *Daemon) waitForLaunchedExit() {
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
		<-done
	}
	slog.Info("all launched games have exited — proceeding with shutdown")
}

// --- ipc.GameController ---

func (d *Daemon) ListConfiguredGames() ([]ipc.GameInfo, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	var games []ipc.GameInfo
	for gameID, gc := range d.config.Games {
		subpath := gc.DataSubpath
		if subpath == "" {
			subpath = "Data"
		}
		games = append(games, ipc.GameInfo{
			GameID:      gameID,
			Name:        gc.Name,
			SteamAppID:  uint32(gc.SteamAppID),
			InstallPath: gc.InstallPath,
			DataPath:    filepath.Join(gc.InstallPath, subpath),
		})
	}
	return games, nil
}

func (d *Daemon) DetectInstalledGames() ([]ipc.GameInfo, error) {
	detected, err := game.DetectInstalledGames()
	if err != nil {
		return nil, err
	}

	// Auto-configure any detected game not already in config.
	d.mu.Lock()
	for _, g := range detected {
		if _, exists := d.config.Games[g.ID]; !exists {
			d.config.Games[g.ID] = config.GameConfig{
				Name:        g.Name,
				InstallPath: g.InstallPath,
				DataSubpath: g.DataSubpath,
				SteamAppID:  int(g.SteamAppID),
			}
			d.ensureMountManager(g.ID, d.config.Games[g.ID])
			slog.Info("auto-configured detected game", "id", g.ID, "path", g.InstallPath)
		}
	}
	d.config.Save()
	d.mu.Unlock()

	var games []ipc.GameInfo
	for _, g := range detected {
		games = append(games, ipc.GameInfo{
			GameID:      g.ID,
			Name:        g.Name,
			SteamAppID:  g.SteamAppID,
			InstallPath: g.InstallPath,
			DataPath:    g.DataPath,
		})
	}
	return games, nil
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

// ListMods / GetMod / RescanMod / RenameMod / UninstallMod / ReinstallMod
// live in archives.go (the v2 ModController surface).

// --- ipc.ProfileController ---

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

	// Mirror each mod's position into its metadata.yaml as `true_index`
	// (16-char hex). The source of truth is still modlist.txt — this is
	// for the frontend so it can render a mod even when its profile-
	// specific position isn't loaded yet, and for users inspecting the
	// yaml directly.
	d.writeTrueIndexes(gameID, modEntries)

	// If the game's VFS is mounted for this profile, rebuild the layer tree
	// in-place so enable/disable toggles take effect without a remount.
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

// --- ipc.VFSController ---

func (d *Daemon) MountVFS(gameID, profileName string) (*ipc.VFSStatusResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if pending := d.recoveryPendingFor(gameID); pending != nil {
		return nil, fmt.Errorf("recovery pending for %s: %s — confirm via the GUI prompt or `gorganizerctl recover-confirm` first",
			gameID, pending.Reason)
	}

	gc, ok := d.config.Games[gameID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}

	mm := d.ensureMountManager(gameID, gc)

	// Load modlist.
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
// nil when none is registered. Used as a guard at the top of MountVFS
// and LaunchGame so we never act on a Data dir whose state we've already
// flagged as ambiguous.
func (d *Daemon) recoveryPendingFor(gameID string) *ipc.RecoveryPendingResult {
	d.pendingRecoveriesMu.Lock()
	defer d.pendingRecoveriesMu.Unlock()
	return d.pendingRecoveries[gameID]
}

// RestoreFromBackup is the IPC counterpart of the GUI "Restore from
// Data.orig" button. Invokes vfs.RestoreFromBackup on the configured
// data path, and on success clears the recovery-pending entry so
// subsequent MountVFS / LaunchGame calls proceed normally.
func (d *Daemon) RestoreFromBackup(gameID string) error {
	d.pendingRecoveriesMu.Lock()
	pending, ok := d.pendingRecoveries[gameID]
	d.pendingRecoveriesMu.Unlock()
	if !ok {
		return fmt.Errorf("no recovery pending for %s", gameID)
	}

	if err := vfs.RestoreFromBackup(pending.DataPath); err != nil {
		return fmt.Errorf("restoring %s: %w", pending.DataPath, err)
	}

	d.pendingRecoveriesMu.Lock()
	delete(d.pendingRecoveries, gameID)
	d.pendingRecoveriesMu.Unlock()

	slog.Info("restore from backup completed via user consent",
		"game", gameID, "path", pending.DataPath)
	// Notify any subscriber that the pending state has cleared. Reuses
	// VFSStatus event with mounted=false so existing UI binding flips
	// "needs recovery" off without needing a new event type.
	select {
	case d.statusCh <- ipc.StatusEventResult{Info: fmt.Sprintf("recovery resolved for %s", gameID)}:
	default:
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
		// Defense-in-depth: Save+Load already strip Overwrite, but if a
		// legacy modlist.txt slips through (older daemon writes, manual
		// edits) we must not double-add it below — Overwrite always lands
		// as the final, highest-priority layer regardless of where it
		// appeared in the entries list.
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

	// Overwrite is always-on and always last (highest priority). It's the
	// catch-all destination for files written by the running game/tools
	// during play (atomic-save sweeps, xEdit output, etc.) and the user's
	// dropbox for loose .esp/.dds/.bsa files. We append it unconditionally
	// — directory may be empty or missing, in which case it contributes
	// zero files but still serves as the write-capture target.
	owDir := filepath.Join(modsDir, profile.OverwriteModName)
	layers = append(layers, vfs.Layer{
		Name:     profile.OverwriteModName,
		RootPath: owDir,
		Enabled:  true,
	})
	return layers
}

// --- ipc.ConflictController ---

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

// v1 surface deleted — StartDownload / CancelDownload / RetryDownload /
// ListArchives / RemoveArchive / SetArchiveHidden / SetArchivesHiddenBulk /
// RefreshArchiveMetadata / PreviewInstall / StartInstall / DiscardPreview /
// UninstallMod / RenameMod / ReinstallMod (v2) live in archives.go.

// installedArchiveMap builds archive-rel-path → archiveInstall for every mod
// under ModsDir(gameID) whose metadata.yaml lists source_archives. Memoized
// per-gameID; invalidated whenever any mod's source_archives could have
// changed (install/delete/reinstall/auto-install). Returns a shared
// reference — callers must not mutate.
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
		// Still cache the empty result — probing a missing modsDir per
		// call is not free.
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

// invalidateInstalledArchiveCache drops the cached archive→mod map for a
// gameID (or all games if gameID is empty). Cheap — next ListDownloads /
// BulkHideDownloads repopulates on demand.
func (d *Daemon) invalidateInstalledArchiveCache(gameID string) {
	d.installedArchiveCacheMu.Lock()
	defer d.installedArchiveCacheMu.Unlock()
	if gameID == "" {
		d.installedArchiveCache = make(map[string]map[string]archiveInstall)
		return
	}
	delete(d.installedArchiveCache, gameID)
}

// SetArchiveHidden / SetArchivesHiddenBulk live in archives.go (v2 surface).

// ensureInModList adds a mod to every configured profile's modlist.txt if
// it isn't already present. New entries are added as disabled to match MO2
// convention and the initial `enabled: false` in the mod's metadata.yaml —
// the user explicitly toggles the checkbox to enable, at which point the
// UI syncs both files back in lockstep. Silent no-op for missing profiles.
//
// Registered as the download manager's PostInstallHook, so this is also
// where we invalidate the installed-archive cache after auto-install.
func (d *Daemon) ensureInModList(gameID, modName string) {
	d.invalidateInstalledArchiveCache(gameID)
	profiles, err := d.profileMgr.List(gameID)
	if err != nil || len(profiles) == 0 {
		// Fall back to "Default" even if List failed — worst case Save creates it.
		profiles = []*profile.Profile{{Name: "Default", GameID: gameID}}
	}
	for _, p := range profiles {
		_, entries, err := d.profileMgr.Load(gameID, p.Name)
		if err != nil {
			// Profile dir may not exist yet; fabricate a minimal one.
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
// when it does its own local extraction). It mirrors the bookkeeping the
// download manager + StartInstall do automatically:
//
//  1. Drop the installed-archive cache so the next ListArchives sees the new
//     mod and the matching Downloads row flips to INSTALLED.
//  2. Add the mod to every profile's modlist.txt (disabled, MO2 convention).
//  3. If archive_rel_path is non-empty, emit an ArchiveEvent.RowChanged so
//     the Downloads tab updates without a manual reload.
//
// Idempotent — calling twice is harmless.
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

	// Emit a synthetic install-complete event so the activity log shows
	// "Installed X (Y files)" the same way it would for a daemon-driven
	// install. Without this, the local-extract FOMOD path leaves the log
	// stuck on the last "Installing..." entry, which reads as a hang.
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

// ListOverwriteFiles walks the always-on Overwrite directory for a game,
// returning each file (and intermediate directory) with size + mtime so the
// UI can render an expandable tree with multi-select. Missing directory
// is treated as "empty" — Overwrite is created lazily by the materializer
// the first time the running game writes to it.
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
// into a fresh mod folder. Intended workflow:
//
//   1. User runs the game / xEdit / a tool that writes into Overwrite.
//   2. They want to keep that batch as a real mod (versionable, conflict-
//      visible, profile-toggleable). Right-click Overwrite → Extract.
//   3. Daemon copies-or-moves the chosen paths into ModsDir/<modName>,
//      writes a metadata.yaml, and registers it in every profile's
//      modlist.txt (disabled by default).
//
// Empty `files` means "extract everything currently in Overwrite". The
// destination is always a brand-new mod folder; collision is an error so
// the user can't accidentally clobber an existing mod.
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

	// Materialize the file list. Empty means everything; otherwise resolve
	// the user's selection (which may include directories — recurse those).
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
			// Copy. Going via os.Rename would also work cross-FS via the
			// fallback path, but the user explicitly asked for files to
			// stay in Overwrite — so always copy here.
			if err := copyFileForExtract(src, dst); err != nil {
				slog.Warn("copy for extract failed", "src", src, "dst", dst, "err", err)
				continue
			}
		} else {
			if err := os.Rename(src, dst); err != nil {
				// Cross-device rename → copy + remove. This is rare in
				// practice (Overwrite shares a filesystem with ModsDir)
				// but the materializer has no such guarantee and we want
				// to be robust to mods/Overwrite living on different
				// mounts.
				if err := copyFileForExtract(src, dst); err != nil {
					slog.Warn("move-via-copy for extract failed", "src", src, "err", err)
					continue
				}
				_ = os.Remove(src)
			}
		}
		count++
	}

	// Prune empty directories left behind in Overwrite when we moved files
	// out. WalkDir bottom-up by sorting depth-descending.
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
		// Deepest first.
		sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
		for _, d := range dirs {
			_ = os.Remove(d) // succeeds only when empty; ignored otherwise
		}
	}

	// Write a minimal metadata.yaml so the new mod is indistinguishable
	// from any other manual install. AppendSourceArchive will fill the
	// fields it cares about; a zero SourceArchiveRef is intentional —
	// Overwrite-graduated mods have no provenance archive.
	if err := download.AppendSourceArchive(
		destDir, modName,
		download.SourceArchiveRef{},
		modName /*displayName*/, "" /*category*/, "" /*version*/, "" /*modPage*/,
		paths,
	); err != nil {
		slog.Warn("ExtractOverwriteToMod: metadata write failed", "err", err)
	}

	if _, err := d.RegisterManualInstall(gameID, modName, ""); err != nil {
		slog.Warn("ExtractOverwriteToMod: RegisterManualInstall failed", "err", err)
	}

	return count, nil
}

// copyFileForExtract is the small file-copy helper used by
// ExtractOverwriteToMod when os.Rename can't be applied (cross-FS, or
// caller asked to keep the original). Preserves mode; doesn't try to
// preserve mtime — the new file's metadata reflects the install.
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
// inside ModsDir(gameID). Stages are produced by download.Install as
// pre-finalize scratch space; on a clean run they get renamed (NEW_MOD)
// or removed (MERGE_INTO) before Install returns. A leftover stage means
// either the daemon was killed mid-install or a prior buggy build forgot
// to clean up. Either way the contents are safe to discard — nothing
// outside the in-flight install ever references them, and scanModsFolder
// already skips dot-prefixed names so they don't appear in the UI.
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

// InstallDownload replaced by StartInstall (archives.go).

// runStatusIngest forwards events from the producer-facing statusCh into
// the coalescer. Exits when statusCh is closed (on shutdown), then closes
// the coalescer so the drain goroutine can finish.
func (d *Daemon) runStatusIngest() {
	defer close(d.ingesterDone)
	for evt := range d.statusCh {
		d.coalescer.Push(evt)
	}
	d.coalescer.Close()
}

// runStatusDrain pulls coalesced events into coalescedCh, which
// WatchStatus exposes. Exits when the coalescer is closed and drained,
// then closes coalescedCh so gRPC stream handlers can return.
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

// RemoveArchive / ReinstallMod(v2) live in archives.go.

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

// --- ipc.LaunchController ---

func (d *Daemon) LaunchGame(gameID string, useTool bool, profileName string) (int, error) {
	if pending := d.recoveryPendingFor(gameID); pending != nil {
		return 0, fmt.Errorf("recovery pending for %s: %s — confirm via the GUI prompt or `gorganizerctl recover-confirm` first",
			gameID, pending.Reason)
	}
	gc, ok := d.config.Games[gameID]
	if !ok {
		return 0, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}

	// Auto-mount VFS if not already mounted and we have mods.
	mm := d.ensureMountManager(gameID, gc)
	if !mm.IsMounted() && profileName != "" {
		slog.Info("auto-mounting VFS before launch", "game", gameID, "profile", profileName)
		// Use unlocked MountVFS since we're already called under various lock states.
		// MountVFS acquires its own lock.
		if _, err := d.MountVFS(gameID, profileName); err != nil {
			slog.Warn("VFS auto-mount failed, launching without mods", "game", gameID, "err", err)
			// Notify frontend.
			select {
			case d.statusCh <- ipc.StatusEventResult{Info: fmt.Sprintf("VFS mount skipped: %v", err)}:
			default:
			}
		}
	}

	// Push profile-specific INI files into the game's Documents/My Games dir
	// when the profile has opted in. Failure is non-fatal — the game still
	// launches using whatever INIs are already on disk.
	//
	// Verbose here because when tweaks don't appear to take effect (intros
	// still play, mods dormant) the first question is always "did the push
	// even run?" Log every skip path with its reason.
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
					// Hard fail for useTool launches — silently proceeding is
					// exactly what produced the "custom INIs ignored" bug.
					if useTool {
						return 0, fmt.Errorf("pushing profile INIs failed: %w", err)
					}
					slog.Warn("pushing profile INIs failed", "game", gameID, "profile", profileName, "err", err)
				}
				// Per-file verification. Every written file must round-trip
				// its size/hash; a mismatch means the Proton prefix redirected
				// the write somewhere the game won't read.
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

	// Deploy plugins.txt so the engine actually loads the enabled plugins.
	// The FUSE mount makes mod files *visible* in Data/, but Bethesda
	// engines only activate plugins listed in the per-user plugins.txt in
	// AppData/Local/{GameSubdir}/. Without this the default launcher can
	// show the mods' ESPs but the running game ignores them.
	if profileName != "" {
		if err := d.writePluginsTxt(gameID, gc, profileName); err != nil {
			slog.Warn("writing plugins.txt failed", "game", gameID, "err", err)
		}
	}

	// If useTool and tool manager is available, launch via Proton with script extender.
	// When useTool is true we *must not* silently fall back to Steam's
	// default launch on error — that path runs FalloutNVLauncher.exe
	// (or the equivalent Bethesda launcher) instead of the extender, so
	// the user ends up with the vanilla game thinking xNVSE ran. They'd
	// see splashes, no mod-aware menu, and no idea why. Surface the
	// error so the frontend can show it.
	if useTool {
		if d.toolMgr == nil {
			return 0, fmt.Errorf("tool launch requested but tool manager is not initialized")
		}
		// Drift check: after a Steam game update, extender files may have
		// been removed or an edited game exe may have desynced against the
		// loader. Manifest records the sha of every file we installed for
		// the extender; a mismatch surfaces as LoaderMissingError so the UI
		// asks the user to reinstall rather than silently launching vanilla.
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

	// useTool=false → launch via steam:// protocol (works for all games).
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
// inside the Proton prefix. Called from LaunchGame after VFS mount so the
// file reflects the merged Data/ view including all enabled mods.
func (d *Daemon) writePluginsTxt(gameID string, gc config.GameConfig, profileName string) error {
	spec, ok := plugins.SpecFor(gameID)
	if !ok {
		// Morrowind and future titles without plugins.txt fall through — not an error.
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
	// Scan from Data.orig/ when mounted (the real source); Data/ only when
	// no mount is active — that path is the FUSE mountpoint otherwise and
	// reading from it still works but is slower.
	baseData := filepath.Join(gc.InstallPath, subpath)
	if _, err := os.Stat(baseData + ".orig"); err == nil {
		baseData = baseData + ".orig"
	}

	list, err := plugins.DiscoverPlugins(baseData, enabled)
	if err != nil {
		return fmt.Errorf("discovering plugins: %w", err)
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

// --- ipc.SettingsController ---

func (d *Daemon) SetNexusAPIKey(ctx context.Context, apiKey string) (*ipc.NexusAPIKeyResult, error) {
	// Validate the key first.
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

	// Reinitialize download manager with the new key. If a previous mgr
	// was running, its in-flight context is discarded — cancel callers
	// should use CancelDownload before rotating the key.
	if d.downloadMgr != nil {
		d.downloadMgr.Stop()
	}
	d.downloadMgr = download.NewManager(nexus, d.config, 3, d.managerHooks())
	d.downloadMgr.SetPostInstallHook(d.ensureInModList)
	d.downloadMgr.RehydrateLedger()

	slog.Info("nexus API key set and validated")
	return &ipc.NexusAPIKeyResult{Valid: true}, nil
}

// --- ipc.IniController ---

// ListProfileIniFiles seeds the profile's ini directory from the game's
// current Documents/My Games INIs (only on the first call — existing profile
// copies are preserved) and returns the contents of every managed INI file.
// Games without a known INI spec yield an empty list.
func (d *Daemon) ListProfileIniFiles(gameID, profileName string) (*ipc.ProfileIniListResult, error) {
	gc, ok := d.config.Games[gameID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	spec, hasSpec := inipkg.SpecFor(gameID)
	if !hasSpec {
		return &ipc.ProfileIniListResult{}, nil
	}
	p, _, err := d.profileMgr.Load(gameID, profileName)
	if err != nil {
		return nil, fmt.Errorf("loading profile: %w", err)
	}
	// Best-effort seed. Missing source files (game never run) are fine.
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
	// If the profile's INI overlay is currently active, also push the single
	// file we just edited so subsequent game launches don't need to wait.
	p, _, err := d.profileMgr.Load(gameID, profileName)
	if err == nil && p.UseCustomIni {
		gc := d.config.Games[gameID]
		if _, err := d.iniMgr.PushToDocuments(gameID, profileName, gc.SteamAppID); err != nil {
			slog.Warn("pushing INI after save failed", "err", err)
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
// If the profile has UseCustomIni=true, the updated Custom.ini is pushed to
// the game's My Documents dir immediately.
func (d *Daemon) SetIniTweak(gameID, profileName, tweakID string, enabled bool) (*ipc.IniTweakStateResult, error) {
	if _, ok := d.config.Games[gameID]; !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	state, err := d.iniMgr.SetTweak(gameID, profileName, tweakID, enabled)
	if err != nil {
		return nil, err
	}
	// Push if the profile's INI overlay is live.
	p, _, perr := d.profileMgr.Load(gameID, profileName)
	if perr == nil && p.UseCustomIni {
		gc := d.config.Games[gameID]
		if _, err := d.iniMgr.PushToDocuments(gameID, profileName, gc.SteamAppID); err != nil {
			slog.Warn("pushing INI after tweak toggle failed", "err", err)
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
	gc, ok := d.config.Games[gameID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
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

// --- ipc.LifecycleController ---

func (d *Daemon) Shutdown() {
	select {
	case d.shutdownCh <- struct{}{}:
	default:
	}
}

func (d *Daemon) WatchStatus() <-chan ipc.StatusEventResult {
	return d.coalescedCh
}

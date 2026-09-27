package daemon

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	inipkg "github.com/parka/gorganizer/internal/ini"
	"github.com/parka/gorganizer/internal/profile"
	"github.com/parka/gorganizer/internal/tools"
	"github.com/parka/gorganizer/internal/vfs"
)

type Daemon struct {
	*session

	*GameService
	*ProfileService
	*VFSService
	*ModService
	*ArchiveService
	*InstallService
	*LaunchService
	*SettingsService
	*IniService
	*ExecutableService
	*TTWService
	*PluginStatusService
	*FNV4GBService
	*TransferService
	*ModLoaderService
	*ModDependencyService
}

// RetryDeferredRecovery attempts an immediate deferred recovery through the VFS service.
func (d *Daemon) RetryDeferredRecovery(gameID string) error {
	return d.VFSService.RetryDeferredRecovery(gameID)
}

// New creates a Daemon from configuration with all subsystems initialized.
func New(cfg *config.Config) (*Daemon, error) {
	return newWithClock(cfg, time.Now)
}

// newWithClock initializes a daemon using the supplied clock for startup recovery and launches.
func newWithClock(cfg *config.Config, now func() time.Time, scans ...func(string) (bool, error)) (*Daemon, error) {
	profileMgr := profile.NewManager(config.DataDir())
	s := &session{
		config:                  cfg,
		profileMgr:              profileMgr,
		iniMgr:                  inipkg.NewManager(profileMgr.ProfileDir),
		mountMgrs:               make(map[string]*vfs.MountManager),
		rootDeployMgrs:          make(map[string]*vfs.RootDeploymentManager),
		mountStates:             make(map[string]mountState),
		toolMgr:                 tools.NewManager(),
		lootInstaller:           tools.NewLOOTInstaller(config.ToolsDir(), nil),
		statusCh:                make(chan dto.StatusEventResult, 64),
		statusDone:              make(chan struct{}),
		startedAt:               time.Now(),
		coalescer:               newStatusCoalescer(),
		coalescedCh:             make(chan dto.StatusEventResult, 16),
		coalescerDone:           make(chan struct{}),
		ingesterDone:            make(chan struct{}),
		shutdownCh:              make(chan struct{}),
		installedArchiveCache:   make(map[string]map[string]archiveInstall),
		launched:                make(map[int]*launchedGame),
		steamLaunched:           make(map[string]bool),
		execRuns:                make(map[string]*execRun),
		installLocks:            make(map[string]*sync.Mutex),
		profileLocks:            make(map[string]*sync.Mutex),
		recoveryReady:           make(chan struct{}),
		pendingRecoveries:       make(map[string]*dto.RecoveryPendingResult),
		rootPendingRecoveries:   make(map[string]*dto.RecoveryPendingResult),
		loaderPendingRecoveries: make(map[string]*dto.RecoveryPendingResult),
		deferredRecoveries:      make(map[string]deferredRecovery),
		heldLandings:            make(map[string][]heldLanding),
		replayPending:           make(map[string]bool),
		replayRunning:           make(map[string]bool),
		gamesAtPath:             make(map[string][]string),
		nexusUsers:              nexusClientUserValidator{},
		now:                     now,
	}
	if len(scans) > 0 {
		s.procScan = scans[0]
	}
	s.svc = services{
		game:      &GameService{s: s},
		mods:      &ModService{s: s},
		archives:  &ArchiveService{s: s},
		install:   &InstallService{s: s},
		vfs:       &VFSService{s: s},
		launch:    &LaunchService{s: s},
		execs:     &ExecutableService{s: s},
		ttw:       &TTWService{s: s},
		ini:       &IniService{s: s},
		settings:  &SettingsService{s: s},
		plugins:   &PluginStatusService{s: s},
		fnv4gb:    &FNV4GBService{s: s},
		profiles:  &ProfileService{s: s},
		transfer:  &TransferService{s: s},
		modLoader: newModLoaderService(s),
		modDeps:   newModDependencyService(s),
	}
	d := &Daemon{
		session:              s,
		GameService:          s.svc.game,
		ProfileService:       s.svc.profiles,
		VFSService:           s.svc.vfs,
		ModService:           s.svc.mods,
		ArchiveService:       s.svc.archives,
		InstallService:       s.svc.install,
		LaunchService:        s.svc.launch,
		SettingsService:      s.svc.settings,
		IniService:           s.svc.ini,
		ExecutableService:    s.svc.execs,
		TTWService:           s.svc.ttw,
		PluginStatusService:  s.svc.plugins,
		FNV4GBService:        s.svc.fnv4gb,
		TransferService:      s.svc.transfer,
		ModLoaderService:     s.svc.modLoader,
		ModDependencyService: s.svc.modDeps,
	}
	if status, statusErr := s.lootInstaller.Status(); statusErr == nil && status.Installed {
		if syncErr := s.svc.execs.syncManagedLOOT(status); syncErr != nil {
			slog.Warn("could not register managed LOOT", "err", syncErr)
		}
	}
	go d.runStatusIngest()
	go d.runStatusDrain()

	d.archiveBus = newStreamBus[dto.ArchiveEventResult](64)
	d.installBus = newStreamBus[dto.InstallEventResult](64)
	d.previews = newPreviewCache(15*time.Minute, 5)
	go d.runPreviewSweeper()

	download.SetModsDirResolver(config.ModsDir)
	d.classifyStartupRecoveries()
	d.recoverInterruptedReinstalls()
	gameIDs := d.recoverableGameIDs()
	recoveredLandings := d.svc.modDeps.recoverInterruptedRequests(gameIDs)

	if cfg.NexusAPIKey != "" {
		nexus := download.NewNexusClient(cfg.NexusAPIKey)
		d.downloadMgr = download.NewManager(nexus, 3, d.managerHooks())
		d.downloadMgr.RehydrateLedger(d.configuredGameIDs())
	}

	d.mu.Lock()
	for gameID := range cfg.Games {
		gc, err := cfg.EffectiveGameConfig(gameID)
		if err != nil {
			slog.Warn("mount manager config unavailable", "game", gameID, "err", err)
			continue
		}
		d.ensureMountManager(gameID, gc)
	}
	d.mu.Unlock()
	for _, gameID := range d.configuredGameIDs() {
		if d.deferredFor(gameID, "recovery") != nil {
			if status, err := d.GetVFSStatus(gameID); err == nil {
				d.publishGuarded(dto.StatusEventResult{VFSStatus: status})
			}
		}
	}
	d.svc.modDeps.resumeRecoveredLandings(recoveredLandings)

	return d, nil
}

const (
	shutdownBackgroundWait  = 3 * time.Second
	ShutdownWatchdogTimeout = 45 * time.Second
)

var shutdownLaunchDeadline = 30 * time.Second

// Run blocks until shutdown, then tears down all subsystems; stopIPC is invoked at the point the gRPC server must stop.
func (d *Daemon) Run(stopIPC func()) error {
	d.setReadinessStep("socket bound", func(r *dto.ReadinessResult) { r.SocketReady = true })
	go d.warmupAsync()
	d.goBackground("deferred recovery", d.retryDeferredRecoveriesLoop)

	<-d.shutdownCh

	d.shutdownAll(stopIPC)
	return nil
}

func (d *Daemon) Health() dto.ReadinessResult {
	d.readinessMu.RLock()
	defer d.readinessMu.RUnlock()
	return d.readiness
}

func (s *session) setReadinessStep(step string, mutate func(*dto.ReadinessResult)) {
	s.readinessMu.Lock()
	s.readiness.LastInitStep = step
	if mutate != nil {
		mutate(&s.readiness)
	}
	s.readinessMu.Unlock()
}

// warmupAsync runs crash recovery, game detection, and per-game warmup in the background.
func (d *Daemon) warmupAsync() {
	d.RecoverAll()

	d.setReadinessStep("detecting games", nil)
	if _, err := d.DetectInstalledGames(); err != nil {
		slog.Warn("warmup: DetectInstalledGames failed", "err", err)
	}
	if err := d.ExecutableService.sweepLOOTWorkspaces(); err != nil {
		slog.Warn("warmup: interrupted LOOT workspace sweep failed", "err", err)
	}

	d.mu.RLock()
	gameIDs := make([]string, 0, len(d.config.Games))
	for id := range d.config.Games {
		gameIDs = append(gameIDs, id)
	}
	d.mu.RUnlock()
	for _, id := range gameIDs {
		d.setReadinessStep("warming "+id, nil)
		_ = d.installedArchiveMap(id)
	}

	d.setReadinessStep("ready", func(r *dto.ReadinessResult) { r.GamesWarmed = true })
	d.publishGuarded(dto.StatusEventResult{Info: "ready"})
}

// shutdownAll refuses new work, halts the download queue, cancels loader operations and waits for them and for background installs within their bounds before abandoning them, waits for launched games until shutdownLaunchDeadline after it began, deactivates idle farms, then stops the IPC server, drops unleased preview extractions and closes the status stream.
func (d *Daemon) shutdownAll(stopIPC func()) {
	started := time.Now()
	d.beginShutdown()
	d.stopDownloadManager()
	d.svc.modLoader.stopLoaderOps(d.svc.modLoader.opWaitTimeout)
	if !d.background.stop(shutdownBackgroundWait) {
		slog.Warn("background installs still running at shutdown; startup recovery resumes their dependency requests", "waited", shutdownBackgroundWait)
	}
	ctx, cancel := context.WithDeadline(context.Background(), started.Add(shutdownLaunchDeadline))
	defer cancel()
	d.waitForLaunchedExit(ctx)

	d.deactivateIdleFarms()

	if stopIPC != nil {
		stopIPC()
	}
	d.previews.discardUnleased()

	d.closeStatus()
	<-d.ingesterDone
	<-d.coalescerDone
}

// stopDownloadManager halts the download queue so no new download starts during shutdown; running downloads resume from the ledger at the next start.
func (d *Daemon) stopDownloadManager() {
	d.mu.RLock()
	manager := d.downloadMgr
	d.mu.RUnlock()
	if manager != nil {
		manager.Stop()
	}
}

// deactivateIdleFarms deactivates, under d.mu, every mounted farm and its root deployment that no launched game, tool or launch admission may still use.
func (d *Daemon) deactivateIdleFarms() {
	d.mu.Lock()
	defer d.mu.Unlock()

	for gameID, mm := range d.mountMgrs {
		if !mm.IsMounted() {
			continue
		}
		if d.deferredForLocked(gameID, "shutdown") != nil || d.exclusiveHeld(d.fenceKeyLocked(gameID)) ||
			d.teardownBusyLocked(gameID) || d.sharedHeldLocked(gameID, dto.BusyOperationLaunch, dto.BusyOperationTool) {
			slog.Warn("leaving VFS mounted on shutdown; a launch may still be using it — recovery will restore on next start",
				"game", gameID)
			continue
		}
		slog.Info("deactivating VFS on shutdown", "game", gameID)
		if rootManager, ok := d.rootDeployMgrs[gameID]; ok {
			if _, err := rootManager.Deactivate(); err != nil {
				slog.Error("game-root deactivation failed on shutdown", "game", gameID, "err", err)
				continue
			}
		}
		if err := mm.Deactivate(); err != nil {
			slog.Error("deactivation failed on shutdown", "game", gameID, "err", err)
			gc, configErr := d.config.EffectiveGameConfig(gameID)
			state := d.mountStates[gameID]
			if configErr != nil {
				slog.Error("cannot restore root deployment after shutdown deactivation failure", "game", gameID, "err", configErr)
			} else if restoreErr := d.applyRootDeployment(gameID, gc, state.profileName); restoreErr != nil {
				slog.Error("restoring root deployment after shutdown deactivation failure failed", "game", gameID, "err", restoreErr)
			}
			continue
		}
		if err := removeLaunchTicket(mm.DataPath()); err != nil {
			slog.Error("removing launch record after shutdown deactivation failed", "game", gameID, "err", err)
		}
	}
}

// waitForLaunchedExit blocks until every registered Proton launch has exited or ctx is cancelled.
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

// Shutdown closes d.shutdownCh to signal shutdown; repeated calls are no-ops.
func (d *Daemon) Shutdown() {
	d.shutdownOnce.Do(func() { close(d.shutdownCh) })
}

func (d *Daemon) WatchStatus() <-chan dto.StatusEventResult {
	return d.coalescedCh
}

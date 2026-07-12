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
}

// New creates a Daemon from configuration with all subsystems initialized.
func New(cfg *config.Config) (*Daemon, error) {
	profileMgr := profile.NewManager(config.DataDir())
	s := &session{
		config:                cfg,
		profileMgr:            profileMgr,
		iniMgr:                inipkg.NewManager(profileMgr.ProfileDir),
		mountMgrs:             make(map[string]*vfs.MountManager),
		rootDeployMgrs:        make(map[string]*vfs.RootDeploymentManager),
		mountStates:           make(map[string]mountState),
		toolMgr:               tools.NewManager(cfg),
		lootInstaller:         tools.NewLOOTInstaller(config.ToolsDir(), nil),
		statusCh:              make(chan dto.StatusEventResult, 64),
		coalescer:             newStatusCoalescer(),
		coalescedCh:           make(chan dto.StatusEventResult, 16),
		coalescerDone:         make(chan struct{}),
		ingesterDone:          make(chan struct{}),
		shutdownCh:            make(chan struct{}),
		installedArchiveCache: make(map[string]map[string]archiveInstall),
		launched:              make(map[int]*launchedGame),
		steamLaunched:         make(map[string]bool),
		execRuns:              make(map[string]*execRun),
		installLocks:          make(map[string]*sync.Mutex),
		recoveryReady:         make(chan struct{}),
		pendingRecoveries:     make(map[string]*dto.RecoveryPendingResult),
		rootPendingRecoveries: make(map[string]*dto.RecoveryPendingResult),
		gamesAtPath:           make(map[string][]string),
	}
	s.svc = services{
		game:     &GameService{s: s},
		mods:     &ModService{s: s},
		archives: &ArchiveService{s: s},
		install:  &InstallService{s: s},
		vfs:      &VFSService{s: s},
		launch:   &LaunchService{s: s},
		execs:    &ExecutableService{s: s},
		ttw:      &TTWService{s: s},
		ini:      &IniService{s: s},
		settings: &SettingsService{s: s},
		plugins:  &PluginStatusService{s: s},
		fnv4gb:   &FNV4GBService{s: s},
		profiles: &ProfileService{s: s},
		transfer: &TransferService{s: s},
	}
	d := &Daemon{
		session:             s,
		GameService:         s.svc.game,
		ProfileService:      s.svc.profiles,
		VFSService:          s.svc.vfs,
		ModService:          s.svc.mods,
		ArchiveService:      s.svc.archives,
		InstallService:      s.svc.install,
		LaunchService:       s.svc.launch,
		SettingsService:     s.svc.settings,
		IniService:          s.svc.ini,
		ExecutableService:   s.svc.execs,
		TTWService:          s.svc.ttw,
		PluginStatusService: s.svc.plugins,
		FNV4GBService:       s.svc.fnv4gb,
		TransferService:     s.svc.transfer,
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

	if cfg.NexusAPIKey != "" {
		nexus := download.NewNexusClient(cfg.NexusAPIKey)
		d.downloadMgr = download.NewManager(nexus, cfg, 3, d.managerHooks())
		d.downloadMgr.SetPostInstallHook(d.ensureInModList)
		d.downloadMgr.RehydrateLedger()
	}

	for gameID := range cfg.Games {
		gc, err := cfg.EffectiveGameConfig(gameID)
		if err != nil {
			slog.Warn("mount manager config unavailable", "game", gameID, "err", err)
			continue
		}
		d.ensureMountManager(gameID, gc)
	}

	return d, nil
}

const shutdownTimeout = 30 * time.Second

// Run blocks until shutdown, then tears down all subsystems; stopIPC is invoked at the point the gRPC server must stop.
func (d *Daemon) Run(stopIPC func()) error {
	d.setReadinessStep("socket bound", func(r *dto.ReadinessResult) { r.SocketReady = true })
	go d.warmupAsync()

	<-d.shutdownCh

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	d.shutdownAll(ctx, stopIPC)
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

// warmupAsync runs the slow cold-start tasks (crash recovery, Steam scan,
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
		d.sweepOrphanStageDirs(id)
		_ = d.installedArchiveMap(id)
	}

	d.setReadinessStep("ready", func(r *dto.ReadinessResult) { r.GamesWarmed = true })
	select {
	case d.statusCh <- dto.StatusEventResult{Info: "ready"}:
	default:
	}
}

func (d *Daemon) shutdownAll(ctx context.Context, stopIPC func()) {
	d.waitForLaunchedExit(ctx)

	d.mu.Lock()
	defer d.mu.Unlock()

	for gameID, mm := range d.mountMgrs {
		if !mm.IsMounted() {
			continue
		}
		if d.mountBusy(gameID) {
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
	}

	if stopIPC != nil {
		stopIPC()
	}

	close(d.statusCh)
	<-d.ingesterDone
	<-d.coalescerDone
}

// waitForLaunchedExit blocks until every registered Proton launch has
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

// Shutdown signals every consumer of d.shutdownCh by closing it. Idempotent
func (d *Daemon) Shutdown() {
	d.shutdownOnce.Do(func() { close(d.shutdownCh) })
}

func (d *Daemon) WatchStatus() <-chan dto.StatusEventResult {
	return d.coalescedCh
}

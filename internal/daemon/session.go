package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"log/slog"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/game"
	inipkg "github.com/parka/gorganizer/internal/ini"
	"github.com/parka/gorganizer/internal/plugins"
	"github.com/parka/gorganizer/internal/profile"
	"github.com/parka/gorganizer/internal/tools"
	"github.com/parka/gorganizer/internal/vfs"
)

type session struct {
	config         *config.Config
	profileMgr     *profile.Manager
	iniMgr         *inipkg.Manager
	mountMgrs      map[string]*vfs.MountManager
	rootDeployMgrs map[string]*vfs.RootDeploymentManager
	mountStates    map[string]mountState
	downloadMgr    *download.Manager
	toolMgr        *tools.Manager
	lootInstaller  *tools.LOOTInstaller

	launched      map[int]*launchedGame
	steamLaunched map[string]bool
	launchedMu    sync.Mutex

	execRuns     map[string]*execRun
	execRunsMu   sync.Mutex
	execLaunchMu sync.Mutex

	pendingRecoveries     map[string]*dto.RecoveryPendingResult
	rootPendingRecoveries map[string]*dto.RecoveryPendingResult
	gamesAtPath           map[string][]string
	pendingRecoveriesMu   sync.Mutex

	installLocks   map[string]*sync.Mutex
	installLocksMu sync.Mutex

	activeGameID   string
	activeGameIDMu sync.RWMutex

	statusCh      chan dto.StatusEventResult
	coalescer     *statusCoalescer
	coalescedCh   chan dto.StatusEventResult
	coalescerDone chan struct{}
	ingesterDone  chan struct{}

	archiveBus *streamBus[dto.ArchiveEventResult]
	installBus *streamBus[dto.InstallEventResult]

	previews *previewCache

	shutdownCh   chan struct{}
	shutdownOnce sync.Once
	mu           sync.RWMutex

	installedArchiveCache   map[string]map[string]archiveInstall
	installedArchiveCacheMu sync.RWMutex

	readiness   dto.ReadinessResult
	readinessMu sync.RWMutex

	recoveryReady     chan struct{}
	recoveryReadyOnce sync.Once

	pluginHeaderCache     *plugins.HeaderCache
	pluginHeaderCacheOnce sync.Once

	softDepFetcher   *plugins.SoftDepFetcher
	softDepFetcherMu sync.Mutex

	svc services
}

type services struct {
	game     *GameService
	mods     *ModService
	archives *ArchiveService
	install  *InstallService
	vfs      *VFSService
	launch   *LaunchService
	execs    *ExecutableService
	ttw      *TTWService
	ini      *IniService
	settings *SettingsService
	plugins  *PluginStatusService
	fnv4gb   *FNV4GBService
	profiles *ProfileService
	transfer *TransferService
}

type GameService struct{ s *session }

type ProfileService struct{ s *session }

type VFSService struct{ s *session }

type ModService struct{ s *session }

type ArchiveService struct{ s *session }

type InstallService struct{ s *session }

type LaunchService struct{ s *session }

type SettingsService struct{ s *session }

type IniService struct{ s *session }

type ExecutableService struct{ s *session }

type TTWService struct{ s *session }

type PluginStatusService struct{ s *session }

type FNV4GBService struct{ s *session }

type TransferService struct{ s *session }

type mountState struct {
	profileName string
}

type archiveInstall struct {
	Folder string
	Merged bool
}

type launchedGame struct {
	gameID string
	done   <-chan struct{}
}

// ensureMountManager creates a MountManager for a game if one doesn't exist.
func (s *session) ensureMountManager(gameID string, gc config.GameConfig) *vfs.MountManager {
	if mm, ok := s.mountMgrs[gameID]; ok {
		return mm
	}
	installPath := gc.InstallPath
	subpath := gc.DataSubpath
	if subpath == "" {
		subpath = "Data"
	}
	if gc.LinkedFromGameID != "" {
		if parent, ok := s.config.Games[gc.LinkedFromGameID]; ok && parent.InstallPath != "" {
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
		gameID,
	)
	s.mountMgrs[gameID] = mm
	return mm
}

func (s *session) ensureRootDeploymentManager(gameID string, gc config.GameConfig) (*vfs.RootDeploymentManager, error) {
	if manager, ok := s.rootDeployMgrs[gameID]; ok {
		return manager, nil
	}
	managerGameID := gameID
	if gc.LinkedFromGameID != "" {
		managerGameID = gc.LinkedFromGameID
		if existing, ok := s.rootDeployMgrs[managerGameID]; ok {
			s.rootDeployMgrs[gameID] = existing
			return existing, nil
		}
	}
	if gc.InstallPath == "" {
		return nil, fmt.Errorf("game %s has no install path", gameID)
	}
	subpath := gc.DataSubpath
	if subpath == "" {
		subpath = "Data"
	}
	protected := []string{
		subpath,
		seManifestFilename,
		game.TTWMarkerFilename,
		fnv4gbMarkerFilename,
	}
	if managerGameID == "morrowind" {
		protected = append(protected, "Morrowind.ini")
	}
	manager, err := vfs.NewRootDeploymentManager(vfs.RootDeploymentConfig{
		GameRoot: gc.InstallPath, GameID: managerGameID, ProtectedPaths: protected,
	})
	if err != nil {
		return nil, err
	}
	s.rootDeployMgrs[gameID] = manager
	s.rootDeployMgrs[managerGameID] = manager
	return manager, nil
}

// installLock returns the per-mod-folder mutex serializing writes to that mod.
func (s *session) installLock(gameID, modName string) *sync.Mutex {
	key := filepath.Clean(filepath.Join(config.ModsDir(gameID), modName))
	s.installLocksMu.Lock()
	defer s.installLocksMu.Unlock()
	m, ok := s.installLocks[key]
	if !ok {
		m = &sync.Mutex{}
		s.installLocks[key] = m
	}
	return m
}

// lockMods locks the install mutexes for one or more mod folders in a fixed order.
func (s *session) lockMods(gameID string, names ...string) func() {
	seen := make(map[string]bool, len(names))
	uniq := make([]string, 0, len(names))
	for _, n := range names {
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		uniq = append(uniq, n)
	}
	sort.Strings(uniq)
	locks := make([]*sync.Mutex, 0, len(uniq))
	for _, n := range uniq {
		l := s.installLock(gameID, n)
		l.Lock()
		locks = append(locks, l)
	}
	return func() {
		for i := len(locks) - 1; i >= 0; i-- {
			locks[i].Unlock()
		}
	}
}

// setSteamLaunched records/clears that a game is running via an untracked Steam launch.
func (s *session) setSteamLaunched(gameID string, active bool) {
	s.launchedMu.Lock()
	defer s.launchedMu.Unlock()
	if active {
		s.steamLaunched[gameID] = true
	} else {
		delete(s.steamLaunched, gameID)
	}
}

// mountBusy reports whether a game's farm may still have a live reader.
func (s *session) mountBusy(gameID string) bool {
	s.launchedMu.Lock()
	if s.steamLaunched[gameID] {
		s.launchedMu.Unlock()
		return true
	}
	s.launchedMu.Unlock()
	return s.trackedMountBusy(gameID)
}

func (s *session) trackedMountBusy(gameID string) bool {
	s.launchedMu.Lock()
	for _, lg := range s.launched {
		if lg.gameID == gameID {
			s.launchedMu.Unlock()
			return true
		}
	}
	s.launchedMu.Unlock()

	s.execRunsMu.Lock()
	defer s.execRunsMu.Unlock()
	for _, r := range s.execRuns {
		if r.gameID == gameID {
			return true
		}
	}
	return false
}

func (s *session) trackLaunched(gameID string, h *tools.LaunchHandle) {
	if h == nil {
		return
	}
	s.launchedMu.Lock()
	s.launched[h.PID] = &launchedGame{gameID: gameID, done: h.Done}
	s.launchedMu.Unlock()
	go func() {
		<-h.Done
		s.launchedMu.Lock()
		delete(s.launched, h.PID)
		remaining := len(s.launched)
		s.launchedMu.Unlock()
		slog.Info("launched game exited", "game", gameID, "pid", h.PID, "still_running", remaining)
	}()
}

// installedArchiveMap builds archive-rel-path → archiveInstall for every installed mod.
func (s *session) installedArchiveMap(gameID string) map[string]archiveInstall {
	s.installedArchiveCacheMu.RLock()
	if cached, ok := s.installedArchiveCache[gameID]; ok {
		s.installedArchiveCacheMu.RUnlock()
		return cached
	}
	s.installedArchiveCacheMu.RUnlock()

	modsDir := config.ModsDir(gameID)
	out := map[string]archiveInstall{}
	entries, err := os.ReadDir(modsDir)
	if err != nil {
		s.installedArchiveCacheMu.Lock()
		s.installedArchiveCache[gameID] = out
		s.installedArchiveCacheMu.Unlock()
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
	s.installedArchiveCacheMu.Lock()
	s.installedArchiveCache[gameID] = out
	s.installedArchiveCacheMu.Unlock()
	return out
}

func (s *session) invalidateInstalledArchiveCache(gameID string) {
	s.installedArchiveCacheMu.Lock()
	defer s.installedArchiveCacheMu.Unlock()
	if gameID == "" {
		s.installedArchiveCache = make(map[string]map[string]archiveInstall)
		return
	}
	delete(s.installedArchiveCache, gameID)
}

func (s *session) runStatusIngest() {
	defer close(s.ingesterDone)
	for evt := range s.statusCh {
		s.coalescer.Push(evt)
	}
	s.coalescer.Close()
}

func (s *session) runStatusDrain() {
	defer close(s.coalescerDone)
	defer close(s.coalescedCh)
	for {
		evt, ok := s.coalescer.Drain()
		if !ok {
			return
		}
		s.coalescedCh <- evt
	}
}

// emitInfo publishes a status Info line (best-effort, non-blocking).
func (s *session) emitInfo(msg string) {
	select {
	case s.statusCh <- dto.StatusEventResult{Info: msg}:
	default:
	}
}

// publishStatus is a non-blocking send to the daemon's status channel.
func (s *session) publishStatus(evt dto.StatusEventResult) {
	select {
	case s.statusCh <- evt:
	default:
		slog.Debug("status channel full, dropping plugin-status event")
	}
}

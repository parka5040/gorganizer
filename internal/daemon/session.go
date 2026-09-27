package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"log/slog"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/game"
	"github.com/parka/gorganizer/internal/gamedef"
	inipkg "github.com/parka/gorganizer/internal/ini"
	"github.com/parka/gorganizer/internal/plugins"
	"github.com/parka/gorganizer/internal/profile"
	"github.com/parka/gorganizer/internal/steam"
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

	launched        map[int]*launchedGame
	steamLaunched   map[string]bool
	steamLaunchedAt map[string]time.Time
	launchedMu      sync.Mutex
	procScan        func(dir string) (bool, error)

	execRuns     map[string]*execRun
	execRunsMu   sync.Mutex
	execLaunchMu sync.Mutex

	pendingRecoveries       map[string]*dto.RecoveryPendingResult
	rootPendingRecoveries   map[string]*dto.RecoveryPendingResult
	loaderPendingRecoveries map[string]*dto.RecoveryPendingResult
	deferredRecoveries      map[string]deferredRecovery
	heldLandings            map[string][]heldLanding
	replayPending           map[string]bool
	replayRunning           map[string]bool
	gamesAtPath             map[string][]string
	pendingRecoveriesMu     sync.Mutex

	fenceMu        sync.Mutex
	fenceExclusive map[string]fenceHolder
	fenceShared    map[string]map[uint64]fenceHolder
	nextFenceID    uint64

	installLocks   map[string]*sync.Mutex
	installLocksMu sync.Mutex

	profileLocks   map[string]*sync.Mutex
	profileLocksMu sync.Mutex

	reinstallFault         func(step string) error
	uninstallRename        func(string, string) error
	uninstallBeforeDelete  func(string)
	modChangeRematerialize func(*vfs.MountManager) error
	launchFault            func(step string) error
	steamOpener            func(url string) (int, error)
	readSteamAppState      func(string, int) (steam.AppState, error)

	activeGameID   string
	activeGameIDMu sync.RWMutex

	statusCh       chan dto.StatusEventResult
	statusClosedMu sync.Mutex
	statusClosed   bool
	statusDone     chan struct{}
	coalescer      *statusCoalescer
	coalescedCh    chan dto.StatusEventResult
	coalescerDone  chan struct{}
	ingesterDone   chan struct{}

	archiveBus *streamBus[dto.ArchiveEventResult]
	installBus *streamBus[dto.InstallEventResult]

	previews *previewCache

	shutdownCh   chan struct{}
	shutdownOnce sync.Once
	shuttingDown atomic.Bool
	startedAt    time.Time
	background   backgroundWork
	mu           sync.RWMutex

	nexusPremiumMu    sync.Mutex
	nexusPremiumCache nexusPremiumCache
	nexusUsers        nexusUserValidator
	now               func() time.Time

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
	game      *GameService
	mods      *ModService
	archives  *ArchiveService
	install   *InstallService
	vfs       *VFSService
	launch    *LaunchService
	execs     *ExecutableService
	ttw       *TTWService
	ini       *IniService
	settings  *SettingsService
	plugins   *PluginStatusService
	fnv4gb    *FNV4GBService
	profiles  *ProfileService
	transfer  *TransferService
	modLoader *ModLoaderService
	modDeps   *ModDependencyService
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

type FNV4GBService struct {
	s           *session
	patcherMu   sync.Mutex
	patcherPath string
}

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

// ensureMountManager creates a MountManager for a game if one does not exist; the caller holds s.mu for writing.
func (s *session) ensureMountManager(gameID string, gc config.GameConfig) *vfs.MountManager {
	if mm, ok := s.mountMgrs[gameID]; ok {
		return mm
	}
	installPath := s.mountInstallPath(gc)
	subpath := gc.DataSubpath
	if subpath == "" {
		subpath = "Data"
	}
	mm := vfs.NewMountManager(
		filepath.Join(installPath, subpath),
		filepath.Join(config.ModsDir(gameID), "Overwrite"),
		gameID,
	)
	s.mountMgrs[gameID] = mm
	return mm
}

// mountInstallPath returns the install path a mount manager uses for gc, preferring a configured linked parent's install path; the caller holds s.mu for reading or writing.
func (s *session) mountInstallPath(gc config.GameConfig) string {
	if gc.LinkedFromGameID != "" {
		if parent, ok := s.config.Games[gc.LinkedFromGameID]; ok && parent.InstallPath != "" {
			return parent.InstallPath
		}
	}
	return gc.InstallPath
}

// ensureRootDeploymentManager creates or shares a game-root deployment manager for gameID; the caller holds s.mu for writing.
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
	protected = append(protected, loaderProtectedRootPaths(managerGameID)...)
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

// loaderProtectedRootPaths returns the game-root paths owned by gameID's managed mod loader, which no mod may deploy over.
func loaderProtectedRootPaths(gameID string) []string {
	def, ok := gamedef.ByID(gameID)
	if !ok || def.ModLoader == nil {
		return nil
	}
	return append([]string(nil), def.ModLoader.ProtectedRootPaths...)
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

// lockProfiles locks the per-game profile mutation mutex and returns its unlock function.
func (s *session) lockProfiles(gameID string) func() {
	s.profileLocksMu.Lock()
	if s.profileLocks == nil {
		s.profileLocks = make(map[string]*sync.Mutex)
	}
	m, ok := s.profileLocks[gameID]
	if !ok {
		m = &sync.Mutex{}
		s.profileLocks[gameID] = m
	}
	s.profileLocksMu.Unlock()
	m.Lock()
	return m.Unlock
}

// setSteamLaunched records, with its time, or clears that a game is running via an untracked Steam launch.
func (s *session) setSteamLaunched(gameID string, active bool) {
	now := s.clock()
	s.launchedMu.Lock()
	defer s.launchedMu.Unlock()
	if s.steamLaunchedAt == nil {
		s.steamLaunchedAt = make(map[string]time.Time)
	}
	if active {
		s.steamLaunched[gameID] = true
		s.steamLaunchedAt[gameID] = now
	} else {
		delete(s.steamLaunched, gameID)
		delete(s.steamLaunchedAt, gameID)
	}
}

// applyBusy reports, for a caller that holds no daemon lock, whether pending changes cannot be applied to gameID's farm now.
func (s *session) applyBusy(gameID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.applyBusyLocked(gameID)
}

// applyBusyLocked reports whether a tracked launch or tool run, a game process or Steam launch on gameID's install, or a Steam launch flag younger than steamLaunchGrace forbids re-materializing its farm, clearing older flags once no such process exists; the caller holds s.mu.
func (s *session) applyBusyLocked(gameID string) bool {
	if s.trackedMountBusy(gameID) {
		return true
	}
	_, running := s.gameProcessRunningLocked(gameID)
	return running
}

// teardownBusyLocked reports whether gameID's farm may still have a reader and must not be torn down: a tracked launch or tool run, any Steam-launch flag on its install however old, or a game process or Steam launch there; it never clears a flag, and the caller holds s.mu.
func (s *session) teardownBusyLocked(gameID string) bool {
	if s.trackedMountBusy(gameID) {
		return true
	}
	key := s.fenceKeyLocked(gameID)
	games := s.gamesOnFenceKeyLocked(gameID, key)
	if flagged, _ := s.steamLaunchesAmong(games); len(flagged) > 0 {
		return true
	}
	if !filepath.IsAbs(key) {
		return false
	}
	running, err := s.processRunningIn(key, s.steamAppIDsLocked(games))
	if err != nil {
		slog.Warn("scanning processes before a farm teardown failed; trusting the launch flags", "game", gameID, "path", key, "err", err)
		return false
	}
	return running
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
		if !entry.IsDir() || entry.Name() == "Downloads" || strings.HasPrefix(entry.Name(), ".") {
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

// runStatusDrain forwards coalesced status events to the watchers, blocking while the stream is open and dropping events nobody reads once it closed.
func (s *session) runStatusDrain() {
	defer close(s.coalescerDone)
	defer close(s.coalescedCh)
	for {
		evt, ok := s.coalescer.Drain()
		if !ok {
			return
		}
		select {
		case s.coalescedCh <- evt:
		case <-s.statusDone:
			select {
			case s.coalescedCh <- evt:
			default:
			}
		}
	}
}

// emitInfo publishes a status Info line without blocking, dropping it once shutdown closed the status stream.
func (s *session) emitInfo(msg string) {
	s.publishGuarded(dto.StatusEventResult{Info: msg})
}

// publishGuarded sends a status event without blocking, dropping it once shutdown closed the status stream.
func (s *session) publishGuarded(evt dto.StatusEventResult) {
	s.statusClosedMu.Lock()
	defer s.statusClosedMu.Unlock()
	if s.statusClosed {
		return
	}
	select {
	case s.statusCh <- evt:
	default:
	}
}

// closeStatus closes the status stream once no guarded publish is in flight, so later guarded publishes are dropped.
func (s *session) closeStatus() {
	s.statusClosedMu.Lock()
	defer s.statusClosedMu.Unlock()
	if s.statusClosed {
		return
	}
	s.statusClosed = true
	close(s.statusCh)
	if s.statusDone != nil {
		close(s.statusDone)
	}
}

// publishStatus sends a status event without blocking, dropping it once shutdown closed the status stream.
func (s *session) publishStatus(evt dto.StatusEventResult) {
	s.publishGuarded(evt)
}

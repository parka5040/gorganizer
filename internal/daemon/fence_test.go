package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/smapi"
	"github.com/parka/gorganizer/internal/vfs"
)

// reserveExclusive takes the exclusive fence for gameID the way a loader operation does.
func reserveExclusive(t *testing.T, d *Daemon, gameID string) (func(), error) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.reserveExclusiveLocked(gameID, dto.BusyOperationModLoader)
}

// requireBusy fails unless err is an OperationBusyError naming operation.
func requireBusy(t *testing.T, what string, err error, operation string) {
	t.Helper()
	var busy *dto.OperationBusyError
	if !errors.As(err, &busy) || busy.Operation != operation {
		t.Fatalf("%s error = %v, want OperationBusyError(%s)", what, err, operation)
	}
}

// requireBusyHolder fails unless err is an OperationBusyError of gameID naming operation and holder.
func requireBusyHolder(t *testing.T, what string, err error, gameID, operation, holder string) {
	t.Helper()
	var busy *dto.OperationBusyError
	if !errors.As(err, &busy) || busy.GameID != gameID || busy.Operation != operation || busy.Holder != holder {
		t.Fatalf("%s error = %#v, want OperationBusyError{game %s, operation %s, holder %s}", what, err, gameID, operation, holder)
	}
}

// fakeProcesses replaces the daemon's process scan with one that reports running and err for every install.
func fakeProcesses(d *Daemon, running bool, err error) {
	d.mu.Lock()
	d.procScan = func(string) (bool, error) { return running, err }
	d.mu.Unlock()
}

// newFNVAndTTWDaemon builds an isolated daemon with Fallout New Vegas and TTW sharing one install.
func newFNVAndTTWDaemon(t *testing.T) *Daemon {
	t.Helper()
	install := filepath.Join(t.TempDir(), "Fallout New Vegas")
	if err := os.MkdirAll(filepath.Join(install, "Data"), 0755); err != nil {
		t.Fatal(err)
	}
	return newIsolatedDaemon(t, map[string]config.GameConfig{
		"falloutnv": {Name: "FNV", InstallPath: install, DataSubpath: "Data", SteamAppID: 22380},
		"ttw":       {Name: "TTW", DataSubpath: "Data", LinkedFromGameID: "falloutnv"},
	})
}

func TestFenceExclusiveAndSharedExcludeEachOther(t *testing.T) {
	d, _ := newLoaderTestDaemon(t, nil, nil)

	releaseShared, err := d.acquireShared("stardewvalley", dto.BusyOperationLaunch)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reserveExclusive(t, d, "stardewvalley")
	requireBusy(t, "exclusive while shared", err, dto.BusyOperationLaunch)
	releaseShared()

	releaseExclusive, err := reserveExclusive(t, d, "stardewvalley")
	if err != nil {
		t.Fatalf("exclusive after release: %v", err)
	}
	_, err = d.acquireShared("stardewvalley", dto.BusyOperationMount)
	requireBusy(t, "shared while exclusive", err, dto.BusyOperationModLoader)
	_, err = reserveExclusive(t, d, "stardewvalley")
	requireBusy(t, "second exclusive", err, dto.BusyOperationModLoader)
	releaseExclusive()
	if release, err := d.acquireShared("stardewvalley", dto.BusyOperationMount); err != nil {
		t.Fatalf("shared after exclusive release: %v", err)
	} else {
		release()
	}
}

func TestFenceReleaseIsIdempotent(t *testing.T) {
	d, _ := newLoaderTestDaemon(t, nil, nil)

	first, err := d.acquireShared("stardewvalley", dto.BusyOperationLaunch)
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.acquireShared("stardewvalley", dto.BusyOperationLaunch)
	if err != nil {
		t.Fatal(err)
	}
	first()
	first()
	_, err = reserveExclusive(t, d, "stardewvalley")
	requireBusy(t, "exclusive with one shared left", err, dto.BusyOperationLaunch)
	second()
	exclusive, err := reserveExclusive(t, d, "stardewvalley")
	if err != nil {
		t.Fatalf("exclusive after both releases: %v", err)
	}
	other, err := func() (func(), error) {
		exclusive()
		return reserveExclusive(t, d, "stardewvalley")
	}()
	if err != nil {
		t.Fatalf("exclusive after release: %v", err)
	}
	exclusive()
	_, err = d.acquireShared("stardewvalley", dto.BusyOperationMount)
	requireBusy(t, "shared after a stale second release", err, dto.BusyOperationModLoader)
	other()
	d.fenceMu.Lock()
	leftovers := len(d.fenceShared) + len(d.fenceExclusive)
	d.fenceMu.Unlock()
	if leftovers != 0 {
		t.Errorf("fence maps still hold %d keys after every release", leftovers)
	}
}

func TestFenceSharesOneKeyForFNVAndTTW(t *testing.T) {
	d := newFNVAndTTWDaemon(t)
	d.mu.RLock()
	fnvKey, ttwKey := d.fenceKeyLocked("falloutnv"), d.fenceKeyLocked("ttw")
	d.mu.RUnlock()
	if fnvKey != ttwKey || !filepath.IsAbs(fnvKey) {
		t.Fatalf("fence keys fnv=%q ttw=%q, want one shared install root", fnvKey, ttwKey)
	}

	release, err := d.acquireShared("ttw", dto.BusyOperationLaunch)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reserveExclusive(t, d, "falloutnv")
	requireBusy(t, "falloutnv exclusive while ttw launches", err, dto.BusyOperationLaunch)
	release()

	exclusive, err := reserveExclusive(t, d, "falloutnv")
	if err != nil {
		t.Fatal(err)
	}
	defer exclusive()
	_, err = d.acquireShared("ttw", dto.BusyOperationMount)
	requireBusy(t, "ttw mount while falloutnv is exclusive", err, dto.BusyOperationModLoader)
}

func TestFenceExclusiveRefusedWhileTheGameIsInUse(t *testing.T) {
	t.Run("mounted", func(t *testing.T) {
		d, _ := newLoaderTestDaemon(t, nil, nil)
		if _, err := d.MountVFS("stardewvalley", "Default"); err != nil {
			t.Fatalf("MountVFS: %v", err)
		}
		t.Cleanup(func() { _ = d.UnmountVFS("stardewvalley") })
		_, err := reserveExclusive(t, d, "stardewvalley")
		requireBusy(t, "exclusive while mounted", err, dto.BusyOperationMounted)
		if !strings.Contains(err.Error(), "unmount the mods first") {
			t.Errorf("mounted refusal %q does not tell the user to unmount", err)
		}
	})
	t.Run("steam launched", func(t *testing.T) {
		d, _ := newLoaderTestDaemon(t, nil, nil)
		fakeProcesses(d, true, nil)
		d.setSteamLaunched("stardewvalley", true)
		_, err := reserveExclusive(t, d, "stardewvalley")
		requireBusyHolder(t, "exclusive while launched", err, "stardewvalley", dto.BusyOperationRunning, "stardewvalley")
	})
	t.Run("sibling launched", func(t *testing.T) {
		d := newFNVAndTTWDaemon(t)
		fakeProcesses(d, true, nil)
		d.setSteamLaunched("ttw", true)
		_, err := reserveExclusive(t, d, "falloutnv")
		requireBusyHolder(t, "exclusive while the sibling runs", err, "falloutnv", dto.BusyOperationRunning, "ttw")
	})
	t.Run("started outside gorganizer", func(t *testing.T) {
		d, _ := newLoaderTestDaemon(t, nil, nil)
		fakeProcesses(d, true, nil)
		_, err := reserveExclusive(t, d, "stardewvalley")
		requireBusyHolder(t, "exclusive while an untracked process runs", err, "stardewvalley", dto.BusyOperationRunning, "stardewvalley")
	})
	t.Run("recovery pending", func(t *testing.T) {
		d, install := newLoaderTestDaemon(t, nil, nil)
		d.pendingRecoveriesMu.Lock()
		d.rootPendingRecoveries["stardewvalley"] = &dto.RecoveryPendingResult{GameID: "stardewvalley", DataPath: install, Reason: "drift"}
		d.pendingRecoveriesMu.Unlock()
		if _, err := reserveExclusive(t, d, "stardewvalley"); err == nil || !strings.Contains(err.Error(), "recovery pending") {
			t.Fatalf("exclusive while recovery pending = %v", err)
		}
	})
	t.Run("root deployment active", func(t *testing.T) {
		d, install := newLoaderTestDaemon(t, nil, nil)
		modRoot := filepath.Join(t.TempDir(), "RootMod")
		writeFixture(t, filepath.Join(modRoot, vfs.RootContentDirName, "extra-tool.sh"))
		d.mu.Lock()
		manager, err := d.ensureRootDeploymentManager("stardewvalley", d.config.Games["stardewvalley"])
		d.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Apply([]vfs.Layer{{Name: "RootMod", RootPath: modRoot, Enabled: true}}, "Default"); err != nil {
			t.Fatalf("root deployment apply: %v", err)
		}
		t.Cleanup(func() { _, _ = manager.Deactivate() })
		if _, err := os.Lstat(filepath.Join(install, "extra-tool.sh")); err != nil {
			t.Fatalf("root deployment did not link the file: %v", err)
		}
		_, err = reserveExclusive(t, d, "stardewvalley")
		requireBusy(t, "exclusive while root-deployed", err, dto.BusyOperationRootDeployment)
	})
}

func TestFenceClearsAStaleSteamLaunchOnlyWhenNoProcessRuns(t *testing.T) {
	d, install := newLoaderTestDaemon(t, nil, nil)
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	d.mu.Lock()
	d.now = func() time.Time { return now }
	d.mu.Unlock()
	var scanned []string
	d.mu.Lock()
	d.procScan = func(dir string) (bool, error) {
		scanned = append(scanned, dir)
		return false, nil
	}
	d.mu.Unlock()

	d.setSteamLaunched("stardewvalley", true)
	now = now.Add(steamLaunchGrace / 2)
	_, err := reserveExclusive(t, d, "stardewvalley")
	requireBusyHolder(t, "exclusive right after a Steam launch", err, "stardewvalley", dto.BusyOperationRunning, "stardewvalley")
	if !steamFlagOrProcess(d, "stardewvalley") {
		t.Fatal("a fresh Steam launch flag was cleared")
	}

	now = now.Add(steamLaunchGrace)
	release, err := reserveExclusive(t, d, "stardewvalley")
	if err != nil {
		t.Fatalf("exclusive after the launched game exited: %v", err)
	}
	release()
	if steamFlagOrProcess(d, "stardewvalley") {
		t.Error("the stale Steam launch flag survived a scan that found no process")
	}
	if len(scanned) == 0 || scanned[len(scanned)-1] != install {
		t.Errorf("process scans = %v, want the install root %s", scanned, install)
	}

	fakeProcesses(d, false, errors.New("no procfs"))
	d.setSteamLaunched("stardewvalley", true)
	now = now.Add(time.Hour)
	_, err = reserveExclusive(t, d, "stardewvalley")
	requireBusy(t, "exclusive when the process scan fails", err, dto.BusyOperationRunning)

	fakeProcesses(d, true, nil)
	_, err = reserveExclusive(t, d, "stardewvalley")
	requireBusy(t, "exclusive while the launched game still runs", err, dto.BusyOperationRunning)
	if !steamFlagOrProcess(d, "stardewvalley") {
		t.Error("a Steam launch flag was cleared while its game still runs")
	}
}

func TestFenceBusyErrorsNameEverySharedOperationAndItsHolder(t *testing.T) {
	d := newFNVAndTTWDaemon(t)
	for _, op := range []string{
		dto.BusyOperationLaunch, dto.BusyOperationTool, dto.BusyOperationMount, dto.BusyOperationUnmount,
		dto.BusyOperationApply, dto.BusyOperationConfigure, dto.BusyOperationScriptExtender,
		dto.BusyOperationImport, dto.BusyOperationReinstall,
	} {
		release, err := d.acquireShared("ttw", op)
		if err != nil {
			t.Fatalf("shared %s: %v", op, err)
		}
		_, err = reserveExclusive(t, d, "falloutnv")
		requireBusyHolder(t, "exclusive while ttw holds "+op, err, "falloutnv", op, "ttw")
		release()
	}
	exclusive, err := reserveExclusive(t, d, "ttw")
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.acquireShared("falloutnv", dto.BusyOperationLaunch)
	requireBusyHolder(t, "falloutnv launch while ttw's loader operation runs", err, "falloutnv", dto.BusyOperationModLoader, "ttw")
	_, err = reserveExclusive(t, d, "falloutnv")
	requireBusyHolder(t, "falloutnv loader operation while ttw's runs", err, "falloutnv", dto.BusyOperationModLoader, "ttw")
	exclusive()

	for _, holder := range []string{"ttw", "falloutnv"} {
		release, err := d.acquireShared(holder, dto.BusyOperationLaunch)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}
	_, err = reserveExclusive(t, d, "falloutnv")
	requireBusyHolder(t, "exclusive with two launch holders", err, "falloutnv", dto.BusyOperationLaunch, "falloutnv")
}

func TestFenceLaunchAdmissionBlocksTheLoader(t *testing.T) {
	engine := &fakeLoaderEngine{status: smapi.Status{State: smapi.StateNotInstalled}}
	d, _ := newLoaderTestDaemon(t, engine, &fakeLoaderArtifacts{})
	stop := errors.New("launch stopped by the test")
	var loaderErr error
	d.launchFault = func(step string) error {
		if step == "admitted" {
			_, loaderErr = d.InstallModLoader(context.Background(), "stardewvalley", false)
			return stop
		}
		return nil
	}
	if _, err := d.LaunchGame("stardewvalley", false, "Default"); !errors.Is(err, stop) {
		t.Fatalf("LaunchGame error = %v, want the test stop", err)
	}
	requireBusy(t, "loader during launch admission", loaderErr, dto.BusyOperationLaunch)
	if installs, _, _ := engine.counts(); len(installs) != 0 {
		t.Errorf("loader ran during a launch: %v", installs)
	}
	d.launchFault = nil
	release, err := reserveExclusive(t, d, "stardewvalley")
	if err != nil {
		t.Fatalf("launch left its reservation behind: %v", err)
	}
	release()
}

func TestFenceLoaderOperationBlocksSharedOperations(t *testing.T) {
	d, _ := newLoaderTestDaemon(t, nil, nil)
	release, err := reserveExclusive(t, d, "stardewvalley")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for name, call := range map[string]func() error{
		"launch":    func() error { _, err := d.LaunchGame("stardewvalley", false, "Default"); return err },
		"mount":     func() error { _, err := d.MountVFS("stardewvalley", "Default"); return err },
		"unmount":   func() error { return d.UnmountVFS("stardewvalley") },
		"configure": func() error { return d.ConfigureGame("stardewvalley", "Stardew Valley", 413150, "/elsewhere", "Mods") },
		"tool":      func() error { _, _, err := d.LaunchExecutable("stardewvalley", "missing", "Default"); return err },
		"reinstall": func() error { _, _, _, err := d.ReinstallMod("stardewvalley", "Some Mod"); return err },
		"import": func() error {
			_, err := d.ImportInstance(context.Background(), dto.ImportRequest{GameID: "stardewvalley", ArchivePath: "/missing.tar.zst"}, func(dto.TransferProgress) {})
			return err
		},
	} {
		requireBusy(t, name, call(), dto.BusyOperationModLoader)
	}
	d.mu.RLock()
	path := d.config.Games["stardewvalley"].InstallPath
	d.mu.RUnlock()
	if path == "/elsewhere" {
		t.Error("ConfigureGame changed the install path while a loader operation held the game")
	}
}

func TestFenceSkyrimOperationsAreUnaffectedByAStardewLoaderOperation(t *testing.T) {
	skyrim := t.TempDir()
	if err := os.MkdirAll(filepath.Join(skyrim, "Data"), 0755); err != nil {
		t.Fatal(err)
	}
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		"stardewvalley": {Name: "Stardew Valley", InstallPath: newStardewInstall(t), DataSubpath: "Mods", SteamAppID: 413150},
		"skyrimse":      {Name: "Skyrim Special Edition", InstallPath: skyrim, DataSubpath: "Data", SteamAppID: 489830},
	})
	release, err := reserveExclusive(t, d, "stardewvalley")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatalf("MountVFS(skyrimse) during a Stardew loader operation: %v", err)
	}
	if err := d.UnmountVFS("skyrimse"); err != nil {
		t.Fatalf("UnmountVFS(skyrimse): %v", err)
	}
}

func TestFenceConcurrentReservationsNeverOverlap(t *testing.T) {
	d, _ := newLoaderTestDaemon(t, nil, nil)
	var shared, exclusive atomic.Int64
	var violations atomic.Int64
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				if (worker+i)%3 == 0 {
					d.mu.Lock()
					release, err := d.reserveExclusiveLocked("stardewvalley", dto.BusyOperationModLoader)
					d.mu.Unlock()
					if err != nil {
						continue
					}
					if exclusive.Add(1) != 1 || shared.Load() != 0 {
						violations.Add(1)
					}
					exclusive.Add(-1)
					release()
					release()
					continue
				}
				release, err := d.acquireShared("stardewvalley", dto.BusyOperationLaunch)
				if err != nil {
					continue
				}
				shared.Add(1)
				if exclusive.Load() != 0 {
					violations.Add(1)
				}
				shared.Add(-1)
				release()
			}
		}(worker)
	}
	wg.Wait()
	if n := violations.Load(); n != 0 {
		t.Fatalf("%d reservations overlapped an exclusive holder", n)
	}
	d.fenceMu.Lock()
	leftovers := len(d.fenceShared) + len(d.fenceExclusive)
	d.fenceMu.Unlock()
	if leftovers != 0 {
		t.Errorf("fence maps still hold %d keys after the stress run", leftovers)
	}
}

// steamFlagOrProcess reports whether teardown would treat gameID's farm as busy, without clearing any flag.
func steamFlagOrProcess(d *Daemon, gameID string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.teardownBusyLocked(gameID)
}

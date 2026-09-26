package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// useProcessTable points the real process scanner at procDir for the rest of the test.
func useProcessTable(t *testing.T, procDir string) {
	t.Helper()
	previous := processTableRoot
	processTableRoot = procDir
	t.Cleanup(func() { processTableRoot = previous })
}

// staleLaunchDaemon mounts a Stardew daemon whose Steam launch flag is older than the launch grace, with the clock at now.
func staleLaunchDaemon(t *testing.T) *Daemon {
	t.Helper()
	d, _ := newLoaderTestDaemon(t, nil, nil)
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	d.mu.Lock()
	d.now = func() time.Time { return now }
	d.mu.Unlock()
	if _, err := d.MountVFS("stardewvalley", "Default"); err != nil {
		t.Fatalf("MountVFS: %v", err)
	}
	d.setSteamLaunched("stardewvalley", true)
	d.mu.Lock()
	later := now.Add(steamLaunchGrace + time.Minute)
	d.now = func() time.Time { return later }
	d.mu.Unlock()
	return d
}

// TestShutdownKeepsAFarmWhoseSteamLaunchFlagIsStale locks that teardown never trusts the process scan alone: a set Steam launch flag keeps the farm mounted however old it is.
func TestShutdownKeepsAFarmWhoseSteamLaunchFlagIsStale(t *testing.T) {
	d := staleLaunchDaemon(t)
	d.shutdownAll(nil)
	d.mu.RLock()
	mounted := d.mountMgrs["stardewvalley"].IsMounted()
	d.mu.RUnlock()
	if !mounted {
		t.Fatal("shutdown tore down a farm whose game was launched through Steam")
	}
	d.launchedMu.Lock()
	flagged := d.steamLaunched["stardewvalley"]
	d.launchedMu.Unlock()
	if !flagged {
		t.Error("the teardown check cleared the Steam launch flag")
	}
	t.Cleanup(func() {
		d.shuttingDown.Store(false)
		_ = d.UnmountVFS("stardewvalley")
	})
}

// TestApplyProceedsOnceAStaleSteamLaunchHasNoProcess locks that the same stale flag no longer blocks an Apply.
func TestApplyProceedsOnceAStaleSteamLaunchHasNoProcess(t *testing.T) {
	d := staleLaunchDaemon(t)
	t.Cleanup(func() { _ = d.UnmountVFS("stardewvalley") })
	if err := d.RebuildVFS("stardewvalley"); err != nil {
		t.Fatalf("RebuildVFS after the launch grace with no game process: %v", err)
	}
}

// TestSteamLaunchWrapperKeepsTheFarmBusy locks that a Proton-like launch whose only trace on the install is Steam's reaper keeps both teardown and Apply busy.
func TestSteamLaunchWrapperKeepsTheFarmBusy(t *testing.T) {
	d := staleLaunchDaemon(t)
	t.Cleanup(func() { _ = d.UnmountVFS("stardewvalley") })
	procDir := filepath.Join(t.TempDir(), "proc")
	base := t.TempDir()
	fakeCommandLine(t, procDir, "300", filepath.Join(base, "Proton", "files", "bin", "wine64"), filepath.Join(base, "Proton", "pfx"),
		filepath.Join(base, "Steam", "ubuntu12_32", "reaper"), "SteamLaunch", "AppId=413150", "--", filepath.Join(base, "Proton", "proton"), "waitforexitandrun")
	useProcessTable(t, procDir)

	if err := d.RebuildVFS("stardewvalley"); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("RebuildVFS under a Steam launch = %v, want a running refusal", err)
	}
	d.setSteamLaunched("stardewvalley", false)
	if !steamFlagOrProcess(d, "stardewvalley") {
		t.Fatal("teardown ignored Steam's launch wrapper")
	}
	d.mu.RLock()
	busy := d.applyBusyLocked("stardewvalley")
	d.mu.RUnlock()
	if !busy {
		t.Fatal("Apply ignored Steam's launch wrapper")
	}
}

// TestStaleFlagClearingKeepsALaunchThatRacedTheScan locks that a Steam launch recorded while the process scan runs is never cleared as stale.
func TestStaleFlagClearingKeepsALaunchThatRacedTheScan(t *testing.T) {
	d, _ := newLoaderTestDaemon(t, nil, nil)
	start := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	now := start
	d.mu.Lock()
	d.now = func() time.Time { return now }
	d.procScan = func(string) (bool, error) {
		d.setSteamLaunched("stardewvalley", true)
		return false, nil
	}
	d.mu.Unlock()
	d.setSteamLaunched("stardewvalley", true)
	now = start.Add(steamLaunchGrace + time.Minute)

	d.mu.RLock()
	d.applyBusyLocked("stardewvalley")
	d.mu.RUnlock()
	d.launchedMu.Lock()
	flagged, at := d.steamLaunched["stardewvalley"], d.steamLaunchedAt["stardewvalley"]
	d.launchedMu.Unlock()
	if !flagged || !at.Equal(now) {
		t.Fatalf("flag = %v at %v, want the launch recorded during the scan kept", flagged, at)
	}
}

// TestLauncherShimsAreOnPath locks that the package's tests run with the failing launcher shims first on PATH.
func TestLauncherShimsAreOnPath(t *testing.T) {
	first := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
	for _, name := range launcherShims {
		if _, err := os.Stat(filepath.Join(first, name)); err != nil {
			t.Errorf("launcher shim %s is not first on PATH: %v", name, err)
		}
	}
}

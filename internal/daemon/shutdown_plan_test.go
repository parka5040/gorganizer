package daemon

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/steam"
)

// newShutdownTestDaemon builds an isolated daemon with an injected idle process scan.
func newShutdownTestDaemon(t *testing.T, games map[string]config.GameConfig) *Daemon {
	t.Helper()
	isolateDaemonState(t)
	cfg := config.DefaultConfig()
	for id, gc := range games {
		cfg.Games[id] = gc
	}
	d, err := newWithClock(cfg, time.Now, func(string) (bool, error) { return false, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Shutdown)
	d.RecoverAll()
	return d
}

// TestShutdownPlanMatchesTeardown checks every retention reason against the subsequent farm teardown.
func TestShutdownPlanMatchesTeardown(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		setup  func(*testing.T, *Daemon)
	}{
		{name: "idle"},
		{name: "game process", reason: "game_running", setup: func(t *testing.T, d *Daemon) {
			d.procScan = func(string) (bool, error) { return true, nil }
		}},
		{name: "tracked launch", reason: "game_running", setup: func(t *testing.T, d *Daemon) {
			d.launchedMu.Lock()
			d.launched[7] = &launchedGame{gameID: "stardewvalley"}
			d.launchedMu.Unlock()
		}},
		{name: "tool run", reason: "tool_running", setup: func(t *testing.T, d *Daemon) {
			d.execRunsMu.Lock()
			d.execRuns["tool"] = &execRun{gameID: "stardewvalley"}
			d.execRunsMu.Unlock()
		}},
		{name: "recent Steam launch", reason: "launch_recent", setup: func(t *testing.T, d *Daemon) {
			d.setSteamLaunched("stardewvalley", true)
		}},
		{name: "stale Steam launch", reason: "game_running", setup: func(t *testing.T, d *Daemon) {
			d.setSteamLaunched("stardewvalley", true)
			d.launchedMu.Lock()
			d.steamLaunchedAt["stardewvalley"] = d.clock().Add(-steamLaunchGrace - time.Minute)
			d.launchedMu.Unlock()
		}},
		{name: "deferred recovery", reason: "recovery_deferred", setup: func(t *testing.T, d *Daemon) {
			d.mu.RLock()
			key := d.fenceKeyLocked("stardewvalley")
			d.mu.RUnlock()
			d.pendingRecoveriesMu.Lock()
			d.deferredRecoveries[key] = deferredRecovery{reason: "game is running"}
			d.pendingRecoveriesMu.Unlock()
		}},
		{name: "exclusive operation", reason: "busy", setup: func(t *testing.T, d *Daemon) {
			d.mu.RLock()
			key := d.fenceKeyLocked("stardewvalley")
			d.mu.RUnlock()
			d.fenceMu.Lock()
			if d.fenceExclusive == nil {
				d.fenceExclusive = make(map[string]fenceHolder)
			}
			d.fenceExclusive[key] = fenceHolder{op: dto.BusyOperationModLoader, gameID: "stardewvalley"}
			d.fenceMu.Unlock()
		}},
		{name: "launch admission", reason: "launch_recent", setup: func(t *testing.T, d *Daemon) {
			release, err := d.acquireShared("stardewvalley", dto.BusyOperationLaunch)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(release)
		}},
		{name: "tool admission", reason: "tool_running", setup: func(t *testing.T, d *Daemon) {
			release, err := d.acquireShared("stardewvalley", dto.BusyOperationTool)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(release)
		}},
		{name: "script extender admission", reason: "tool_running", setup: func(t *testing.T, d *Daemon) {
			release, err := d.acquireShared("stardewvalley", dto.BusyOperationScriptExtender)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(release)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			install := newStardewInstall(t)
			d := newShutdownTestDaemon(t, map[string]config.GameConfig{
				"stardewvalley": {Name: "Stardew Valley", InstallPath: install, DataSubpath: "Mods", SteamAppID: 413150},
			})
			d.procScan = func(string) (bool, error) { return false, nil }
			if _, err := d.MountVFS("stardewvalley", "Default"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				d.mu.Lock()
				if d.mountMgrs["stardewvalley"].IsMounted() {
					if err := d.mountMgrs["stardewvalley"].Deactivate(); err != nil {
						t.Errorf("cleanup farm: %v", err)
					}
				}
				d.mu.Unlock()
			})
			if tc.setup != nil {
				tc.setup(t, d)
			}
			plan := d.GetShutdownPlan()
			if len(plan) != 1 || plan[0].GameID != "stardewvalley" || plan[0].ProfileName != "Default" {
				t.Fatalf("plan = %+v, want the mounted Default profile", plan)
			}
			if plan[0].RetainedReason != tc.reason || plan[0].WillUnmount != (tc.reason == "") {
				t.Errorf("plan = %+v, want reason %q", plan[0], tc.reason)
			}
			d.deactivateIdleFarms()
			d.mu.RLock()
			mounted := d.mountMgrs["stardewvalley"].IsMounted()
			d.mu.RUnlock()
			if mounted == plan[0].WillUnmount {
				t.Errorf("plan willUnmount = %t, mounted after teardown = %t", plan[0].WillUnmount, mounted)
			}
		})
	}
}

// TestShutdownPlanDoesNotClearSteamFlags checks repeated plan queries leave stale launch flags and timestamps untouched.
func TestShutdownPlanDoesNotClearSteamFlags(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	install := newStardewInstall(t)
	d := newShutdownTestDaemon(t, map[string]config.GameConfig{
		"stardewvalley": {Name: "Stardew Valley", InstallPath: install, DataSubpath: "Mods", SteamAppID: 413150},
	})
	if _, err := d.MountVFS("stardewvalley", "Default"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.UnmountVFS("stardewvalley") })
	d.setSteamLaunched("stardewvalley", true)
	d.launchedMu.Lock()
	d.steamLaunchedAt["stardewvalley"] = d.clock().Add(-steamLaunchGrace - time.Minute)
	d.launchedMu.Unlock()
	d.launchedMu.Lock()
	at := d.steamLaunchedAt["stardewvalley"]
	d.launchedMu.Unlock()
	for i := 0; i < 2; i++ {
		plan := d.GetShutdownPlan()
		if len(plan) != 1 || plan[0].WillUnmount || plan[0].RetainedReason != "game_running" {
			t.Fatalf("plan = %+v, want a retained farm with a stale Steam flag", plan)
		}
	}
	d.launchedMu.Lock()
	flagged, timestamp := d.steamLaunched["stardewvalley"], d.steamLaunchedAt["stardewvalley"]
	d.launchedMu.Unlock()
	if !flagged || !timestamp.Equal(at) {
		t.Errorf("plan changed Steam launch flag or time: flagged=%t, at=%v; want true at %v", flagged, timestamp, at)
	}
}

// TestShutdownReportsRetainedFarms checks mixed mounted games and Steam maintenance have distinct plan outcomes.
func TestShutdownReportsRetainedFarms(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first := newStardewInstall(t)
	second := filepath.Join(t.TempDir(), "Skyrim")
	writeFixture(t, filepath.Join(second, "Data", "original.esm"))
	d := newShutdownTestDaemon(t, map[string]config.GameConfig{
		"stardewvalley": {Name: "Stardew Valley", InstallPath: first, DataSubpath: "Mods", SteamAppID: 413150},
		"skyrimse":      {Name: "Skyrim Special Edition", InstallPath: second, DataSubpath: "Data", SteamAppID: 489830},
	})
	d.procScan = func(path string) (bool, error) { return path == first, nil }
	for _, gameID := range []string{"stardewvalley", "skyrimse"} {
		if _, err := d.MountVFS(gameID, "Default"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		d.mu.Lock()
		for _, mm := range d.mountMgrs {
			if mm.IsMounted() {
				if err := mm.Deactivate(); err != nil {
					t.Errorf("cleanup farm: %v", err)
				}
			}
		}
		d.mu.Unlock()
	})
	plan := d.GetShutdownPlan()
	if len(plan) != 2 {
		t.Fatalf("plan = %+v, want both mounted games", plan)
	}
	if plan[0].GameID != "skyrimse" || !plan[0].WillUnmount || plan[0].RetainedReason != "" ||
		plan[1].GameID != "stardewvalley" || plan[1].WillUnmount || plan[1].RetainedReason != "game_running" {
		t.Errorf("plan = %+v, want only Stardew retained", plan)
	}
	d.deactivateIdleFarms()
	if !d.mountMgrs["stardewvalley"].IsMounted() || d.mountMgrs["skyrimse"].IsMounted() {
		t.Error("teardown did not retain just the running game")
	}
}

// TestShutdownReportsSteamBusy checks a Steam update retains a mounted farm when its baseline was recorded.
func TestShutdownReportsSteamBusy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	install := newStardewInstall(t)
	d := newShutdownTestDaemon(t, map[string]config.GameConfig{
		"stardewvalley": {Name: "Stardew Valley", InstallPath: install, DataSubpath: "Mods", SteamAppID: 413150},
	})
	d.procScan = func(string) (bool, error) { return false, nil }
	updating := false
	d.readSteamAppState = func(string, int) (steam.AppState, error) {
		flags := uint64(4)
		if updating {
			flags = 2
		}
		return steam.AppState{AppID: 413150, StateFlags: flags}, nil
	}
	if _, err := d.MountVFS("stardewvalley", "Default"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.UnmountVFS("stardewvalley") })
	updating = true
	plan := d.GetShutdownPlan()
	if len(plan) != 1 || plan[0].RetainedReason != "steam_busy" || plan[0].WillUnmount {
		t.Fatalf("plan = %+v, want Steam busy retention", plan)
	}
	d.deactivateIdleFarms()
	if !d.mountMgrs["stardewvalley"].IsMounted() {
		t.Fatal("teardown removed a farm while Steam was updating")
	}
}

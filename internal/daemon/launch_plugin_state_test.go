package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
)

// newPluginLaunchDaemon prepares a Skyrim profile, Proton prefix, and existing engine plugin files without a real launcher.
func newPluginLaunchDaemon(t *testing.T) (*Daemon, *steamOpenRecorder, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	d := newIsolatedDaemon(t, newSkyrimGames(t))
	prefix := filepath.Join(os.Getenv("XDG_DATA_HOME"), "Steam", "steamapps", "compatdata", "489830", "pfx")
	if err := os.MkdirAll(prefix, 0755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(prefix, "drive_c", "users", "steamuser", "AppData", "Local", "Skyrim Special Edition")
	writeFileContent(t, filepath.Join(dest, "plugins.txt"), "previous plugins\r\n")
	writeFileContent(t, filepath.Join(dest, "loadorder.txt"), "previous order\r\n")
	recorder := &steamOpenRecorder{}
	d.mu.Lock()
	d.steamOpener = recorder.open
	d.mu.Unlock()
	return d, recorder, dest
}

// blockPluginWrite makes the engine plugin destination unwritable until the test has finished.
func blockPluginWrite(t *testing.T, dest string) {
	t.Helper()
	if err := os.Chmod(dest, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dest, 0755); err != nil {
			t.Errorf("restoring plugin directory permissions: %v", err)
		}
	})
}

// requirePreviousPluginFiles checks that a failed plugin write preserved both existing engine files.
func requirePreviousPluginFiles(t *testing.T, dest string) {
	t.Helper()
	for _, tc := range []struct {
		name string
		want string
	}{
		{name: "plugins.txt", want: "previous plugins\r\n"},
		{name: "loadorder.txt", want: "previous order\r\n"},
	} {
		got, err := os.ReadFile(filepath.Join(dest, tc.name))
		if err != nil || string(got) != tc.want {
			t.Errorf("%s after failed write = %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}

// requirePluginStateFailure checks that the launch refused a plugin write with the typed error for the requested game.
func requirePluginStateFailure(t *testing.T, err error) {
	t.Helper()
	var pluginState *dto.PluginStateError
	if !errors.As(err, &pluginState) || pluginState.GameID != "skyrimse" || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("launch error = %v, want PluginStateError for skyrimse caused by an unwritable destination", err)
	}
}

// requireNoLaunchFence checks that a failed launch holds no shared game or tool reservation.
func requireNoLaunchFence(t *testing.T, d *Daemon) {
	t.Helper()
	d.mu.RLock()
	held := d.sharedHeldLocked("skyrimse", dto.BusyOperationLaunch, dto.BusyOperationTool)
	d.mu.RUnlock()
	if held {
		t.Error("failed launch left its fence reserved")
	}
}

// TestPluginWriteFailureNeverLaunches checks that failed plugin writes refuse Steam and script extenders without recording a launch or holding a fence.
func TestPluginWriteFailureNeverLaunches(t *testing.T) {
	for _, tc := range []struct {
		name    string
		useTool bool
	}{
		{name: "Steam"},
		{name: "script extender", useTool: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, recorder, dest := newPluginLaunchDaemon(t)
			blockPluginWrite(t, dest)

			_, err := d.LaunchGame("skyrimse", tc.useTool, "Default")
			requirePluginStateFailure(t, err)
			if recorder.count() != 0 {
				t.Fatalf("Steam opened %d times after a failed plugin write", recorder.count())
			}
			d.launchedMu.Lock()
			flagged := d.steamLaunched["skyrimse"]
			_, timestamped := d.steamLaunchedAt["skyrimse"]
			tracked := len(d.launched)
			d.launchedMu.Unlock()
			if flagged || timestamped || tracked != 0 {
				t.Errorf("failed launch recorded flag=%t, timestamp=%t, tracked=%d", flagged, timestamped, tracked)
			}
			requirePreviousPluginFiles(t, dest)
			requireNoLaunchFence(t, d)

			if tc.useTool {
				return
			}
			if err := os.Chmod(dest, 0755); err != nil {
				t.Fatal(err)
			}
			if _, err := d.LaunchGame("skyrimse", false, "Default"); err != nil {
				t.Fatalf("LaunchGame after fixing plugin destination: %v", err)
			}
			if recorder.count() != 1 {
				t.Errorf("Steam opened %d times after fixing plugin destination, want once", recorder.count())
			}
		})
	}
}

// TestLOOTPluginWriteFailureNeverStartsTool checks that LOOT cannot start or register a run with stale plugin files.
func TestLOOTPluginWriteFailureNeverStartsTool(t *testing.T) {
	d, recorder, dest := newPluginLaunchDaemon(t)
	blockPluginWrite(t, dest)
	lootPath := filepath.Join(config.ToolsDir(), "loot", "test", "LOOT.exe")
	writeFileContent(t, lootPath, "fixture")
	prefix := filepath.Join(os.Getenv("XDG_DATA_HOME"), "Steam", "steamapps", "compatdata", "489830", "pfx")
	writeFileContent(t, filepath.Join(prefix, ".gorganizer-prefix-runtime.json"), `{"schema_version":1,"packages":["vcrun2022"]}`)
	d.mu.Lock()
	game := d.config.Games["skyrimse"]
	game.Executables = append(game.Executables, config.Executable{
		ID: "managed-loot", Title: "LOOT", ToolID: "loot", ExePath: lootPath,
		Runner: "proton", NeedsVFSMounted: true,
	})
	d.config.Games["skyrimse"] = game
	d.mu.Unlock()

	_, runID, err := d.LaunchExecutable("skyrimse", "managed-loot", "Default")
	requirePluginStateFailure(t, err)
	if runID != "" || recorder.count() != 0 {
		t.Errorf("failed LOOT launch recorded run %q or opened Steam %d times", runID, recorder.count())
	}
	d.execRunsMu.Lock()
	runs := len(d.execRuns)
	d.execRunsMu.Unlock()
	if runs != 0 {
		t.Errorf("failed LOOT launch registered %d tool runs", runs)
	}
	requirePreviousPluginFiles(t, dest)
	requireNoLaunchFence(t, d)
	gc, ok := d.gameConfigSnapshot("skyrimse")
	if !ok {
		t.Fatal("Skyrim game config disappeared")
	}
	prefixRelease, err := d.toolMgr.ReservePrefix(&gc, 0)
	if err != nil {
		t.Fatalf("failed LOOT launch left its Proton prefix reserved: %v", err)
	}
	prefixRelease()
}

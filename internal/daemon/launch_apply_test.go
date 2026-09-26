package daemon

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
)

type steamOpenRecorder struct {
	mu   sync.Mutex
	urls []string
}

// open records a steam:// URL instead of handing it to the desktop.
func (r *steamOpenRecorder) open(url string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.urls = append(r.urls, url)
	return 4242, nil
}

// count returns how many URLs were opened.
func (r *steamOpenRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.urls)
}

// newDirtyLaunchDaemon mounts a Stardew daemon with a plain mod, flags an older Steam launch, and dirties the farm by enabling the mod.
func newDirtyLaunchDaemon(t *testing.T) (*Daemon, *steamOpenRecorder, *time.Time) {
	t.Helper()
	d, _ := newLoaderTestDaemon(t, nil, nil)
	writeFileContent(t, filepath.Join(config.ModsDir("stardewvalley"), "Plain", "Plain", "readme.txt"), "not a SMAPI mod")
	setStardewModList(t, d, "Default", map[string]bool{"Plain": false})
	recorder := &steamOpenRecorder{}
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	d.mu.Lock()
	d.steamOpener = recorder.open
	d.now = func() time.Time { return now }
	d.mu.Unlock()
	fakeProcesses(d, false, nil)
	if _, err := d.MountVFS("stardewvalley", "Default"); err != nil {
		t.Fatalf("MountVFS: %v", err)
	}
	t.Cleanup(func() { _ = d.UnmountVFS("stardewvalley") })
	d.setSteamLaunched("stardewvalley", true)
	setStardewModList(t, d, "Default", map[string]bool{"Plain": true})
	if !d.mountMgrs["stardewvalley"].IsDirty() {
		t.Fatal("enabling a mod did not dirty the mounted farm")
	}
	return d, recorder, &now
}

// TestLaunchAppliesPendingChangesOnceAnEarlierSteamLaunchEnded locks that a Steam launch flag older than the grace with no game process no longer skips the pre-launch Apply.
func TestLaunchAppliesPendingChangesOnceAnEarlierSteamLaunchEnded(t *testing.T) {
	d, recorder, now := newDirtyLaunchDaemon(t)
	*now = now.Add(steamLaunchGrace + time.Minute)

	if _, err := d.LaunchGame("stardewvalley", false, "Default"); err != nil {
		t.Fatalf("LaunchGame: %v", err)
	}
	if d.mountMgrs["stardewvalley"].IsDirty() {
		t.Fatal("the launch skipped the pending Apply after the earlier game had exited")
	}
	if recorder.count() != 1 {
		t.Fatalf("Steam opened %d times, want once", recorder.count())
	}
}

// TestLaunchRefusesADirtyFarmWhileTheGameRuns locks that pending changes are never skipped silently: a running game or a fresh launch refuses the launch.
func TestLaunchRefusesADirtyFarmWhileTheGameRuns(t *testing.T) {
	t.Run("process running", func(t *testing.T) {
		d, recorder, now := newDirtyLaunchDaemon(t)
		*now = now.Add(steamLaunchGrace + time.Minute)
		fakeProcesses(d, true, nil)
		_, err := d.LaunchGame("stardewvalley", false, "Default")
		requireGameRunning(t, "LaunchGame while the game runs", err, dto.GameRunningOperationLaunch)
		if !strings.Contains(err.Error(), "pending mod changes") {
			t.Errorf("refusal %q does not name the pending changes", err)
		}
		if recorder.count() != 0 || !d.mountMgrs["stardewvalley"].IsDirty() {
			t.Fatal("a refused launch opened Steam or applied under the running game")
		}
	})
	t.Run("launch grace", func(t *testing.T) {
		d, recorder, now := newDirtyLaunchDaemon(t)
		*now = now.Add(steamLaunchGrace / 2)
		_, err := d.LaunchGame("stardewvalley", false, "Default")
		requireGameRunning(t, "LaunchGame during the launch grace", err, dto.GameRunningOperationLaunch)
		if recorder.count() != 0 {
			t.Fatal("a launch during the grace opened Steam")
		}
	})
}

// TestRebuildVFSClearsAStaleSteamLaunchFlag locks that Apply is refused only while a game process runs or a launch is fresh.
func TestRebuildVFSClearsAStaleSteamLaunchFlag(t *testing.T) {
	d, _, now := newDirtyLaunchDaemon(t)
	requireGameRunning(t, "RebuildVFS during the launch grace", d.RebuildVFS("stardewvalley"), dto.GameRunningOperationApply)
	*now = now.Add(steamLaunchGrace + time.Minute)
	fakeProcesses(d, true, nil)
	if err := d.RebuildVFS("stardewvalley"); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("RebuildVFS while the game runs = %v, want a running refusal", err)
	}
	fakeProcesses(d, false, nil)
	if err := d.RebuildVFS("stardewvalley"); err != nil {
		t.Fatalf("RebuildVFS after the game exited: %v", err)
	}
	if steamFlagOrProcess(d, "stardewvalley") {
		t.Error("the stale Steam launch flag survived an Apply that found no game process")
	}
}

// requireGameRunning fails unless err is a GameRunningError for operation.
func requireGameRunning(t *testing.T, what string, err error, operation string) {
	t.Helper()
	var running *dto.GameRunningError
	if !errors.As(err, &running) || running.Operation != operation {
		t.Fatalf("%s error = %v, want GameRunningError(%s)", what, err, operation)
	}
}

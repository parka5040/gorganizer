package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/vfs"
)

// newRetargetDaemon prepares two isolated Skyrim profiles with different Data files and game-root links.
func newRetargetDaemon(t *testing.T) (*Daemon, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	games := newSkyrimGames(t)
	install := games["skyrimse"].InstallPath
	d := newIsolatedDaemon(t, games)
	fakeProcesses(d, false, nil)
	for _, name := range []string{"A", "B"} {
		if _, err := d.CreateProfile("skyrimse", name); err != nil {
			t.Fatal(err)
		}
		writeFixture(t, filepath.Join(config.ModsDir("skyrimse"), "Mod"+name, name+".esp"))
		writeFixture(t, filepath.Join(config.ModsDir("skyrimse"), "Mod"+name, ".gorganizer-root", "root-"+name+".txt"))
		if err := d.SetModList("skyrimse", name, []dto.ModListEntryResult{{ModName: "Mod" + name, Enabled: true}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.MountVFS("skyrimse", "A"); err != nil {
		t.Fatal(err)
	}
	return d, install
}

// requireDeployedProfile checks the farm, root links, sentinel, status, and saved mount state agree on one profile.
func requireDeployedProfile(t *testing.T, d *Daemon, install, name string) {
	t.Helper()
	other := "A"
	if name == "A" {
		other = "B"
	}
	for _, path := range []string{
		filepath.Join(install, "Data", name+".esp"),
		filepath.Join(install, "root-"+name+".txt"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected deployed file %s: %v", path, err)
		}
	}
	for _, path := range []string{
		filepath.Join(install, "Data", other+".esp"),
		filepath.Join(install, "root-"+other+".txt"),
	} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("old profile file %s remains: %v", path, err)
		}
	}
	sentinel, err := vfs.ReadSentinel(filepath.Join(install, "Data"))
	if err != nil || sentinel.ProfileName != name {
		t.Fatalf("sentinel = %+v, %v; want %s", sentinel, err, name)
	}
	status, err := d.GetVFSStatus("skyrimse")
	if err != nil || !status.Mounted || status.ProfileName != name || status.Dirty {
		t.Fatalf("VFS status = %+v, %v; want mounted clean %s", status, err, name)
	}
	d.mu.RLock()
	mountedName := d.mountStates["skyrimse"].profileName
	d.mu.RUnlock()
	if mountedName != name {
		t.Errorf("mount state profile = %q, want %s", mountedName, name)
	}
}

// TestRetargetChangesDataRootAndProfileTogether checks a profile switch deploys both surfaces and publishes the new status.
func TestRetargetChangesDataRootAndProfileTogether(t *testing.T) {
	d, install := newRetargetDaemon(t)
	status, err := d.MountVFSWithOptions("skyrimse", "B", false, true)
	if err != nil {
		t.Fatalf("retarget: %v", err)
	}
	if status.ProfileName != "B" || !status.Mounted || status.Dirty {
		t.Errorf("retarget status = %+v", status)
	}
	requireDeployedProfile(t, d, install, "B")
}

// TestRetargetFailureKeepsOldAppliedProfile checks a failed Data switch compensates the root deployment and retains A.
func TestRetargetFailureKeepsOldAppliedProfile(t *testing.T) {
	d, install := newRetargetDaemon(t)
	exchangeErr := errors.New("exchange refused")
	d.retargetData = func(_ *vfs.MountManager, _ []vfs.Layer, _ string) error { return exchangeErr }
	_, err := d.MountVFSWithOptions("skyrimse", "B", false, true)
	if !errors.Is(err, exchangeErr) {
		t.Fatalf("retarget error = %v, want exchange failure", err)
	}
	requireDeployedProfile(t, d, install, "A")
}

// TestRetargetRollbackFailureRegistersRootRecovery checks both failures survive and game-root recovery is requested.
func TestRetargetRollbackFailureRegistersRootRecovery(t *testing.T) {
	d, install := newRetargetDaemon(t)
	exchangeErr := errors.New("exchange refused")
	blocked := filepath.Join(install, "root-A.txt")
	d.retargetData = func(_ *vfs.MountManager, _ []vfs.Layer, _ string) error {
		if err := os.Mkdir(blocked, 0755); err != nil {
			return err
		}
		return exchangeErr
	}
	_, err := d.MountVFSWithOptions("skyrimse", "B", false, true)
	if !errors.Is(err, exchangeErr) || !strings.Contains(err.Error(), "restoring game-root deployment") {
		t.Fatalf("retarget error = %v, want Data and root failures", err)
	}
	status, statusErr := d.GetVFSStatus("skyrimse")
	if statusErr != nil || status.ProfileName != "A" || status.PendingRecovery == nil || status.PendingRecovery.Kind != dto.RecoveryKindGameRoot {
		t.Fatalf("status = %+v, %v, want mounted A with game-root recovery", status, statusErr)
	}
	if sentinel, readErr := vfs.ReadSentinel(filepath.Join(install, "Data")); readErr != nil || sentinel.ProfileName != "A" {
		t.Errorf("sentinel = %+v, %v, want A", sentinel, readErr)
	}
	if mount, mountErr := d.MountVFSWithOptions("skyrimse", "B", false, true); mountErr == nil || mount != nil {
		t.Errorf("retarget while recovery pending = %+v, %v", mount, mountErr)
	}
}

// TestRetargetCommittedFailureKeepsNewAppliedProfile checks a live B farm keeps B's root and status but blocks further work.
func TestRetargetCommittedFailureKeepsNewAppliedProfile(t *testing.T) {
	d, install := newRetargetDaemon(t)
	cleanupErr := errors.New("old farm cleanup refused")
	d.retargetData = func(mm *vfs.MountManager, layers []vfs.Layer, profile string) error {
		if err := mm.Retarget(layers, profile); err != nil {
			return err
		}
		return &vfs.RetargetCommittedError{Cause: cleanupErr}
	}
	_, err := d.MountVFSWithOptions("skyrimse", "B", false, true)
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("retarget error = %v, want cleanup failure", err)
	}
	requireDeployedProfile(t, d, install, "B")
	status, err := d.GetVFSStatus("skyrimse")
	if err != nil || status.PendingRecovery == nil || status.PendingRecovery.Kind != dto.RecoveryKindData || status.LifecycleState != dto.VFSLifecycleStateRecoveryPending {
		t.Fatalf("status = %+v, %v; want B with Data recovery pending", status, err)
	}
	if _, err := d.MountVFSWithOptions("skyrimse", "A", false, true); err == nil {
		t.Fatal("retarget proceeded despite pending Data recovery")
	}
	recorder := &steamOpenRecorder{}
	d.steamOpener = recorder.open
	if _, err := d.LaunchGame("skyrimse", false, "B"); err == nil || recorder.count() != 0 {
		t.Fatalf("launch during pending recovery = %v, Steam calls = %d", err, recorder.count())
	}
}

// TestRetargetCommittedRecoveryClearsBothDeployments checks confirmed recovery does not leave stale mount state or root links.
func TestRetargetCommittedRecoveryClearsBothDeployments(t *testing.T) {
	d, install := newRetargetDaemon(t)
	d.retargetData = func(mm *vfs.MountManager, layers []vfs.Layer, profile string) error {
		if err := mm.Retarget(layers, profile); err != nil {
			return err
		}
		return &vfs.RetargetCommittedError{Cause: errors.New("cleanup refused")}
	}
	if _, err := d.MountVFSWithOptions("skyrimse", "B", false, true); err == nil {
		t.Fatal("retarget succeeded despite incomplete cleanup")
	}
	status, err := d.GetVFSStatus("skyrimse")
	if err != nil || status.PendingRecovery == nil {
		t.Fatalf("status before recovery = %+v, %v", status, err)
	}
	if err := d.RestoreFromBackup("skyrimse", status.PendingRecovery.Kind, status.PendingRecovery.RecoveryID); err != nil {
		t.Fatalf("confirm recovery: %v", err)
	}
	status, err = d.GetVFSStatus("skyrimse")
	if err != nil || status.Mounted || status.ProfileName != "" || status.PendingRecovery != nil {
		t.Fatalf("status after recovery = %+v, %v, want unmounted with no recovery", status, err)
	}
	if _, err := os.Stat(filepath.Join(install, "Data", "Skyrim.esm")); err != nil {
		t.Errorf("original Data not restored: %v", err)
	}
	for _, path := range []string{filepath.Join(install, "root-A.txt"), filepath.Join(install, "root-B.txt")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("root link %s after recovery: %v", path, err)
		}
	}
	if _, err := d.MountVFS("skyrimse", "A"); err != nil {
		t.Fatalf("mount after recovery: %v", err)
	}
	requireDeployedProfile(t, d, install, "A")
}

// TestRetargetRecoveryAfterManagerResetRemovesRootLinks checks pending Data recovery also clears root deployment after restart.
func TestRetargetRecoveryAfterManagerResetRemovesRootLinks(t *testing.T) {
	d, install := newRetargetDaemon(t)
	d.retargetData = func(mm *vfs.MountManager, layers []vfs.Layer, profile string) error {
		if err := mm.Retarget(layers, profile); err != nil {
			return err
		}
		return &vfs.RetargetCommittedError{Cause: errors.New("cleanup refused")}
	}
	if _, err := d.MountVFSWithOptions("skyrimse", "B", false, true); err == nil {
		t.Fatal("retarget succeeded despite incomplete cleanup")
	}
	status, err := d.GetVFSStatus("skyrimse")
	if err != nil || status.PendingRecovery == nil {
		t.Fatalf("status before recovery = %+v, %v", status, err)
	}
	d.mu.Lock()
	d.mountMgrs["skyrimse"].ResetAfterRestore()
	d.mu.Unlock()
	if err := d.RestoreFromBackup("skyrimse", status.PendingRecovery.Kind, status.PendingRecovery.RecoveryID); err != nil {
		t.Fatalf("confirm recovery after reset: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(install, "root-B.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("root B link survived Data recovery: %v", err)
	}
	if _, err := os.Stat(filepath.Join(install, "Data", "Skyrim.esm")); err != nil {
		t.Errorf("original Data not restored: %v", err)
	}
	status, err = d.GetVFSStatus("skyrimse")
	if err != nil || status.Mounted || status.ProfileName != "" || status.PendingRecovery != nil {
		t.Fatalf("status after recovery = %+v, %v; want unmounted", status, err)
	}
}

// TestRetargetRollbackCleanupFailureRegistersDataRecovery checks a restored A farm is gated when cleanup remains unfinished.
func TestRetargetRollbackCleanupFailureRegistersDataRecovery(t *testing.T) {
	d, install := newRetargetDaemon(t)
	cleanupErr := errors.New("rollback cleanup refused")
	d.retargetData = func(_ *vfs.MountManager, _ []vfs.Layer, _ string) error {
		return &vfs.RetargetCleanupError{Cause: cleanupErr}
	}
	_, err := d.MountVFSWithOptions("skyrimse", "B", false, true)
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("retarget error = %v, want rollback cleanup failure", err)
	}
	requireDeployedProfile(t, d, install, "A")
	status, err := d.GetVFSStatus("skyrimse")
	if err != nil || status.PendingRecovery == nil || status.PendingRecovery.Kind != dto.RecoveryKindData {
		t.Fatalf("status = %+v, %v; want A with Data recovery pending", status, err)
	}
}

// TestRetargetDeferredRecoveryBlocksTheSwitch checks the same recovery gate as a new mount.
func TestRetargetDeferredRecoveryBlocksTheSwitch(t *testing.T) {
	d, install := newRetargetDaemon(t)
	d.mu.Lock()
	d.pendingRecoveriesMu.Lock()
	d.deferredRecoveries[d.fenceKeyLocked("skyrimse")] = deferredRecovery{reason: "a game process may still be running"}
	d.pendingRecoveriesMu.Unlock()
	d.mu.Unlock()
	_, err := d.MountVFSWithOptions("skyrimse", "B", false, true)
	var deferred *dto.RecoveryDeferredError
	if !errors.As(err, &deferred) {
		t.Fatalf("retarget error = %v, want RecoveryDeferredError", err)
	}
	requireDeployedProfile(t, d, install, "A")
}

// TestRetargetRefusedWhileGameRuns checks process presence, launch grace, and scan failure never change the farm.
func TestRetargetRefusedWhileGameRuns(t *testing.T) {
	for _, tc := range []struct {
		name     string
		running  bool
		scanErr  error
		launched bool
	}{
		{name: "process", running: true},
		{name: "launch grace", launched: true},
		{name: "process scan failure", scanErr: errors.New("scan failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, install := newRetargetDaemon(t)
			fakeProcesses(d, tc.running, tc.scanErr)
			if tc.launched {
				d.setSteamLaunched("skyrimse", true)
			}
			_, err := d.MountVFSWithOptions("skyrimse", "B", false, true)
			requireGameRunning(t, tc.name, err, dto.GameRunningOperationRetarget)
			requireDeployedProfile(t, d, install, "A")
		})
	}
}

// TestRetargetRefusesTrackedSiblingTool checks a tool on a shared install cannot be hidden behind another game's ID.
func TestRetargetRefusesTrackedSiblingTool(t *testing.T) {
	d, install := newRetargetDaemon(t)
	d.mu.Lock()
	d.config.Games["sibling"] = config.GameConfig{InstallPath: install, DataSubpath: "Data"}
	d.mu.Unlock()
	d.execRunsMu.Lock()
	d.execRuns["running-tool"] = &execRun{gameID: "sibling"}
	d.execRunsMu.Unlock()
	defer func() {
		d.execRunsMu.Lock()
		delete(d.execRuns, "running-tool")
		d.execRunsMu.Unlock()
	}()
	_, err := d.MountVFSWithOptions("skyrimse", "B", false, true)
	requireGameRunning(t, "tracked sibling tool", err, dto.GameRunningOperationRetarget)
	requireDeployedProfile(t, d, install, "A")
}

// TestRetargetWithoutFlagStillRefused checks legacy mounting leaves the active root links and Data unchanged.
func TestRetargetWithoutFlagStillRefused(t *testing.T) {
	d, install := newRetargetDaemon(t)
	if _, err := d.MountVFS("skyrimse", "B"); !errors.Is(err, vfs.ErrAlreadyMounted) {
		t.Fatalf("legacy mount error = %v, want ErrAlreadyMounted", err)
	}
	requireDeployedProfile(t, d, install, "A")
}

// TestRetargetRefusesPendingAdmission checks a launch reservation cannot be bypassed by a profile switch.
func TestRetargetRefusesPendingAdmission(t *testing.T) {
	d, install := newRetargetDaemon(t)
	reservation, err := d.acquireSharedOwned("skyrimse", dto.BusyOperationLaunch)
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.Release()
	_, err = d.MountVFSWithOptions("skyrimse", "B", false, true)
	requireBusyHolder(t, "retarget during admission", err, "skyrimse", dto.BusyOperationLaunch, "skyrimse")
	requireDeployedProfile(t, d, install, "A")
}

// TestLaunchRetargetsBeforeWritingProfileState checks that an unwritable plugins destination fails only after the farm has switched.
func TestLaunchRetargetsBeforeWritingProfileState(t *testing.T) {
	d, install := newRetargetDaemon(t)
	prefix := filepath.Join(os.Getenv("XDG_DATA_HOME"), "Steam", "steamapps", "compatdata", "489830", "pfx")
	if err := os.MkdirAll(prefix, 0755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(prefix, "drive_c", "users", "steamuser", "AppData", "Local", "Skyrim Special Edition")
	writeFileContent(t, filepath.Join(dest, "plugins.txt"), "original plugins\r\n")
	blockPluginWrite(t, dest)
	recorder := &steamOpenRecorder{}
	d.steamOpener = recorder.open
	_, err := d.LaunchGame("skyrimse", false, "B")
	if err == nil {
		t.Fatal("launch succeeded despite the blocked plugin destination")
	}
	requireDeployedProfile(t, d, install, "B")
	if recorder.count() != 0 {
		t.Fatal("Steam opened despite the failed profile state write")
	}
	if got, readErr := os.ReadFile(filepath.Join(dest, "plugins.txt")); readErr != nil || string(got) != "original plugins\r\n" {
		t.Errorf("plugins.txt = %q, %v", got, readErr)
	}
}

// TestLaunchNeverMixesProfiles checks the launcher sees B's farm, root links, and plugin list rather than A's.
func TestLaunchNeverMixesProfiles(t *testing.T) {
	d, install := newRetargetDaemon(t)
	prefix := filepath.Join(os.Getenv("XDG_DATA_HOME"), "Steam", "steamapps", "compatdata", "489830", "pfx")
	if err := os.MkdirAll(prefix, 0755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(prefix, "drive_c", "users", "steamuser", "AppData", "Local", "Skyrim Special Edition")
	writeFileContent(t, filepath.Join(dest, "plugins.txt"), "previous plugins\r\n")
	calls := 0
	d.steamOpener = func(string) (int, error) {
		calls++
		requireDeployedProfile(t, d, install, "B")
		body, err := os.ReadFile(filepath.Join(dest, "plugins.txt"))
		if err != nil || !strings.Contains(string(body), "B.esp") || strings.Contains(string(body), "A.esp") {
			t.Errorf("plugins.txt at launch = %q, %v; want B only", body, err)
		}
		return 1234, nil
	}
	if _, err := d.LaunchGame("skyrimse", false, "B"); err != nil {
		t.Fatalf("launch B: %v", err)
	}
	if calls != 1 {
		t.Errorf("launcher called %d times, want once", calls)
	}
}

// TestToolRetargetsBeforeLaunching checks a tool requesting B cannot start against the still-mounted A farm.
func TestToolRetargetsBeforeLaunching(t *testing.T) {
	d, install := newRetargetDaemon(t)
	d.mu.Lock()
	game := d.config.Games["skyrimse"]
	game.Executables = append(game.Executables, config.Executable{
		ID: "test-tool", Title: "Test tool", ExePath: filepath.Join(install, "nonexistent-tool"),
		Runner: "native", NeedsVFSMounted: true,
	})
	d.config.Games["skyrimse"] = game
	d.mu.Unlock()
	exchangeErr := errors.New("exchange refused")
	d.retargetData = func(_ *vfs.MountManager, _ []vfs.Layer, profile string) error {
		if profile != "B" {
			t.Errorf("tool retarget requested %q, want B", profile)
		}
		return exchangeErr
	}
	_, runID, err := d.LaunchExecutable("skyrimse", "test-tool", "B")
	if !errors.Is(err, exchangeErr) || runID != "" {
		t.Fatalf("tool launch = %q, %v, want failed retarget", runID, err)
	}
	requireDeployedProfile(t, d, install, "A")
}

// TestRetargetRacesProfileEditsSafely checks concurrent A and B saves cannot leave B's deployed farm stale.
func TestRetargetRacesProfileEditsSafely(t *testing.T) {
	d, install := newRetargetDaemon(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	d.retargetData = func(mm *vfs.MountManager, layers []vfs.Layer, profile string) error {
		close(entered)
		<-release
		return mm.Retarget(layers, profile)
	}
	retargetDone := make(chan error, 1)
	go func() {
		_, err := d.MountVFSWithOptions("skyrimse", "B", false, true)
		retargetDone <- err
	}()
	<-entered
	var wg sync.WaitGroup
	for _, name := range []string{"A", "B"} {
		name := name
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := d.SetModList("skyrimse", name, []dto.ModListEntryResult{{ModName: "Mod" + name, Enabled: false}}); err != nil {
				t.Errorf("SetModList(%s): %v", name, err)
			}
		}()
	}
	time.Sleep(100 * time.Millisecond)
	for _, name := range []string{"A", "B"} {
		_, entries, err := d.profileMgr.Load("skyrimse", name)
		if err != nil || len(entries) != 1 || !entries[0].Enabled {
			t.Errorf("profile %s changed during the retarget: %+v, %v", name, entries, err)
		}
	}
	close(release)
	if err := <-retargetDone; err != nil {
		t.Errorf("retarget: %v", err)
	}
	wg.Wait()
	if err := d.RebuildVFS("skyrimse"); err != nil {
		t.Fatalf("apply final saved modlist: %v", err)
	}
	entries, err := d.GetModList("skyrimse", "B")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Enabled {
		t.Fatalf("saved B list = %+v, want one disabled mod", entries)
	}
	if _, err := os.Lstat(filepath.Join(install, "Data", "B.esp")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("disabled B plugin deployed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(install, "root-B.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("disabled B root file deployed: %v", err)
	}
	sentinel, err := vfs.ReadSentinel(filepath.Join(install, "Data"))
	if err != nil || sentinel.ProfileName != "B" {
		t.Fatalf("sentinel = %+v, %v, want B", sentinel, err)
	}
}

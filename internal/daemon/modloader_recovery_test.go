package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/smapi"
	"github.com/parka/gorganizer/internal/vfs"
)

// writeLoaderIntent journals an interrupted install that placed rel with the given identity.
func writeLoaderIntent(t *testing.T, gameDir, rel string, placed smapi.FileID) {
	t.Helper()
	opID := uuid.NewString()
	intent := smapi.Intent{
		SchemaVersion: 1, OpID: opID, Op: smapi.OpInstall, Phase: smapi.PhasePlacing,
		WorkDir: smapi.WorkDir + "/" + opID, Place: []string{rel}, Remove: []string{},
		Existing: map[string]smapi.FileID{}, Placed: map[string]smapi.FileID{rel: placed},
		CreatedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gameDir, smapi.IntentFile), data, 0644); err != nil {
		t.Fatal(err)
	}
}

// fileIdentity returns the device and inode of path.
func fileIdentity(t *testing.T, path string) smapi.FileID {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Lstat(path, &st); err != nil {
		t.Fatal(err)
	}
	return smapi.FileID{Dev: uint64(st.Dev), Ino: st.Ino}
}

// newStardewGames returns a Stardew-only game configuration rooted at install.
func newStardewGames(install string) map[string]config.GameConfig {
	return map[string]config.GameConfig{
		"stardewvalley": {Name: "Stardew Valley", InstallPath: install, DataSubpath: "Mods", SteamAppID: 413150},
	}
}

func TestStartupRollsBackAnInterruptedLoaderTransaction(t *testing.T) {
	install := newStardewInstall(t)
	placed := filepath.Join(install, "StardewModdingAPI")
	writeFixture(t, placed)
	writeLoaderIntent(t, install, "StardewModdingAPI", fileIdentity(t, placed))

	d := newIsolatedDaemon(t, newStardewGames(install))

	for _, path := range []string{placed, filepath.Join(install, smapi.IntentFile)} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived startup recovery: %v", filepath.Base(path), err)
		}
	}
	if pending := d.recoveryPendingFor("stardewvalley"); pending != nil {
		t.Errorf("clean rollback left a pending recovery: %+v", pending)
	}
	spec, _, _ := loaderSpecFor("stardewvalley")
	if status, err := smapi.Inspect(install, spec); err != nil || status.State != smapi.StateNotInstalled {
		t.Errorf("state after recovery = %v (%v), want not installed", status.State, err)
	}
	for _, name := range []string{"StardewValley", "Stardew Valley"} {
		if _, err := os.Lstat(filepath.Join(install, name)); err != nil {
			t.Errorf("recovery touched game file %s: %v", name, err)
		}
	}
}

func TestFailedLoaderRecoveryBlocksMountLaunchAndLoaderUntilConfirmed(t *testing.T) {
	install := newStardewInstall(t)
	foreign := filepath.Join(install, "StardewModdingAPI")
	writeFixture(t, foreign)
	writeLoaderIntent(t, install, "StardewModdingAPI", smapi.FileID{Dev: 1, Ino: 1})

	d := newIsolatedDaemon(t, newStardewGames(install))

	pending := d.recoveryPendingFor("stardewvalley")
	if pending == nil {
		t.Fatal("failed loader recovery registered no pending entry")
	}
	want := dto.RecoveryPendingResult{GameID: "stardewvalley", DataPath: install, BackupPath: filepath.Join(install, smapi.BackupDir)}
	if pending.GameID != want.GameID || pending.DataPath != want.DataPath || pending.BackupPath != want.BackupPath || !strings.HasPrefix(pending.Reason, loaderRecoveryPrefix) {
		t.Fatalf("pending = %+v, want %+v with a %q reason", pending, want, loaderRecoveryPrefix)
	}
	if _, err := d.MountVFS("stardewvalley", "Default"); err == nil || !strings.Contains(err.Error(), "recovery pending") {
		t.Errorf("MountVFS while loader recovery pending = %v", err)
	}
	if _, err := d.LaunchGame("stardewvalley", false, "Default"); err == nil || !strings.Contains(err.Error(), "recovery pending") {
		t.Errorf("LaunchGame while loader recovery pending = %v", err)
	}
	for name, call := range map[string]func() error{
		"install":   func() error { _, err := d.InstallModLoader(t.Context(), "stardewvalley", false); return err },
		"uninstall": func() error { _, err := d.UninstallModLoader(t.Context(), "stardewvalley"); return err },
		"rollback":  func() error { _, err := d.RollbackModLoader(t.Context(), "stardewvalley"); return err },
	} {
		if err := call(); err == nil || !strings.Contains(err.Error(), "recovery pending") {
			t.Errorf("%s while loader recovery pending = %v", name, err)
		}
	}
	if err := d.RestoreFromBackup("stardewvalley", dto.RecoveryKindUnspecified, ""); err == nil {
		t.Fatal("RestoreFromBackup succeeded while the fault remains")
	}
	if d.recoveryPendingFor("stardewvalley") == nil {
		t.Fatal("a failed retry cleared the pending entry")
	}

	if err := os.Remove(foreign); err != nil {
		t.Fatal(err)
	}
	if err := d.RestoreFromBackup("stardewvalley", dto.RecoveryKindUnspecified, ""); err != nil {
		t.Fatalf("RestoreFromBackup after removing the fault: %v", err)
	}
	if pending := d.recoveryPendingFor("stardewvalley"); pending != nil {
		t.Fatalf("pending entry survived a successful retry: %+v", pending)
	}
	if _, err := os.Lstat(filepath.Join(install, smapi.IntentFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("intent survived the confirmed recovery: %v", err)
	}
	if _, err := d.MountVFS("stardewvalley", "Default"); err != nil {
		t.Fatalf("MountVFS after recovery: %v", err)
	}
	if err := d.UnmountVFS("stardewvalley"); err != nil {
		t.Fatalf("UnmountVFS: %v", err)
	}
}

func TestStartupLoaderRecoveryIgnoresCleanAndMissingInstalls(t *testing.T) {
	for _, tc := range []struct {
		name     string
		install  func(t *testing.T) string
		leftover bool
	}{
		{name: "missing install", install: func(t *testing.T) string { return filepath.Join(t.TempDir(), "gone", "Stardew Valley") }},
		{name: "clean install", install: newStardewInstall, leftover: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := &fakeLoaderEngine{}
			install := tc.install(t)
			isolateDaemonState(t)
			cfg := config.DefaultConfig()
			cfg.Games["stardewvalley"] = newStardewGames(install)["stardewvalley"]
			d, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(d.Shutdown)
			d.svc.modLoader.engineFor = func(smapi.LoaderSpec) loaderEngine { return engine }
			d.RecoverAll()
			if _, _, recovers := engine.counts(); recovers != 0 {
				t.Errorf("startup ran %d loader recoveries on a %s", recovers, tc.name)
			}
			if pending := d.recoveryPendingFor("stardewvalley"); pending != nil {
				t.Errorf("startup registered %+v for a %s", pending, tc.name)
			}
			if !tc.leftover {
				return
			}
			if err := os.MkdirAll(filepath.Join(install, smapi.WorkDir, uuid.NewString()), 0700); err != nil {
				t.Fatal(err)
			}
			d.RecoverAll()
			if _, _, recovers := engine.counts(); recovers != 1 {
				t.Errorf("startup ran %d loader recoveries with a leftover work dir, want 1", recovers)
			}
		})
	}
}

func TestConfigureGameRecoversALoaderTransactionLeftAtTheNewInstall(t *testing.T) {
	install := newStardewInstall(t)
	placed := filepath.Join(install, "StardewModdingAPI")
	writeFixture(t, placed)
	writeLoaderIntent(t, install, "StardewModdingAPI", fileIdentity(t, placed))
	d := newIsolatedDaemon(t, nil)

	if err := d.ConfigureGame("stardewvalley", "Stardew Valley", 413150, install, "Mods"); err != nil {
		t.Fatalf("ConfigureGame: %v", err)
	}
	for _, path := range []string{placed, filepath.Join(install, smapi.IntentFile)} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived the recovery after ConfigureGame: %v", filepath.Base(path), err)
		}
	}
	if pending := d.recoveryPendingFor("stardewvalley"); pending != nil {
		t.Errorf("clean rollback left a pending recovery: %+v", pending)
	}
	if held := d.exclusiveHeld(install); held {
		t.Error("the recovery after ConfigureGame kept the exclusive fence")
	}
}

func TestConfigureGameRegistersAFailedLoaderRecovery(t *testing.T) {
	install := newStardewInstall(t)
	writeFixture(t, filepath.Join(install, "StardewModdingAPI"))
	writeLoaderIntent(t, install, "StardewModdingAPI", smapi.FileID{Dev: 1, Ino: 1})
	d := newIsolatedDaemon(t, nil)

	if err := d.ConfigureGame("stardewvalley", "Stardew Valley", 413150, install, "Mods"); err != nil {
		t.Fatalf("ConfigureGame: %v", err)
	}
	pending := d.recoveryPendingFor("stardewvalley")
	if pending == nil || pending.DataPath != install || !strings.HasPrefix(pending.Reason, loaderRecoveryPrefix) {
		t.Fatalf("pending = %+v, want a mod-loader entry for %s", pending, install)
	}
	if _, err := d.MountVFS("stardewvalley", "Default"); err == nil || !strings.Contains(err.Error(), "recovery pending") {
		t.Errorf("MountVFS after a failed recovery = %v, want recovery pending", err)
	}
}

func TestMountAndLaunchAreRefusedWhileALoaderIntentIsUnresolved(t *testing.T) {
	d, install := newLoaderTestDaemon(t, nil, nil)
	writeFileContent(t, filepath.Join(install, smapi.IntentFile), "{}")
	requireInterrupted := func(what string, err error) {
		t.Helper()
		var unavailable *smapi.UnavailableError
		if !errors.As(err, &unavailable) || unavailable.State != smapi.StateInterrupted || unavailable.Context != smapi.UnavailableForMount {
			t.Fatalf("%s with an unresolved intent = %v, want UnavailableError(interrupted, mount)", what, err)
		}
		if strings.Contains(err.Error(), "mods enabled") {
			t.Errorf("%s refusal %q uses the launch-preflight wording", what, err)
		}
	}
	_, err := d.MountVFS("stardewvalley", "Default")
	requireInterrupted("MountVFS", err)
	_, err = d.LaunchGame("stardewvalley", false, "")
	requireInterrupted("vanilla LaunchGame", err)
	_, err = d.LaunchGame("stardewvalley", false, "Default")
	requireInterrupted("LaunchGame", err)
	d.mu.RLock()
	mounted := d.mountMgrs["stardewvalley"].IsMounted()
	d.mu.RUnlock()
	if mounted {
		t.Fatal("a refused launch mounted the mods")
	}

	if err := os.Remove(filepath.Join(install, smapi.IntentFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.MountVFS("stardewvalley", "Default"); err != nil {
		t.Fatalf("MountVFS once the intent is resolved: %v", err)
	}
	if err := d.UnmountVFS("stardewvalley"); err != nil {
		t.Fatal(err)
	}
}

func TestLoaderStatePresentSkipsUnusableInstallDirectories(t *testing.T) {
	install := newStardewInstall(t)
	if err := os.MkdirAll(filepath.Join(install, smapi.WorkDir), 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(install, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "file")
	writeFixture(t, file)
	for _, dir := range []string{link, file, filepath.Join(t.TempDir(), "missing")} {
		if loaderStatePresent(dir) {
			t.Errorf("loaderStatePresent(%s) = true, want the unusable directory skipped", dir)
		}
	}
	if !loaderStatePresent(install) {
		t.Error("loaderStatePresent missed a leftover work directory")
	}
}

func TestRestoreFromBackupResolvesOneRecoveryPerConfirmation(t *testing.T) {
	engine := &fakeLoaderEngine{}
	d, install := newLoaderTestDaemon(t, engine, nil)
	d.registerLoaderPending([]string{"stardewvalley"}, install, errors.New("preimage missing"), d.publishRecoveryEvent)
	d.mu.RLock()
	mm := d.mountMgrs["stardewvalley"]
	d.mu.RUnlock()
	dataPath := mm.DataPath()
	writeFileContent(t, filepath.Join(dataPath, "live.txt"), "live")
	writeFileContent(t, filepath.Join(mm.BackupPath(), "backup.txt"), "backup")
	resolved, err := filepath.Abs(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	mountPending := &dto.RecoveryPendingResult{GameID: "stardewvalley", DataPath: dataPath, BackupPath: mm.BackupPath(), Reason: "farm left behind"}
	d.pendingRecoveriesMu.Lock()
	d.pendingRecoveries[resolved] = mountPending
	d.gamesAtPath[resolved] = []string{"stardewvalley"}
	d.pendingRecoveriesMu.Unlock()

	if err := d.RestoreFromBackup("stardewvalley", dto.RecoveryKindUnspecified, ""); err != nil {
		t.Fatalf("RestoreFromBackup: %v", err)
	}
	if _, _, recovers := engine.counts(); recovers != 1 {
		t.Errorf("loader recoveries = %d, want 1", recovers)
	}
	d.pendingRecoveriesMu.Lock()
	loaderLeft, mountLeft := d.loaderPendingRecoveries["stardewvalley"], d.pendingRecoveries[resolved]
	d.pendingRecoveriesMu.Unlock()
	if loaderLeft != nil || mountLeft != mountPending {
		t.Fatalf("after one confirmation loader=%+v mount=%+v, want only the loader entry resolved", loaderLeft, mountLeft)
	}
	if data, err := os.ReadFile(filepath.Join(dataPath, "live.txt")); err != nil || string(data) != "live" {
		t.Errorf("the loader confirmation also restored the farm backup: %q (%v)", data, err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case evt := <-d.WatchStatus():
			if evt.RecoveryPending != nil && evt.RecoveryPending.Reason == mountPending.Reason {
				return
			}
		case <-deadline:
			t.Fatal("the remaining recovery was not announced again")
		}
	}
}

func TestRestoreFromBackupReadsDaemonMapsUnderTheDaemonLock(t *testing.T) {
	d, install := newLoaderTestDaemon(t, &fakeLoaderEngine{}, nil)
	stop := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			d.mu.Lock()
			d.ensureMountManager(fmt.Sprintf("extra%d", i), config.GameConfig{InstallPath: install, DataSubpath: "Extra"})
			d.mu.Unlock()
		}
	}()
	for i := 0; i < 50; i++ {
		d.registerLoaderPending([]string{"stardewvalley"}, install, errors.New("x"), d.publishRecoveryEvent)
		if err := d.RestoreFromBackup("stardewvalley", dto.RecoveryKindUnspecified, ""); err != nil {
			t.Fatalf("RestoreFromBackup: %v", err)
		}
		_ = d.RestoreFromBackup("stardewvalley", dto.RecoveryKindUnspecified, "")
	}
	close(stop)
	<-writerDone
}

func TestLoaderProtectedRootPathsRefuseModsReplacingTheLauncher(t *testing.T) {
	install := newStardewInstall(t)
	d := newIsolatedDaemon(t, newStardewGames(install))
	d.mu.Lock()
	manager, err := d.ensureRootDeploymentManager("stardewvalley", d.config.Games["stardewvalley"])
	d.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"StardewValley", "StardewModdingAPI", "smapi-internal/config.json", "steam_appid.txt", smapi.RecordFile, smapi.BackupDir + "/x"} {
		modRoot := filepath.Join(t.TempDir(), "Evil")
		writeFixture(t, filepath.Join(modRoot, vfs.RootContentDirName, filepath.FromSlash(rel)))
		if _, err := manager.Apply([]vfs.Layer{{Name: "Evil", RootPath: modRoot, Enabled: true}}, "Default"); !errors.Is(err, vfs.ErrRootPathConflict) {
			t.Errorf("root deployment of %s = %v, want ErrRootPathConflict", rel, err)
		}
	}
	if got := loaderProtectedRootPaths("skyrimse"); got != nil {
		t.Errorf("skyrimse loader protected paths = %v, want none", got)
	}
}

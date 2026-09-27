package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/vfs"
)

const modChangeGame = "skyrimse"

// mountedModChangeFixture builds a mounted game with one enabled mod, a Data file and a game-root link.
func mountedModChangeFixture(t *testing.T) (*Daemon, string, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	install := filepath.Join(t.TempDir(), "Game")
	writeFileContent(t, filepath.Join(install, "Data", "base.txt"), "base")
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		modChangeGame: {Name: "Skyrim Special Edition", InstallPath: install, DataSubpath: "Data", SteamAppID: 489830},
	})
	modDir := filepath.Join(config.ModsDir(modChangeGame), "A")
	writeFileContent(t, filepath.Join(modDir, "a.esp"), "original bytes")
	writeFileContent(t, filepath.Join(modDir, vfs.RootContentDirName, "root.txt"), "root bytes")
	fakeProcesses(d, false, nil)
	if err := d.SetModList(modChangeGame, "Default", []dto.ModListEntryResult{{ModName: "A", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.MountVFS(modChangeGame, "Default"); err != nil {
		t.Fatalf("MountVFS: %v", err)
	}
	t.Cleanup(func() {
		fakeProcesses(d, false, nil)
		d.setSteamLaunched(modChangeGame, false)
		if d.mountMgrs[modChangeGame].IsMounted() {
			if err := d.UnmountVFS(modChangeGame); err != nil {
				t.Errorf("UnmountVFS: %v", err)
			}
		}
	})
	return d, install, modDir
}

// requireFarmWithoutMod verifies uninstall captured game output but not the removed mod and left a clean farm.
func requireFarmWithoutMod(t *testing.T, d *Daemon, install, modDir string) {
	t.Helper()
	data := filepath.Join(install, "Data")
	if _, err := os.Lstat(filepath.Join(data, "a.esp")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("removed mod file still deployed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(modDir, "a.esp")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("removed mod folder still present: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(install, "root.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("root file still deployed: %v", err)
	}
	if output, err := os.ReadFile(filepath.Join(config.ModsDir(modChangeGame), "Overwrite", "game-output.txt")); err != nil || string(output) != "game output" {
		t.Errorf("game output not captured: %q, %v", output, err)
	}
	if _, err := os.Lstat(filepath.Join(config.ModsDir(modChangeGame), "Overwrite", "a.esp")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("removed mod leaked into Overwrite: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(data, "game-output.txt")); err != nil {
		t.Errorf("captured game output not redeployed: %v", err)
	}
	if status, err := d.GetVFSStatus(modChangeGame); err != nil || !status.Mounted || status.Dirty {
		t.Errorf("VFS status after uninstall = %+v, %v, want clean mounted farm", status, err)
	}
	if entries, err := d.GetModList(modChangeGame, "Default"); err != nil || len(entries) != 0 {
		t.Errorf("modlist after uninstall = %+v, %v", entries, err)
	}
}

// TestUninstallAppliedModRebuildsFarmFirst ensures removing a deployed mod captures game writes without capturing the mod into Overwrite.
func TestUninstallAppliedModRebuildsFarmFirst(t *testing.T) {
	d, install, modDir := mountedModChangeFixture(t)
	writeFileContent(t, filepath.Join(install, "Data", "game-output.txt"), "game output")
	if _, err := d.UninstallMod(modChangeGame, "A", true); err != nil {
		t.Fatalf("UninstallMod: %v", err)
	}
	requireFarmWithoutMod(t, d, install, modDir)
}

// TestUninstallCannotBeRedeployedBeforeDeletion verifies a stale modlist and Apply cannot restore a mod while its trash awaits deletion.
func TestUninstallCannotBeRedeployedBeforeDeletion(t *testing.T) {
	d, install, modDir := mountedModChangeFixture(t)
	paused := make(chan string, 1)
	resume := make(chan struct{})
	defer func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	}()
	d.uninstallBeforeDelete = func(trash string) {
		paused <- trash
		<-resume
	}
	done := make(chan error, 1)
	go func() {
		_, err := d.UninstallMod(modChangeGame, "A", true)
		done <- err
	}()
	var trash string
	select {
	case trash = <-paused:
	case err := <-done:
		t.Fatalf("UninstallMod finished before deletion pause: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("UninstallMod did not reach deletion pause")
	}
	if _, err := os.Lstat(modDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("mod folder exists before deletion: %v", err)
	}
	if _, err := os.Stat(filepath.Join(trash, "a.esp")); err != nil {
		t.Errorf("mod was not moved to trash: %v", err)
	}
	if err := d.SetModList(modChangeGame, "Default", []dto.ModListEntryResult{{ModName: "A", Enabled: true}}); err != nil {
		t.Fatalf("SetModList with stale entry: %v", err)
	}
	if err := d.RebuildVFS(modChangeGame); err != nil {
		t.Fatalf("Apply with missing mod: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(install, "Data", "a.esp")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing mod file redeployed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(install, "root.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing mod root file redeployed: %v", err)
	}
	sentinel, err := vfs.ReadSentinel(filepath.Join(install, "Data"))
	if err != nil {
		t.Fatal(err)
	}
	for _, layer := range sentinel.Layers {
		if layer.Name == "A" {
			t.Errorf("missing mod remained in applied layers: %+v", sentinel.Layers)
		}
	}
	close(resume)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("UninstallMod: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("UninstallMod did not complete after deletion resumed")
	}
	for _, dir := range []string{modDir, trash} {
		if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("uninstall left folder %s: %v", filepath.Base(dir), err)
		}
	}
}

// TestUninstallRenameFailureKeepsModFolder verifies a failed move reports the error without deleting the original mod.
func TestUninstallRenameFailureKeepsModFolder(t *testing.T) {
	d, install, modDir := mountedModChangeFixture(t)
	d.mu.Lock()
	d.uninstallRename = func(string, string) error { return errors.New("injected rename failure") }
	d.mu.Unlock()
	_, err := d.UninstallMod(modChangeGame, "A", true)
	if err == nil || !strings.Contains(err.Error(), "injected rename failure") {
		t.Fatalf("UninstallMod error = %v, want rename failure", err)
	}
	if _, err := os.Stat(filepath.Join(modDir, "a.esp")); err != nil {
		t.Errorf("failed rename removed mod folder: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(install, "Data", "a.esp")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("failed rename restored Data file: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(install, "root.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("failed rename restored root link: %v", err)
	}
	if entries, err := d.GetModList(modChangeGame, "Default"); err != nil || len(entries) != 0 {
		t.Errorf("modlist after rename failure = %+v, %v", entries, err)
	}
	entries, err := os.ReadDir(config.ModsDir(modChangeGame))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".gorganizer-trash-") {
			t.Errorf("rename failure left trash: %s", entry.Name())
		}
	}
}

// TestUninstallDisabledButStillAppliedMod ensures an unapplied disable does not make the old hardlinks eligible for capture.
func TestUninstallDisabledButStillAppliedMod(t *testing.T) {
	d, install, modDir := mountedModChangeFixture(t)
	if err := d.SetModList(modChangeGame, "Default", []dto.ModListEntryResult{{ModName: "A", Enabled: false}}); err != nil {
		t.Fatal(err)
	}
	if !d.mountMgrs[modChangeGame].IsDirty() {
		t.Fatal("disabling A did not dirty the mounted farm")
	}
	writeFileContent(t, filepath.Join(install, "Data", "game-output.txt"), "game output")
	if _, err := d.UninstallMod(modChangeGame, "A", false); err != nil {
		t.Fatalf("UninstallMod: %v", err)
	}
	requireFarmWithoutMod(t, d, install, modDir)
}

// TestUninstallRootSourceRebuildsFarm ensures recorded root sources count even after the Data farm has dropped the mod.
func TestUninstallRootSourceRebuildsFarm(t *testing.T) {
	d, install, modDir := mountedModChangeFixture(t)
	if err := d.SetModList(modChangeGame, "Default", []dto.ModListEntryResult{{ModName: "A", Enabled: false}}); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	gc, err := d.config.EffectiveGameConfig(modChangeGame)
	if err == nil {
		_, entries, loadErr := d.profileMgr.Load(modChangeGame, "Default")
		err = loadErr
		if err == nil {
			err = d.mountMgrs[modChangeGame].MarkDirty(d.svc.vfs.buildLayers(modChangeGame, gc, entries))
		}
		if err == nil {
			err = d.mountMgrs[modChangeGame].ReMaterialize()
		}
	}
	d.mu.Unlock()
	if err != nil {
		t.Fatalf("preparing a root-only source: %v", err)
	}
	if _, err := d.UninstallMod(modChangeGame, "A", false); err != nil {
		t.Fatalf("UninstallMod: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(install, "root.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("root deployment still points at removed mod: %v", err)
	}
	if _, err := os.Lstat(modDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("removed mod still exists: %v", err)
	}
	if d.mountMgrs[modChangeGame].IsDirty() {
		t.Error("farm is dirty after removing its root source")
	}
}

// TestUninstallRefusedWithoutConfirmation keeps an enabled mod deployed until the user confirms its removal.
func TestUninstallRefusedWithoutConfirmation(t *testing.T) {
	d, install, modDir := mountedModChangeFixture(t)
	_, err := d.UninstallMod(modChangeGame, "A", false)
	var inUse *ModInUseError
	if !errors.As(err, &inUse) || len(inUse.Profiles) != 1 || inUse.Profiles[0] != "Default" {
		t.Fatalf("UninstallMod without confirmation = %v, want ModInUseError(Default)", err)
	}
	if _, err := os.Stat(filepath.Join(modDir, "a.esp")); err != nil {
		t.Errorf("mod removed without confirmation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(install, "Data", "a.esp")); err != nil {
		t.Errorf("farm changed without confirmation: %v", err)
	}
}

// TestUninstallRefusedWhileGameRunning ensures detected processes, scan errors, Steam grace and pending admissions cannot bypass the farm guard.
func TestUninstallRefusedWhileGameRunning(t *testing.T) {
	for _, tc := range []struct {
		name string
		busy func(*testing.T, *Daemon)
	}{
		{name: "running process", busy: func(_ *testing.T, d *Daemon) { fakeProcesses(d, true, nil) }},
		{name: "process scan error", busy: func(_ *testing.T, d *Daemon) { fakeProcesses(d, false, errors.New("proc unavailable")) }},
		{name: "fresh Steam launch", busy: func(_ *testing.T, d *Daemon) { d.setSteamLaunched(modChangeGame, true) }},
		{name: "pending launch admission", busy: func(t *testing.T, d *Daemon) {
			d.mu.Lock()
			release, err := d.reserveShared(modChangeGame, dto.BusyOperationLaunch)
			d.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(release)
		}},
		{name: "pending recovery", busy: func(t *testing.T, d *Daemon) {
			d.pendingRecoveriesMu.Lock()
			d.rootPendingRecoveries[modChangeGame] = &dto.RecoveryPendingResult{GameID: modChangeGame, Reason: "test recovery"}
			d.pendingRecoveriesMu.Unlock()
			t.Cleanup(func() {
				d.pendingRecoveriesMu.Lock()
				delete(d.rootPendingRecoveries, modChangeGame)
				d.pendingRecoveriesMu.Unlock()
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, install, modDir := mountedModChangeFixture(t)
			before, err := vfs.ReadSentinel(filepath.Join(install, "Data"))
			if err != nil {
				t.Fatal(err)
			}
			tc.busy(t, d)
			_, err = d.UninstallMod(modChangeGame, "A", true)
			requireGameRunning(t, "UninstallMod", err, dto.GameRunningOperationUninstall)
			var running *dto.GameRunningError
			if !errors.As(err, &running) || running.GameID != modChangeGame {
				t.Errorf("UninstallMod error = %v, want game %s", err, modChangeGame)
			}
			if _, err := os.Stat(filepath.Join(modDir, "a.esp")); err != nil {
				t.Errorf("refused uninstall removed the mod: %v", err)
			}
			if _, err := os.Stat(filepath.Join(install, "Data", "a.esp")); err != nil {
				t.Errorf("refused uninstall changed the farm: %v", err)
			}
			after, err := vfs.ReadSentinel(filepath.Join(install, "Data"))
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Errorf("refused uninstall changed the sentinel: %v", err)
			}
			if entries, err := d.GetModList(modChangeGame, "Default"); err != nil || len(entries) != 1 || entries[0].ModName != "A" {
				t.Errorf("refused uninstall changed the modlist: %+v, %v", entries, err)
			}
		})
	}
}

// TestRenameAppliedModRebuildsOrRollsBack ensures root links and the Data sentinel change together, or the old folder and profile are restored.
func TestRenameAppliedModRebuildsOrRollsBack(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail bool
	}{
		{name: "applies rename"},
		{name: "rebuild failure restores old mod", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, install, oldDir := mountedModChangeFixture(t)
			if _, err := d.CreateProfile(modChangeGame, "Alt"); err != nil {
				t.Fatal(err)
			}
			if err := d.SetModList(modChangeGame, "Alt", []dto.ModListEntryResult{{ModName: "A", Enabled: false}}); err != nil {
				t.Fatal(err)
			}
			if tc.fail {
				d.mu.Lock()
				d.modChangeRematerialize = func(*vfs.MountManager) error { return errors.New("injected rebuild failure") }
				d.mu.Unlock()
			}
			err := d.RenameMod(modChangeGame, "A", "Renamed")
			want := "Renamed"
			modDir := filepath.Join(config.ModsDir(modChangeGame), want)
			if tc.fail {
				want, modDir = "A", oldDir
				if err == nil || !strings.Contains(err.Error(), "injected rebuild failure") {
					t.Fatalf("RenameMod error = %v, want rebuild failure", err)
				}
			} else if err != nil {
				t.Fatalf("RenameMod: %v", err)
			}
			if _, err := os.Stat(filepath.Join(modDir, "a.esp")); err != nil {
				t.Errorf("mod file missing: %v", err)
			}
			if _, err := os.Stat(filepath.Join(install, "Data", "a.esp")); err != nil {
				t.Errorf("farm file missing: %v", err)
			}
			rootTarget, err := os.Readlink(filepath.Join(install, "root.txt"))
			if err != nil || rootTarget != filepath.Join(modDir, vfs.RootContentDirName, "root.txt") {
				t.Errorf("root link target = %q, %v, want %s", rootTarget, err, modDir)
			}
			if _, err := os.Stat(filepath.Join(install, "root.txt")); err != nil {
				t.Errorf("root link is dangling: %v", err)
			}
			sentinel, err := vfs.ReadSentinel(filepath.Join(install, "Data"))
			if err != nil {
				t.Fatal(err)
			}
			if len(sentinel.Layers) < 2 || sentinel.Layers[1].Name != want || sentinel.Layers[1].Root != modDir {
				t.Errorf("sentinel layers after rename = %+v", sentinel.Layers)
			}
			for _, profileName := range []string{"Default", "Alt"} {
				if entries, err := d.GetModList(modChangeGame, profileName); err != nil || len(entries) != 1 || entries[0].ModName != want || entries[0].Enabled != (profileName == "Default") {
					t.Errorf("%s modlist after rename = %+v, %v", profileName, entries, err)
				}
			}
			other := oldDir
			if tc.fail {
				other = filepath.Join(config.ModsDir(modChangeGame), "Renamed")
			}
			if _, err := os.Lstat(other); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("other mod folder still present: %v", err)
			}
			if d.mountMgrs[modChangeGame].IsDirty() {
				t.Error("farm is dirty after rename")
			}
		})
	}
}

// TestMergeIntoDeployedModRefused ensures a merge cannot overwrite bytes already exposed by the mounted farm.
func TestMergeIntoDeployedModRefused(t *testing.T) {
	d, install, modDir := mountedModChangeFixture(t)
	archive := filepath.Join(config.DownloadsDir(modChangeGame), "Update.zip")
	writeZipFiles(t, archive, map[string]string{"a.esp": "replacement bytes"})
	_, _, err := d.StartInstall(dto.StartInstallRequest{GameID: modChangeGame, ArchiveRelPath: "Update.zip", Mode: dto.InstallMergeIntoMod, TargetMod: "A"})
	var mounted *download.ModMountedError
	if !errors.As(err, &mounted) || mounted.Mod != "A" {
		t.Fatalf("StartInstall error = %v, want ModMountedError(A)", err)
	}
	for _, path := range []string{filepath.Join(modDir, "a.esp"), filepath.Join(install, "Data", "a.esp")} {
		body, err := os.ReadFile(path)
		if err != nil || string(body) != "original bytes" {
			t.Errorf("%s = %q, %v, want original bytes", path, body, err)
		}
	}
	if d.mountMgrs[modChangeGame].IsDirty() {
		t.Error("a refused merge dirtied the farm")
	}
}

// TestRenameRefusedWhileSteamLaunchIsFresh ensures force is not relevant to rename and a fresh launch cannot expose dangling root links.
func TestRenameRefusedWhileSteamLaunchIsFresh(t *testing.T) {
	d, install, modDir := mountedModChangeFixture(t)
	d.mu.Lock()
	d.now = func() time.Time { return time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC) }
	d.mu.Unlock()
	d.setSteamLaunched(modChangeGame, true)
	requireGameRunning(t, "RenameMod", d.RenameMod(modChangeGame, "A", "B"), dto.GameRunningOperationRename)
	if _, err := os.Stat(filepath.Join(modDir, "a.esp")); err != nil {
		t.Errorf("mod moved despite refusal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(install, "root.txt")); err != nil {
		t.Errorf("root link broke despite refusal: %v", err)
	}
}

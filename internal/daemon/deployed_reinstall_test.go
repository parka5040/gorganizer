package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/vfs"
)

// recordDeployedReinstallSource gives the mounted mod an archive containing replacement Data and game-root files.
func recordDeployedReinstallSource(t *testing.T, modDir string) {
	t.Helper()
	archive := filepath.Join(config.DownloadsDir(modChangeGame), "Replacement.zip")
	writeZipFiles(t, archive, map[string]string{
		"a.esp": "replacement bytes", ".gorganizer-root/root.txt": "replacement root",
	})
	if err := download.SaveModMetadata(modDir, &download.ModMetadata{
		Name: "A", Folder: "A", SourceArchives: []download.SourceArchiveRef{{Path: "Downloads/Replacement.zip"}},
	}); err != nil {
		t.Fatal(err)
	}
}

// assertDeployedReplacement checks both live surfaces, the mounted state, and removal of swap artifacts.
func assertDeployedReplacement(t *testing.T, d *Daemon, install, modDir string) {
	t.Helper()
	for path, want := range map[string]string{
		filepath.Join(modDir, "a.esp"):          "replacement bytes",
		filepath.Join(install, "Data", "a.esp"): "replacement bytes",
		filepath.Join(install, "root.txt"):      "replacement root",
	} {
		body, err := os.ReadFile(path)
		if err != nil || string(body) != want {
			t.Errorf("%s = %q, %v, want %q", path, body, err, want)
		}
	}
	if target, err := os.Readlink(filepath.Join(install, "root.txt")); err != nil || target != filepath.Join(modDir, vfs.RootContentDirName, "root.txt") {
		t.Errorf("root link = %q, %v", target, err)
	}
	status, err := d.GetVFSStatus(modChangeGame)
	if err != nil || !status.Mounted || status.Dirty {
		t.Errorf("VFS status = %+v, %v, want clean mounted farm", status, err)
	}
	assertNoReinstallState(t, config.ModsDir(modChangeGame))
}

// TestReinstallDeployedModRebuildsFarm checks that a reinstall replaces live files before removing the original mod folder.
func TestReinstallDeployedModRebuildsFarm(t *testing.T) {
	d, install, modDir := mountedModChangeFixture(t)
	recordDeployedReinstallSource(t, modDir)
	original, err := os.Stat(modDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := d.ReinstallMod(modChangeGame, "A"); err != nil {
		t.Fatalf("ReinstallMod: %v", err)
	}
	updated, err := os.Stat(modDir)
	if err != nil {
		t.Fatalf("stat reinstalled mod folder: %v", err)
	}
	if os.SameFile(original, updated) {
		t.Error("reinstall did not replace the mod folder")
	}
	assertDeployedReplacement(t, d, install, modDir)
}

// TestReinstallDeployedModRemovesOldRootDeployment checks that root files absent from the replacement are undeployed.
func TestReinstallDeployedModRemovesOldRootDeployment(t *testing.T) {
	d, install, modDir := mountedModChangeFixture(t)
	archive := filepath.Join(config.DownloadsDir(modChangeGame), "Replacement.zip")
	writeZipFiles(t, archive, map[string]string{"a.esp": "replacement bytes"})
	if err := download.SaveModMetadata(modDir, &download.ModMetadata{
		Name: "A", Folder: "A", SourceArchives: []download.SourceArchiveRef{{Path: "Downloads/Replacement.zip"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := d.ReinstallMod(modChangeGame, "A"); err != nil {
		t.Fatalf("ReinstallMod: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(install, "root.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old root link was not removed: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(install, "Data", "a.esp")); err != nil || string(body) != "replacement bytes" {
		t.Errorf("deployed file = %q, %v, want replacement bytes", body, err)
	}
	assertNoReinstallState(t, config.ModsDir(modChangeGame))
}

// TestDeployedReinstallRefusedWhileGameRuns checks the deployed-mod guard against processes, launch grace and pending admissions.
func TestDeployedReinstallRefusedWhileGameRuns(t *testing.T) {
	for _, tc := range []struct {
		name string
		busy func(*testing.T, *Daemon)
	}{
		{name: "running process", busy: func(_ *testing.T, d *Daemon) { fakeProcesses(d, true, nil) }},
		{name: "process scan failed", busy: func(_ *testing.T, d *Daemon) { fakeProcesses(d, false, errors.New("scan failed")) }},
		{name: "fresh Steam launch", busy: func(_ *testing.T, d *Daemon) { d.setSteamLaunched(modChangeGame, true) }},
		{name: "pending launch", busy: func(t *testing.T, d *Daemon) {
			d.mu.Lock()
			release, err := d.reserveShared(modChangeGame, dto.BusyOperationLaunch)
			d.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(release)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, install, modDir := mountedModChangeFixture(t)
			recordDeployedReinstallSource(t, modDir)
			tc.busy(t, d)
			_, _, _, err := d.ReinstallMod(modChangeGame, "A")
			requireGameRunning(t, "ReinstallMod", err, dto.GameRunningOperationReinstall)
			for _, path := range []string{filepath.Join(modDir, "a.esp"), filepath.Join(install, "Data", "a.esp")} {
				body, readErr := os.ReadFile(path)
				if readErr != nil || string(body) != "original bytes" {
					t.Errorf("%s = %q, %v, want original bytes", path, body, readErr)
				}
			}
			if _, statErr := os.Stat(filepath.Join(install, "root.txt")); statErr != nil {
				t.Errorf("game-root link broke: %v", statErr)
			}
			assertNoReinstallState(t, config.ModsDir(modChangeGame))
		})
	}
}

// TestDeployedReinstallRebuildFailureRestoresOriginal checks that a failed rebuild restores the mod, farm and root link.
func TestDeployedReinstallRebuildFailureRestoresOriginal(t *testing.T) {
	d, install, modDir := mountedModChangeFixture(t)
	recordDeployedReinstallSource(t, modDir)
	original, err := os.Stat(modDir)
	if err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.modChangeRematerialize = func(*vfs.MountManager) error { return errors.New("injected rebuild failure") }
	d.mu.Unlock()
	_, _, _, err = d.ReinstallMod(modChangeGame, "A")
	if err == nil || !strings.Contains(err.Error(), "injected rebuild failure") {
		t.Fatalf("ReinstallMod error = %v, want rebuild failure", err)
	}
	restored, statErr := os.Stat(modDir)
	if statErr != nil {
		t.Fatalf("stat restored mod folder: %v", statErr)
	}
	if !os.SameFile(original, restored) {
		t.Error("original mod folder was not restored")
	}
	for path, want := range map[string]string{
		filepath.Join(modDir, "a.esp"):          "original bytes",
		filepath.Join(install, "Data", "a.esp"): "original bytes",
		filepath.Join(install, "root.txt"):      "root bytes",
	} {
		body, readErr := os.ReadFile(path)
		if readErr != nil || string(body) != want {
			t.Errorf("%s = %q, %v, want %q", path, body, readErr, want)
		}
	}
	if target, readErr := os.Readlink(filepath.Join(install, "root.txt")); readErr != nil || target != filepath.Join(modDir, vfs.RootContentDirName, "root.txt") {
		t.Errorf("restored root link = %q, %v", target, readErr)
	}
	if status, err := d.GetVFSStatus(modChangeGame); err != nil || !status.Mounted || status.Dirty {
		t.Errorf("VFS status after rollback = %+v, %v", status, err)
	}
	assertNoReinstallState(t, config.ModsDir(modChangeGame))
}

// TestDeployedReinstallCrashAfterSwapRecovers checks that intent recovery and the next mount deploy the replacement without stale files.
func TestDeployedReinstallCrashAfterSwapRecovers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	install := filepath.Join(t.TempDir(), "Game")
	writeFileContent(t, filepath.Join(install, "Data", "base.txt"), "base")
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		modChangeGame: {Name: "Skyrim Special Edition", InstallPath: install, DataSubpath: "Data", SteamAppID: 489830},
	})
	modDir := filepath.Join(config.ModsDir(modChangeGame), "A")
	writeFileContent(t, filepath.Join(modDir, "a.esp"), "original bytes")
	writeFileContent(t, filepath.Join(modDir, vfs.RootContentDirName, "root.txt"), "root bytes")
	recordDeployedReinstallSource(t, modDir)
	fakeProcesses(d, false, nil)
	if err := d.SetModList(modChangeGame, "Default", []dto.ModListEntryResult{{ModName: "A", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.MountVFS(modChangeGame, "Default"); err != nil {
		t.Fatal(err)
	}
	d.reinstallFault = func(step string) error {
		if step == "swapped" {
			return errSimulatedCrash
		}
		return nil
	}
	if _, _, _, err := d.ReinstallMod(modChangeGame, "A"); !errors.Is(err, errSimulatedCrash) {
		t.Fatalf("ReinstallMod error = %v, want simulated crash", err)
	}
	body, err := os.ReadFile(filepath.Join(install, "Data", "a.esp"))
	if err != nil || string(body) != "original bytes" {
		t.Fatalf("farm before recovery = %q, %v, want original bytes", body, err)
	}
	restarted := restartDaemon(t, d)
	restarted.RecoverAll()
	assertNoReinstallState(t, config.ModsDir(modChangeGame))
	if _, err := restarted.MountVFS(modChangeGame, "Default"); err != nil {
		t.Fatalf("MountVFS after recovery: %v", err)
	}
	assertDeployedReplacement(t, restarted, install, modDir)
	if _, err := os.Lstat(filepath.Join(config.ModsDir(modChangeGame), "Overwrite", "a.esp")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old mod file was captured into Overwrite: %v", err)
	}
	if err := restarted.UnmountVFS(modChangeGame); err != nil {
		t.Fatalf("UnmountVFS after recovery: %v", err)
	}
}

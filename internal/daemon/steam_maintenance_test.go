package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/steam"
	"github.com/parka/gorganizer/internal/vfs"
)

// newSteamMaintenanceDaemon creates a daemon and Steam manifest under disposable directories.
func newSteamMaintenanceDaemon(t *testing.T) (*Daemon, string, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	install, manifest := steamFixture(t, 489830, "Skyrim Special Edition")
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		"skyrimse": {InstallPath: install, DataSubpath: "Data", SteamAppID: 489830},
	})
	d.readSteamAppState = steam.ReadAppState
	return d, filepath.Join(install, "Data"), manifest
}

// changeSteamFixture updates a disposable app manifest's build or activity flag.
func changeSteamFixture(t *testing.T, manifest, field, old, replacement string) {
	t.Helper()
	body, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	before := `"` + field + `" "` + old + `"`
	after := `"` + field + `" "` + replacement + `"`
	if !strings.Contains(string(body), before) {
		t.Fatalf("manifest has no %s", before)
	}
	if err := os.WriteFile(manifest, []byte(strings.Replace(string(body), before, after, 1)), 0644); err != nil {
		t.Fatal(err)
	}
}

// requireSteamRefusal checks the public maintenance reason on a failed operation.
func requireSteamRefusal(t *testing.T, err error, reason string) {
	t.Helper()
	var maintenance *dto.SteamMaintenanceError
	if !errors.As(err, &maintenance) || maintenance.Reason != reason {
		t.Fatalf("error = %v, want Steam maintenance %s", err, reason)
	}
}

// TestSteamBusyBlocksFarmChanges checks that a live Steam update prevents Apply and teardown.
func TestSteamBusyBlocksFarmChanges(t *testing.T) {
	d, data, manifest := newSteamMaintenanceDaemon(t)
	changeSteamFixture(t, manifest, "StateFlags", "4", "2")
	if _, err := d.MountVFS("skyrimse", "Default"); err == nil {
		t.Fatal("mounted while Steam was busy")
	} else {
		requireSteamRefusal(t, err, "busy")
	}
	changeSteamFixture(t, manifest, "StateFlags", "2", "4")
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatal(err)
	}
	changeSteamFixture(t, manifest, "StateFlags", "4", "2")
	requireSteamRefusal(t, d.RebuildVFS("skyrimse"), "busy")
	requireSteamRefusal(t, d.UnmountVFS("skyrimse"), "busy")
	if _, err := os.Stat(filepath.Join(data, vfs.SentinelFilename)); err != nil {
		t.Fatalf("Steam update destroyed the farm: %v", err)
	}
	status, err := d.GetVFSStatus("skyrimse")
	if err != nil || status.SteamMaintenance != dto.SteamMaintenanceBusy {
		t.Fatalf("busy status = %+v, %v", status, err)
	}
	changeSteamFixture(t, manifest, "StateFlags", "2", "4")
	if err := d.UnmountVFS("skyrimse"); err != nil {
		t.Fatal(err)
	}
}

// TestSteamShutdownKeepsBusyFarm checks shutdown teardown leaves Steam's active farm for later recovery.
func TestSteamShutdownKeepsBusyFarm(t *testing.T) {
	d, data, manifest := newSteamMaintenanceDaemon(t)
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatal(err)
	}
	changeSteamFixture(t, manifest, "StateFlags", "4", "2")
	d.deactivateIdleFarms()
	if _, err := os.Stat(filepath.Join(data, vfs.SentinelFilename)); err != nil {
		t.Errorf("shutdown removed farm while Steam was busy: %v", err)
	}
	if _, err := os.Stat(data + ".orig"); err != nil {
		t.Errorf("shutdown removed the original Data during Steam update: %v", err)
	}
}

// TestSteamBusyWithoutBaselineBlocksTeardown checks that a later readable Steam update still protects a farm mounted without a manifest.
func TestSteamBusyWithoutBaselineBlocksTeardown(t *testing.T) {
	d, data, manifest := newSteamMaintenanceDaemon(t)
	body, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte(strings.Replace(string(body), `"StateFlags" "4"`, `"StateFlags" "2"`, 1)), 0644); err != nil {
		t.Fatal(err)
	}
	requireSteamRefusal(t, d.UnmountVFS("skyrimse"), "busy")
	if _, err := os.Stat(filepath.Join(data, vfs.SentinelFilename)); err != nil {
		t.Fatalf("farm removed during Steam update: %v", err)
	}
}

// TestSteamBusyRetargetKeepsDeployedProfile checks that a profile switch leaves the old farm in place while Steam updates.
func TestSteamBusyRetargetKeepsDeployedProfile(t *testing.T) {
	d, data, manifest := newSteamMaintenanceDaemon(t)
	if _, err := d.CreateProfile("skyrimse", "Other"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatal(err)
	}
	changeSteamFixture(t, manifest, "StateFlags", "4", "2")
	_, err := d.MountVFSWithOptions("skyrimse", "Other", false, true)
	requireSteamRefusal(t, err, "busy")
	sentinel, err := vfs.ReadSentinel(data)
	if err != nil || sentinel.ProfileName != "Default" {
		t.Fatalf("deployed profile during Steam update = %+v, %v", sentinel, err)
	}
}

// TestSteamChangedAutoSwapPreservesConflict checks that the old farm is preserved before a shared install refuses a new mount.
func TestSteamChangedAutoSwapPreservesConflict(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	install, manifest := steamFixture(t, 22380, "Fallout New Vegas")
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		"falloutnv": {InstallPath: install, DataSubpath: "Data", SteamAppID: 22380},
		"ttw":       {InstallPath: filepath.Join(t.TempDir(), "synthetic"), DataSubpath: "Data", LinkedFromGameID: "falloutnv"},
	})
	d.readSteamAppState = steam.ReadAppState
	if _, err := d.MountVFS("falloutnv", "Default"); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(install, "Data")
	writeFixture(t, filepath.Join(data, "new.esp"))
	changeSteamFixture(t, manifest, "buildid", "123", "124")
	_, err := d.MountVFSWithSwap("ttw", "Default")
	requireSteamRefusal(t, err, "verify")
	status, err := d.GetVFSStatus("falloutnv")
	if err != nil || status.Mounted || status.SteamMaintenance != dto.SteamMaintenanceVerify || len(status.PreservedBatches) != 1 {
		t.Fatalf("auto-swap status = %+v, %v", status, err)
	}
	if _, err := os.Stat(filepath.Join(status.PreservedBatches[0].Path, "files", "new.esp")); err != nil {
		t.Errorf("auto-swap lost output: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "original.esm")); err != nil {
		t.Errorf("auto-swap failed to restore original Data: %v", err)
	}
	if _, err := os.Stat(filepath.Join(config.ModsDir("falloutnv"), "Overwrite", "new.esp")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("auto-swap captured Steam output into Overwrite: %v", err)
	}
}

// TestSteamBusyBlocksDeployedModChanges checks rename, uninstall, and reinstall before any mounted farm rebuild.
func TestSteamBusyBlocksDeployedModChanges(t *testing.T) {
	d, data, manifest := newSteamMaintenanceDaemon(t)
	modDir := filepath.Join(config.ModsDir("skyrimse"), "A")
	writeFixture(t, filepath.Join(modDir, "a.esp"))
	if err := d.SetModList("skyrimse", "Default", []dto.ModListEntryResult{{ModName: "A", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(config.DownloadsDir("skyrimse"), "Replacement.zip")
	writeZipFiles(t, archive, map[string]string{"a.esp": "replacement bytes"})
	if err := download.SaveModMetadata(modDir, &download.ModMetadata{
		Name: "A", Folder: "A", SourceArchives: []download.SourceArchiveRef{{Path: "Downloads/Replacement.zip"}},
	}); err != nil {
		t.Fatal(err)
	}
	changeSteamFixture(t, manifest, "StateFlags", "4", "2")
	requireSteamRefusal(t, d.RenameMod("skyrimse", "A", "B"), "busy")
	_, err := d.UninstallMod("skyrimse", "A", true)
	requireSteamRefusal(t, err, "busy")
	_, _, _, err = d.ReinstallMod(context.Background(), "skyrimse", "A", "")
	requireSteamRefusal(t, err, "busy")
	if _, err := os.Stat(filepath.Join(modDir, "a.esp")); err != nil {
		t.Errorf("deployed mod removed during Steam update: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "a.esp")); err != nil {
		t.Errorf("deployed file removed during Steam update: %v", err)
	}
}

// TestSteamChangedPreservesFarmOutput checks that a changed Steam build never sends replaced or new files to Overwrite.
func TestSteamChangedPreservesFarmOutput(t *testing.T) {
	d, data, manifest := newSteamMaintenanceDaemon(t)
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(data, "new.esp"))
	writeFixture(t, filepath.Join(filepath.Dir(data), "replacement"))
	if err := os.WriteFile(filepath.Join(filepath.Dir(data), "replacement"), []byte("steam replacement"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(filepath.Dir(data), "replacement"), filepath.Join(data, "original.esm")); err != nil {
		t.Fatal(err)
	}
	changeSteamFixture(t, manifest, "buildid", "123", "124")
	if err := d.UnmountVFS("skyrimse"); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(data, "original.esm")); err != nil || string(body) != "fixture" {
		t.Fatalf("restored original = %q, %v", body, err)
	}
	status, err := d.GetVFSStatus("skyrimse")
	if err != nil || status.Mounted || status.SteamMaintenance != dto.SteamMaintenanceVerify || len(status.PreservedBatches) != 1 {
		t.Fatalf("status after Steam change = %+v, %v", status, err)
	}
	batch := status.PreservedBatches[0]
	if batch.FileCount != 2 || batch.Reason != "steam_changed" {
		t.Fatalf("preserved batch = %+v", batch)
	}
	for _, name := range []string{"new.esp", "original.esm"} {
		body, err := os.ReadFile(filepath.Join(batch.Path, "files", name))
		if err != nil {
			t.Errorf("preserved %s: %v", name, err)
		} else if name == "original.esm" && string(body) != "steam replacement" {
			t.Errorf("preserved replacement = %q", body)
		}
		if _, err := os.Stat(filepath.Join(config.ModsDir("skyrimse"), "Overwrite", name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Steam output %s entered Overwrite: %v", name, err)
		}
	}
	requireSteamRefusal(t, func() error { _, err := d.MountVFS("skyrimse", "Default"); return err }(), "verify")
}

// TestSteamUnknownBlocksTeardownWithBaseline checks that an unreadable manifest does not discard the baseline's farm.
func TestSteamUnknownBlocksTeardownWithBaseline(t *testing.T) {
	d, data, manifest := newSteamMaintenanceDaemon(t)
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(manifest); err != nil {
		t.Fatal(err)
	}
	requireSteamRefusal(t, d.UnmountVFS("skyrimse"), "busy")
	if _, err := os.Stat(filepath.Join(data, vfs.SentinelFilename)); err != nil {
		t.Fatalf("unreadable manifest removed farm: %v", err)
	}
}

// TestSteamChangedRecoveryPreservesFarmOutput checks that crash recovery applies the same preservation decision as unmount.
func TestSteamChangedRecoveryPreservesFarmOutput(t *testing.T) {
	d, data, manifest := newSteamMaintenanceDaemon(t)
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(data, "new.esp"))
	changeSteamFixture(t, manifest, "buildid", "123", "124")
	d2, err := New(d.config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d2.Shutdown)
	d2.readSteamAppState = steam.ReadAppState
	d2.RecoverAll()
	status, err := d2.GetVFSStatus("skyrimse")
	if err != nil || status.SteamMaintenance != dto.SteamMaintenanceVerify || len(status.PreservedBatches) != 1 {
		t.Fatalf("recovery status = %+v, %v", status, err)
	}
	if _, err := os.Stat(filepath.Join(data, "original.esm")); err != nil {
		t.Fatalf("original was not restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(status.PreservedBatches[0].Path, "files", "new.esp")); err != nil {
		t.Fatalf("output was not preserved: %v", err)
	}
	d.mountMgrs["skyrimse"].ResetAfterRestore()
}

// TestSteamChangedStrandedFarmRecoveryPreservesOutput checks recovery after an interrupted farm rename.
func TestSteamChangedStrandedFarmRecoveryPreservesOutput(t *testing.T) {
	d, data, manifest := newSteamMaintenanceDaemon(t)
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(data, "new.esp"))
	if err := os.Rename(data, data+".gorganizer-oldfarm"); err != nil {
		t.Fatal(err)
	}
	changeSteamFixture(t, manifest, "buildid", "123", "124")
	d2, err := New(d.config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d2.Shutdown)
	d2.readSteamAppState = steam.ReadAppState
	d2.RecoverAll()
	status, err := d2.GetVFSStatus("skyrimse")
	if err != nil || status.PendingRecovery != nil || status.SteamMaintenance != dto.SteamMaintenanceVerify || len(status.PreservedBatches) != 1 {
		t.Fatalf("stranded farm recovery = %+v, %v", status, err)
	}
	if _, err := os.Stat(filepath.Join(status.PreservedBatches[0].Path, "files", "new.esp")); err != nil {
		t.Errorf("stranded farm output missing: %v", err)
	}
	d.mountMgrs["skyrimse"].ResetAfterRestore()
}

// TestSteamChangedOfflineRecoveryPreservesFarmOutput checks that the offline repair path retains Steam writes.
func TestSteamChangedOfflineRecoveryPreservesFarmOutput(t *testing.T) {
	d, data, manifest := newSteamMaintenanceDaemon(t)
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(data, "new.esp"))
	changeSteamFixture(t, manifest, "buildid", "123", "124")
	report, err := RecoverGameOffline(d.config, "skyrimse")
	if err != nil || !report.Data.Recovered || report.Data.Pending != "" {
		t.Fatalf("offline recovery = %+v, %v", report, err)
	}
	batches, err := vfs.ListPreservedBatches(data)
	if err != nil || len(batches) != 1 || len(batches[0].Files) != 1 {
		t.Fatalf("offline preserved batches = %+v, %v", batches, err)
	}
	if _, err := os.Stat(filepath.Join(batches[0].Path, "files", "new.esp")); err != nil {
		t.Errorf("offline recovery lost Steam output: %v", err)
	}
	d.mountMgrs["skyrimse"].ResetAfterRestore()
}

// TestSteamBusyRecoveryRetainsFarm checks that startup recovery waits rather than capturing output during a Steam update.
func TestSteamBusyRecoveryRetainsFarm(t *testing.T) {
	d, data, manifest := newSteamMaintenanceDaemon(t)
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(data, "new.esp"))
	changeSteamFixture(t, manifest, "StateFlags", "4", "2")
	d2, err := New(d.config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d2.Shutdown)
	d2.readSteamAppState = steam.ReadAppState
	d2.RecoverAll()
	status, err := d2.GetVFSStatus("skyrimse")
	if err != nil || status.PendingRecovery == nil || status.SteamMaintenance != dto.SteamMaintenanceBusy {
		t.Fatalf("busy recovery = %+v, %v", status, err)
	}
	if _, err := os.Stat(filepath.Join(data, "new.esp")); err != nil {
		t.Errorf("Steam output disappeared during deferred recovery: %v", err)
	}
	requireSteamRefusal(t, d2.RestoreFromBackup("skyrimse", dto.RecoveryKindData, status.PendingRecovery.RecoveryID), "busy")
	changeSteamFixture(t, manifest, "StateFlags", "2", "4")
	if err := d2.RestoreFromBackup("skyrimse", dto.RecoveryKindData, status.PendingRecovery.RecoveryID); err != nil {
		t.Fatal(err)
	}
	d.mountMgrs["skyrimse"].ResetAfterRestore()
}

// TestPauseKeepsVerifyMarkerCreatedDuringUnmount checks that pausing after a changed Steam build never replaces the verification marker.
func TestPauseKeepsVerifyMarkerCreatedDuringUnmount(t *testing.T) {
	d, data, manifest := newSteamMaintenanceDaemon(t)
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatal(err)
	}
	changeSteamFixture(t, manifest, "buildid", "123", "124")
	status, err := d.SetSteamMaintenance("skyrimse", true, false)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := vfs.ReadMaintenance(data)
	if err != nil || marker == nil || marker.Reason != "verify" || status.SteamMaintenance != dto.SteamMaintenanceVerify {
		t.Fatalf("paused maintenance = %+v, marker = %+v, error = %v", status, marker, err)
	}
	if _, err := d.SetSteamMaintenance("skyrimse", false, false); !errors.Is(err, ErrVerificationConfirmationRequired) {
		t.Fatalf("finishing without verification = %v, want confirmation required", err)
	}
	if marker, err = vfs.ReadMaintenance(data); err != nil || marker == nil || marker.Reason != "verify" {
		t.Fatalf("verify marker after refused finish = %+v, %v", marker, err)
	}
}

// TestSteamMaintenanceBlocksMountAndLaunch checks retained verification refusal without reaching a launcher.
func TestSteamMaintenanceBlocksMountAndLaunch(t *testing.T) {
	d, _, manifest := newSteamMaintenanceDaemon(t)
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatal(err)
	}
	changeSteamFixture(t, manifest, "buildid", "123", "124")
	if err := d.UnmountVFS("skyrimse"); err != nil {
		t.Fatal(err)
	}
	_, err := d.LaunchGame("skyrimse", false, "Default")
	requireSteamRefusal(t, err, "verify")
	_, err = d.MountVFS("skyrimse", "Default")
	requireSteamRefusal(t, err, "verify")
}

// TestLoaderAdmissionRejectsSteamMaintenance checks that a retained marker refuses a loader mutation before any installer runs.
func TestLoaderAdmissionRejectsSteamMaintenance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	install, _ := steamFixture(t, 413150, "Stardew Valley")
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		"stardewvalley": {InstallPath: install, DataSubpath: "Mods", SteamAppID: 413150},
	})
	d.readSteamAppState = steam.ReadAppState
	marker := vfs.MaintenanceMarker{SchemaVersion: 1, Reason: "verify", GameID: "stardewvalley", CreatedAt: time.Now().UTC()}
	body, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := atomicfile.WriteFileDurable(vfs.MaintenancePath(filepath.Join(install, "Mods")), body, 0644); err != nil {
		t.Fatal(err)
	}
	op, err := d.ModLoaderService.admitLoaderOp(context.Background(), "stardewvalley")
	if op != nil {
		op.finish()
		t.Fatal("loader mutation was admitted during Steam maintenance")
	}
	requireSteamRefusal(t, err, "verify")
}

// TestNoSteamBaselineKeepsOverwriteCapture checks that an absent manifest retains normal output capture.
func TestNoSteamBaselineKeepsOverwriteCapture(t *testing.T) {
	d, data, manifest := newSteamMaintenanceDaemon(t)
	if err := os.Remove(manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(data, "new.esp"))
	if err := d.UnmountVFS("skyrimse"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(config.ModsDir("skyrimse"), "Overwrite", "new.esp")); err != nil {
		t.Fatalf("no-baseline capture lost output: %v", err)
	}
	if _, err := os.Lstat(vfs.MaintenancePath(data)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("unexpected maintenance marker: %v", err)
	}
}

// TestSteamChangedStatusReportsPreservedBatches checks maintenance state before and after teardown.
func TestSteamChangedStatusReportsPreservedBatches(t *testing.T) {
	d, data, manifest := newSteamMaintenanceDaemon(t)
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatal(err)
	}
	changeSteamFixture(t, manifest, "buildid", "123", "124")
	before, err := d.GetVFSStatus("skyrimse")
	if err != nil || !before.Mounted || before.SteamMaintenance != dto.SteamMaintenanceVerify {
		t.Fatalf("mounted status = %+v, %v", before, err)
	}
	requireSteamRefusal(t, d.RebuildVFS("skyrimse"), "verify")
	writeFixture(t, filepath.Join(data, "new.esp"))
	if err := d.UnmountVFS("skyrimse"); err != nil {
		t.Fatal(err)
	}
	after, err := d.GetVFSStatus("skyrimse")
	if err != nil || after.Mounted || after.SteamMaintenance != dto.SteamMaintenanceVerify || len(after.PreservedBatches) != 1 || after.PreservedBatches[0].FileCount != 1 {
		t.Fatalf("unmounted status = %+v, %v", after, err)
	}
}

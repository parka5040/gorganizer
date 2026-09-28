package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/vfs"
)

// TestMaintenanceSurvivesRestart checks that a user pause still prevents mounting after a restart.
func TestMaintenanceSurvivesRestart(t *testing.T) {
	d, data, _ := newSteamMaintenanceDaemon(t)
	status, err := d.SetSteamMaintenance("skyrimse", true, false)
	if err != nil || status.SteamMaintenance != dto.SteamMaintenanceUser {
		t.Fatalf("enable = %+v, %v", status, err)
	}
	d.Shutdown()
	restarted, err := New(d.config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.Shutdown)
	restarted.RecoverAll()
	marker, err := vfs.ReadMaintenance(data)
	if err != nil || marker == nil || marker.Reason != "user" {
		t.Fatalf("restarted marker = %+v, %v", marker, err)
	}
	_, err = restarted.MountVFS("skyrimse", "Default")
	requireSteamRefusal(t, err, "user")
}

// TestFinishMaintenanceRequiresIdleAndAcknowledgment checks verification, activity, and unreadable-manifest gates.
func TestFinishMaintenanceRequiresIdleAndAcknowledgment(t *testing.T) {
	d, data, manifest := newSteamMaintenanceDaemon(t)
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatal(err)
	}
	changeSteamFixture(t, manifest, "buildid", "123", "124")
	if err := d.UnmountVFS("skyrimse"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SetSteamMaintenance("skyrimse", false, false); !errors.Is(err, ErrVerificationConfirmationRequired) {
		t.Fatalf("finish without confirmation = %v", err)
	}
	changeSteamFixture(t, manifest, "StateFlags", "4", "2")
	if _, err := d.SetSteamMaintenance("skyrimse", false, true); err == nil {
		t.Fatal("finished while Steam was busy")
	} else {
		requireSteamRefusal(t, err, "busy")
	}
	changeSteamFixture(t, manifest, "StateFlags", "2", "4")
	if err := os.Remove(manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SetSteamMaintenance("skyrimse", false, false); !errors.Is(err, ErrVerificationConfirmationRequired) {
		t.Fatalf("finish without readable manifest and confirmation = %v", err)
	}
	status, err := d.SetSteamMaintenance("skyrimse", false, true)
	if err != nil || status.SteamMaintenance != dto.SteamMaintenanceNone {
		t.Fatalf("finish = %+v, %v", status, err)
	}
	if _, err := os.Lstat(vfs.MaintenancePath(data)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker remains: %v", err)
	}
	if _, err := d.SetSteamMaintenance("skyrimse", true, false); err != nil {
		t.Fatalf("pause with missing manifest: %v", err)
	}
	if _, err := d.SetSteamMaintenance("skyrimse", false, false); !errors.Is(err, ErrVerificationConfirmationRequired) {
		t.Fatalf("missing manifest without confirmation = %v", err)
	}
	if _, err := d.SetSteamMaintenance("skyrimse", false, true); err != nil {
		t.Fatalf("finish with missing manifest and confirmation: %v", err)
	}
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatalf("mount after verification: %v", err)
	}
}

// TestEnableMaintenanceUnmountsFirst checks the restored Data folder, idempotence, and Steam busy refusal.
func TestEnableMaintenanceUnmountsFirst(t *testing.T) {
	d, data, manifest := newSteamMaintenanceDaemon(t)
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatal(err)
	}
	changeSteamFixture(t, manifest, "StateFlags", "4", "2")
	if _, err := d.SetSteamMaintenance("skyrimse", true, false); err == nil {
		t.Fatal("paused while Steam was busy")
	} else {
		requireSteamRefusal(t, err, "busy")
	}
	if _, err := os.Lstat(vfs.MaintenancePath(data)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker written before unmount: %v", err)
	}
	changeSteamFixture(t, manifest, "StateFlags", "2", "4")
	d.procScan = func(string) (bool, error) { return true, nil }
	if _, err := d.SetSteamMaintenance("skyrimse", true, false); err == nil {
		t.Fatal("paused while the game was running")
	} else {
		var running *dto.GameRunningError
		if !errors.As(err, &running) {
			t.Fatalf("running game refusal = %v", err)
		}
	}
	d.procScan = func(string) (bool, error) { return false, nil }
	writeFixture(t, filepath.Join(data, "captured.esp"))
	status, err := d.SetSteamMaintenance("skyrimse", true, false)
	if err != nil || status.Mounted || status.SteamMaintenance != dto.SteamMaintenanceUser {
		t.Fatalf("pause = %+v, %v", status, err)
	}
	if _, err := os.Stat(filepath.Join(data, "original.esm")); err != nil {
		t.Fatalf("original game file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(config.ModsDir("skyrimse"), "Overwrite", "captured.esp")); err != nil {
		t.Fatalf("unmount did not capture output: %v", err)
	}
	marker, err := vfs.ReadMaintenance(data)
	if err != nil || marker == nil {
		t.Fatalf("marker = %+v, %v", marker, err)
	}
	if _, err := d.SetSteamMaintenance("skyrimse", true, false); err != nil {
		t.Fatalf("repeated pause: %v", err)
	}
	again, err := vfs.ReadMaintenance(data)
	if err != nil || !again.CreatedAt.Equal(marker.CreatedAt) {
		t.Fatalf("repeated pause changed marker: %+v, %v", again, err)
	}
}

// makePreservedFixture records disposable retained files beside a fixture deploy folder.
func makePreservedFixture(t *testing.T, data string) vfs.PreservedBatch {
	t.Helper()
	id := uuid.NewString()
	root := filepath.Join(vfs.PreservedDir(data), id)
	for rel, content := range map[string]string{"selected/file.esp": "selected", "other.esp": "other"} {
		path := filepath.Join(root, "files", rel)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	batch := vfs.PreservedBatch{SchemaVersion: 1, BatchID: id, GameID: "skyrimse", CreatedAt: time.Now().UTC(), Reason: "steam_changed", Files: []string{"selected/file.esp", "other.esp"}, Path: root}
	body, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := atomicfile.WriteFileDurable(filepath.Join(root, "batch.json"), body, 0644); err != nil {
		t.Fatal(err)
	}
	return batch
}

// TestPreservedImportIsDisabledAndContained checks selected-only copies, registration, and unsafe inputs.
func TestPreservedImportIsDisabledAndContained(t *testing.T) {
	d, data, _ := newSteamMaintenanceDaemon(t)
	if _, err := d.CreateProfile("skyrimse", "Default"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateProfile("skyrimse", "Other"); err != nil {
		t.Fatal(err)
	}
	batch := makePreservedFixture(t, data)
	if _, err := d.SetSteamMaintenance("skyrimse", true, false); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		path string
	}{
		{"parent", "../outside"},
		{"unlisted", "unlisted.esp"},
		{"unclean", "selected/../other.esp"},
		{"metadata", "metadata.yaml"},
		{"symlink", "linked.esp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "symlink" {
				if err := os.Symlink(filepath.Join(batch.Path, "files", "other.esp"), filepath.Join(batch.Path, "files", tc.path)); err != nil {
					t.Fatal(err)
				}
				batch.Files = append(batch.Files, tc.path)
				body, err := json.Marshal(batch)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := atomicfile.WriteFileDurable(filepath.Join(batch.Path, "batch.json"), body, 0644); err != nil {
					t.Fatal(err)
				}
			}
			_, _, err := d.ImportPreservedFiles("skyrimse", batch.BatchID, "Rejected", []string{tc.path})
			var unsafe *UnsafePathError
			if !errors.As(err, &unsafe) {
				t.Fatalf("import %q = %v, want unsafe path", tc.path, err)
			}
		})
	}
	name, count, err := d.ImportPreservedFiles("skyrimse", batch.BatchID, "Recovered Files", []string{"selected/file.esp"})
	if err != nil || name != "Recovered Files" || count != 1 {
		t.Fatalf("import = %q, %d, %v", name, count, err)
	}
	modDir := filepath.Join(config.ModsDir("skyrimse"), name)
	if body, err := os.ReadFile(filepath.Join(modDir, "selected", "file.esp")); err != nil || string(body) != "selected" {
		t.Fatalf("imported file = %q, %v", body, err)
	}
	if _, err := os.Lstat(filepath.Join(modDir, "other.esp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unselected file imported: %v", err)
	}
	meta, err := download.LoadModMetadata(modDir)
	if err != nil || meta.Name != name || meta.Enabled || len(meta.SourceArchives) != 0 || meta.FileCount != 1 {
		t.Fatalf("metadata = %+v, %v", meta, err)
	}
	for _, profile := range []string{"Default", "Other"} {
		entries, err := d.GetModList("skyrimse", profile)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, e := range entries {
			if e.ModName == name {
				found = true
				if e.Enabled {
					t.Errorf("mod enabled in %s", profile)
				}
			}
		}
		if !found {
			t.Errorf("mod absent from %s", profile)
		}
	}
	if body, err := os.ReadFile(filepath.Join(batch.Path, "files", "selected", "file.esp")); err != nil || string(body) != "selected" {
		t.Fatalf("batch was changed: %q, %v", body, err)
	}
	if _, _, err := d.ImportPreservedFiles("skyrimse", batch.BatchID, name, []string{"selected/file.esp"}); err == nil {
		t.Fatal("accepted an existing mod folder")
	} else {
		var collision *ModCollisionError
		if !errors.As(err, &collision) {
			t.Fatalf("collision = %v", err)
		}
	}
	if _, _, err := d.ImportPreservedFiles("skyrimse", "not-a-uuid", "New", []string{"selected/file.esp"}); err == nil {
		t.Fatal("accepted an invalid batch ID")
	}
	if _, err := d.SetSteamMaintenance("skyrimse", false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.ImportPreservedFiles("skyrimse", batch.BatchID, "Mounted Import", []string{"selected/file.esp"}); err != nil {
		t.Fatalf("import while mounted: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(data, "selected", "file.esp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled mod deployed while mounted: %v", err)
	}
}

// TestPreservedActionsRejectUnknownGames checks that no action reads or writes files for an unconfigured game.
func TestPreservedActionsRejectUnknownGames(t *testing.T) {
	d, _, _ := newSteamMaintenanceDaemon(t)
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"maintenance", func() error { _, err := d.SetSteamMaintenance("unknown", true, false); return err }},
		{"import", func() error {
			_, _, err := d.ImportPreservedFiles("unknown", uuid.NewString(), "Recovered", []string{"file.esp"})
			return err
		}},
		{"delete", func() error { _, err := d.DeletePreservedBatch("unknown", uuid.NewString()); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unknown game = %v, want not found", err)
			}
		})
	}
}

// TestDeletePreservedBatchUpdatesMarker checks that deletion removes the retained folder and its marker reference.
func TestDeletePreservedBatchUpdatesMarker(t *testing.T) {
	d, data, _ := newSteamMaintenanceDaemon(t)
	batch := makePreservedFixture(t, data)
	marker := &vfs.MaintenanceMarker{SchemaVersion: 1, Reason: "verify", GameID: "skyrimse", CreatedAt: time.Now().UTC(), BatchIDs: []string{batch.BatchID, uuid.NewString()}}
	if err := writeMaintenanceMarker(data, marker); err != nil {
		t.Fatal(err)
	}
	status, err := d.DeletePreservedBatch("skyrimse", batch.BatchID)
	if err != nil || len(status.PreservedBatches) != 0 || status.SteamMaintenance != dto.SteamMaintenanceVerify {
		t.Fatalf("delete = %+v, %v", status, err)
	}
	if _, err := os.Lstat(batch.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("batch directory remains: %v", err)
	}
	updated, err := vfs.ReadMaintenance(data)
	if err != nil || slices.Contains(updated.BatchIDs, batch.BatchID) || len(updated.BatchIDs) != 1 {
		t.Fatalf("marker = %+v, %v", updated, err)
	}
	if _, err := d.DeletePreservedBatch("skyrimse", "../escape"); err == nil || !strings.Contains(err.Error(), "batch_id") {
		t.Fatalf("invalid ID = %v", err)
	}
}

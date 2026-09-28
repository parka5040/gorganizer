package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/steam"
	"github.com/parka/gorganizer/internal/transfer"
	"github.com/parka/gorganizer/internal/vfs"
)

// TestFinishVerificationRefusedWhileDeployed ensures a changed Steam build cannot be acknowledged over a live farm.
func TestFinishVerificationRefusedWhileDeployed(t *testing.T) {
	d, data, manifest := newSteamMaintenanceDaemon(t)
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatal(err)
	}
	changeSteamFixture(t, manifest, "buildid", "123", "124")
	status, err := d.GetVFSStatus("skyrimse")
	if err != nil || status.SteamMaintenance != dto.SteamMaintenanceVerify || !status.Mounted {
		t.Fatalf("status = %+v, %v", status, err)
	}
	_, err = d.SetSteamMaintenance("skyrimse", false, true)
	requireSteamRefusal(t, err, "verify")
	if _, err := os.Stat(filepath.Join(data, vfs.SentinelFilename)); err != nil {
		t.Fatalf("farm lost after finish refusal: %v", err)
	}
	if marker, err := vfs.ReadMaintenance(data); err != nil || marker != nil {
		t.Fatalf("unexpected maintenance marker = %+v, %v", marker, err)
	}
}

// TestSharedInstallFinishRefusesOtherFarm checks verification cannot finish over a sibling's active Data farm.
func TestSharedInstallFinishRefusesOtherFarm(t *testing.T) {
	for _, mounted := range []string{"falloutnv", "ttw"} {
		t.Run(mounted, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			install, manifest := steamFixture(t, 22380, "Fallout New Vegas")
			d := newIsolatedDaemon(t, map[string]config.GameConfig{
				"falloutnv": {InstallPath: install, DataSubpath: "Data", SteamAppID: 22380},
				"ttw":       {InstallPath: filepath.Join(t.TempDir(), "synthetic"), DataSubpath: "Data", LinkedFromGameID: "falloutnv"},
			})
			d.readSteamAppState = steam.ReadAppState
			if _, err := d.MountVFS(mounted, "Default"); err != nil {
				t.Fatal(err)
			}
			changeSteamFixture(t, manifest, "buildid", "123", "124")
			other := "ttw"
			if mounted == "ttw" {
				other = "falloutnv"
			}
			_, err := d.SetSteamMaintenance(other, false, true)
			var busy *dto.OperationBusyError
			if !errors.As(err, &busy) || busy.GameID != other || busy.Holder != mounted || busy.Operation != dto.BusyOperationMounted {
				t.Fatalf("finish for %s while %s deployed = %v", other, mounted, err)
			}
			if _, err := os.Stat(filepath.Join(install, "Data", vfs.SentinelFilename)); err != nil {
				t.Fatalf("mounted farm changed: %v", err)
			}
			owner, err := d.GetVFSStatus(mounted)
			if err != nil || !owner.Mounted || owner.SteamMaintenance != dto.SteamMaintenanceVerify {
				t.Fatalf("owner status = %+v, %v", owner, err)
			}
		})
	}
}

// TestSiblingSteamStatusDoesNotReportPausedFarm checks a sibling never reports Verify plus unmounted while Data is deployed.
func TestSiblingSteamStatusDoesNotReportPausedFarm(t *testing.T) {
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
	changeSteamFixture(t, manifest, "buildid", "123", "124")
	status, err := d.GetVFSStatus("ttw")
	if err != nil || status.Mounted || status.SteamMaintenance != dto.SteamMaintenanceNone {
		t.Fatalf("TTW status over FNV farm = %+v, %v", status, err)
	}
}

// TestPauseFromChangedWhileDeployedPreservesAndMarks ensures pausing captures Steam changes before a verify marker is recorded.
func TestPauseFromChangedWhileDeployedPreservesAndMarks(t *testing.T) {
	d, data, manifest := newSteamMaintenanceDaemon(t)
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(data, "new.esp"))
	changeSteamFixture(t, manifest, "buildid", "123", "124")
	status, err := d.SetSteamMaintenance("skyrimse", true, false)
	if err != nil || status.Mounted || status.SteamMaintenance != dto.SteamMaintenanceVerify || len(status.PreservedBatches) != 1 {
		t.Fatalf("pause = %+v, %v", status, err)
	}
	if marker, err := vfs.ReadMaintenance(data); err != nil || marker == nil || marker.Reason != "verify" {
		t.Fatalf("marker = %+v, %v", marker, err)
	}
	if _, err := os.Stat(filepath.Join(status.PreservedBatches[0].Path, "files", "new.esp")); err != nil {
		t.Fatalf("changed file was not preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "original.esm")); err != nil {
		t.Fatalf("original Data not restored: %v", err)
	}
}

// TestFinishVerificationRefusedWhileRootDeployed checks that a live root-only deployment blocks Verify completion.
func TestFinishVerificationRefusedWhileRootDeployed(t *testing.T) {
	d, data, _ := newSteamMaintenanceDaemon(t)
	d.mu.Lock()
	root, err := d.ensureRootDeploymentManager("skyrimse", d.config.Games["skyrimse"])
	d.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	modRoot := t.TempDir()
	writeFixture(t, filepath.Join(modRoot, vfs.RootContentDirName, "root.txt"))
	if _, err := root.Apply([]vfs.Layer{{Name: "root-only", RootPath: modRoot, Enabled: true}}, "Default"); err != nil {
		t.Fatal(err)
	}
	if err := writeMaintenanceMarker(data, &vfs.MaintenanceMarker{SchemaVersion: 1, GameID: "skyrimse", Reason: "verify", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	_, err = d.SetSteamMaintenance("skyrimse", false, true)
	requireSteamRefusal(t, err, "verify")
	if _, err := os.Stat(filepath.Join(filepath.Dir(data), "root.txt")); err != nil {
		t.Fatalf("root deployment removed: %v", err)
	}
}

// TestSharedInstallPauseRefusesOtherDeployedGame checks both FNV and TTW cannot pause over each other's live deployment.
func TestSharedInstallPauseRefusesOtherDeployedGame(t *testing.T) {
	for _, mounted := range []string{"falloutnv", "ttw"} {
		t.Run(mounted, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			install, _ := steamFixture(t, 22380, "Fallout New Vegas")
			d := newIsolatedDaemon(t, map[string]config.GameConfig{
				"falloutnv": {InstallPath: install, DataSubpath: "Data", SteamAppID: 22380},
				"ttw":       {InstallPath: filepath.Join(t.TempDir(), "synthetic"), DataSubpath: "Data", LinkedFromGameID: "falloutnv"},
			})
			d.readSteamAppState = steam.ReadAppState
			if _, err := d.MountVFS(mounted, "Default"); err != nil {
				t.Fatal(err)
			}
			other := "ttw"
			if mounted == "ttw" {
				other = "falloutnv"
			}
			_, err := d.SetSteamMaintenance(other, true, false)
			var busy *dto.OperationBusyError
			if !errors.As(err, &busy) || busy.Holder != mounted || busy.Operation != dto.BusyOperationMounted {
				t.Fatalf("pause = %v, want mounted holder %s", err, mounted)
			}
			data := filepath.Join(install, "Data")
			if marker, err := vfs.ReadMaintenance(data); err != nil || marker != nil {
				t.Fatalf("marker after refusal = %+v, %v", marker, err)
			}
			status, err := d.GetVFSStatus(mounted)
			if err != nil || !status.Mounted {
				t.Fatalf("mounted game status = %+v, %v", status, err)
			}
		})
	}
}

// TestSharedInstallPauseRefusesRootDeployment checks the shared root manifest even when only its parent owns the deployment.
func TestSharedInstallPauseRefusesRootDeployment(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	install, _ := steamFixture(t, 22380, "Fallout New Vegas")
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		"falloutnv": {InstallPath: install, DataSubpath: "Data", SteamAppID: 22380},
		"ttw":       {InstallPath: filepath.Join(t.TempDir(), "synthetic"), DataSubpath: "Data", LinkedFromGameID: "falloutnv"},
	})
	d.mu.Lock()
	root, err := d.ensureRootDeploymentManager("falloutnv", d.config.Games["falloutnv"])
	d.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	modRoot := t.TempDir()
	writeFixture(t, filepath.Join(modRoot, vfs.RootContentDirName, "root.txt"))
	if _, err := root.Apply([]vfs.Layer{{Name: "root-only", RootPath: modRoot, Enabled: true}}, "Default"); err != nil {
		t.Fatal(err)
	}
	_, err = d.SetSteamMaintenance("ttw", true, false)
	var busy *dto.OperationBusyError
	if !errors.As(err, &busy) || busy.Holder != "falloutnv" || busy.Operation != dto.BusyOperationMounted {
		t.Fatalf("shared-root pause = %v", err)
	}
	if _, err := os.Stat(filepath.Join(install, "root.txt")); err != nil {
		t.Fatalf("deployed root file removed: %v", err)
	}
}

// TestImportRechecksDeploymentBeforePublish checks that mounting during extraction cannot overwrite a newly deployed mod.
func TestImportRechecksDeploymentBeforePublish(t *testing.T) {
	d, data, _ := newSteamMaintenanceDaemon(t)
	modFile := filepath.Join(config.ModsDir("skyrimse"), "A", "a.esp")
	writeFixture(t, modFile)
	if err := d.SetModList("skyrimse", "Default", []dto.ModListEntryResult{{ModName: "A", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "backup.tar.zst")
	if _, err := d.ExportInstance(context.Background(), dto.ExportRequest{GameID: "skyrimse", OutputPath: archive, ModFolders: []string{"A"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modFile, []byte("original stays"), 0644); err != nil {
		t.Fatal(err)
	}
	mounted := false
	_, err := d.ImportInstance(context.Background(), dto.ImportRequest{GameID: "skyrimse", ArchivePath: archive, Policy: dto.PolicyOverwrite}, func(p dto.TransferProgress) {
		if p.Step == "extract" && !mounted {
			mounted = true
			if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
				t.Errorf("mount at extraction barrier: %v", err)
			}
		}
	})
	var blocked *TransferOverwriteMountedError
	if !mounted || !errors.As(err, &blocked) || blocked.Name != "A" {
		t.Fatalf("import after mount = %v, barrier %v", err, mounted)
	}
	if body, err := os.ReadFile(modFile); err != nil || string(body) != "original stays" {
		t.Fatalf("replaced deployed original: %q, %v", body, err)
	}
	if body, err := os.ReadFile(filepath.Join(data, "a.esp")); err != nil || string(body) != "original stays" {
		t.Fatalf("farm after refusal: %q, %v", body, err)
	}
}

// TestImportBindsArchiveToPreview checks mismatches, equal-size replacements and an unchanged archive without changing the target on refusal.
func TestImportBindsArchiveToPreview(t *testing.T) {
	for _, tc := range []struct {
		name      string
		alter     func(*testing.T, string)
		wantError bool
	}{
		{"mismatched identity", func(t *testing.T, path string) {}, true},
		{"equal size and mtime replacement", func(t *testing.T, path string) {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			replacement := path + ".new"
			if err := os.WriteFile(replacement, body, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(replacement, path); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"unchanged", func(t *testing.T, path string) {}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _, _ := newSteamMaintenanceDaemon(t)
			modFile := filepath.Join(config.ModsDir("skyrimse"), "A", "a.esp")
			writeFixture(t, modFile)
			archive := filepath.Join(t.TempDir(), "backup.tar.zst")
			if _, err := d.ExportInstance(context.Background(), dto.ExportRequest{GameID: "skyrimse", OutputPath: archive, ModFolders: []string{"A"}}, nil); err != nil {
				t.Fatal(err)
			}
			preview, err := d.PreviewImport(context.Background(), "skyrimse", archive)
			if err != nil || !strings.HasPrefix(preview.ArchiveIdentity, "v1:") {
				t.Fatalf("preview = %+v, %v", preview, err)
			}
			if err := os.WriteFile(modFile, []byte("keep original"), 0644); err != nil {
				t.Fatal(err)
			}
			tc.alter(t, archive)
			identity := preview.ArchiveIdentity
			if tc.name == "mismatched identity" {
				identity += "wrong"
			}
			_, err = d.ImportInstance(context.Background(), dto.ImportRequest{GameID: "skyrimse", ArchivePath: archive, ExpectedArchiveIdentity: identity, Policy: dto.PolicyOverwrite}, nil)
			if tc.wantError {
				var rejected *transfer.BundleRejectedError
				if !errors.As(err, &rejected) || rejected.Reason != transfer.BundleRejectedChanged || rejected.Item != filepath.Base(archive) {
					t.Fatalf("changed archive = %v", err)
				}
				if body, readErr := os.ReadFile(modFile); readErr != nil || string(body) != "keep original" {
					t.Fatalf("refusal changed mod: %q, %v", body, readErr)
				}
			} else if err != nil {
				t.Fatalf("unchanged archive refused: %v", err)
			} else if body, readErr := os.ReadFile(modFile); readErr != nil || string(body) == "keep original" {
				t.Fatalf("unchanged archive was not imported: %q, %v", body, readErr)
			}
		})
	}
}

// TestImportRefusesArchiveChangedDuringExtraction checks that the final descriptor identity gates all publication.
func TestImportRefusesArchiveChangedDuringExtraction(t *testing.T) {
	d, _, _ := newSteamMaintenanceDaemon(t)
	modFile := filepath.Join(config.ModsDir("skyrimse"), "A", "a.esp")
	writeFixture(t, modFile)
	archive := filepath.Join(t.TempDir(), "backup.tar.zst")
	if _, err := d.ExportInstance(context.Background(), dto.ExportRequest{GameID: "skyrimse", OutputPath: archive, ModFolders: []string{"A"}}, nil); err != nil {
		t.Fatal(err)
	}
	preview, err := d.PreviewImport(context.Background(), "skyrimse", archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modFile, []byte("keep original"), 0644); err != nil {
		t.Fatal(err)
	}
	changed := false
	_, err = d.ImportInstance(context.Background(), dto.ImportRequest{GameID: "skyrimse", ArchivePath: archive, ExpectedArchiveIdentity: preview.ArchiveIdentity, Policy: dto.PolicyOverwrite}, func(p dto.TransferProgress) {
		if p.Step != "extract" || changed {
			return
		}
		changed = true
		f, openErr := os.OpenFile(archive, os.O_RDWR, 0)
		if openErr != nil {
			t.Error(openErr)
			return
		}
		if _, writeErr := f.WriteAt([]byte{0}, 0); writeErr != nil {
			t.Error(writeErr)
		}
		if closeErr := f.Close(); closeErr != nil {
			t.Error(closeErr)
		}
		info, statErr := os.Stat(archive)
		if statErr != nil {
			t.Error(statErr)
			return
		}
		if timeErr := os.Chtimes(archive, info.ModTime().Add(2*time.Second), info.ModTime().Add(2*time.Second)); timeErr != nil {
			t.Error(timeErr)
		}
	})
	var rejected *transfer.BundleRejectedError
	if !changed || !errors.As(err, &rejected) || rejected.Reason != transfer.BundleRejectedChanged {
		t.Fatalf("import after rewrite = %v, extraction observed %t", err, changed)
	}
	if body, readErr := os.ReadFile(modFile); readErr != nil || string(body) != "keep original" {
		t.Fatalf("original mod changed: %q, %v", body, readErr)
	}
	stages, globErr := filepath.Glob(filepath.Join(config.ModsDir("skyrimse"), ".gorganizer-import-*"))
	if globErr != nil || len(stages) != 0 {
		t.Fatalf("import staging left behind: %v, %v", stages, globErr)
	}
}

// TestLinkedPreservedActionsRaceConfigure checks that a linked game's effective install path is safe during reconfiguration.
func TestLinkedPreservedActionsRaceConfigure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	install, _ := steamFixture(t, 22380, "Fallout New Vegas")
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		"falloutnv": {InstallPath: install, DataSubpath: "Data", SteamAppID: 22380},
		"ttw":       {InstallPath: filepath.Join(t.TempDir(), "synthetic"), DataSubpath: "Data", LinkedFromGameID: "falloutnv"},
	})
	data := filepath.Join(install, "Data")
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 12; i++ {
			batch := vfs.PreservedBatch{SchemaVersion: 1, BatchID: uuid.NewString(), GameID: "ttw", CreatedAt: time.Now().UTC(), Reason: "steam_changed", Files: []string{"file.esp"}}
			root := filepath.Join(vfs.PreservedDir(data), batch.BatchID)
			if err := os.MkdirAll(filepath.Join(root, "files"), 0755); err != nil {
				errs <- err
				return
			}
			if err := os.WriteFile(filepath.Join(root, "files", "file.esp"), []byte("saved"), 0644); err != nil {
				errs <- err
				return
			}
			body, err := json.Marshal(batch)
			if err != nil {
				errs <- err
				return
			}
			if _, err := atomicfile.WriteFileDurable(filepath.Join(root, "batch.json"), body, 0644); err != nil {
				errs <- err
				return
			}
			if _, _, err := d.ImportPreservedFiles("ttw", batch.BatchID, "Recovered "+uuid.NewString(), []string{"file.esp"}); err != nil {
				errs <- err
				return
			}
			if _, err := d.DeletePreservedBatch("ttw", batch.BatchID); err != nil {
				errs <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 25; i++ {
			if err := d.ConfigureGame("falloutnv", "Fallout New Vegas", 22380, install, "Data"); err != nil {
				errs <- err
				return
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent action: %v", err)
	}
}

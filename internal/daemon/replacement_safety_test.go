package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
)

// interruptedReinstall moves an installed mod aside and returns its unfinished journal.
func interruptedReinstall(t *testing.T, d *Daemon, folder string) (string, string, reinstallIntent) {
	t.Helper()
	modsDir := config.ModsDir("skyrimse")
	d.reinstallFault = func(step string) error {
		if step == "moved-aside" {
			return errSimulatedCrash
		}
		return nil
	}
	if _, _, _, err := d.ReinstallMod(context.Background(), "skyrimse", folder, ""); !errors.Is(err, errSimulatedCrash) {
		t.Fatalf("ReinstallMod = %v, want interrupted swap", err)
	}
	d.reinstallFault = nil
	entries, err := os.ReadDir(modsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), reinstallIntentPrefix) {
			path := filepath.Join(modsDir, entry.Name())
			intent, err := readReinstallIntent(path)
			if err != nil {
				t.Fatal(err)
			}
			return modsDir, path, intent
		}
	}
	t.Fatal("interrupted reinstall left no intent")
	return "", "", reinstallIntent{}
}

// TestPendingReinstallReservesName checks every mod mutation rejects an unfinished replacement even with no live target.
func TestPendingReinstallReservesName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newStardewDaemon(t)
	folder, archive := installForReinstall(t, d, "skyrimse", "Reserved", map[string]string{"plugin.esp": "original"}, false)
	other, _ := installForReinstall(t, d, "skyrimse", "Other", map[string]string{"other.esp": "other"}, false)
	bundle := filepath.Join(t.TempDir(), "saved.tar.zst")
	if _, err := d.ExportInstance(context.Background(), dto.ExportRequest{GameID: "skyrimse", OutputPath: bundle, ModFolders: []string{folder}}, nil); err != nil {
		t.Fatalf("ExportInstance: %v", err)
	}
	modsDir, _, intent := interruptedReinstall(t, d, folder)
	old := snapshotTree(t, filepath.Join(modsDir, intent.Old))
	checks := []struct {
		name string
		run  func() error
	}{
		{"new install", func() error {
			_, _, err := d.StartInstall(context.Background(), dto.StartInstallRequest{GameID: "skyrimse", ArchiveRelPath: filepath.Base(archive), Mode: dto.InstallAsNewMod, TargetMod: folder})
			return err
		}},
		{"merge", func() error {
			_, _, err := d.StartInstall(context.Background(), dto.StartInstallRequest{GameID: "skyrimse", ArchiveRelPath: filepath.Base(archive), Mode: dto.InstallMergeIntoMod, TargetMod: folder})
			return err
		}},
		{"rename from", func() error { return d.RenameMod("skyrimse", folder, "Fresh") }},
		{"rename to", func() error { return d.RenameMod("skyrimse", other, folder) }},
		{"reinstall", func() error { _, _, _, err := d.ReinstallMod(context.Background(), "skyrimse", folder, ""); return err }},
		{"uninstall", func() error { _, err := d.UninstallMod("skyrimse", folder, true); return err }},
		{"import", func() error {
			_, err := d.ImportInstance(context.Background(), dto.ImportRequest{GameID: "skyrimse", ArchivePath: bundle, Policy: dto.PolicySkip}, nil)
			return err
		}},
	}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			var pending *download.ReplacementPendingError
			if err := tc.run(); !errors.As(err, &pending) || pending.Name != folder {
				t.Fatalf("error = %v, want pending replacement of %q", err, folder)
			}
			if got := snapshotTree(t, filepath.Join(modsDir, intent.Old)); !reflect.DeepEqual(got, old) {
				t.Fatalf("previous copy changed: %v", got)
			}
		})
	}
}

// TestPendingTransferJournalReservesDaemonModName checks daemon mutations honor transfer journals for absent names.
func TestPendingTransferJournalReservesDaemonModName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newStardewDaemon(t)
	other, archive := installForReinstall(t, d, "skyrimse", "Other", map[string]string{"other.esp": "original"}, false)
	modsDir := config.ModsDir("skyrimse")
	id := strings.Repeat("c", 32)
	journal := filepath.Join(modsDir, ".gorganizer-transfer-intent-"+id+".json")
	data := `{"schema_version":1,"op_id":"` + id + `","kind":"mod","name":"Reserved","staged":".transfer-stage-` + id + `","old":".transfer-old-` + id + `"}`
	if _, err := atomicfile.WriteFileDurable(journal, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name string
		run  func() error
	}{
		{"install", func() error {
			_, _, err := d.StartInstall(context.Background(), dto.StartInstallRequest{GameID: "skyrimse", ArchiveRelPath: filepath.Base(archive), Mode: dto.InstallAsNewMod, TargetMod: "Reserved"})
			return err
		}},
		{"rename to", func() error { return d.RenameMod("skyrimse", other, "Reserved") }},
	}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			var pending *download.ReplacementPendingError
			if err := tc.run(); !errors.As(err, &pending) || pending.Name != "Reserved" {
				t.Fatalf("error = %v, want pending Reserved", err)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(modsDir, "Reserved")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("reserved folder was created: %v", err)
	}
}

// TestRecoveryKeepsOldWhenTargetIsForeign checks a foreign target never causes deletion of the only good original.
func TestRecoveryKeepsOldWhenTargetIsForeign(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "with-identity"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			d := newStardewDaemon(t)
			folder, _ := installForReinstall(t, d, "skyrimse", "Reserved", map[string]string{"plugin.esp": "original"}, false)
			modsDir, path, intent := interruptedReinstall(t, d, folder)
			old := snapshotTree(t, filepath.Join(modsDir, intent.Old))
			if intent.StageIdentity == nil {
				t.Fatal("new intent has no stage identity")
			}
			if legacy {
				intent.SchemaVersion = 1
				intent.StageIdentity = nil
				if err := writeReinstallIntent(path, intent); err != nil {
					t.Fatal(err)
				}
			}
			foreign := filepath.Join(modsDir, folder)
			writeFixture(t, filepath.Join(foreign, "foreign.esp"))
			restartDaemon(t, d)
			if _, err := os.Stat(filepath.Join(foreign, "foreign.esp")); err != nil {
				t.Errorf("foreign target changed: %v", err)
			}
			token := strings.TrimPrefix(intent.Old, reinstallOldPrefix)
			recovered := filepath.Join(modsDir, ".gorganizer-recovered-"+token)
			if got := snapshotTree(t, recovered); !reflect.DeepEqual(got, old) {
				t.Errorf("preserved original = %v, want %v", got, old)
			}
			if err := download.ValidateTargetModName(filepath.Base(recovered)); err == nil {
				t.Error("recovered folder could be installed as a mod")
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("recovered journal remains: %v", err)
			}
		})
	}
}

// TestReplacementSyncsFilesystemBeforeRemovingOld checks staged and swapped data are flushed before the old copy is removed.
func TestReplacementSyncsFilesystemBeforeRemovingOld(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newStardewDaemon(t)
	folder, _ := installForReinstall(t, d, "skyrimse", "Reserved", map[string]string{"plugin.esp": "original"}, false)
	var steps []string
	d.reinstallFault = func(step string) error {
		steps = append(steps, step)
		return nil
	}
	if _, _, _, err := d.ReinstallMod(context.Background(), "skyrimse", folder, ""); err != nil {
		t.Fatal(err)
	}
	d.reinstallFault = nil
	position := func(step string) int {
		for i, recorded := range steps {
			if recorded == step {
				return i
			}
		}
		t.Fatalf("missing step %q: %v", step, steps)
		return -1
	}
	for _, pair := range [][2]string{{"sync-filesystem", "sync-stage-dir"}, {"sync-stage-dir", "move-aside"}, {"install", "sync-swap-dir"}, {"sync-swap-dir", "remove-old"}} {
		if position(pair[0]) >= position(pair[1]) {
			t.Errorf("wrong order for %s and %s: %v", pair[0], pair[1], steps)
		}
	}
}

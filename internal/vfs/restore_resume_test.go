package vfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestConfirmedRestoreCrashResumesAtEveryStep checks every interrupted cleanup reaches plain Data without losing captured output.
func TestConfirmedRestoreCrashResumesAtEveryStep(t *testing.T) {
	for step := 0; step <= 7; step++ {
		t.Run(fmt.Sprintf("step-%d", step), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			root := t.TempDir()
			data := filepath.Join(root, "Data")
			overwrite := filepath.Join(root, "Overwrite")
			mustFile(t, filepath.Join(data, "original.esp"), "original bytes")
			mm := NewMountManager(data, overwrite, "testgame")
			if err := mm.Activate([]Layer{{Name: "__base__", RootPath: data, Enabled: true}}, "Default"); err != nil {
				t.Fatal(err)
			}
			mustFile(t, filepath.Join(data, "output.esp"), "new output")
			if err := WriteIntent(applyingIntentPath(data), &ActivationIntent{SchemaVersion: 2, Magic: IntentMagic, Kind: IntentApplying, DataPath: data, BackupPath: data + farmBackupSuffix, StagingPath: stagingDirPath(data), LiveFarmID: "leftover", StagingFarmID: "unfinished"}); err != nil {
				t.Fatal(err)
			}
			for _, sibling := range []string{stagingDirPath(data), oldFarmPath(data)} {
				if err := os.Mkdir(sibling, 0700); err != nil {
					t.Fatal(err)
				}
			}
			stopped := errors.New("simulated crash")
			old := restoreStep
			restoreStep = func(n int) error {
				if n == step {
					return stopped
				}
				return nil
			}
			t.Cleanup(func() { restoreStep = old })
			if err := RestoreFromBackup(data); !errors.Is(err, stopped) {
				t.Fatalf("injected restore = %v", err)
			}
			restoreStep = old
			outcome, err := CleanupStale(data)
			if err != nil {
				t.Fatal(err)
			}
			if outcome.Pending != nil {
				if err := RestoreFromBackup(data); err != nil {
					t.Fatalf("resume after %+v: %v", outcome.Pending, err)
				}
			}
			outcome, err = CleanupStale(data)
			if err != nil || outcome.Pending != nil {
				t.Fatalf("final recovery = %+v, %v", outcome, err)
			}
			if got := mustRead(t, filepath.Join(data, "original.esp")); got != "original bytes" {
				t.Fatalf("restored original = %q", got)
			}
			if got := mustRead(t, filepath.Join(overwrite, "output.esp")); got != "new output" {
				t.Fatalf("captured output = %q", got)
			}
			for _, suffix := range FarmSiblingSuffixes() {
				if _, err := os.Lstat(data + suffix); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("leftover %s: %v", suffix, err)
				}
			}
			if _, err := os.Lstat(filepath.Join(data, SentinelFilename)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("farm sentinel remains: %v", err)
			}
		})
	}
}

// TestRestoreRecordMismatchStaysPending checks that a foreign Data directory or backup is never removed on retry or startup.
func TestRestoreRecordMismatchStaysPending(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	data := filepath.Join(root, "Data")
	mustFile(t, filepath.Join(data, "original"), "original")
	backup, _, err := directoryAt(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(data, data+farmBackupSuffix); err != nil {
		t.Fatal(err)
	}
	mustFile(t, filepath.Join(data, "foreign"), "keep")
	if err := writeRestoreRecord(data, &restoreRecord{SchemaVersion: 1, DataPath: data, Backup: backup}); err != nil {
		t.Fatal(err)
	}
	outcome, err := CleanupStale(data)
	if err != nil || outcome.Pending == nil {
		t.Fatalf("foreign recovery = %+v, %v", outcome, err)
	}
	if err := RestoreFromBackup(data); err == nil {
		t.Fatal("foreign Data was accepted")
	}
	if got := mustRead(t, filepath.Join(data, "foreign")); got != "keep" {
		t.Fatalf("foreign Data changed: %q", got)
	}
}

// TestRestoreRecordChangedBackupStaysPending checks that backup replacement does not consume an unrelated directory.
func TestRestoreRecordChangedBackupStaysPending(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	data := filepath.Join(t.TempDir(), "Data")
	mustFile(t, filepath.Join(data, "original"), "saved")
	backup, _, err := directoryAt(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(data, data+farmBackupSuffix); err != nil {
		t.Fatal(err)
	}
	if err := writeRestoreRecord(data, &restoreRecord{SchemaVersion: 1, DataPath: data, Backup: backup}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(data+farmBackupSuffix, data+".detached"); err != nil {
		t.Fatal(err)
	}
	mustFile(t, filepath.Join(data+farmBackupSuffix, "foreign"), "keep")
	outcome, err := CleanupStale(data)
	if err != nil || outcome.Pending == nil {
		t.Fatalf("changed backup recovery = %+v, %v", outcome, err)
	}
	if err := RestoreFromBackup(data); err == nil {
		t.Fatal("changed backup was accepted")
	}
	if got := mustRead(t, filepath.Join(data+farmBackupSuffix, "foreign")); got != "keep" {
		t.Fatalf("foreign backup changed: %q", got)
	}
	if got := mustRead(t, filepath.Join(data+".detached", "original")); got != "saved" {
		t.Fatalf("original backup changed: %q", got)
	}
}

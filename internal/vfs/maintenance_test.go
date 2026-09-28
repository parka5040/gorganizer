package vfs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestPreservedBatchRetryResumesPartialCapture checks that a failed move retries into the same batch without losing previous output.
func TestPreservedBatchRetryResumesPartialCapture(t *testing.T) {
	data, overwrite, mm := teardownFixture(t)
	mustFile(t, filepath.Join(data, "Other", "new.esp"), "plugin")
	baseline := &StorefrontSnapshot{Store: "steam", AppID: 489830, BuildID: "123", CapturedAt: time.Now().UTC()}
	current := &StorefrontSnapshot{Store: "steam", AppID: 489830, BuildID: "124", CapturedAt: time.Now().UTC()}
	opts := CaptureOptions{PreserveInto: PreservedDir(data), BatchID: uuid.NewString(), Baseline: baseline, Current: current}
	original := captureRename
	captureRename = func(src, dst string) error {
		if strings.HasSuffix(src, filepath.Join("Saves", "new.ess")) {
			return errors.New("simulated move failure")
		}
		return original(src, dst)
	}
	t.Cleanup(func() { captureRename = original })
	if err := mm.DeactivateWithOptions(opts); !errors.Is(err, ErrCaptureFailed) {
		t.Fatalf("failed capture = %v, want ErrCaptureFailed", err)
	}
	captureRename = original
	outcome, err := CleanupStale(data)
	if err != nil || outcome.Pending != nil || !outcome.Restored {
		t.Fatalf("capture retry = %+v, %v", outcome, err)
	}
	batches, err := ListPreservedBatches(data)
	if err != nil || len(batches) != 1 || len(batches[0].Files) != 2 {
		t.Fatalf("recovered batches = %+v, %v", batches, err)
	}
	for _, rel := range []string{"Other/new.esp", "Saves/new.ess"} {
		if _, err := os.Stat(filepath.Join(batches[0].Path, "files", filepath.FromSlash(rel))); err != nil {
			t.Errorf("preserved %s: %v", rel, err)
		}
		if _, err := os.Stat(filepath.Join(overwrite, filepath.FromSlash(rel))); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("output %s entered Overwrite: %v", rel, err)
		}
	}
}

// TestInterruptedRetiredRemovalAfterPreservationFinishes resumes partial removal after preserving Steam output.
func TestInterruptedRetiredRemovalAfterPreservationFinishes(t *testing.T) {
	for _, metadata := range []string{"sentinel", "manifest"} {
		for _, confirm := range []bool{false, true} {
			name := metadata + "/recovery"
			if confirm {
				name = metadata + "/confirmed restore"
			}
			t.Run(name, func(t *testing.T) {
				data, overwrite, mm := teardownFixture(t)
				baseline := &StorefrontSnapshot{Store: "steam", AppID: 489830, BuildID: "123", CapturedAt: time.Now().UTC()}
				current := &StorefrontSnapshot{Store: "steam", AppID: 489830, BuildID: "124", CapturedAt: time.Now().UTC()}
				opts := CaptureOptions{PreserveInto: PreservedDir(data), BatchID: uuid.NewString(), Baseline: baseline, Current: current}
				old := removeRetiredFarm
				t.Cleanup(func() { removeRetiredFarm = old })
				stopped := errors.New("simulated incomplete retired folder removal")
				removeRetiredFarm = func(path string) error {
					s, err := ReadSentinel(path)
					if err != nil {
						return err
					}
					name := SentinelFilename
					if metadata == "manifest" {
						name = s.Manifest
					}
					if err := os.Remove(filepath.Join(path, name)); err != nil {
						return err
					}
					if err := os.Remove(filepath.Join(path, "Skyrim.esm")); err != nil {
						return err
					}
					return stopped
				}
				if err := mm.DeactivateWithOptions(opts); !errors.Is(err, stopped) {
					t.Fatalf("DeactivateWithOptions = %v, want interruption", err)
				}
				removeRetiredFarm = old
				if confirm {
					if err := RestoreFromBackup(data); err != nil {
						t.Fatalf("RestoreFromBackup: %v", err)
					}
				} else {
					outcome, err := CleanupStale(data)
					if err != nil || outcome.Pending != nil || !outcome.Restored {
						t.Fatalf("CleanupStale = %+v, %v; want restored", outcome, err)
					}
				}
				if got := mustRead(t, filepath.Join(data, "Skyrim.esm")); got != "original master\x00bytes" {
					t.Errorf("restored original = %q", got)
				}
				batches, err := ListPreservedBatches(data)
				if err != nil || len(batches) != 1 || len(batches[0].Files) != 1 || batches[0].Files[0] != "Saves/new.ess" {
					t.Fatalf("preserved batches = %+v, %v", batches, err)
				}
				if got := mustRead(t, filepath.Join(batches[0].Path, "files", "Saves", "new.ess")); got != "new save" {
					t.Errorf("preserved save = %q", got)
				}
				if _, err := os.Lstat(filepath.Join(overwrite, "Saves", "new.ess")); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("preserved save entered Overwrite: %v", err)
				}
				for _, path := range []string{retiredFarmPath(data), deactivationJournalPath(data)} {
					if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
						t.Errorf("teardown leftover %s remains: %v", path, err)
					}
				}
			})
		}
	}
}

// TestPreservedBatchSurvivesInterruptedTeardown checks that the durable capture decision survives every retirement boundary.
func TestPreservedBatchSurvivesInterruptedTeardown(t *testing.T) {
	for _, step := range []int{1, 2, 3, 4} {
		t.Run(string(rune('0'+step)), func(t *testing.T) {
			data, overwrite, mm := teardownFixture(t)
			baseline := &StorefrontSnapshot{Store: "steam", AppID: 489830, BuildID: "123", CapturedAt: time.Now().UTC()}
			current := &StorefrontSnapshot{Store: "steam", AppID: 489830, BuildID: "124", CapturedAt: time.Now().UTC()}
			opts := CaptureOptions{PreserveInto: PreservedDir(data), BatchID: uuid.NewString(), Baseline: baseline, Current: current}
			stopped := stopDeactivationAt(t, step)
			if err := mm.DeactivateWithOptions(opts); !errors.Is(err, stopped) {
				t.Fatalf("interrupted teardown = %v, want simulated stop", err)
			}
			journal, err := readDeactivationJournal(deactivationJournalPath(data))
			if err != nil || journal.DataCaptured != (step >= 2) || journal.RetiredCaptured != (step >= 4) {
				t.Fatalf("journal capture state after step %d = %+v, %v", step, journal, err)
			}
			deactivationStep = func(int) error { return nil }
			outcome, err := CleanupStale(data)
			if err != nil || outcome.Pending != nil || !outcome.Restored {
				t.Fatalf("recovery = %+v, %v", outcome, err)
			}
			batches, err := ListPreservedBatches(data)
			if err != nil || len(batches) != 1 || len(batches[0].Files) != 1 || batches[0].Files[0] != "Saves/new.ess" {
				t.Fatalf("preserved batches = %+v, %v", batches, err)
			}
			if got := mustRead(t, filepath.Join(batches[0].Path, "files", "Saves", "new.ess")); got != "new save" {
				t.Errorf("preserved output = %q", got)
			}
			if _, err := os.Stat(filepath.Join(overwrite, "Saves", "new.ess")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("Steam write entered Overwrite: %v", err)
			}
			if marker, err := ReadMaintenance(data); err != nil || marker == nil || marker.Reason != "verify" || len(marker.BatchIDs) != 1 {
				t.Errorf("maintenance marker = %+v, %v", marker, err)
			}
		})
	}
}

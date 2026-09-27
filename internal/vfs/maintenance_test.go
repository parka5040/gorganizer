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

package transfer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
)

// TestPartialImportPendingRecovery counts a published mod when its old folder cannot be removed and leaves the journal for startup.
func TestPartialImportPendingRecovery(t *testing.T) {
	archive := exportTestArchive(t)
	setRoot(t, t.TempDir())
	seedCollisions(t)
	ops := defaultTransferCommitOps()
	ops.remove = func(step, path string) error {
		if step == "remove-old" {
			return errors.New("injected cleanup failure")
		}
		return os.RemoveAll(path)
	}
	sum, err := Import(context.Background(), ImportOptions{GameID: testGame, ArchivePath: archive, Policy: dto.PolicyOverwrite, commitOps: &ops}, nil)
	var incomplete *BundleIncompleteError
	if !errors.As(err, &incomplete) || incomplete.Items != 1 || incomplete.Recovery != "pending" {
		t.Fatalf("error = %v, summary = %+v; want one committed item pending recovery", err, sum)
	}
	if got := readFileT(t, filepath.Join(config.ModsDir(testGame), "Alpha Mod", "AlphaMod.esp")); got != "alpha-esp-payload" {
		t.Errorf("published mod content = %q", got)
	}
	if err := RecoverTransfers(config.ModsDir(testGame)); err != nil {
		t.Fatal(err)
	}
	if got := readFileT(t, filepath.Join(config.ModsDir(testGame), "Alpha Mod", "AlphaMod.esp")); got != "alpha-esp-payload" {
		t.Errorf("recovered mod content = %q", got)
	}
}

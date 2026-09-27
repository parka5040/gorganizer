package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
)

// TestTransferReplacementFaultMatrix verifies every swap fault preserves the previous directory or a recoverable replacement.
func TestTransferReplacementFaultMatrix(t *testing.T) {
	for _, tc := range []struct {
		name       string
		kind       string
		fail       string
		wantBefore string
		wantAfter  string
	}{
		{"stage", "mod", "stage", "old", "old"},
		{"sync-stage", "mod", "sync-stage", "old", "old"},
		{"sync-filesystem", "mod", "sync-filesystem", "old", "old"},
		{"sync-swap", "mod", "sync-swap", "new", "new"},
		{"sync-rollback", "mod", "sync-rollback", "old", "old"},
		{"move-aside", "mod", "move-aside", "old", "old"},
		{"install", "mod", "install", "old", "old"},
		{"restore", "mod", "restore", "", "old"},
		{"discard-stage", "mod", "discard-stage", "old", "old"},
		{"remove-old", "mod", "remove-old", "new", "new"},
		{"remove-intent", "mod", "remove-intent", "new", "new"},
		{"profile-install", "profile", "install", "old", "old"},
		{"profile-remove-old", "profile", "remove-old", "new", "new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			name := "Existing"
			writeFileT(t, filepath.Join(root, name, "content"), "old")
			staged := filepath.Join(root, ".gorganizer-import-fixture", name)
			writeFileT(t, filepath.Join(staged, "content"), "new")
			ops := defaultTransferCommitOps()
			ops.rename = func(step, from, to string) error {
				if step == tc.fail || (tc.fail == "restore" || tc.fail == "sync-rollback") && step == "install" {
					return errors.New("injected rename failure")
				}
				if tc.fail == "discard-stage" && step == "install" {
					return errors.New("injected install failure")
				}
				return os.Rename(from, to)
			}
			ops.remove = func(step, path string) error {
				if step == tc.fail {
					return errors.New("injected remove failure")
				}
				return os.RemoveAll(path)
			}
			ops.removeIntent = func(path string) error {
				if tc.fail == "remove-intent" {
					return errors.New("injected intent removal failure")
				}
				return atomicfile.RemoveDurable(path)
			}
			syncCalls := 0
			ops.sync = func(path string) error {
				syncCalls++
				if tc.fail == "sync-stage" && syncCalls == 1 ||
					(tc.fail == "sync-swap" || tc.fail == "sync-rollback") && syncCalls == 3 {
					return errors.New("injected sync failure")
				}
				return atomicfile.SyncDir(path)
			}
			ops.syncFilesystem = func(path string) error {
				if tc.fail == "sync-filesystem" {
					return errors.New("injected filesystem sync failure")
				}
				return atomicfile.SyncFilesystem(path)
			}
			if err := replaceDirWithOps(root, name, staged, tc.kind, ops); err == nil {
				t.Fatal("replacement succeeded despite injected fault")
			}
			if tc.wantBefore != "" && readFileT(t, filepath.Join(root, name, "content")) != tc.wantBefore {
				t.Fatalf("target lost its expected content before recovery")
			}
			if tc.wantBefore == "" {
				entries, err := os.ReadDir(root)
				if err != nil {
					t.Fatal(err)
				}
				foundOld := false
				for _, e := range entries {
					if strings.HasPrefix(e.Name(), transferOldPrefix) {
						foundOld = readFileT(t, filepath.Join(root, e.Name(), "content")) == "old"
					}
				}
				if !foundOld {
					t.Fatal("original absent from both target and backup")
				}
			}
			if err := RecoverTransfers(root); err != nil {
				t.Fatal(err)
			}
			if got := readFileT(t, filepath.Join(root, name, "content")); got != tc.wantAfter {
				t.Errorf("recovered content = %q, want %q", got, tc.wantAfter)
			}
		})
	}
}

// writeTransferIntentFixture creates a journal in root for simulated interruption states.
func writeTransferIntentFixture(t *testing.T, root, kind, name, id string) transferIntent {
	t.Helper()
	intent := transferIntent{SchemaVersion: 1, OpID: id, Kind: kind, Name: name,
		Staged: transferStagePrefix + id, Old: transferOldPrefix + id}
	data, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, transferIntentPrefix+id+transferIntentSuffix)
	if _, err := atomicfile.WriteFileDurable(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	return intent
}

// TestTransferRecoveryIdempotent checks that recovery restores, installs or keeps a committed target once and then has no further effect.
func TestTransferRecoveryIdempotent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		old    bool
		staged bool
		target bool
		want   string
	}{
		{"restore-original", true, true, false, "old"},
		{"install-staged", false, true, false, "staged"},
		{"keep-committed", true, true, true, "target"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			id := strings.Repeat("a", 32)
			intent := writeTransferIntentFixture(t, root, "profile", "Existing", id)
			if tc.old {
				writeFileT(t, filepath.Join(root, intent.Old, "content"), "old")
			}
			if tc.staged {
				writeFileT(t, filepath.Join(root, intent.Staged, "content"), "staged")
			}
			if tc.target {
				writeFileT(t, filepath.Join(root, intent.Name, "content"), "target")
			}
			for i := 0; i < 2; i++ {
				if err := RecoverTransfers(root); err != nil {
					t.Fatal(err)
				}
				if tc.target && tc.old {
					if got := readFileT(t, filepath.Join(root, ".gorganizer-recovered-"+id, "content")); got != "old" {
						t.Errorf("recovery %d preserved original = %q, want old", i, got)
					}
				}
				if got := readFileT(t, filepath.Join(root, intent.Name, "content")); got != tc.want {
					t.Errorf("recovery %d content = %q, want %q", i, got, tc.want)
				}
				for _, unused := range []string{intent.Old, intent.Staged, transferIntentPrefix + id + transferIntentSuffix} {
					if _, err := os.Lstat(filepath.Join(root, unused)); !errors.Is(err, os.ErrNotExist) {
						t.Errorf("recovery %d left %s: %v", i, unused, err)
					}
				}
			}
		})
	}
}

// TestTransferJournalCannotEscapeRoot verifies unsafe recorded names never move or delete files outside the transfer root.
func TestTransferJournalCannotEscapeRoot(t *testing.T) {
	for _, field := range []string{"name", "staged", "old", "op_id"} {
		t.Run(field, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "root")
			if err := os.Mkdir(root, 0755); err != nil {
				t.Fatal(err)
			}
			writeFileT(t, filepath.Join(base, "outside", "content"), "untouched")
			id := strings.Repeat("b", 32)
			intent := writeTransferIntentFixture(t, root, "mod", "Existing", id)
			writeFileT(t, filepath.Join(root, intent.Old, "content"), "old")
			writeFileT(t, filepath.Join(root, intent.Staged, "content"), "staged")
			switch field {
			case "name":
				intent.Name = "../outside"
			case "staged":
				intent.Staged = "../outside"
			case "old":
				intent.Old = "../outside"
			case "op_id":
				intent.OpID = "../outside"
			}
			data, err := json.Marshal(intent)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, transferIntentPrefix+id+transferIntentSuffix)
			if _, err := atomicfile.WriteFileDurable(path, data, 0644); err != nil {
				t.Fatal(err)
			}
			if err := RecoverTransfers(root); err == nil {
				t.Fatal("unsafe journal accepted")
			}
			if got := readFileT(t, filepath.Join(base, "outside", "content")); got != "untouched" {
				t.Errorf("outside content changed to %q", got)
			}
			if got := readFileT(t, filepath.Join(root, intent.Old, "content")); field != "old" && got != "old" {
				t.Errorf("original backup changed to %q", got)
			}
			if _, err := os.Stat(path); err != nil {
				t.Errorf("unsafe intent removed: %v", err)
			}
		})
	}
}

// TestTransferReplacementWaitsForPendingRecovery prevents a second swap from consuming the original of an unresolved journal.
func TestTransferReplacementWaitsForPendingRecovery(t *testing.T) {
	root := t.TempDir()
	id := strings.Repeat("c", 32)
	intent := writeTransferIntentFixture(t, root, "mod", "Existing", id)
	writeFileT(t, filepath.Join(root, "Existing", "content"), "old")
	writeFileT(t, filepath.Join(root, intent.Old, "content"), "older")
	staged := filepath.Join(root, ".gorganizer-import-fixture", "Existing")
	writeFileT(t, filepath.Join(staged, "content"), "new")
	var commitErr *transferCommitError
	if err := replaceDir(root, "Existing", staged); !errors.As(err, &commitErr) || !commitErr.pending {
		t.Fatalf("replacement error = %v, want pending recovery", err)
	}
	if got := readFileT(t, filepath.Join(root, "Existing", "content")); got != "old" {
		t.Errorf("live target changed to %q", got)
	}
	if got := readFileT(t, filepath.Join(root, intent.Old, "content")); got != "older" {
		t.Errorf("previous backup changed to %q", got)
	}
}

// TestImportOverwriteRenameFailurePreservesOriginal ensures a failed stage publication rolls the existing mod back.
func TestImportOverwriteRenameFailurePreservesOriginal(t *testing.T) {
	archive := exportTestArchive(t)
	setRoot(t, t.TempDir())
	seedCollisions(t)
	ops := defaultTransferCommitOps()
	ops.rename = func(step, from, to string) error {
		if step == "install" {
			return errors.New("injected install failure")
		}
		return os.Rename(from, to)
	}
	_, err := Import(context.Background(), ImportOptions{GameID: testGame, ArchivePath: archive, Policy: dto.PolicyOverwrite, commitOps: &ops}, nil)
	if err == nil {
		t.Fatal("replacement unexpectedly succeeded")
	}
	if got := readFileT(t, filepath.Join(config.ModsDir(testGame), "Alpha Mod", "AlphaMod.esp")); got != "sentinel-alpha" {
		t.Errorf("original mod content = %q", got)
	}
	if err := RecoverTransfers(config.ModsDir(testGame)); err != nil {
		t.Fatal(err)
	}
	assertNoStagingLeftovers(t)
}

// TestOverwriteMergePreservesUnrelatedFiles confirms a merge replaces only conflicting files without touching unrelated Overwrite data.
func TestOverwriteMergePreservesUnrelatedFiles(t *testing.T) {
	root := t.TempDir()
	ow := filepath.Join(root, "Overwrite")
	stage := filepath.Join(root, "stage")
	writeFileT(t, filepath.Join(ow, "existing.txt"), "original")
	writeFileT(t, filepath.Join(ow, "unrelated.txt"), "keep")
	writeFileT(t, filepath.Join(stage, "existing.txt"), "replacement")
	writeFileT(t, filepath.Join(stage, "new.txt"), "new")
	count, err := mergeOverwriteCount(stage, ow)
	if err != nil || count != 2 {
		t.Fatalf("merge count = %d, err = %v", count, err)
	}
	for name, want := range map[string]string{"existing.txt": "replacement", "unrelated.txt": "keep", "new.txt": "new"} {
		if got := readFileT(t, filepath.Join(ow, name)); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// TestRelabelModMetadataFailureStopsPublication ensures a staged metadata error prevents publication of the renamed mod.
func TestRelabelModMetadataFailureStopsPublication(t *testing.T) {
	setRoot(t, t.TempDir())
	root := config.ModsDir(testGame)
	writeFileT(t, filepath.Join(root, "Existing", "content"), "old")
	staged := filepath.Join(root, ".gorganizer-import-fixture", "Existing")
	writeFileT(t, filepath.Join(staged, "metadata.yaml", "unexpected"), "data")
	summary := dto.TransferSummary{Renamed: map[string]string{}}
	if err := finalizeMod(ImportOptions{GameID: testGame}, "Existing", staged, dto.PolicyRename, &summary); err == nil {
		t.Fatal("renamed mod was published despite unreadable staged metadata")
	}
	if got := readFileT(t, filepath.Join(root, "Existing", "content")); got != "old" {
		t.Errorf("original content = %q", got)
	}
	if _, err := os.Lstat(filepath.Join(root, "Existing (2)")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("renamed mod was published: %v", err)
	}
}

// TestPartialImportReportsCommittedItems verifies earlier mods and partial Overwrite merges are counted when a later item fails.
func TestPartialImportReportsCommittedItems(t *testing.T) {
	for _, tc := range []struct {
		name      string
		prepare   func(*testing.T)
		wantItems int
	}{
		{"later-profile", func(t *testing.T) {
			writeFileT(t, filepath.Join(config.ProfilesDir(testGame), "Default"), "not-a-directory")
		}, 3},
		{"overwrite-merge", func(t *testing.T) {
			ow := filepath.Join(config.ModsDir(testGame), "Overwrite")
			writeFileT(t, filepath.Join(ow, "textures", "generated.dds", "blocker"), "cannot-replace")
		}, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "instance.tar.zst")
			setRoot(t, t.TempDir())
			buildInstance(t)
			if tc.name == "overwrite-merge" {
				writeFileT(t, filepath.Join(config.ModsDir(testGame), "Overwrite", "a.txt"), "new")
			}
			if _, err := Export(context.Background(), ExportOptions{GameID: testGame, OutputPath: archive, IncludeOverwrite: true}, nil); err != nil {
				t.Fatal(err)
			}
			setRoot(t, t.TempDir())
			tc.prepare(t)
			sum, err := Import(context.Background(), ImportOptions{GameID: testGame, ArchivePath: archive, Policy: dto.PolicyOverwrite}, nil)
			var incomplete *BundleIncompleteError
			if !errors.As(err, &incomplete) {
				t.Fatalf("error = %v, want BundleIncompleteError", err)
			}
			if incomplete.Items != tc.wantItems || incomplete.Recovery != "none" {
				t.Errorf("incomplete = %+v, want %d items and no recovery", incomplete, tc.wantItems)
			}
			if sum.ModsImported != 3 {
				t.Errorf("imported mods = %d, want 3", sum.ModsImported)
			}
			if got := readFileT(t, filepath.Join(config.ModsDir(testGame), "Alpha Mod", "AlphaMod.esp")); got != "alpha-esp-payload" {
				t.Errorf("committed mod removed during cleanup: %q", got)
			}
		})
	}
}

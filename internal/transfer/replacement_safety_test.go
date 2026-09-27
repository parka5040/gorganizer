package transfer

import (
	"encoding/json"
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

// TestPendingTransferReservesName checks new imports, overwrites, skips and renames refuse a name claimed by a journal.
func TestPendingTransferReservesName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setRoot(t, t.TempDir())
	for _, tc := range []struct {
		kind string
		root string
	}{
		{"mod", config.ModsDir(testGame)},
		{"profile", config.ProfilesDir(testGame)},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			if err := os.MkdirAll(tc.root, 0755); err != nil {
				t.Fatal(err)
			}
			name := "Existing"
			id := strings.Repeat("a", 32)
			intent := writeTransferIntentFixture(t, tc.root, tc.kind, name, id)
			writeFileT(t, filepath.Join(tc.root, intent.Old, "content"), "original")
			staged := filepath.Join(tc.root, ".gorganizer-import-staged", name)
			writeFileT(t, filepath.Join(staged, "content"), "new")
			summary := dto.TransferSummary{Renamed: map[string]string{}}
			for _, policy := range []dto.CollisionPolicy{dto.PolicySkip, dto.PolicyRename, dto.PolicyOverwrite} {
				var err error
				if tc.kind == "mod" {
					err = finalizeMod(ImportOptions{GameID: testGame}, name, staged, policy, &summary)
				} else {
					err = finalizeProfile(ImportOptions{GameID: testGame, Policy: policy}, name, staged, &summary)
				}
				var pending *download.ReplacementPendingError
				if !errors.As(err, &pending) || pending.Name != name {
					t.Fatalf("policy %v: error = %v, want pending %q", policy, err, name)
				}
			}
			if got := readFileT(t, filepath.Join(tc.root, intent.Old, "content")); got != "original" {
				t.Errorf("previous copy changed to %q", got)
			}
		})
	}
}

// TestTransferRecoveryKeepsOldWhenTargetIsForeign checks identity mismatches and legacy intents preserve the previous folder.
func TestTransferRecoveryKeepsOldWhenTargetIsForeign(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct {
		kind   string
		legacy bool
	}{
		{"mod", false}, {"profile", false}, {"mod", true}, {"profile", true},
	} {
		name := tc.kind
		if tc.legacy {
			name += "-legacy"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			id := strings.Repeat("b", 32)
			intent := writeTransferIntentFixture(t, root, tc.kind, "Existing", id)
			stage := filepath.Join(root, intent.Staged)
			writeFileT(t, filepath.Join(stage, "content"), "staged")
			identity, err := atomicfile.Identity(stage)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.legacy {
				intent.SchemaVersion = transferIntentVersion
				intent.StageIdentity = &identity
			}
			data, err := json.Marshal(intent)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := atomicfile.WriteFileDurable(filepath.Join(root, transferIntentPrefix+id+transferIntentSuffix), data, 0644); err != nil {
				t.Fatal(err)
			}
			writeFileT(t, filepath.Join(root, intent.Old, "content"), "original")
			writeFileT(t, filepath.Join(root, intent.Name, "content"), "foreign")
			if err := RecoverTransfers(root); err != nil {
				t.Fatal(err)
			}
			if got := readFileT(t, filepath.Join(root, intent.Name, "content")); got != "foreign" {
				t.Errorf("foreign target changed to %q", got)
			}
			recovered := filepath.Join(root, ".gorganizer-recovered-"+id)
			if got := readFileT(t, filepath.Join(recovered, "content")); got != "original" {
				t.Errorf("recovered original = %q", got)
			}
			if tc.kind == "mod" && download.ValidateTargetModName(filepath.Base(recovered)) == nil {
				t.Error("recovered folder is a valid mod name")
			}
			if err := RecoverTransfers(root); err != nil {
				t.Fatalf("repeat recovery: %v", err)
			}
		})
	}
}

// TestTransferReplacementSyncsFilesystemBeforeRemovingOld records the filesystem flush, directory syncs and removal order.
func TestTransferReplacementSyncsFilesystemBeforeRemovingOld(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	writeFileT(t, filepath.Join(root, "Existing", "content"), "original")
	staged := filepath.Join(root, ".gorganizer-import-staged", "Existing")
	writeFileT(t, filepath.Join(staged, "content"), "replacement")
	ops := defaultTransferCommitOps()
	var events []string
	ops.syncFilesystem = func(path string) error {
		events = append(events, "sync-filesystem")
		return atomicfile.SyncFilesystem(path)
	}
	ops.sync = func(path string) error {
		events = append(events, "sync-dir")
		return atomicfile.SyncDir(path)
	}
	ops.rename = func(step, from, to string) error {
		events = append(events, step)
		return os.Rename(from, to)
	}
	ops.remove = func(step, path string) error {
		events = append(events, step)
		return os.RemoveAll(path)
	}
	if err := replaceDirWithOps(root, "Existing", staged, "mod", ops); err != nil {
		t.Fatal(err)
	}
	want := []string{"stage", "sync-dir", "sync-filesystem", "sync-dir", "move-aside", "install", "sync-dir", "remove-old"}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("operation order = %v, want %v", events, want)
	}
}

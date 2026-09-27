package vfs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/parka/gorganizer/internal/atomicfile"
)

// TestActivationRollbackFailureRetainsJournal checks that failed cleanup retains the intent until recovery restores the original files.
func TestActivationRollbackFailureRetainsJournal(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail func(*testing.T)
	}{
		{"remove", func(t *testing.T) {
			old := removeActivationData
			removeActivationData = func(string) error { return errors.New("remove refused") }
			t.Cleanup(func() { removeActivationData = old })
		}},
		{"rename", func(t *testing.T) {
			old := renameActivationBackup
			renameActivationBackup = func(string, string) error { return errors.New("rename refused") }
			t.Cleanup(func() { renameActivationBackup = old })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			data := filepath.Join(t.TempDir(), "Data")
			mustFile(t, filepath.Join(data, "Skyrim.esm"), "original")
			oldSync := syncFarmParent
			calls := 0
			syncFarmParent = func(path string) error {
				calls++
				if calls == 1 {
					return errors.New("sync refused")
				}
				return oldSync(path)
			}
			t.Cleanup(func() { syncFarmParent = oldSync })
			tc.fail(t)
			mm := NewMountManager(data, "", "testgame")
			err := mm.Activate([]Layer{{Name: "__base__", RootPath: data, Enabled: true}}, "")
			if err == nil || !strings.Contains(err.Error(), "sync refused") || !strings.Contains(err.Error(), tc.name+" refused") {
				t.Fatalf("Activate = %v, want joined sync and rollback failures", err)
			}
			in, err := ReadIntent(activatingIntentPath(data))
			if err != nil {
				t.Fatalf("rollback failure lost intent: %v", err)
			}
			backupID, _, err := directoryAt(data + farmBackupSuffix)
			if err != nil || in.Original != backupID || in.OperationID == "" {
				t.Fatalf("intent does not identify original Data: %+v, backup=%+v, err=%v", in, backupID, err)
			}
			if got := mustRead(t, filepath.Join(data+farmBackupSuffix, "Skyrim.esm")); got != "original" {
				t.Fatalf("backup content = %q", got)
			}
			removeActivationData = os.RemoveAll
			renameActivationBackup = os.Rename
			outcome, err := CleanupStale(data)
			if err != nil || outcome.Pending != nil || !outcome.Restored {
				t.Fatalf("CleanupStale = %+v, %v", outcome, err)
			}
			if got := mustRead(t, filepath.Join(data, "Skyrim.esm")); got != "original" {
				t.Errorf("restored content = %q", got)
			}
			if _, err := os.Lstat(activatingIntentPath(data)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("intent remains: %v", err)
			}
		})
	}
}

// TestActivationRecoveryVerifiesBackupIdentity preserves every folder when the recorded backup has been replaced.
func TestActivationRecoveryVerifiesBackupIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	data := filepath.Join(t.TempDir(), "Data")
	mustFile(t, filepath.Join(data, "Skyrim.esm"), "original")
	writeActivatingIntent(t, data, data+farmBackupSuffix)
	if err := os.Rename(data, data+farmBackupSuffix); err != nil {
		t.Fatal(err)
	}
	mustFile(t, filepath.Join(data, "half-built"), "partial")
	moved := data + ".moved"
	if err := os.Rename(data+farmBackupSuffix, moved); err != nil {
		t.Fatal(err)
	}
	mustFile(t, filepath.Join(data+farmBackupSuffix, "different"), "other")
	outcome, err := CleanupStale(data)
	if err != nil || outcome.Pending == nil || outcome.Restored {
		t.Fatalf("CleanupStale = %+v, %v", outcome, err)
	}
	if got := mustRead(t, filepath.Join(data, "half-built")); got != "partial" {
		t.Errorf("partial Data = %q", got)
	}
	if got := mustRead(t, filepath.Join(data+farmBackupSuffix, "different")); got != "other" {
		t.Errorf("replacement backup = %q", got)
	}
	if got := mustRead(t, filepath.Join(moved, "Skyrim.esm")); got != "original" {
		t.Errorf("moved original = %q", got)
	}
	if _, err := ReadIntent(activatingIntentPath(data)); err != nil {
		t.Errorf("intent was removed: %v", err)
	}
}

// TestActivationRecoveryBeforeRename removes only the intent when Data still has its recorded directory identity.
func TestActivationRecoveryBeforeRename(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	data := filepath.Join(t.TempDir(), "Data")
	mustFile(t, filepath.Join(data, "Skyrim.esm"), "original")
	before, _, err := directoryAt(data)
	if err != nil {
		t.Fatal(err)
	}
	writeActivatingIntent(t, data, data+farmBackupSuffix)
	outcome, err := CleanupStale(data)
	if err != nil || outcome.Pending != nil || outcome.Restored {
		t.Fatalf("CleanupStale = %+v, %v", outcome, err)
	}
	after, _, err := directoryAt(data)
	if err != nil || before != after || mustRead(t, filepath.Join(data, "Skyrim.esm")) != "original" {
		t.Fatalf("original Data changed: before=%+v after=%+v err=%v", before, after, err)
	}
	if _, err := os.Lstat(activatingIntentPath(data)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("intent remains: %v", err)
	}
}

// writeApplyFarm writes a valid farm sentinel with the selected identity.
func writeApplyFarm(t *testing.T, path, backup, id string) {
	t.Helper()
	mustFile(t, filepath.Join(path, "content"), id)
	if err := WriteSentinel(path, &Sentinel{
		SchemaVersion: CurrentSentinelSchema,
		Magic:         SentinelMagic,
		GameID:        "testgame",
		BackupPath:    backup,
		Hash:          ComputeLayerHash(nil),
		FarmID:        id,
		Manifest:      farmManifestPrefix + id + ".jsonl",
	}); err != nil {
		t.Fatal(err)
	}
}

// TestApplyRecoveryReapsOnlyIdentifiedSiblings checks both swap phases and keeps an unrecognized staging folder untouched.
func TestApplyRecoveryReapsOnlyIdentifiedSiblings(t *testing.T) {
	const live = "00000000-0000-4000-8000-000000000001"
	const staged = "00000000-0000-4000-8000-000000000002"
	const other = "00000000-0000-4000-8000-000000000003"
	for _, tc := range []struct {
		name, dataID, siblingID string
		pending                 bool
	}{
		{"before swap", live, staged, false},
		{"after swap", staged, live, false},
		{"unrelated before swap", live, other, true},
		{"unrelated after swap", staged, other, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			data := filepath.Join(t.TempDir(), "Data")
			backup := data + farmBackupSuffix
			staging := stagingDirPath(data)
			mustFile(t, filepath.Join(backup, "original"), "original")
			writeApplyFarm(t, data, backup, tc.dataID)
			writeApplyFarm(t, staging, backup, tc.siblingID)
			apply := applyingIntentPath(data)
			if err := WriteIntent(apply, &ActivationIntent{
				SchemaVersion: CurrentIntentSchema, Magic: IntentMagic, Kind: IntentApplying,
				OperationID: uuid.NewString(), DataPath: data, BackupPath: backup, StagingPath: staging,
				LiveFarmID: live, StagingFarmID: staged,
			}); err != nil {
				t.Fatal(err)
			}
			outcome, err := CleanupStale(data)
			if err != nil {
				t.Fatal(err)
			}
			if tc.pending {
				if outcome.Pending == nil || outcome.Restored {
					t.Fatalf("outcome = %+v, want pending", outcome)
				}
				if got := mustRead(t, filepath.Join(data, "content")); got != tc.dataID {
					t.Errorf("Data changed to %q", got)
				}
				if got := mustRead(t, filepath.Join(staging, "content")); got != tc.siblingID {
					t.Errorf("unrecognized staging content = %q", got)
				}
				if _, err := ReadIntent(apply); err != nil {
					t.Errorf("apply intent removed: %v", err)
				}
			} else {
				if outcome.Pending != nil || !outcome.Restored {
					t.Fatalf("outcome = %+v, want restored", outcome)
				}
				if got := mustRead(t, filepath.Join(data, "original")); got != "original" {
					t.Errorf("restored original = %q", got)
				}
				for _, path := range []string{staging, apply} {
					if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
						t.Errorf("transition sibling %s remains: %v", path, err)
					}
				}
			}
		})
	}
}

// TestV1IntentsStillRecover checks the previous activation and apply intent layouts remain readable and recoverable.
func TestV1IntentsStillRecover(t *testing.T) {
	for _, kind := range []IntentKind{IntentActivating, IntentApplying} {
		t.Run(string(kind), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			data := filepath.Join(t.TempDir(), "Data")
			backup := data + farmBackupSuffix
			mustFile(t, filepath.Join(backup, "original"), "original")
			path := activatingIntentPath(data)
			if kind == IntentApplying {
				path = applyingIntentPath(data)
				writeApplyFarm(t, data, backup, "00000000-0000-4000-8000-000000000001")
				mustFile(t, filepath.Join(stagingDirPath(data), "old"), "old")
			} else {
				mustFile(t, filepath.Join(data, "partial"), "partial")
			}
			if err := WriteIntent(path, &ActivationIntent{SchemaVersion: 1, Magic: IntentMagic, Kind: kind, DataPath: data, BackupPath: backup}); err != nil {
				t.Fatal(err)
			}
			if in, err := ReadIntent(path); err != nil || in.SchemaVersion != 1 {
				t.Fatalf("ReadIntent = %+v, %v", in, err)
			}
			outcome, err := CleanupStale(data)
			if err != nil || outcome.Pending != nil || !outcome.Restored {
				t.Fatalf("CleanupStale = %+v, %v", outcome, err)
			}
			if got := mustRead(t, filepath.Join(data, "original")); got != "original" {
				t.Errorf("restored = %q", got)
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("v1 intent remains: %v", err)
			}
		})
	}
}

// TestIntentWriteIsDurable checks that WriteIntent calls the durable writer with the intent and its file mode.
func TestIntentWriteIsDurable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := writeIntentDurable
	t.Cleanup(func() { writeIntentDurable = old })
	called := false
	writeIntentDurable = func(path string, body []byte, perm os.FileMode) (atomicfile.Outcome, error) {
		called = true
		if filepath.Base(path) != "Data.gorganizer-activating" || perm != 0644 || !strings.Contains(string(body), `"schema_version": 2`) {
			t.Errorf("unexpected durable write: %s %o %s", path, perm, body)
		}
		return atomicfile.Durable, nil
	}
	if err := WriteIntent(filepath.Join(t.TempDir(), "Data.gorganizer-activating"), &ActivationIntent{SchemaVersion: 2}); err != nil || !called {
		t.Fatalf("WriteIntent = %v, called=%t", err, called)
	}
}

// TestApplySyncFailureRetainsJournal keeps both exchanged farms until a failed parent sync can be reconciled.
func TestApplySyncFailureRetainsJournal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	data := filepath.Join(t.TempDir(), "Data")
	mustFile(t, filepath.Join(data, "original"), "original")
	mm := NewMountManager(data, "", "testgame")
	layers := []Layer{{Name: "__base__", RootPath: data, Enabled: true}}
	if err := mm.Activate(layers, ""); err != nil {
		t.Fatal(err)
	}
	if err := mm.MarkDirty(layers); err != nil {
		t.Fatal(err)
	}
	old := syncFarmParent
	t.Cleanup(func() { syncFarmParent = old })
	syncErr := errors.New("sync exchange refused")
	syncFarmParent = func(string) error { return syncErr }
	if err := mm.ReMaterialize(); !errors.Is(err, syncErr) {
		t.Fatalf("ReMaterialize = %v, want %v", err, syncErr)
	}
	in, err := ReadIntent(applyingIntentPath(data))
	if err != nil {
		t.Fatalf("apply intent was removed: %v", err)
	}
	current, err := ReadSentinel(data)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := ReadSentinel(stagingDirPath(data))
	if err != nil {
		t.Fatalf("old farm was removed before sync: %v", err)
	}
	if in.SchemaVersion != 2 || in.OperationID == "" || in.LiveFarmID != previous.FarmID || in.StagingFarmID != current.FarmID {
		t.Fatalf("apply intent does not identify exchanged farms: %+v", in)
	}
	if err := mm.ReMaterialize(); err == nil || !strings.Contains(err.Error(), "unfinished mod change") {
		t.Fatalf("retry overwrote unresolved old farm: %v", err)
	}
	syncFarmParent = old
	outcome, err := CleanupStale(data)
	if err != nil || outcome.Pending != nil || !outcome.Restored {
		t.Fatalf("CleanupStale = %+v, %v", outcome, err)
	}
	if got := mustRead(t, filepath.Join(data, "original")); got != "original" {
		t.Errorf("restored original = %q", got)
	}
}

// TestActivationSyncsParent checks that activation syncs the renamed directory's parent before building the farm.
func TestActivationSyncsParent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	data := filepath.Join(t.TempDir(), "Data")
	mustFile(t, filepath.Join(data, "original"), "original")
	old := syncFarmParent
	t.Cleanup(func() { syncFarmParent = old })
	calls := 0
	syncFarmParent = func(dir string) error {
		calls++
		if dir != filepath.Dir(data) {
			t.Errorf("synced %s, want %s", dir, filepath.Dir(data))
		}
		return old(dir)
	}
	mm := NewMountManager(data, "", "testgame")
	if err := mm.Activate([]Layer{{Name: "__base__", RootPath: data, Enabled: true}}, ""); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("activation parent syncs = %d, want 1", calls)
	}
	if err := mm.Deactivate(); err != nil {
		t.Fatal(err)
	}
}

// TestApplyOnLegacyFarmWritesV1Intent keeps Apply working on a farm deployed before farm identities existed.
func TestApplyOnLegacyFarmWritesV1Intent(t *testing.T) {
	root := t.TempDir()
	dataPath := filepath.Join(root, "Data")
	modRoot := filepath.Join(root, "mods", "A")
	if err := os.MkdirAll(dataPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(modRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataPath, "base.esm"), []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modRoot, "a.esp"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	mm := NewMountManager(dataPath, filepath.Join(root, "overwrite"), "skyrimse")
	layers := []Layer{{Name: "__base__", Enabled: true}, {Name: "A", RootPath: modRoot, Enabled: true}}
	if err := mm.Activate(layers, "Default"); err != nil {
		t.Fatal(err)
	}
	s, err := ReadSentinel(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	s.SchemaVersion = 2
	s.FarmID, s.Manifest, s.ManifestSHA256, s.ManifestEntries = "", "", "", 0
	if err := WriteSentinel(dataPath, s); err != nil {
		t.Fatal(err)
	}
	var written *ActivationIntent
	previous := writeIntentDurable
	writeIntentDurable = func(path string, body []byte, perm os.FileMode) (atomicfile.Outcome, error) {
		if strings.HasSuffix(path, applyingSuffix) {
			var in ActivationIntent
			if err := json.Unmarshal(body, &in); err != nil {
				t.Fatal(err)
			}
			written = &in
		}
		return previous(path, body, perm)
	}
	t.Cleanup(func() { writeIntentDurable = previous })
	if err := mm.MarkDirty([]Layer{{Name: "__base__", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if err := mm.ReMaterialize(); err != nil {
		t.Fatalf("Apply on a legacy farm: %v", err)
	}
	if written == nil || written.SchemaVersion != 1 || written.LiveFarmID != "" {
		t.Fatalf("apply intent = %+v, want schema 1 without farm identities", written)
	}
	if _, err := os.Stat(filepath.Join(dataPath, "a.esp")); !os.IsNotExist(err) {
		t.Fatalf("disabled mod file still deployed: %v", err)
	}
}

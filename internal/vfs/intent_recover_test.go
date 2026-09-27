package vfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

// writeActivatingIntent writes an activating intent marker beside dataPath to simulate a crash mid-Activate.
func writeActivatingIntent(t *testing.T, dataPath, backupPath string) {
	t.Helper()
	original, exists, err := directoryAt(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		original, exists, err = directoryAt(dataPath)
		if err != nil || !exists {
			t.Fatalf("finding original directory: exists=%t err=%v", exists, err)
		}
	}
	err = WriteIntent(activatingIntentPath(dataPath), &ActivationIntent{
		SchemaVersion: CurrentIntentSchema,
		Magic:         IntentMagic,
		Kind:          IntentActivating,
		OperationID:   uuid.NewString(),
		Original:      original,
		GameID:        "testgame",
		DataPath:      dataPath,
		BackupPath:    backupPath,
		PID:           4242,
	})
	if err != nil {
		t.Fatalf("WriteIntent: %v", err)
	}
}

// TestCleanupStale_IntentRollback_PartialFarm locks that a partial farm with an intent marker rolls back.
func TestCleanupStale_IntentRollback_PartialFarm(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")
	backupPath := dataPath + ".orig"

	mustDir(t, backupPath)
	mustFile(t, filepath.Join(backupPath, "Skyrim.esm"), "master")
	mustDir(t, dataPath)
	mustFile(t, filepath.Join(dataPath, "half-materialized.nif"), "junk")
	writeActivatingIntent(t, dataPath, backupPath)

	outcome, err := CleanupStale(dataPath)
	if err != nil {
		t.Fatalf("CleanupStale: %v", err)
	}
	if outcome.Pending != nil {
		t.Fatalf("expected auto-rollback, got Pending: %s", outcome.Pending.Reason)
	}
	if !outcome.Restored {
		t.Fatal("expected Restored=true")
	}
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Error("backup should be consumed by the rollback")
	}
	if got := mustRead(t, filepath.Join(dataPath, "Skyrim.esm")); got != "master" {
		t.Errorf("Data/Skyrim.esm = %q, want restored master", got)
	}
	if _, err := os.Stat(activatingIntentPath(dataPath)); !os.IsNotExist(err) {
		t.Error("intent marker should be removed after rollback")
	}
	if _, err := os.Stat(filepath.Join(dataPath, "half-materialized.nif")); !os.IsNotExist(err) {
		t.Error("partial farm content should be gone after rollback")
	}
}

// TestCleanupStale_IntentRollback_DataAbsent locks that the backup is restored when the crash left Data absent.
func TestCleanupStale_IntentRollback_DataAbsent(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")
	backupPath := dataPath + ".orig"

	mustDir(t, backupPath)
	mustFile(t, filepath.Join(backupPath, "Skyrim.esm"), "master")
	writeActivatingIntent(t, dataPath, backupPath)

	outcome, err := CleanupStale(dataPath)
	if err != nil {
		t.Fatalf("CleanupStale: %v", err)
	}
	if !outcome.Restored {
		t.Fatal("expected Restored=true")
	}
	if got := mustRead(t, filepath.Join(dataPath, "Skyrim.esm")); got != "master" {
		t.Errorf("Data/Skyrim.esm = %q, want restored master", got)
	}
}

// TestCleanupStale_IntentNoBackup_LeavesData locks that Data is left untouched when the crash preceded the rename.
func TestCleanupStale_IntentNoBackup_LeavesData(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")

	mustDir(t, dataPath)
	mustFile(t, filepath.Join(dataPath, "Skyrim.esm"), "pristine")
	writeActivatingIntent(t, dataPath, dataPath+".orig")

	outcome, err := CleanupStale(dataPath)
	if err != nil {
		t.Fatalf("CleanupStale: %v", err)
	}
	if outcome.Restored {
		t.Error("nothing to restore; Restored should be false")
	}
	if got := mustRead(t, filepath.Join(dataPath, "Skyrim.esm")); got != "pristine" {
		t.Errorf("Data/Skyrim.esm = %q, want untouched pristine", got)
	}
	if _, err := os.Stat(activatingIntentPath(dataPath)); !os.IsNotExist(err) {
		t.Error("intent marker should be removed")
	}
}

// TestCleanupStale_V1Sentinel_BackCompat locks that a v1 sentinel still validates and restores without capture.
func TestCleanupStale_V1Sentinel_BackCompat(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")
	backupPath := dataPath + ".orig"

	mustDir(t, backupPath)
	mustFile(t, filepath.Join(backupPath, "Skyrim.esm"), "master")
	mustDir(t, dataPath)
	if err := WriteSentinel(dataPath, &Sentinel{
		SchemaVersion:       1,
		Magic:               SentinelMagic,
		BackupPath:          backupPath,
		MaterializerVersion: CurrentMaterializerVersion,
	}); err != nil {
		t.Fatal(err)
	}

	outcome, err := CleanupStale(dataPath)
	if err != nil {
		t.Fatalf("CleanupStale: %v", err)
	}
	if !outcome.Restored {
		t.Fatal("v1 sentinel should still restore")
	}
	if got := mustRead(t, filepath.Join(dataPath, "Skyrim.esm")); got != "master" {
		t.Errorf("restored Data/Skyrim.esm = %q", got)
	}
}

// TestCleanupStale_CaptureAwareRecovery locks that recovery moves new writes into Overwrite before removing the farm.
func TestCleanupStale_CaptureAwareRecovery(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")
	backupPath := dataPath + ".orig"
	overwriteRoot := filepath.Join(dir, "Overwrite")

	mustDir(t, backupPath)
	mustFile(t, filepath.Join(backupPath, "Skyrim.esm"), "master")
	mustDir(t, overwriteRoot)
	mustDir(t, dataPath)
	mustFile(t, filepath.Join(dataPath, "Saves", "quicksave.ess"), "SAVEDATA")

	s := &Sentinel{
		SchemaVersion:       2,
		Magic:               SentinelMagic,
		GameID:              "testgame",
		BackupPath:          backupPath,
		OverwriteRoot:       overwriteRoot,
		MaterializerVersion: CurrentMaterializerVersion,
	}
	s.Hash = ComputeLayerHash(s.Layers)
	if err := WriteSentinel(dataPath, s); err != nil {
		t.Fatal(err)
	}

	outcome, err := CleanupStale(dataPath)
	if err != nil {
		t.Fatalf("CleanupStale: %v", err)
	}
	if outcome.Pending != nil {
		t.Fatalf("unexpected Pending: %s", outcome.Pending.Reason)
	}
	if !outcome.Restored {
		t.Fatal("expected Restored=true")
	}
	if got := mustRead(t, filepath.Join(overwriteRoot, "Saves", "quicksave.ess")); got != "SAVEDATA" {
		t.Errorf("save should have been captured into Overwrite, got %q", got)
	}
	if got := mustRead(t, filepath.Join(dataPath, "Skyrim.esm")); got != "master" {
		t.Errorf("Data should be restored from backup, got %q", got)
	}
}

// TestActivationIntentWithCommittedSentinelCapturesWrites restores the original files after capturing writes from a committed farm.
func TestActivationIntentWithCommittedSentinelCapturesWrites(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")
	backupPath := dataPath + farmBackupSuffix
	overwriteRoot := filepath.Join(dir, "Overwrite")
	mustFile(t, filepath.Join(backupPath, "Skyrim.esm"), "master")
	mustDir(t, overwriteRoot)
	mustFile(t, filepath.Join(dataPath, "Saves", "quicksave.ess"), "SAVEDATA")
	writeRecoverableFarm(t, dataPath, backupPath, overwriteRoot)
	writeActivatingIntent(t, dataPath, backupPath)

	outcome, err := CleanupStale(dataPath)
	if err != nil {
		t.Fatalf("CleanupStale: %v", err)
	}
	if !outcome.Restored || outcome.Pending != nil {
		t.Fatalf("outcome = %+v, want restored without pending", outcome)
	}
	if got := mustRead(t, filepath.Join(overwriteRoot, "Saves", "quicksave.ess")); got != "SAVEDATA" {
		t.Errorf("captured save = %q, want SAVEDATA", got)
	}
	if got := mustRead(t, filepath.Join(dataPath, "Skyrim.esm")); got != "master" {
		t.Errorf("restored master = %q, want master", got)
	}
	if _, err := os.Stat(activatingIntentPath(dataPath)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("activation intent still present: %v", err)
	}
}

// TestActivationRollbackFailureKeepsIntent keeps the recovery marker when restoring the backup fails.
func TestActivationRollbackFailureKeepsIntent(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")
	backupPath := dataPath + farmBackupSuffix
	mustFile(t, filepath.Join(backupPath, "Skyrim.esm"), "master")
	mustFile(t, filepath.Join(dataPath, "partial.nif"), "partial")
	writeActivatingIntent(t, dataPath, backupPath)
	renameErr := errors.New("cannot restore backup")
	originalRename := renameActivationBackup
	renameActivationBackup = func(_, _ string) error { return renameErr }
	t.Cleanup(func() { renameActivationBackup = originalRename })

	if _, err := CleanupStale(dataPath); !errors.Is(err, renameErr) {
		t.Fatalf("rollback error = %v, want %v", err, renameErr)
	}
	if _, err := os.Stat(activatingIntentPath(dataPath)); err != nil {
		t.Errorf("rollback failure removed activation intent: %v", err)
	}
	if got := mustRead(t, filepath.Join(backupPath, "Skyrim.esm")); got != "master" {
		t.Errorf("backup master = %q, want master", got)
	}
}

// TestRecoverLegacyFallbackGapRestoresOldFarm captures writes from the old farm when Data is absent during a legacy swap.
func TestRecoverLegacyFallbackGapRestoresOldFarm(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")
	backupPath := dataPath + farmBackupSuffix
	overwriteRoot := filepath.Join(dir, "Overwrite")
	mustFile(t, filepath.Join(backupPath, "Skyrim.esm"), "master")
	mustDir(t, overwriteRoot)
	mustFile(t, filepath.Join(oldFarmPath(dataPath), "Saves", "quicksave.ess"), "SAVEDATA")
	writeRecoverableFarm(t, oldFarmPath(dataPath), backupPath, overwriteRoot)
	mustFile(t, filepath.Join(stagingDirPath(dataPath), "new.esp"), "new")
	writeRecoverableFarm(t, stagingDirPath(dataPath), backupPath, overwriteRoot)

	outcome, err := CleanupStale(dataPath)
	if err != nil {
		t.Fatalf("CleanupStale: %v", err)
	}
	if !outcome.Restored || outcome.Pending != nil {
		t.Fatalf("outcome = %+v, want restored without pending", outcome)
	}
	if got := mustRead(t, filepath.Join(overwriteRoot, "Saves", "quicksave.ess")); got != "SAVEDATA" {
		t.Errorf("captured save = %q, want SAVEDATA", got)
	}
	if got := mustRead(t, filepath.Join(dataPath, "Skyrim.esm")); got != "master" {
		t.Errorf("restored master = %q, want master", got)
	}
	for _, path := range []string{oldFarmPath(dataPath), stagingDirPath(dataPath)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("transition sibling %s still present: %v", path, err)
		}
	}
}

// TestRecoverStagingOnlyGap captures writes from staging when no old farm exists and Data is absent.
func TestRecoverStagingOnlyGap(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")
	backupPath := dataPath + farmBackupSuffix
	overwriteRoot := filepath.Join(dir, "Overwrite")
	mustFile(t, filepath.Join(backupPath, "Skyrim.esm"), "master")
	mustDir(t, overwriteRoot)
	mustFile(t, filepath.Join(stagingDirPath(dataPath), "Saves", "quicksave.ess"), "SAVEDATA")
	writeRecoverableFarm(t, stagingDirPath(dataPath), backupPath, overwriteRoot)

	outcome, err := CleanupStale(dataPath)
	if err != nil {
		t.Fatalf("CleanupStale: %v", err)
	}
	if !outcome.Restored || outcome.Pending != nil {
		t.Fatalf("outcome = %+v, want restored without pending", outcome)
	}
	if got := mustRead(t, filepath.Join(overwriteRoot, "Saves", "quicksave.ess")); got != "SAVEDATA" {
		t.Errorf("captured save = %q, want SAVEDATA", got)
	}
	if got := mustRead(t, filepath.Join(dataPath, "Skyrim.esm")); got != "master" {
		t.Errorf("restored master = %q, want master", got)
	}
	if _, err := os.Stat(stagingDirPath(dataPath)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("staging farm still present: %v", err)
	}
}

// TestRecoverAmbiguousGapKeepsSiblings leaves unrecognized transition folders in place for manual recovery.
func TestRecoverAmbiguousGapKeepsSiblings(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")
	backupPath := dataPath + farmBackupSuffix
	mustFile(t, filepath.Join(backupPath, "Skyrim.esm"), "master")
	mustFile(t, filepath.Join(oldFarmPath(dataPath), "Saves", "quicksave.ess"), "SAVEDATA")

	outcome, err := CleanupStale(dataPath)
	if err != nil {
		t.Fatalf("CleanupStale: %v", err)
	}
	if outcome.Pending == nil || outcome.Restored {
		t.Fatalf("outcome = %+v, want pending without restore", outcome)
	}
	if got := mustRead(t, filepath.Join(oldFarmPath(dataPath), "Saves", "quicksave.ess")); got != "SAVEDATA" {
		t.Errorf("uncertain farm write = %q, want SAVEDATA", got)
	}
	if _, err := os.Stat(dataPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Data should remain absent: %v", err)
	}
}

// TestRecoverValidSiblingWithEmptyDataKeepsFarm leaves a recoverable sibling intact when Data is an empty mountpoint.
func TestRecoverValidSiblingWithEmptyDataKeepsFarm(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")
	backupPath := dataPath + farmBackupSuffix
	mustDir(t, dataPath)
	mustFile(t, filepath.Join(backupPath, "Skyrim.esm"), "master")
	mustFile(t, filepath.Join(oldFarmPath(dataPath), "Saves", "quicksave.ess"), "SAVEDATA")
	writeRecoverableFarm(t, oldFarmPath(dataPath), backupPath, filepath.Join(dir, "Overwrite"))

	outcome, err := CleanupStale(dataPath)
	if err != nil {
		t.Fatalf("CleanupStale: %v", err)
	}
	if outcome.Pending == nil || outcome.Restored {
		t.Fatalf("outcome = %+v, want pending without restore", outcome)
	}
	if got := mustRead(t, filepath.Join(oldFarmPath(dataPath), "Saves", "quicksave.ess")); got != "SAVEDATA" {
		t.Errorf("uncaptured save = %q, want SAVEDATA", got)
	}
}

// writeRecoverableFarm writes a valid sentinel in a farm with the given backup and capture destination.
func writeRecoverableFarm(t *testing.T, farmPath, backupPath, overwriteRoot string) {
	t.Helper()
	mustDir(t, farmPath)
	s := &Sentinel{
		SchemaVersion:       2,
		Magic:               SentinelMagic,
		GameID:              "testgame",
		BackupPath:          backupPath,
		OverwriteRoot:       overwriteRoot,
		MaterializerVersion: CurrentMaterializerVersion,
	}
	s.Hash = ComputeLayerHash(s.Layers)
	if err := WriteSentinel(farmPath, s); err != nil {
		t.Fatal(err)
	}
}

// TestCleanupStale_ReapsTransientSiblings locks that leftover Apply siblings are reaped without blocking recovery.
func TestCleanupStale_ReapsTransientSiblings(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")
	backupPath := dataPath + ".orig"

	mustDir(t, stagingDirPath(dataPath))
	mustFile(t, filepath.Join(stagingDirPath(dataPath), "leftover"), "x")
	mustDir(t, oldFarmPath(dataPath))
	mustDir(t, backupPath)
	mustFile(t, filepath.Join(backupPath, "Skyrim.esm"), "master")
	mustDir(t, dataPath)

	if _, err := CleanupStale(dataPath); err != nil {
		t.Fatalf("CleanupStale: %v", err)
	}
	if _, err := os.Stat(stagingDirPath(dataPath)); !os.IsNotExist(err) {
		t.Error("staging dir should be reaped")
	}
	if _, err := os.Stat(oldFarmPath(dataPath)); !os.IsNotExist(err) {
		t.Error("oldfarm dir should be reaped")
	}
}

func TestValidateSentinel_V2HashMismatchRejected(t *testing.T) {
	base := t.TempDir()
	backup := filepath.Join(base, "Data.orig")
	mustDir(t, backup)
	layers := []SentinelLayer{{Name: "__base__", Root: backup, Enabled: true}}
	s := &Sentinel{
		SchemaVersion: 2,
		Magic:         SentinelMagic,
		GameID:        "testgame",
		BackupPath:    backup,
		Hash:          ComputeLayerHash(layers),
		Layers:        layers,
	}
	if err := ValidateSentinel(s); err != nil {
		t.Fatalf("baseline should validate: %v", err)
	}
	s.Layers[0].Enabled = false
	if err := ValidateSentinel(s); !errors.Is(err, ErrSentinelInvalid) {
		t.Errorf("tampered layers should be rejected, got %v", err)
	}
}

// TestActivateCommitsV3Sentinel checks that activation writes a valid v3 sentinel and removes its intent.
func TestActivateCommitsV3Sentinel(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")
	mustDir(t, dataPath)
	mustFile(t, filepath.Join(dataPath, "Skyrim.esm"), "master")

	mm := NewMountManager(dataPath, "", "skyrimse")
	layers := []Layer{{Name: "__base__", RootPath: dataPath, Enabled: true}}
	if err := mm.Activate(layers, "MyProfile"); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	t.Cleanup(func() { _ = mm.Deactivate() })

	if _, err := os.Stat(activatingIntentPath(dataPath)); !os.IsNotExist(err) {
		t.Error("intent marker should be removed after a committed Activate")
	}
	s, err := ReadSentinel(dataPath)
	if err != nil {
		t.Fatalf("ReadSentinel: %v", err)
	}
	if s.SchemaVersion != CurrentSentinelSchema {
		t.Errorf("schema = %d, want %d", s.SchemaVersion, CurrentSentinelSchema)
	}
	if s.GameID != "skyrimse" || s.ProfileName != "MyProfile" {
		t.Errorf("identity not populated: game=%q profile=%q", s.GameID, s.ProfileName)
	}
	if err := ValidateSentinel(s); err != nil {
		t.Errorf("committed sentinel should validate: %v", err)
	}
}

func mustDir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0755); err != nil {
		t.Fatal(err)
	}
}

func mustFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

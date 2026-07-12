package vfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeActivatingIntent drops an "activating" intent marker beside dataPath,
func writeActivatingIntent(t *testing.T, dataPath, backupPath string) {
	t.Helper()
	err := WriteIntent(activatingIntentPath(dataPath), &ActivationIntent{
		SchemaVersion: CurrentIntentSchema,
		Magic:         IntentMagic,
		Kind:          IntentActivating,
		GameID:        "testgame",
		DataPath:      dataPath,
		BackupPath:    backupPath,
		PID:           4242,
	})
	if err != nil {
		t.Fatalf("WriteIntent: %v", err)
	}
}

// Case B: an interrupted Activate left a partial farm (no valid sentinel) plus
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

// Case C: Data was already renamed away (absent) when the crash hit; restore
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

// Case D: the crash hit before the rename, so Data is the pristine original and
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

// A v1 sentinel (older build, no hash/identity) must still validate and recover
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

// A valid v2 sentinel with an OverwriteRoot: recovery must move new writes
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
		SchemaVersion:       CurrentSentinelSchema,
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

// Transient build siblings from an interrupted Apply must be reaped, and must
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
		SchemaVersion: CurrentSentinelSchema,
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

// A successful Activate must commit: no intent marker left behind, and the
func TestActivate_CommitsNoIntentAndWritesV2(t *testing.T) {
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

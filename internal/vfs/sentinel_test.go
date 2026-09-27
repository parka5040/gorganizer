package vfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeBackupDir(t *testing.T, base string) string {
	t.Helper()
	backup := filepath.Join(base, "Data.orig")
	if err := os.MkdirAll(backup, 0755); err != nil {
		t.Fatal(err)
	}
	return backup
}

func TestSentinel_RoundTripWriteReadValidate(t *testing.T) {
	base := t.TempDir()
	dataPath := filepath.Join(base, "Data")
	if err := os.MkdirAll(dataPath, 0755); err != nil {
		t.Fatal(err)
	}
	backup := writeBackupDir(t, base)

	now := time.Now().UTC().Truncate(time.Second)
	layers := []SentinelLayer{
		{Name: "__base__", Root: backup, Enabled: true},
		{Name: "YUP", Root: filepath.Join(base, "mods", "YUP"), Enabled: true},
	}
	want := &Sentinel{
		SchemaVersion:       CurrentSentinelSchema,
		Magic:               SentinelMagic,
		GameID:              "falloutnv",
		ProfileName:         "Default",
		ActivationPID:       12345,
		ActivationStartedAt: now,
		Hash:                ComputeLayerHash(layers),
		BackupPath:          backup,
		OverwriteMod:        "Overwrite",
		OverwriteRoot:       filepath.Join(base, "mods", "Overwrite"),
		Layers:              layers,
		MaterializerVersion: CurrentMaterializerVersion,
		FarmID:              "12345678-1234-1234-1234-123456789abc",
		Manifest:            ".gorganizer-farm-12345678-1234-1234-1234-123456789abc.jsonl",
		ManifestSHA256:      "test-digest",
	}

	if err := WriteSentinel(dataPath, want); err != nil {
		t.Fatalf("WriteSentinel: %v", err)
	}
	got, err := ReadSentinel(dataPath)
	if err != nil {
		t.Fatalf("ReadSentinel: %v", err)
	}
	if got.GameID != want.GameID || got.Magic != want.Magic ||
		got.BackupPath != want.BackupPath || got.OverwriteMod != want.OverwriteMod ||
		got.ActivationPID != want.ActivationPID || got.FarmID != want.FarmID ||
		got.Manifest != want.Manifest || got.ManifestSHA256 != want.ManifestSHA256 ||
		got.ManifestEntries != want.ManifestEntries {
		t.Errorf("round-trip mismatch:\n want=%+v\n  got=%+v", want, got)
	}
	if len(got.Layers) != len(want.Layers) {
		t.Fatalf("layer count: got %d want %d", len(got.Layers), len(want.Layers))
	}

	if err := ValidateSentinel(got); err != nil {
		t.Errorf("ValidateSentinel: %v", err)
	}
}

func TestSentinel_RejectsBadMagic(t *testing.T) {
	base := t.TempDir()
	backup := writeBackupDir(t, base)
	s := &Sentinel{
		SchemaVersion: CurrentSentinelSchema,
		Magic:         "not-us",
		GameID:        "falloutnv",
		BackupPath:    backup,
	}
	if err := ValidateSentinel(s); !errors.Is(err, ErrSentinelInvalid) {
		t.Errorf("expected ErrSentinelInvalid for bad magic, got %v", err)
	}
}

func TestSentinel_RejectsMissingBackup(t *testing.T) {
	s := &Sentinel{
		SchemaVersion: CurrentSentinelSchema,
		Magic:         SentinelMagic,
		GameID:        "falloutnv",
		BackupPath:    "/nonexistent/Data.orig",
	}
	if err := ValidateSentinel(s); !errors.Is(err, ErrSentinelInvalid) {
		t.Errorf("expected ErrSentinelInvalid for missing backup, got %v", err)
	}
}

func TestSentinel_RejectsWrongSchema(t *testing.T) {
	base := t.TempDir()
	backup := writeBackupDir(t, base)
	s := &Sentinel{
		SchemaVersion: CurrentSentinelSchema + 99,
		Magic:         SentinelMagic,
		GameID:        "falloutnv",
		BackupPath:    backup,
	}
	if err := ValidateSentinel(s); !errors.Is(err, ErrSentinelInvalid) {
		t.Errorf("expected ErrSentinelInvalid for wrong schema, got %v", err)
	}
}

func TestSentinel_MissingFileReturnsErrSentinelMissing(t *testing.T) {
	base := t.TempDir()
	dataPath := filepath.Join(base, "Data")
	if err := os.MkdirAll(dataPath, 0755); err != nil {
		t.Fatal(err)
	}
	_, err := ReadSentinel(dataPath)
	if !errors.Is(err, ErrSentinelMissing) {
		t.Errorf("expected ErrSentinelMissing, got %v", err)
	}
}

func TestSentinel_RemoveIdempotent(t *testing.T) {
	base := t.TempDir()
	dataPath := filepath.Join(base, "Data")
	if err := os.MkdirAll(dataPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSentinel(dataPath); err != nil {
		t.Errorf("first RemoveSentinel on empty dir: %v", err)
	}
	if err := RemoveSentinel(dataPath); err != nil {
		t.Errorf("second RemoveSentinel: %v", err)
	}
}

// TestSentinelWriteIsAtomicAndItsTempNeverCaptured locks that the sentinel is replaced whole with mode 0644 and a torn temporary copy is never captured into Overwrite.
func TestSentinelWriteIsAtomicAndItsTempNeverCaptured(t *testing.T) {
	base := t.TempDir()
	dataPath := filepath.Join(base, "Data")
	if err := os.MkdirAll(dataPath, 0755); err != nil {
		t.Fatal(err)
	}
	backup := writeBackupDir(t, base)
	for _, profile := range []string{"First", "Second"} {
		if err := WriteSentinel(dataPath, &Sentinel{SchemaVersion: 1, Magic: SentinelMagic, GameID: "skyrimse", ProfileName: profile, BackupPath: backup}); err != nil {
			t.Fatalf("WriteSentinel(%s): %v", profile, err)
		}
	}
	got, err := ReadSentinel(dataPath)
	if err != nil || got.ProfileName != "Second" {
		t.Fatalf("ReadSentinel = %+v, %v; want the second write", got, err)
	}
	info, err := os.Stat(filepath.Join(dataPath, SentinelFilename))
	if err != nil || info.Mode().Perm() != 0644 {
		t.Fatalf("sentinel mode = %v, %v; want 0644", info, err)
	}
	entries, err := os.ReadDir(dataPath)
	if err != nil || len(entries) != 1 {
		t.Fatalf("Data holds %v after two sentinel writes, want only the sentinel", entries)
	}

	torn := filepath.Join(dataPath, ".tmp-"+SentinelFilename+"-123")
	if err := os.WriteFile(torn, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	userWrite := filepath.Join(dataPath, "user.ini")
	if err := os.WriteFile(userWrite, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	overwrite := filepath.Join(base, "Overwrite")
	moved, err := CaptureNewFiles(dataPath, overwrite)
	if err != nil || moved != 1 {
		t.Fatalf("CaptureNewFiles = %d, %v; want only the user write", moved, err)
	}
	if _, err := os.Stat(filepath.Join(overwrite, filepath.Base(torn))); !os.IsNotExist(err) {
		t.Errorf("a torn sentinel temp was captured into Overwrite: %v", err)
	}
	if _, err := os.Stat(filepath.Join(overwrite, "user.ini")); err != nil {
		t.Errorf("the user write was not captured: %v", err)
	}
}

// TestFarmSiblingSuffixesNameEveryLifecycleSibling locks the exported sibling suffixes the daemon hands to the mod-loader farm check.
func TestFarmSiblingSuffixesNameEveryLifecycleSibling(t *testing.T) {
	dataPath := filepath.Join(t.TempDir(), "Data")
	want := map[string]bool{
		activatingIntentPath(dataPath):                         true,
		applyingIntentPath(dataPath):                           true,
		stagingDirPath(dataPath):                               true,
		oldFarmPath(dataPath):                                  true,
		deactivationJournalPath(dataPath):                      true,
		retiredFarmPath(dataPath):                              true,
		NewMountManager(dataPath, "", "skyrimse").BackupPath(): true,
	}
	got := FarmSiblingSuffixes()
	if len(got) != len(want) {
		t.Fatalf("FarmSiblingSuffixes = %v, want %d suffixes", got, len(want))
	}
	for _, suffix := range got {
		if !want[dataPath+suffix] {
			t.Errorf("suffix %q names no farm lifecycle sibling", suffix)
		}
		if suffix == RetainedSessionSiblingSuffix {
			t.Error("retained launch ticket must not appear among pending farm transitions")
		}
	}
	retained := RetainedFarmSiblingSuffixes()
	if len(retained) != 3 || retained[0] != RetainedSessionSiblingSuffix ||
		retained[1] != preservedSuffix || retained[2] != maintenanceSuffix {
		t.Errorf("retained farm siblings = %v, want launch ticket, preserved batches, and maintenance marker", retained)
	}
}

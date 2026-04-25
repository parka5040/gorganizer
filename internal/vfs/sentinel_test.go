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
	want := &Sentinel{
		SchemaVersion:       CurrentSentinelSchema,
		Magic:               SentinelMagic,
		GameID:              "falloutnv",
		ProfileName:         "Default",
		ActivationPID:       12345,
		ActivationStartedAt: now,
		Hash:                "sha256:deadbeef",
		BackupPath:          backup,
		OverwriteMod:        "Overwrite",
		Layers: []SentinelLayer{
			{Name: "__base__", Root: backup, Enabled: true},
			{Name: "YUP", Root: filepath.Join(base, "mods", "YUP"), Enabled: true},
		},
		MaterializerVersion: CurrentMaterializerVersion,
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
		got.ActivationPID != want.ActivationPID {
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

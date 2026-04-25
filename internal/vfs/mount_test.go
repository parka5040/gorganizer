package vfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecoverIfNeeded_NoBackup(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")
	os.MkdirAll(dataPath, 0755)

	mm := NewMountManager(dataPath, "")
	if _, err := mm.RecoverIfNeeded(); err != nil {
		t.Fatalf("RecoverIfNeeded: %v", err)
	}

	// Data/ should still exist, no changes.
	if _, err := os.Stat(dataPath); err != nil {
		t.Fatalf("Data/ should still exist: %v", err)
	}
}

func TestRecoverIfNeeded_StaleBackup(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")
	backupPath := dataPath + ".orig"

	// Simulate crash state: Data.orig/ exists with content, Data/ is empty mountpoint.
	os.MkdirAll(backupPath, 0755)
	os.WriteFile(filepath.Join(backupPath, "test.esp"), []byte("data"), 0644)
	os.MkdirAll(dataPath, 0755)

	mm := NewMountManager(dataPath, "")
	if _, err := mm.RecoverIfNeeded(); err != nil {
		t.Fatalf("RecoverIfNeeded: %v", err)
	}

	// Data.orig/ should be gone.
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Error("Data.orig/ should have been removed after recovery")
	}

	// Data/ should exist with the original content.
	if _, err := os.Stat(filepath.Join(dataPath, "test.esp")); err != nil {
		t.Fatalf("test.esp should be in restored Data/: %v", err)
	}
}

func TestMountManagerNotMounted(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")
	os.MkdirAll(dataPath, 0755)

	mm := NewMountManager(dataPath, "")
	if mm.IsMounted() {
		t.Error("should not be mounted initially")
	}

	err := mm.Deactivate()
	if err == nil {
		t.Error("Deactivate should fail when not mounted")
	}
}

func TestMountManagerActivateBackupExists(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")
	backupPath := dataPath + ".orig"
	os.MkdirAll(dataPath, 0755)
	os.MkdirAll(backupPath, 0755) // Pre-existing backup.

	mm := NewMountManager(dataPath, "")
	layers := []Layer{{Name: "__base__", RootPath: dataPath, Enabled: true}}
	err := mm.Activate(layers)
	if err == nil {
		t.Error("Activate should fail when backup already exists")
	}
}

func TestMountManagerActivateDataMissing(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data") // Does not exist.

	mm := NewMountManager(dataPath, "")
	layers := []Layer{{Name: "__base__", RootPath: dataPath, Enabled: true}}
	err := mm.Activate(layers)
	if err == nil {
		t.Error("Activate should fail when Data/ does not exist")
	}
}

// TestActivateDeactivate_RoundTrip verifies the central guarantee of the new
// VFS: a clean Activate followed by a clean Deactivate restores the game's
// Data dir to byte-identical state. No FUSE, no daemon-in-the-IO-path, and
// the user's install dir lands exactly where it started.
func TestActivateDeactivate_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")
	if err := os.MkdirAll(dataPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataPath, "FalloutNV.esm"),
		[]byte("master file"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataPath, "Meshes"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataPath, "Meshes", "iron.nif"),
		[]byte("vanilla iron"), 0644); err != nil {
		t.Fatal(err)
	}

	mm := NewMountManager(dataPath, "")
	layers := []Layer{{Name: "__base__", RootPath: dataPath, Enabled: true}}
	if err := mm.Activate(layers); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if !mm.IsMounted() {
		t.Fatal("expected mounted after Activate")
	}

	// Sentinel must be present and parseable.
	if _, err := ReadSentinel(dataPath); err != nil {
		t.Errorf("sentinel missing/invalid after Activate: %v", err)
	}

	// Files in materialized Data must be readable.
	if got, err := os.ReadFile(filepath.Join(dataPath, "FalloutNV.esm")); err != nil ||
		string(got) != "master file" {
		t.Errorf("FalloutNV.esm = %q err=%v, want %q", string(got), err, "master file")
	}

	if err := mm.Deactivate(); err != nil {
		t.Fatalf("Deactivate: %v", err)
	}
	if mm.IsMounted() {
		t.Fatal("expected NOT mounted after Deactivate")
	}

	// Backup should be gone, original Data restored byte-identical.
	if _, err := os.Stat(dataPath + ".orig"); !os.IsNotExist(err) {
		t.Errorf("Data.orig should be removed after Deactivate, got err=%v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dataPath, "FalloutNV.esm")); err != nil ||
		string(got) != "master file" {
		t.Errorf("post-deactivate FalloutNV.esm = %q err=%v", string(got), err)
	}
	// Sentinel must NOT linger in the restored Data dir.
	if _, err := os.Stat(filepath.Join(dataPath, SentinelFilename)); !os.IsNotExist(err) {
		t.Errorf("sentinel should not survive Deactivate, got err=%v", err)
	}
}

// TestDeactivate_RefusesWithoutSentinel guards the destructive path.
// If something has clobbered the sentinel between Activate and Deactivate,
// we'd rather fail loudly than rm -rf an unverified Data tree.
func TestDeactivate_RefusesWithoutSentinel(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")
	if err := os.MkdirAll(dataPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataPath, "x.esp"), []byte("y"), 0644); err != nil {
		t.Fatal(err)
	}

	mm := NewMountManager(dataPath, "")
	layers := []Layer{{Name: "__base__", RootPath: dataPath, Enabled: true}}
	if err := mm.Activate(layers); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	// Clobber the sentinel.
	if err := os.Remove(filepath.Join(dataPath, SentinelFilename)); err != nil {
		t.Fatal(err)
	}

	if err := mm.Deactivate(); err == nil {
		t.Error("Deactivate should refuse when sentinel is missing")
	}
}

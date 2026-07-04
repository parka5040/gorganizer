package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFile_CreatesWithExactPerm(t *testing.T) {
	dir := t.TempDir()
	for _, perm := range []os.FileMode{0600, 0644} {
		path := filepath.Join(dir, "perm.txt")
		if err := WriteFile(path, []byte("hello"), perm); err != nil {
			t.Fatalf("WriteFile(perm=%o): %v", perm, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if got := info.Mode().Perm(); got != perm {
			t.Fatalf("perm = %o, want %o", got, perm)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(got) != "hello" {
			t.Fatalf("content = %q, want hello", got)
		}
	}
}

func TestWriteFile_OverwritesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")
	if err := WriteFile(path, []byte("v1"), 0644); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteFile(path, []byte("v2-longer"), 0644); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "v2-longer" {
		t.Fatalf("content = %q, want v2-longer", got)
	}
	// No stray temp files should remain in the directory.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 file, found %d: %v", len(entries), entries)
	}
}

func TestWriteFile_LeavesNoTempOnBadDir(t *testing.T) {
	// A non-existent directory makes CreateTemp fail; the destination and its
	// parent must be untouched (nothing to clean up, no panic).
	err := WriteFile(filepath.Join(t.TempDir(), "missing", "x.txt"), []byte("x"), 0644)
	if err == nil {
		t.Fatal("expected error writing into a missing directory")
	}
}

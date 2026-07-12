package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestImportScratchOutputReplacesNamedOutput(t *testing.T) {
	root := t.TempDir()
	scratch := filepath.Join(root, "scratch")
	capture := filepath.Join(root, "Generated Output")
	if err := os.MkdirAll(filepath.Join(scratch, "meshes"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(capture, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(capture, "stale.txt"), []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "meshes", "generated.nif"), []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}

	count, err := importScratchOutput(scratch, capture, false)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("imported %d files, want 1", count)
	}
	if _, err := os.Stat(filepath.Join(capture, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("stale generated output survived replacement: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(capture, "meshes", "generated.nif")); err != nil || string(got) != "new" {
		t.Fatalf("generated output = %q, %v", got, err)
	}
}

func TestImportScratchOutputPreservesOverwrite(t *testing.T) {
	root := t.TempDir()
	scratch := filepath.Join(root, "scratch")
	capture := filepath.Join(root, "Overwrite")
	if err := os.MkdirAll(scratch, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(capture, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(capture, "unrelated.txt"), []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "generated.txt"), []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := importScratchOutput(scratch, capture, true); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"unrelated.txt": "keep", "generated.txt": "new"} {
		got, err := os.ReadFile(filepath.Join(capture, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v; want %q", name, got, err, want)
		}
	}
}

func TestImportScratchOutputRejectsSymlinkWithoutChangingCapture(t *testing.T) {
	root := t.TempDir()
	scratch := filepath.Join(root, "scratch")
	capture := filepath.Join(root, "output")
	if err := os.MkdirAll(scratch, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(capture, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(capture, "existing.txt"), []byte("safe"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(scratch, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := importScratchOutput(scratch, capture, false); err == nil {
		t.Fatal("symlinked scratch output was accepted")
	}
	got, err := os.ReadFile(filepath.Join(capture, "existing.txt"))
	if err != nil || string(got) != "safe" {
		t.Fatalf("capture changed after rejected import: %q, %v", got, err)
	}
}

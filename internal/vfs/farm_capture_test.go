package vfs

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func activatedCaptureFarm(t *testing.T, modFiles map[string]string) (string, string, string, *MountManager) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	data := filepath.Join(root, "Game", "Data")
	modRoot := filepath.Join(root, "Mods", "Source")
	overwrite := filepath.Join(root, "Mods", "Overwrite")
	for _, dir := range []string{data, modRoot, overwrite} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	for rel, body := range modFiles {
		writeCaptureFile(t, filepath.Join(modRoot, filepath.FromSlash(rel)), body)
	}
	mm := NewMountManager(data, overwrite, "testgame")
	if err := mm.Activate([]Layer{
		{Name: "__base__", RootPath: data, Enabled: true},
		{Name: "Source", RootPath: modRoot, Enabled: true},
	}, "Default"); err != nil {
		t.Fatal(err)
	}
	return data, modRoot, overwrite, mm
}

func writeCaptureFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func assertEmptyCaptureDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries in %q = %v, %v; want empty", dir, entries, err)
	}
}

// TestOrphanFarmLinksAreNotOutput checks a removed mod source does not reappear in Overwrite.
func TestOrphanFarmLinksAreNotOutput(t *testing.T) {
	data, modRoot, overwrite, mm := activatedCaptureFarm(t, map[string]string{"item.esp": "placed"})
	if err := os.Remove(filepath.Join(modRoot, "item.esp")); err != nil {
		t.Fatal(err)
	}
	s, err := ReadSentinel(data)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadFarmManifest(data, s)
	if err != nil {
		t.Fatal(err)
	}
	classification, err := ClassifyFarm(data, manifest)
	if err != nil || len(classification.Owned) != 1 || len(classification.Output) != 0 {
		t.Fatalf("classification = %+v, %v; want one owned file", classification, err)
	}
	if err := mm.Deactivate(); err != nil {
		t.Fatal(err)
	}
	assertEmptyCaptureDir(t, overwrite)
}

// TestReplacedPlacedFileIsCaptured checks that a new inode at a placed path is captured.
func TestReplacedPlacedFileIsCaptured(t *testing.T) {
	data, _, overwrite, mm := activatedCaptureFarm(t, map[string]string{"item.esp": "placed"})
	writeCaptureFile(t, filepath.Join(data, "replacement.tmp"), "new output")
	if err := os.Rename(filepath.Join(data, "replacement.tmp"), filepath.Join(data, "item.esp")); err != nil {
		t.Fatal(err)
	}
	if err := mm.Deactivate(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(overwrite, "item.esp"))
	if err != nil || string(body) != "new output" {
		t.Fatalf("captured replacement = %q, %v", body, err)
	}
}

// TestNewMultiplyLinkedOutputIsCaptured checks that a new file is captured even with an outside hardlink.
func TestNewMultiplyLinkedOutputIsCaptured(t *testing.T) {
	data, _, overwrite, mm := activatedCaptureFarm(t, nil)
	writeCaptureFile(t, filepath.Join(data, "result.txt"), "output")
	outside := filepath.Join(t.TempDir(), "second-link.txt")
	if err := os.Link(filepath.Join(data, "result.txt"), outside); err != nil {
		t.Fatal(err)
	}
	if err := mm.Deactivate(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{outside, filepath.Join(overwrite, "result.txt")} {
		body, err := os.ReadFile(name)
		if err != nil || string(body) != "output" {
			t.Errorf("%s = %q, %v; want output", name, body, err)
		}
	}
}

// TestCasefoldVariantIsCaptured checks that exact path identity does not fold case.
func TestCasefoldVariantIsCaptured(t *testing.T) {
	data, _, overwrite, mm := activatedCaptureFarm(t, map[string]string{"textures/a.dds": "placed"})
	writeCaptureFile(t, filepath.Join(data, "textures", "A.dds"), "new output")
	if err := mm.Deactivate(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(overwrite, "textures", "A.dds"))
	if err != nil || string(body) != "new output" {
		t.Fatalf("captured case variant = %q, %v", body, err)
	}
	if _, err := os.Lstat(filepath.Join(overwrite, "textures", "a.dds")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("placed file entered Overwrite: %v", err)
	}
}

// TestUnknownSymlinkIsNotCapturedOrFollowed checks that game-created links leave outside targets untouched.
func TestUnknownSymlinkIsNotCapturedOrFollowed(t *testing.T) {
	data, _, overwrite, mm := activatedCaptureFarm(t, nil)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeCaptureFile(t, outside, "outside")
	if err := os.Symlink(outside, filepath.Join(data, "external.txt")); err != nil {
		t.Fatal(err)
	}
	s, err := ReadSentinel(data)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadFarmManifest(data, s)
	if err != nil {
		t.Fatal(err)
	}
	classification, err := ClassifyFarm(data, manifest)
	if err != nil || len(classification.UnknownSymlink) != 1 || classification.UnknownSymlink[0] != "external.txt" || len(classification.Output) != 0 {
		t.Fatalf("classification = %+v, %v; want unknown symlink", classification, err)
	}
	if err := mm.Deactivate(); err != nil {
		t.Fatal(err)
	}
	assertEmptyCaptureDir(t, overwrite)
	body, err := os.ReadFile(outside)
	if err != nil || string(body) != "outside" {
		t.Errorf("outside target = %q, %v", body, err)
	}
}

// TestSpecialFarmEntryIsNotCaptured checks that a FIFO stays out of Overwrite.
func TestSpecialFarmEntryIsNotCaptured(t *testing.T) {
	data, _, overwrite, _ := activatedCaptureFarm(t, nil)
	pipe := filepath.Join(data, "tool.pipe")
	if err := syscall.Mkfifo(pipe, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := ReadSentinel(data)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadFarmManifest(data, s)
	if err != nil {
		t.Fatal(err)
	}
	classification, err := ClassifyFarm(data, manifest)
	if err != nil || len(classification.Special) != 1 || classification.Special[0] != "tool.pipe" || len(classification.Output) != 0 {
		t.Fatalf("classification = %+v, %v; want one special entry", classification, err)
	}
	moved, err := CaptureNewFiles(data, overwrite)
	if err != nil || moved != 0 {
		t.Fatalf("capture = %d, %v; want zero", moved, err)
	}
	assertEmptyCaptureDir(t, overwrite)
	if _, err := os.Lstat(pipe); err != nil {
		t.Errorf("FIFO was removed: %v", err)
	}
}

// TestLegacySentinelKeepsLinkCountRule checks that v2 farms still capture single-link files.
func TestLegacySentinelKeepsLinkCountRule(t *testing.T) {
	data, modRoot, overwrite, _ := activatedCaptureFarm(t, map[string]string{"item.esp": "placed"})
	s, err := ReadSentinel(data)
	if err != nil {
		t.Fatal(err)
	}
	s.SchemaVersion = 2
	if err := WriteSentinel(data, s); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(modRoot, "item.esp")); err != nil {
		t.Fatal(err)
	}
	writeCaptureFile(t, filepath.Join(data, "new.txt"), "multiply linked")
	outside := filepath.Join(t.TempDir(), "link")
	if err := os.Link(filepath.Join(data, "new.txt"), outside); err != nil {
		t.Fatal(err)
	}
	moved, err := CaptureNewFiles(data, overwrite)
	if err != nil || moved != 1 {
		t.Fatalf("legacy capture = %d, %v; want orphan only", moved, err)
	}
	body, err := os.ReadFile(filepath.Join(overwrite, "item.esp"))
	if err != nil || string(body) != "placed" {
		t.Errorf("orphan in Overwrite = %q, %v", body, err)
	}
	if _, err := os.Lstat(filepath.Join(overwrite, "new.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("multiply linked output was captured: %v", err)
	}
}

// TestInvalidManifestFallsBackToLinkCount checks a corrupt v3 manifest uses the legacy capture rule.
func TestInvalidManifestFallsBackToLinkCount(t *testing.T) {
	for _, tc := range []struct {
		name    string
		corrupt func(*testing.T, string)
	}{
		{"missing", func(t *testing.T, path string) {
			t.Helper()
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
		{"invalid hash", func(t *testing.T, path string) { t.Helper(); writeCaptureFile(t, path, "invalid manifest") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, modRoot, overwrite, _ := activatedCaptureFarm(t, map[string]string{"item.esp": "placed"})
			s, err := ReadSentinel(data)
			if err != nil {
				t.Fatal(err)
			}
			tc.corrupt(t, filepath.Join(data, s.Manifest))
			if err := os.Remove(filepath.Join(modRoot, "item.esp")); err != nil {
				t.Fatal(err)
			}
			moved, err := CaptureNewFiles(data, overwrite)
			if err != nil || moved != 1 {
				t.Fatalf("legacy fallback = %d, %v; want orphan captured", moved, err)
			}
			body, err := os.ReadFile(filepath.Join(overwrite, "item.esp"))
			if err != nil || string(body) != "placed" {
				t.Errorf("captured orphan = %q, %v", body, err)
			}
		})
	}
}

// TestRelinkedToolOutputIsNotRecaptured checks successor manifest ownership protects named mod output.
func TestRelinkedToolOutputIsNotRecaptured(t *testing.T) {
	data, _, overwrite, _ := activatedCaptureFarm(t, map[string]string{"item.esp": "placed"})
	old, err := ReadSentinel(data)
	if err != nil {
		t.Fatal(err)
	}
	named := filepath.Join(t.TempDir(), "Named Output")
	writeCaptureFile(t, filepath.Join(data, "meshes", "tool.nif"), "generated")
	moved, err := CaptureNewFilesInto(data, named, true, false)
	if err != nil || moved != 1 {
		t.Fatalf("tool capture = %d, %v; want one file", moved, err)
	}
	s, err := ReadSentinel(data)
	if err != nil {
		t.Fatal(err)
	}
	if s.FarmID == old.FarmID || s.Manifest == old.Manifest || s.ManifestEntries != old.ManifestEntries+1 {
		t.Fatalf("sentinel = %+v; want new manifest and one more entry", s)
	}
	if _, err := os.Lstat(filepath.Join(data, old.Manifest)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old manifest remains after successor commit: %v", err)
	}
	manifest, err := ReadFarmManifest(data, s)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Entries["meshes/tool.nif"].Type != "f" {
		t.Errorf("successor lost tool output: %+v", manifest.Entries)
	}
	moved, err = CaptureNewFiles(data, overwrite)
	if err != nil || moved != 0 {
		t.Fatalf("second capture = %d, %v; want zero", moved, err)
	}
	assertEmptyCaptureDir(t, overwrite)
	for _, path := range []string{filepath.Join(named, "meshes", "tool.nif"), filepath.Join(data, "meshes", "tool.nif")} {
		body, err := os.ReadFile(path)
		if err != nil || string(body) != "generated" {
			t.Errorf("output at %s = %q, %v", path, body, err)
		}
	}
}

// TestRelinkedCrossDeviceOutputStaysOwned checks that a symlink fallback is recorded in the successor manifest.
func TestRelinkedCrossDeviceOutputStaysOwned(t *testing.T) {
	data, _, overwrite, _ := activatedCaptureFarm(t, nil)
	named := filepath.Join(t.TempDir(), "Named Output")
	writeCaptureFile(t, filepath.Join(data, "tool.txt"), "generated")
	original := devIDOf
	devIDOf = func(path string) (uint64, error) {
		if path == filepath.Join(named, "tool.txt") {
			return ^uint64(0), nil
		}
		return original(path)
	}
	t.Cleanup(func() { devIDOf = original })
	moved, err := CaptureNewFilesInto(data, named, true, false)
	if err != nil || moved != 1 {
		t.Fatalf("tool capture = %d, %v; want one symlink fallback", moved, err)
	}
	devIDOf = original
	s, err := ReadSentinel(data)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadFarmManifest(data, s)
	if err != nil {
		t.Fatal(err)
	}
	entry := manifest.Entries["tool.txt"]
	if entry.Type != "l" || entry.LinkTarget != filepath.Join(named, "tool.txt") {
		t.Fatalf("symlink entry = %+v; want link to named output", entry)
	}
	moved, err = CaptureNewFiles(data, overwrite)
	if err != nil || moved != 0 {
		t.Fatalf("second capture = %d, %v; want zero", moved, err)
	}
	assertEmptyCaptureDir(t, overwrite)
}

// TestRelinkedCasefoldVariantStaysOwned checks a successor manifest can record paths differing only in case.
func TestRelinkedCasefoldVariantStaysOwned(t *testing.T) {
	data, _, overwrite, _ := activatedCaptureFarm(t, map[string]string{"textures/a.dds": "placed"})
	named := filepath.Join(t.TempDir(), "Named Output")
	writeCaptureFile(t, filepath.Join(data, "textures", "A.dds"), "generated")
	moved, err := CaptureNewFilesInto(data, named, true, false)
	if err != nil || moved != 1 {
		t.Fatalf("tool capture = %d, %v; want case variant", moved, err)
	}
	s, err := ReadSentinel(data)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadFarmManifest(data, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Entries) != 2 || manifest.Entries["textures/a.dds"].Type != "f" || manifest.Entries["textures/A.dds"].Type != "f" {
		t.Fatalf("successor entries = %+v; want both case variants", manifest.Entries)
	}
	moved, err = CaptureNewFiles(data, overwrite)
	if err != nil || moved != 0 {
		t.Fatalf("second capture = %d, %v; want zero", moved, err)
	}
	assertEmptyCaptureDir(t, overwrite)
}

// TestCrashBeforeSuccessorSentinelLosesNothing checks the old manifest remains authoritative after an interrupted publish.
func TestCrashBeforeSuccessorSentinelLosesNothing(t *testing.T) {
	data, _, overwrite, _ := activatedCaptureFarm(t, nil)
	old, err := ReadSentinel(data)
	if err != nil {
		t.Fatal(err)
	}
	named := filepath.Join(t.TempDir(), "Named Output")
	writeCaptureFile(t, filepath.Join(data, "tool.txt"), "generated")
	original := writeSuccessorSentinel
	writeSuccessorSentinel = func(string, *Sentinel) error { return syscall.EIO }
	t.Cleanup(func() { writeSuccessorSentinel = original })
	moved, err := CaptureNewFilesInto(data, named, true, false)
	if moved != 1 || !errors.Is(err, syscall.EIO) {
		t.Fatalf("interrupted capture = %d, %v; want one moved and EIO", moved, err)
	}
	writeSuccessorSentinel = original
	current, err := ReadSentinel(data)
	if err != nil || current.FarmID != old.FarmID || current.Manifest != old.Manifest {
		t.Fatalf("sentinel after interrupted publish = %+v, %v", current, err)
	}
	if _, err := ReadFarmManifest(data, current); err != nil {
		t.Fatal(err)
	}
	manifests, err := filepath.Glob(filepath.Join(data, farmManifestPrefix+"*.jsonl"))
	if err != nil || len(manifests) != 2 {
		t.Fatalf("published manifests after failed sentinel write = %v, %v; want old and successor", manifests, err)
	}
	moved, err = CaptureNewFiles(data, overwrite)
	if err != nil || moved != 1 {
		t.Fatalf("retry capture = %d, %v; want relinked output", moved, err)
	}
	for _, path := range []string{filepath.Join(named, "tool.txt"), filepath.Join(overwrite, "tool.txt")} {
		body, err := os.ReadFile(path)
		if err != nil || string(body) != "generated" {
			t.Errorf("output at %s = %q, %v", path, body, err)
		}
	}
}

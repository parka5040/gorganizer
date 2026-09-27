package vfs

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/atomicfile"
)

// forceCaptureEXDEV makes captured renames take the cross-device copy path.
func forceCaptureEXDEV(t *testing.T) {
	t.Helper()
	original := captureRename
	captureRename = func(src, dst string) error {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: syscall.EXDEV}
	}
	t.Cleanup(func() { captureRename = original })
}

// TestCrossDeviceCaptureFailureKeepsPreviousOverwrite checks a failed copy retains both versions.
func TestCrossDeviceCaptureFailureKeepsPreviousOverwrite(t *testing.T) {
	data, _, overwrite, mm := activatedCaptureFarm(t, nil)
	src := filepath.Join(data, "a.txt")
	dst := filepath.Join(overwrite, "a.txt")
	writeCaptureFile(t, src, "new")
	writeCaptureFile(t, dst, "old")
	forceCaptureEXDEV(t)
	original := captureCopy
	captureCopy = func(_, dst string, _ os.FileMode, _ bool) (atomicfile.Outcome, error) {
		partial, err := os.CreateTemp(filepath.Dir(dst), ".partial-*")
		if err != nil {
			return atomicfile.NotPublished, err
		}
		defer os.Remove(partial.Name())
		_, _ = partial.WriteString("n")
		_ = partial.Close()
		return atomicfile.NotPublished, syscall.ENOSPC
	}
	t.Cleanup(func() { captureCopy = original })
	if err := mm.Deactivate(); !errors.Is(err, ErrCaptureFailed) {
		t.Fatalf("deactivate = %v; want capture failure", err)
	}
	if body, err := os.ReadFile(dst); err != nil || string(body) != "old" {
		t.Errorf("previous overwrite = %q, %v; want old", body, err)
	}
	if body, err := os.ReadFile(src); err != nil || string(body) != "new" {
		t.Errorf("farm file = %q, %v; want new", body, err)
	}
	if _, err := ReadSentinel(data); err != nil {
		t.Errorf("farm no longer mounted: %v", err)
	}
}

// TestCrossDeviceCaptureSucceeds checks both manifest and legacy captures publish complete files.
func TestCrossDeviceCaptureSucceeds(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "manifest"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			data, _, overwrite, _ := activatedCaptureFarm(t, nil)
			if legacy {
				s, err := ReadSentinel(data)
				if err != nil {
					t.Fatal(err)
				}
				s.SchemaVersion = 2
				if err := WriteSentinel(data, s); err != nil {
					t.Fatal(err)
				}
			}
			src := filepath.Join(data, "a.txt")
			dst := filepath.Join(overwrite, "a.txt")
			writeCaptureFile(t, src, "new")
			writeCaptureFile(t, dst, "old")
			if err := os.Chmod(src, 0620); err != nil {
				t.Fatal(err)
			}
			forceCaptureEXDEV(t)
			moved, err := CaptureNewFiles(data, overwrite)
			if err != nil || moved != 1 {
				t.Fatalf("capture = %d, %v; want one file", moved, err)
			}
			if body, err := os.ReadFile(dst); err != nil || string(body) != "new" {
				t.Errorf("overwrite = %q, %v; want new", body, err)
			}
			if info, err := os.Stat(dst); err != nil || info.Mode().Perm() != 0620 {
				t.Errorf("overwrite permissions = %v, %v; want 0620", info, err)
			}
			if _, err := os.Lstat(src); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("farm source remains: %v", err)
			}
		})
	}
}

// TestCrossDeviceCaptureSyncsNewAncestorsBeforeUnlink checks nested destination directories are durable before source removal.
func TestCrossDeviceCaptureSyncsNewAncestorsBeforeUnlink(t *testing.T) {
	data, _, overwrite, _ := activatedCaptureFarm(t, nil)
	src := filepath.Join(data, "Saves", "Character", "new.ess")
	writeCaptureFile(t, src, "new save")
	forceCaptureEXDEV(t)
	originalSync := captureSyncDir
	originalRemove := captureRemove
	var events []string
	captureSyncDir = func(dir string) error {
		events = append(events, "sync "+dir)
		return originalSync(dir)
	}
	captureRemove = func(path string) error {
		events = append(events, "remove "+path)
		return originalRemove(path)
	}
	t.Cleanup(func() {
		captureSyncDir = originalSync
		captureRemove = originalRemove
	})
	if moved, err := CaptureNewFiles(data, overwrite); moved != 1 || err != nil {
		t.Fatalf("CaptureNewFiles = %d, %v; want one file", moved, err)
	}
	want := []string{
		"sync " + filepath.Join(overwrite, "Saves", "Character"),
		"sync " + filepath.Join(overwrite, "Saves"),
		"sync " + overwrite,
		"remove " + src,
	}
	if len(events) < len(want) || !slices.Equal(events[:len(want)], want) {
		t.Errorf("events = %v; want prefix %v", events, want)
	}
}

// TestCrossDeviceCaptureAncestorSyncFailureKeepsSource checks a failed directory sync never unlinks the farm copy.
func TestCrossDeviceCaptureAncestorSyncFailureKeepsSource(t *testing.T) {
	data, _, overwrite, _ := activatedCaptureFarm(t, nil)
	src := filepath.Join(data, "Saves", "Character", "new.ess")
	writeCaptureFile(t, src, "new save")
	forceCaptureEXDEV(t)
	originalSync := captureSyncDir
	originalRemove := captureRemove
	removed := false
	captureSyncDir = func(dir string) error {
		if dir == filepath.Join(overwrite, "Saves") {
			return syscall.EIO
		}
		return originalSync(dir)
	}
	captureRemove = func(path string) error {
		removed = true
		return originalRemove(path)
	}
	t.Cleanup(func() {
		captureSyncDir = originalSync
		captureRemove = originalRemove
	})
	if moved, err := CaptureNewFiles(data, overwrite); moved != 0 || !errors.Is(err, ErrCaptureFailed) {
		t.Fatalf("CaptureNewFiles = %d, %v; want sync failure", moved, err)
	}
	if removed || mustRead(t, src) != "new save" {
		t.Errorf("source removed = %t; want intact save", removed)
	}
}

// TestCaptureFailureSyncsCreatedDirectories checks an interrupted batch still flushes earlier destination directories.
func TestCaptureFailureSyncsCreatedDirectories(t *testing.T) {
	data, _, overwrite, _ := activatedCaptureFarm(t, nil)
	writeCaptureFile(t, filepath.Join(data, "a", "first.txt"), "first")
	writeCaptureFile(t, filepath.Join(data, "z", "second.txt"), "second")
	if err := os.Symlink(t.TempDir(), filepath.Join(overwrite, "z")); err != nil {
		t.Fatal(err)
	}
	original := captureSyncDir
	var synced []string
	captureSyncDir = func(dir string) error {
		synced = append(synced, dir)
		return original(dir)
	}
	t.Cleanup(func() { captureSyncDir = original })
	if moved, err := CaptureNewFiles(data, overwrite); moved != 1 || !errors.Is(err, ErrCaptureFailed) {
		t.Fatalf("CaptureNewFiles = %d, %v; want one move and failure", moved, err)
	}
	if !slices.Contains(synced, overwrite) || !slices.Contains(synced, filepath.Join(overwrite, "a")) {
		t.Errorf("synced directories = %v; want new directory and its parent", synced)
	}
}

// TestCaptureDetectsChangingSource checks inode, size and mtime changes retain the farm file.
func TestCaptureDetectsChangingSource(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, string, os.FileInfo)
	}{
		{"mtime", func(t *testing.T, src string, info os.FileInfo) {
			when := info.ModTime().Add(2 * time.Second)
			if err := os.Chtimes(src, when, when); err != nil {
				t.Fatal(err)
			}
		}},
		{"size", func(t *testing.T, src string, info os.FileInfo) {
			writeCaptureFile(t, src, "new output")
			if err := os.Chtimes(src, info.ModTime(), info.ModTime()); err != nil {
				t.Fatal(err)
			}
		}},
		{"inode", func(t *testing.T, src string, info os.FileInfo) {
			replacement := filepath.Join(filepath.Dir(src), "replacement")
			writeCaptureFile(t, replacement, "new")
			if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(replacement, src); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, _, overwrite, _ := activatedCaptureFarm(t, nil)
			src := filepath.Join(data, "a.txt")
			writeCaptureFile(t, src, "new")
			forceCaptureEXDEV(t)
			original := captureCopy
			captureCopy = func(src, dst string, perm os.FileMode, replace bool) (atomicfile.Outcome, error) {
				outcome, err := original(src, dst, perm, replace)
				if err == nil {
					info, statErr := os.Lstat(src)
					if statErr != nil {
						return outcome, statErr
					}
					tc.mutate(t, src, info)
				}
				return outcome, err
			}
			t.Cleanup(func() { captureCopy = original })
			moved, err := CaptureNewFiles(data, overwrite)
			if moved != 0 || !errors.Is(err, ErrCaptureFailed) || !strings.Contains(err.Error(), "changed during copy") {
				t.Fatalf("capture = %d, %v; want source-change failure", moved, err)
			}
			if _, err := os.Lstat(src); err != nil {
				t.Errorf("farm source removed: %v", err)
			}
			if body, err := os.ReadFile(filepath.Join(overwrite, "a.txt")); err != nil || string(body) != "new" {
				t.Errorf("published snapshot = %q, %v; want new", body, err)
			}
		})
	}
}

// TestCaptureRefusesSymlinkedDestinationAncestor checks capture never follows an Overwrite link.
func TestCaptureRefusesSymlinkedDestinationAncestor(t *testing.T) {
	data, _, overwrite, _ := activatedCaptureFarm(t, nil)
	src := filepath.Join(data, "textures", "a.txt")
	writeCaptureFile(t, src, "new")
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(overwrite, "textures")); err != nil {
		t.Fatal(err)
	}
	moved, err := CaptureNewFiles(data, overwrite)
	if moved != 0 || !errors.Is(err, ErrCaptureFailed) {
		t.Fatalf("capture = %d, %v; want unsafe-destination failure", moved, err)
	}
	if body, err := os.ReadFile(src); err != nil || string(body) != "new" {
		t.Errorf("farm source = %q, %v; want new", body, err)
	}
	assertEmptyCaptureDir(t, outside)
}

// TestCaptureRefusesNonRegularDestination checks unsafe destination types remain untouched.
func TestCaptureRefusesNonRegularDestination(t *testing.T) {
	for _, tc := range []struct {
		name   string
		create func(*testing.T, string)
	}{
		{"directory", func(t *testing.T, dst string) {
			if err := os.Mkdir(dst, 0755); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlink", func(t *testing.T, dst string) {
			outside := filepath.Join(t.TempDir(), "outside.txt")
			writeCaptureFile(t, outside, "old")
			if err := os.Symlink(outside, dst); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if body, err := os.ReadFile(outside); err != nil || string(body) != "old" {
					t.Errorf("outside file = %q, %v; want old", body, err)
				}
			})
		}},
		{"fifo", func(t *testing.T, dst string) {
			if err := syscall.Mkfifo(dst, 0600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, _, overwrite, _ := activatedCaptureFarm(t, nil)
			src := filepath.Join(data, "a.txt")
			writeCaptureFile(t, src, "new")
			dst := filepath.Join(overwrite, "a.txt")
			tc.create(t, dst)
			before, err := os.Lstat(dst)
			if err != nil {
				t.Fatal(err)
			}
			moved, err := CaptureNewFiles(data, overwrite)
			if moved != 0 || !errors.Is(err, ErrCaptureFailed) {
				t.Fatalf("capture = %d, %v; want non-regular destination failure", moved, err)
			}
			if body, err := os.ReadFile(src); err != nil || string(body) != "new" {
				t.Errorf("farm source = %q, %v; want new", body, err)
			}
			after, err := os.Lstat(dst)
			if err != nil || !os.SameFile(before, after) {
				t.Errorf("destination changed: before %v, after %v, err %v", before, after, err)
			}
		})
	}
}

// TestCaptureRechecksDestinationAfterDirectoryCreation checks a replaced parent is rejected.
func TestCaptureRechecksDestinationAfterDirectoryCreation(t *testing.T) {
	data, _, overwrite, _ := activatedCaptureFarm(t, nil)
	src := filepath.Join(data, "textures", "a.txt")
	writeCaptureFile(t, src, "new")
	outside := t.TempDir()
	original := captureMkdirAll
	captureMkdirAll = func(path string, mode os.FileMode) error {
		if path == filepath.Join(overwrite, "textures") {
			return os.Symlink(outside, path)
		}
		return original(path, mode)
	}
	t.Cleanup(func() { captureMkdirAll = original })
	moved, err := CaptureNewFiles(data, overwrite)
	if moved != 0 || !errors.Is(err, ErrCaptureFailed) {
		t.Fatalf("capture = %d, %v; want rechecked ancestor failure", moved, err)
	}
	if body, err := os.ReadFile(src); err != nil || string(body) != "new" {
		t.Errorf("farm source = %q, %v; want new", body, err)
	}
	assertEmptyCaptureDir(t, outside)
}

// TestCaptureSyncsDestinationDirectories checks deduplicated directory syncs and failure handling.
func TestCaptureSyncsDestinationDirectories(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			data, _, overwrite, mm := activatedCaptureFarm(t, nil)
			for _, rel := range []string{"textures/a.txt", "textures/b.txt", "other/c.txt"} {
				writeCaptureFile(t, filepath.Join(data, rel), "new")
			}
			original := captureSyncDir
			var synced []string
			captureSyncDir = func(dir string) error {
				synced = append(synced, dir)
				if fail && dir == filepath.Join(overwrite, "textures") {
					return syscall.EIO
				}
				return original(dir)
			}
			t.Cleanup(func() { captureSyncDir = original })
			err := mm.Deactivate()
			if fail {
				if !errors.Is(err, ErrCaptureFailed) {
					t.Fatalf("deactivate = %v; want capture failure", err)
				}
				if _, err := ReadSentinel(data); err != nil {
					t.Errorf("farm was torn down: %v", err)
				}
			} else if err != nil {
				t.Fatalf("deactivate: %v", err)
			}
			if !slices.Contains(synced, filepath.Join(overwrite, "textures")) || !slices.Contains(synced, filepath.Join(overwrite, "other")) && !fail {
				t.Errorf("synced directories = %v; missing destination directory", synced)
			}
			seen := make(map[string]bool)
			for _, dir := range synced {
				if seen[dir] {
					t.Errorf("directory synced more than once: %q", dir)
				}
				seen[dir] = true
			}
			if !fail && !slices.Contains(synced, overwrite) {
				t.Errorf("new subdirectories' parent was not synced: %v", synced)
			}
		})
	}
}

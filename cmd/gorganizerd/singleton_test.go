package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/config"
)

// singletonTestRuntime sets an isolated short runtime directory for the singleton tests.
func singletonTestRuntime(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir, err := os.MkdirTemp("", "gzr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XDG_RUNTIME_DIR", dir)
	return config.RuntimeDir()
}

// TestSingletonRejectsLinkedLock checks that symlinked and hardlinked locks cannot modify their targets.
func TestSingletonRejectsLinkedLock(t *testing.T) {
	cases := []struct {
		name string
		link func(target, lockPath string) error
	}{
		{name: "symlink", link: os.Symlink},
		{name: "hardlink", link: os.Link},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := singletonTestRuntime(t)
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(filepath.Dir(dir), "target")
			if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := tc.link(target, config.LockPath()); err != nil {
				t.Fatal(err)
			}
			if release, err := acquireSingleInstanceLock(); err == nil {
				release()
				t.Fatal("accepted a linked lock")
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "unchanged" {
				t.Fatalf("target changed: data %q, error %v", data, err)
			}
			if tc.name == "symlink" {
				if link, err := os.Readlink(config.LockPath()); err != nil || link != target {
					t.Errorf("lock symlink changed: target %q, error %v", link, err)
				}
			}
		})
	}
}

// TestSingletonKeepsStableLockInode checks that releases preserve the file used for locking.
func TestSingletonKeepsStableLockInode(t *testing.T) {
	singletonTestRuntime(t)
	release, err := acquireSingleInstanceLock()
	if err != nil {
		t.Fatal(err)
	}
	first, err := os.Lstat(config.LockPath())
	if err != nil {
		release()
		t.Fatal(err)
	}
	if other, err := acquireSingleInstanceLock(); err == nil {
		other()
		release()
		t.Fatal("second daemon acquired the held lock")
	} else if !strings.Contains(err.Error(), "Gorganizer's background service is already running") {
		t.Errorf("unexpected contention error: %v", err)
	}
	release()
	stillThere, err := os.Lstat(config.LockPath())
	if err != nil {
		t.Fatalf("lock removed on release: %v", err)
	}
	if !os.SameFile(first, stillThere) {
		t.Error("lock inode changed on release")
	}
	secondRelease, err := acquireSingleInstanceLock()
	if err != nil {
		t.Fatal(err)
	}
	defer secondRelease()
	second, err := os.Lstat(config.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(first, second) || !second.Mode().IsRegular() {
		t.Errorf("lock inode changed or is not regular: before %v, after %v", first, second)
	}
}

// TestSingletonRejectsUnsafeRuntimeDir checks that a symlinked runtime folder is refused without modification.
func TestSingletonRejectsUnsafeRuntimeDir(t *testing.T) {
	dir := singletonTestRuntime(t)
	target := t.TempDir()
	if err := os.Symlink(target, dir); err != nil {
		t.Fatal(err)
	}
	if release, err := acquireSingleInstanceLock(); err == nil {
		release()
		t.Fatal("accepted symlinked runtime directory")
	} else if !strings.Contains(err.Error(), "No existing files were removed.") {
		t.Errorf("unexpected unsafe runtime error: %v", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatalf("runtime folder removed: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("runtime symlink changed: %v", info)
	}
	if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
		t.Errorf("symlink target modified: %v %v", entries, err)
	}
}

// TestSingletonTightensOwnedPublicRuntimeDir checks that an owned 0755 runtime folder left by an older launcher is made private and used.
func TestSingletonTightensOwnedPublicRuntimeDir(t *testing.T) {
	dir := singletonTestRuntime(t)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	release, err := acquireSingleInstanceLock()
	if err != nil {
		t.Fatalf("acquireSingleInstanceLock: %v", err)
	}
	release()
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("runtime folder mode = %v, want 0700", info.Mode().Perm())
	}
}

package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEnsurePrivateDirCreatesAndAccepts checks creation, permissions, and reuse of a private directory.
func TestEnsurePrivateDirCreatesAndAccepts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "private")
	for i := 0; i < 2; i++ {
		if err := EnsurePrivateDir(path); err != nil {
			t.Fatalf("EnsurePrivateDir attempt %d: %v", i, err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Errorf("mode = %v, want private directory with mode 0700", info.Mode())
		}
	}
}

// TestEnsurePrivateDirRejectsUnsafe checks that unsafe entries remain unchanged when refused.
func TestEnsurePrivateDirRejectsUnsafe(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cases := []struct {
		name    string
		prepare func(t *testing.T, path string)
	}{
		{name: "symlink to directory", prepare: func(t *testing.T, path string) {
			target := filepath.Join(filepath.Dir(path), "target")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "regular file", prepare: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("leave this intact"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "private")
			tc.prepare(t, path)
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := EnsurePrivateDir(path); err == nil {
				t.Fatal("EnsurePrivateDir accepted an unsafe entry")
			}
			after, err := os.Lstat(path)
			if err != nil {
				t.Fatalf("unsafe entry removed: %v", err)
			}
			if !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Errorf("entry changed: before %v, after %v", before, after)
			}
			if before.Mode()&os.ModeSymlink != 0 {
				target := filepath.Join(filepath.Dir(path), "target")
				if link, err := os.Readlink(path); err != nil || link != target {
					t.Errorf("symlink changed: target %q, error %v", link, err)
				}
				info, err := os.Lstat(target)
				if err != nil {
					t.Fatalf("symlink target removed: %v", err)
				}
				if !info.IsDir() || info.Mode().Perm() != 0o700 {
					t.Errorf("symlink target changed: %v", info)
				}
			} else if before.Mode().IsRegular() {
				if contents, err := os.ReadFile(path); err != nil || string(contents) != "leave this intact" {
					t.Errorf("file changed: content %q, error %v", contents, err)
				}
			}
		})
	}
}

// TestEnsurePrivateDirTightensOwnedPublicDirectory checks that an owned 0755 directory from an older launcher becomes 0700 and keeps its contents.
func TestEnsurePrivateDirTightensOwnedPublicDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(path, "keep")
	if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDir(path); err != nil {
		t.Fatalf("EnsurePrivateDir: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("mode = %v, want 0700", info.Mode().Perm())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("contents lost: %v", err)
	}
}

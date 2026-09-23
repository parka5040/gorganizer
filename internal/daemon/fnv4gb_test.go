package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/parka/gorganizer/internal/atomicfile"
)

// TestResolvePatcherExe rejects paths that do not resolve to regular files in the install path.
func TestResolvePatcherExe(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(*testing.T, string) string
		wantSuccess bool
	}{
		{
			name: "regular file inside install path",
			setup: func(t *testing.T, installPath string) string {
				path := filepath.Join(installPath, "patcher")
				if err := atomicfile.WriteFile(path, []byte("patcher"), 0755); err != nil {
					t.Fatal(err)
				}
				return path
			},
			wantSuccess: true,
		},
		{
			name: "empty candidate",
			setup: func(_ *testing.T, _ string) string {
				return ""
			},
		},
		{
			name: "relative candidate",
			setup: func(_ *testing.T, _ string) string {
				return "patcher"
			},
		},
		{
			name: "outside install path",
			setup: func(t *testing.T, _ string) string {
				path := filepath.Join(t.TempDir(), "patcher")
				if err := atomicfile.WriteFile(path, []byte("patcher"), 0755); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "symlink to outside install path",
			setup: func(t *testing.T, installPath string) string {
				target := filepath.Join(t.TempDir(), "patcher")
				if err := atomicfile.WriteFile(target, []byte("patcher"), 0755); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(installPath, "patcher")
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "directory",
			setup: func(t *testing.T, installPath string) string {
				path := filepath.Join(installPath, "patcher")
				if err := os.Mkdir(path, 0755); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			installPath := filepath.Join(t.TempDir(), "install")
			if err := os.Mkdir(installPath, 0755); err != nil {
				t.Fatal(err)
			}
			candidate := test.setup(t, installPath)
			got, err := resolvePatcherExe(installPath, candidate)
			if test.wantSuccess {
				if err != nil {
					t.Fatal(err)
				}
				if got != candidate {
					t.Errorf("resolvePatcherExe() = %q, want %q", got, candidate)
				}
				return
			}
			var unsafePathErr *UnsafePathError
			if !errors.As(err, &unsafePathErr) {
				t.Fatalf("resolvePatcherExe() error = %v, want UnsafePathError", err)
			}
			if unsafePathErr.Field != "patcher_exe_path" {
				t.Errorf("UnsafePathError.Field = %q, want %q", unsafePathErr.Field, "patcher_exe_path")
			}
		})
	}
}

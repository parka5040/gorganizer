package steam

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parka/gorganizer/internal/fsutil"
)

// TestFindRoot locks candidate paths, priority order, and the not-found error.
func TestFindRoot(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, home string) string
		wantErr string
	}{
		{
			name: "xdg data home primary",
			setup: func(t *testing.T, home string) string {
				xdg := filepath.Join(home, "custom-data")
				t.Setenv("XDG_DATA_HOME", xdg)
				root := filepath.Join(xdg, "Steam")
				mustMkdirAll(t, filepath.Join(root, "steamapps"))
				return root
			},
		},
		{
			name: "default local share primary",
			setup: func(t *testing.T, home string) string {
				root := filepath.Join(home, ".local", "share", "Steam")
				mustMkdirAll(t, filepath.Join(root, "steamapps"))
				return root
			},
		},
		{
			name: "dot steam symlink resolved",
			setup: func(t *testing.T, home string) string {
				real := filepath.Join(home, "actual-steam")
				mustMkdirAll(t, filepath.Join(real, "steamapps"))
				mustMkdirAll(t, filepath.Join(home, ".steam"))
				if err := os.Symlink(real, filepath.Join(home, ".steam", "steam")); err != nil {
					t.Fatal(err)
				}
				resolved, err := filepath.EvalSymlinks(real)
				if err != nil {
					t.Fatal(err)
				}
				return resolved
			},
		},
		{
			name: "dot steam plain directory",
			setup: func(t *testing.T, home string) string {
				root := filepath.Join(home, ".steam", "steam")
				mustMkdirAll(t, filepath.Join(root, "steamapps"))
				resolved, err := filepath.EvalSymlinks(root)
				if err != nil {
					t.Fatal(err)
				}
				return resolved
			},
		},
		{
			name: "flatpak fallback",
			setup: func(t *testing.T, home string) string {
				root := filepath.Join(home, ".var", "app", "com.valvesoftware.Steam", ".local", "share", "Steam")
				mustMkdirAll(t, filepath.Join(root, "steamapps"))
				return root
			},
		},
		{
			name: "primary wins over symlink and flatpak",
			setup: func(t *testing.T, home string) string {
				primary := filepath.Join(home, ".local", "share", "Steam")
				mustMkdirAll(t, filepath.Join(primary, "steamapps"))
				mustMkdirAll(t, filepath.Join(home, ".steam", "steam", "steamapps"))
				mustMkdirAll(t, filepath.Join(home, ".var", "app", "com.valvesoftware.Steam", ".local", "share", "Steam", "steamapps"))
				return primary
			},
		},
		{
			name: "symlink wins over flatpak",
			setup: func(t *testing.T, home string) string {
				root := filepath.Join(home, ".steam", "steam")
				mustMkdirAll(t, filepath.Join(root, "steamapps"))
				mustMkdirAll(t, filepath.Join(home, ".var", "app", "com.valvesoftware.Steam", ".local", "share", "Steam", "steamapps"))
				resolved, err := filepath.EvalSymlinks(root)
				if err != nil {
					t.Fatal(err)
				}
				return resolved
			},
		},
		{
			name: "steamapps as regular file is not a valid root",
			setup: func(t *testing.T, home string) string {
				root := filepath.Join(home, ".local", "share", "Steam")
				mustMkdirAll(t, root)
				if err := os.WriteFile(filepath.Join(root, "steamapps"), []byte("x"), 0644); err != nil {
					t.Fatal(err)
				}
				return ""
			},
			wantErr: "steam root not found",
		},
		{
			name: "steam dir without steamapps is not a valid root",
			setup: func(t *testing.T, home string) string {
				mustMkdirAll(t, filepath.Join(home, ".local", "share", "Steam"))
				return ""
			},
			wantErr: "steam root not found",
		},
		{
			name: "nothing installed",
			setup: func(t *testing.T, home string) string {
				return ""
			},
			wantErr: "steam root not found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_DATA_HOME", "")
			want := tt.setup(t, home)
			got, err := FindRoot()
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("FindRoot() = %q, want error %q", got, tt.wantErr)
				}
				if err.Error() != tt.wantErr {
					t.Fatalf("FindRoot() error = %q, want %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("FindRoot() error = %v", err)
			}
			if got != want {
				t.Fatalf("FindRoot() = %q, want %q", got, want)
			}
		})
	}
}

// TestDirExists locks the directory-probe helper's stat semantics.
func TestDirExists(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "file")
	if err := os.WriteFile(file, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "existing directory", path: tmp, want: true},
		{name: "regular file", path: file, want: false},
		{name: "missing path", path: filepath.Join(tmp, "nope"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fsutil.DirExists(tt.path); got != tt.want {
				t.Fatalf("DirExists(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
}

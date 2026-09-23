package daemon

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/parka/gorganizer/internal/config"
)

// TestPathResolversValidateUntrustedPaths verifies daemon path validation boundaries.
func TestPathResolversValidateUntrustedPaths(t *testing.T) {
	t.Setenv("GORGANIZER_ROOT", t.TempDir())

	t.Run("modDir", func(t *testing.T) {
		for _, name := range []string{"..", "../escape", "a/b", "", "bad\x00name"} {
			t.Run(name, func(t *testing.T) {
				_, err := resolveModDir("skyrimse", name)
				var unsafePath *UnsafePathError
				if !errors.As(err, &unsafePath) {
					t.Fatalf("error = %v, want UnsafePathError", err)
				}
				if unsafePath.Field != "mod_name" {
					t.Errorf("field = %q, want %q", unsafePath.Field, "mod_name")
				}
			})
		}

		path, err := resolveModDir("skyrimse", "SkyUI")
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(config.ModsDir("skyrimse"), "SkyUI")
		if path != want {
			t.Errorf("path = %q, want %q", path, want)
		}
	})

	t.Run("archivePath", func(t *testing.T) {
		downloadsDir := config.DownloadsDir("skyrimse")
		for _, tc := range []struct {
			name    string
			relPath string
			wantErr bool
		}{
			{"rejects escape", "../../etc/passwd", true},
			{"accepts nested archive", "Skyrim Special Edition/mod.7z", false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				path, err := archivePath(downloadsDir, tc.relPath)
				if tc.wantErr {
					var unsafePath *UnsafePathError
					if !errors.As(err, &unsafePath) {
						t.Fatalf("error = %v, want UnsafePathError", err)
					}
					if unsafePath.Field != "archive_rel_path" {
						t.Errorf("field = %q, want %q", unsafePath.Field, "archive_rel_path")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				want := filepath.Join(downloadsDir, "Skyrim Special Edition", "mod.7z")
				if path != want {
					t.Errorf("path = %q, want %q", path, want)
				}
			})
		}
	})

	t.Run("validateProfileName", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			wantErr bool
		}{
			{"..", true},
			{"Default", false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				err := validateProfileName(tc.name)
				if tc.wantErr {
					var unsafePath *UnsafePathError
					if !errors.As(err, &unsafePath) {
						t.Fatalf("error = %v, want UnsafePathError", err)
					}
					if unsafePath.Field != "profile_name" {
						t.Errorf("field = %q, want %q", unsafePath.Field, "profile_name")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
			})
		}
	})
}

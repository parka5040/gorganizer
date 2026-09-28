package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parka/gorganizer/internal/config"
)

// TestFNVPrefixPathUsesSecondSteamLibrary checks TTW uses the prefix beside FNV instead of the first Steam root.
func TestFNVPrefixPathUsesSecondSteamLibrary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	d := newIsolatedDaemon(t, nil)
	root := filepath.Join(home, ".local", "share", "Steam")
	library := filepath.Join(home, "second-library")
	fnv := config.GameConfig{
		InstallPath:      filepath.Join(library, "steamapps", "common", "Fallout New Vegas"),
		SteamAppID:       22380,
		SteamLibraryPath: library,
	}
	for _, path := range []string{
		filepath.Join(root, "steamapps", "compatdata", "22380", "pfx"),
		filepath.Join(library, "steamapps", "compatdata", "22380", "pfx"),
		fnv.InstallPath,
	} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	vdf := `"libraryfolders" { "0" { "path" "` + library + `" } }`
	if err := os.WriteFile(filepath.Join(root, "steamapps", "libraryfolders.vdf"), []byte(vdf), 0644); err != nil {
		t.Fatal(err)
	}
	prefix, ok := d.fnvPrefixPath(fnv)
	want := filepath.Join(library, "steamapps", "compatdata", "22380", "pfx")
	if !ok || prefix != want {
		t.Fatalf("fnvPrefixPath = (%q, %v), want (%q, true)", prefix, ok, want)
	}
}

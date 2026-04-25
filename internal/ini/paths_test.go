package ini

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercises the real on-disk prefix for the user's FalloutNV install.
// Skipped when the prefix isn't present (CI, other machines).
func TestDocumentsPath_ResolvesExistingPrefix(t *testing.T) {
	root, err := SteamRoot()
	if err != nil {
		t.Skip("Steam not installed:", err)
	}
	prefix := filepath.Join(root, "steamapps", "compatdata", "22380", "pfx",
		"drive_c", "users", "steamuser")
	if _, err := os.Stat(prefix); err != nil {
		t.Skip("FalloutNV prefix not present")
	}

	got, err := DocumentsPath(22380, "FalloutNV")
	if err != nil {
		t.Fatalf("DocumentsPath: %v", err)
	}

	// The resolved path must land under the compatdata prefix, not ~/Documents.
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("resolved path escaped prefix: %s", got)
	}

	// And must actually resolve to an existing directory (following symlinks)
	// if the game has ever run, or to the parent we can create.
	if info, err := os.Stat(filepath.Dir(got)); err != nil || !info.IsDir() {
		t.Fatalf("parent of %s is not a directory: err=%v", got, err)
	}
}

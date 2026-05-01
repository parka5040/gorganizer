package ini

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("resolved path escaped prefix: %s", got)
	}

	if info, err := os.Stat(filepath.Dir(got)); err != nil || !info.IsDir() {
		t.Fatalf("parent of %s is not a directory: err=%v", got, err)
	}
}

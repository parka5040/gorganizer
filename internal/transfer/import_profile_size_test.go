package transfer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
)

// TestOversizedImportedProfileJSONRejected verifies a large profile is rejected before publication.
func TestOversizedImportedProfileJSONRejected(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "oversized.tar.zst")
	setRoot(t, t.TempDir())
	writeFileT(t, filepath.Join(config.ProfilesDir(testGame), "Default", "profile.json"),
		`{"name":"Default","extra":"`+strings.Repeat("x", 1<<20)+`"}`)
	if _, err := Export(context.Background(), ExportOptions{GameID: testGame, OutputPath: archive}, nil); err != nil {
		t.Fatal(err)
	}
	setRoot(t, t.TempDir())
	original := `{"name":"Default","game_id":"skyrimse","extra":"original"}`
	path := filepath.Join(config.ProfilesDir(testGame), "Default", "profile.json")
	writeFileT(t, path, original)
	_, err := Import(context.Background(), ImportOptions{GameID: testGame, ArchivePath: archive, Policy: dto.PolicyOverwrite}, nil)
	requireBundleRejected(t, err, BundleRejectedLimit, "Default/profile.json")
	if got, err := os.ReadFile(path); err != nil || string(got) != original {
		t.Fatalf("existing profile changed: %q, %v", got, err)
	}
	assertNoStagingLeftovers(t)
}

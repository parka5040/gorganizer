package transfer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
)

// TestImportCollisionPolicyCoversOverwriteAndGameSettings checks every policy with and without existing Overwrite files and settings.
func TestImportCollisionPolicyCoversOverwriteAndGameSettings(t *testing.T) {
	for _, policy := range []dto.CollisionPolicy{dto.PolicyAbort, dto.PolicyRename, dto.PolicySkip, dto.PolicyOverwrite} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("policy-%d-existing-%t", policy, existing), func(t *testing.T) {
				archive := exportTestArchive(t)
				setRoot(t, t.TempDir())
				overwrite := filepath.Join(config.ModsDir(testGame), "Overwrite", "textures", "generated.dds")
				settings := config.GameSettingsPath(testGame)
				if existing {
					writeFileT(t, overwrite, "old overwrite")
					writeFileT(t, settings, "old settings")
				}
				summary, err := Import(context.Background(), ImportOptions{GameID: testGame, ArchivePath: archive, Policy: policy, ModFolders: []string{"Gamma"}, ProfileNames: []string{"Default"}}, nil)
				if existing && policy == dto.PolicyAbort {
					var collision *TransferCollisionError
					if !errors.As(err, &collision) || !strings.HasPrefix(collision.Name, "Overwrite/") {
						t.Fatalf("abort collision = %v", err)
					}
					if _, err := os.Stat(filepath.Join(config.ModsDir(testGame), "Gamma")); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("abort published Gamma before detecting Overwrite collision: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("import = %v", err)
				}
				for _, item := range []struct{ path, old, incoming, skip string }{
					{overwrite, "old overwrite", "overwrite-payload", "Overwrite/textures/generated.dds"},
					{settings, "old settings", "# Gorganizer per-game settings — auto-generated\nauto_install: true\n", "game settings"},
				} {
					want := item.incoming
					if existing && policy != dto.PolicyOverwrite {
						want = item.old
					}
					if body, err := os.ReadFile(item.path); err != nil || string(body) != want {
						t.Errorf("%s = %q, %v, want %q", item.path, body, err, want)
					}
					if skipped := slices.Contains(summary.Skipped, item.skip); skipped != (existing && (policy == dto.PolicySkip || policy == dto.PolicyRename)) {
						t.Errorf("skipped %s = %v, summary = %v", item.skip, skipped, summary.Skipped)
					}
				}
			})
		}
	}
}

// TestAbortSettingsCollisionDoesNotPublishMods checks a settings-only conflict before any mod is imported.
func TestAbortSettingsCollisionDoesNotPublishMods(t *testing.T) {
	archive := exportTestArchive(t)
	setRoot(t, t.TempDir())
	writeFileT(t, config.GameSettingsPath(testGame), "old settings")
	_, err := Import(context.Background(), ImportOptions{GameID: testGame, ArchivePath: archive, Policy: dto.PolicyAbort, ModFolders: []string{"Gamma"}}, nil)
	var collision *TransferCollisionError
	if !errors.As(err, &collision) || collision.Name != "game settings" {
		t.Fatalf("settings collision = %v", err)
	}
	if _, err := os.Stat(filepath.Join(config.ModsDir(testGame), "Gamma")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("settings abort published Gamma: %v", err)
	}
}

// TestOverwriteSkipSummaryCapsPaths checks a large skip list is bounded and reports the omitted count.
func TestOverwriteSkipSummaryCapsPaths(t *testing.T) {
	root := t.TempDir()
	staged, target := filepath.Join(root, "stage"), filepath.Join(root, "Overwrite")
	for i := 0; i < 52; i++ {
		name := fmt.Sprintf("file-%02d", i)
		writeFileT(t, filepath.Join(staged, name), "incoming")
		writeFileT(t, filepath.Join(target, name), "original")
	}
	summary := dto.TransferSummary{}
	count, err := mergeOverwriteWithPolicy(staged, target, dto.PolicySkip, &summary)
	if err != nil || count != 0 || len(summary.Skipped) != 51 || summary.Skipped[50] != "…and 2 more files in Overwrite" {
		t.Fatalf("merged %d, skipped %v, err %v", count, summary.Skipped, err)
	}
}

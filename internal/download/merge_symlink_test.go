package download

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestMergeDoesNotFollowSymlinksInTarget refuses existing linked parents and files without changing their targets.
func TestMergeDoesNotFollowSymlinksInTarget(t *testing.T) {
	for _, rel := range []string{"textures/a.dds", "a.dds"} {
		t.Run(rel, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", t.TempDir())
			t.Setenv("GORGANIZER_ROOT", filepath.Join(root, "instance"))
			for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR", "TMPDIR"} {
				t.Setenv(name, t.TempDir())
			}
			modsDir := useModsDir(t)
			targetDir := filepath.Join(modsDir, "Target")
			if err := os.MkdirAll(targetDir, 0755); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			victim := filepath.Join(outside, "a.dds")
			if err := os.WriteFile(victim, []byte("untouched"), 0644); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(targetDir, "textures")
			linkTarget := outside
			if rel == "a.dds" {
				link = filepath.Join(targetDir, "a.dds")
				linkTarget = victim
			}
			if err := os.Symlink(linkTarget, link); err != nil {
				t.Fatal(err)
			}
			extract := t.TempDir()
			writeTree(t, extract, rel)
			_, err := Install(InstallRequest{
				GameID: "skyrimse", ExtractedRoot: extract, ContentRoot: extract,
				Mode: ModeMergeIntoMod, TargetMod: "Target",
			})
			var rejected *ArchiveRejectedError
			if !errors.As(err, &rejected) || rejected.Reason != ArchiveRejectedUnsafeEntry {
				t.Fatalf("Install error = %v, want ArchiveRejectedUnsafeEntry", err)
			}
			data, err := os.ReadFile(victim)
			if err != nil || string(data) != "untouched" {
				t.Errorf("outside victim = %q, %v", data, err)
			}
		})
	}
}

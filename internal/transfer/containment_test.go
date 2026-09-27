package transfer

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
)

// TestImportOverwriteDoesNotFollowExistingSymlinks keeps external files unchanged when Overwrite has a linked ancestor or destination.
func TestImportOverwriteDoesNotFollowExistingSymlinks(t *testing.T) {
	for _, rel := range []string{"link/victim", "victim"} {
		t.Run(rel, func(t *testing.T) {
			isolatedImportRoot(t)
			outside := t.TempDir()
			victim := filepath.Join(outside, "victim")
			writeFileT(t, victim, "untouched")
			owDir := filepath.Join(config.ModsDir(testGame), "Overwrite")
			if err := os.MkdirAll(owDir, 0755); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(owDir, "link")
			if rel == "victim" {
				link = filepath.Join(owDir, "victim")
			} else {
				victim = filepath.Join(outside, "victim")
			}
			target := outside
			if rel == "victim" {
				target = victim
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			m := craftedManifest()
			m.Mods = nil
			m.IncludesOverwrite = true
			archive := writeArchiveFile(t, buildTarBytes(t, m, []tarEntry{
				{header: &tar.Header{Name: "overwrite/" + rel, Typeflag: tar.TypeReg, Size: 4}, data: []byte("evil")},
			}))
			_, err := Import(context.Background(), ImportOptions{GameID: testGame, ArchivePath: archive, Policy: dto.PolicyOverwrite}, nil)
			item := rel
			if rel == "link/victim" {
				item = "link"
			}
			requireBundleRejected(t, err, BundleRejectedLink, item)
			if got := readFileT(t, victim); got != "untouched" {
				t.Errorf("outside victim = %q", got)
			}
			assertNoStagingLeftovers(t)
		})
	}
}

// TestMergeOverwriteRejectsTypeConflicts preserves files and directories when incoming types differ.
func TestMergeOverwriteRejectsTypeConflicts(t *testing.T) {
	for _, tc := range []struct {
		name string
		dir  bool
	}{
		{"existing file incoming directory", false},
		{"existing directory incoming file", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolatedImportRoot(t)
			owDir := filepath.Join(config.ModsDir(testGame), "Overwrite")
			staged := t.TempDir()
			if tc.dir {
				writeFileT(t, filepath.Join(owDir, "conflict", "victim"), "untouched")
				writeFileT(t, filepath.Join(staged, "conflict"), "new")
			} else {
				writeFileT(t, filepath.Join(owDir, "conflict"), "untouched")
				writeFileT(t, filepath.Join(staged, "conflict", "victim"), "new")
			}
			err := mergeOverwrite(staged, owDir)
			requireBundleRejected(t, err, BundleRejectedDuplicate, "conflict")
			path := filepath.Join(owDir, "conflict")
			if tc.dir {
				path = filepath.Join(path, "victim")
			}
			if got := readFileT(t, path); got != "untouched" {
				t.Errorf("existing content = %q", got)
			}
		})
	}
}

// TestImportRejectsFileBundleRoot keeps earlier selected mods intact when a later selected root is a file.
func TestImportRejectsFileBundleRoot(t *testing.T) {
	for _, rootType := range []string{"mod", "profile"} {
		t.Run(rootType, func(t *testing.T) {
			isolatedImportRoot(t)
			writeFileT(t, filepath.Join(config.ModsDir(testGame), "N", "existing.txt"), "untouched")
			writeFileT(t, filepath.Join(config.ModsDir(testGame), "M", "existing.txt"), "original M")
			m := craftedManifest()
			m.Mods = []ModEntry{{Folder: "N"}, {Folder: "M"}}
			entries := []tarEntry{{header: &tar.Header{Name: "mods/N/new.txt", Typeflag: tar.TypeReg, Size: 3}, data: []byte("new")}}
			item := "mods/M"
			if rootType == "profile" {
				m.Mods = m.Mods[:1]
				m.Profiles = []string{"P"}
				item = "profiles/P"
			}
			entries = append(entries, tarEntry{header: &tar.Header{Name: item, Typeflag: tar.TypeReg, Size: 4}, data: []byte("file")})
			archive := writeArchiveFile(t, buildTarBytes(t, m, entries))
			_, err := Import(context.Background(), ImportOptions{GameID: testGame, ArchivePath: archive, Policy: dto.PolicyOverwrite}, nil)
			requireBundleRejected(t, err, BundleRejectedManifest, item)
			if got := readFileT(t, filepath.Join(config.ModsDir(testGame), "N", "existing.txt")); got != "untouched" {
				t.Errorf("existing mod = %q", got)
			}
			if got := readFileT(t, filepath.Join(config.ModsDir(testGame), "M", "existing.txt")); got != "original M" {
				t.Errorf("file-root target = %q", got)
			}
			if _, err := os.Lstat(filepath.Join(config.ModsDir(testGame), "N", "new.txt")); !os.IsNotExist(err) {
				t.Errorf("earlier mod was changed: %v", err)
			}
			assertNoStagingLeftovers(t)
		})
	}
}

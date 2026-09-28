package transfer

import (
	"archive/tar"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/mod"
)

// requireBundleRejected checks the typed rejection reason and offending item.
func requireBundleRejected(t *testing.T, err error, reason, item string) {
	t.Helper()
	var rejected *BundleRejectedError
	if !errors.As(err, &rejected) || rejected.Reason != reason || rejected.Item != item {
		t.Fatalf("error = %v, want BundleRejectedError{%q, %q}", err, reason, item)
	}
}

// TestImportRejectsChainedLinks rejects a link chain before it can write outside staging.
func TestImportRejectsChainedLinks(t *testing.T) {
	root := t.TempDir()
	setRoot(t, root)
	marker := filepath.Join(root, "marker")
	writeFileT(t, marker, "unchanged")
	entries := []tarEntry{
		{header: &tar.Header{Name: "mods/M/a", Typeflag: tar.TypeSymlink, Linkname: "."}},
		{header: &tar.Header{Name: "mods/M/a/b", Typeflag: tar.TypeSymlink, Linkname: ".."}},
		{header: &tar.Header{Name: "mods/M/a/b/c", Typeflag: tar.TypeSymlink, Linkname: "../.."}},
		{header: &tar.Header{Name: "mods/M/a/b/c/x", Typeflag: tar.TypeReg, Mode: 0644, Size: 4}, data: []byte("evil")},
	}
	archive := writeArchiveFile(t, buildTarBytes(t, craftedManifest(), entries))
	_, err := Import(context.Background(), ImportOptions{GameID: testGame, ArchivePath: archive, Policy: dto.PolicyOverwrite}, nil)
	requireBundleRejected(t, err, BundleRejectedLink, entries[0].header.Name)
	if got := readFileT(t, marker); got != "unchanged" {
		t.Errorf("outside marker changed: %q", got)
	}
	for _, outside := range []string{filepath.Join(root, "x"), filepath.Join(filepath.Dir(config.ModsDir(testGame)), "x")} {
		if _, err := os.Lstat(outside); !os.IsNotExist(err) {
			t.Errorf("unexpected outside file %q: %v", outside, err)
		}
	}
	assertNoStagingLeftovers(t)
}

// TestImportRejectsProfileTraversalBeforeWrites validates every profile name before creating directories.
func TestImportRejectsProfileTraversalBeforeWrites(t *testing.T) {
	for _, name := range []string{"../..", ".", "a/b", ".hidden", "", "Overwrite", "Downloads", `a\b`} {
		t.Run(name, func(t *testing.T) {
			setRoot(t, t.TempDir())
			profilesDir := config.ProfilesDir(testGame)
			if err := os.MkdirAll(profilesDir, 0755); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(config.DataDir(), "marker")
			writeFileT(t, marker, "unchanged")
			m := craftedManifest()
			m.Profiles = []string{"Safe", name}
			archive := writeArchiveFile(t, buildTarBytes(t, m, nil))
			_, err := Preview(context.Background(), testGame, archive)
			requireBundleRejected(t, err, BundleRejectedProfileName, name)
			_, err = Import(context.Background(), ImportOptions{
				GameID: testGame, ArchivePath: archive, Policy: dto.PolicyOverwrite, ProfileNames: []string{"Safe"},
			}, nil)
			requireBundleRejected(t, err, BundleRejectedProfileName, name)
			if got := readFileT(t, marker); got != "unchanged" {
				t.Errorf("outside marker changed: %q", got)
			}
			if entries, err := os.ReadDir(profilesDir); err != nil || len(entries) != 0 {
				t.Errorf("profiles dir changed: %v, %v", entries, err)
			}
			if _, err := os.Stat(config.ModsDir(testGame)); !os.IsNotExist(err) {
				t.Errorf("import created the mods dir: %v", err)
			}
		})
	}
}

// TestImportRejectsDuplicateIdentity rejects duplicate files, path-type conflicts, and manifest identities.
func TestImportRejectsDuplicateIdentity(t *testing.T) {
	file := tarEntry{header: &tar.Header{Name: "mods/M/a.esp", Typeflag: tar.TypeReg, Mode: 0644, Size: 4}, data: []byte("good")}
	cases := []struct {
		name    string
		modify  func(*Manifest)
		entries []tarEntry
		item    string
	}{
		{"file", nil, []tarEntry{file, file}, "mods/M/a.esp"},
		{"repeated_directory", nil, []tarEntry{{header: &tar.Header{Name: "mods/M/", Typeflag: tar.TypeDir}}, {header: &tar.Header{Name: "mods/M/", Typeflag: tar.TypeDir}}}, "mods/M/"},
		{"file_then_directory", nil, []tarEntry{file, {header: &tar.Header{Name: "mods/M/a.esp/", Typeflag: tar.TypeDir}}}, "mods/M/a.esp/"},
		{"directory_then_file", nil, []tarEntry{{header: &tar.Header{Name: "mods/M/a.esp/", Typeflag: tar.TypeDir}}, file}, "mods/M/a.esp"},
		{"parent_file", nil, []tarEntry{{header: &tar.Header{Name: "mods/M/a", Typeflag: tar.TypeReg, Size: 4}, data: []byte("good")}, {header: &tar.Header{Name: "mods/M/a/b", Typeflag: tar.TypeReg, Size: 4}, data: []byte("evil")}}, "mods/M/a/b"},
		{"mod_casefold", func(m *Manifest) { m.Mods = append(m.Mods, ModEntry{Folder: "m"}) }, nil, "m"},
		{"mod_exact", func(m *Manifest) { m.Mods = append(m.Mods, ModEntry{Folder: "M"}) }, nil, "M"},
		{"profile", func(m *Manifest) { m.Profiles = []string{"Safe", "Safe"} }, nil, "Safe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setRoot(t, t.TempDir())
			m := craftedManifest()
			if tc.modify != nil {
				tc.modify(m)
			}
			archive := writeArchiveFile(t, buildTarBytes(t, m, tc.entries))
			if tc.modify != nil {
				_, err := Preview(context.Background(), testGame, archive)
				requireBundleRejected(t, err, BundleRejectedDuplicate, tc.item)
			}
			_, err := Import(context.Background(), ImportOptions{GameID: testGame, ArchivePath: archive, Policy: dto.PolicyOverwrite}, nil)
			requireBundleRejected(t, err, BundleRejectedDuplicate, tc.item)
			assertNoStagingLeftovers(t)
		})
	}
}

// TestImportRejectsPhysicalStageCollision rejects a file beneath a staged path occupied by another section.
func TestImportRejectsPhysicalStageCollision(t *testing.T) {
	setRoot(t, t.TempDir())
	m := craftedManifest()
	m.Mods = append(m.Mods, ModEntry{Folder: "__overwrite__", Name: "__overwrite__"})
	m.IncludesOverwrite = true
	archive := writeArchiveFile(t, buildTarBytes(t, m, []tarEntry{
		{header: &tar.Header{Name: "mods/__overwrite__/file", Typeflag: tar.TypeReg, Size: 4}, data: []byte("good")},
		{header: &tar.Header{Name: "overwrite/file/sub", Typeflag: tar.TypeReg, Size: 4}, data: []byte("evil")},
	}))
	_, err := Import(context.Background(), ImportOptions{GameID: testGame, ArchivePath: archive, Policy: dto.PolicyAbort}, nil)
	requireBundleRejected(t, err, BundleRejectedDuplicate, "overwrite/file/sub")
	assertNoStagingLeftovers(t)
}

// TestImportAllowsImplicitDirectoryEntries accepts explicit directories created by earlier files.
func TestImportAllowsImplicitDirectoryEntries(t *testing.T) {
	setRoot(t, t.TempDir())
	archive := writeArchiveFile(t, buildTarBytes(t, craftedManifest(), []tarEntry{
		{header: &tar.Header{Name: "mods/M/a/file", Typeflag: tar.TypeReg, Size: 4}, data: []byte("good")},
		{header: &tar.Header{Name: "mods/M/", Typeflag: tar.TypeDir}},
		{header: &tar.Header{Name: "mods/M/a/", Typeflag: tar.TypeDir}},
	}))
	_, err := Import(context.Background(), ImportOptions{GameID: testGame, ArchivePath: archive, Policy: dto.PolicyAbort}, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if got := readFileT(t, filepath.Join(config.ModsDir(testGame), "M", "a", "file")); got != "good" {
		t.Errorf("imported file = %q", got)
	}
}

// TestImportRejectsUnselectedUnsafeEntry rejects links even inside an unselected mod.
func TestImportRejectsUnselectedUnsafeEntry(t *testing.T) {
	setRoot(t, t.TempDir())
	m := craftedManifest()
	m.Mods = append(m.Mods, ModEntry{Folder: "N", Name: "N"})
	archive := writeArchiveFile(t, buildTarBytes(t, m, []tarEntry{
		{header: &tar.Header{Name: "mods/M/link", Typeflag: tar.TypeSymlink, Linkname: "."}},
		{header: &tar.Header{Name: "mods/N/file", Typeflag: tar.TypeReg, Size: 4}, data: []byte("good")},
	}))
	_, err := Import(context.Background(), ImportOptions{
		GameID: testGame, ArchivePath: archive, Policy: dto.PolicyAbort, ModFolders: []string{"N"},
	}, nil)
	requireBundleRejected(t, err, BundleRejectedLink, "mods/M/link")
	assertNoStagingLeftovers(t)
}

// TestImportRejectsHardlinkAndDevice rejects link and special tar types before extraction.
func TestImportRejectsHardlinkAndDevice(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		typ          byte
	}{
		{"symlink", BundleRejectedLink, tar.TypeSymlink},
		{"hardlink", BundleRejectedLink, tar.TypeLink},
		{"character_device", BundleRejectedSpecial, tar.TypeChar},
		{"block_device", BundleRejectedSpecial, tar.TypeBlock},
		{"fifo", BundleRejectedSpecial, tar.TypeFifo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setRoot(t, t.TempDir())
			entry := tarEntry{header: &tar.Header{Name: "mods/M/entry", Typeflag: tc.typ, Linkname: "mods/M/target"}}
			archive := writeArchiveFile(t, buildTarBytes(t, craftedManifest(), []tarEntry{entry}))
			_, err := Import(context.Background(), ImportOptions{GameID: testGame, ArchivePath: archive, Policy: dto.PolicyAbort}, nil)
			requireBundleRejected(t, err, tc.reason, entry.header.Name)
			assertNoStagingLeftovers(t)
		})
	}
}

// TestImportRejectsExcludedSections rejects entries that contradict their manifest flags.
func TestImportRejectsExcludedSections(t *testing.T) {
	for _, tc := range []struct {
		name  string
		entry tarEntry
	}{
		{"overwrite", tarEntry{header: &tar.Header{Name: "overwrite/old", Typeflag: tar.TypeReg, Size: 4}, data: []byte("evil")}},
		{"gamesettings", tarEntry{header: &tar.Header{Name: "gamesettings/.gorganizer-game.yaml", Typeflag: tar.TypeReg, Size: 4}, data: []byte("evil")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setRoot(t, t.TempDir())
			archive := writeArchiveFile(t, buildTarBytes(t, craftedManifest(), []tarEntry{tc.entry}))
			_, err := Import(context.Background(), ImportOptions{GameID: testGame, ArchivePath: archive, Policy: dto.PolicyAbort}, nil)
			requireBundleRejected(t, err, BundleRejectedManifest, tc.entry.header.Name)
			assertNoStagingLeftovers(t)
		})
	}
}

// TestImportRejectsUnknownCollisionPolicy refuses invalid default and override policies before writes.
func TestImportRejectsUnknownCollisionPolicy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		policy    dto.CollisionPolicy
		overrides map[string]dto.CollisionPolicy
		item      string
	}{
		{"default", dto.CollisionPolicy(99), nil, "99"},
		{"override", dto.PolicyAbort, map[string]dto.CollisionPolicy{"M": -1}, "-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setRoot(t, t.TempDir())
			archive := writeArchiveFile(t, buildTarBytes(t, craftedManifest(), nil))
			_, err := Import(context.Background(), ImportOptions{
				GameID: testGame, ArchivePath: archive, Policy: tc.policy, ModPolicyOverrides: tc.overrides,
			}, nil)
			requireBundleRejected(t, err, BundleRejectedManifest, tc.item)
			if _, err := os.Stat(config.ModsDir(testGame)); !os.IsNotExist(err) {
				t.Errorf("import created the mods dir: %v", err)
			}
		})
	}
}

// TestImportCreatesPrivateStaging checks the reserved staging prefix and private directory permissions.
func TestImportCreatesPrivateStaging(t *testing.T) {
	setRoot(t, t.TempDir())
	archive := writeArchiveFile(t, buildTarBytes(t, craftedManifest(), []tarEntry{
		{header: &tar.Header{Name: "mods/M/file", Typeflag: tar.TypeReg, Size: 4}, data: []byte("good")},
	}))
	checked := false
	_, err := Import(context.Background(), ImportOptions{GameID: testGame, ArchivePath: archive, Policy: dto.PolicyAbort}, func(progress dto.TransferProgress) {
		if progress.Step != "extract" || checked {
			return
		}
		checked = true
		for _, dir := range []string{config.ModsDir(testGame), config.ProfilesDir(testGame)} {
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Error(err)
				continue
			}
			found := false
			for _, entry := range entries {
				if !strings.HasPrefix(entry.Name(), mod.ImportStagePrefix) {
					continue
				}
				found = true
				info, err := entry.Info()
				if err != nil {
					t.Error(err)
				} else if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
					t.Errorf("insecure staging dir %q: %v", entry.Name(), info.Mode())
				}
			}
			if !found {
				t.Errorf("no staging dir in %q", dir)
			}
		}
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !checked {
		t.Fatal("no extraction progress emitted")
	}
	assertNoStagingLeftovers(t)
}

// TestFinalizeProfileRejectsUnsafeName preserves the target tree on direct finalization.
func TestFinalizeProfileRejectsUnsafeName(t *testing.T) {
	setRoot(t, t.TempDir())
	marker := filepath.Join(config.DataDir(), "marker")
	writeFileT(t, marker, "unchanged")
	staged := filepath.Join(t.TempDir(), "staged")
	if err := os.Mkdir(staged, 0700); err != nil {
		t.Fatal(err)
	}
	summary := dto.TransferSummary{Renamed: map[string]string{}}
	err := finalizeProfile(ImportOptions{GameID: testGame, Policy: dto.PolicyOverwrite}, "../..", staged, &summary)
	requireBundleRejected(t, err, BundleRejectedProfileName, "../..")
	if got := readFileT(t, marker); got != "unchanged" {
		t.Errorf("outside marker changed: %q", got)
	}
}

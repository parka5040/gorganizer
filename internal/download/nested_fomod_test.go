package download

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestNestedFomodCannotDeleteParent rejects unsafe stems while preserving their parent directories.
func TestNestedFomodCannotDeleteParent(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	emptyZip, err := os.ReadFile(createZip(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{name: "junk", data: []byte("not an archive")},
		{name: "valid empty zip", data: emptyZip},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			extractDir := filepath.Join(parent, "x")
			if err := os.Mkdir(extractDir, 0755); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(parent, "sentinel")
			if err := os.WriteFile(marker, []byte("untouched"), 0644); err != nil {
				t.Fatal(err)
			}
			nested := filepath.Join(extractDir, "...fomod")
			if err := os.WriteFile(nested, tc.data, 0644); err != nil {
				t.Fatal(err)
			}

			err := ExpandNestedFomods(extractDir, NewExtractBudget())
			var rejected *ArchiveRejectedError
			if !errors.As(err, &rejected) || rejected.Reason != ArchiveRejectedNestedInstaller {
				t.Fatalf("ExpandNestedFomods error = %v, want nested_installer", err)
			}
			for _, path := range []string{parent, extractDir, nested} {
				if _, err := os.Lstat(path); err != nil {
					t.Errorf("%s was removed: %v", path, err)
				}
			}
			data, err := os.ReadFile(marker)
			if err != nil || string(data) != "untouched" {
				t.Errorf("sentinel = %q, %v, want untouched", data, err)
			}
		})
	}
}

// TestNestedFomodCollisionPreservesExisting rejects a nested installer whose target already exists.
func TestNestedFomodCollisionPreservesExisting(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	archive, err := os.ReadFile(createZip(t, []zipTestEntry{{name: "inner.txt", data: []byte("inner")}}))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "foo.fomod"), archive, 0644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(root, "foo")
	if err := os.Mkdir(outDir, 0755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(outDir, "marker")
	if err := os.WriteFile(marker, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}

	err = ExpandNestedFomods(root, NewExtractBudget())
	var rejected *ArchiveRejectedError
	if !errors.As(err, &rejected) || rejected.Reason != ArchiveRejectedNestedInstaller {
		t.Fatalf("ExpandNestedFomods error = %v, want nested_installer", err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "original" {
		t.Fatalf("marker = %q, %v, want original", data, err)
	}
}

// TestNestedFomodStillExpands extracts a nested installer and removes its source archive.
func TestNestedFomodStillExpands(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	archive, err := os.ReadFile(createZip(t, []zipTestEntry{{name: "fomod/ModuleConfig.xml", data: []byte("<config/>")}}))
	if err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "Installer.fomod")
	if err := os.WriteFile(nested, archive, 0644); err != nil {
		t.Fatal(err)
	}
	if err := ExpandNestedFomods(root, NewExtractBudget()); err != nil {
		t.Fatalf("ExpandNestedFomods: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "Installer", "fomod", "ModuleConfig.xml"))
	if err != nil || string(data) != "<config/>" {
		t.Fatalf("expanded config = %q, %v", data, err)
	}
	if _, err := os.Lstat(nested); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source archive after expansion: %v, want not exist", err)
	}
}

// TestNestedFomodSharedBudget rejects nested archives that exceed the outer extraction's remaining budget.
func TestNestedFomodSharedBudget(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	first, err := os.ReadFile(createZip(t, []zipTestEntry{{name: "first.txt", data: []byte("one")}}))
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(createZip(t, []zipTestEntry{{name: "second.txt", data: []byte("two")}}))
	if err != nil {
		t.Fatal(err)
	}
	outer := createZip(t, []zipTestEntry{{name: "First.fomod", data: first}, {name: "Second.fomod", data: second}})
	for _, tc := range []struct {
		name   string
		limits extractLimits
	}{
		{name: "entry count", limits: extractLimits{MaxEntries: 3, MaxEntryBytes: 1024, MaxTotalBytes: int64(len(first) + len(second) + 20)}},
		{name: "total bytes", limits: extractLimits{MaxEntries: 4, MaxEntryBytes: 1024, MaxTotalBytes: int64(len(first) + len(second) + 5)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "extract")
			budget := newExtractBudget(tc.limits)
			if err := (&ZipExtractor{}).ExtractWithBudget(outer, root, budget); err != nil {
				t.Fatalf("outer extraction: %v", err)
			}
			err := ExpandNestedFomods(root, budget)
			var rejected *ArchiveRejectedError
			if !errors.As(err, &rejected) || rejected.Reason != ArchiveRejectedLimit {
				t.Fatalf("ExpandNestedFomods error = %v, want limit", err)
			}
			if _, err := os.Stat(filepath.Join(root, "First", "first.txt")); err != nil {
				t.Fatalf("first nested archive did not expand: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(root, "Second.fomod")); err != nil {
				t.Fatalf("second nested archive removed after refusal: %v", err)
			}
		})
	}
}

// TestExtractorFailurePreservesCallerDirectory keeps a caller-owned directory on archive open failures.
func TestExtractorFailurePreservesCallerDirectory(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	for _, tc := range []struct {
		name      string
		extractor Extractor
	}{
		{name: "zip", extractor: &ZipExtractor{}},
		{name: "7z", extractor: &SevenZipExtractor{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			archive := filepath.Join(root, "broken."+tc.name)
			if err := os.WriteFile(archive, []byte("corrupt archive"), 0644); err != nil {
				t.Fatal(err)
			}
			destDir := filepath.Join(root, "dest")
			if err := os.Mkdir(destDir, 0755); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(destDir, "marker")
			if err := os.WriteFile(marker, []byte("keep"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := tc.extractor.Extract(archive, destDir); err == nil {
				t.Fatal("Extract() error = nil, want open failure")
			}
			data, err := os.ReadFile(marker)
			if err != nil || string(data) != "keep" {
				t.Fatalf("marker after extraction = %q, %v, want keep", data, err)
			}
		})
	}
}

// TestNestedFomodIgnoresNonArchivesAndSymlinks leaves unrecognized files and links in place.
func TestNestedFomodIgnoresNonArchivesAndSymlinks(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	junk := filepath.Join(root, "Notes.fomod")
	if err := os.WriteFile(junk, []byte("not an archive"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "Linked.fomod")
	if err := os.Symlink(junk, link); err != nil {
		t.Fatal(err)
	}
	if err := ExpandNestedFomods(root, NewExtractBudget()); err != nil {
		t.Fatalf("ExpandNestedFomods: %v", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("symlink after expansion: %v", err)
	}
	data, err := os.ReadFile(junk)
	if err != nil || string(data) != "not an archive" {
		t.Fatalf("unrecognized file = %q, %v", data, err)
	}
}

// TestInstallRejectsParentNestedFomod reports the rejection and removes only its own extraction directory.
func TestInstallRejectsParentNestedFomod(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("HOME", t.TempDir())
	useModsDir(t)
	marker := filepath.Join(tmp, "sentinel")
	if err := os.WriteFile(marker, []byte("safe"), 0644); err != nil {
		t.Fatal(err)
	}
	archive := createZip(t, []zipTestEntry{{name: "...fomod", data: []byte("PK broken zip")}})
	_, err := Install(InstallRequest{GameID: "skyrimse", ArchivePath: archive, TargetMod: "Example"})
	var rejected *ArchiveRejectedError
	if !errors.As(err, &rejected) || rejected.Reason != ArchiveRejectedNestedInstaller {
		t.Fatalf("Install error = %v, want nested_installer", err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "safe" {
		t.Fatalf("sentinel after install = %q, %v, want safe", data, err)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 1 || entries[0].Name() != "sentinel" {
		t.Fatalf("temporary directory entries = %v, %v, want only sentinel", entries, err)
	}
}

// TestNestedFomodCorruptArchiveRejectsAndCleansStaging keeps the source and removes only its private staging.
func TestNestedFomodCorruptArchiveRejectsAndCleansStaging(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	root := t.TempDir()
	nested := filepath.Join(root, "Broken.fomod")
	if err := os.WriteFile(nested, []byte("PK corrupt archive"), 0644); err != nil {
		t.Fatal(err)
	}
	err := ExpandNestedFomods(root, NewExtractBudget())
	var rejected *ArchiveRejectedError
	if !errors.As(err, &rejected) || rejected.Reason != ArchiveRejectedNestedInstaller {
		t.Fatalf("ExpandNestedFomods error = %v, want nested_installer", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "Broken.fomod" {
		t.Fatalf("entries after failure = %v, %v, want only Broken.fomod", entries, err)
	}
}

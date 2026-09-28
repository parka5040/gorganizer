package download

import (
	"archive/zip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type zipTestEntry struct {
	name string
	data []byte
	mode os.FileMode
}

// TestZipExtractorExtract extracts a normal zip archive.
func TestZipExtractorExtract(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	archivePath := createZip(t, []zipTestEntry{{name: "meshes/example.nif", data: []byte("mesh")}})
	destDir := filepath.Join(t.TempDir(), "extract")

	err := (&ZipExtractor{}).Extract(archivePath, destDir)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(destDir, "meshes", "example.nif"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(contents) != "mesh" {
		t.Fatalf("extracted contents = %q, want %q", contents, "mesh")
	}
}

// TestExtractEntriesRejectsUnsafeArchives returns typed rejections without deleting the caller's directory.
func TestExtractEntriesRejectsUnsafeArchives(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	tests := []struct {
		name    string
		entries []zipTestEntry
		limits  extractLimits
		reason  string
	}{
		{
			name:    "traversal",
			entries: []zipTestEntry{{name: "../escape.txt", data: []byte("escape")}},
			limits:  defaultExtractLimits(),
			reason:  ArchiveRejectedUnsafeEntry,
		},
		{
			name: "entry count",
			entries: []zipTestEntry{
				{name: "one.txt", data: []byte("one")},
				{name: "two.txt", data: []byte("two")},
			},
			limits: extractLimits{MaxEntries: 1, MaxEntryBytes: 10, MaxTotalBytes: 10},
			reason: ArchiveRejectedLimit,
		},
		{
			name:    "entry bytes",
			entries: []zipTestEntry{{name: "large.txt", data: []byte("12345")}},
			limits:  extractLimits{MaxEntries: 1, MaxEntryBytes: 4, MaxTotalBytes: 10},
			reason:  ArchiveRejectedLimit,
		},
		{
			name: "total bytes",
			entries: []zipTestEntry{
				{name: "one.txt", data: []byte("123")},
				{name: "two.txt", data: []byte("456")},
			},
			limits: extractLimits{MaxEntries: 2, MaxEntryBytes: 10, MaxTotalBytes: 5},
			reason: ArchiveRejectedLimit,
		},
		{
			name:    "symlink",
			entries: []zipTestEntry{{name: "link", data: []byte("target"), mode: os.ModeSymlink | 0777}},
			limits:  defaultExtractLimits(),
			reason:  ArchiveRejectedUnsafeEntry,
		},
		{
			name:    "duplicate",
			entries: []zipTestEntry{{name: "same.txt", data: []byte("one")}, {name: "same.txt", data: []byte("two")}},
			limits:  defaultExtractLimits(),
			reason:  ArchiveRejectedUnsafeEntry,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archivePath := createZip(t, test.entries)
			destDir := filepath.Join(t.TempDir(), "extract")
			if err := os.Mkdir(destDir, 0755); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(destDir, "marker")
			if err := os.WriteFile(marker, []byte("keep"), 0644); err != nil {
				t.Fatal(err)
			}

			err := extractZipWithLimits(archivePath, destDir, test.limits)
			var rejected *ArchiveRejectedError
			if !errors.As(err, &rejected) || rejected.Reason != test.reason || !errors.Is(err, ErrUnsafeArchive) {
				t.Fatalf("error = %v, want ArchiveRejectedError reason %q", err, test.reason)
			}
			data, err := os.ReadFile(marker)
			if err != nil || string(data) != "keep" {
				t.Fatalf("marker after extraction = %q, %v, want keep", data, err)
			}
		})
	}
}

// createZip writes zip entries for an extraction test.
func createZip(t *testing.T, entries []zipTestEntry) string {
	t.Helper()
	archivePath := filepath.Join(t.TempDir(), "archive.zip")
	archive, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	writer := zip.NewWriter(archive)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("CreateHeader() error = %v", err)
		}
		if _, err := file.Write(entry.data); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Writer.Close() error = %v", err)
	}
	if err := archive.Close(); err != nil {
		t.Fatalf("File.Close() error = %v", err)
	}
	return archivePath
}

// extractZipWithLimits runs the shared loop with a zip archive and test limits.
func extractZipWithLimits(archivePath, destDir string, limits extractLimits) error {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer archive.Close()
	return extractEntries(archive.File, destDir, newExtractBudget(limits), func(file *zip.File) string {
		return file.Name
	}, func(file *zip.File) bool {
		return file.FileInfo().IsDir()
	}, func(file *zip.File) os.FileMode {
		return file.Mode()
	}, func(file *zip.File) (io.ReadCloser, error) {
		return file.Open()
	})
}

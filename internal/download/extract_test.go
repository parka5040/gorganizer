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

// TestExtractEntriesRejectsUnsafeArchives rejects entries that violate extraction limits.
func TestExtractEntriesRejectsUnsafeArchives(t *testing.T) {
	tests := []struct {
		name    string
		entries []zipTestEntry
		limits  extractLimits
	}{
		{
			name:    "traversal",
			entries: []zipTestEntry{{name: "../escape.txt", data: []byte("escape")}},
			limits:  defaultExtractLimits(),
		},
		{
			name: "entry count",
			entries: []zipTestEntry{
				{name: "one.txt", data: []byte("one")},
				{name: "two.txt", data: []byte("two")},
			},
			limits: extractLimits{MaxEntries: 1, MaxEntryBytes: 10, MaxTotalBytes: 10},
		},
		{
			name:    "entry bytes",
			entries: []zipTestEntry{{name: "large.txt", data: []byte("12345")}},
			limits:  extractLimits{MaxEntries: 1, MaxEntryBytes: 4, MaxTotalBytes: 10},
		},
		{
			name: "total bytes",
			entries: []zipTestEntry{
				{name: "one.txt", data: []byte("123")},
				{name: "two.txt", data: []byte("456")},
			},
			limits: extractLimits{MaxEntries: 2, MaxEntryBytes: 10, MaxTotalBytes: 5},
		},
		{
			name:    "symlink",
			entries: []zipTestEntry{{name: "link", data: []byte("target"), mode: os.ModeSymlink | 0777}},
			limits:  defaultExtractLimits(),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archivePath := createZip(t, test.entries)
			destDir := filepath.Join(t.TempDir(), "extract")

			err := extractZipWithLimits(archivePath, destDir, test.limits)
			if !errors.Is(err, ErrUnsafeArchive) {
				t.Fatalf("error = %v, want ErrUnsafeArchive", err)
			}
			assertEmptyDirectory(t, destDir)
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
	return extractEntries(archive.File, destDir, limits, func(file *zip.File) string {
		return file.Name
	}, func(file *zip.File) bool {
		return file.FileInfo().IsDir()
	}, func(file *zip.File) os.FileMode {
		return file.Mode()
	}, func(file *zip.File) (io.ReadCloser, error) {
		return file.Open()
	})
}

// assertEmptyDirectory verifies that extraction cleanup left no output behind.
func assertEmptyDirectory(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("directory contains %d entries, want none", len(entries))
	}
}

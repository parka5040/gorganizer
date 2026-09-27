package download

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

// TestRARExtractsPlainArchive checks stored files, directories, and RAR magic detection.
func TestRARExtractsPlainArchive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dest := filepath.Join(t.TempDir(), "extract")
	extractor, err := DetectExtractor(filepath.Join("testdata", "rar", "plain.rar"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := extractor.(*RarExtractor); !ok {
		t.Fatalf("DetectExtractor() = %T, want *RarExtractor", extractor)
	}
	if err := extractor.Extract(filepath.Join("testdata", "rar", "plain.rar"), dest); err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	for path, want := range map[string]string{
		"meshes/example.nif": "mesh",
		"readme.txt":         "hello",
	} {
		data, err := os.ReadFile(filepath.Join(dest, path))
		if err != nil || string(data) != want {
			t.Errorf("ReadFile(%q) = %q, %v; want %q", path, data, err, want)
		}
	}
	info, err := os.Stat(filepath.Join(dest, "meshes"))
	if err != nil || !info.IsDir() {
		t.Fatalf("meshes directory = %v, %v", info, err)
	}
}

// TestRARDetectsLegacyMagic checks that RAR 1.5-4 magic still selects the RAR extractor.
func TestRARDetectsLegacyMagic(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "legacy.rar")
	if err := os.WriteFile(path, []byte("Rar!\x1a\x07\x00"), 0644); err != nil {
		t.Fatal(err)
	}
	extractor, err := DetectExtractor(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := extractor.(*RarExtractor); !ok {
		t.Fatalf("DetectExtractor() = %T, want *RarExtractor", extractor)
	}
}

// TestRARRejectsLinksBeforeWriting refuses RAR links without creating their targets.
func TestRARRejectsLinksBeforeWriting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, archive := range []string{"symlink.rar", "hardlink.rar"} {
		t.Run(archive, func(t *testing.T) {
			root := t.TempDir()
			dest := filepath.Join(root, "extract")
			if err := os.Mkdir(dest, 0755); err != nil {
				t.Fatal(err)
			}
			assertRARRejection(t, filepath.Join("testdata", "rar", archive), dest, NewExtractBudget(), ArchiveRejectedUnsafeEntry)
			if _, err := os.Lstat(filepath.Join(dest, "link")); !os.IsNotExist(err) {
				t.Fatalf("link exists or cannot be checked: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(root, "outside.txt")); !os.IsNotExist(err) {
				t.Fatalf("outside file exists or cannot be checked: %v", err)
			}
			if _, err := os.Stat(dest); err != nil {
				t.Fatalf("caller destination removed: %v", err)
			}
		})
	}
}

// TestRARRejectsTraversal refuses a parent-relative entry before it is created.
func TestRARRejectsTraversal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	dest := filepath.Join(root, "extract")
	assertRARRejection(t, filepath.Join("testdata", "rar", "traversal.rar"), dest, NewExtractBudget(), ArchiveRejectedUnsafeEntry)
	if _, err := os.Stat(filepath.Join(root, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("traversal target exists or cannot be checked: %v", err)
	}
}

// TestRARLiveLimits verifies count, per-file, total, and reused budgets bound written data.
func TestRARLiveLimits(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct {
		name    string
		archive string
		limits  extractLimits
		file    string
		maxSize int64
	}{
		{name: "entry count includes directories", archive: "plain.rar", limits: extractLimits{MaxEntries: 1, MaxEntryBytes: 100, MaxTotalBytes: 100}},
		{name: "per-entry bytes", archive: "big.rar", limits: extractLimits{MaxEntries: 5, MaxEntryBytes: 4, MaxTotalBytes: 100}, file: "big.txt", maxSize: 4},
		{name: "total bytes", archive: "plain.rar", limits: extractLimits{MaxEntries: 5, MaxEntryBytes: 100, MaxTotalBytes: 6}, file: "readme.txt", maxSize: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "extract")
			if err := os.Mkdir(dest, 0755); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(dest, "keep")
			if err := os.WriteFile(marker, []byte("keep"), 0644); err != nil {
				t.Fatal(err)
			}
			assertRARRejection(t, filepath.Join("testdata", "rar", tc.archive), dest, newExtractBudget(tc.limits), ArchiveRejectedLimit)
			if tc.file != "" {
				info, err := os.Stat(filepath.Join(dest, tc.file))
				if err != nil || info.Size() > tc.maxSize {
					t.Fatalf("output file = %v, %v; want at most %d bytes", info, err, tc.maxSize)
				}
			}
			data, err := os.ReadFile(marker)
			if err != nil || string(data) != "keep" {
				t.Fatalf("caller marker = %q, %v; want keep", data, err)
			}
		})
	}

	budget := newExtractBudget(extractLimits{MaxEntries: 4, MaxEntryBytes: 100, MaxTotalBytes: 100})
	archive := filepath.Join("testdata", "rar", "plain.rar")
	if err := (&RarExtractor{}).ExtractWithBudget(archive, filepath.Join(t.TempDir(), "first"), budget); err != nil {
		t.Fatal(err)
	}
	assertRARRejection(t, archive, filepath.Join(t.TempDir(), "second"), budget, ArchiveRejectedLimit)
}

// TestRARNeverExecutesTools confirms RAR extraction never invokes archive utilities.
func TestRARNeverExecutesTools(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tools := t.TempDir()
	marker := filepath.Join(t.TempDir(), "executed")
	for _, name := range []string{"unrar", "7z"} {
		path := filepath.Join(tools, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf touched > '"+marker+"'\nexit 1\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", tools)
	if err := (&RarExtractor{}).Extract(filepath.Join("testdata", "rar", "plain.rar"), filepath.Join(t.TempDir(), "extract")); err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("external tool was executed or marker cannot be checked: %v", err)
	}
}

// TestRARRejectsUnsupportedFeatures checks encrypted archives and multi-volume continuations.
func TestRARRejectsUnsupportedFeatures(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	multi := filepath.Join(t.TempDir(), "multi.rar")
	if err := os.WriteFile(multi, rar5StoredArchive(nil, true), 0644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "encrypted header", path: filepath.Join("testdata", "rar", "encrypted.rar")},
		{name: "encrypted file", path: filepath.Join("testdata", "rar", "encrypted_file.rar")},
		{name: "multi-volume", path: multi},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "extract")
			assertRARRejection(t, tc.path, dest, NewExtractBudget(), ArchiveRejectedUnsupported)
			if _, err := os.Stat(filepath.Join(dest, "secret.txt")); !os.IsNotExist(err) {
				t.Fatalf("encrypted entry exists or cannot be checked: %v", err)
			}
		})
	}
}

// TestRARRejectsDuplicateFiles confirms exclusive creation refuses repeated entry names.
func TestRARRejectsDuplicateFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	archive := filepath.Join(t.TempDir(), "duplicate.rar")
	entries := []rar5StoredEntry{{name: "same.txt", data: "first"}, {name: "same.txt", data: "second"}}
	if err := os.WriteFile(archive, rar5StoredArchive(entries, false), 0644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "extract")
	assertRARRejection(t, archive, dest, NewExtractBudget(), ArchiveRejectedUnsafeEntry)
	data, err := os.ReadFile(filepath.Join(dest, "same.txt"))
	if err != nil || string(data) != "first" {
		t.Fatalf("first entry = %q, %v; want first", data, err)
	}
}

// assertRARRejection checks the typed reason returned by budgeted RAR extraction.
func assertRARRejection(t *testing.T, archive, dest string, budget *ExtractBudget, reason string) {
	t.Helper()
	err := (&RarExtractor{}).ExtractWithBudget(archive, dest, budget)
	var rejected *ArchiveRejectedError
	if !errors.As(err, &rejected) || rejected.Reason != reason || !errors.Is(err, ErrUnsafeArchive) {
		t.Fatalf("ExtractWithBudget() error = %v, want ArchiveRejectedError reason %q", err, reason)
	}
}

type rar5StoredEntry struct {
	name string
	data string
}

// rar5StoredArchive builds a minimal RAR5 archive containing stored files.
func rar5StoredArchive(entries []rar5StoredEntry, multi bool) []byte {
	archive := []byte("Rar!\x1a\x07\x01\x00")
	flags := uint64(0)
	if multi {
		flags = 1
	}
	archive = append(archive, rar5Block(1, 0, rar5Varint(flags), nil)...)
	for _, entry := range entries {
		data := []byte(entry.data)
		header := append(rar5Varint(4), rar5Varint(uint64(len(data)))...)
		header = append(header, rar5Varint(0o100644)...)
		crc := make([]byte, 4)
		binary.LittleEndian.PutUint32(crc, crc32.ChecksumIEEE(data))
		header = append(header, crc...)
		header = append(header, rar5Varint(0)...)
		header = append(header, rar5Varint(1)...)
		header = append(header, rar5Varint(uint64(len(entry.name)))...)
		header = append(header, entry.name...)
		archive = append(archive, rar5Block(2, 2, header, data)...)
	}
	archive = append(archive, rar5Block(5, 0, rar5Varint(flags), nil)...)
	return archive
}

// rar5Block encodes one RAR5 block with its header checksum.
func rar5Block(kind, flags uint64, data, payload []byte) []byte {
	header := append(rar5Varint(kind), rar5Varint(flags)...)
	if flags&2 != 0 {
		header = append(header, rar5Varint(uint64(len(payload)))...)
	}
	header = append(header, data...)
	header = append(rar5Varint(uint64(len(header))), header...)
	block := make([]byte, 4)
	binary.LittleEndian.PutUint32(block, crc32.ChecksumIEEE(header))
	block = append(block, header...)
	return append(block, payload...)
}

// rar5Varint encodes one unsigned RAR5 header value.
func rar5Varint(value uint64) []byte {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], value)
	return bytes.Clone(buf[:n])
}

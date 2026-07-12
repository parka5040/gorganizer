package transfer

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
)

type tarEntry struct {
	header *tar.Header
	data   []byte
}

// buildTarBytes assembles a plain (uncompressed) tar with a manifest first entry followed by entries.
func buildTarBytes(t *testing.T, m *Manifest, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	manifestBytes, err := EncodeManifest(m)
	if err != nil {
		t.Fatalf("EncodeManifest: %v", err)
	}
	if err := writeTarBytes(tw, manifestEntryName, manifestBytes, time.Now()); err != nil {
		t.Fatalf("writing manifest entry: %v", err)
	}
	for _, e := range entries {
		if err := tw.WriteHeader(e.header); err != nil {
			t.Fatalf("WriteHeader %s: %v", e.header.Name, err)
		}
		if len(e.data) > 0 {
			if _, err := tw.Write(e.data); err != nil {
				t.Fatalf("Write %s: %v", e.header.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar: %v", err)
	}
	return buf.Bytes()
}

func writeArchiveFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "crafted.tar")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("writing archive: %v", err)
	}
	return path
}

func craftedManifest() *Manifest {
	return &Manifest{
		SchemaVersion:     SchemaVersion,
		GorganizerVersion: "test",
		GameID:            testGame,
		ExportedAt:        time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Mods:              []ModEntry{{Folder: "M", Name: "M", FileCount: 1, TotalBytes: 4}},
	}
}

// TestImportRejectsPathTraversal locks that hostile entry names, symlinks, and hardlinks all fail with TransferPathError.
func TestImportRejectsPathTraversal(t *testing.T) {
	cases := []struct {
		name  string
		entry tarEntry
	}{
		{
			"dotdot_relative",
			tarEntry{&tar.Header{Name: "../evil", Typeflag: tar.TypeReg, Mode: 0644, Size: 4}, []byte("evil")},
		},
		{
			"absolute_path",
			tarEntry{&tar.Header{Name: "/abs/evil", Typeflag: tar.TypeReg, Mode: 0644, Size: 4}, []byte("evil")},
		},
		{
			"dotdot_inside_known_prefix",
			tarEntry{&tar.Header{Name: "mods/M/../../evil", Typeflag: tar.TypeReg, Mode: 0644, Size: 4}, []byte("evil")},
		},
		{
			"symlink_escaping",
			tarEntry{&tar.Header{Name: "mods/M/link", Typeflag: tar.TypeSymlink, Linkname: "../../../etc/passwd"}, nil},
		},
		{
			"symlink_absolute_target",
			tarEntry{&tar.Header{Name: "mods/M/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, nil},
		},
		{
			"symlink_crossing_mods",
			tarEntry{&tar.Header{Name: "mods/M/link", Typeflag: tar.TypeSymlink, Linkname: "../Other/file"}, nil},
		},
		{
			"hardlink",
			tarEntry{&tar.Header{Name: "mods/M/hard", Typeflag: tar.TypeLink, Linkname: "mods/M/a"}, nil},
		},
		{
			"unknown_root_prefix",
			tarEntry{&tar.Header{Name: "loot/x", Typeflag: tar.TypeReg, Mode: 0644, Size: 4}, []byte("evil")},
		},
		{
			"mod_folder_not_in_manifest",
			tarEntry{&tar.Header{Name: "mods/Rogue/x.esp", Typeflag: tar.TypeReg, Mode: 0644, Size: 4}, []byte("evil")},
		},
		{
			"second_manifest",
			tarEntry{&tar.Header{Name: "manifest.json", Typeflag: tar.TypeReg, Mode: 0644, Size: 2, ModTime: time.Now()}, []byte("{}")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			setRoot(t, root)
			archive := writeArchiveFile(t, buildTarBytes(t, craftedManifest(), []tarEntry{tc.entry}))
			_, err := Import(context.Background(), ImportOptions{
				GameID: testGame, ArchivePath: archive, Policy: dto.PolicyAbort,
			}, nil)
			var pathErr *TransferPathError
			if !errors.As(err, &pathErr) {
				t.Fatalf("err = %v, want TransferPathError", err)
			}
			if _, statErr := os.Stat(filepath.Join(root, "evil")); !os.IsNotExist(statErr) {
				t.Errorf("traversal escaped the staging root")
			}
			assertNoStagingLeftovers(t)
			entries, _ := os.ReadDir(config.ModsDir(testGame))
			for _, e := range entries {
				if e.Name() != "Downloads" {
					t.Errorf("unexpected entry %q landed in ModsDir", e.Name())
				}
			}
		})
	}
}

// TestImportTruncatedArchive locks that a torn archive fails cleanly with no partial mods and no staging leftovers.
func TestImportTruncatedArchive(t *testing.T) {
	archive := exportTestArchive(t)
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatalf("reading archive: %v", err)
	}
	truncated := filepath.Join(t.TempDir(), "torn.tar.zst")
	if err := os.WriteFile(truncated, data[:len(data)*3/5], 0644); err != nil {
		t.Fatalf("writing truncated archive: %v", err)
	}

	setRoot(t, t.TempDir())
	_, err = Import(context.Background(), ImportOptions{
		GameID: testGame, ArchivePath: truncated, Policy: dto.PolicyAbort,
	}, nil)
	if err == nil {
		t.Fatalf("Import of truncated archive succeeded")
	}
	assertNoStagingLeftovers(t)
	entries, _ := os.ReadDir(config.ModsDir(testGame))
	for _, e := range entries {
		t.Errorf("partial entry %q landed in ModsDir", e.Name())
	}
	profEntries, _ := os.ReadDir(config.ProfilesDir(testGame))
	for _, e := range profEntries {
		t.Errorf("partial entry %q landed in ProfilesDir", e.Name())
	}
}

// TestImportCancelledContext locks that cancellation aborts the import and cleans staging.
func TestImportCancelledContext(t *testing.T) {
	archive := exportTestArchive(t)
	setRoot(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Import(ctx, ImportOptions{
		GameID: testGame, ArchivePath: archive, Policy: dto.PolicyAbort,
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	assertNoStagingLeftovers(t)
}

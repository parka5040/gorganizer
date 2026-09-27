package transfer

import (
	"archive/tar"
	"bytes"
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
)

// isolatedImportRoot confines import test state and temporary files to scratch directories.
func isolatedImportRoot(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	setRoot(t, root)
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR", "TMPDIR"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(name, path)
	}
}

// TestImportLiveBudgets rejects archives exceeding entry, file, payload, or stream limits without touching existing mods.
func TestImportLiveBudgets(t *testing.T) {
	cases := []struct {
		name    string
		entries []tarEntry
		adjust  func(*importLimits, int64)
		item    string
	}{
		{
			name: "entries_include_directories",
			entries: []tarEntry{
				{header: &tar.Header{Name: "mods/M/new.bin", Typeflag: tar.TypeReg, Size: 4}, data: []byte("good")},
				{header: &tar.Header{Name: "mods/M/empty/", Typeflag: tar.TypeDir}},
			},
			adjust: func(l *importLimits, _ int64) { l.entries = 2 },
			item:   "mods/M/empty/",
		},
		{
			name: "per_file",
			entries: []tarEntry{
				{header: &tar.Header{Name: "mods/M/new.bin", Typeflag: tar.TypeReg, Size: 17}, data: []byte("seventeen bytes!!")},
			},
			adjust: func(l *importLimits, _ int64) { l.fileBytes = 16 },
			item:   "mods/M/new.bin",
		},
		{
			name: "total_payload",
			entries: []tarEntry{
				{header: &tar.Header{Name: "mods/M/a.bin", Typeflag: tar.TypeReg, Size: 4}, data: []byte("good")},
				{header: &tar.Header{Name: "mods/M/b.bin", Typeflag: tar.TypeReg, Size: 4}, data: []byte("more")},
			},
			adjust: func(l *importLimits, manifestSize int64) { l.payloadBytes = manifestSize + 4 },
			item:   "mods/M/b.bin",
		},
		{
			name: "decompressed_stream",
			entries: []tarEntry{
				{header: &tar.Header{Name: "mods/M/new.bin", Typeflag: tar.TypeReg, Size: 4}, data: []byte("good")},
			},
			adjust: func(l *importLimits, manifestSize int64) {
				l.streamBytes = 512 + (manifestSize+511)/512*512 + 512 + 2
			},
			item: "mods/M/new.bin",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolatedImportRoot(t)
			writeFileT(t, filepath.Join(config.ModsDir(testGame), "M", "existing.txt"), "untouched")
			m := craftedManifest()
			manifest, err := EncodeManifest(m)
			if err != nil {
				t.Fatal(err)
			}
			archive := writeArchiveFile(t, buildTarBytes(t, m, tc.entries))
			limits := defaultImportLimits()
			tc.adjust(&limits, int64(len(manifest)))
			_, err = Import(context.Background(), ImportOptions{GameID: testGame, ArchivePath: archive, Policy: dto.PolicyOverwrite, limits: &limits}, nil)
			requireBundleRejected(t, err, BundleRejectedLimit, tc.item)
			if got := readFileT(t, filepath.Join(config.ModsDir(testGame), "M", "existing.txt")); got != "untouched" {
				t.Errorf("existing mod changed: %q", got)
			}
			assertNoStagingLeftovers(t)
		})
	}
}

// TestImportIgnoresManifestByteCounts bounds progress while measuring actual tar payloads.
func TestImportIgnoresManifestByteCounts(t *testing.T) {
	isolatedImportRoot(t)
	m := craftedManifest()
	m.Mods[0].TotalBytes = math.MaxInt64
	archive := writeArchiveFile(t, buildTarBytes(t, m, []tarEntry{
		{header: &tar.Header{Name: "mods/M/new.bin", Typeflag: tar.TypeReg, Size: 4}, data: []byte("good")},
	}))
	limits := defaultImportLimits()
	limits.payloadBytes = 1 << 20
	var progressTotal int64
	_, err := Import(context.Background(), ImportOptions{GameID: testGame, ArchivePath: archive, Policy: dto.PolicyAbort, limits: &limits}, func(progress dto.TransferProgress) {
		progressTotal = progress.BytesTotal
	})
	if err != nil {
		t.Fatalf("Import with informational byte count: %v", err)
	}
	if progressTotal != limits.payloadBytes {
		t.Errorf("progress total = %d, want bounded total %d", progressTotal, limits.payloadBytes)
	}
	if got := readFileT(t, filepath.Join(config.ModsDir(testGame), "M", "new.bin")); got != "good" {
		t.Errorf("imported file = %q", got)
	}
}

// TestSkippedPayloadConsumesBudget rejects unselected payloads against both total and decompressed-stream limits.
func TestSkippedPayloadConsumesBudget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		adjust func(*importLimits, int64)
	}{
		{"total", func(l *importLimits, size int64) { l.payloadBytes = size + 7 }},
		{"stream", func(l *importLimits, size int64) { l.streamBytes = 512 + (size+511)/512*512 + 512 + 7 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolatedImportRoot(t)
			writeFileT(t, filepath.Join(config.ModsDir(testGame), "M", "existing.txt"), "untouched")
			m := craftedManifest()
			m.Mods = append(m.Mods, ModEntry{Folder: "N", Name: "N"})
			manifest, err := EncodeManifest(m)
			if err != nil {
				t.Fatal(err)
			}
			archive := writeArchiveFile(t, buildTarBytes(t, m, []tarEntry{
				{header: &tar.Header{Name: "mods/N/skipped.bin", Typeflag: tar.TypeReg, Size: 16}, data: []byte("unselected bytes")},
				{header: &tar.Header{Name: "mods/M/new.bin", Typeflag: tar.TypeReg, Size: 4}, data: []byte("good")},
			}))
			limits := defaultImportLimits()
			tc.adjust(&limits, int64(len(manifest)))
			_, err = Import(context.Background(), ImportOptions{
				GameID: testGame, ArchivePath: archive, Policy: dto.PolicyOverwrite,
				ModFolders: []string{"M"}, limits: &limits,
			}, nil)
			requireBundleRejected(t, err, BundleRejectedLimit, "mods/N/skipped.bin")
			if got := readFileT(t, filepath.Join(config.ModsDir(testGame), "M", "existing.txt")); got != "untouched" {
				t.Errorf("existing mod changed: %q", got)
			}
			assertNoStagingLeftovers(t)
		})
	}
}

type cancelAfterChecks struct {
	context.Context
	cancel context.CancelFunc
	checks int
	at     int
}

// Err cancels the context after the configured number of import cancellation checks.
func (c *cancelAfterChecks) Err() error {
	c.checks++
	if c.checks == c.at {
		c.cancel()
	}
	return c.Context.Err()
}

// TestImportCancellationDuringFile stops a multi-megabyte extraction before the next tar entry.
func TestImportCancellationDuringFile(t *testing.T) {
	isolatedImportRoot(t)
	data := bytes.Repeat([]byte("x"), 6<<20)
	archive := writeArchiveFile(t, buildTarBytes(t, craftedManifest(), []tarEntry{
		{header: &tar.Header{Name: "mods/M/big.bin", Typeflag: tar.TypeReg, Size: int64(len(data))}, data: data},
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	controlled := &cancelAfterChecks{Context: ctx, cancel: cancel, at: 5}
	limits := defaultImportLimits()
	limits.fileBytes = int64(len(data))
	limits.payloadBytes = int64(len(data)) + 1<<20
	_, err := Import(controlled, ImportOptions{GameID: testGame, ArchivePath: archive, Policy: dto.PolicyAbort, limits: &limits}, nil)
	if err != context.Canceled {
		t.Fatalf("Import error = %v, want context.Canceled", err)
	}
	if controlled.checks < controlled.at {
		t.Errorf("cancellation checks = %d, want at least %d", controlled.checks, controlled.at)
	}
	if _, err := os.Stat(filepath.Join(config.ModsDir(testGame), "M")); !os.IsNotExist(err) {
		t.Errorf("partially imported mod remains: %v", err)
	}
	assertNoStagingLeftovers(t)
}

// TestManifestLimitCannotBeBypassed rejects an oversized first entry from both Preview and Import.
func TestManifestLimitCannotBeBypassed(t *testing.T) {
	isolatedImportRoot(t)
	writeFileT(t, filepath.Join(config.ModsDir(testGame), "M", "existing.txt"), "untouched")
	archive := filepath.Join(t.TempDir(), "oversized.tar")
	var header bytes.Buffer
	tw := tar.NewWriter(&header)
	size := int64(64<<20 + 1)
	if err := tw.WriteHeader(&tar.Header{Name: manifestEntryName, Typeflag: tar.TypeReg, Size: size, Mode: 0644}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(header.Bytes()); err != nil {
		t.Fatal(err)
	}
	manifest, err := EncodeManifest(craftedManifest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(manifest); err != nil {
		t.Fatal(err)
	}
	spaces := bytes.Repeat([]byte(" "), 64<<10)
	for remaining := size - int64(len(manifest)) - 1; remaining > 0; {
		n := int64(len(spaces))
		if n > remaining {
			n = remaining
		}
		if _, err := f.Write(spaces[:n]); err != nil {
			t.Fatal(err)
		}
		remaining -= n
	}
	if _, err := f.Write([]byte("!")); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(int64(header.Len()) + (size+511)/512*512 + 1024); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Preview(testGame, archive)
	requireBundleRejected(t, err, BundleRejectedLimit, manifestEntryName)
	_, err = Import(context.Background(), ImportOptions{GameID: testGame, ArchivePath: archive, Policy: dto.PolicyOverwrite}, nil)
	requireBundleRejected(t, err, BundleRejectedLimit, manifestEntryName)
	if got := readFileT(t, filepath.Join(config.ModsDir(testGame), "M", "existing.txt")); got != "untouched" {
		t.Errorf("existing mod changed: %q", got)
	}
	assertNoStagingLeftovers(t)
}

// TestImportLiveLimitsAllowBoundary checks that a payload equal to the configured cap is accepted.
func TestImportLiveLimitsAllowBoundary(t *testing.T) {
	isolatedImportRoot(t)
	m := craftedManifest()
	manifest, err := EncodeManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	archive := writeArchiveFile(t, buildTarBytes(t, m, []tarEntry{
		{header: &tar.Header{Name: "mods/M/new.bin", Typeflag: tar.TypeReg, Size: 4}, data: []byte("good")},
	}))
	limits := defaultImportLimits()
	limits.entries = 2
	limits.fileBytes = 4
	limits.payloadBytes = int64(len(manifest)) + 4
	limits.streamBytes = 512 + (int64(len(manifest))+511)/512*512 + 512 + 512 + 1024
	_, err = Import(context.Background(), ImportOptions{GameID: testGame, ArchivePath: archive, Policy: dto.PolicyAbort, limits: &limits}, nil)
	if err != nil {
		t.Fatalf("Import at budget boundary: %v", err)
	}
	if got := readFileT(t, filepath.Join(config.ModsDir(testGame), "M", "new.bin")); got != "good" {
		t.Errorf("imported file = %q", got)
	}
	assertNoStagingLeftovers(t)
}

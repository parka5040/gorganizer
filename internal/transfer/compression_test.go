package transfer

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// TestCompressionAutoDetect locks that zstd, gzip, and plain tar archives are all recognized by magic bytes.
func TestCompressionAutoDetect(t *testing.T) {
	setRoot(t, t.TempDir())
	raw := buildTarBytes(t, craftedManifest(), nil)

	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	if _, err := gw.Write(raw); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	var zstdBuf bytes.Buffer
	zw, err := zstd.NewWriter(&zstdBuf)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	if _, err := zw.Write(raw); err != nil {
		t.Fatalf("zstd write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zstd close: %v", err)
	}

	cases := []struct {
		name string
		data []byte
	}{
		{"plain_tar", raw},
		{"gzip", gzBuf.Bytes()},
		{"zstd", zstdBuf.Bytes()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "archive.bin")
			if err := os.WriteFile(path, tc.data, 0644); err != nil {
				t.Fatalf("write: %v", err)
			}
			preview, err := Preview(testGame, path)
			if err != nil {
				t.Fatalf("Preview(%s): %v", tc.name, err)
			}
			if preview.GameID != testGame {
				t.Errorf("game id = %q", preview.GameID)
			}
			if len(preview.Mods) != 1 || preview.Mods[0].Folder != "M" {
				t.Errorf("mods = %+v", preview.Mods)
			}
		})
	}
}

// TestExportProducesZstd locks that exported archives carry the zstd magic bytes.
func TestExportProducesZstd(t *testing.T) {
	archive := exportTestArchive(t)
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(data) < 4 || !bytes.Equal(data[:4], zstdMagic) {
		t.Errorf("archive does not start with zstd magic: % x", data[:4])
	}
}

package transfer

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/vfs"
)

// TestExportLeavesRetainedFarmArtifactsOutsideBundle checks that only selected mod files enter a transfer archive.
func TestExportLeavesRetainedFarmArtifactsOutsideBundle(t *testing.T) {
	isolatedImportRoot(t)
	modFile := filepath.Join(config.ModsDir(testGame), "M", "mod.txt")
	writeFileT(t, modFile, "mod bytes")
	data := filepath.Join(t.TempDir(), "Game", "Data")
	cfg := config.DefaultConfig()
	cfg.Games[testGame] = config.GameConfig{InstallPath: filepath.Dir(data), DataSubpath: "Data"}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	artifacts := map[string]string{
		filepath.Join(vfs.PreservedDir(data), "batch", "files", "saved.txt"): "preserved game output",
		vfs.MaintenancePath(data):               "verify marker",
		data + vfs.RetainedSessionSiblingSuffix: "launch ticket",
		data + ".gorganizer-restoring":          "restore record",
	}
	for path, content := range artifacts {
		writeFileT(t, path, content)
	}
	archive := filepath.Join(t.TempDir(), "bundle.tar.zst")
	if _, err := Export(context.Background(), ExportOptions{GameID: testGame, ModFolders: []string{"M"}, OutputPath: archive}, nil); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder, err := zstd.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer decoder.Close()
	reader := tar.NewReader(decoder)
	seenMod := false
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == "mods/M/mod.txt" {
			seenMod = true
		}
		if strings.Contains(header.Name, ".gorganizer-") || strings.Contains(header.Name, "saved.txt") {
			t.Errorf("retained farm artifact entered export: %s", header.Name)
		}
	}
	if !seenMod {
		t.Fatal("export omitted selected mod")
	}
	for path, content := range artifacts {
		if got := readFileT(t, path); got != content {
			t.Errorf("retained file %s changed to %q", path, got)
		}
	}
}

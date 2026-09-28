package daemon

import (
	"errors"
	"github.com/parka/gorganizer/internal/dto"
	"os"
	"path/filepath"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
)

// TestPreviewRejectsParentNestedFomod preserves the private extraction root when a nested name points at its parent.
func TestPreviewRejectsParentNestedFomod(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		"skyrimse": {InstallPath: t.TempDir(), DataSubpath: "Data"},
	})
	root, err := extractionRoot()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "sentinel")
	if err := os.WriteFile(marker, []byte("safe"), 0644); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(config.DownloadsDir("skyrimse"), "Nested.zip")
	writeZipFiles(t, archive, map[string]string{"...fomod": "PK corrupt archive"})

	_, err = d.PreviewInstall(dto.PreviewInstallRequest{GameID: "skyrimse", ArchiveRelPath: "Nested.zip"})
	var rejected *download.ArchiveRejectedError
	if !errors.As(err, &rejected) || rejected.Reason != download.ArchiveRejectedNestedInstaller {
		t.Fatalf("PreviewInstall error = %v, want nested_installer", err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "safe" {
		t.Fatalf("sentinel after preview = %q, %v, want safe", data, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "sentinel" {
		t.Fatalf("extraction root entries = %v, %v, want only sentinel", entries, err)
	}
}

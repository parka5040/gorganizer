package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
)

// TestInstallRecordFailureMapsToToken preserves a typed installation-record error through the daemon install path.
func TestInstallRecordFailureMapsToToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newStardewDaemon(t)
	modsDir := config.ModsDir("skyrimse")
	writeZipFiles(t, filepath.Join(config.DownloadsDir("skyrimse"), "Patch.zip"), map[string]string{
		"plugin.esp": "plugin", "metadata.yaml/payload.txt": "not a record",
	})

	_, _, err := d.StartInstall(context.Background(), dto.StartInstallRequest{
		GameID: "skyrimse", ArchiveRelPath: "Patch.zip", Mode: dto.InstallAsNewMod, TargetMod: "New Mod",
	})
	var recordErr *download.InstallRecordError
	if !errors.As(err, &recordErr) || recordErr.Mod != "New Mod" {
		t.Fatalf("StartInstall error = %v, want InstallRecordError for New Mod", err)
	}
	if entries, err := os.ReadDir(modsDir); err != nil || len(entries) != 1 || entries[0].Name() != "Downloads" {
		t.Fatalf("mods directory entries = %v, %v, want only Downloads", entries, err)
	}
	entries, err := d.GetModList("skyrimse", "Default")
	if err != nil || len(entries) != 0 {
		t.Fatalf("mod list after failed install = %+v, %v, want empty", entries, err)
	}
}

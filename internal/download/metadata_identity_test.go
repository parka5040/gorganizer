package download

import (
	"path/filepath"
	"testing"
)

func TestInstalledMetadataUsesFinalFolder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	modsDir := useModsDir(t)
	archive := filepath.Join(t.TempDir(), "innocent.zip")
	writeZip(t, archive, map[string][]byte{
		"metadata.yaml": []byte("folder: Downloads\nname: Innocent\n"),
		"plugin.esp":    []byte("plugin"),
	})
	_, err := Install(InstallRequest{
		GameID: "skyrimse", ArchivePath: archive, Mode: ModeNewMod, TargetMod: "Actual Mod",
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	meta, err := LoadModMetadata(filepath.Join(modsDir, "Actual Mod"))
	if err != nil {
		t.Fatalf("LoadModMetadata: %v", err)
	}
	if meta.Folder != "Actual Mod" || meta.Name != "Actual Mod" {
		t.Errorf("installed metadata folder = %q, name = %q, want Actual Mod for both", meta.Folder, meta.Name)
	}
}

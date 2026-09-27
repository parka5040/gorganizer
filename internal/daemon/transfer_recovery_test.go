package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/vfs"
)

// TestImportRefusesAppliedDisabledMod keeps an applied mod intact when the saved profile has disabled it but not applied the change.
func TestImportRefusesAppliedDisabledMod(t *testing.T) {
	d, dataPath, _ := newSteamMaintenanceDaemon(t)
	modFile := filepath.Join(config.ModsDir("skyrimse"), "A", "a.esp")
	writeFixture(t, modFile)
	writeFixture(t, filepath.Join(config.ModsDir("skyrimse"), "A", vfs.RootContentDirName, "Hook.dll"))
	if err := d.SetModList("skyrimse", "Default", []dto.ModListEntryResult{{ModName: "A", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateProfile("skyrimse", "Backup"); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "instance.tar.zst")
	if _, err := d.ExportInstance(context.Background(), dto.ExportRequest{
		GameID: "skyrimse", OutputPath: archive, ModFolders: []string{"A"}, ProfileNames: []string{"Backup"},
	}, nil); err != nil {
		t.Fatalf("export fixture: %v", err)
	}
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetModList("skyrimse", "Default", []dto.ModListEntryResult{{ModName: "A", Enabled: false}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(modFile)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.ImportInstance(context.Background(), dto.ImportRequest{
		GameID: "skyrimse", ArchivePath: archive, Policy: dto.PolicyOverwrite, ModFolders: []string{"A"}, ProfileNames: []string{"Backup"},
	}, nil)
	var mounted *TransferOverwriteMountedError
	if !errors.As(err, &mounted) || mounted.Name != "A" {
		t.Fatalf("import overwrite = %v, want transfer_overwrite_mounted for A", err)
	}
	if after, err := os.ReadFile(modFile); err != nil || string(after) != string(before) {
		t.Fatalf("applied mod changed: %q, %v", after, err)
	}
	if _, err := os.Stat(filepath.Join(dataPath, "a.esp")); err != nil {
		t.Fatalf("applied file missing: %v", err)
	}
	if target, err := os.Readlink(filepath.Join(filepath.Dir(dataPath), "Hook.dll")); err != nil || target != filepath.Join(config.ModsDir("skyrimse"), "A", vfs.RootContentDirName, "Hook.dll") {
		t.Fatalf("root deployment changed: %q, %v", target, err)
	}
}

// TestImportRecoveryPrecedesSweep verifies daemon construction restores moved-aside mods and profiles before orphan staging is swept.
func TestImportRecoveryPrecedesSweep(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	isolateDaemonState(t)
	cfg := config.DefaultConfig()
	cfg.Games["skyrimse"] = newSkyrimGames(t)["skyrimse"]
	id := strings.Repeat("a", 32)
	for _, tc := range []struct {
		root string
		kind string
		name string
	}{
		{config.ModsDir("skyrimse"), "mod", "Existing"},
		{config.ProfilesDir("skyrimse"), "profile", "Default"},
	} {
		old := filepath.Join(tc.root, ".transfer-old-"+id)
		writeFixture(t, filepath.Join(old, "original.txt"))
		stage := filepath.Join(tc.root, ".gorganizer-import-orphan")
		agedDir(t, stage, time.Hour)
		journal := filepath.Join(tc.root, ".gorganizer-transfer-intent-"+id+".json")
		data := `{"schema_version":1,"op_id":"` + id + `","kind":"` + tc.kind + `","name":"` + tc.name + `","staged":".transfer-stage-` + id + `","old":".transfer-old-` + id + `"}`
		if _, err := atomicfile.WriteFileDurable(journal, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	d, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Shutdown)
	d.RecoverAll()
	for _, tc := range []struct {
		root string
		name string
	}{
		{config.ModsDir("skyrimse"), "Existing"},
		{config.ProfilesDir("skyrimse"), "Default"},
	} {
		if _, err := os.Stat(filepath.Join(tc.root, tc.name, "original.txt")); err != nil {
			t.Errorf("original %s was not restored: %v", tc.name, err)
		}
		for _, name := range []string{".transfer-old-" + id, ".gorganizer-transfer-intent-" + id + ".json", ".gorganizer-import-orphan"} {
			if _, err := os.Lstat(filepath.Join(tc.root, name)); !os.IsNotExist(err) {
				t.Errorf("%s remains after recovery and sweep: %v", name, err)
			}
		}
	}
}

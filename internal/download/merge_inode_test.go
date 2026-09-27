package download

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMergePreparationPreservesHardlinkedOriginal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	modsDir := useModsDir(t)
	extract := t.TempDir()
	writeFomodTestFile(t, extract, "plugin.esp", "new")
	target := filepath.Join(modsDir, "Target")
	writeFomodTestFile(t, target, "plugin.esp", "old")
	other := filepath.Join(t.TempDir(), "deployed.esp")
	if err := os.Link(filepath.Join(target, "plugin.esp"), other); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(InstallRequest{GameID: "skyrimse", ExtractedRoot: extract, ContentRoot: extract, Mode: ModeMergeIntoMod, TargetMod: "Target"}); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{filepath.Join(target, "plugin.esp"): "new", other: "old"} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Errorf("%s = %q, %v; want %q", path, data, err, want)
		}
	}
}

func TestStagedMergeRecordFailureNamesOriginalMod(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	modsDir := useModsDir(t)
	extract := t.TempDir()
	writeFomodTestFile(t, extract, "plugin.esp", "new")
	if err := os.Mkdir(filepath.Join(modsDir, ".reinstall-token"), 0755); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("record write failed")
	failInstallRecordWrite(t, failure)
	_, err := Install(InstallRequest{
		GameID: "skyrimse", ExtractedRoot: extract, ContentRoot: extract,
		Mode: ModeMergeIntoMod, TargetMod: ".reinstall-token", RecordModName: "Real Mod",
	})
	var recordErr *InstallRecordError
	if !errors.As(err, &recordErr) || recordErr.Mod != "Real Mod" || !errors.Is(err, failure) {
		t.Errorf("Install error = %v, want a record failure naming Real Mod", err)
	}
}

func TestMergeReplacesFilesWithNewInodes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	modsDir := useModsDir(t)
	extract := t.TempDir()
	writeFomodTestFile(t, extract, "nested/plugin.esp", "new")
	target := filepath.Join(modsDir, "Target", "nested", "plugin.esp")
	writeFomodTestFile(t, filepath.Join(modsDir, "Target"), "nested/plugin.esp", "old")
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Install(InstallRequest{GameID: "skyrimse", ExtractedRoot: extract, ContentRoot: extract, Mode: ModeMergeIntoMod, TargetMod: "Target"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Fatal("merged file retained the original inode")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "new" {
		t.Errorf("merged file = %q, %v; want new", data, err)
	}
}

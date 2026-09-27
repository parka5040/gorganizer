package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
)

// replaceFixture installs an existing mod and creates a replacement archive in isolated directories.
func replaceFixture(t *testing.T) (*Daemon, string, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	d := newStardewDaemon(t)
	folder, _ := installForReinstall(t, d, "skyrimse", "Base", map[string]string{"a.txt": "old a", "b.txt": "old b"}, false)
	archive := filepath.Join(config.DownloadsDir("skyrimse"), "Update.zip")
	writeZipFiles(t, archive, map[string]string{"b.txt": "new b", "c.txt": "new c"})
	return d, folder, archive
}

// TestReplaceInstallSwapsFilesAndKeepsSettings checks replacement content, metadata, profile placement and archive status.
func TestReplaceInstallSwapsFilesAndKeepsSettings(t *testing.T) {
	d, folder, _ := replaceFixture(t)
	modsDir := config.ModsDir("skyrimse")
	modDir := filepath.Join(modsDir, folder)
	original, err := download.LoadModMetadata(modDir)
	if err != nil {
		t.Fatal(err)
	}
	original.Name = "My preferred name"
	original.Category = "My category"
	original.Version = "old version"
	original.ModPage = "https://example.invalid/original"
	original.TrueIndex = "5"
	original.VisualIndex = "3"
	original.Separator = "My section"
	original.Enabled = true
	if err := download.SaveModMetadata(modDir, original); err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{"Default", "Other"} {
		if err := d.SetModList("skyrimse", profile, []dto.ModListEntryResult{{ModName: folder, Enabled: true}}); err != nil {
			t.Fatal(err)
		}
	}
	original, err = download.LoadModMetadata(modDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := download.UpsertEntry("skyrimse", download.IndexEntry{Path: "Update.zip", Uninstalled: true}); err != nil {
		t.Fatal(err)
	}
	if installed := d.installedArchiveMap("skyrimse"); installed["Downloads/Base.zip"].Folder != folder {
		t.Fatalf("original archive not installed: %v", installed)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := d.StreamInstallEvents(ctx, "skyrimse")
	if err != nil {
		t.Fatal(err)
	}
	observedSwap := false
	d.reinstallFault = func(step string) error {
		if step != "install" {
			return nil
		}
		observedSwap = true
		for len(events) > 0 {
			if evt := <-events; evt.Progress != nil && evt.Progress.Step == dto.InstallStepComplete {
				t.Error("replace published StageComplete before the swap")
			}
		}
		index, err := download.LoadIndex("skyrimse")
		if err != nil || len(index.Archives) != 1 || !index.Archives[0].Uninstalled {
			t.Errorf("replacement download was marked installed before the swap: %+v, %v", index, err)
		}
		return nil
	}
	folderAfter, count, err := d.StartInstall(dto.StartInstallRequest{
		GameID: "skyrimse", ArchiveRelPath: "Update.zip", Mode: dto.InstallReplaceMod, TargetMod: folder,
	})
	d.reinstallFault = nil
	if err != nil || folderAfter != folder || count != 2 || !observedSwap {
		t.Fatalf("StartInstall = %q, %d, %v, swap observed %t; want %q, 2", folderAfter, count, err, observedSwap, folder)
	}
	completed := false
	for len(events) > 0 {
		if evt := <-events; evt.Progress != nil && evt.Progress.Step == dto.InstallStepComplete {
			completed = true
		}
	}
	if !completed {
		t.Error("replace did not publish StageComplete after the swap")
	}
	if _, err := os.Lstat(filepath.Join(modDir, "a.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old file remains: %v", err)
	}
	for path, want := range map[string]string{"b.txt": "new b", "c.txt": "new c"} {
		got, err := os.ReadFile(filepath.Join(modDir, path))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v, want %q", path, got, err, want)
		}
	}
	meta, err := download.LoadModMetadata(modDir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Name != original.Name || meta.Category != original.Category || meta.Version != original.Version || meta.ModPage != original.ModPage || meta.Installed != original.Installed || meta.TrueIndex != original.TrueIndex || meta.VisualIndex != original.VisualIndex || meta.Separator != original.Separator || !meta.Enabled {
		t.Errorf("settings changed: %+v, want %+v", meta, original)
	}
	if meta.Folder != folder || meta.FileCount != 2 || !reflect.DeepEqual(meta.Files, []string{"b.txt", "c.txt"}) || len(meta.SourceArchives) != 1 || meta.SourceArchives[0].Path != "Downloads/Update.zip" || meta.SourceArchives[0].Merged {
		t.Errorf("replacement record = %+v", meta)
	}
	for _, profile := range []string{"Default", "Other"} {
		entries, err := d.GetModList("skyrimse", profile)
		if err != nil || len(entries) != 1 || entries[0].ModName != folder || !entries[0].Enabled {
			t.Errorf("%s modlist = %+v, %v", profile, entries, err)
		}
	}
	index, err := download.LoadIndex("skyrimse")
	if err != nil || len(index.Archives) != 1 || index.Archives[0].Uninstalled {
		t.Errorf("replacement download index = %+v, %v", index, err)
	}
	installed := d.installedArchiveMap("skyrimse")
	if installed["Downloads/Update.zip"].Folder != folder || installed["Downloads/Base.zip"].Folder != "" {
		t.Errorf("installed archive cache = %v", installed)
	}
	assertNoReinstallState(t, modsDir)
}

// TestReplaceStageMetadataKeepsSettingsAndNewArchiveValues checks version and mod page precedence.
func TestReplaceStageMetadataKeepsSettingsAndNewArchiveValues(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct {
		name, version, page, wantVersion, wantPage string
	}{
		{"no archive details", "", "", "old version", "old page"},
		{"new version", "new version", "", "new version", "old page"},
		{"new page", "", "new page", "old version", "new page"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := &download.ModMetadata{Name: "user name", Category: "user category", Version: "old version", ModPage: "old page", Enabled: true, Files: []string{"old"}}
			staged := &download.ModMetadata{Name: "archive name", Category: "archive category", Version: tc.version, ModPage: tc.page, Files: []string{"new"}, FileCount: 1, SourceArchives: []download.SourceArchiveRef{{Path: "new.zip"}}}
			got := replaceStageMetadata(current, staged)
			if got.Name != current.Name || got.Category != current.Category || got.Version != tc.wantVersion || got.ModPage != tc.wantPage || !got.Enabled || !reflect.DeepEqual(got.Files, staged.Files) || !reflect.DeepEqual(got.SourceArchives, staged.SourceArchives) {
				t.Errorf("replacement metadata = %+v", got)
			}
		})
	}
}

// TestReplaceInstallDeployedRebuildsFarm checks that a mounted farm and root links reflect only the replacement.
func TestReplaceInstallDeployedRebuildsFarm(t *testing.T) {
	d, install, modDir := mountedModChangeFixture(t)
	archive := filepath.Join(config.DownloadsDir(modChangeGame), "Replacement.zip")
	writeZipFiles(t, archive, map[string]string{"b.esp": "new plugin", ".gorganizer-root/root.txt": "new root"})
	folder, count, err := d.StartInstall(dto.StartInstallRequest{
		GameID: modChangeGame, ArchiveRelPath: "Replacement.zip", Mode: dto.InstallReplaceMod, TargetMod: "A",
	})
	if err != nil || folder != "A" || count != 2 {
		t.Fatalf("StartInstall = %q, %d, %v", folder, count, err)
	}
	for _, path := range []string{filepath.Join(modDir, "a.esp"), filepath.Join(install, "Data", "a.esp")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("old file remains at %s: %v", path, err)
		}
	}
	for path, want := range map[string]string{
		filepath.Join(modDir, "b.esp"): "new plugin", filepath.Join(install, "Data", "b.esp"): "new plugin", filepath.Join(install, "root.txt"): "new root",
	} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v, want %q", path, got, err, want)
		}
	}
	status, err := d.GetVFSStatus(modChangeGame)
	if err != nil || !status.Mounted || status.Dirty {
		t.Errorf("farm after replace = %+v, %v", status, err)
	}
	assertNoReinstallState(t, config.ModsDir(modChangeGame))
}

// TestReplaceRefusedWhileGameRuns checks that a running game keeps its original mod and deployed files.
func TestReplaceRefusedWhileGameRuns(t *testing.T) {
	d, install, modDir := mountedModChangeFixture(t)
	archive := filepath.Join(config.DownloadsDir(modChangeGame), "Replacement.zip")
	writeZipFiles(t, archive, map[string]string{"a.esp": "replacement"})
	fakeProcesses(d, true, nil)
	_, _, err := d.StartInstall(dto.StartInstallRequest{
		GameID: modChangeGame, ArchiveRelPath: "Replacement.zip", Mode: dto.InstallReplaceMod, TargetMod: "A",
	})
	requireGameRunning(t, "StartInstall replace", err, dto.GameRunningOperationReinstall)
	for _, path := range []string{filepath.Join(modDir, "a.esp"), filepath.Join(install, "Data", "a.esp")} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "original bytes" {
			t.Errorf("%s = %q, %v, want original bytes", path, got, err)
		}
	}
	assertNoReinstallState(t, config.ModsDir(modChangeGame))
}

// TestReplaceMissingTargetRefused checks that replace requires a real existing mod folder.
func TestReplaceMissingTargetRefused(t *testing.T) {
	d, _, archive := replaceFixture(t)
	_, _, err := d.StartInstall(dto.StartInstallRequest{
		GameID: "skyrimse", ExternalArchivePath: archive, Mode: dto.InstallReplaceMod, TargetMod: "Missing",
	})
	var invalid *download.InvalidTargetModError
	if !errors.As(err, &invalid) || invalid.Name != "Missing" || invalid.Reason != "merge target is not an existing mod folder" {
		t.Fatalf("StartInstall error = %v, want missing-target refusal", err)
	}
	assertNoReinstallState(t, config.ModsDir("skyrimse"))
}

// TestReplaceFailureKeepsOriginal checks a failed stage record cannot change the original or index.
func TestReplaceFailureKeepsOriginal(t *testing.T) {
	d, folder, archive := replaceFixture(t)
	modsDir := config.ModsDir("skyrimse")
	modDir := filepath.Join(modsDir, folder)
	before := snapshotTree(t, modDir)
	if err := download.UpsertEntry("skyrimse", download.IndexEntry{Path: "Update.zip", Uninstalled: true}); err != nil {
		t.Fatal(err)
	}
	extracted := t.TempDir()
	writeFixture(t, filepath.Join(extracted, "new.esp"))
	writeFixture(t, filepath.Join(extracted, "metadata.yaml", "payload.txt"))
	_, _, err := d.startInstallFrom(dto.StartInstallRequest{
		GameID: "skyrimse", ExternalArchivePath: archive, Mode: dto.InstallReplaceMod, TargetMod: folder,
	}, extracted)
	var recordErr *download.InstallRecordError
	if !errors.As(err, &recordErr) {
		t.Fatalf("StartInstall error = %v, want stage record failure", err)
	}
	if got := snapshotTree(t, modDir); !reflect.DeepEqual(got, before) {
		t.Errorf("failed replace changed original: got %v, want %v", got, before)
	}
	index, err := download.LoadIndex("skyrimse")
	if err != nil || len(index.Archives) != 1 || !index.Archives[0].Uninstalled {
		t.Errorf("failed replacement download index = %+v, %v", index, err)
	}
	assertNoReinstallState(t, modsDir)
}

// TestReplaceInterruptedSwapRecovers checks that startup recovery restores or completes an interrupted replacement.
func TestReplaceInterruptedSwapRecovers(t *testing.T) {
	for _, tc := range []struct {
		step        string
		wantNewFile bool
	}{
		{step: "moved-aside"},
		{step: "installed", wantNewFile: true},
	} {
		t.Run(tc.step, func(t *testing.T) {
			d, folder, archive := replaceFixture(t)
			modDir := filepath.Join(config.ModsDir("skyrimse"), folder)
			d.reinstallFault = func(step string) error {
				if step == tc.step {
					return errSimulatedCrash
				}
				return nil
			}
			_, _, err := d.StartInstall(dto.StartInstallRequest{
				GameID: "skyrimse", ExternalArchivePath: archive, Mode: dto.InstallReplaceMod, TargetMod: folder,
			})
			if !errors.Is(err, errSimulatedCrash) {
				t.Fatalf("StartInstall error = %v, want simulated crash", err)
			}
			d.reinstallFault = nil
			restartDaemon(t, d)
			got := snapshotTree(t, modDir)
			if tc.wantNewFile {
				if got["b.txt"] != "new b" || got["c.txt"] != "new c" || got["a.txt"] != "" {
					t.Errorf("recovered replacement = %v", got)
				}
			} else if got["a.txt"] != "old a" || got["b.txt"] != "old b" || got["c.txt"] != "" {
				t.Errorf("recovered original = %v", got)
			}
			assertNoReinstallState(t, config.ModsDir("skyrimse"))
		})
	}
}

// TestReplaceInstallUsesNewDownloadDetails checks that a supplied archive version and mod page replace the old values.
func TestReplaceInstallUsesNewDownloadDetails(t *testing.T) {
	d, folder, archive := replaceFixture(t)
	modDir := filepath.Join(config.ModsDir("skyrimse"), folder)
	old, err := download.LoadModMetadata(modDir)
	if err != nil {
		t.Fatal(err)
	}
	old.Version = "old"
	old.ModPage = "old page"
	old.Category = "my category"
	if err := download.SaveModMetadata(modDir, old); err != nil {
		t.Fatal(err)
	}
	if err := download.SaveSidecar(archive, download.ArchiveSidecar{ModName: "new name", Version: "new", Category: "new category", GameDomain: "skyrimspecialedition", ModID: 42}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.StartInstall(dto.StartInstallRequest{GameID: "skyrimse", ArchiveRelPath: "Update.zip", Mode: dto.InstallReplaceMod, TargetMod: folder}); err != nil {
		t.Fatal(err)
	}
	meta, err := download.LoadModMetadata(modDir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Name != old.Name || meta.Category != old.Category || meta.Version != "new" || meta.ModPage != "https://www.nexusmods.com/skyrimspecialedition/mods/42" {
		t.Errorf("replacement metadata = %+v", meta)
	}
}

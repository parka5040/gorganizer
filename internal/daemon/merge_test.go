package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
)

// mergeFixture installs a base mod and creates an update archive within isolated test directories.
func mergeFixture(t *testing.T) (*Daemon, string, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	d := newStardewDaemon(t)
	folder, _ := installForReinstall(t, d, "skyrimse", "Base", map[string]string{"plugin.esp": "old", "keep.txt": "keep"}, false)
	update := filepath.Join(t.TempDir(), "Update.zip")
	writeZipFiles(t, update, map[string]string{"plugin.esp": "new", "added.txt": "added"})
	return d, folder, update
}

func TestMergeStageMetadataKeepsNewFieldsAndConcurrentEdits(t *testing.T) {
	for _, tc := range []struct {
		name     string
		current  download.ModMetadata
		category string
		version  string
	}{
		{name: "archive fields survive", current: download.ModMetadata{Category: "old"}, category: "old", version: "new"},
		{name: "user edits survive", current: download.ModMetadata{Category: "edited"}, category: "edited", version: "new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := &download.ModMetadata{Category: "old"}
			staged := &download.ModMetadata{Category: "old", Version: "new", SourceArchives: []download.SourceArchiveRef{{Path: "base.zip"}, {Path: "update.zip"}}}
			merged := mergeStageMetadata(snapshot, &tc.current, staged)
			if merged.Category != tc.category || merged.Version != tc.version || len(merged.SourceArchives) != 2 {
				t.Errorf("merged metadata = %+v", merged)
			}
		})
	}
}

func TestMergeCopyFailureLeavesTargetUntouched(t *testing.T) {
	d, folder, update := mergeFixture(t)
	modsDir := config.ModsDir("skyrimse")
	modDir := filepath.Join(modsDir, folder)
	before := snapshotTree(t, modDir)
	extracted := t.TempDir()
	if err := os.WriteFile(filepath.Join(extracted, "a.esp"), []byte("first"), 0644); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(extracted, "keep.txt", "nested.esp"))
	_, _, err := d.startInstallFrom(context.Background(), dto.StartInstallRequest{
		GameID: "skyrimse", ExternalArchivePath: update, Mode: dto.InstallMergeIntoMod, TargetMod: folder,
	}, extracted, new(bool))
	var rejected *download.ArchiveRejectedError
	if !errors.As(err, &rejected) || rejected.Reason != download.ArchiveRejectedUnsafeEntry {
		t.Fatalf("merge error = %v, want a target type collision after the first file", err)
	}
	if got := snapshotTree(t, modDir); !reflect.DeepEqual(got, before) {
		t.Errorf("original mod changed after failed merge: got %v, want %v", got, before)
	}
	assertNoReinstallState(t, modsDir)
}

func TestMergePublishesThroughSwap(t *testing.T) {
	d, folder, update := mergeFixture(t)
	modsDir := config.ModsDir("skyrimse")
	modDir := filepath.Join(modsDir, folder)
	oldFile, err := os.Stat(filepath.Join(modDir, "plugin.esp"))
	if err != nil {
		t.Fatal(err)
	}
	oldUnchanged, err := os.Stat(filepath.Join(modDir, "keep.txt"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := d.StreamInstallEvents(ctx, "skyrimse")
	if err != nil {
		t.Fatal(err)
	}
	checkedBeforeSwap := false
	d.reinstallFault = func(step string) error {
		if step != "install" {
			return nil
		}
		checkedBeforeSwap = true
		if _, err := os.Lstat(modDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("original folder at install step: %v, want moved aside", err)
		}
		for len(events) > 0 {
			event := <-events
			if event.Progress != nil && event.Progress.Step == dto.InstallStep(download.StageComplete) {
				t.Fatal("StageComplete was sent before the mod swap")
			}
		}
		return nil
	}
	installed, count, err := d.StartInstall(context.Background(), dto.StartInstallRequest{
		GameID: "skyrimse", ExternalArchivePath: update, Mode: dto.InstallMergeIntoMod, TargetMod: folder,
	})
	d.reinstallFault = nil
	if err != nil || installed != folder || count != 3 || !checkedBeforeSwap {
		t.Fatalf("StartInstall = %q, %d, %v; swap observed %t", installed, count, err, checkedBeforeSwap)
	}
	newFile, err := os.Stat(filepath.Join(modDir, "plugin.esp"))
	if err != nil || os.SameFile(oldFile, newFile) {
		t.Errorf("merged plugin still uses its original inode: %v", err)
	}
	unchanged, err := os.Stat(filepath.Join(modDir, "keep.txt"))
	if err != nil || !os.SameFile(oldUnchanged, unchanged) {
		t.Errorf("unchanged file was copied instead of hardlinked: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(modDir, "added.txt")); err != nil || string(data) != "added" {
		t.Errorf("added file = %q, %v", data, err)
	}
	meta, err := download.LoadModMetadata(modDir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Folder != folder || meta.FileCount != count || len(meta.SourceArchives) != 2 || meta.SourceArchives[0].Path != "Downloads/Base.zip" || meta.SourceArchives[1].Path != update || !meta.SourceArchives[1].Merged {
		t.Errorf("merged record = %+v", meta)
	}
	complete := false
	for len(events) > 0 {
		event := <-events
		if event.Progress != nil && event.Progress.Step == dto.InstallStep(download.StageComplete) && event.Progress.ModName == folder && event.Progress.FilesTotal == int64(count) {
			complete = true
		}
	}
	if !complete {
		t.Error("merge did not publish StageComplete for the original mod folder")
	}
	assertNoReinstallState(t, modsDir)
}

func TestInterruptedMergeRecoversAtStartup(t *testing.T) {
	for _, tc := range []struct {
		step       string
		wantMerged bool
	}{
		{step: "moved-aside"},
		{step: "installed", wantMerged: true},
	} {
		t.Run(tc.step, func(t *testing.T) {
			d, folder, update := mergeFixture(t)
			modsDir := config.ModsDir("skyrimse")
			modDir := filepath.Join(modsDir, folder)
			original := snapshotTree(t, modDir)
			d.reinstallFault = func(step string) error {
				if step == tc.step {
					return errSimulatedCrash
				}
				return nil
			}
			_, _, err := d.StartInstall(context.Background(), dto.StartInstallRequest{
				GameID: "skyrimse", ExternalArchivePath: update, Mode: dto.InstallMergeIntoMod, TargetMod: folder,
			})
			if !errors.Is(err, errSimulatedCrash) {
				t.Fatalf("StartInstall error = %v, want simulated crash", err)
			}
			d.reinstallFault = nil
			restartDaemon(t, d)
			got := snapshotTree(t, modDir)
			if tc.wantMerged {
				if got["plugin.esp"] != "new" || got["added.txt"] != "added" || got["keep.txt"] != "keep" {
					t.Errorf("recovered mod is incomplete: %v", got)
				}
				meta, err := download.LoadModMetadata(modDir)
				if err != nil || meta == nil || len(meta.SourceArchives) != 2 {
					t.Errorf("recovered record = %+v, %v; want both archives", meta, err)
				}
			} else if !reflect.DeepEqual(got, original) {
				t.Errorf("recovery did not restore the original: got %v, want %v", got, original)
			}
			assertNoReinstallState(t, modsDir)
		})
	}
}

func TestMergePreSwapFaultLeavesTargetUntouched(t *testing.T) {
	for _, step := range []string{"intent-written", "install"} {
		t.Run(step, func(t *testing.T) {
			d, folder, update := mergeFixture(t)
			modsDir := config.ModsDir("skyrimse")
			modDir := filepath.Join(modsDir, folder)
			before := snapshotTree(t, modDir)
			d.reinstallFault = func(current string) error {
				if current == step {
					return errSimulatedCrash
				}
				return nil
			}
			_, _, err := d.StartInstall(context.Background(), dto.StartInstallRequest{
				GameID: "skyrimse", ExternalArchivePath: update, Mode: dto.InstallMergeIntoMod, TargetMod: folder,
			})
			d.reinstallFault = nil
			if !errors.Is(err, errSimulatedCrash) {
				t.Fatalf("StartInstall error = %v, want simulated fault", err)
			}
			if got := snapshotTree(t, modDir); !reflect.DeepEqual(got, before) {
				t.Errorf("failed swap changed the original: got %v, want %v", got, before)
			}
			assertNoReinstallState(t, modsDir)
		})
	}
}

func TestMergeRefusesSymlinkInExistingMod(t *testing.T) {
	d, folder, update := mergeFixture(t)
	modsDir := config.ModsDir("skyrimse")
	modDir := filepath.Join(modsDir, folder)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(modDir, "linked.txt")); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, modDir)
	_, _, err := d.StartInstall(context.Background(), dto.StartInstallRequest{
		GameID: "skyrimse", ExternalArchivePath: update, Mode: dto.InstallMergeIntoMod, TargetMod: folder,
	})
	var rejected *download.ArchiveRejectedError
	if !errors.As(err, &rejected) || rejected.Reason != download.ArchiveRejectedUnsafeEntry {
		t.Fatalf("StartInstall error = %v, want unsafe entry", err)
	}
	if got := snapshotTree(t, modDir); !reflect.DeepEqual(got, before) {
		t.Errorf("refused merge changed the original: got %v, want %v", got, before)
	}
	assertNoReinstallState(t, modsDir)
}

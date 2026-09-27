package download

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// failInstallRecordWrite makes installation record writes fail until test cleanup.
func failInstallRecordWrite(t *testing.T, failure error) {
	t.Helper()
	original := writeModMetadataFn
	writeModMetadataFn = func(string, *ModMetadata) error { return failure }
	t.Cleanup(func() { writeModMetadataFn = original })
}

// TestMetadataFailureDoesNotPublishNewMod refuses a new mod without leaving a published or staged folder.
func TestMetadataFailureDoesNotPublishNewMod(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	modsDir := useModsDir(t)
	extract := t.TempDir()
	writeTree(t, extract, "plugin.esp")
	failure := errors.New("record write failed")
	failInstallRecordWrite(t, failure)

	_, err := Install(InstallRequest{GameID: "skyrimse", ExtractedRoot: extract, Mode: ModeNewMod, TargetMod: "New Mod"})
	var recordErr *InstallRecordError
	if !errors.As(err, &recordErr) || recordErr.Mod != "New Mod" || !errors.Is(err, failure) {
		t.Fatalf("Install error = %v, want InstallRecordError wrapping write failure", err)
	}
	if entries, err := os.ReadDir(modsDir); err != nil || len(entries) != 0 {
		t.Fatalf("mods directory entries = %v, %v, want empty", entries, err)
	}
}

// TestInstallFailureNeverEmitsComplete reports a record write failure without announcing completion.
func TestInstallFailureNeverEmitsComplete(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	useModsDir(t)
	extract := t.TempDir()
	writeTree(t, extract, "plugin.esp")
	failInstallRecordWrite(t, errors.New("record write failed"))
	var events []InstallProgress
	_, err := Install(InstallRequest{
		GameID: "skyrimse", ExtractedRoot: extract, Mode: ModeNewMod, TargetMod: "New Mod",
		ProgressSink: func(p InstallProgress) { events = append(events, p) },
	})
	if err == nil {
		t.Fatal("Install succeeded despite a failed record write")
	}
	if len(events) == 0 || events[len(events)-1].Step != StageFailed || events[len(events)-1].Error != err.Error() {
		t.Fatalf("last progress = %+v, want StageFailed with the install error", events)
	}
	for _, event := range events {
		if event.Step == StageComplete {
			t.Fatalf("failed install emitted StageComplete: %+v", events)
		}
	}
}

// TestCanonicalMetadataWrittenBeforeRename observes the complete record inside staging before publication.
func TestCanonicalMetadataWrittenBeforeRename(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	modsDir := useModsDir(t)
	extract := t.TempDir()
	writeTree(t, extract, "plugin.esp")
	original := renameInstallStageFn
	seen := false
	renameInstallStageFn = func(stage, final string) error {
		if filepath.Dir(stage) != modsDir || filepath.Base(final) != "New Mod" {
			t.Fatalf("rename from %q to %q", stage, final)
		}
		if _, err := os.Stat(final); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("final dir exists before rename: %v", err)
		}
		meta, err := LoadModMetadata(stage)
		if err != nil || meta.Folder != "New Mod" || meta.FileCount != 1 || len(meta.SourceArchives) != 1 || meta.SourceArchives[0].Merged {
			t.Fatalf("staged metadata = %+v, %v", meta, err)
		}
		if _, err := os.Stat(filepath.Join(stage, "metadata.yaml")); err != nil {
			t.Fatalf("staged record missing: %v", err)
		}
		seen = true
		return os.Rename(stage, final)
	}
	t.Cleanup(func() { renameInstallStageFn = original })

	var complete InstallProgress
	result, err := Install(InstallRequest{
		GameID: "skyrimse", ExtractedRoot: extract, Mode: ModeNewMod, TargetMod: "New Mod",
		SourceArchiveRef: SourceArchiveRef{Path: "external.zip", Merged: true},
		ProgressSink: func(p InstallProgress) {
			if p.Step == StageComplete {
				complete = p
			}
		},
	})
	if err != nil || !seen || result.FileCount != 1 || complete.FilesTotal != 1 || complete.FilesDone != 1 {
		t.Fatalf("Install = %+v, %v; rename observed %t, completion %+v", result, err, seen, complete)
	}
}

// TestArchiveMetadataIsIgnored excludes forged root records across copy paths while preserving nested files.
func TestArchiveMetadataIsIgnored(t *testing.T) {
	for _, tc := range []struct {
		name       string
		files      []string
		selection  []FomodFile
		planner    LayoutPlanner
		mode       InstallMode
		wantNested string
		wantFiles  int
	}{
		{name: "flat", files: []string{"MeTaDaTa.YaMl", "plugin.esp", "sub/metadata.yaml"}, wantNested: "sub/metadata.yaml"},
		{name: "merge", files: []string{"MeTaDaTa.YaMl", "plugin.esp", "sub/metadata.yaml"}, mode: ModeMergeIntoMod, wantNested: "sub/metadata.yaml"},
		{name: "FOMOD file selection", files: []string{"fomod/ModuleConfig.xml", "MeTaDaTa.YaMl", "plugin.esp", "sub/metadata.yaml"}, selection: []FomodFile{{Source: "MeTaDaTa.YaMl"}, {Source: "plugin.esp"}, {Source: "sub", Destination: "sub", IsFolder: true}}, wantNested: "sub/metadata.yaml"},
		{name: "FOMOD folder selection", files: []string{"fomod/ModuleConfig.xml", "Content/MeTaDaTa.YaMl", "Content/plugin.esp", "Content/sub/metadata.yaml"}, selection: []FomodFile{{Source: "Content", IsFolder: true}}, wantNested: "sub/metadata.yaml"},
		{name: "planned", files: []string{"Pack/MeTaDaTa.YaMl", "Pack/plugin.esp", "Pack/sub/metadata.yaml"}, planner: &fakePlanner{copies: []PlannedCopy{{SourceRel: "Pack", DestName: "Pack"}}}, wantNested: "Pack/MeTaDaTa.YaMl", wantFiles: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			modsDir := useModsDir(t)
			extract := t.TempDir()
			forged := "folder: \"Forged\"\nmod_page: \"https://forged.example\"\nsource_archives:\n  - path: \"forged.zip\"\n"
			for _, name := range tc.files {
				content := name
				if strings.EqualFold(filepath.Base(name), "metadata.yaml") {
					content = forged
				}
				writeFomodTestFile(t, extract, name, content)
			}
			final := filepath.Join(modsDir, "Target")
			if tc.mode == ModeMergeIntoMod {
				if err := os.MkdirAll(final, 0755); err != nil {
					t.Fatal(err)
				}
				if err := SaveModMetadata(final, &ModMetadata{Folder: "Target", Name: "Existing", SourceArchives: []SourceArchiveRef{{Path: "original.zip"}}}); err != nil {
					t.Fatal(err)
				}
			}
			result, err := Install(InstallRequest{
				GameID: "skyrimse", ExtractedRoot: extract, ContentRoot: extract,
				Mode: tc.mode, TargetMod: "Target", SourceArchiveRef: SourceArchiveRef{Path: "actual.zip"},
				ModPage: "https://actual.example", FomodSelectedFiles: tc.selection, Layout: tc.planner,
			})
			if err != nil {
				t.Fatal(err)
			}
			meta, err := LoadModMetadata(final)
			if err != nil {
				t.Fatal(err)
			}
			wantRefs := []SourceArchiveRef{{Path: "actual.zip"}}
			if tc.mode == ModeMergeIntoMod {
				wantRefs = append([]SourceArchiveRef{{Path: "original.zip"}}, wantRefs...)
			}
			paths := make([]string, 0, len(meta.SourceArchives))
			for _, ref := range meta.SourceArchives {
				paths = append(paths, ref.Path)
			}
			wantPaths := make([]string, 0, len(wantRefs))
			for _, ref := range wantRefs {
				wantPaths = append(wantPaths, ref.Path)
			}
			if !reflect.DeepEqual(paths, wantPaths) || meta.Folder != "Target" || meta.ModPage != "https://actual.example" {
				t.Errorf("record = %+v, want only our refs %v and canonical fields", meta, wantPaths)
			}
			if _, err := os.Stat(filepath.Join(final, tc.wantNested)); err != nil {
				t.Errorf("nested metadata not installed: %v", err)
			}
			wantFiles := tc.wantFiles
			if wantFiles == 0 {
				wantFiles = 2
			}
			if result.FileCount != len(meta.Files) || result.FileCount != wantFiles {
				t.Errorf("file count = %d, record files = %v, want %d", result.FileCount, meta.Files, wantFiles)
			}
			for _, file := range result.Files {
				if strings.EqualFold(file, "metadata.yaml") {
					t.Errorf("forged record listed as installed file: %v", result.Files)
				}
			}
		})
	}
}

// TestMergeMetadataFailureReportsError reports an unsuccessful merge record without a completion event.
func TestMergeMetadataFailureReportsError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	modsDir := useModsDir(t)
	final := filepath.Join(modsDir, "Existing")
	if err := os.MkdirAll(final, 0755); err != nil {
		t.Fatal(err)
	}
	if err := SaveModMetadata(final, &ModMetadata{Folder: "Existing", SourceArchives: []SourceArchiveRef{{Path: "old.zip"}}, Files: []string{"old.esp"}, FileCount: 1}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(final, "metadata.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	extract := t.TempDir()
	writeTree(t, extract, "new.esp")
	failure := errors.New("record write failed")
	failInstallRecordWrite(t, failure)
	var events []InstallProgress
	_, err = Install(InstallRequest{
		GameID: "skyrimse", ExtractedRoot: extract, Mode: ModeMergeIntoMod, TargetMod: "Existing",
		ProgressSink: func(p InstallProgress) { events = append(events, p) },
	})
	var recordErr *InstallRecordError
	if !errors.As(err, &recordErr) || recordErr.Mod != "Existing" || !errors.Is(err, failure) {
		t.Fatalf("Install error = %v, want InstallRecordError wrapping write failure", err)
	}
	if len(events) == 0 || events[len(events)-1].Step != StageFailed || events[len(events)-1].Error != err.Error() {
		t.Fatalf("progress events = %+v, want trailing StageFailed", events)
	}
	for _, event := range events {
		if event.Step == StageComplete {
			t.Fatalf("failed merge emitted StageComplete: %+v", events)
		}
	}
	if after, err := os.ReadFile(filepath.Join(final, "metadata.yaml")); err != nil || !reflect.DeepEqual(after, before) {
		t.Errorf("existing metadata changed: %q, %v", after, err)
	}
	if data, err := os.ReadFile(filepath.Join(final, "new.esp")); err != nil || string(data) != "content:new.esp" {
		t.Errorf("merged file = %q, %v", data, err)
	}
	if entries := listFiles(t, modsDir); len(entries) != 2 {
		t.Errorf("unexpected staged files after failed merge: %v", entries)
	}
}

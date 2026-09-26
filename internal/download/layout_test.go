package download

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"
)

type fakePlanner struct {
	copies []PlannedCopy
	err    error
	calls  int
	roots  []string
}

// Plan records the extract root it was given and returns the configured plan.
func (f *fakePlanner) Plan(extractRoot string) ([]PlannedCopy, error) {
	f.calls++
	f.roots = append(f.roots, extractRoot)
	return f.copies, f.err
}

// writeTree creates each slash-separated file under root with content derived from its path.
func writeTree(t *testing.T, root string, files ...string) {
	t.Helper()
	for _, rel := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("content:"+rel), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

// listFiles returns the sorted slash-separated regular files under root.
func listFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// useModsDir points the package mods-dir resolver at a temp dir for the duration of the test.
func useModsDir(t *testing.T) string {
	t.Helper()
	modsDir := t.TempDir()
	previous := modsDirResolver
	SetModsDirResolver(func(string) string { return modsDir })
	t.Cleanup(func() { modsDirResolver = previous })
	return modsDir
}

// writeZip writes a zip archive at path holding the given slash-separated files.
func writeZip(t *testing.T, path string, files map[string][]byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(files[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestCopyPlannedPlacesFoldersUnderDestNames verifies planned sources land under their destination folders.
func TestCopyPlannedPlacesFoldersUnderDestNames(t *testing.T) {
	cases := []struct {
		name        string
		files       []string
		copies      []PlannedCopy
		wantWritten []string
	}{
		{
			name:   "nested folder",
			files:  []string{"Wrapper/ModA/manifest.json", "Wrapper/ModA/assets/sprite.png", "Wrapper/readme.txt"},
			copies: []PlannedCopy{{SourceRel: "Wrapper/ModA", DestName: "ModA"}},
			wantWritten: []string{
				"ModA/assets/sprite.png",
				"ModA/manifest.json",
			},
		},
		{
			name:        "empty source wraps the archive root",
			files:       []string{"manifest.json", "Mod.dll"},
			copies:      []PlannedCopy{{SourceRel: "", DestName: "Wrapped Mod"}},
			wantWritten: []string{"Wrapped Mod/Mod.dll", "Wrapped Mod/manifest.json"},
		},
		{
			name:   "several folders",
			files:  []string{"Pack/[CP] Pack/manifest.json", "Pack/[CP] Pack/content.json", "Pack/Core/manifest.json"},
			copies: []PlannedCopy{{SourceRel: "Pack/Core", DestName: "Core"}, {SourceRel: "Pack/[CP] Pack", DestName: "[CP] Pack"}},
			wantWritten: []string{
				"Core/manifest.json",
				"[CP] Pack/content.json",
				"[CP] Pack/manifest.json",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			extract := t.TempDir()
			stage := t.TempDir()
			writeTree(t, extract, tc.files...)
			written, err := copyPlanned(extract, stage, tc.copies, "test", nil)
			if err != nil {
				t.Fatalf("copyPlanned: %v", err)
			}
			sort.Strings(written)
			if !reflect.DeepEqual(written, tc.wantWritten) {
				t.Errorf("written = %v, want %v", written, tc.wantWritten)
			}
			if got := listFiles(t, stage); !reflect.DeepEqual(got, tc.wantWritten) {
				t.Errorf("staged files = %v, want %v", got, tc.wantWritten)
			}
			for _, rel := range tc.wantWritten {
				data, err := os.ReadFile(filepath.Join(stage, filepath.FromSlash(rel)))
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(string(data), "content:") {
					t.Errorf("staged %s content = %q", rel, data)
				}
			}
		})
	}
}

// TestCopyPlannedRejectsUnsafePlans verifies unsafe destinations, sources and collisions are refused before staging.
func TestCopyPlannedRejectsUnsafePlans(t *testing.T) {
	cases := []struct {
		name   string
		copies []PlannedCopy
	}{
		{name: "parent destination", copies: []PlannedCopy{{SourceRel: "ModA", DestName: ".."}}},
		{name: "dot destination", copies: []PlannedCopy{{SourceRel: "ModA", DestName: "."}}},
		{name: "hidden destination", copies: []PlannedCopy{{SourceRel: "ModA", DestName: ".hidden"}}},
		{name: "reserved gorganizer root", copies: []PlannedCopy{{SourceRel: "ModA", DestName: ".gorganizer-root"}}},
		{name: "nested destination", copies: []PlannedCopy{{SourceRel: "ModA", DestName: "a/b"}}},
		{name: "backslash destination", copies: []PlannedCopy{{SourceRel: "ModA", DestName: `a\b`}}},
		{name: "empty destination", copies: []PlannedCopy{{SourceRel: "ModA", DestName: ""}}},
		{name: "whitespace destination", copies: []PlannedCopy{{SourceRel: "ModA", DestName: " ModA"}}},
		{name: "metadata destination", copies: []PlannedCopy{{SourceRel: "ModA", DestName: "metadata.yaml"}}},
		{name: "metadata destination folded", copies: []PlannedCopy{{SourceRel: "ModA", DestName: "MetaData.YAML"}}},
		{name: "casefold collision", copies: []PlannedCopy{{SourceRel: "ModA", DestName: "Mod"}, {SourceRel: "ModB", DestName: "mod"}}},
		{name: "escaping source", copies: []PlannedCopy{{SourceRel: "../outside", DestName: "Outside"}}},
		{name: "absolute source", copies: []PlannedCopy{{SourceRel: "/etc", DestName: "Etc"}}},
		{name: "file source", copies: []PlannedCopy{{SourceRel: "ModA/manifest.json", DestName: "Manifest"}}},
		{name: "missing source", copies: []PlannedCopy{{SourceRel: "Missing", DestName: "Missing"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			extract := filepath.Join(parent, "extract")
			writeTree(t, extract, "ModA/manifest.json", "ModB/manifest.json")
			writeTree(t, parent, "outside/secret.txt")
			stage := t.TempDir()
			if _, err := copyPlanned(extract, stage, tc.copies, "test", nil); !errors.Is(err, ErrUnsafeArchive) {
				t.Fatalf("copyPlanned(%+v) error = %v, want ErrUnsafeArchive", tc.copies, err)
			}
			if got := listFiles(t, stage); len(got) != 0 {
				t.Errorf("unsafe plan staged files %v", got)
			}
		})
	}
}

// TestCopyPlannedRejectsEscapingSymlinkAndSpecialFiles verifies archive symlinks and FIFOs never reach the stage.
func TestCopyPlannedRejectsEscapingSymlinkAndSpecialFiles(t *testing.T) {
	cases := []struct {
		name    string
		wantErr string
		setup   func(t *testing.T, modDir string)
	}{
		{
			name:    "symlink escaping the extract root",
			wantErr: "resolves outside its extract root",
			setup: func(t *testing.T, modDir string) {
				outside := filepath.Join(t.TempDir(), "outside.dll")
				if err := os.WriteFile(outside, []byte("outside"), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(modDir, "escape.dll")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:    "planned source symlinked outside the extract root",
			wantErr: "resolves outside the extract root",
			setup: func(t *testing.T, modDir string) {
				outsideDir := t.TempDir()
				writeTree(t, outsideDir, "manifest.json")
				if err := os.RemoveAll(modDir); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outsideDir, modDir); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:    "fifo",
			wantErr: "unsupported special file",
			setup: func(t *testing.T, modDir string) {
				if err := syscall.Mkfifo(filepath.Join(modDir, "pipe"), 0644); err != nil {
					t.Skipf("mkfifo unavailable: %v", err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			extract := t.TempDir()
			stage := t.TempDir()
			writeTree(t, extract, "ModA/manifest.json")
			tc.setup(t, filepath.Join(extract, "ModA"))
			_, err := copyPlanned(extract, stage, []PlannedCopy{{SourceRel: "ModA", DestName: "ModA"}}, "test", nil)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("copyPlanned error = %v, want %q", err, tc.wantErr)
			}
			if _, statErr := os.Stat(filepath.Join(stage, "ModA", "escape.dll")); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("escaping symlink target was staged: %v", statErr)
			}
		})
	}
}

// TestInstallWithPlannerRejectsFomodSelections verifies FOMOD selections are refused before any work for layout installs.
func TestInstallWithPlannerRejectsFomodSelections(t *testing.T) {
	modsDir := useModsDir(t)
	extract := t.TempDir()
	writeTree(t, extract, "ModA/manifest.json")
	planner := &fakePlanner{copies: []PlannedCopy{{SourceRel: "ModA", DestName: "ModA"}}}
	_, err := Install(InstallRequest{
		GameID: "stardewvalley", ExtractedRoot: extract, Mode: ModeNewMod, TargetMod: "Target",
		FomodSelectedFiles: []FomodFile{{Source: "ModA", Destination: "ModA", IsFolder: true}},
		Layout:             planner,
	})
	if !errors.Is(err, ErrFomodNotSupportedForLayout) {
		t.Fatalf("Install error = %v, want ErrFomodNotSupportedForLayout", err)
	}
	if planner.calls != 0 {
		t.Errorf("planner called %d times, want 0", planner.calls)
	}
	if entries, _ := os.ReadDir(modsDir); len(entries) != 0 {
		t.Errorf("mods dir not empty after refused install: %v", entries)
	}
}

// TestInstallWithPlannerIgnoresFomodInstaller verifies a layout install stages the plan instead of requiring the FOMOD wizard.
func TestInstallWithPlannerIgnoresFomodInstaller(t *testing.T) {
	modsDir := useModsDir(t)
	extract := t.TempDir()
	writeTree(t, extract, "fomod/ModuleConfig.xml", "ModA/manifest.json", "ModA/ModA.dll")

	planner := &fakePlanner{copies: []PlannedCopy{{SourceRel: "ModA", DestName: "ModA"}}}
	result, err := Install(InstallRequest{
		GameID: "stardewvalley", ExtractedRoot: extract, Mode: ModeNewMod, TargetMod: "Target",
		SourceArchiveRef: SourceArchiveRef{Path: "/external/ModA.zip"},
		Layout:           planner,
	})
	if err != nil {
		t.Fatalf("Install with planner: %v", err)
	}
	if planner.calls != 1 || planner.roots[0] != extract {
		t.Errorf("planner calls = %d roots = %v, want one call on %s", planner.calls, planner.roots, extract)
	}
	want := []string{"ModA/ModA.dll", "ModA/manifest.json", "metadata.yaml"}
	if got := listFiles(t, filepath.Join(modsDir, "Target")); !reflect.DeepEqual(got, want) {
		t.Errorf("installed files = %v, want %v", got, want)
	}
	if result.FileCount != 2 {
		t.Errorf("FileCount = %d, want 2", result.FileCount)
	}
	meta, err := LoadModMetadata(filepath.Join(modsDir, "Target"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(meta.Files, []string{"ModA/ModA.dll", "ModA/manifest.json"}) {
		t.Errorf("metadata files = %v", meta.Files)
	}

	_, err = Install(InstallRequest{
		GameID: "stardewvalley", ExtractedRoot: extract, Mode: ModeNewMod, TargetMod: "Flattened",
	})
	if _, isFomod := IsFomodMarker(err); !isFomod {
		t.Fatalf("nil planner install error = %v, want FOMOD marker", err)
	}
}

// TestInstallWithPlannerSkipsNestedFomodExpansion verifies a layout install copies nested .fomod files verbatim.
func TestInstallWithPlannerSkipsNestedFomodExpansion(t *testing.T) {
	modsDir := useModsDir(t)
	nested := filepath.Join(t.TempDir(), "inner.fomod")
	writeZip(t, nested, map[string][]byte{"payload.txt": []byte("payload")})
	nestedBytes, err := os.ReadFile(nested)
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "ModA.zip")
	writeZip(t, archive, map[string][]byte{
		"ModA/manifest.json": []byte("{}"),
		"ModA/inner.fomod":   nestedBytes,
	})

	planner := &fakePlanner{copies: []PlannedCopy{{SourceRel: "ModA", DestName: "ModA"}}}
	if _, err := Install(InstallRequest{
		GameID: "stardewvalley", ArchivePath: archive, Mode: ModeNewMod, TargetMod: "Target",
		Layout: planner,
	}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	want := []string{"ModA/inner.fomod", "ModA/manifest.json", "metadata.yaml"}
	if got := listFiles(t, filepath.Join(modsDir, "Target")); !reflect.DeepEqual(got, want) {
		t.Errorf("installed files = %v, want %v", got, want)
	}
}

// TestInstallWithPlannerPropagatesPlanError verifies a planner refusal aborts the install without staging anything.
func TestInstallWithPlannerPropagatesPlanError(t *testing.T) {
	modsDir := useModsDir(t)
	extract := t.TempDir()
	writeTree(t, extract, "readme.txt")
	refusal := errors.New("no manifest")
	var events []InstallProgress
	_, err := Install(InstallRequest{
		GameID: "stardewvalley", ExtractedRoot: extract, Mode: ModeNewMod, TargetMod: "Target",
		Layout:       &fakePlanner{err: refusal},
		ProgressSink: func(p InstallProgress) { events = append(events, p) },
	})
	if !errors.Is(err, refusal) {
		t.Fatalf("Install error = %v, want wrapped planner refusal", err)
	}
	if entries, _ := os.ReadDir(modsDir); len(entries) != 0 {
		t.Errorf("mods dir not empty after refused plan: %v", entries)
	}
	if len(events) == 0 || events[len(events)-1].Step != StageFailed {
		t.Errorf("progress events = %+v, want a trailing StageFailed", events)
	}
}

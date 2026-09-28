package download

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFindContentRoot_PreservesKnownDataSubdir(t *testing.T) {
	cases := []struct {
		name      string
		layout    []string
		wantInner string
	}{
		{
			name:      "lowercase nvse wrapper preserved",
			layout:    []string{"nvse/plugins/foo.dll"},
			wantInner: "extract",
		},
		{
			name:      "uppercase NVSE wrapper preserved",
			layout:    []string{"NVSE/Plugins/foo.dll"},
			wantInner: "extract",
		},
		{
			name:      "skse wrapper preserved",
			layout:    []string{"skse/plugins/bar.dll"},
			wantInner: "extract",
		},
		{
			name:      "edit scripts wrapper preserved",
			layout:    []string{"Edit Scripts/foo.pas"},
			wantInner: "extract",
		},
		{
			name:      "ModName + Data dives into Data",
			layout:    []string{"MyMod/Data/foo.esp", "MyMod/Data/meshes/x.nif"},
			wantInner: "Data",
		},
		{
			name:      "ModName wrapper without Data still stripped",
			layout:    []string{"MyMod/foo.esp", "MyMod/meshes/x.nif"},
			wantInner: "MyMod",
		},
		{
			name:      "Data folder still stripped",
			layout:    []string{"Data/foo.esp"},
			wantInner: "Data",
		},
		{
			name:      "multiple top-level dirs, no strip",
			layout:    []string{"nvse/plugins/foo.dll", "textures/x.dds"},
			wantInner: "extract",
		},
		{
			name:      "Oblivion Remastered nested Data is flattened",
			layout:    []string{"OblivionRemastered/Content/Dev/ObvData/Data/foo.esp"},
			wantInner: "Data",
		},
		{
			name:      "Oblivion Remastered nested Data below archive wrapper is flattened",
			layout:    []string{"MyMod/OblivionRemastered/Content/Dev/ObvData/Data/foo.esp"},
			wantInner: "Data",
		},
		{
			name: "Oblivion Remastered multi-root archive stays rooted",
			layout: []string{
				"OblivionRemastered/Content/Dev/ObvData/Data/foo.esp",
				"OblivionRemastered/Content/Paks/~mods/foo.pak",
			},
			wantInner: "extract",
		},
		{
			name:      "Oblivion Remastered root-only PAK archive stays rooted",
			layout:    []string{"OblivionRemastered/Content/Paks/~mods/foo.pak"},
			wantInner: "extract",
		},
		{
			name:      "Oblivion Remastered root-only Engine archive stays rooted",
			layout:    []string{"Engine/Binaries/Win64/foo.dll"},
			wantInner: "extract",
		},
		{
			name:      "Oblivion Remastered lowercase nested Data is flattened",
			layout:    []string{"oblivionremastered/content/dev/obvdata/data/foo.esp"},
			wantInner: "data",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			extract := filepath.Join(tmp, "extract")
			if err := os.MkdirAll(extract, 0755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			for _, p := range tc.layout {
				full := filepath.Join(extract, p)
				if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
					t.Fatalf("mkdir parent: %v", err)
				}
				if err := os.WriteFile(full, []byte("x"), 0644); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
			got := findContentRoot(extract)
			if filepath.Base(got) != tc.wantInner {
				t.Errorf("findContentRoot(%v): got %s, want basename %s",
					tc.layout, got, tc.wantInner)
			}
		})
	}
}

func TestRouteOblivionRemasteredFullHierarchy(t *testing.T) {
	tests := map[string]string{
		filepath.Join("OblivionRemastered", "Content", "Dev", "ObvData", "Data", "Example.esp"): "Example.esp",
		filepath.Join("OblivionRemastered", "Content", "Paks", "~mods", "Example.pak"):          filepath.Join(".gorganizer-root", "OblivionRemastered", "Content", "Paks", "~mods", "Example.pak"),
		filepath.Join("OblivionRemastered", "Binaries", "Win64", "Example.dll"):                 filepath.Join(".gorganizer-root", "OblivionRemastered", "Binaries", "Win64", "Example.dll"),
		filepath.Join("Engine", "Binaries", "Win64", "Example.dll"):                             filepath.Join(".gorganizer-root", "Engine", "Binaries", "Win64", "Example.dll"),
		filepath.Join("engine", "binaries", "Win64", "lower.dll"):                               filepath.Join(".gorganizer-root", "engine", "binaries", "Win64", "lower.dll"),
		filepath.Join("OBLIVIONREMASTERED", "CONTENT", "DEV", "OBVDATA", "DATA", "Case.esp"):    "Case.esp",
		"readme.txt": "readme.txt",
	}
	for input, want := range tests {
		if got := routeOblivionRemasteredPath(input, true); got != want {
			t.Errorf("routeOblivionRemasteredPath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCopyFomodSelectionRejectsEscapingPaths(t *testing.T) {
	extract := t.TempDir()
	stage := t.TempDir()
	outside := filepath.Join(filepath.Dir(extract), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := copyFomodSelection("skyrimse", extract, stage, []FomodFile{{
		Source: "../outside.txt", Destination: "safe.txt",
	}}, "test", nil); err == nil {
		t.Fatal("escaping FOMOD source was accepted")
	}
	inside := filepath.Join(extract, "inside.txt")
	if err := os.WriteFile(inside, []byte("inside"), 0644); err != nil {
		t.Fatal(err)
	}
	escapeDestination := filepath.Join(filepath.Dir(stage), "escaped.txt")
	if _, err := copyFomodSelection("skyrimse", extract, stage, []FomodFile{{
		Source: "inside.txt", Destination: "../escaped.txt",
	}}, "test", nil); err == nil {
		t.Fatal("escaping FOMOD destination was accepted")
	}
	if _, err := os.Stat(escapeDestination); !os.IsNotExist(err) {
		t.Fatalf("escaping destination was written: %v", err)
	}
	folder := filepath.Join(extract, "folder")
	if err := os.MkdirAll(folder, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(folder, "escape-link.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := copyFomodSelection("skyrimse", extract, stage, []FomodFile{{
		Source: "folder", Destination: "copied", IsFolder: true,
	}}, "test", nil); err == nil {
		t.Fatal("escaping FOMOD source symlink was accepted")
	}
}

func TestCopyFomodSelectionRoutesExplicitOblivionRemasteredDestination(t *testing.T) {
	extract := t.TempDir()
	stage := t.TempDir()
	if err := os.WriteFile(filepath.Join(extract, "payload.dll"), []byte("payload"), 0644); err != nil {
		t.Fatal(err)
	}
	destination := "oblivionremastered/binaries/Win64/payload.dll"
	if _, err := copyFomodSelection("oblivionremastered", extract, stage, []FomodFile{{
		Source: "payload.dll", Destination: destination,
	}}, "test", nil); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(stage, ".gorganizer-root", filepath.FromSlash(destination))
	if got, err := os.ReadFile(want); err != nil || string(got) != "payload" {
		t.Fatalf("routed FOMOD payload = %q, %v", got, err)
	}
}

func TestCopyFlattenRejectsArchiveSymlinkEscape(t *testing.T) {
	extract := t.TempDir()
	stage := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.dll")
	if err := os.WriteFile(outside, []byte("outside"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(extract, "escape.dll")); err != nil {
		t.Fatal(err)
	}
	if _, err := copyFlatten("skyrimse", extract, extract, stage, "test", nil, false); err == nil {
		t.Fatal("escaping archive symlink was accepted")
	}
}

// writeFomodTestFile creates an extracted file within a test directory.
func writeFomodTestFile(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestFomodSelectionPriorityOrder applies lower priorities first and preserves request order for ties.
func TestFomodSelectionPriorityOrder(t *testing.T) {
	for _, tc := range []struct {
		name     string
		priority int32
		want     string
	}{
		{name: "higher priority wins", priority: 0, want: "high"},
		{name: "equal priority preserves request order", priority: 5, want: "low"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extract := t.TempDir()
			stage := t.TempDir()
			writeFomodTestFile(t, extract, "high.esp", "high")
			writeFomodTestFile(t, extract, "low.esp", "low")
			selection := []FomodFile{
				{Source: "high.esp", Destination: "chosen.esp", Priority: 5},
				{Source: "low.esp", Destination: "chosen.esp", Priority: tc.priority},
			}
			if _, err := copyFomodSelection("skyrimse", extract, stage, selection, "test", nil); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(stage, "chosen.esp"))
			if err != nil || string(data) != tc.want {
				t.Fatalf("chosen.esp = %q, %v; want %q", data, err, tc.want)
			}
			if selection[0].Source != "high.esp" || selection[1].Source != "low.esp" {
				t.Fatalf("selection order was modified: %+v", selection)
			}
		})
	}
}

// TestFomodEmptyDestinationSemantics copies folder contents and file basenames into the mod root.
func TestFomodEmptyDestinationSemantics(t *testing.T) {
	extract := t.TempDir()
	stage := t.TempDir()
	writeFomodTestFile(t, extract, "Textures/Foo.dds", "texture")
	writeFomodTestFile(t, extract, "plugins/My.esp", "plugin")
	written, err := copyFomodSelection("skyrimse", extract, stage, []FomodFile{
		{Source: "Textures", IsFolder: true},
		{Source: "plugins/My.esp"},
	}, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(written, []string{"Foo.dds", "My.esp"}) {
		t.Fatalf("written = %v", written)
	}
	for name, want := range map[string]string{"Foo.dds": "texture", "My.esp": "plugin"} {
		data, err := os.ReadFile(filepath.Join(stage, name))
		if err != nil || string(data) != want {
			t.Errorf("%s = %q, %v; want %q", name, data, err, want)
		}
	}
	for _, name := range []string{"Textures", "plugins"} {
		if _, err := os.Stat(filepath.Join(stage, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("unexpected wrapper %s: %v", name, err)
		}
	}
}

// TestFomodSourceCaseInsensitive resolves Windows paths and refuses ambiguous casefolded sources.
func TestFomodSourceCaseInsensitive(t *testing.T) {
	extract := t.TempDir()
	writeFomodTestFile(t, extract, "textures/foo.dds", "texture")
	writeFomodTestFile(t, extract, "A.esp", "upper")
	writeFomodTestFile(t, extract, "a.esp", "lower")
	writeFomodTestFile(t, extract, "a.ESP", "exact")
	for _, tc := range []struct {
		name   string
		source string
		want   string
		fail   bool
	}{
		{name: "Windows path", source: `Textures\Foo.dds`, want: "texture"},
		{name: "exact case wins", source: "A.esp", want: "upper"},
		{name: "exact case wins after folded matches", source: "a.ESP", want: "exact"},
		{name: "ambiguous fold", source: "A.ESP", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stage := t.TempDir()
			_, err := copyFomodSelection("skyrimse", extract, stage, []FomodFile{{Source: tc.source, Destination: "result"}}, "test", nil)
			if tc.fail {
				if err == nil || !strings.Contains(err.Error(), tc.source) {
					t.Fatalf("ambiguous source error = %v, want source name", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(stage, "result"))
			if err != nil || string(data) != tc.want {
				t.Fatalf("result = %q, %v; want %q", data, err, tc.want)
			}
		})
	}
}

// TestFomodAllSourcesMissingIsEmptySelection refuses selections that install no files.
func TestFomodAllSourcesMissingIsEmptySelection(t *testing.T) {
	extract := t.TempDir()
	stage := t.TempDir()
	_, err := copyFomodSelection("skyrimse", extract, stage, []FomodFile{{Source: "missing.esp"}}, "test", nil)
	if !errors.Is(err, ErrEmptyInstallSelection) {
		t.Fatalf("missing selection error = %v, want ErrEmptyInstallSelection", err)
	}
}

// TestLegacyFomodSkipsScripts omits installer metadata and C# scripts from legacy flat installs.
func TestLegacyFomodSkipsScripts(t *testing.T) {
	extract := t.TempDir()
	stage := t.TempDir()
	for _, name := range []string{"fomod/info.xml", "installer.cs", "scripts/Build.CS", "scripts/Readme.txt", "plugin.esp"} {
		writeFomodTestFile(t, extract, name, name)
	}
	written, err := copyFlatten("skyrimse", extract, extract, stage, "test", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(written, []string{"plugin.esp", "scripts/Readme.txt"}) {
		t.Fatalf("written = %v", written)
	}
	for _, name := range []string{"fomod", "installer.cs", "scripts/Build.CS"} {
		if _, err := os.Stat(filepath.Join(stage, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("unexpected legacy install entry %s: %v", name, err)
		}
	}
}

// TestDetectContentRootCanonicalDataDirs keeps Bethesda data folders rooted and strips genuine wrappers.
func TestDetectContentRootCanonicalDataDirs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []string
		want  string
	}{
		{name: "music", files: []string{"music/track.xwm"}},
		{name: "strings", files: []string{"strings/English.strings"}},
		{name: "materials", files: []string{"materials/foo.bgsm"}},
		{name: "obse", files: []string{"obse/Plugins/foo.dll"}},
		{name: "sfse", files: []string{"sfse/Plugins/foo.dll"}},
		{name: "SKSE", files: []string{"SKSE/Plugins/foo.dll"}},
		{name: "data folder with nested Data", files: []string{"SKSE/Data/Plugins/foo.dll"}},
		{name: "data folder beside wrapper", files: []string{"music/track.xwm", "Docs/readme.txt"}},
		{name: "wrapper", files: []string{"ModName/meshes/foo.nif"}, want: "ModName"},
		{name: "Data", files: []string{"Data/foo.esp"}, want: "Data"},
		{name: "root plugin", files: []string{"foo.esp", "ModName/readme.txt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extract := t.TempDir()
			for _, name := range tc.files {
				writeFomodTestFile(t, extract, name, "content")
			}
			got, ambiguous := DetectContentRoot(extract, "skyrimse")
			if got != tc.want || ambiguous {
				t.Fatalf("DetectContentRoot = %q, ambiguous %t; want %q", got, ambiguous, tc.want)
			}
		})
	}
}

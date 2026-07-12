package download

import (
	"os"
	"path/filepath"
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
	if _, err := copyFlatten("skyrimse", extract, stage, "test", nil); err == nil {
		t.Fatal("escaping archive symlink was accepted")
	}
}

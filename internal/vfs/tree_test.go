package vfs

import (
	"os"
	"path/filepath"
	"testing"
)

// createLayerTree creates a temporary directory tree representing a mod layer.
// files is a map of relative paths to file contents.
func createLayerTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for relPath, content := range files {
		absPath := filepath.Join(dir, relPath)
		if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestMergedTreeBuild(t *testing.T) {
	base := createLayerTree(t, map[string]string{
		"Textures/sky.dds":  "base-sky",
		"Textures/land.dds": "base-land",
		"meshes/actor.nif":  "base-actor",
	})

	tree := NewMergedTree()
	layers := []Layer{
		{Name: "__base__", RootPath: base, Enabled: true},
	}
	if err := tree.Build(layers); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Verify files are discoverable.
	if realPath, ok := tree.LookupFile("textures/sky.dds"); !ok {
		t.Error("expected to find textures/sky.dds")
	} else if realPath != filepath.Join(base, "Textures/sky.dds") {
		t.Errorf("unexpected realPath: %s", realPath)
	}

	// Verify case-insensitive lookup.
	if _, ok := tree.LookupFile("TEXTURES/SKY.DDS"); !ok {
		t.Error("case-insensitive lookup failed for TEXTURES/SKY.DDS")
	}

	// Verify directory listing.
	children, ok := tree.Children("")
	if !ok {
		t.Fatal("root directory not found")
	}
	if len(children) != 2 { // textures, meshes
		t.Errorf("expected 2 root children, got %d", len(children))
	}

	// Verify IsDir.
	if !tree.IsDir("textures") {
		t.Error("expected textures to be a directory")
	}
	if tree.IsDir("textures/sky.dds") {
		t.Error("expected textures/sky.dds to not be a directory")
	}

	// Verify stats.
	fileCount, dirCount := tree.Stats()
	if fileCount != 3 {
		t.Errorf("expected 3 files, got %d", fileCount)
	}
	if dirCount != 3 { // root, textures, meshes
		t.Errorf("expected 3 dirs, got %d", dirCount)
	}
}

func TestMergedTreePriorityOverride(t *testing.T) {
	base := createLayerTree(t, map[string]string{
		"textures/sky.dds": "base-sky",
		"textures/sea.dds": "base-sea",
	})
	modA := createLayerTree(t, map[string]string{
		"textures/sky.dds": "modA-sky",
	})
	modB := createLayerTree(t, map[string]string{
		"textures/sky.dds": "modB-sky",
	})

	tree := NewMergedTree()
	layers := []Layer{
		{Name: "__base__", RootPath: base, Enabled: true},
		{Name: "ModA", RootPath: modA, Enabled: true},
		{Name: "ModB", RootPath: modB, Enabled: true},
	}
	if err := tree.Build(layers); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// ModB has highest priority, so its sky.dds should win.
	realPath, ok := tree.LookupFile("textures/sky.dds")
	if !ok {
		t.Fatal("expected to find textures/sky.dds")
	}
	expected := filepath.Join(modB, "textures/sky.dds")
	if realPath != expected {
		t.Errorf("expected %s, got %s", expected, realPath)
	}

	// sea.dds only exists in base, should still be found.
	realPath, ok = tree.LookupFile("textures/sea.dds")
	if !ok {
		t.Fatal("expected to find textures/sea.dds")
	}
	expected = filepath.Join(base, "textures/sea.dds")
	if realPath != expected {
		t.Errorf("expected %s, got %s", expected, realPath)
	}
}

func TestMergedTreeDirectoryMerge(t *testing.T) {
	base := createLayerTree(t, map[string]string{
		"textures/sky.dds": "base",
	})
	mod := createLayerTree(t, map[string]string{
		"textures/cloud.dds": "mod",
		"scripts/main.pex":   "mod",
	})

	tree := NewMergedTree()
	layers := []Layer{
		{Name: "__base__", RootPath: base, Enabled: true},
		{Name: "WeatherMod", RootPath: mod, Enabled: true},
	}
	if err := tree.Build(layers); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// textures/ should contain files from both layers.
	children, ok := tree.Children("textures")
	if !ok {
		t.Fatal("textures directory not found")
	}
	if len(children) != 2 { // sky.dds, cloud.dds
		t.Errorf("expected 2 children in textures, got %d", len(children))
	}

	// scripts/ should only come from the mod.
	children, ok = tree.Children("scripts")
	if !ok {
		t.Fatal("scripts directory not found")
	}
	if len(children) != 1 {
		t.Errorf("expected 1 child in scripts, got %d", len(children))
	}

	// Root should have both textures and scripts.
	rootChildren, ok := tree.Children("")
	if !ok {
		t.Fatal("root not found")
	}
	if len(rootChildren) != 2 {
		t.Errorf("expected 2 root children, got %d", len(rootChildren))
	}
}

func TestMergedTreeDisabledLayer(t *testing.T) {
	base := createLayerTree(t, map[string]string{
		"textures/sky.dds": "base",
	})
	disabled := createLayerTree(t, map[string]string{
		"textures/sky.dds":   "disabled-override",
		"textures/extra.dds": "disabled-new",
	})

	tree := NewMergedTree()
	layers := []Layer{
		{Name: "__base__", RootPath: base, Enabled: true},
		{Name: "DisabledMod", RootPath: disabled, Enabled: false},
	}
	if err := tree.Build(layers); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// sky.dds should come from base, not the disabled mod.
	realPath, _ := tree.LookupFile("textures/sky.dds")
	if realPath != filepath.Join(base, "textures/sky.dds") {
		t.Errorf("disabled mod's file should not override base: got %s", realPath)
	}

	// extra.dds should not exist since the mod is disabled.
	if _, ok := tree.LookupFile("textures/extra.dds"); ok {
		t.Error("disabled mod's new file should not appear")
	}
}

func TestMergedTreeRebuild(t *testing.T) {
	base := createLayerTree(t, map[string]string{
		"textures/sky.dds": "base",
	})
	mod := createLayerTree(t, map[string]string{
		"textures/sky.dds": "mod",
	})

	tree := NewMergedTree()
	layers := []Layer{
		{Name: "__base__", RootPath: base, Enabled: true},
	}
	if err := tree.Build(layers); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// sky.dds should come from base.
	realPath, _ := tree.LookupFile("textures/sky.dds")
	if realPath != filepath.Join(base, "textures/sky.dds") {
		t.Fatalf("expected base path, got %s", realPath)
	}

	// Rebuild with mod enabled.
	newLayers := []Layer{
		{Name: "__base__", RootPath: base, Enabled: true},
		{Name: "Mod", RootPath: mod, Enabled: true},
	}
	if err := tree.Rebuild(newLayers); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	// Now sky.dds should come from mod.
	realPath, _ = tree.LookupFile("textures/sky.dds")
	if realPath != filepath.Join(mod, "textures/sky.dds") {
		t.Errorf("after rebuild, expected mod path, got %s", realPath)
	}
}

func BenchmarkTreeBuild(b *testing.B) {
	// Create a base layer with realistic file count.
	dir := b.TempDir()
	dirs := []string{"textures", "meshes", "scripts", "sound", "interface"}
	for _, d := range dirs {
		for i := range 200 {
			subdir := filepath.Join(dir, d, "sub"+string(rune('a'+i%26)))
			os.MkdirAll(subdir, 0755)
			for j := range 50 {
				name := filepath.Join(subdir, "file"+string(rune('0'+j%10))+".dds")
				os.WriteFile(name, []byte("x"), 0644)
			}
		}
	}

	layers := []Layer{
		{Name: "__base__", RootPath: dir, Enabled: true},
	}

	b.ResetTimer()
	for range b.N {
		tree := NewMergedTree()
		if err := tree.Build(layers); err != nil {
			b.Fatal(err)
		}
	}
}

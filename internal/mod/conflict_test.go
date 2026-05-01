package mod

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parka/gorganizer/internal/vfs"
)

func createTestLayer(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for relPath, content := range files {
		absPath := filepath.Join(dir, relPath)
		os.MkdirAll(filepath.Dir(absPath), 0755)
		os.WriteFile(absPath, []byte(content), 0644)
	}
	return dir
}

func TestBuildConflictMapNoConflicts(t *testing.T) {
	base := createTestLayer(t, map[string]string{
		"textures/sky.dds": "base",
	})
	modA := createTestLayer(t, map[string]string{
		"textures/cloud.dds": "modA",
	})

	layers := []vfs.Layer{
		{Name: "__base__", RootPath: base, Enabled: true},
		{Name: "ModA", RootPath: modA, Enabled: true},
	}

	cm, err := BuildConflictMap(layers)
	if err != nil {
		t.Fatalf("BuildConflictMap: %v", err)
	}
	if len(cm.Conflicts) != 0 {
		t.Errorf("expected 0 conflicts, got %d", len(cm.Conflicts))
	}
}

func TestBuildConflictMapTwoMods(t *testing.T) {
	base := createTestLayer(t, map[string]string{
		"textures/sky.dds": "base",
	})
	modA := createTestLayer(t, map[string]string{
		"textures/sky.dds": "modA",
	})
	modB := createTestLayer(t, map[string]string{
		"textures/sky.dds": "modB",
	})

	layers := []vfs.Layer{
		{Name: "__base__", RootPath: base, Enabled: true},
		{Name: "ModA", RootPath: modA, Enabled: true},
		{Name: "ModB", RootPath: modB, Enabled: true},
	}

	cm, err := BuildConflictMap(layers)
	if err != nil {
		t.Fatalf("BuildConflictMap: %v", err)
	}

	conflict, ok := cm.Conflicts["textures/sky.dds"]
	if !ok {
		t.Fatal("expected conflict for textures/sky.dds")
	}
	if conflict.Winner != "ModB" {
		t.Errorf("expected ModB as winner, got %s", conflict.Winner)
	}
	if len(conflict.Losers) != 2 {
		t.Fatalf("expected 2 losers, got %d", len(conflict.Losers))
	}
	if conflict.Losers[0] != "__base__" {
		t.Errorf("expected __base__ as first loser, got %s", conflict.Losers[0])
	}
	if conflict.Losers[1] != "ModA" {
		t.Errorf("expected ModA as second loser, got %s", conflict.Losers[1])
	}
}

func TestBuildConflictMapDisabledLayer(t *testing.T) {
	base := createTestLayer(t, map[string]string{
		"textures/sky.dds": "base",
	})
	disabled := createTestLayer(t, map[string]string{
		"textures/sky.dds": "disabled",
	})

	layers := []vfs.Layer{
		{Name: "__base__", RootPath: base, Enabled: true},
		{Name: "DisabledMod", RootPath: disabled, Enabled: false},
	}

	cm, err := BuildConflictMap(layers)
	if err != nil {
		t.Fatalf("BuildConflictMap: %v", err)
	}
	if len(cm.Conflicts) != 0 {
		t.Errorf("expected 0 conflicts when mod is disabled, got %d", len(cm.Conflicts))
	}
}

func TestConflictMapForMod(t *testing.T) {
	base := createTestLayer(t, map[string]string{
		"textures/sky.dds":  "base",
		"textures/land.dds": "base",
	})
	modA := createTestLayer(t, map[string]string{
		"textures/sky.dds": "modA",
	})

	layers := []vfs.Layer{
		{Name: "__base__", RootPath: base, Enabled: true},
		{Name: "ModA", RootPath: modA, Enabled: true},
	}

	cm, err := BuildConflictMap(layers)
	if err != nil {
		t.Fatal(err)
	}

	modAConflicts := cm.ForMod("ModA")
	if len(modAConflicts) != 1 {
		t.Errorf("expected 1 conflict for ModA, got %d", len(modAConflicts))
	}
	if cm.WinnerCount("ModA") != 1 {
		t.Errorf("expected ModA to win 1 conflict, got %d", cm.WinnerCount("ModA"))
	}

	baseConflicts := cm.ForMod("__base__")
	if len(baseConflicts) != 1 {
		t.Errorf("expected 1 conflict for __base__, got %d", len(baseConflicts))
	}
	if cm.LoserCount("__base__") != 1 {
		t.Errorf("expected __base__ to lose 1 conflict, got %d", cm.LoserCount("__base__"))
	}
}

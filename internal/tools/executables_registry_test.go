package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectExecutables_FindsKnownTools(t *testing.T) {
	dir := t.TempDir()
	dataOrig := filepath.Join(dir, "Data.orig")
	modA := filepath.Join(dir, "mods", "xEdit")
	modB := filepath.Join(dir, "mods", "DynDOLOD")
	for _, p := range []string{
		filepath.Join(modA, "SSEEdit.exe"),
		filepath.Join(modB, "DynDOLOD", "DynDOLODx64.exe"),
		filepath.Join(dataOrig, "notatool.exe"),
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("MZ"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	got := DetectExecutables([]string{dataOrig, modA, modB})
	byTitle := map[string]DetectedTool{}
	for _, d := range got {
		byTitle[d.Title] = d
	}
	if _, ok := byTitle["SSEEdit / xEdit"]; !ok {
		t.Errorf("did not detect SSEEdit; got %+v", got)
	}
	dl, ok := byTitle["DynDOLOD"]
	if !ok {
		t.Fatalf("did not detect DynDOLOD; got %+v", got)
	}
	if dl.CaptureOutputToMod != "DynDOLOD Output" || !dl.ExtraRWScratch {
		t.Errorf("DynDOLOD defaults wrong: %+v", dl)
	}
	if len(got) != 2 {
		t.Errorf("expected exactly 2 tools, got %d: %+v", len(got), got)
	}
}

func TestManagedLOOTCatalogHookRequiresManagedPath(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	if _, ok := ValidateCatalogMatch("loot", "skyrimse", filepath.Join(t.TempDir(), "gorganizer", "tools", "loot", "LOOT.exe")); ok {
		t.Fatal("unmanaged LOOT path received managed hooks")
	}
	managed := filepath.Join(dataHome, "gorganizer", "tools", "loot", "0.29.1", "LOOT.exe")
	if _, ok := ValidateCatalogMatch("loot", "skyrimse", managed); !ok {
		t.Fatal("managed LOOT path was rejected")
	}
}

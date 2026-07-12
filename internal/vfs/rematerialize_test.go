package vfs

import (
	"os"
	"path/filepath"
	"testing"
)

// ReMaterialize must (a) make a newly-enabled mod visible on disk, and (b)
func TestReMaterialize_AppliesModAndCapturesWrites(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")
	modA := filepath.Join(dir, "mods", "ModA")
	overwrite := filepath.Join(dir, "mods", "Overwrite")

	mustFile(t, filepath.Join(dataPath, "Skyrim.esm"), "master")
	mustFile(t, filepath.Join(modA, "ModA.esp"), "modA-plugin")
	mustDir(t, overwrite)

	mm := NewMountManager(dataPath, overwrite, "skyrimse")
	base := []Layer{
		{Name: "__base__", RootPath: dataPath, Enabled: true},
		{Name: "Overwrite", RootPath: overwrite, Enabled: true},
	}
	if err := mm.Activate(base, ""); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	t.Cleanup(func() { _ = mm.Deactivate() })

	mustFile(t, filepath.Join(dataPath, "Saves", "quicksave.ess"), "SAVE")

	next := []Layer{
		{Name: "__base__", RootPath: dataPath, Enabled: true},
		{Name: "ModA", RootPath: modA, Enabled: true},
		{Name: "Overwrite", RootPath: overwrite, Enabled: true},
	}
	if err := mm.MarkDirty(next); err != nil {
		t.Fatalf("MarkDirty: %v", err)
	}
	if appliedLayers := mm.AppliedLayers(); len(appliedLayers) != 2 || appliedLayers[1].Name != "Overwrite" {
		t.Fatalf("applied layers changed before materialization: %+v", appliedLayers)
	}
	if !mm.IsDirty() {
		t.Fatal("expected dirty after MarkDirty")
	}
	if err := mm.ReMaterialize(); err != nil {
		t.Fatalf("ReMaterialize: %v", err)
	}
	if mm.IsDirty() {
		t.Error("expected clean after ReMaterialize")
	}
	if appliedLayers := mm.AppliedLayers(); len(appliedLayers) != 3 || appliedLayers[1].Name != "ModA" {
		t.Fatalf("applied layers were not advanced: %+v", appliedLayers)
	}
	applied, desired := mm.Generations()
	if applied != desired {
		t.Errorf("applied(%d) != desired(%d)", applied, desired)
	}

	if got := mustRead(t, filepath.Join(dataPath, "ModA.esp")); got != "modA-plugin" {
		t.Errorf("ModA.esp = %q, want materialized", got)
	}
	if got := mustRead(t, filepath.Join(overwrite, "Saves", "quicksave.ess")); got != "SAVE" {
		t.Errorf("save not captured into Overwrite: %q", got)
	}
	if got := mustRead(t, filepath.Join(dataPath, "Saves", "quicksave.ess")); got != "SAVE" {
		t.Errorf("captured save not re-linked into farm: %q", got)
	}
	for _, sib := range []string{stagingDirPath(dataPath), oldFarmPath(dataPath), applyingIntentPath(dataPath)} {
		if _, err := os.Stat(sib); !os.IsNotExist(err) {
			t.Errorf("residue left behind: %s", sib)
		}
	}
}

// ReMaterialize must materialize the LATEST in-memory tree and set appliedGen to
func TestReMaterialize_MaterializesLatestTree(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")
	modA := filepath.Join(dir, "mods", "ModA")
	modB := filepath.Join(dir, "mods", "ModB")
	overwrite := filepath.Join(dir, "mods", "Overwrite")

	mustFile(t, filepath.Join(dataPath, "Skyrim.esm"), "master")
	mustFile(t, filepath.Join(modA, "ModA.esp"), "A")
	mustFile(t, filepath.Join(modB, "ModB.esp"), "B")
	mustDir(t, overwrite)

	mm := NewMountManager(dataPath, overwrite, "skyrimse")
	if err := mm.Activate([]Layer{
		{Name: "__base__", RootPath: dataPath, Enabled: true},
		{Name: "Overwrite", RootPath: overwrite, Enabled: true},
	}, ""); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	t.Cleanup(func() { _ = mm.Deactivate() })

	if err := mm.MarkDirty([]Layer{
		{Name: "__base__", RootPath: dataPath, Enabled: true},
		{Name: "ModA", RootPath: modA, Enabled: true},
		{Name: "Overwrite", RootPath: overwrite, Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := mm.MarkDirty([]Layer{
		{Name: "__base__", RootPath: dataPath, Enabled: true},
		{Name: "ModB", RootPath: modB, Enabled: true},
		{Name: "Overwrite", RootPath: overwrite, Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	_, desired := mm.Generations()
	if desired != 3 {
		t.Fatalf("desiredGen = %d, want 3", desired)
	}

	if err := mm.ReMaterialize(); err != nil {
		t.Fatalf("ReMaterialize: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataPath, "ModB.esp")); err != nil {
		t.Errorf("latest mod (B) should be materialized: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataPath, "ModA.esp")); !os.IsNotExist(err) {
		t.Error("superseded mod (A) must NOT be materialized")
	}
	applied, desired := mm.Generations()
	if applied != desired || applied != 3 {
		t.Errorf("gens applied=%d desired=%d, want both 3", applied, desired)
	}
}

// A clean farm ReMaterialize is a no-op.
func TestReMaterialize_NoopWhenClean(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")
	mustFile(t, filepath.Join(dataPath, "Skyrim.esm"), "master")

	mm := NewMountManager(dataPath, "", "skyrimse")
	if err := mm.Activate([]Layer{{Name: "__base__", RootPath: dataPath, Enabled: true}}, ""); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	t.Cleanup(func() { _ = mm.Deactivate() })

	applied0, desired0 := mm.Generations()
	if err := mm.ReMaterialize(); err != nil {
		t.Fatalf("ReMaterialize (clean): %v", err)
	}
	applied1, desired1 := mm.Generations()
	if applied0 != applied1 || desired0 != desired1 {
		t.Errorf("clean ReMaterialize changed gens: before(%d/%d) after(%d/%d)",
			applied0, desired0, applied1, desired1)
	}
}

// CaptureNewFilesInto with relink must move the loose file into the target AND
func TestCaptureNewFilesInto_Relink(t *testing.T) {
	dir := t.TempDir()
	farm := filepath.Join(dir, "Data")
	target := filepath.Join(dir, "mods", "DynDOLOD Output")
	mustDir(t, target)
	mustFile(t, filepath.Join(farm, "meshes", "lod.nif"), "LODDATA")
	if err := os.Chmod(filepath.Join(farm, "meshes", "lod.nif"), 0620); err != nil {
		t.Fatal(err)
	}

	n, err := CaptureNewFilesInto(farm, target, true, false)
	if err != nil {
		t.Fatalf("CaptureNewFilesInto: %v", err)
	}
	if n != 1 {
		t.Fatalf("moved %d, want 1", n)
	}
	if got := mustRead(t, filepath.Join(target, "meshes", "lod.nif")); got != "LODDATA" {
		t.Errorf("target copy = %q", got)
	}
	if got := mustRead(t, filepath.Join(farm, "meshes", "lod.nif")); got != "LODDATA" {
		t.Errorf("farm re-link = %q", got)
	}
	for _, path := range []string{
		filepath.Join(target, "meshes", "lod.nif"),
		filepath.Join(farm, "meshes", "lod.nif"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0620 {
			t.Errorf("%s mode changed to %o, want 620", path, info.Mode().Perm())
		}
	}
}

func TestCaptureNewFilesIntoSkipsEngineControlFiles(t *testing.T) {
	farm := t.TempDir()
	target := t.TempDir()
	for _, name := range []string{"Plugins.txt", "loadorder.txt"} {
		if err := os.WriteFile(filepath.Join(farm, name), []byte("state"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	n, err := CaptureNewFilesInto(farm, target, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("captured %d engine control files", n)
	}
	for _, name := range []string{"Plugins.txt", "loadorder.txt"} {
		if _, err := os.Stat(filepath.Join(farm, name)); err != nil {
			t.Fatalf("control file %s was removed: %v", name, err)
		}
	}
}

package profile

import (
	"testing"

	"github.com/parka/gorganizer/internal/mod"
)

func TestCreateAndLoad(t *testing.T) {
	dir := t.TempDir()
	pm := NewManager(dir)

	p, err := pm.Create("skyrimse", "Default")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.Name != "Default" {
		t.Errorf("Name = %q, want \"Default\"", p.Name)
	}
	if p.GameID != "skyrimse" {
		t.Errorf("GameID = %q, want \"skyrimse\"", p.GameID)
	}

	loaded, entries, err := pm.Load("skyrimse", "Default")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Name != "Default" {
		t.Errorf("loaded Name = %q", loaded.Name)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty modlist, got %d entries", len(entries))
	}
}

func TestSaveWithModList(t *testing.T) {
	dir := t.TempDir()
	pm := NewManager(dir)

	p, err := pm.Create("skyrimse", "Modded")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	entries := []mod.ModListEntry{
		{Name: "USSEP", Enabled: true},
		{Name: "SkyUI", Enabled: true},
		{Name: "HD Textures", Enabled: false},
	}
	if err := pm.Save(p, entries); err != nil {
		t.Fatalf("Save: %v", err)
	}

	_, loaded, err := pm.Load("skyrimse", "Modded")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(loaded))
	}
	if loaded[0].Name != "USSEP" || !loaded[0].Enabled {
		t.Errorf("entry 0: %+v", loaded[0])
	}
	if loaded[2].Name != "HD Textures" || loaded[2].Enabled {
		t.Errorf("entry 2: %+v", loaded[2])
	}
}

func TestListProfiles(t *testing.T) {
	dir := t.TempDir()
	pm := NewManager(dir)

	pm.Create("skyrimse", "Default")
	pm.Create("skyrimse", "Vanilla")
	pm.Create("falloutnv", "TestProfile")

	profiles, err := pm.List("skyrimse")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(profiles) != 2 {
		t.Errorf("expected 2 skyrimse profiles, got %d", len(profiles))
	}

	profiles, err = pm.List("falloutnv")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(profiles) != 1 {
		t.Errorf("expected 1 falloutnv profile, got %d", len(profiles))
	}
}

func TestDeleteProfile(t *testing.T) {
	dir := t.TempDir()
	pm := NewManager(dir)

	pm.Create("skyrimse", "ToDelete")

	if err := pm.Delete("skyrimse", "ToDelete"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	profiles, _ := pm.List("skyrimse")
	if len(profiles) != 0 {
		t.Errorf("expected 0 profiles after delete, got %d", len(profiles))
	}
}

func TestCreateDuplicate(t *testing.T) {
	dir := t.TempDir()
	pm := NewManager(dir)

	pm.Create("skyrimse", "Default")
	_, err := pm.Create("skyrimse", "Default")
	if err == nil {
		t.Error("expected error when creating duplicate profile")
	}
}

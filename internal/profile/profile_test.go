package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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

func TestPluginStateOrderOverridesCompatibilityMirror(t *testing.T) {
	pm := NewManager(t.TempDir())
	want := []PluginLoadoutEntry{
		{Filename: "First.esp", Enabled: false},
		{Filename: "Second.esm", Enabled: true},
	}
	if err := pm.SavePluginLoadout("oblivionremastered", "Default", want); err != nil {
		t.Fatal(err)
	}
	mirrorPath := filepath.Join(pm.ProfileDir("oblivionremastered", "Default"), pluginOrderFile)
	if err := os.WriteFile(mirrorPath, []byte("Second.esm\nFirst.esp\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got, authoritative, err := pm.LoadPluginLoadoutSnapshot("oblivionremastered", "Default")
	if err != nil {
		t.Fatal(err)
	}
	if !authoritative {
		t.Fatal("signed plugin state was not treated as authoritative")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loadout = %#v, want authoritative state %#v", got, want)
	}
}

func TestPluginLoadoutSurvivesCompatibilityMirrorFailure(t *testing.T) {
	pm := NewManager(t.TempDir())
	dir := pm.ProfileDir("skyrimse", "Default")
	if err := os.MkdirAll(filepath.Join(dir, pluginOrderFile), 0755); err != nil {
		t.Fatal(err)
	}
	want := []PluginLoadoutEntry{{Filename: "SkyUI_SE.esp", Enabled: false}}
	if err := pm.SavePluginLoadout("skyrimse", "Default", want); err != nil {
		t.Fatalf("authoritative save failed because mirror was unwritable: %v", err)
	}
	got, err := pm.LoadPluginLoadout("skyrimse", "Default")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loadout = %#v, want %#v", got, want)
	}
}

func TestConcurrentPluginLoadoutSavesKeepMirrorConsistent(t *testing.T) {
	pm := NewManager(t.TempDir())
	const writers = 32
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			entries := []PluginLoadoutEntry{
				{Filename: fmt.Sprintf("First-%02d.esp", i), Enabled: i%2 == 0},
				{Filename: fmt.Sprintf("Second-%02d.esp", i), Enabled: i%2 != 0},
			}
			if err := pm.SavePluginLoadout("skyrimse", "Concurrent", entries); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	loadout, err := pm.LoadPluginLoadout("skyrimse", "Concurrent")
	if err != nil {
		t.Fatal(err)
	}
	mirror, err := pm.LoadPluginOrder("skyrimse", "Concurrent")
	if err != nil {
		t.Fatal(err)
	}
	var authoritativeOrder []string
	for _, entry := range loadout {
		authoritativeOrder = append(authoritativeOrder, entry.Filename)
	}
	if !reflect.DeepEqual(mirror, authoritativeOrder) {
		t.Fatalf("compatibility mirror = %v, authoritative order = %v", mirror, authoritativeOrder)
	}
}

func TestPluginLoadoutLegacyOrderDefaultsEnabled(t *testing.T) {
	dir := t.TempDir()
	pm := NewManager(dir)
	if err := pm.SavePluginOrder("skyrimse", "Default", []string{"Skyrim.esm", "SkyUI_SE.esp"}); err != nil {
		t.Fatal(err)
	}

	loadout, err := pm.LoadPluginLoadout("skyrimse", "Default")
	if err != nil {
		t.Fatal(err)
	}
	want := []PluginLoadoutEntry{
		{Filename: "Skyrim.esm", Enabled: true},
		{Filename: "SkyUI_SE.esp", Enabled: true},
	}
	if !reflect.DeepEqual(loadout, want) {
		t.Fatalf("loadout = %#v, want %#v", loadout, want)
	}
	if _, exists, err := pm.LoadPluginState("skyrimse", "Default"); err != nil || exists {
		t.Fatalf("legacy state exists=%v err=%v, want false/nil", exists, err)
	}
}

func TestSaveAndLoadPluginLoadout(t *testing.T) {
	dir := t.TempDir()
	pm := NewManager(dir)
	want := []PluginLoadoutEntry{
		{Filename: "Skyrim.esm", Enabled: true},
		{Filename: "Some Patch.esp", Enabled: false},
		{Filename: "SkyUI_SE.esp", Enabled: true},
	}
	if err := pm.SavePluginLoadout("skyrimse", "Modded", want); err != nil {
		t.Fatal(err)
	}

	got, err := pm.LoadPluginLoadout("skyrimse", "Modded")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loadout = %#v, want %#v", got, want)
	}

	stateBytes, err := os.ReadFile(filepath.Join(pm.ProfileDir("skyrimse", "Modded"), pluginStateFile))
	if err != nil {
		t.Fatal(err)
	}
	stateText := string(stateBytes)
	for _, signed := range []string{"+Skyrim.esm\n", "-Some Patch.esp\n", "+SkyUI_SE.esp\n"} {
		if !strings.Contains(stateText, signed) {
			t.Errorf("plugin_state.txt missing %q:\n%s", signed, stateText)
		}
	}
}

func TestLegacySetPluginOrderPreservesActivationState(t *testing.T) {
	pm := NewManager(t.TempDir())
	if err := pm.SavePluginLoadout("skyrimse", "Default", []PluginLoadoutEntry{
		{Filename: "A.esp", Enabled: false},
		{Filename: "B.esp", Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := pm.SavePluginOrder("skyrimse", "Default", []string{"B.esp", "A.esp"}); err != nil {
		t.Fatal(err)
	}
	got, err := pm.LoadPluginLoadout("skyrimse", "Default")
	if err != nil {
		t.Fatal(err)
	}
	want := []PluginLoadoutEntry{
		{Filename: "B.esp", Enabled: true},
		{Filename: "A.esp", Enabled: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loadout = %#v, want %#v", got, want)
	}
}

func TestLoadPluginStateRejectsUnsignedEntry(t *testing.T) {
	dir := t.TempDir()
	pm := NewManager(dir)
	profileDir := pm.ProfileDir("skyrimse", "Broken")
	if err := os.MkdirAll(profileDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, pluginStateFile), []byte("SkyUI_SE.esp\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pm.LoadPluginState("skyrimse", "Broken"); err == nil {
		t.Fatal("LoadPluginState accepted an unsigned entry")
	}
}

func TestSavePluginLoadoutNormalizesDuplicatesAndClears(t *testing.T) {
	dir := t.TempDir()
	pm := NewManager(dir)
	entries := []PluginLoadoutEntry{
		{Filename: " SkyUI_SE.esp ", Enabled: false},
		{Filename: "skyui_se.ESP", Enabled: true},
		{Filename: "\n", Enabled: true},
	}
	if err := pm.SavePluginLoadout("skyrimse", "Default", entries); err != nil {
		t.Fatal(err)
	}
	got, err := pm.LoadPluginLoadout("skyrimse", "Default")
	if err != nil {
		t.Fatal(err)
	}
	want := []PluginLoadoutEntry{{Filename: "SkyUI_SE.esp", Enabled: false}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loadout = %#v, want %#v", got, want)
	}

	if err := pm.SavePluginLoadout("skyrimse", "Default", nil); err != nil {
		t.Fatal(err)
	}
	profileDir := pm.ProfileDir("skyrimse", "Default")
	if _, err := os.Stat(filepath.Join(profileDir, pluginOrderFile)); !os.IsNotExist(err) {
		t.Errorf("compatibility mirror was not cleared: %v", err)
	}
	got, authoritative, err := pm.LoadPluginLoadoutSnapshot("skyrimse", "Default")
	if err != nil {
		t.Fatal(err)
	}
	if !authoritative || len(got) != 0 {
		t.Fatalf("cleared loadout = %#v authoritative=%v, want empty authoritative state", got, authoritative)
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

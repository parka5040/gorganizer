package profile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/parka/gorganizer/internal/mod"
)

func TestProfileIdentityComesFromDirectory(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pm := NewManager(t.TempDir())
	dir := pm.ProfileDir("skyrimse", "Default")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"name":"other","game_id":"othergame","created_at":"2026-01-02T03:04:05Z","use_custom_ini":true}`)
	if err := os.WriteFile(filepath.Join(dir, "profile.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".hidden", "bad\\name"} {
		hidden := pm.ProfileDir("skyrimse", name)
		if err := os.MkdirAll(hidden, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(hidden, "profile.json"), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(dir, pm.ProfileDir("skyrimse", "Linked")); err != nil {
		t.Fatal(err)
	}

	loaded, _, err := pm.Load("skyrimse", "Default")
	if err != nil {
		t.Fatal(err)
	}
	listed, err := pm.List("skyrimse")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("List returned %d profiles, want one: %+v", len(listed), listed)
	}
	for _, p := range []*Profile{loaded, listed[0]} {
		if p.Name != "Default" || p.GameID != "skyrimse" || p.CreatedAt.Format("2006-01-02T15:04:05Z") != "2026-01-02T03:04:05Z" || !p.UseCustomIni {
			t.Errorf("profile = %+v, want directory identity and saved settings", p)
		}
	}
}

func TestProfileWritesRejectUnsafeNames(t *testing.T) {
	for _, name := range []string{"..", "a/b", ".x", ""} {
		for _, segment := range []string{"profile", "game"} {
			t.Run(fmt.Sprintf("%s/%q", segment, name), func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				root := t.TempDir()
				pm := NewManager(root)
				gameID, profileName := "skyrimse", "Default"
				if segment == "game" {
					gameID = name
				} else {
					profileName = name
				}
				checks := []struct {
					name string
					run  func() error
				}{
					{"Load", func() error { _, _, err := pm.Load(gameID, profileName); return err }},
					{"Save", func() error { return pm.Save(&Profile{GameID: gameID, Name: profileName}, nil) }},
					{"Create", func() error { _, err := pm.Create(gameID, profileName); return err }},
					{"Delete", func() error { return pm.Delete(gameID, profileName) }},
					{"List", func() error { _, err := pm.List(gameID); return err }},
					{"CheckedProfileDir", func() error { _, err := pm.CheckedProfileDir(gameID, profileName); return err }},
					{"CheckedProfilesDir", func() error { _, err := pm.CheckedProfilesDir(gameID); return err }},
					{"SavePluginOrder", func() error { return pm.SavePluginOrder(gameID, profileName, []string{"A.esp"}) }},
					{"SavePluginLoadout", func() error {
						return pm.SavePluginLoadout(gameID, profileName, []PluginLoadoutEntry{{Filename: "A.esp", Enabled: true}})
					}},
					{"LoadPluginOrder", func() error { _, err := pm.LoadPluginOrder(gameID, profileName); return err }},
					{"LoadPluginState", func() error { _, _, err := pm.LoadPluginState(gameID, profileName); return err }},
					{"LoadPluginLoadoutSnapshot", func() error { _, _, err := pm.LoadPluginLoadoutSnapshot(gameID, profileName); return err }},
				}
				for _, check := range checks {
					if (check.name == "List" || check.name == "CheckedProfilesDir") && segment != "game" {
						continue
					}
					err := check.run()
					var invalid *IdentityInvalidError
					if !errors.As(err, &invalid) || invalid.Name != name {
						t.Errorf("%s returned %v, want IdentityInvalidError for %q", check.name, err, name)
					}
				}
				entries, err := os.ReadDir(root)
				if err != nil || len(entries) != 0 {
					t.Errorf("invalid identity created entries %v: %v", entries, err)
				}
			})
		}
	}
}

func TestSaveRefusesSymlinkedProfileDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pm := NewManager(t.TempDir())
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(pm.ProfileDir("skyrimse", "Linked")), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, pm.ProfileDir("skyrimse", "Linked")); err != nil {
		t.Fatal(err)
	}
	if err := pm.Save(&Profile{GameID: "skyrimse", Name: "Linked"}, nil); err == nil {
		t.Fatal("Save accepted a symlinked profile directory")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("symlink destination contains %v: %v", entries, err)
	}
}

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

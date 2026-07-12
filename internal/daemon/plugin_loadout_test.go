package daemon

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/parka/gorganizer/internal/plugins"
	"github.com/parka/gorganizer/internal/profile"
)

func TestApplyProfilePluginLoadoutSeedsDataStateOnce(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "Plugins.txt"), []byte(
		"Oblivion.esm\r\n#Optional.esp\r\nMixedMaster.esm\r\n"), 0644); err != nil {
		t.Fatal(err)
	}
	pm := profile.NewManager(t.TempDir())
	spec := plugins.Spec{
		PluginsFileName: "Plugins.txt",
		DisabledPrefix:  "#",
		PreserveOrder:   true,
		SupportedExts:   []string{".esm", ".esp"},
		SeedFromData:    true,
		ImplicitMasters: []string{"Oblivion.esm"},
	}
	list := []plugins.Plugin{
		{Filename: "MixedMaster.esm", Ext: ".esm", Enabled: true},
		{Filename: "Oblivion.esm", Ext: ".esm", Enabled: true},
		{Filename: "Optional.esp", Ext: ".esp", Enabled: true},
		{Filename: "NewMod.esp", Ext: ".esp", Enabled: true},
	}
	if err := applyProfilePluginLoadout(pm, "oblivionremastered", "Default", dataDir, spec, list); err != nil {
		t.Fatal(err)
	}

	var gotNames []string
	var gotEnabled []bool
	for _, entry := range list {
		gotNames = append(gotNames, entry.Filename)
		gotEnabled = append(gotEnabled, entry.Enabled)
	}
	if want := []string{"Oblivion.esm", "Optional.esp", "MixedMaster.esm", "NewMod.esp"}; !reflect.DeepEqual(gotNames, want) {
		t.Fatalf("order = %v, want %v", gotNames, want)
	}
	if want := []bool{true, false, true, true}; !reflect.DeepEqual(gotEnabled, want) {
		t.Fatalf("enabled = %v, want %v", gotEnabled, want)
	}

	saved, err := pm.LoadPluginLoadout("oblivionremastered", "Default")
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 4 || saved[1].Filename != "Optional.esp" || saved[1].Enabled {
		t.Fatalf("saved loadout = %#v", saved)
	}

	if err := os.WriteFile(filepath.Join(dataDir, "Plugins.txt"), []byte("#Oblivion.esm\nOptional.esp\n"), 0644); err != nil {
		t.Fatal(err)
	}
	second := append([]plugins.Plugin(nil), list...)
	for i := range second {
		second[i].Enabled = true
	}
	if err := applyProfilePluginLoadout(pm, "oblivionremastered", "Default", dataDir, spec, second); err != nil {
		t.Fatal(err)
	}
	if !second[0].Enabled || second[1].Enabled {
		t.Fatalf("established profile was reseeded: %#v", second)
	}
}

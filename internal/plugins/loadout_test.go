package plugins

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/gamedef"
)

func TestApplyActivationStateDefaultsAndPinnedMasters(t *testing.T) {
	spec := Spec{ImplicitMasters: []string{"Skyrim.esm"}, PinnedPrefixes: []string{"cc"}}
	list := []Plugin{
		{Filename: "Skyrim.esm", Enabled: true},
		{Filename: "ccBGSSSE001-Fish.esm", Enabled: true},
		{Filename: "SkyUI_SE.esp", Enabled: true},
		{Filename: "NewPlugin.esp", Enabled: true},
	}
	ApplyActivationState(list, spec, map[string]bool{
		"skyrim.esm":           false,
		"ccbgssse001-fish.esm": false,
		"skyui_se.esp":         false,
	})

	if !list[0].Enabled {
		t.Error("implicit master was disabled")
	}
	if !list[1].Enabled {
		t.Error("Creation Club plugin was disabled")
	}
	if list[2].Enabled {
		t.Error("saved disabled plugin remained enabled")
	}
	if !list[3].Enabled {
		t.Error("new plugin did not default enabled")
	}
}

func TestMorrowindINIAdapterRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Morrowind.ini")
	if err := os.WriteFile(path, []byte("[General]\r\nFoo=Bar\r\n[Game Files]\r\nGameFile0=Old.esp\r\n"), 0644); err != nil {
		t.Fatal(err)
	}
	spec := Spec{
		PluginsFileName: "Morrowind.ini", StateLocation: gamedef.PluginStateGameRootIni,
		PreserveOrder: true, SupportedExts: []string{".esm", ".esp"},
	}
	plugins := []Plugin{
		{Filename: "Morrowind.esm", Ext: ".esm", Enabled: true},
		{Filename: "Enabled.esp", Ext: ".esp", Enabled: true},
		{Filename: "Disabled.esp", Ext: ".esp", Enabled: false},
	}
	if err := Write(spec, dir, plugins); err != nil {
		t.Fatal(err)
	}
	loadout, err := ReadEngineLoadout(spec, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loadout) != 2 || loadout[0].Filename != "Morrowind.esm" || loadout[1].Filename != "Enabled.esp" {
		t.Fatalf("unexpected Morrowind loadout: %+v", loadout)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Foo=Bar") || strings.Contains(string(data), "Old.esp") || strings.Contains(string(data), "Disabled.esp") {
		t.Fatalf("Morrowind.ini was not patched surgically:\n%s", data)
	}
}

func TestOblivionRemasteredStateFormatPreservesMixedOrder(t *testing.T) {
	dir := t.TempDir()
	spec := Spec{
		PluginsFileName:   "Plugins.txt",
		LoadOrderFileName: "loadorder.txt",
		DisabledPrefix:    "#",
		PreserveOrder:     true,
		ImplicitMasters:   []string{"Oblivion.esm"},
	}
	list := []Plugin{
		{Filename: "First.esp", Ext: ".esp", Enabled: true},
		{Filename: "LateMaster.esm", Ext: ".esm", Enabled: false},
		{Filename: "Oblivion.esm", Ext: ".esm", Enabled: true},
	}
	if err := Write(spec, dir, list); err != nil {
		t.Fatal(err)
	}
	pluginsText, err := os.ReadFile(filepath.Join(dir, "Plugins.txt"))
	if err != nil {
		t.Fatal(err)
	}
	wantPlugins := "First.esp\r\n#LateMaster.esm\r\nOblivion.esm\r\n"
	if string(pluginsText) != wantPlugins {
		t.Fatalf("Plugins.txt = %q, want %q", pluginsText, wantPlugins)
	}
	loadOrder, err := os.ReadFile(filepath.Join(dir, "loadorder.txt"))
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := "First.esp\r\nLateMaster.esm\r\nOblivion.esm\r\n"
	if string(loadOrder) != wantOrder {
		t.Fatalf("loadorder.txt = %q, want %q", loadOrder, wantOrder)
	}
}

func TestReadEngineLoadoutDisabledPrefix(t *testing.T) {
	dir := t.TempDir()
	content := "\ufeffOblivion.esm\r\n#Optional.esp\r\n# not a plugin comment\r\nIgnored.esl\r\n"
	if err := os.WriteFile(filepath.Join(dir, "Plugins.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	spec := Spec{
		PluginsFileName: "Plugins.txt",
		DisabledPrefix:  "#",
		SupportedExts:   []string{".esm", ".esp"},
	}
	got, err := ReadEngineLoadout(spec, dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []EngineLoadoutEntry{
		{Filename: "Oblivion.esm", Enabled: true},
		{Filename: "Optional.esp", Enabled: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loadout = %#v, want %#v", got, want)
	}
}

func TestDiscoverPluginsHonorsSupportedExtensionsAndMixedOrder(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"A.esp", "B.esl", "C.esm"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	spec := gamedef.PluginSpec{SupportedExts: []string{".esm", ".esp"}, PreserveOrder: true}
	got, err := DiscoverPlugins(dir, nil, spec)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, plugin := range got {
		names = append(names, plugin.Filename)
	}
	want := []string{"A.esp", "C.esm"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("plugins = %v, want %v", names, want)
	}
}

func TestApplyDefaultOrderDoesNotGroupByPluginType(t *testing.T) {
	list := []Plugin{
		{Filename: "Unknown.esm", Ext: ".esm"},
		{Filename: "AltarESPMain.esp", Ext: ".esp"},
		{Filename: "Oblivion.esm", Ext: ".esm"},
		{Filename: "DLCBattlehornCastle.esp", Ext: ".esp"},
	}
	spec := Spec{
		ImplicitMasters:   []string{"Oblivion.esm"},
		CanonicalDLCOrder: []string{"DLCBattlehornCastle.esp", "AltarESPMain.esp"},
	}
	ApplyDefaultOrder(list, spec)
	var got []string
	for _, plugin := range list {
		got = append(got, plugin.Filename)
	}
	want := []string{"Oblivion.esm", "DLCBattlehornCastle.esp", "AltarESPMain.esp", "Unknown.esm"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

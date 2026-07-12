package gamedef

import (
	"reflect"
	"testing"
)

var legacyModsDirNames = map[string]string{
	"morrowind":          "Morrowind_Mods",
	"oblivion":           "Oblivion_Mods",
	"skyrim":             "Skyrim_Mods",
	"skyrimse":           "SkyrimSE_Mods",
	"fallout3":           "Fallout3_Mods",
	"falloutnv":          "FalloutNV_Mods",
	"fallout4":           "Fallout4_Mods",
	"starfield":          "Starfield_Mods",
	"oblivionremastered": "OblivionRemastered_Mods",
	"ttw":                "TTW_Mods",
}

var legacyPluginSpecs = map[string]PluginSpec{
	"morrowind": {
		PluginsFileName: "Morrowind.ini",
		StateLocation:   PluginStateGameRootIni,
		PreserveOrder:   true,
		SupportedExts:   []string{".esm", ".esp"},
		ImplicitMasters: []string{"Morrowind.esm"},
	},
	"oblivion": {
		AppDataSubdir:   "Oblivion",
		PluginsFileName: "plugins.txt",
		StarPrefix:      false,
		ImplicitMasters: []string{"Oblivion.esm"},
	},
	"skyrim": {
		AppDataSubdir:   "Skyrim",
		PluginsFileName: "plugins.txt",
		StarPrefix:      false,
		ImplicitMasters: []string{"Skyrim.esm", "Update.esm"},
	},
	"skyrimse": {
		AppDataSubdir:     "Skyrim Special Edition",
		PluginsFileName:   "plugins.txt",
		LoadOrderFileName: "loadorder.txt",
		StarPrefix:        true,
		ImplicitMasters: []string{
			"Skyrim.esm", "Update.esm", "Dawnguard.esm",
			"HearthFires.esm", "Dragonborn.esm",
		},
		PinnedPrefixes: []string{"cc"},
	},
	"fallout3": {
		AppDataSubdir:   "Fallout3",
		PluginsFileName: "plugins.txt",
		DLCListFileName: "DLCList.txt",
		StarPrefix:      false,
		ImplicitMasters: []string{"Fallout3.esm"},
		CanonicalDLCOrder: []string{
			"Anchorage.esm",
			"ThePitt.esm",
			"BrokenSteel.esm",
			"PointLookout.esm",
			"Zeta.esm",
		},
	},
	"falloutnv": {
		AppDataSubdir:   "FalloutNV",
		PluginsFileName: "plugins.txt",
		DLCListFileName: "NVDLCList.txt",
		StarPrefix:      false,
		ImplicitMasters: []string{"FalloutNV.esm"},
		CanonicalDLCOrder: []string{
			"DeadMoney.esm",
			"HonestHearts.esm",
			"OldWorldBlues.esm",
			"LonesomeRoad.esm",
			"GunRunnersArsenal.esm",
			"ClassicPack.esm",
			"MercenaryPack.esm",
			"TribalPack.esm",
			"CaravanPack.esm",
		},
	},
	"ttw": {
		AppDataSubdir:   "FalloutNV",
		PluginsFileName: "plugins.txt",
		DLCListFileName: "NVDLCList.txt",
		StarPrefix:      false,
		ImplicitMasters: []string{"FalloutNV.esm"},
		CanonicalDLCOrder: []string{
			"DeadMoney.esm",
			"HonestHearts.esm",
			"OldWorldBlues.esm",
			"LonesomeRoad.esm",
			"GunRunnersArsenal.esm",
			"ClassicPack.esm",
			"MercenaryPack.esm",
			"TribalPack.esm",
			"CaravanPack.esm",
			"Fallout3.esm",
			"Anchorage.esm",
			"ThePitt.esm",
			"BrokenSteel.esm",
			"PointLookout.esm",
			"Zeta.esm",
			"TaleOfTwoWastelands.esm",
		},
	},
	"fallout4": {
		AppDataSubdir:     "Fallout4",
		PluginsFileName:   "plugins.txt",
		LoadOrderFileName: "loadorder.txt",
		StarPrefix:        true,
		ImplicitMasters: []string{
			"Fallout4.esm", "DLCRobot.esm", "DLCworkshop01.esm",
			"DLCCoast.esm", "DLCworkshop02.esm", "DLCworkshop03.esm",
			"DLCNukaWorld.esm",
		},
	},
	"starfield": {
		AppDataSubdir:    "Starfield",
		PluginsFileName:  "Plugins.txt",
		StarPrefix:       true,
		OrderFromPlugins: true,
		ImplicitMasters: []string{
			"Starfield.esm", "Constellation.esm", "OldMars.esm",
			"SFBGS003.esm", "SFBGS006.esm", "SFBGS007.esm", "SFBGS008.esm",
		},
	},
	"oblivionremastered": {
		PluginsFileName:   "Plugins.txt",
		LoadOrderFileName: "loadorder.txt",
		StateLocation:     PluginStateDataDir,
		DisabledPrefix:    "#",
		PreserveOrder:     true,
		SupportedExts:     []string{".esm", ".esp"},
		SeedFromData:      true,
		ImplicitMasters:   []string{"Oblivion.esm"},
		CanonicalDLCOrder: []string{
			"DLCBattlehornCastle.esp",
			"DLCFrostcrag.esp",
			"DLCHorseArmor.esp",
			"DLCMehrunesRazor.esp",
			"DLCOrrery.esp",
			"DLCShiveringIsles.esp",
			"DLCSpellTomes.esp",
			"DLCThievesDen.esp",
			"DLCVileLair.esp",
			"Knights.esp",
			"AltarESPMain.esp",
			"AltarDeluxe.esp",
			"AltarESPLocal.esp",
		},
		DefaultDisabled: []string{"AltarGymNavigation.esp", "TamrielLeveledRegion.esp"},
	},
}

var legacyIniSpecs = map[string]IniSpec{
	"morrowind": {
		MyGamesSubdir:   "Morrowind",
		Files:           []string{"Morrowind.ini"},
		PrimaryIni:      "Morrowind.ini",
		CustomIni:       "",
		NativeCustomIni: false,
	},
	"oblivion": {
		MyGamesSubdir:   "Oblivion",
		Files:           []string{"Oblivion.ini", "OblivionCustom.ini", "Plugins.txt"},
		PrimaryIni:      "Oblivion.ini",
		CustomIni:       "OblivionCustom.ini",
		NativeCustomIni: false,
		TweakSet:        "oblivion",
	},
	"skyrim": {
		MyGamesSubdir:   "Skyrim",
		Files:           []string{"Skyrim.ini", "SkyrimCustom.ini", "SkyrimPrefs.ini"},
		PrimaryIni:      "Skyrim.ini",
		CustomIni:       "SkyrimCustom.ini",
		NativeCustomIni: false,
		TweakSet:        "skyrimle",
	},
	"skyrimse": {
		MyGamesSubdir:   "Skyrim Special Edition",
		Files:           []string{"Skyrim.ini", "SkyrimCustom.ini", "SkyrimPrefs.ini"},
		PrimaryIni:      "Skyrim.ini",
		CustomIni:       "SkyrimCustom.ini",
		NativeCustomIni: true,
		TweakSet:        "skyrimse",
	},
	"fallout3": {
		MyGamesSubdir:   "Fallout3",
		Files:           []string{"Fallout.ini", "FalloutCustom.ini", "FalloutPrefs.ini"},
		PrimaryIni:      "Fallout.ini",
		CustomIni:       "FalloutCustom.ini",
		NativeCustomIni: false,
		TweakSet:        "fallout",
	},
	"falloutnv": {
		MyGamesSubdir:   "FalloutNV",
		Files:           []string{"Fallout.ini", "FalloutCustom.ini", "FalloutPrefs.ini"},
		PrimaryIni:      "Fallout.ini",
		CustomIni:       "FalloutCustom.ini",
		NativeCustomIni: false,
		TweakSet:        "fallout",
	},
	"ttw": {
		MyGamesSubdir:   "FalloutNV",
		Files:           []string{"Fallout.ini", "FalloutCustom.ini", "FalloutPrefs.ini"},
		PrimaryIni:      "Fallout.ini",
		CustomIni:       "FalloutCustom.ini",
		NativeCustomIni: false,
	},
	"fallout4": {
		MyGamesSubdir:   "Fallout4",
		Files:           []string{"Fallout4.ini", "Fallout4Custom.ini", "Fallout4Prefs.ini"},
		PrimaryIni:      "Fallout4.ini",
		CustomIni:       "Fallout4Custom.ini",
		NativeCustomIni: true,
	},
	"starfield": {
		MyGamesSubdir:   "Starfield",
		Files:           []string{"Starfield.ini", "StarfieldCustom.ini", "StarfieldPrefs.ini"},
		PrimaryIni:      "Starfield.ini",
		CustomIni:       "StarfieldCustom.ini",
		NativeCustomIni: true,
	},
	"oblivionremastered": {
		MyGamesSubdir:   "Oblivion Remastered/Saved/Config/Windows",
		SaveSubdir:      "Oblivion Remastered/Saved/SaveGames",
		Files:           []string{"Altar.ini"},
		PrimaryIni:      "Altar.ini",
		NativeCustomIni: false,
	},
}

var legacyScriptExtenderToolIDs = map[string]string{
	"skyrimse":           "skse64",
	"skyrim":             "skse",
	"falloutnv":          "xnvse",
	"ttw":                "xnvse",
	"fallout3":           "fose",
	"fallout4":           "f4se",
	"oblivion":           "obse",
	"starfield":          "sfse",
	"oblivionremastered": "obse64",
}

var legacyScriptExtenderSources = map[string]ScriptExtenderSource{
	"fallout3": {
		Name: "FOSE", GameSlug: "fallout3", ModID: 8606,
		LoaderExe: "fose_loader.exe", DataSubdirs: []string{"FOSE"},
	},
	"falloutnv": {
		Name:        "xNVSE",
		GitHubRepo:  "xNVSE/NVSE",
		AssetSuffix: ".7z",
		LoaderExe:   "nvse_loader.exe",
		DataSubdirs: []string{"NVSE"},
	},
	"ttw": {
		Name:        "xNVSE",
		GitHubRepo:  "xNVSE/NVSE",
		AssetSuffix: ".7z",
		LoaderExe:   "nvse_loader.exe",
		DataSubdirs: []string{"NVSE"},
	},
	"skyrimse": {
		Name: "SKSE64", GameSlug: "skyrimspecialedition", ModID: 30379,
		LoaderExe: "skse64_loader.exe", DataSubdirs: []string{"SKSE"},
	},
	"fallout4": {
		Name: "F4SE", GameSlug: "fallout4", ModID: 42147,
		LoaderExe: "f4se_loader.exe", DataSubdirs: []string{"F4SE"},
	},
	"oblivionremastered": {
		Name: "OBSE64", GameSlug: "oblivionremastered", ModID: 282,
		LoaderExe:      "obse64_loader.exe",
		InstallSubpath: "OblivionRemastered/Binaries/Win64",
	},
}

var legacyRedistPackages = map[string][]string{
	"falloutnv":          {"vcrun2022", "d3dx9", "xact"},
	"fallout3":           {"vcrun2022", "d3dx9", "xact"},
	"oblivion":           {"vcrun2022", "d3dx9", "xact"},
	"skyrim":             {"vcrun2022", "d3dx9"},
	"skyrimse":           {"vcrun2022"},
	"fallout4":           {"vcrun2022"},
	"starfield":          {"vcrun2022"},
	"oblivionremastered": {"vcrun2022"},
}

var legacy4GBGames = map[string]bool{
	"falloutnv": true,
	"ttw":       true,
}

func TestAllCount(t *testing.T) {
	if len(All) != 10 {
		t.Fatalf("expected 10 games in All, got %d", len(All))
	}
	for id := range legacyModsDirNames {
		if _, ok := ByID(id); !ok {
			t.Errorf("game %q missing from registry", id)
		}
	}
}

func TestModsDirNamesMatchLegacy(t *testing.T) {
	for _, g := range All {
		want, ok := legacyModsDirNames[g.ID]
		if !ok {
			t.Errorf("game %q has no legacy mods-dir name", g.ID)
			continue
		}
		if g.ModsDirName != want {
			t.Errorf("game %q ModsDirName = %q, want %q", g.ID, g.ModsDirName, want)
		}
	}
}

func TestPluginSpecsMatchLegacy(t *testing.T) {
	for _, g := range All {
		want, ok := legacyPluginSpecs[g.ID]
		if !ok {
			if g.Plugins != nil {
				t.Errorf("game %q has a plugin spec but the legacy map had none", g.ID)
			}
			continue
		}
		if g.Plugins == nil {
			t.Errorf("game %q lost its plugin spec", g.ID)
			continue
		}
		if !reflect.DeepEqual(*g.Plugins, want) {
			t.Errorf("game %q plugin spec drifted:\n got  %+v\n want %+v", g.ID, *g.Plugins, want)
		}
	}
	for id := range legacyPluginSpecs {
		if _, ok := ByID(id); !ok {
			t.Errorf("legacy plugin spec game %q missing from registry", id)
		}
	}
}

func TestIniSpecsMatchLegacy(t *testing.T) {
	for _, g := range All {
		want, ok := legacyIniSpecs[g.ID]
		if !ok {
			if g.Ini != nil {
				t.Errorf("game %q has an ini spec but the legacy map had none", g.ID)
			}
			continue
		}
		if g.Ini == nil {
			t.Errorf("game %q lost its ini spec", g.ID)
			continue
		}
		if !reflect.DeepEqual(*g.Ini, want) {
			t.Errorf("game %q ini spec drifted:\n got  %+v\n want %+v", g.ID, *g.Ini, want)
		}
	}
	for id := range legacyIniSpecs {
		if _, ok := ByID(id); !ok {
			t.Errorf("legacy ini spec game %q missing from registry", id)
		}
	}
}

func TestScriptExtenderToolIDsMatchLegacy(t *testing.T) {
	for _, g := range All {
		if want := legacyScriptExtenderToolIDs[g.ID]; g.ScriptExtenderToolID != want {
			t.Errorf("game %q ScriptExtenderToolID = %q, want %q", g.ID, g.ScriptExtenderToolID, want)
		}
	}
}

func TestScriptExtenderSourcesMatchLegacy(t *testing.T) {
	for _, g := range All {
		want, ok := legacyScriptExtenderSources[g.ID]
		if !ok {
			if g.ScriptExtenderSource != nil {
				t.Errorf("game %q has an extender source but the legacy map had none", g.ID)
			}
			continue
		}
		if g.ScriptExtenderSource == nil {
			t.Errorf("game %q lost its extender source", g.ID)
			continue
		}
		if !reflect.DeepEqual(*g.ScriptExtenderSource, want) {
			t.Errorf("game %q extender source drifted:\n got  %+v\n want %+v", g.ID, *g.ScriptExtenderSource, want)
		}
	}
	for id := range legacyScriptExtenderSources {
		if _, ok := ByID(id); !ok {
			t.Errorf("legacy extender source game %q missing from registry", id)
		}
	}
}

func TestRedistPackagesMatchLegacy(t *testing.T) {
	for _, g := range All {
		want := legacyRedistPackages[g.ID]
		if !reflect.DeepEqual(g.RedistPackages, want) {
			t.Errorf("game %q RedistPackages = %v, want %v", g.ID, g.RedistPackages, want)
		}
	}
}

func Test4GBFlagsMatchLegacy(t *testing.T) {
	for _, g := range All {
		if g.Supports4GBPatch != legacy4GBGames[g.ID] {
			t.Errorf("game %q Supports4GBPatch = %v, want %v", g.ID, g.Supports4GBPatch, legacy4GBGames[g.ID])
		}
	}
}

func TestLookups(t *testing.T) {
	g, ok := ByID("skyrimse")
	if !ok || g.SteamAppID != 489830 {
		t.Fatalf("ByID(skyrimse) = %+v, %v", g, ok)
	}
	if _, ok := ByID("nonexistent"); ok {
		t.Error("ByID(nonexistent) should return false")
	}
	g, ok = ByAppID(22380)
	if !ok || g.ID != "falloutnv" {
		t.Fatalf("ByAppID(22380) = %+v, %v", g, ok)
	}
	if _, ok := ByAppID(0); ok {
		t.Error("ByAppID(0) must not return the synthetic TTW entry")
	}
	g, ok = BySlug("newvegas")
	if !ok || g.ID != "falloutnv" {
		t.Fatalf("BySlug(newvegas) = %+v, %v (synthetic entries must not own slugs)", g, ok)
	}
	if _, ok := BySlug("nonexistent"); ok {
		t.Error("BySlug(nonexistent) should return false")
	}
}

func TestOblivionRemasteredLayout(t *testing.T) {
	g, ok := ByID("oblivionremastered")
	if !ok {
		t.Fatal("oblivionremastered missing from registry")
	}
	if g.SteamAppID != 2623190 {
		t.Errorf("SteamAppID = %d, want 2623190", g.SteamAppID)
	}
	if g.DataSubpath != "OblivionRemastered/Content/Dev/ObvData/Data" {
		t.Errorf("DataSubpath = %q", g.DataSubpath)
	}
	if len(g.ExecutablePaths) != 2 {
		t.Fatalf("ExecutablePaths = %v, want wrapper and shipping executables", g.ExecutablePaths)
	}
	if g.Plugins == nil || g.Plugins.StateLocation != PluginStateDataDir ||
		g.Plugins.PluginsFileName != "Plugins.txt" || g.Plugins.DisabledPrefix != "#" ||
		!g.Plugins.PreserveOrder || !g.Plugins.SeedFromData {
		t.Fatalf("unexpected plugin semantics: %+v", g.Plugins)
	}
	if !reflect.DeepEqual(g.Plugins.SupportedExts, []string{".esm", ".esp"}) {
		t.Errorf("SupportedExts = %v", g.Plugins.SupportedExts)
	}
	if g.Ini == nil || g.Ini.MyGamesSubdir != "Oblivion Remastered/Saved/Config/Windows" ||
		g.Ini.SaveSubdir != "Oblivion Remastered/Saved/SaveGames" {
		t.Fatalf("unexpected INI/save semantics: %+v", g.Ini)
	}
	if g.ScriptExtenderSource == nil ||
		g.ScriptExtenderSource.InstallSubpath != "OblivionRemastered/Binaries/Win64" {
		t.Fatalf("unexpected OBSE64 install layout: %+v", g.ScriptExtenderSource)
	}
}

func TestMorrowindUsesDataFiles(t *testing.T) {
	g, ok := ByID("morrowind")
	if !ok {
		t.Fatal("morrowind missing from registry")
	}
	if g.DataSubpath != "Data Files" {
		t.Errorf("DataSubpath = %q, want Data Files", g.DataSubpath)
	}
}

package gamedef

var All = []Definition{
	{
		ID: "morrowind", Name: "The Elder Scrolls III: Morrowind", SteamAppID: 22320,
		DataSubpath:       "Data Files",
		ExecutablePaths:   []string{"Morrowind.exe", "Morrowind Launcher.exe"},
		RequiredDataFiles: []string{"Morrowind.esm"},
		NxmSlug:           "morrowind",
		ModsDirName:       "Morrowind_Mods",
		Plugins: &PluginSpec{
			PluginsFileName: "Morrowind.ini",
			StateLocation:   PluginStateGameRootIni,
			PreserveOrder:   true,
			SupportedExts:   []string{".esm", ".esp"},
			ImplicitMasters: []string{"Morrowind.esm"},
		},
		Ini: &IniSpec{
			MyGamesSubdir:   "Morrowind",
			Files:           []string{"Morrowind.ini"},
			PrimaryIni:      "Morrowind.ini",
			CustomIni:       "",
			NativeCustomIni: false,
		},
	},
	{
		ID: "oblivion", Name: "The Elder Scrolls IV: Oblivion", SteamAppID: 22330,
		DataSubpath:       "Data",
		ExecutablePaths:   []string{"Oblivion.exe", "OblivionLauncher.exe"},
		RequiredDataFiles: []string{"Oblivion.esm"},
		NxmSlug:           "oblivion",
		ModsDirName:       "Oblivion_Mods",
		Plugins: &PluginSpec{
			AppDataSubdir:   "Oblivion",
			PluginsFileName: "plugins.txt",
			StarPrefix:      false,
			ImplicitMasters: []string{"Oblivion.esm"},
		},
		Ini: &IniSpec{
			MyGamesSubdir:   "Oblivion",
			Files:           []string{"Oblivion.ini", "OblivionCustom.ini", "Plugins.txt"},
			PrimaryIni:      "Oblivion.ini",
			CustomIni:       "OblivionCustom.ini",
			NativeCustomIni: false,
			TweakSet:        "oblivion",
		},
		ScriptExtenderToolID: "obse",
		RedistPackages:       []string{"vcrun2022", "d3dx9", "xact"},
	},
	{
		ID: "skyrim", Name: "The Elder Scrolls V: Skyrim", SteamAppID: 72850,
		DataSubpath:       "Data",
		ExecutablePaths:   []string{"TESV.exe", "SkyrimLauncher.exe"},
		RequiredDataFiles: []string{"Skyrim.esm"},
		NxmSlug:           "skyrim",
		ModsDirName:       "Skyrim_Mods",
		Plugins: &PluginSpec{
			AppDataSubdir:   "Skyrim",
			PluginsFileName: "plugins.txt",
			StarPrefix:      false,
			ImplicitMasters: []string{"Skyrim.esm", "Update.esm"},
		},
		Ini: &IniSpec{
			MyGamesSubdir:   "Skyrim",
			Files:           []string{"Skyrim.ini", "SkyrimCustom.ini", "SkyrimPrefs.ini"},
			PrimaryIni:      "Skyrim.ini",
			CustomIni:       "SkyrimCustom.ini",
			NativeCustomIni: false,
			TweakSet:        "skyrimle",
		},
		ScriptExtenderToolID: "skse",
		RedistPackages:       []string{"vcrun2022", "d3dx9"},
	},
	{
		ID: "skyrimse", Name: "The Elder Scrolls V: Skyrim Special Edition", SteamAppID: 489830,
		DataSubpath:       "Data",
		ExecutablePaths:   []string{"SkyrimSE.exe", "SkyrimSELauncher.exe"},
		RequiredDataFiles: []string{"Skyrim.esm"},
		NxmSlug:           "skyrimspecialedition",
		ModsDirName:       "SkyrimSE_Mods",
		Plugins: &PluginSpec{
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
		Ini: &IniSpec{
			MyGamesSubdir:   "Skyrim Special Edition",
			Files:           []string{"Skyrim.ini", "SkyrimCustom.ini", "SkyrimPrefs.ini"},
			PrimaryIni:      "Skyrim.ini",
			CustomIni:       "SkyrimCustom.ini",
			NativeCustomIni: true,
			TweakSet:        "skyrimse",
		},
		ScriptExtenderToolID: "skse64",
		ScriptExtenderSource: &ScriptExtenderSource{
			Name: "SKSE64", GameSlug: "skyrimspecialedition", ModID: 30379,
			LoaderExe: "skse64_loader.exe", DataSubdirs: []string{"SKSE"},
		},
		RedistPackages: []string{"vcrun2022"},
	},
	{
		ID: "fallout3", Name: "Fallout 3", SteamAppID: 22370,
		DataSubpath:       "Data",
		ExecutablePaths:   []string{"Fallout3.exe", "FalloutLauncher.exe"},
		RequiredDataFiles: []string{"Fallout3.esm"},
		NxmSlug:           "fallout3",
		ModsDirName:       "Fallout3_Mods",
		Plugins: &PluginSpec{
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
		Ini: &IniSpec{
			MyGamesSubdir:   "Fallout3",
			Files:           []string{"Fallout.ini", "FalloutCustom.ini", "FalloutPrefs.ini"},
			PrimaryIni:      "Fallout.ini",
			CustomIni:       "FalloutCustom.ini",
			NativeCustomIni: false,
			TweakSet:        "fallout",
		},
		ScriptExtenderToolID: "fose",
		ScriptExtenderSource: &ScriptExtenderSource{
			Name: "FOSE", GameSlug: "fallout3", ModID: 8606,
			LoaderExe: "fose_loader.exe", DataSubdirs: []string{"FOSE"},
		},
		RedistPackages: []string{"vcrun2022", "d3dx9", "xact"},
	},
	{
		ID: "falloutnv", Name: "Fallout: New Vegas", SteamAppID: 22380,
		DataSubpath:       "Data",
		ExecutablePaths:   []string{"FalloutNV.exe", "FalloutNVLauncher.exe"},
		RequiredDataFiles: []string{"FalloutNV.esm"},
		NxmSlug:           "newvegas",
		ModsDirName:       "FalloutNV_Mods",
		Plugins: &PluginSpec{
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
		Ini: &IniSpec{
			MyGamesSubdir:   "FalloutNV",
			Files:           []string{"Fallout.ini", "FalloutCustom.ini", "FalloutPrefs.ini"},
			PrimaryIni:      "Fallout.ini",
			CustomIni:       "FalloutCustom.ini",
			NativeCustomIni: false,
			TweakSet:        "fallout",
		},
		ScriptExtenderToolID: "xnvse",
		ScriptExtenderSource: &ScriptExtenderSource{
			Name:        "xNVSE",
			GitHubRepo:  "xNVSE/NVSE",
			AssetSuffix: ".7z",
			LoaderExe:   "nvse_loader.exe",
			DataSubdirs: []string{"NVSE"},
		},
		RedistPackages:   []string{"vcrun2022", "d3dx9", "xact"},
		Supports4GBPatch: true,
	},
	{
		ID: "fallout4", Name: "Fallout 4", SteamAppID: 377160,
		DataSubpath:       "Data",
		ExecutablePaths:   []string{"Fallout4.exe", "Fallout4Launcher.exe"},
		RequiredDataFiles: []string{"Fallout4.esm"},
		NxmSlug:           "fallout4",
		ModsDirName:       "Fallout4_Mods",
		Plugins: &PluginSpec{
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
		Ini: &IniSpec{
			MyGamesSubdir:   "Fallout4",
			Files:           []string{"Fallout4.ini", "Fallout4Custom.ini", "Fallout4Prefs.ini"},
			PrimaryIni:      "Fallout4.ini",
			CustomIni:       "Fallout4Custom.ini",
			NativeCustomIni: true,
		},
		ScriptExtenderToolID: "f4se",
		ScriptExtenderSource: &ScriptExtenderSource{
			Name: "F4SE", GameSlug: "fallout4", ModID: 42147,
			LoaderExe: "f4se_loader.exe", DataSubdirs: []string{"F4SE"},
		},
		RedistPackages: []string{"vcrun2022"},
	},
	{
		ID: "starfield", Name: "Starfield", SteamAppID: 1716740,
		DataSubpath:       "Data",
		ExecutablePaths:   []string{"Starfield.exe"},
		RequiredDataFiles: []string{"Starfield.esm"},
		NxmSlug:           "starfield",
		ModsDirName:       "Starfield_Mods",
		Plugins: &PluginSpec{
			AppDataSubdir:    "Starfield",
			PluginsFileName:  "Plugins.txt",
			StarPrefix:       true,
			OrderFromPlugins: true,
			ImplicitMasters: []string{
				"Starfield.esm", "Constellation.esm", "OldMars.esm",
				"SFBGS003.esm", "SFBGS006.esm", "SFBGS007.esm", "SFBGS008.esm",
			},
		},
		Ini: &IniSpec{
			MyGamesSubdir:   "Starfield",
			Files:           []string{"Starfield.ini", "StarfieldCustom.ini", "StarfieldPrefs.ini"},
			PrimaryIni:      "Starfield.ini",
			CustomIni:       "StarfieldCustom.ini",
			NativeCustomIni: true,
		},
		ScriptExtenderToolID: "sfse",
		RedistPackages:       []string{"vcrun2022"},
	},
	{
		ID: "oblivionremastered", Name: "The Elder Scrolls IV: Oblivion Remastered", SteamAppID: 2623190,
		DataSubpath: "OblivionRemastered/Content/Dev/ObvData/Data",
		ExecutablePaths: []string{
			"OblivionRemastered.exe",
			"OblivionRemastered/Binaries/Win64/OblivionRemastered-Win64-Shipping.exe",
		},
		RequiredDataFiles: []string{"Oblivion.esm"},
		NxmSlug:           "oblivionremastered",
		ModsDirName:       "OblivionRemastered_Mods",
		Plugins: &PluginSpec{
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
		Ini: &IniSpec{
			MyGamesSubdir:   "Oblivion Remastered/Saved/Config/Windows",
			SaveSubdir:      "Oblivion Remastered/Saved/SaveGames",
			Files:           []string{"Altar.ini"},
			PrimaryIni:      "Altar.ini",
			NativeCustomIni: false,
		},
		ScriptExtenderToolID: "obse64",
		ScriptExtenderSource: &ScriptExtenderSource{
			Name: "OBSE64", GameSlug: "oblivionremastered", ModID: 282,
			LoaderExe:      "obse64_loader.exe",
			InstallSubpath: "OblivionRemastered/Binaries/Win64",
		},
		RedistPackages: []string{"vcrun2022"},
	},
	{
		ID: "ttw", Name: "Tale of Two Wastelands", SteamAppID: 0,
		DataSubpath: "Data", NxmSlug: "newvegas",
		Synthetic: true, ParentGameID: "falloutnv",
		Requires:    []string{"fallout3", "falloutnv"},
		ModsDirName: "TTW_Mods",
		Plugins: &PluginSpec{
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
		Ini: &IniSpec{
			MyGamesSubdir:   "FalloutNV",
			Files:           []string{"Fallout.ini", "FalloutCustom.ini", "FalloutPrefs.ini"},
			PrimaryIni:      "Fallout.ini",
			CustomIni:       "FalloutCustom.ini",
			NativeCustomIni: false,
		},
		ScriptExtenderToolID: "xnvse",
		ScriptExtenderSource: &ScriptExtenderSource{
			Name:        "xNVSE",
			GitHubRepo:  "xNVSE/NVSE",
			AssetSuffix: ".7z",
			LoaderExe:   "nvse_loader.exe",
			DataSubdirs: []string{"NVSE"},
		},
		Supports4GBPatch: true,
	},
}

var (
	byID    map[string]Definition
	byAppID map[uint32]Definition
	bySlug  map[string]Definition
)

func init() {
	byID = make(map[string]Definition, len(All))
	byAppID = make(map[uint32]Definition, len(All))
	bySlug = make(map[string]Definition, len(All))
	for _, d := range All {
		byID[d.ID] = d
		if d.Synthetic {
			continue
		}
		byAppID[d.SteamAppID] = d
		bySlug[d.NxmSlug] = d
	}
}

// ByID returns the definition for an internal game ID, if known.
func ByID(gameID string) (Definition, bool) {
	d, ok := byID[gameID]
	return d, ok
}

// ByAppID returns the non-synthetic definition for a Steam app ID, if known.
func ByAppID(appID uint32) (Definition, bool) {
	d, ok := byAppID[appID]
	return d, ok
}

// BySlug returns the non-synthetic definition for a Nexus Mods slug, if known.
func BySlug(slug string) (Definition, bool) {
	d, ok := bySlug[slug]
	return d, ok
}

// Package ini provides per-profile INI file management for Bethesda games.
package ini

// GameIniSpec describes where a game looks for its INI files.
type GameIniSpec struct {
	MyGamesSubdir   string
	Files           []string
	PrimaryIni      string
	CustomIni       string
	NativeCustomIni bool
}

var gameIniSpecs = map[string]GameIniSpec{
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
	},
	"skyrim": {
		MyGamesSubdir:   "Skyrim",
		Files:           []string{"Skyrim.ini", "SkyrimCustom.ini", "SkyrimPrefs.ini"},
		PrimaryIni:      "Skyrim.ini",
		CustomIni:       "SkyrimCustom.ini",
		NativeCustomIni: false,
	},
	"skyrimse": {
		MyGamesSubdir:   "Skyrim Special Edition",
		Files:           []string{"Skyrim.ini", "SkyrimCustom.ini", "SkyrimPrefs.ini"},
		PrimaryIni:      "Skyrim.ini",
		CustomIni:       "SkyrimCustom.ini",
		NativeCustomIni: true,
	},
	"fallout3": {
		MyGamesSubdir:   "Fallout3",
		Files:           []string{"Fallout.ini", "FalloutCustom.ini", "FalloutPrefs.ini"},
		PrimaryIni:      "Fallout.ini",
		CustomIni:       "FalloutCustom.ini",
		NativeCustomIni: false,
	},
	"falloutnv": {
		MyGamesSubdir:   "FalloutNV",
		Files:           []string{"Fallout.ini", "FalloutCustom.ini", "FalloutPrefs.ini"},
		PrimaryIni:      "Fallout.ini",
		CustomIni:       "FalloutCustom.ini",
		NativeCustomIni: false,
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
}

// SpecFor returns the INI layout spec for a known Bethesda game.
func SpecFor(gameID string) (GameIniSpec, bool) {
	s, ok := gameIniSpecs[gameID]
	return s, ok
}

// IsINIFile returns true when the filename is a managed INI for the game.
func IsINIFile(gameID, filename string) bool {
	spec, ok := gameIniSpecs[gameID]
	if !ok {
		return false
	}
	for _, f := range spec.Files {
		if f == filename {
			return true
		}
	}
	return false
}

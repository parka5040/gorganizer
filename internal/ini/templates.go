// Package ini provides per-profile INI file management for Bethesda games.
// Each supported game has a canonical set of INI files that live in the
// user's Documents/My Games/{Subdir}/ folder; gorganizer stores a
// per-profile copy and can push it into that folder at launch time so
// profile-specific tweaks (shadows, VATS, etc.) take effect per-profile.
package ini

// GameIniSpec describes where a game looks for its INI files and which
// filenames it actually reads.
type GameIniSpec struct {
	// MyGamesSubdir is the folder name beneath "My Documents/My Games/"
	// inside the Proton pfx (or the user's ~/Documents/My Games for a
	// native install) where the game reads and writes its INI files.
	MyGamesSubdir string
	// Files is the full list of INI filenames managed per profile. Every
	// supported Bethesda title gets a {Game}Custom.ini here, mirroring MO2:
	// even when the engine doesn't read Custom.ini natively, we use it as
	// the conventional place for user tweaks and merge it into the primary
	// INI at push time.
	Files []string
	// PrimaryIni is the main INI file the engine reads (e.g. "Skyrim.ini").
	// Used as the merge target for engines without native Custom.ini
	// support. Empty when there's no distinction (Morrowind).
	PrimaryIni string
	// CustomIni is the {Game}Custom.ini filename, or "" when the game has
	// no Custom.ini convention.
	CustomIni string
	// NativeCustomIni reports whether the game's engine reads Custom.ini
	// directly. When false, daemon merges Custom.ini → PrimaryIni before
	// pushing to the game's My Games directory.
	NativeCustomIni bool
}

// gameIniSpecs maps internal gameID → spec. Filenames taken from each
// game's documented INI layout; subdir names match Bethesda's capitalization
// so path comparisons on case-sensitive filesystems don't fail.
//
// Every engine gets a {Game}Custom.ini managed per profile. The older
// engines (Oblivion, Fallout 3/NV, Skyrim LE) don't read Custom.ini
// natively — the daemon merges it into the primary INI at push time so
// the user's tweaks still land where the engine looks.
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

// SpecFor returns the INI layout spec for a game, or (zero, false) when the
// game isn't a known Bethesda title.
func SpecFor(gameID string) (GameIniSpec, bool) {
	s, ok := gameIniSpecs[gameID]
	return s, ok
}

// IsINIFile returns true when the filename is one of the managed INI files
// for the given game. Used to guard writes that come in over IPC.
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

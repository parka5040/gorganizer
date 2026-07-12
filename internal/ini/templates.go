package ini

import (
	"github.com/parka/gorganizer/internal/gamedef"
)

type GameIniSpec = gamedef.IniSpec

// SpecFor returns the INI layout spec for a known Bethesda game.
func SpecFor(gameID string) (GameIniSpec, bool) {
	g, ok := gamedef.ByID(gameID)
	if !ok || g.Ini == nil {
		return GameIniSpec{}, false
	}
	return *g.Ini, true
}

// IsINIFile returns true when the filename is a managed INI for the game.
func IsINIFile(gameID, filename string) bool {
	spec, ok := SpecFor(gameID)
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

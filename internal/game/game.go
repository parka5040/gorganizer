package game

type GameDefinition struct {
	ID                string
	Name              string
	SteamAppID        uint32
	DataSubpath       string
	ExecutablePaths   []string
	RequiredDataFiles []string
	Synthetic         bool
	ParentGameID      string
	Requires          []string
	NxmSlug           string
}

// NxmSlugForID returns the Nexus slug for a game id, or "".
func NxmSlugForID(gameID string) string {
	if g, ok := FindByID(gameID); ok {
		return g.NxmSlug
	}
	return ""
}

// GameIDForNxmSlug maps a Nexus slug to the owning (non-synthetic) game id.
func GameIDForNxmSlug(slug string) (string, bool) {
	for _, g := range KnownGames {
		if g.Synthetic {
			continue
		}
		if g.NxmSlug == slug {
			return g.ID, true
		}
	}
	return "", false
}

type DetectedGame struct {
	GameDefinition
	InstallPath string
	DataPath    string
	LibraryPath string
}

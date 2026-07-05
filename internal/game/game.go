package game

// GameDefinition describes a supported Bethesda game.
type GameDefinition struct {
	ID           string
	Name         string
	SteamAppID   uint32
	DataSubpath  string
	Synthetic    bool
	ParentGameID string
	Requires     []string
	// NxmSlug is the canonical Nexus Mods game slug. Central home for what used
	// to be a separate slug map in the download package (registry consolidation).
	NxmSlug string
}

// NxmSlugForID returns the Nexus slug for a game id, or "".
func NxmSlugForID(gameID string) string {
	if g, ok := FindByID(gameID); ok {
		return g.NxmSlug
	}
	return ""
}

// GameIDForNxmSlug maps a Nexus slug to the owning (non-synthetic) game id.
// Synthetic games (e.g. TTW) share their parent's slug, so downloads route to
// the real game.
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

// DetectedGame is a GameDefinition that was found installed on the system.
type DetectedGame struct {
	GameDefinition
	InstallPath string
	DataPath    string
	LibraryPath string
}

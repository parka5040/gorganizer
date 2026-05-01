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
}

// DetectedGame is a GameDefinition that was found installed on the system.
type DetectedGame struct {
	GameDefinition
	InstallPath string
	DataPath    string
	LibraryPath string
}

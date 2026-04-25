package game

// GameDefinition describes a supported Bethesda game.
type GameDefinition struct {
	ID          string
	Name        string
	SteamAppID  uint32
	DataSubpath string // always "Data" for Bethesda games
}

// DetectedGame is a GameDefinition that was found installed on the system.
type DetectedGame struct {
	GameDefinition
	InstallPath string // absolute path to game install directory
	DataPath    string // absolute path to Data/ subdirectory
	LibraryPath string // Steam library folder containing this game
}

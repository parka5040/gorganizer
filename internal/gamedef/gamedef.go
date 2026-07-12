package gamedef

type PluginStateLocation uint8

const (
	PluginStateAppDataLocal PluginStateLocation = iota
	PluginStateDataDir
	PluginStateGameRootIni
)

type PluginSpec struct {
	AppDataSubdir     string
	PluginsFileName   string
	LoadOrderFileName string
	DLCListFileName   string
	StarPrefix        bool
	StateLocation     PluginStateLocation
	DisabledPrefix    string
	PreserveOrder     bool
	OrderFromPlugins  bool
	SupportedExts     []string
	SeedFromData      bool
	ImplicitMasters   []string
	PinnedPrefixes    []string
	CanonicalDLCOrder []string
	DefaultDisabled   []string
}

type IniSpec struct {
	MyGamesSubdir   string
	SaveSubdir      string
	Files           []string
	PrimaryIni      string
	CustomIni       string
	NativeCustomIni bool
	TweakSet        string
}

type ScriptExtenderSource struct {
	Name           string
	LoaderExe      string
	InstallSubpath string
	DataSubdirs    []string
	GitHubRepo     string
	AssetSuffix    string
	GameSlug       string
	ModID          int
}

type Definition struct {
	ID                   string
	Name                 string
	SteamAppID           uint32
	DataSubpath          string
	ExecutablePaths      []string
	RequiredDataFiles    []string
	NxmSlug              string
	Synthetic            bool
	ParentGameID         string
	Requires             []string
	ModsDirName          string
	Plugins              *PluginSpec
	Ini                  *IniSpec
	ScriptExtenderToolID string
	ScriptExtenderSource *ScriptExtenderSource
	RedistPackages       []string
	Supports4GBPatch     bool
}

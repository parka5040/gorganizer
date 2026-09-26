package gamedef

import "strconv"

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

type InstallLayout uint8

const (
	LayoutDataRoot InstallLayout = iota
	LayoutSMAPIManifest
)

// String returns the stable snake_case name of an install layout.
func (l InstallLayout) String() string {
	switch l {
	case LayoutDataRoot:
		return "data_root"
	case LayoutSMAPIManifest:
		return "smapi_manifest"
	}
	return "layout_" + strconv.Itoa(int(l))
}

type ModLoaderKind uint8

const (
	ModLoaderNone ModLoaderKind = iota
	ModLoaderSMAPI
)

type ModLoaderSpec struct {
	Kind                ModLoaderKind
	DisplayName         string
	GitHubRepo          string
	AssetPattern        string
	InstallerRelPath    string
	PayloadRelPath      string
	LauncherName        string
	LauncherBackupName  string
	LauncherPayloadName string
	LoaderExecutable    string
	GameAssembly        string
	GameVersionFile     string
	GameVersionPackage  string
	LoaderDepsFile      string
	NativeMarkers       []string
	ForeignMarkers      []string
	BundledModIDs       []string
	UninstallPaths      []string
	ProtectedRootPaths  []string
	WebAPIBaseURL       string
	WebAPIVersion       string
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
	DataDirOptional      bool
	Layout               InstallLayout
	ModLoader            *ModLoaderSpec
}

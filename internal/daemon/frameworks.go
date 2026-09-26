package daemon

import (
	"fmt"
	"os"
	"strings"

	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/gamedef"
	"github.com/parka/gorganizer/internal/smapi"
)

const (
	manifestReasonRootManifest = "root_manifest"
	manifestReasonNoManifest   = "no_manifest"
)

type smapiLayout struct{}

// Plan maps each SMAPI manifest folder of the extracted archive to a planned top-level copy.
func (smapiLayout) Plan(extractRoot string) ([]download.PlannedCopy, error) {
	plan, err := smapi.PlanArchive(extractRoot)
	if err != nil {
		return nil, err
	}
	copies := make([]download.PlannedCopy, 0, len(plan.Folders))
	for _, folder := range plan.Folders {
		copies = append(copies, download.PlannedCopy{SourceRel: folder.SourceRel, DestName: folder.DestName})
	}
	return copies, nil
}

var layoutPlanners = map[gamedef.InstallLayout]func() download.LayoutPlanner{
	gamedef.LayoutDataRoot:      func() download.LayoutPlanner { return nil },
	gamedef.LayoutSMAPIManifest: func() download.LayoutPlanner { return smapiLayout{} },
}

var modFolderValidators = map[gamedef.InstallLayout]func(modName, modDir string) error{
	gamedef.LayoutSMAPIManifest: validateSMAPIModFolder,
}

var deployRootGuards = map[gamedef.InstallLayout]func(modDir string) string{
	gamedef.LayoutSMAPIManifest: smapiRootProblem,
}

var loaderSpecMappers = map[gamedef.ModLoaderKind]func(*gamedef.ModLoaderSpec) smapi.LoaderSpec{
	gamedef.ModLoaderSMAPI: smapiLoaderSpec,
}

var dependencyAnalyzers = map[gamedef.ModLoaderKind]bool{
	gamedef.ModLoaderSMAPI: true,
}

// dependencySpecFor returns the loader spec of a game whose mod loader has a manifest dependency analyzer, or ModDependenciesUnsupportedError.
func dependencySpecFor(gameID string) (smapi.LoaderSpec, *gamedef.ModLoaderSpec, error) {
	spec, def, ok := loaderSpecFor(gameID)
	if !ok || !dependencyAnalyzers[def.Kind] {
		return smapi.LoaderSpec{}, nil, &dto.ModDependenciesUnsupportedError{GameID: gameID}
	}
	return spec, def, nil
}

// loaderSpecFor returns the installer spec and registry row of gameID's managed mod loader, or false when it has none.
func loaderSpecFor(gameID string) (smapi.LoaderSpec, *gamedef.ModLoaderSpec, bool) {
	def, ok := gamedef.ByID(gameID)
	if !ok || def.ModLoader == nil {
		return smapi.LoaderSpec{}, nil, false
	}
	mapper, ok := loaderSpecMappers[def.ModLoader.Kind]
	if !ok || mapper == nil {
		return smapi.LoaderSpec{}, nil, false
	}
	return mapper(def.ModLoader), def.ModLoader, true
}

// smapiLoaderSpec copies a registry SMAPI row into the installer spec field by field.
func smapiLoaderSpec(spec *gamedef.ModLoaderSpec) smapi.LoaderSpec {
	return smapi.LoaderSpec{
		InstallerRelPath:    spec.InstallerRelPath,
		PayloadRelPath:      spec.PayloadRelPath,
		LauncherName:        spec.LauncherName,
		LauncherBackupName:  spec.LauncherBackupName,
		LauncherPayloadName: spec.LauncherPayloadName,
		LoaderExecutable:    spec.LoaderExecutable,
		GameAssembly:        spec.GameAssembly,
		GameVersionFile:     spec.GameVersionFile,
		GameVersionPackage:  spec.GameVersionPackage,
		LoaderDepsFile:      spec.LoaderDepsFile,
		NativeMarkers:       append([]string(nil), spec.NativeMarkers...),
		ForeignMarkers:      append([]string(nil), spec.ForeignMarkers...),
		BundledModIDs:       append([]string(nil), spec.BundledModIDs...),
		UninstallPaths:      append([]string(nil), spec.UninstallPaths...),
	}
}

// resolveLayoutPlanner returns the registered planner for gameID's layout, or LayoutUnsupportedError when none is registered.
func resolveLayoutPlanner(registry map[gamedef.InstallLayout]func() download.LayoutPlanner, gameID string) (download.LayoutPlanner, error) {
	def, ok := gamedef.ByID(gameID)
	if !ok || def.Layout == gamedef.LayoutDataRoot {
		return nil, nil
	}
	factory, ok := registry[def.Layout]
	if !ok || factory == nil {
		return nil, &download.LayoutUnsupportedError{GameID: gameID, Layout: def.Layout.String()}
	}
	return factory(), nil
}

// layoutPlannerFor returns the archive layout planner for gameID, nil meaning the Data-root layout.
func layoutPlannerFor(gameID string) download.LayoutPlanner {
	planner, _ := resolveLayoutPlanner(layoutPlanners, gameID)
	return planner
}

// checkInstallLayout refuses installs for a game whose archive layout has no registered planner.
func checkInstallLayout(gameID string) error {
	_, err := resolveLayoutPlanner(layoutPlanners, gameID)
	return err
}

// validateModFolderLayout checks a registered mod folder against the layout rules of gameID.
func validateModFolderLayout(gameID, modName, modDir string) error {
	def, ok := gamedef.ByID(gameID)
	if !ok {
		return nil
	}
	validate, ok := modFolderValidators[def.Layout]
	if !ok {
		return nil
	}
	return validate(modName, modDir)
}

// validateSMAPIModFolder requires every SMAPI manifest to sit in a folder below the mod root.
func validateSMAPIModFolder(modName, modDir string) error {
	folders, err := smapi.Scan(modDir)
	if err != nil {
		return fmt.Errorf("scanning mod %q for SMAPI manifests: %w", modName, err)
	}
	found := false
	for _, folder := range folders {
		switch {
		case folder.Kind == smapi.FolderRootManifest, folder.Kind == smapi.FolderMod && folder.RelPath == "":
			return &download.ManifestLayoutError{Mod: modName, Reason: manifestReasonRootManifest}
		case folder.Kind == smapi.FolderMod:
			found = true
		}
	}
	if !found {
		return &download.ManifestLayoutError{Mod: modName, Reason: manifestReasonNoManifest}
	}
	return nil
}

// deployRootGuardFor returns the deploy-time mod-root check of gameID's layout, or nil when the layout has none.
func deployRootGuardFor(gameID string) func(modDir string) string {
	def, ok := gamedef.ByID(gameID)
	if !ok {
		return nil
	}
	return deployRootGuards[def.Layout]
}

// smapiRootProblem reports a manifest.json sitting directly in a mod root, where SMAPI never loads it.
func smapiRootProblem(modDir string) string {
	entries, err := os.ReadDir(modDir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(entry.Name(), "manifest.json") {
			return "manifest.json at the mod root"
		}
	}
	return ""
}

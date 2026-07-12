package game

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/parka/gorganizer/internal/fsutil"
	"github.com/parka/gorganizer/internal/gamedef"
	"github.com/parka/gorganizer/internal/steam"
)

var KnownGames = knownGamesFromRegistry()

// knownGamesFromRegistry derives the identity slice from the gamedef registry.
func knownGamesFromRegistry() []GameDefinition {
	out := make([]GameDefinition, 0, len(gamedef.All))
	for _, d := range gamedef.All {
		out = append(out, GameDefinition{
			ID:                d.ID,
			Name:              d.Name,
			SteamAppID:        d.SteamAppID,
			DataSubpath:       d.DataSubpath,
			ExecutablePaths:   append([]string(nil), d.ExecutablePaths...),
			RequiredDataFiles: append([]string(nil), d.RequiredDataFiles...),
			Synthetic:         d.Synthetic,
			ParentGameID:      d.ParentGameID,
			Requires:          append([]string(nil), d.Requires...),
			NxmSlug:           d.NxmSlug,
		})
	}
	return out
}

const TTWMarkerFilename = ".gorganizer-ttw.applied"

var knownByAppID map[uint32]GameDefinition

func init() {
	knownByAppID = make(map[uint32]GameDefinition, len(KnownGames))
	for _, g := range KnownGames {
		if g.Synthetic {
			continue
		}
		knownByAppID[g.SteamAppID] = g
	}
}

// FindByAppID returns the game definition for a Steam app ID, if known.
func FindByAppID(appID uint32) (GameDefinition, bool) {
	g, ok := knownByAppID[appID]
	return g, ok
}

// FindByID returns the game definition for an internal game ID, if known.
func FindByID(gameID string) (GameDefinition, bool) {
	for _, g := range KnownGames {
		if g.ID == gameID {
			return g, true
		}
	}
	return GameDefinition{}, false
}

// DetectInstalledGames scans all Steam library folders for known games and appends synthetic entries.
func DetectInstalledGames() ([]DetectedGame, error) {
	steamRoot, err := steam.FindRoot()
	if err != nil {
		return nil, err
	}

	folders, err := findLibraryFolders(steamRoot)
	if err != nil {
		return nil, err
	}

	var detected []DetectedGame
	for _, folder := range folders {
		steamapps := filepath.Join(folder, "steamapps")
		entries, err := os.ReadDir(steamapps)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasPrefix(name, "appmanifest_") || !strings.HasSuffix(name, ".acf") {
				continue
			}
			game, err := parseAppManifest(filepath.Join(steamapps, name), folder)
			if err != nil {
				slog.Debug("skipping manifest", "file", name, "err", err)
				continue
			}
			if game != nil {
				detected = append(detected, *game)
			}
		}
	}

	sort.Slice(detected, func(i, j int) bool {
		return detected[i].SteamAppID < detected[j].SteamAppID
	})

	detected = AppendSyntheticGames(detected, nil)
	return detected, nil
}

type TTWPlayableProbe func() (fnvInstallPath string, ok bool)

// AppendSyntheticGames returns detected with synthetic TTW entries appended when installable or playable.
func AppendSyntheticGames(detected []DetectedGame, playable TTWPlayableProbe) []DetectedGame {
	has := map[string]*DetectedGame{}
	for i := range detected {
		has[detected[i].ID] = &detected[i]
	}
	if _, alreadyTTW := has["ttw"]; alreadyTTW {
		return detected
	}
	def, ok := FindByID("ttw")
	if !ok {
		return detected
	}
	fnv, fnvOk := has["falloutnv"]
	_, fo3Ok := has["fallout3"]

	switch {
	case fnvOk && fo3Ok:
		detected = append(detected, DetectedGame{
			GameDefinition: def,
			InstallPath:    fnv.InstallPath,
			DataPath:       fnv.DataPath,
			LibraryPath:    fnv.LibraryPath,
		})
	case fnvOk && playable != nil:
		if installPath, playableOk := playable(); playableOk && installPath != "" {
			detected = append(detected, DetectedGame{
				GameDefinition: def,
				InstallPath:    fnv.InstallPath,
				DataPath:       fnv.DataPath,
				LibraryPath:    fnv.LibraryPath,
			})
		}
	}
	return detected
}

// TTWInstallable reports whether both Fallout 3 and Fallout: New Vegas are in the detected slice.
func TTWInstallable(detected []DetectedGame) bool {
	has := map[string]bool{}
	for _, d := range detected {
		has[d.ID] = true
	}
	return has["fallout3"] && has["falloutnv"]
}

// HasTTWMarker checks for the .gorganizer-ttw.applied marker file in an FNV install directory.
func HasTTWMarker(fnvInstallPath string) bool {
	if fnvInstallPath == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(fnvInstallPath, TTWMarkerFilename))
	return err == nil && info.Mode().IsRegular()
}

// findLibraryFolders parses libraryfolders.vdf for Steam library paths.
func findLibraryFolders(steamRoot string) ([]string, error) {
	vdfPath := filepath.Join(steamRoot, "steamapps", "libraryfolders.vdf")
	f, err := os.Open(vdfPath)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", vdfPath, err)
	}
	defer f.Close()

	parsed, err := steam.ParseVDF(f)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", vdfPath, err)
	}

	lf, ok := parsed["libraryfolders"]
	if !ok {
		return nil, fmt.Errorf("libraryfolders key not found in %s", vdfPath)
	}
	lfMap, ok := lf.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("libraryfolders is not an object in %s", vdfPath)
	}

	var folders []string
	for _, v := range lfMap {
		entry, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		path, ok := entry["path"].(string)
		if !ok || path == "" {
			continue
		}
		if fsutil.DirExists(filepath.Join(path, "steamapps")) {
			folders = append(folders, path)
		}
	}
	return folders, nil
}

// parseAppManifest reads an appmanifest_*.acf and matches against KnownGames.
func parseAppManifest(acfPath, libraryFolder string) (*DetectedGame, error) {
	f, err := os.Open(acfPath)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", acfPath, err)
	}
	defer f.Close()

	parsed, err := steam.ParseVDF(f)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", acfPath, err)
	}

	appState, ok := parsed["AppState"]
	if !ok {
		return nil, nil
	}
	asMap, ok := appState.(map[string]interface{})
	if !ok {
		return nil, nil
	}

	appIDStr, _ := asMap["appid"].(string)
	appID, err := strconv.ParseUint(appIDStr, 10, 32)
	if err != nil || appID == 0 {
		return nil, nil
	}

	gameDef, ok := knownByAppID[uint32(appID)]
	if !ok {
		return nil, nil
	}

	installDir, _ := asMap["installdir"].(string)
	if installDir == "" {
		return nil, nil
	}

	installPath := filepath.Join(libraryFolder, "steamapps", "common", installDir)
	if !fsutil.DirExists(installPath) {
		return nil, nil
	}

	dataPath, ok := validateInstallLayout(installPath, gameDef)
	if !ok {
		return nil, nil
	}

	name := gameDef.Name
	if n, ok := asMap["name"].(string); ok && n != "" {
		name = n
	}
	detectedDef := gameDef
	detectedDef.Name = name

	return &DetectedGame{
		GameDefinition: detectedDef,
		InstallPath:    installPath,
		DataPath:       dataPath,
		LibraryPath:    libraryFolder,
	}, nil
}

// validateInstallLayout rejects incomplete or incorrectly rooted installs.
func validateInstallLayout(installPath string, def GameDefinition) (string, bool) {
	dataPath := filepath.Join(installPath, filepath.FromSlash(def.DataSubpath))
	if !fsutil.DirExists(dataPath) {
		return "", false
	}

	if len(def.ExecutablePaths) > 0 {
		found := false
		for _, rel := range def.ExecutablePaths {
			info, err := os.Stat(filepath.Join(installPath, filepath.FromSlash(rel)))
			if err == nil && info.Mode().IsRegular() {
				found = true
				break
			}
		}
		if !found {
			return "", false
		}
	}

	for _, rel := range def.RequiredDataFiles {
		info, err := os.Stat(filepath.Join(dataPath, filepath.FromSlash(rel)))
		if err != nil || !info.Mode().IsRegular() {
			return "", false
		}
	}
	return dataPath, true
}

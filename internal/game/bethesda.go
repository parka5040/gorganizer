package game

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/parka/gorganizer/internal/steam"
)

// KnownGames is the registry of supported Bethesda games; must match the C++ frontend's GameInfo::knownGames().
var KnownGames = []GameDefinition{
	{ID: "morrowind", Name: "The Elder Scrolls III: Morrowind", SteamAppID: 22320, DataSubpath: "Data"},
	{ID: "oblivion", Name: "The Elder Scrolls IV: Oblivion", SteamAppID: 22330, DataSubpath: "Data"},
	{ID: "skyrim", Name: "The Elder Scrolls V: Skyrim", SteamAppID: 72850, DataSubpath: "Data"},
	{ID: "skyrimse", Name: "The Elder Scrolls V: Skyrim Special Edition", SteamAppID: 489830, DataSubpath: "Data"},
	{ID: "fallout3", Name: "Fallout 3", SteamAppID: 22370, DataSubpath: "Data"},
	{ID: "falloutnv", Name: "Fallout: New Vegas", SteamAppID: 22380, DataSubpath: "Data"},
	{ID: "fallout4", Name: "Fallout 4", SteamAppID: 377160, DataSubpath: "Data"},
	{ID: "starfield", Name: "Starfield", SteamAppID: 1716740, DataSubpath: "Data"},
	{ID: "ttw", Name: "Tale of Two Wastelands", SteamAppID: 0, DataSubpath: "Data",
		Synthetic: true, ParentGameID: "falloutnv",
		Requires: []string{"fallout3", "falloutnv"}},
}

// TTWMarkerFilename is the sentinel file dropped in FNV's install root after a successful TTW install.
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
	steamRoot, err := FindSteamRoot()
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

// TTWPlayableProbe is an optional callback that reports whether TTW is playable from an existing install.
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

// FindSteamRoot is a back-compat shim delegating to steam.FindRoot.
func FindSteamRoot() (string, error) {
	return steam.FindRoot()
}

// findLibraryFolders parses libraryfolders.vdf for Steam library paths.
func findLibraryFolders(steamRoot string) ([]string, error) {
	vdfPath := filepath.Join(steamRoot, "steamapps", "libraryfolders.vdf")
	f, err := os.Open(vdfPath)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", vdfPath, err)
	}
	defer f.Close()

	parsed, err := ParseVDF(f)
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
		if dirExists(filepath.Join(path, "steamapps")) {
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

	parsed, err := ParseVDF(f)
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
	if !dirExists(installPath) {
		return nil, nil
	}

	dataPath := filepath.Join(installPath, gameDef.DataSubpath)
	if !dirExists(dataPath) {
		return nil, nil
	}

	name := gameDef.Name
	if n, ok := asMap["name"].(string); ok && n != "" {
		name = n
	}

	return &DetectedGame{
		GameDefinition: GameDefinition{
			ID:          gameDef.ID,
			Name:        name,
			SteamAppID:  gameDef.SteamAppID,
			DataSubpath: gameDef.DataSubpath,
		},
		InstallPath: installPath,
		DataPath:    dataPath,
		LibraryPath: libraryFolder,
	}, nil
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// ParseVDF parses Valve's VDF text format into a map structure.
func ParseVDF(r io.Reader) (map[string]interface{}, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	p := &vdfParser{input: string(data)}
	return p.parse()
}

type vdfTokenType int

const (
	vdfString vdfTokenType = iota
	vdfBraceOpen
	vdfBraceClose
	vdfEOF
)

type vdfToken struct {
	typ vdfTokenType
	val string
}

type vdfParser struct {
	input string
	pos   int
}

func (p *vdfParser) skipWhitespaceAndComments() {
	for p.pos < len(p.input) {
		c := p.input[p.pos]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			p.pos++
			continue
		}
		if p.pos+1 < len(p.input) && c == '/' && p.input[p.pos+1] == '/' {
			for p.pos < len(p.input) && p.input[p.pos] != '\n' {
				p.pos++
			}
			continue
		}
		break
	}
}

func (p *vdfParser) next() vdfToken {
	p.skipWhitespaceAndComments()

	if p.pos >= len(p.input) {
		return vdfToken{typ: vdfEOF}
	}

	c := p.input[p.pos]

	if c == '{' {
		p.pos++
		return vdfToken{typ: vdfBraceOpen}
	}
	if c == '}' {
		p.pos++
		return vdfToken{typ: vdfBraceClose}
	}
	if c == '"' {
		p.pos++
		var sb strings.Builder
		for p.pos < len(p.input) {
			ch := p.input[p.pos]
			if ch == '\\' && p.pos+1 < len(p.input) {
				escaped := p.input[p.pos+1]
				if escaped == '"' || escaped == '\\' {
					sb.WriteByte(escaped)
					p.pos += 2
					continue
				}
			}
			if ch == '"' {
				p.pos++
				return vdfToken{typ: vdfString, val: sb.String()}
			}
			sb.WriteByte(ch)
			p.pos++
		}
		return vdfToken{typ: vdfString, val: sb.String()}
	}

	var sb strings.Builder
	for p.pos < len(p.input) {
		ch := p.input[p.pos]
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == '{' || ch == '}' {
			break
		}
		sb.WriteByte(ch)
		p.pos++
	}
	return vdfToken{typ: vdfString, val: sb.String()}
}

func (p *vdfParser) parse() (map[string]interface{}, error) {
	rootKey := p.next()
	if rootKey.typ != vdfString {
		return nil, fmt.Errorf("expected root key, got token type %d", rootKey.typ)
	}

	brace := p.next()
	if brace.typ != vdfBraceOpen {
		return nil, fmt.Errorf("expected '{' after root key %q", rootKey.val)
	}

	obj, err := p.parseObject()
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{rootKey.val: obj}, nil
}

func (p *vdfParser) parseObject() (map[string]interface{}, error) {
	result := make(map[string]interface{})
	for {
		key := p.next()
		if key.typ == vdfBraceClose || key.typ == vdfEOF {
			return result, nil
		}
		if key.typ != vdfString {
			return nil, fmt.Errorf("expected string key, got token type %d", key.typ)
		}

		valueOrBrace := p.next()
		switch valueOrBrace.typ {
		case vdfBraceOpen:
			sub, err := p.parseObject()
			if err != nil {
				return nil, err
			}
			result[key.val] = sub
		case vdfString:
			result[key.val] = valueOrBrace.val
		default:
			return nil, fmt.Errorf("expected value or '{' after key %q", key.val)
		}
	}
}

// ParseVDFFromFile opens a file and parses it as VDF.
func ParseVDFFromFile(path string) (map[string]interface{}, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewReader(f)
	return ParseVDF(sc)
}

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
)

// KnownGames is the registry of supported Bethesda games.
// Must match the C++ frontend's GameInfo::knownGames() exactly.
var KnownGames = []GameDefinition{
	{ID: "morrowind", Name: "The Elder Scrolls III: Morrowind", SteamAppID: 22320, DataSubpath: "Data"},
	{ID: "oblivion", Name: "The Elder Scrolls IV: Oblivion", SteamAppID: 22330, DataSubpath: "Data"},
	{ID: "skyrim", Name: "The Elder Scrolls V: Skyrim", SteamAppID: 72850, DataSubpath: "Data"},
	{ID: "skyrimse", Name: "The Elder Scrolls V: Skyrim Special Edition", SteamAppID: 489830, DataSubpath: "Data"},
	{ID: "fallout3", Name: "Fallout 3", SteamAppID: 22370, DataSubpath: "Data"},
	{ID: "falloutnv", Name: "Fallout: New Vegas", SteamAppID: 22380, DataSubpath: "Data"},
	{ID: "fallout4", Name: "Fallout 4", SteamAppID: 377160, DataSubpath: "Data"},
	{ID: "starfield", Name: "Starfield", SteamAppID: 1716740, DataSubpath: "Data"},
}

// knownByAppID maps Steam app IDs to game definitions.
var knownByAppID map[uint32]GameDefinition

func init() {
	knownByAppID = make(map[uint32]GameDefinition, len(KnownGames))
	for _, g := range KnownGames {
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

// DetectInstalledGames scans all Steam library folders for known games.
// Matches the C++ GameDetector::detectAll() logic exactly.
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

	// Sort by app ID for consistent ordering (matches C++ frontend).
	sort.Slice(detected, func(i, j int) bool {
		return detected[i].SteamAppID < detected[j].SteamAppID
	})
	return detected, nil
}

// FindSteamRoot checks the three locations in priority order,
// matching the C++ Paths::steamRoot() exactly.
func FindSteamRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding home directory: %w", err)
	}

	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}

	// Primary location.
	primary := filepath.Join(dataHome, "Steam")
	if dirExists(filepath.Join(primary, "steamapps")) {
		return primary, nil
	}

	// Symlink fallback.
	symlink := filepath.Join(home, ".steam", "steam")
	if dirExists(filepath.Join(symlink, "steamapps")) {
		resolved, err := filepath.EvalSymlinks(symlink)
		if err == nil {
			return resolved, nil
		}
		return symlink, nil
	}

	// Flatpak fallback.
	flatpak := filepath.Join(home, ".var", "app", "com.valvesoftware.Steam", ".local", "share", "Steam")
	if dirExists(filepath.Join(flatpak, "steamapps")) {
		return flatpak, nil
	}

	return "", fmt.Errorf("steam root not found")
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

	// Navigate to "libraryfolders" -> each numeric key -> "path".
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
// Returns nil (not an error) if the game is not a known Bethesda game.
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
		return nil, nil // Not a known game.
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

	// Use the name from the manifest if available.
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

// dirExists checks if a path exists and is a directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// --- VDF Parser ---
// Matches the C++ VdfParser grammar: quoted strings with escape sequences,
// // line comments, bare words, nested brace objects.

// ParseVDF parses Valve's VDF text format into a map structure.
// Used for both libraryfolders.vdf and appmanifest_*.acf files.
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
	vdfString     vdfTokenType = iota
	vdfBraceOpen               // {
	vdfBraceClose              // }
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

	// Bare word.
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

// ParseVDFFromFile is a convenience wrapper that opens a file and parses it.
func ParseVDFFromFile(path string) (map[string]interface{}, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewReader(f)
	return ParseVDF(sc)
}

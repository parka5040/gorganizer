package download

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// NXMLink holds the parsed components of an nxm:// URI.
type NXMLink struct {
	GameSlug string
	ModID    int
	FileID   int
	Key      string
	Expires  int64
}

// slugToGameID maps Nexus game slugs to internal game IDs.
var slugToGameID = map[string]string{
	"skyrimspecialedition": "skyrimse",
	"skyrim":              "skyrim",
	"newvegas":            "falloutnv",
	"fallout3":            "fallout3",
	"fallout4":            "fallout4",
	"oblivion":            "oblivion",
	"morrowind":           "morrowind",
	"starfield":           "starfield",
}

// ParseNXM parses an nxm:// URI into its components.
// Format: nxm://skyrimspecialedition/mods/12345/files/67890?key=abc123&expires=1234567890
func ParseNXM(uri string) (*NXMLink, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidNXMURI, err)
	}
	if u.Scheme != "nxm" {
		return nil, fmt.Errorf("%w: scheme is %q, expected \"nxm\"", ErrInvalidNXMURI, u.Scheme)
	}

	// Path: /<game_slug>/mods/<mod_id>/files/<file_id>
	// Host is the game slug, path starts after the host.
	gameSlug := u.Host
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")

	if len(parts) < 4 || parts[0] != "mods" || parts[2] != "files" {
		return nil, fmt.Errorf("%w: unexpected path format: %s", ErrInvalidNXMURI, u.Path)
	}

	modID, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, fmt.Errorf("%w: invalid mod ID: %s", ErrInvalidNXMURI, parts[1])
	}
	fileID, err := strconv.Atoi(parts[3])
	if err != nil {
		return nil, fmt.Errorf("%w: invalid file ID: %s", ErrInvalidNXMURI, parts[3])
	}

	link := &NXMLink{
		GameSlug: gameSlug,
		ModID:    modID,
		FileID:   fileID,
		Key:      u.Query().Get("key"),
	}

	if exp := u.Query().Get("expires"); exp != "" {
		link.Expires, _ = strconv.ParseInt(exp, 10, 64)
	}

	return link, nil
}

// GameID returns the internal game ID for this NXM link's game slug.
func (n *NXMLink) GameID() (string, error) {
	id, ok := slugToGameID[n.GameSlug]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnknownSlug, n.GameSlug)
	}
	return id, nil
}

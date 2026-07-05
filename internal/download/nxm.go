package download

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/parka/gorganizer/internal/game"
)

// NXMLink holds the parsed components of an nxm:// URI.
type NXMLink struct {
	GameSlug string
	ModID    int
	FileID   int
	Key      string
	Expires  int64
}

// GameSlug returns the canonical Nexus slug for an internal gameID, or "".
// The slug data lives on the central game registry (game.GameDefinition.NxmSlug).
func GameSlug(gameID string) string {
	return game.NxmSlugForID(gameID)
}

// ParseNXM parses an nxm:// URI into its components.
func ParseNXM(uri string) (*NXMLink, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidNXMURI, err)
	}
	if u.Scheme != "nxm" {
		return nil, fmt.Errorf("%w: scheme is %q, expected \"nxm\"", ErrInvalidNXMURI, u.Scheme)
	}

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

func (n *NXMLink) GameID() (string, error) {
	id, ok := game.GameIDForNxmSlug(n.GameSlug)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnknownSlug, n.GameSlug)
	}
	return id, nil
}

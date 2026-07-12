package gamedef

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var cppGameLine = regexp.MustCompile(`^\s*\{\s*(\d+)\s*,\s*"([^"]*)"\s*,\s*"([^"]*)"`)

type gameIdentity struct {
	appID uint32
	name  string
}

func parseCppKnownGames(t *testing.T, path string) map[string]gameIdentity {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading C++ registry %s: %v", path, err)
	}
	out := make(map[string]gameIdentity)
	inBlock := false
	for _, line := range splitLines(string(data)) {
		if !inBlock {
			if regexp.MustCompile(`knownGames\(\)`).MatchString(line) {
				inBlock = true
			}
			continue
		}
		if m := cppGameLine.FindStringSubmatch(line); m != nil {
			appID := parseUint(t, m[1])
			out[m[3]] = gameIdentity{appID: appID, name: m[2]}
		}
		if inBlock && regexpEndBlock.MatchString(line) {
			break
		}
	}
	if len(out) == 0 {
		t.Fatalf("parsed zero games from %s — regex or file layout changed", path)
	}
	return out
}

var regexpEndBlock = regexp.MustCompile(`^\s*\};`)

func TestGameRegistryMatchesCppFrontend(t *testing.T) {
	cppPath := filepath.Join("..", "..", "src", "core", "GameInfo.cpp")
	if _, err := os.Stat(cppPath); err != nil {
		t.Skipf("C++ registry not found at %s (skipping cross-language check): %v", cppPath, err)
	}
	cpp := parseCppKnownGames(t, cppPath)

	goGames := make(map[string]gameIdentity, len(All))
	for _, g := range All {
		goGames[g.ID] = gameIdentity{appID: g.SteamAppID, name: g.Name}
	}

	for id, gi := range goGames {
		c, ok := cpp[id]
		if !ok {
			t.Errorf("game %q is in Go gamedef.All but missing from C++ GameInfo::knownGames()", id)
			continue
		}
		if c.appID != gi.appID {
			t.Errorf("game %q appID mismatch: Go=%d C++=%d", id, gi.appID, c.appID)
		}
		if c.name != gi.name {
			t.Errorf("game %q name mismatch:\n  Go = %q\n  C++= %q", id, gi.name, c.name)
		}
	}
	for id := range cpp {
		if _, ok := goGames[id]; !ok {
			t.Errorf("game %q is in C++ GameInfo::knownGames() but missing from Go gamedef.All", id)
		}
	}
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func parseUint(t *testing.T, s string) uint32 {
	t.Helper()
	var n uint32
	for _, r := range s {
		if r < '0' || r > '9' {
			t.Fatalf("non-numeric appID %q", s)
		}
		n = n*10 + uint32(r-'0')
	}
	return n
}

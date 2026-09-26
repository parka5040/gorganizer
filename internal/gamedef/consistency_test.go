package gamedef

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

var cppGameLine = regexp.MustCompile(`^\s*\{\s*(\d+)\s*,\s*"([^"]*)"\s*,\s*"([^"]*)"`)

var regexpEndBlock = regexp.MustCompile(`^\s*\};`)

const (
	cppFieldDataSubpath       = 9
	cppFieldModsDirName       = 10
	cppFieldExecutablePaths   = 11
	cppFieldRequiredDataFiles = 12
	cppFieldCanonicalDlcOrder = 17
	cppFieldDataDirOptional   = 18
)

type cppValue struct {
	isList   bool
	isString bool
	text     string
	list     []cppValue
}

type cppGameRow struct {
	appID  uint32
	name   string
	text   string
	fields []cppValue
}

func parseCppKnownGames(t *testing.T, path string) map[string]cppGameRow {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading C++ registry %s: %v", path, err)
	}
	out := make(map[string]cppGameRow)
	var currentID string
	var current cppGameRow
	var rowText strings.Builder
	flush := func() {
		if currentID == "" {
			return
		}
		current.text = rowText.String()
		current.fields = parseCppRow(t, currentID, current.text)
		out[currentID] = current
		currentID = ""
		rowText.Reset()
	}
	inBlock := false
	for _, line := range splitLines(string(data)) {
		if !inBlock {
			if regexp.MustCompile(`knownGames\(\)`).MatchString(line) {
				inBlock = true
			}
			continue
		}
		if regexpEndBlock.MatchString(line) {
			flush()
			break
		}
		if m := cppGameLine.FindStringSubmatch(line); m != nil {
			flush()
			currentID = m[3]
			current = cppGameRow{appID: parseUint(t, m[1]), name: m[2]}
		}
		if currentID != "" {
			rowText.WriteString(line)
			rowText.WriteByte('\n')
		}
	}
	if len(out) == 0 {
		t.Fatalf("parsed zero games from %s — regex or file layout changed", path)
	}
	return out
}

// parseCppRow parses one knownGames() brace-initializer row into its positional fields.
func parseCppRow(t *testing.T, id, text string) []cppValue {
	t.Helper()
	tokens := tokenizeCpp(t, id, text)
	pos := 0
	row := parseCppValue(t, id, tokens, &pos)
	for ; pos < len(tokens); pos++ {
		if tokens[pos] != "," {
			t.Fatalf("C++ row %q: unexpected token %q after the row initializer", id, tokens[pos])
		}
	}
	if !row.isList {
		t.Fatalf("C++ row %q is not a brace initializer", id)
	}
	return row.list
}

// parseCppValue parses one scalar or brace list starting at tokens[*pos].
func parseCppValue(t *testing.T, id string, tokens []string, pos *int) cppValue {
	t.Helper()
	if *pos >= len(tokens) {
		t.Fatalf("C++ row %q: unexpected end of row", id)
	}
	tok := tokens[*pos]
	*pos++
	switch {
	case tok == "{":
		v := cppValue{isList: true}
		for {
			if *pos >= len(tokens) {
				t.Fatalf("C++ row %q: unterminated brace list", id)
			}
			if tokens[*pos] == "}" {
				*pos++
				return v
			}
			v.list = append(v.list, parseCppValue(t, id, tokens, pos))
			if *pos < len(tokens) && tokens[*pos] == "," {
				*pos++
			}
		}
	case tok == "}" || tok == ",":
		t.Fatalf("C++ row %q: unexpected %q", id, tok)
	case strings.HasPrefix(tok, `"`):
		text := tok[1:]
		for *pos < len(tokens) && strings.HasPrefix(tokens[*pos], `"`) {
			text += tokens[*pos][1:]
			*pos++
		}
		return cppValue{isString: true, text: text}
	}
	return cppValue{text: tok}
}

// tokenizeCpp splits a C++ initializer into braces, commas, string literals prefixed by a quote, and bare words.
func tokenizeCpp(t *testing.T, id, text string) []string {
	t.Helper()
	var tokens []string
	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '{' || c == '}' || c == ',':
			tokens = append(tokens, string(c))
			i++
		case c == '"':
			var lit strings.Builder
			lit.WriteByte('"')
			i++
			for {
				if i >= len(text) {
					t.Fatalf("C++ row %q: unterminated string literal", id)
				}
				if text[i] == '\\' && i+1 < len(text) {
					lit.WriteByte(text[i+1])
					i += 2
					continue
				}
				if text[i] == '"' {
					i++
					break
				}
				lit.WriteByte(text[i])
				i++
			}
			tokens = append(tokens, lit.String())
		case c == '_' || c == '.' || c == ':' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			start := i
			for i < len(text) && (text[i] == '_' || text[i] == '.' || text[i] == ':' ||
				(text[i] >= '0' && text[i] <= '9') || (text[i] >= 'a' && text[i] <= 'z') || (text[i] >= 'A' && text[i] <= 'Z')) {
				i++
			}
			tokens = append(tokens, text[start:i])
		default:
			t.Fatalf("C++ row %q: unsupported character %q in initializer", id, c)
		}
	}
	return tokens
}

// cppString returns the string literal at field index, failing when it is absent or not a string.
func cppString(t *testing.T, id string, row cppGameRow, index int) string {
	t.Helper()
	if index >= len(row.fields) || !row.fields[index].isString {
		t.Fatalf("C++ row %q: field %d is not a string literal", id, index)
	}
	return row.fields[index].text
}

// cppStringList returns the brace list of string literals at field index.
func cppStringList(t *testing.T, id string, row cppGameRow, index int) []string {
	t.Helper()
	if index >= len(row.fields) || !row.fields[index].isList {
		t.Fatalf("C++ row %q: field %d is not a brace list", id, index)
	}
	out := make([]string, 0, len(row.fields[index].list))
	for _, v := range row.fields[index].list {
		if !v.isString {
			t.Fatalf("C++ row %q: field %d holds non-string %q", id, index, v.text)
		}
		out = append(out, v.text)
	}
	return out
}

// cppDataDirOptional returns the optional trailing dataDirOptional flag of a row.
func cppDataDirOptional(t *testing.T, id string, row cppGameRow) bool {
	t.Helper()
	switch len(row.fields) {
	case cppFieldCanonicalDlcOrder + 1:
		return false
	case cppFieldDataDirOptional + 1:
		v := row.fields[cppFieldDataDirOptional]
		if v.isList || v.isString || (v.text != "true" && v.text != "false") {
			t.Fatalf("C++ row %q: dataDirOptional must be a bare true/false, got %+v", id, v)
		}
		return v.text == "true"
	}
	t.Fatalf("C++ row %q has %d positional fields, want %d or %d", id, len(row.fields),
		cppFieldCanonicalDlcOrder+1, cppFieldDataDirOptional+1)
	return false
}

// normalizeList maps a nil slice to an empty one so registry and C++ lists compare by content.
func normalizeList(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func TestGameRegistryMatchesCppFrontend(t *testing.T) {
	cppPath := filepath.Join("..", "..", "src", "core", "GameInfo.cpp")
	if _, err := os.Stat(cppPath); err != nil {
		t.Skipf("C++ registry not found at %s (skipping cross-language check): %v", cppPath, err)
	}
	cpp := parseCppKnownGames(t, cppPath)

	goGames := make(map[string]Definition, len(All))
	for _, g := range All {
		goGames[g.ID] = g
	}

	for id, def := range goGames {
		c, ok := cpp[id]
		if !ok {
			t.Errorf("game %q is in Go gamedef.All but missing from C++ GameInfo::knownGames()", id)
			continue
		}
		if c.appID != def.SteamAppID {
			t.Errorf("game %q appID mismatch: Go=%d C++=%d", id, def.SteamAppID, c.appID)
		}
		if c.name != def.Name {
			t.Errorf("game %q name mismatch:\n  Go = %q\n  C++= %q", id, def.Name, c.name)
		}
		if got := cppString(t, id, c, cppFieldDataSubpath); got != def.DataSubpath {
			t.Errorf("game %q dataSubpath mismatch: Go=%q C++=%q", id, def.DataSubpath, got)
		}
		if got := cppString(t, id, c, cppFieldModsDirName); got != def.ModsDirName {
			t.Errorf("game %q modsDirName mismatch: Go=%q C++=%q", id, def.ModsDirName, got)
		}
		if got := cppStringList(t, id, c, cppFieldExecutablePaths); !reflect.DeepEqual(got, normalizeList(def.ExecutablePaths)) {
			t.Errorf("game %q executablePaths mismatch: Go=%q C++=%q", id, def.ExecutablePaths, got)
		}
		if got := cppStringList(t, id, c, cppFieldRequiredDataFiles); !reflect.DeepEqual(got, normalizeList(def.RequiredDataFiles)) {
			t.Errorf("game %q requiredDataFiles mismatch: Go=%q C++=%q", id, def.RequiredDataFiles, got)
		}
		if got := cppDataDirOptional(t, id, c); got != def.DataDirOptional {
			t.Errorf("game %q dataDirOptional mismatch: Go=%v C++=%v\n  row: %s", id, def.DataDirOptional, got, c.text)
		}
	}
	for id := range cpp {
		if _, ok := goGames[id]; !ok {
			t.Errorf("game %q is in C++ GameInfo::knownGames() but missing from Go gamedef.All", id)
		}
	}
}

func TestParseCppRowShapes(t *testing.T) {
	text := `{1, "Name", "short",
         {}, {}, false, false, "", false,
         "Sub" "path", "Short_Mods", {"a.exe", "Dir/b"}, {}, "", "", "",
         {"M.esm"}, {}, true},
`
	fields := parseCppRow(t, "short", text)
	row := cppGameRow{fields: fields}
	if got := cppString(t, "short", row, cppFieldDataSubpath); got != "Subpath" {
		t.Errorf("dataSubpath = %q, want Subpath", got)
	}
	if got := cppStringList(t, "short", row, cppFieldExecutablePaths); !reflect.DeepEqual(got, []string{"a.exe", "Dir/b"}) {
		t.Errorf("executablePaths = %q", got)
	}
	if !cppDataDirOptional(t, "short", row) {
		t.Error("dataDirOptional = false, want true")
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

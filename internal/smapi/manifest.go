package smapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

type ModKind uint8

const (
	KindInvalid ModKind = iota
	KindCode
	KindContentPack
)

type ContentPackFor struct {
	UniqueID       string
	MinimumVersion *Version
}

type Dependency struct {
	UniqueID       string
	MinimumVersion *Version
	IsRequired     bool
}

type Manifest struct {
	Name               string
	Author             string
	Description        string
	Version            Version
	UniqueID           string
	EntryDll           string
	ContentPackFor     *ContentPackFor
	Dependencies       []Dependency
	UpdateKeys         []string
	MinimumApiVersion  *Version
	MinimumGameVersion *Version
}

var errNullManifest = errors.New("its manifest is invalid.")

type lenientString struct {
	Value string
	Set   bool
}

type lenientBool struct {
	Value bool
	Set   bool
}

type rawManifest struct {
	Name               lenientString
	Author             lenientString
	Description        lenientString
	Version            json.RawMessage
	UniqueID           lenientString
	EntryDll           lenientString
	ContentPackFor     json.RawMessage
	Dependencies       json.RawMessage
	UpdateKeys         json.RawMessage
	MinimumApiVersion  json.RawMessage
	MinimumGameVersion json.RawMessage
}

type rawContentPackFor struct {
	UniqueID       lenientString
	MinimumVersion json.RawMessage
}

type rawDependency struct {
	UniqueID       lenientString
	MinimumVersion lenientString
	IsRequired     lenientBool
}

// UnmarshalJSON accepts a string, number, boolean or null the way Newtonsoft coerces scalars into strings.
func (s *lenientString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case len(b) == 0 || bytes.Equal(b, []byte("null")):
		*s = lenientString{}
	case b[0] == '"':
		var text string
		if err := json.Unmarshal(b, &text); err != nil {
			return err
		}
		*s = lenientString{Value: text, Set: true}
	case bytes.Equal(b, []byte("true")):
		*s = lenientString{Value: "True", Set: true}
	case bytes.Equal(b, []byte("false")):
		*s = lenientString{Value: "False", Set: true}
	case b[0] == '-' || b[0] >= '0' && b[0] <= '9':
		var number json.Number
		if err := json.Unmarshal(b, &number); err != nil {
			return err
		}
		*s = lenientString{Value: number.String(), Set: true}
	default:
		return fmt.Errorf("expected a string value, got %s", b)
	}
	return nil
}

// UnmarshalJSON accepts a boolean, a "true"/"false" string, a number or null the way Newtonsoft converts nullable booleans.
func (v *lenientBool) UnmarshalJSON(b []byte) error {
	var text lenientString
	if err := text.UnmarshalJSON(b); err != nil {
		return fmt.Errorf("expected a boolean value: %w", err)
	}
	if !text.Set {
		*v = lenientBool{}
		return nil
	}
	trimmed := strings.TrimSpace(text.Value)
	switch {
	case strings.EqualFold(trimmed, "true"):
		*v = lenientBool{Value: true, Set: true}
	case strings.EqualFold(trimmed, "false"):
		*v = lenientBool{Value: false, Set: true}
	default:
		number, err := strconv.ParseFloat(trimmed, 64)
		if err != nil || bytes.TrimSpace(b)[0] == '"' {
			return fmt.Errorf("expected a boolean value, got %s", b)
		}
		*v = lenientBool{Value: number != 0, Set: true}
	}
	return nil
}

// ParseManifest parses a SMAPI manifest.json and applies the Manifest constructor normalization.
func ParseManifest(data []byte) (*Manifest, error) {
	data, err := Standardize(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidManifest, err)
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, fmt.Errorf("%w: %w", ErrInvalidManifest, errNullManifest)
	}
	if trimmed[0] != '{' {
		return nil, fmt.Errorf("%w: manifest must be a JSON object", ErrInvalidManifest)
	}
	var raw rawManifest
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		return nil, fmt.Errorf("%w: %s%w", ErrInvalidManifest, curlyQuoteHint(trimmed, err), err)
	}
	m := &Manifest{
		Name:        normalizeField(raw.Name.Value, true),
		Author:      normalizeField(raw.Author.Value, false),
		Description: normalizeField(raw.Description.Value, false),
		UniqueID:    normalizeField(raw.UniqueID.Value, false),
		EntryDll:    normalizeField(raw.EntryDll.Value, false),
	}
	version, err := parseVersionField(raw.Version, "Version")
	if err != nil {
		return nil, err
	}
	if version != nil {
		m.Version = *version
	}
	if m.MinimumApiVersion, err = parseVersionField(raw.MinimumApiVersion, "MinimumApiVersion"); err != nil {
		return nil, err
	}
	if m.MinimumGameVersion, err = parseVersionField(raw.MinimumGameVersion, "MinimumGameVersion"); err != nil {
		return nil, err
	}
	if m.ContentPackFor, err = parseContentPackFor(raw.ContentPackFor); err != nil {
		return nil, err
	}
	if m.Dependencies, err = parseDependencies(raw.Dependencies); err != nil {
		return nil, err
	}
	if m.UpdateKeys, err = parseUpdateKeys(raw.UpdateKeys); err != nil {
		return nil, err
	}
	return m, nil
}

// curlyQuoteHint returns SMAPI's curly-quote hint when a syntax error is likely caused by typographic quotes.
func curlyQuoteHint(data []byte, err error) string {
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) && (bytes.Contains(data, []byte("“")) || bytes.Contains(data, []byte("”"))) {
		return "found curly quotes in the text; note that only straight quotes are allowed in JSON: "
	}
	return ""
}

// parseVersionField decodes one manifest version field, returning nil when it is absent, null or blank.
func parseVersionField(raw json.RawMessage, field string) (*Version, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var v Version
	if err := v.UnmarshalJSON(raw); err != nil {
		return nil, fmt.Errorf("%w: invalid %s: %w", ErrInvalidManifest, field, err)
	}
	if v.IsZero() {
		return nil, nil
	}
	return &v, nil
}

// parseContentPackFor decodes the ContentPackFor object, treating null as absent.
func parseContentPackFor(raw json.RawMessage) (*ContentPackFor, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	if raw[0] != '{' {
		return nil, fmt.Errorf("%w: ContentPackFor must be an object", ErrInvalidManifest)
	}
	var parsed rawContentPackFor
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("%w: invalid ContentPackFor: %w", ErrInvalidManifest, err)
	}
	minimum, err := parseVersionField(parsed.MinimumVersion, "ContentPackFor.MinimumVersion")
	if err != nil {
		return nil, err
	}
	return &ContentPackFor{UniqueID: strings.TrimSpace(parsed.UniqueID.Value), MinimumVersion: minimum}, nil
}

// parseDependencies ports ManifestDependencyArrayConverter, skipping entries that are not objects.
func parseDependencies(raw json.RawMessage) ([]Dependency, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("%w: invalid Dependencies: %w", ErrInvalidManifest, err)
	}
	var result []Dependency
	for i, entry := range entries {
		entry = bytes.TrimSpace(entry)
		if len(entry) == 0 || entry[0] != '{' {
			continue
		}
		var parsed rawDependency
		if err := json.Unmarshal(entry, &parsed); err != nil {
			return nil, fmt.Errorf("%w: invalid Dependencies[%d]: %w", ErrInvalidManifest, i, err)
		}
		dep := Dependency{UniqueID: strings.TrimSpace(parsed.UniqueID.Value), IsRequired: true}
		if parsed.IsRequired.Set {
			dep.IsRequired = parsed.IsRequired.Value
		}
		if strings.TrimSpace(parsed.MinimumVersion.Value) != "" {
			minimum, err := ParseVersion(parsed.MinimumVersion.Value, false)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid Dependencies[%d].MinimumVersion: %w", ErrInvalidManifest, i, err)
			}
			dep.MinimumVersion = &minimum
		}
		result = append(result, dep)
	}
	return result, nil
}

// parseUpdateKeys decodes the UpdateKeys string array, treating null as empty.
func parseUpdateKeys(raw json.RawMessage) ([]string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var keys []lenientString
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, fmt.Errorf("%w: invalid UpdateKeys: %w", ErrInvalidManifest, err)
	}
	result := make([]string, len(keys))
	for i, key := range keys {
		result[i] = key.Value
	}
	return result, nil
}

// normalizeField ports Manifest.NormalizeField: trim, newlines to spaces, and optionally square to round brackets.
func normalizeField(s string, replaceBrackets bool) string {
	s = strings.TrimSpace(s)
	replacer := strings.NewReplacer("\r", " ", "\n", " ")
	if replaceBrackets {
		replacer = strings.NewReplacer("\r", " ", "\n", " ", "[", "(", "]", ")")
	}
	return replacer.Replace(s)
}

// Kind classifies the manifest like ModScanner.ReadFolder: code mod, content pack or invalid.
func (m *Manifest) Kind() ModKind {
	if m == nil {
		return KindInvalid
	}
	isContentPack := m.ContentPackFor != nil && strings.TrimSpace(m.ContentPackFor.UniqueID) != ""
	isCode := strings.TrimSpace(m.EntryDll) != ""
	switch {
	case isContentPack == isCode:
		return KindInvalid
	case isContentPack:
		return KindContentPack
	}
	return KindCode
}

// Validate ports ManifestValidator.TryValidateFields and returns an error wrapping ErrInvalidManifest.
func (m *Manifest) Validate() error {
	if problem := m.validationProblem(); problem != "" {
		return fmt.Errorf("%w: %s", ErrInvalidManifest, problem)
	}
	return nil
}

// validationProblem returns SMAPI's sentence describing the first invalid field, or an empty string.
func (m *Manifest) validationProblem() string {
	if m == nil {
		return "manifest is missing."
	}
	hasDll := strings.TrimSpace(m.EntryDll) != ""
	isContentPack := m.ContentPackFor != nil
	if hasDll == isContentPack {
		if hasDll {
			return "manifest sets both EntryDll and ContentPackFor, which are mutually exclusive."
		}
		return "manifest has no EntryDll or ContentPackFor field; must specify one."
	}
	if hasDll {
		if strings.ContainsFunc(m.EntryDll, isInvalidFileNameRune) {
			return fmt.Sprintf("manifest has invalid filename '%s' for the EntryDll field.", m.EntryDll)
		}
	} else if strings.TrimSpace(m.ContentPackFor.UniqueID) == "" {
		return "manifest declares ContentPackFor without its required UniqueID field."
	}
	var missing []string
	if strings.TrimSpace(m.Name) == "" {
		missing = append(missing, "Name")
	}
	if m.Version.IsZero() || m.Version.String() == "0.0.0" {
		missing = append(missing, "Version")
	}
	if strings.TrimSpace(m.UniqueID) == "" {
		missing = append(missing, "UniqueID")
	}
	if len(missing) != 0 {
		return "manifest is missing required fields (" + strings.Join(missing, ", ") + ")."
	}
	if !isSlug(m.UniqueID) {
		return "manifest specifies an invalid ID (IDs must only contain letters, numbers, underscores, periods, or hyphens)."
	}
	for _, dep := range m.Dependencies {
		if strings.TrimSpace(dep.UniqueID) == "" {
			return "manifest has a Dependencies entry with no UniqueID field."
		}
		if !isSlug(dep.UniqueID) {
			return "manifest has a Dependencies entry with an invalid UniqueID field (IDs must only contain letters, numbers, underscores, periods, or hyphens)."
		}
	}
	return ""
}

// isInvalidFileNameRune reports whether r is forbidden in a file name on Linux or Windows.
func isInvalidFileNameRune(r rune) bool {
	return r < 32 || strings.ContainsRune(`<>:"/\|?*`, r)
}

// isSlug ports PathUtilities.IsSlug: only letters, decimal digits, underscores, periods and hyphens.
func isSlug(value string) bool {
	for _, r := range value {
		if !unicode.IsLetter(r) && !unicode.Is(unicode.Nd, r) && r != '_' && r != '.' && r != '-' {
			return false
		}
	}
	return true
}

// RequiredDependencies returns the required dependencies plus ContentPackFor as a required dependency.
func (m *Manifest) RequiredDependencies() []Dependency {
	if m == nil {
		return nil
	}
	var result []Dependency
	for _, dep := range m.allDependencies() {
		if dep.IsRequired {
			result = append(result, dep)
		}
	}
	return result
}

// allDependencies ports ModResolver.GetDependenciesFrom: every declared dependency followed by the content pack parent.
func (m *Manifest) allDependencies() []Dependency {
	result := make([]Dependency, 0, len(m.Dependencies)+1)
	result = append(result, m.Dependencies...)
	if m.ContentPackFor != nil {
		result = append(result, Dependency{UniqueID: m.ContentPackFor.UniqueID, MinimumVersion: m.ContentPackFor.MinimumVersion, IsRequired: true})
	}
	return result
}

// NexusIDs extracts the distinct Nexus mod IDs from update keys like "Nexus:1915" or "Nexus:1915@main".
func NexusIDs(updateKeys []string) []int {
	var result []int
	seen := map[int]bool{}
	for _, key := range updateKeys {
		parts := strings.Split(strings.TrimSpace(key), ":")
		if len(parts) != 2 || !strings.EqualFold(strings.TrimSpace(parts[0]), "Nexus") {
			continue
		}
		id := strings.TrimSpace(parts[1])
		if subkey := strings.Split(id, "@"); len(subkey) == 2 {
			id = strings.TrimSpace(subkey[0])
		}
		value, err := strconv.ParseInt(id, 10, 32)
		if err != nil || value <= 0 || seen[int(value)] {
			continue
		}
		seen[int(value)] = true
		result = append(result, int(value))
	}
	return result
}

// SameID reports whether two mod IDs match after trimming, ignoring case.
func SameID(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

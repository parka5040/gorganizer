package smapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

type Version struct {
	Major, Minor, Patch, Platform int
	Prerelease, Build             string
}

type versionReader struct {
	s  string
	at int
}

// ParseVersion parses a SMAPI semantic version, accepting a fourth platform number only when allowNonStandard is set.
func ParseVersion(s string, allowNonStandard bool) (Version, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return Version{}, fmt.Errorf("%w: %q", ErrInvalidVersion, s)
	}
	r := versionReader{s: trimmed}
	var v Version
	var ok bool
	if v.Major, ok = r.part(); !ok || !r.literal('.') {
		return Version{}, fmt.Errorf("%w: %q", ErrInvalidVersion, s)
	}
	if v.Minor, ok = r.part(); !ok {
		return Version{}, fmt.Errorf("%w: %q", ErrInvalidVersion, s)
	}
	if r.literal('.') {
		if v.Patch, ok = r.part(); !ok {
			return Version{}, fmt.Errorf("%w: %q", ErrInvalidVersion, s)
		}
	}
	if allowNonStandard && r.literal('.') {
		if v.Platform, ok = r.part(); !ok {
			return Version{}, fmt.Errorf("%w: %q", ErrInvalidVersion, s)
		}
	}
	if r.literal('-') {
		if v.Prerelease, ok = r.tag(); !ok {
			return Version{}, fmt.Errorf("%w: %q", ErrInvalidVersion, s)
		}
	}
	if r.literal('+') {
		if v.Build, ok = r.tag(); !ok {
			return Version{}, fmt.Errorf("%w: %q", ErrInvalidVersion, s)
		}
	}
	if r.at != len(r.s) {
		return Version{}, fmt.Errorf("%w: %q", ErrInvalidVersion, s)
	}
	if err := v.validate(); err != nil {
		return Version{}, fmt.Errorf("%w: %q: %s", ErrInvalidVersion, s, err.Error())
	}
	return v, nil
}

// part reads one numeric version component without leading zeros.
func (r *versionReader) part() (int, bool) {
	start := r.at
	end := start
	for end < len(r.s) && r.s[end] >= '0' && r.s[end] <= '9' {
		end++
	}
	digits := r.s[start:end]
	if digits == "" || len(digits) > 1 && digits[0] == '0' {
		return 0, false
	}
	value, err := strconv.ParseInt(digits, 10, 32)
	if err != nil {
		return 0, false
	}
	r.at = end
	return int(value), true
}

// literal consumes ch when it is the next character.
func (r *versionReader) literal(ch byte) bool {
	if r.at >= len(r.s) || r.s[r.at] != ch {
		return false
	}
	r.at++
	return true
}

// tag reads a prerelease or build tag made of letters, digits, hyphens and periods.
func (r *versionReader) tag() (string, bool) {
	start := r.at
	for r.at < len(r.s) && (isASCIIAlnum(r.s[r.at]) || r.s[r.at] == '-' || r.s[r.at] == '.') {
		r.at++
	}
	return r.s[start:r.at], r.at > start
}

// isASCIIAlnum reports whether ch is an ASCII letter or digit.
func isASCIIAlnum(ch byte) bool {
	return ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
}

// validTag reports whether tag matches SMAPI's tag pattern of alphanumeric runs separated by single hyphens or periods.
func validTag(tag string) bool {
	if tag == "" || !isASCIIAlnum(tag[0]) {
		return false
	}
	for i := 0; i < len(tag); i++ {
		ch := tag[i]
		if isASCIIAlnum(ch) {
			continue
		}
		if ch != '-' && ch != '.' {
			return false
		}
		if i+1 < len(tag) && !isASCIIAlnum(tag[i+1]) {
			return false
		}
	}
	return true
}

// validate applies the SemanticVersion constructor assertions.
func (v Version) validate() error {
	if v.Major < 0 || v.Minor < 0 || v.Patch < 0 {
		return fmt.Errorf("the major, minor, and patch numbers can't be negative")
	}
	if v.Major == 0 && v.Minor == 0 && v.Patch == 0 {
		return fmt.Errorf("at least one of the major, minor, and patch numbers must be more than zero")
	}
	if v.Prerelease != "" && !validTag(v.Prerelease) {
		return fmt.Errorf("the prerelease tag is invalid")
	}
	if v.Build != "" && !validTag(v.Build) {
		return fmt.Errorf("the build metadata is invalid")
	}
	return nil
}

// MustParseVersion parses s with non-standard versions allowed and panics when it is invalid.
func MustParseVersion(s string) Version {
	v, err := ParseVersion(s, true)
	if err != nil {
		panic(err)
	}
	return v
}

// String formats the version like SMAPI's SemanticVersion.ToString.
func (v Version) String() string {
	var b strings.Builder
	b.WriteString(strconv.Itoa(v.Major))
	b.WriteByte('.')
	b.WriteString(strconv.Itoa(v.Minor))
	b.WriteByte('.')
	b.WriteString(strconv.Itoa(v.Patch))
	if v.Platform != 0 {
		b.WriteByte('.')
		b.WriteString(strconv.Itoa(v.Platform))
	}
	if v.Prerelease != "" {
		b.WriteByte('-')
		b.WriteString(v.Prerelease)
	}
	if v.Build != "" {
		b.WriteByte('+')
		b.WriteString(v.Build)
	}
	return b.String()
}

// Compare returns -1, 0 or 1 when v precedes, equals or supersedes o, ignoring build metadata.
func (v Version) Compare(o Version) int {
	return sign(v.compareRaw(o))
}

// compareRaw ports SemanticVersion.CompareTo before its result is clamped to a sign.
func (v Version) compareRaw(o Version) int {
	if v.Major != o.Major {
		return compareInt(v.Major, o.Major)
	}
	if v.Minor != o.Minor {
		return compareInt(v.Minor, o.Minor)
	}
	if v.Patch != o.Patch {
		return compareInt(v.Patch, o.Patch)
	}
	if v.Platform != o.Platform {
		return compareInt(v.Platform, o.Platform)
	}
	if v.Prerelease == o.Prerelease {
		return 0
	}
	if strings.TrimSpace(v.Prerelease) == "" {
		return 1
	}
	if strings.TrimSpace(o.Prerelease) == "" {
		return -1
	}
	cur := splitTag(v.Prerelease)
	other := splitTag(o.Prerelease)
	length := max(len(cur), len(other))
	for i := 0; i < length; i++ {
		if len(cur) <= i {
			return -1
		}
		if len(other) <= i {
			return 1
		}
		if cur[i] == other[i] {
			if i == length-1 {
				return 0
			}
			continue
		}
		if strings.EqualFold(other[i], "unofficial") {
			return 1
		}
		if strings.EqualFold(cur[i], "unofficial") {
			return -1
		}
		curNum, curErr := strconv.ParseInt(cur[i], 10, 32)
		otherNum, otherErr := strconv.ParseInt(other[i], 10, 32)
		if curErr == nil && otherErr == nil {
			return compareInt(int(curNum), int(otherNum))
		}
		return compareOrdinalIgnoreCase(cur[i], other[i])
	}
	return compareOrdinalIgnoreCase(v.String(), o.String())
}

// splitTag splits a prerelease tag on periods and hyphens, keeping empty parts like String.Split.
func splitTag(tag string) []string {
	parts := []string{}
	start := 0
	for i := 0; i < len(tag); i++ {
		if tag[i] == '.' || tag[i] == '-' {
			parts = append(parts, tag[start:i])
			start = i + 1
		}
	}
	return append(parts, tag[start:])
}

// compareOrdinalIgnoreCase compares two strings by their upper-cased code points.
func compareOrdinalIgnoreCase(a, b string) int {
	return strings.Compare(strings.ToUpper(a), strings.ToUpper(b))
}

// compareInt returns the ordering of two integers.
func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// sign clamps n to -1, 0 or 1.
func sign(n int) int {
	return compareInt(n, 0)
}

// IsNewerThan reports whether v supersedes o.
func (v Version) IsNewerThan(o Version) bool {
	return v.Compare(o) > 0
}

// IsZero reports whether v is the unset zero version.
func (v Version) IsZero() bool {
	return v == Version{}
}

// UnmarshalJSON decodes a version string, the legacy object form, or null.
func (v *Version) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case len(b) == 0 || bytes.Equal(b, []byte("null")):
		*v = Version{}
		return nil
	case b[0] == '"':
		var text string
		if err := json.Unmarshal(b, &text); err != nil {
			return fmt.Errorf("%w: decoding version string: %w", ErrInvalidVersion, err)
		}
		if strings.TrimSpace(text) == "" {
			*v = Version{}
			return nil
		}
		parsed, err := ParseVersion(text, false)
		if err != nil {
			return err
		}
		*v = parsed
		return nil
	case b[0] == '{':
		parsed, err := readLegacyVersion(b)
		if err != nil {
			return err
		}
		*v = parsed
		return nil
	}
	return fmt.Errorf("%w: can't parse a version from %s", ErrInvalidVersion, b)
}

// readLegacyVersion ports SemanticVersionConverter.ReadObject for the MajorVersion/MinorVersion/PatchVersion/PrereleaseTag form.
func readLegacyVersion(b []byte) (Version, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return Version{}, fmt.Errorf("%w: decoding legacy version: %w", ErrInvalidVersion, err)
	}
	var v Version
	var err error
	if v.Major, err = legacyInt(raw, "MajorVersion"); err != nil {
		return Version{}, err
	}
	if v.Minor, err = legacyInt(raw, "MinorVersion"); err != nil {
		return Version{}, err
	}
	if v.Patch, err = legacyInt(raw, "PatchVersion"); err != nil {
		return Version{}, err
	}
	if field := lookupIgnoreCase(raw, "PrereleaseTag"); field != nil {
		var text lenientString
		if err := json.Unmarshal(field, &text); err != nil {
			return Version{}, fmt.Errorf("%w: legacy PrereleaseTag: %w", ErrInvalidVersion, err)
		}
		v.Prerelease = strings.TrimSpace(text.Value)
	}
	if err := v.validate(); err != nil {
		return Version{}, fmt.Errorf("%w: legacy version %s: %s", ErrInvalidVersion, v.String(), err.Error())
	}
	return v, nil
}

// legacyInt reads one legacy version number like Newtonsoft's Value<int>, where only an absent field defaults to zero.
func legacyInt(raw map[string]json.RawMessage, name string) (int, error) {
	field := bytes.TrimSpace(lookupIgnoreCase(raw, name))
	switch {
	case len(field) == 0:
		return 0, nil
	case bytes.Equal(field, []byte("null")):
		return 0, fmt.Errorf("%w: legacy %s is null", ErrInvalidVersion, name)
	case bytes.Equal(field, []byte("true")):
		return 1, nil
	case bytes.Equal(field, []byte("false")):
		return 0, nil
	case field[0] == '"':
		var text string
		if err := json.Unmarshal(field, &text); err != nil {
			return 0, fmt.Errorf("%w: legacy %s: %w", ErrInvalidVersion, name, err)
		}
		value, err := strconv.ParseInt(strings.TrimSpace(text), 10, 32)
		if err != nil {
			return 0, fmt.Errorf("%w: legacy %s: %w", ErrInvalidVersion, name, err)
		}
		return int(value), nil
	case field[0] == '-' || field[0] >= '0' && field[0] <= '9':
		return legacyNumber(field, name)
	}
	return 0, fmt.Errorf("%w: legacy %s must be a number, got %s", ErrInvalidVersion, name, field)
}

// legacyNumber converts a JSON number like Convert.ToInt32, rounding fractions half to even and rejecting overflow.
func legacyNumber(field []byte, name string) (int, error) {
	text := string(field)
	if !strings.ContainsAny(text, ".eE") {
		value, err := strconv.ParseInt(text, 10, 32)
		if err != nil {
			return 0, fmt.Errorf("%w: legacy %s: %w", ErrInvalidVersion, name, err)
		}
		return int(value), nil
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: legacy %s: %w", ErrInvalidVersion, name, err)
	}
	rounded := math.RoundToEven(value)
	if rounded < math.MinInt32 || rounded > math.MaxInt32 {
		return 0, fmt.Errorf("%w: legacy %s %s is out of range", ErrInvalidVersion, name, text)
	}
	return int(rounded), nil
}

// lookupIgnoreCase returns the field named name, preferring an exact match over a case-insensitive one.
func lookupIgnoreCase(raw map[string]json.RawMessage, name string) json.RawMessage {
	if value, ok := raw[name]; ok {
		return value
	}
	keys := make([]string, 0, len(raw))
	for key := range raw {
		if strings.EqualFold(key, name) {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	return raw[keys[0]]
}

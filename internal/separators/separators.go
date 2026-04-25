// Package separators is per-profile storage for MO2-style mod-list
// separators. Separators are a pure UI construct — they group mods
// visually without affecting conflict resolution or load order. The
// daemon never cares about them at launch time; we persist them here so
// the Qt frontend can restore the user's visual layout across sessions.
package separators

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Separator is a named row the user inserts between mods in Visual mode.
// Depth is always 1 — separators cannot be nested.
type Separator struct {
	// Name is the user-facing label and the stable identifier referenced
	// from each mod's metadata.yaml `separator:` field.
	Name string
	// VisualIndex is a 16-char lowercase hex string whose integer value
	// determines the separator's row in Visual mode. Uses the same index
	// space as mod visual indices (they sort together).
	VisualIndex string
	// Collapsed persists the fold-out state of the separator so Visual
	// mode reopens exactly where the user left it.
	Collapsed bool
}

// Index returns the separator's visual index as a uint64. Returns 0 on
// parse failure, which naturally sorts the separator to the top —
// matches the "fall back to beginning" expectation.
func (s Separator) Index() uint64 {
	v, _ := strconv.ParseUint(strings.TrimSpace(s.VisualIndex), 16, 64)
	return v
}

// Load reads separators.yaml from the given profile directory. Missing
// file → empty list + nil error (first-use case).
func Load(profileDir string) ([]Separator, error) {
	path := filepath.Join(profileDir, "separators.yaml")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	var out []Separator
	var cur *Separator
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		raw := scanner.Text()
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || line == "separators:" {
			continue
		}
		if strings.HasPrefix(line, "- ") {
			if cur != nil {
				out = append(out, *cur)
			}
			cur = &Separator{}
			line = strings.TrimPrefix(line, "- ")
		}
		if cur == nil {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(strings.Trim(strings.TrimSpace(v), `"`))
		switch k {
		case "name":
			cur.Name = v
		case "visual_index":
			cur.VisualIndex = v
		case "collapsed":
			cur.Collapsed = (v == "true")
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out, scanner.Err()
}

// Save writes separators.yaml atomically. Creates the profile dir if
// needed. An empty list produces an empty-list file rather than
// deleting — simplifies the read-modify-write cycle the RPC uses.
func Save(profileDir string, sep []Separator) error {
	if err := os.MkdirAll(profileDir, 0755); err != nil {
		return fmt.Errorf("creating %s: %w", profileDir, err)
	}
	path := filepath.Join(profileDir, "separators.yaml")

	var b strings.Builder
	b.WriteString("# Gorganizer separators — visual grouping only. Safe to delete.\n")
	b.WriteString("separators:\n")
	for _, s := range sep {
		fmt.Fprintf(&b, "  - name: %q\n", s.Name)
		fmt.Fprintf(&b, "    visual_index: %q\n", s.VisualIndex)
		fmt.Fprintf(&b, "    collapsed: %t\n", s.Collapsed)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// FormatIndex converts a uint64 into the canonical 16-hex-char form
// used in the yaml. Zero-padded so string sort == numeric sort.
func FormatIndex(v uint64) string {
	return fmt.Sprintf("%016x", v)
}

// ParseIndex returns the numeric value of a hex index string, or 0 on
// parse failure.
func ParseIndex(s string) uint64 {
	v, _ := strconv.ParseUint(strings.TrimSpace(s), 16, 64)
	return v
}

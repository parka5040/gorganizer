// Package separators is per-profile storage for MO2-style mod-list separators.
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
type Separator struct {
	Name        string
	VisualIndex string
	Collapsed   bool
}

// Layout bundles the per-profile separator order with the persistent
// "Separator View" checkbox state. Both live in separators.yaml so the
// view follows the profile.
type Layout struct {
	ViewEnabled bool
	Separators  []Separator
}

// Index returns the separator's visual index as a uint64; 0 on parse failure.
func (s Separator) Index() uint64 {
	v, _ := strconv.ParseUint(strings.TrimSpace(s.VisualIndex), 16, 64)
	return v
}

// LoadLayout reads separators.yaml from the given profile directory.
// Missing file → empty layout with ViewEnabled=false.
func LoadLayout(profileDir string) (Layout, error) {
	path := filepath.Join(profileDir, "separators.yaml")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Layout{}, nil
		}
		return Layout{}, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	var out Layout
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
				out.Separators = append(out.Separators, *cur)
			}
			cur = &Separator{}
			line = strings.TrimPrefix(line, "- ")
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(strings.Trim(strings.TrimSpace(v), `"`))
		// Top-level keys (no leading "- ", no current separator block).
		if cur == nil {
			if k == "view_enabled" {
				out.ViewEnabled = (v == "true")
			}
			continue
		}
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
		out.Separators = append(out.Separators, *cur)
	}
	return out, scanner.Err()
}

// SaveLayout writes separators.yaml atomically.
func SaveLayout(profileDir string, l Layout) error {
	if err := os.MkdirAll(profileDir, 0755); err != nil {
		return fmt.Errorf("creating %s: %w", profileDir, err)
	}
	path := filepath.Join(profileDir, "separators.yaml")

	var b strings.Builder
	b.WriteString("# Gorganizer separators — visual grouping only. Safe to delete.\n")
	fmt.Fprintf(&b, "view_enabled: %t\n", l.ViewEnabled)
	b.WriteString("separators:\n")
	for _, s := range l.Separators {
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

// Load is the legacy separator-only reader; kept for callers that don't
// care about the view-enabled flag.
func Load(profileDir string) ([]Separator, error) {
	l, err := LoadLayout(profileDir)
	if err != nil {
		return nil, err
	}
	return l.Separators, nil
}

// Save is the legacy separator-only writer; preserves the existing
// view_enabled value on disk so it isn't accidentally clobbered.
func Save(profileDir string, sep []Separator) error {
	prev, _ := LoadLayout(profileDir)
	return SaveLayout(profileDir, Layout{ViewEnabled: prev.ViewEnabled, Separators: sep})
}

// FormatIndex converts a uint64 to the 16-hex-char form used in the yaml.
func FormatIndex(v uint64) string {
	return fmt.Sprintf("%016x", v)
}

// ParseIndex returns the numeric value of a hex index string, or 0.
func ParseIndex(s string) uint64 {
	v, _ := strconv.ParseUint(strings.TrimSpace(s), 16, 64)
	return v
}

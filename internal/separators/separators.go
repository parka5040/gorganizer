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

// Index returns the separator's visual index as a uint64; 0 on parse failure.
func (s Separator) Index() uint64 {
	v, _ := strconv.ParseUint(strings.TrimSpace(s.VisualIndex), 16, 64)
	return v
}

// Load reads separators.yaml from the given profile directory.
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

// Save writes separators.yaml atomically.
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

// FormatIndex converts a uint64 to the 16-hex-char form used in the yaml.
func FormatIndex(v uint64) string {
	return fmt.Sprintf("%016x", v)
}

// ParseIndex returns the numeric value of a hex index string, or 0.
func ParseIndex(s string) uint64 {
	v, _ := strconv.ParseUint(strings.TrimSpace(s), 16, 64)
	return v
}

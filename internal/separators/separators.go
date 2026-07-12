package separators

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/parka/gorganizer/internal/kvfile"
)

type Separator struct {
	Name        string
	VisualIndex string
	Collapsed   bool
}

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
	sc := kvfile.NewScanner(f)
	for sc.Scan() {
		l := sc.Line()
		if l.Text == "separators:" {
			continue
		}
		if l.IsListItem {
			if cur != nil {
				out.Separators = append(out.Separators, *cur)
			}
			cur = &Separator{}
		}
		k, v, ok := kvfile.CutKV(l.Item)
		if !ok {
			continue
		}
		v = kvfile.UnquoteValue(v)
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
	return out, sc.Err()
}

// SaveLayout writes separators.yaml atomically.
func SaveLayout(profileDir string, l Layout) error {
	if err := os.MkdirAll(profileDir, 0755); err != nil {
		return fmt.Errorf("creating %s: %w", profileDir, err)
	}
	path := filepath.Join(profileDir, "separators.yaml")

	var w kvfile.Writer
	w.Comment("Gorganizer separators — visual grouping only. Safe to delete.")
	w.KVBool("view_enabled", l.ViewEnabled)
	w.ListHeader("separators")
	for _, s := range l.Separators {
		w.ItemQuoted("name", s.Name)
		w.ContQuoted("visual_index", s.VisualIndex)
		w.ContBool("collapsed", s.Collapsed)
	}
	return w.WriteAtomic(path, 0644)
}

// Load is the legacy separator-only reader; kept for callers that don't
func Load(profileDir string) ([]Separator, error) {
	l, err := LoadLayout(profileDir)
	if err != nil {
		return nil, err
	}
	return l.Separators, nil
}

// Save is the legacy separator-only writer; preserves the existing
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

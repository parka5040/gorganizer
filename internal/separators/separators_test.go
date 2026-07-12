package separators

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestLayoutRoundTrip locks SaveLayout→LoadLayout behavior including quirky names.
func TestLayoutRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   Layout
		want Layout
	}{
		{
			name: "empty layout",
			in:   Layout{},
			want: Layout{},
		},
		{
			name: "empty non-nil slice loads back as nil",
			in:   Layout{Separators: []Separator{}},
			want: Layout{},
		},
		{
			name: "view enabled with no separators",
			in:   Layout{ViewEnabled: true},
			want: Layout{ViewEnabled: true},
		},
		{
			name: "plain separators with collapsed flags",
			in: Layout{
				ViewEnabled: true,
				Separators: []Separator{
					{Name: "Textures", VisualIndex: "0000000000000010", Collapsed: false},
					{Name: "Gameplay", VisualIndex: "00000000000000ff", Collapsed: true},
				},
			},
			want: Layout{
				ViewEnabled: true,
				Separators: []Separator{
					{Name: "Textures", VisualIndex: "0000000000000010", Collapsed: false},
					{Name: "Gameplay", VisualIndex: "00000000000000ff", Collapsed: true},
				},
			},
		},
		{
			name: "colon hash and unicode names survive",
			in: Layout{
				Separators: []Separator{
					{Name: "Late: patches", VisualIndex: "0000000000000001"},
					{Name: "mods # heavy", VisualIndex: "0000000000000002"},
					{Name: "日本語 séparateur", VisualIndex: "0000000000000003"},
				},
			},
			want: Layout{
				Separators: []Separator{
					{Name: "Late: patches", VisualIndex: "0000000000000001"},
					{Name: "mods # heavy", VisualIndex: "0000000000000002"},
					{Name: "日本語 séparateur", VisualIndex: "0000000000000003"},
				},
			},
		},
		{
			name: "embedded double quote gains escape backslash on reload",
			in: Layout{
				Separators: []Separator{
					{Name: `he"llo`, VisualIndex: "0000000000000004"},
				},
			},
			want: Layout{
				Separators: []Separator{
					{Name: `he\"llo`, VisualIndex: "0000000000000004"},
				},
			},
		},
		{
			name: "fully quoted name loses outer quotes and keeps escapes",
			in: Layout{
				Separators: []Separator{
					{Name: `"quoted"`, VisualIndex: "0000000000000005"},
				},
			},
			want: Layout{
				Separators: []Separator{
					{Name: `\"quoted\`, VisualIndex: "0000000000000005"},
				},
			},
		},
		{
			name: "empty name",
			in: Layout{
				Separators: []Separator{
					{Name: "", VisualIndex: "0000000000000006", Collapsed: true},
				},
			},
			want: Layout{
				Separators: []Separator{
					{Name: "", VisualIndex: "0000000000000006", Collapsed: true},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := SaveLayout(dir, tt.in); err != nil {
				t.Fatalf("SaveLayout() error = %v", err)
			}
			got, err := LoadLayout(dir)
			if err != nil {
				t.Fatalf("LoadLayout() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("LoadLayout() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// TestLoadLayoutMissingFile locks the empty-layout nil-error contract.
func TestLoadLayoutMissingFile(t *testing.T) {
	tests := []struct {
		name string
		dir  func(t *testing.T) string
	}{
		{name: "existing dir without file", dir: func(t *testing.T) string { return t.TempDir() }},
		{name: "nonexistent dir", dir: func(t *testing.T) string { return filepath.Join(t.TempDir(), "nope") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := LoadLayout(tt.dir(t))
			if err != nil {
				t.Fatalf("LoadLayout() error = %v, want nil", err)
			}
			if !reflect.DeepEqual(got, Layout{}) {
				t.Fatalf("LoadLayout() = %#v, want empty Layout", got)
			}
		})
	}
}

// TestSaveCreatesProfileDir locks that SaveLayout mkdirs the profile directory.
func TestSaveCreatesProfileDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "profiles", "default")
	if err := SaveLayout(dir, Layout{ViewEnabled: true}); err != nil {
		t.Fatalf("SaveLayout() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "separators.yaml")); err != nil {
		t.Fatalf("separators.yaml not written: %v", err)
	}
}

// TestLegacySavePreservesViewEnabled locks that Save keeps the on-disk view flag.
func TestLegacySavePreservesViewEnabled(t *testing.T) {
	dir := t.TempDir()
	if err := SaveLayout(dir, Layout{ViewEnabled: true, Separators: []Separator{{Name: "Old", VisualIndex: "0000000000000001"}}}); err != nil {
		t.Fatal(err)
	}
	newSeps := []Separator{{Name: "New", VisualIndex: "0000000000000002", Collapsed: true}}
	if err := Save(dir, newSeps); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := LoadLayout(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := Layout{ViewEnabled: true, Separators: newSeps}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadLayout() = %#v, want %#v", got, want)
	}
}

// TestLegacyLoad locks that Load returns only the separator slice.
func TestLegacyLoad(t *testing.T) {
	dir := t.TempDir()
	seps := []Separator{{Name: "A", VisualIndex: "000000000000000a"}}
	if err := SaveLayout(dir, Layout{ViewEnabled: true, Separators: seps}); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(got, seps) {
		t.Fatalf("Load() = %#v, want %#v", got, seps)
	}
}

// TestIndexHelpers locks hex index formatting, parsing, and the zero-on-failure rule.
func TestIndexHelpers(t *testing.T) {
	tests := []struct {
		name       string
		value      uint64
		formatted  string
		parseInput string
		parsed     uint64
	}{
		{name: "zero", value: 0, formatted: "0000000000000000", parseInput: "0000000000000000", parsed: 0},
		{name: "small", value: 16, formatted: "0000000000000010", parseInput: "0000000000000010", parsed: 16},
		{name: "large", value: 0xdeadbeef, formatted: "00000000deadbeef", parseInput: "00000000deadbeef", parsed: 0xdeadbeef},
		{name: "whitespace trimmed", value: 255, formatted: "00000000000000ff", parseInput: "  ff  ", parsed: 255},
		{name: "garbage parses to zero", value: 1, formatted: "0000000000000001", parseInput: "not-hex", parsed: 0},
		{name: "empty parses to zero", value: 1, formatted: "0000000000000001", parseInput: "", parsed: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatIndex(tt.value); got != tt.formatted {
				t.Fatalf("FormatIndex(%d) = %q, want %q", tt.value, got, tt.formatted)
			}
			if got := ParseIndex(tt.parseInput); got != tt.parsed {
				t.Fatalf("ParseIndex(%q) = %d, want %d", tt.parseInput, got, tt.parsed)
			}
			if got := (Separator{VisualIndex: tt.parseInput}).Index(); got != tt.parsed {
				t.Fatalf("Separator.Index() with %q = %d, want %d", tt.parseInput, got, tt.parsed)
			}
		})
	}
}

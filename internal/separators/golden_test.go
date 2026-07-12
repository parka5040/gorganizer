package separators

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSaveLayoutGoldenBytes(t *testing.T) {
	tests := []struct {
		name   string
		layout Layout
		want   string
	}{
		{
			name:   "empty layout",
			layout: Layout{},
			want: `# Gorganizer separators — visual grouping only. Safe to delete.
view_enabled: false
separators:
`,
		},
		{
			name: "view enabled with entries",
			layout: Layout{
				ViewEnabled: true,
				Separators: []Separator{
					{Name: "Core", VisualIndex: "0000000000000010", Collapsed: false},
					{Name: "Extra Utilities", VisualIndex: "0000000000000120", Collapsed: true},
				},
			},
			want: `# Gorganizer separators — visual grouping only. Safe to delete.
view_enabled: true
separators:
  - name: "Core"
    visual_index: "0000000000000010"
    collapsed: false
  - name: "Extra Utilities"
    visual_index: "0000000000000120"
    collapsed: true
`,
		},
		{
			name: "quirky names escaped by percent q",
			layout: Layout{
				Separators: []Separator{
					{Name: `Say "Hi"`, VisualIndex: "0000000000000010"},
					{Name: "héllo — ★ 日本語", VisualIndex: "0000000000000020"},
					{Name: "colon: and # hash", VisualIndex: "0000000000000030"},
					{Name: "  padded  ", VisualIndex: "0000000000000040"},
					{Name: "", VisualIndex: "0000000000000050"},
				},
			},
			want: `# Gorganizer separators — visual grouping only. Safe to delete.
view_enabled: false
separators:
  - name: "Say \"Hi\""
    visual_index: "0000000000000010"
    collapsed: false
  - name: "héllo — ★ 日本語"
    visual_index: "0000000000000020"
    collapsed: false
  - name: "colon: and # hash"
    visual_index: "0000000000000030"
    collapsed: false
  - name: "  padded  "
    visual_index: "0000000000000040"
    collapsed: false
  - name: ""
    visual_index: "0000000000000050"
    collapsed: false
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := SaveLayout(dir, tt.layout); err != nil {
				t.Fatalf("SaveLayout: %v", err)
			}
			got, err := os.ReadFile(filepath.Join(dir, "separators.yaml"))
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("bytes = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLayoutRoundTripQuirks(t *testing.T) {
	tests := []struct {
		name       string
		saved      string
		wantLoaded string
	}{
		{name: "plain", saved: "Core", wantLoaded: "Core"},
		{name: "unicode preserved", saved: "héllo — ★ 日本語", wantLoaded: "héllo — ★ 日本語"},
		{name: "colon preserved", saved: "colon: inside", wantLoaded: "colon: inside"},
		{name: "hash preserved", saved: "a # b", wantLoaded: "a # b"},
		{name: "empty stays empty", saved: "", wantLoaded: ""},
		{name: "inner quotes keep escapes", saved: `Say "Hi" now`, wantLoaded: `Say \"Hi\" now`},
		{name: "trailing quote becomes backslash", saved: `x"`, wantLoaded: `x\`},
		{name: "outer spaces dropped", saved: "  padded  ", wantLoaded: "padded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			in := Layout{ViewEnabled: true, Separators: []Separator{{Name: tt.saved, VisualIndex: "00000000000000a0", Collapsed: true}}}
			if err := SaveLayout(dir, in); err != nil {
				t.Fatalf("SaveLayout: %v", err)
			}
			got, err := LoadLayout(dir)
			if err != nil {
				t.Fatalf("LoadLayout: %v", err)
			}
			want := Layout{ViewEnabled: true, Separators: []Separator{{Name: tt.wantLoaded, VisualIndex: "00000000000000a0", Collapsed: true}}}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got %+v, want %+v", got, want)
			}
		})
	}
}

func TestLayoutByteStability(t *testing.T) {
	fixture := `# Gorganizer separators — visual grouping only. Safe to delete.
view_enabled: true
separators:
  - name: "Core"
    visual_index: "0000000000000010"
    collapsed: false
  - name: "Extra Utilities"
    visual_index: "0000000000000120"
    collapsed: false
  - name: "Fixes"
    visual_index: "0000000000000210"
    collapsed: true
  - name: "Optimization"
    visual_index: "0000000000000360"
    collapsed: false
`
	dir := t.TempDir()
	path := filepath.Join(dir, "separators.yaml")
	if err := os.WriteFile(path, []byte(fixture), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	loaded, err := LoadLayout(dir)
	if err != nil {
		t.Fatalf("LoadLayout: %v", err)
	}
	if err := SaveLayout(dir, loaded); err != nil {
		t.Fatalf("SaveLayout: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != fixture {
		t.Errorf("Save(Load(fixture)) = %q, want %q", got, fixture)
	}
}

func TestLoadLayoutMissingFileReturnsEmpty(t *testing.T) {
	got, err := LoadLayout(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("LoadLayout: %v", err)
	}
	if !reflect.DeepEqual(got, Layout{}) {
		t.Errorf("got %+v, want zero Layout", got)
	}
}

func TestLegacySaveKeepsViewEnabledOnDisk(t *testing.T) {
	dir := t.TempDir()
	if err := SaveLayout(dir, Layout{ViewEnabled: true}); err != nil {
		t.Fatalf("SaveLayout: %v", err)
	}
	if err := Save(dir, []Separator{{Name: "New", VisualIndex: "0000000000000010"}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := LoadLayout(dir)
	if err != nil {
		t.Fatalf("LoadLayout: %v", err)
	}
	if !got.ViewEnabled {
		t.Errorf("ViewEnabled = false, want true after legacy Save")
	}
	if len(got.Separators) != 1 || got.Separators[0].Name != "New" {
		t.Errorf("Separators = %+v, want single entry named New", got.Separators)
	}
}

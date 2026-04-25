package mod

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseModList(t *testing.T) {
	input := `# Gorganizer modlist — do not edit while daemon is running
+Unofficial Skyrim SE Patch
+SKSE64
+SkyUI
-Optional HD Textures
`
	entries, err := ParseModList(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseModList: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(entries))
	}

	expected := []ModListEntry{
		{Name: "Unofficial Skyrim SE Patch", Enabled: true},
		{Name: "SKSE64", Enabled: true},
		{Name: "SkyUI", Enabled: true},
		{Name: "Optional HD Textures", Enabled: false},
	}
	for i, e := range expected {
		if entries[i].Name != e.Name || entries[i].Enabled != e.Enabled {
			t.Errorf("entry %d: got {%q, %v}, want {%q, %v}",
				i, entries[i].Name, entries[i].Enabled, e.Name, e.Enabled)
		}
	}
}

func TestParseModListEmpty(t *testing.T) {
	entries, err := ParseModList(strings.NewReader(""))
	if err != nil {
		t.Fatalf("ParseModList: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestParseModListCommentsOnly(t *testing.T) {
	input := "# comment\n# another comment\n"
	entries, err := ParseModList(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseModList: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestParseModListBadPrefix(t *testing.T) {
	input := "NoPrefix\n"
	_, err := ParseModList(strings.NewReader(input))
	if err == nil {
		t.Error("expected error for line without +/- prefix")
	}
}

func TestWriteModList(t *testing.T) {
	entries := []ModListEntry{
		{Name: "Unofficial Skyrim SE Patch", Enabled: true},
		{Name: "SKSE64", Enabled: true},
		{Name: "Optional HD Textures", Enabled: false},
	}

	var buf bytes.Buffer
	if err := WriteModList(&buf, entries); err != nil {
		t.Fatalf("WriteModList: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "+Unofficial Skyrim SE Patch") {
		t.Error("expected +Unofficial Skyrim SE Patch in output")
	}
	if !strings.Contains(output, "+SKSE64") {
		t.Error("expected +SKSE64 in output")
	}
	if !strings.Contains(output, "-Optional HD Textures") {
		t.Error("expected -Optional HD Textures in output")
	}
}

func TestModListRoundTrip(t *testing.T) {
	entries := []ModListEntry{
		{Name: "Mod With Spaces", Enabled: true},
		{Name: "Another-Mod_v2.0", Enabled: false},
		{Name: "Simple", Enabled: true},
	}

	var buf bytes.Buffer
	if err := WriteModList(&buf, entries); err != nil {
		t.Fatalf("WriteModList: %v", err)
	}

	parsed, err := ParseModList(&buf)
	if err != nil {
		t.Fatalf("ParseModList: %v", err)
	}

	if len(parsed) != len(entries) {
		t.Fatalf("expected %d entries, got %d", len(entries), len(parsed))
	}
	for i, e := range entries {
		if parsed[i].Name != e.Name || parsed[i].Enabled != e.Enabled {
			t.Errorf("entry %d: got {%q, %v}, want {%q, %v}",
				i, parsed[i].Name, parsed[i].Enabled, e.Name, e.Enabled)
		}
	}
}

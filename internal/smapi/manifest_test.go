package smapi

import (
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"
)

// versionPtr parses s into a Version pointer for test expectations.
func versionPtr(s string) *Version {
	v := MustParseVersion(s)
	return &v
}

// TestParseManifestFull verifies normalization, lenient JSON and dependency decoding of a code mod manifest.
func TestParseManifestFull(t *testing.T) {
	input := "\xef\xbb\xbf{\n" +
		"  // SMAPI tolerates comments.\n" +
		"  \"Name\": \"  My [Cool]\\nMod \",\n" +
		"  \"Author\": \" Someone \",\n" +
		"  \"Version\": \"1.2.3-beta.1\",\n" +
		"  \"Description\": \"Line1\\r\\nLine2\",\n" +
		"  \"UniqueID\": \" Someone.MyMod \",\n" +
		"  \"EntryDll\": \" MyMod.dll \",\n" +
		"  \"MinimumApiVersion\": \"4.0.0\",\n" +
		"  \"MinimumGameVersion\": \"1.6\",\n" +
		"  /* dependencies */\n" +
		"  \"Dependencies\": [\n" +
		"    { \"UniqueID\": \" Pathoschild.ContentPatcher \", \"MinimumVersion\": \"2.0.0\" },\n" +
		"    { \"uniqueid\": \"spacechase0.SpaceCore\", \"isrequired\": false },\n" +
		"    null,\n" +
		"    \"not an object\",\n" +
		"    { \"UniqueID\": \"a.b\", \"MinimumVersion\": \"  \", \"IsRequired\": null },\n" +
		"  ],\n" +
		"  \"UpdateKeys\": [ \"Nexus:1915\", \"GitHub:a/b\" ],\n" +
		"  \"ExtraField\": { \"anything\": [1, 2, 3] },\n" +
		"}\n"
	got, err := ParseManifest([]byte(input))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	want := &Manifest{
		Name:               "My (Cool) Mod",
		Author:             "Someone",
		Description:        "Line1  Line2",
		Version:            Version{Major: 1, Minor: 2, Patch: 3, Prerelease: "beta.1"},
		UniqueID:           "Someone.MyMod",
		EntryDll:           "MyMod.dll",
		UpdateKeys:         []string{"Nexus:1915", "GitHub:a/b"},
		MinimumApiVersion:  versionPtr("4.0.0"),
		MinimumGameVersion: versionPtr("1.6.0"),
		Dependencies: []Dependency{
			{UniqueID: "Pathoschild.ContentPatcher", MinimumVersion: versionPtr("2.0.0"), IsRequired: true},
			{UniqueID: "spacechase0.SpaceCore", IsRequired: false},
			{UniqueID: "a.b", IsRequired: true},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseManifest = %#v, want %#v", got, want)
	}
	if got.Kind() != KindCode {
		t.Fatalf("Kind() = %v, want KindCode", got.Kind())
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// TestParseManifestVariants verifies alternative but valid manifest encodings.
func TestParseManifestVariants(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  *Manifest
	}{
		{
			name:  "case-insensitive keys",
			input: `{"name":"x","uniqueid":"a.b","VERSION":"1.0","entrydll":"a.dll"}`,
			want:  &Manifest{Name: "x", UniqueID: "a.b", Version: Version{Major: 1}, EntryDll: "a.dll"},
		},
		{
			name:  "content pack",
			input: `{"Name":"CP","Version":"1.0.0","UniqueID":"a.cp","ContentPackFor":{"UniqueID":" Pathoschild.ContentPatcher ","MinimumVersion":"1.20"}}`,
			want:  &Manifest{Name: "CP", UniqueID: "a.cp", Version: Version{Major: 1}, ContentPackFor: &ContentPackFor{UniqueID: "Pathoschild.ContentPatcher", MinimumVersion: versionPtr("1.20.0")}},
		},
		{
			name:  "content pack legacy minimum version",
			input: `{"Name":"CP","Version":"1.0.0","UniqueID":"a.cp","ContentPackFor":{"uniqueID":"x.y","minimumVersion":{"MajorVersion":1,"MinorVersion":5}}}`,
			want:  &Manifest{Name: "CP", UniqueID: "a.cp", Version: Version{Major: 1}, ContentPackFor: &ContentPackFor{UniqueID: "x.y", MinimumVersion: versionPtr("1.5.0")}},
		},
		{
			name:  "empty content pack object",
			input: `{"Name":"CP","Version":"1.0.0","UniqueID":"a.cp","ContentPackFor":{}}`,
			want:  &Manifest{Name: "CP", UniqueID: "a.cp", Version: Version{Major: 1}, ContentPackFor: &ContentPackFor{}},
		},
		{
			name:  "legacy version object",
			input: `{"Name":"Old","UniqueID":"old.mod","EntryDll":"Old.dll","Version":{"MajorVersion":1,"MinorVersion":0,"PatchVersion":2}}`,
			want:  &Manifest{Name: "Old", UniqueID: "old.mod", EntryDll: "Old.dll", Version: Version{Major: 1, Patch: 2}},
		},
		{
			name:  "blank versions are absent",
			input: `{"Name":"x","UniqueID":"a.b","EntryDll":"a.dll","Version":" ","MinimumApiVersion":"","MinimumGameVersion":null}`,
			want:  &Manifest{Name: "x", UniqueID: "a.b", EntryDll: "a.dll"},
		},
		{
			name:  "null collections",
			input: `{"Name":"x","UniqueID":"a.b","EntryDll":"a.dll","Version":"1.0","Dependencies":null,"UpdateKeys":null,"ContentPackFor":null}`,
			want:  &Manifest{Name: "x", UniqueID: "a.b", EntryDll: "a.dll", Version: Version{Major: 1}},
		},
		{
			name:  "scalar strings coerced",
			input: `{"Name":123,"Author":true,"UniqueID":"a.b","EntryDll":"a.dll","Version":"1.0","UpdateKeys":[42,null]}`,
			want:  &Manifest{Name: "123", Author: "True", UniqueID: "a.b", EntryDll: "a.dll", Version: Version{Major: 1}, UpdateKeys: []string{"42", ""}},
		},
		{
			name:  "Newtonsoft-tolerant syntax",
			input: "{\n\u00a0\u00a0Name: 'Tab\tName',\n\u00a0\u00a0'Version': '1.0',\n\u00a0\u00a0UniqueID: \"a.b\",\n\u00a0\u00a0EntryDll: 'a.dll',\n\u00a0\u00a0Description: \"one\ntwo\tthree \\'q\\'\",\n}",
			want:  &Manifest{Name: "Tab\tName", UniqueID: "a.b", EntryDll: "a.dll", Version: Version{Major: 1}, Description: "one two\tthree 'q'"},
		},
		{
			name:  "legacy version object with absent fields",
			input: `{"Name":"Old","UniqueID":"old.mod","EntryDll":"Old.dll","Version":{"MinorVersion":3}}`,
			want:  &Manifest{Name: "Old", UniqueID: "old.mod", EntryDll: "Old.dll", Version: Version{Minor: 3}},
		},
		{
			name:  "IsRequired string and number",
			input: `{"Name":"x","UniqueID":"a.b","EntryDll":"a.dll","Version":"1.0","Dependencies":[{"UniqueID":"c.d","IsRequired":"False"},{"UniqueID":"e.f","IsRequired":0},{"UniqueID":"g.h","IsRequired":1}]}`,
			want: &Manifest{Name: "x", UniqueID: "a.b", EntryDll: "a.dll", Version: Version{Major: 1}, Dependencies: []Dependency{
				{UniqueID: "c.d"}, {UniqueID: "e.f"}, {UniqueID: "g.h", IsRequired: true},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseManifest([]byte(tc.input))
			if err != nil {
				t.Fatalf("ParseManifest(%s): %v", tc.input, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseManifest(%s) = %#v, want %#v", tc.input, got, tc.want)
			}
		})
	}
}

// TestParseManifestUTF16 verifies a UTF-16 little-endian manifest with a byte order mark parses like its UTF-8 form.
func TestParseManifestUTF16(t *testing.T) {
	text := "{\r\n  \"Name\": \"Caf\u00e9\",\r\n  \"Version\": \"1.0.0\",\r\n  \"UniqueID\": \"a.cafe\",\r\n  \"EntryDll\": \"Cafe.dll\"\r\n}"
	data := []byte{0xff, 0xfe}
	for _, unit := range utf16.Encode([]rune(text)) {
		data = binary.LittleEndian.AppendUint16(data, unit)
	}
	got, err := ParseManifest(data)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	want := &Manifest{Name: "Caf\u00e9", Version: Version{Major: 1}, UniqueID: "a.cafe", EntryDll: "Cafe.dll"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseManifest = %#v, want %#v", got, want)
	}
}

// TestParseManifestErrors verifies malformed manifests fail with ErrInvalidManifest naming the bad field.
func TestParseManifestErrors(t *testing.T) {
	cases := []struct {
		name        string
		input       string
		contains    string
		wantVersion bool
		wantNull    bool
	}{
		{name: "invalid Version", input: `{"Version":"abc"}`, contains: "invalid Version", wantVersion: true},
		{name: "non-standard Version", input: `{"Version":"1.6.15.24356"}`, contains: "invalid Version", wantVersion: true},
		{name: "invalid MinimumApiVersion", input: `{"Version":"1.0","MinimumApiVersion":"x"}`, contains: "invalid MinimumApiVersion", wantVersion: true},
		{name: "invalid MinimumGameVersion", input: `{"Version":"1.0","MinimumGameVersion":"1.6.x"}`, contains: "invalid MinimumGameVersion", wantVersion: true},
		{name: "invalid dependency MinimumVersion", input: `{"Dependencies":[{"UniqueID":"a.b","MinimumVersion":"1.x"}]}`, contains: "invalid Dependencies[0].MinimumVersion", wantVersion: true},
		{name: "legacy dependency MinimumVersion object", input: `{"Dependencies":[{"UniqueID":"a.b","MinimumVersion":{"MajorVersion":1}}]}`, contains: "invalid Dependencies[0]"},
		{name: "invalid ContentPackFor MinimumVersion", input: `{"ContentPackFor":{"UniqueID":"a.b","MinimumVersion":"one"}}`, contains: "invalid ContentPackFor.MinimumVersion", wantVersion: true},
		{name: "ContentPackFor string", input: `{"ContentPackFor":"Pathoschild.ContentPatcher"}`, contains: "ContentPackFor must be an object"},
		{name: "Dependencies object", input: `{"Dependencies":{"UniqueID":"a.b"}}`, contains: "invalid Dependencies"},
		{name: "UpdateKeys string", input: `{"UpdateKeys":"Nexus:1"}`, contains: "invalid UpdateKeys"},
		{name: "Name object", input: `{"Name":{"x":1}}`, contains: "expected a string value"},
		{name: "IsRequired word", input: `{"Dependencies":[{"UniqueID":"a.b","IsRequired":"nope"}]}`, contains: "expected a boolean value"},
		{name: "null document", input: `null`, wantNull: true},
		{name: "empty document", input: " \n ", wantNull: true},
		{name: "comment-only document", input: "// nothing\n", wantNull: true},
		{name: "array document", input: `[]`, contains: "manifest must be a JSON object"},
		{name: "truncated JSON", input: `{"Name":`, contains: "unexpected end of JSON input"},
		{name: "trailing content", input: `{"Name":"a"} {"Name":"b"}`, contains: "after top-level value"},
		{name: "curly quotes", input: "{“Name”: “x”}", contains: "found curly quotes"},
		{name: "unterminated comment", input: `{"Name":"a" /*`, contains: "unterminated block comment"},
		{name: "legacy Version with null number", input: `{"Version":{"MajorVersion":null,"MinorVersion":1}}`, contains: "invalid Version", wantVersion: true},
		{name: "unterminated single-quoted string", input: `{'Name':'a}`, contains: "unterminated string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseManifest([]byte(tc.input))
			if !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("ParseManifest(%s) = %#v, %v; want ErrInvalidManifest", tc.input, got, err)
			}
			if got != nil {
				t.Fatalf("ParseManifest(%s) manifest = %#v, want nil", tc.input, got)
			}
			if tc.contains != "" && !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("ParseManifest(%s) error = %q, want it to contain %q", tc.input, err, tc.contains)
			}
			if errors.Is(err, ErrInvalidVersion) != tc.wantVersion {
				t.Fatalf("ParseManifest(%s) error = %q, ErrInvalidVersion match = %v, want %v", tc.input, err, !tc.wantVersion, tc.wantVersion)
			}
			if errors.Is(err, errNullManifest) != tc.wantNull {
				t.Fatalf("ParseManifest(%s) error = %q, null-manifest match = %v, want %v", tc.input, err, !tc.wantNull, tc.wantNull)
			}
		})
	}
}

// TestManifestKind verifies the ModScanner.ReadFolder mod type rules.
func TestManifestKind(t *testing.T) {
	cases := []struct {
		name     string
		manifest *Manifest
		want     ModKind
	}{
		{name: "nil manifest", manifest: nil, want: KindInvalid},
		{name: "code mod", manifest: &Manifest{EntryDll: "a.dll"}, want: KindCode},
		{name: "content pack", manifest: &Manifest{ContentPackFor: &ContentPackFor{UniqueID: "x.y"}}, want: KindContentPack},
		{name: "both", manifest: &Manifest{EntryDll: "a.dll", ContentPackFor: &ContentPackFor{UniqueID: "x.y"}}, want: KindInvalid},
		{name: "neither", manifest: &Manifest{}, want: KindInvalid},
		{name: "dll with blank content pack id", manifest: &Manifest{EntryDll: "a.dll", ContentPackFor: &ContentPackFor{UniqueID: " "}}, want: KindCode},
		{name: "blank content pack id only", manifest: &Manifest{ContentPackFor: &ContentPackFor{}}, want: KindInvalid},
		{name: "blank dll with content pack", manifest: &Manifest{EntryDll: "  ", ContentPackFor: &ContentPackFor{UniqueID: "x.y"}}, want: KindContentPack},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.manifest.Kind(); got != tc.want {
				t.Fatalf("Kind() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestManifestValidate verifies every ManifestValidator.TryValidateFields rule and its SMAPI wording.
func TestManifestValidate(t *testing.T) {
	code := func(edit func(*Manifest)) *Manifest {
		m := &Manifest{Name: "Mod", Version: Version{Major: 1}, UniqueID: "author.mod", EntryDll: "Mod.dll"}
		edit(m)
		return m
	}
	cases := []struct {
		name     string
		manifest *Manifest
		want     string
	}{
		{name: "valid code mod", manifest: code(func(*Manifest) {})},
		{name: "valid content pack", manifest: code(func(m *Manifest) { m.EntryDll = ""; m.ContentPackFor = &ContentPackFor{UniqueID: "x.y"} })},
		{name: "unicode slug", manifest: code(func(m *Manifest) { m.UniqueID = "Ünïcödé.Mod_1-2" })},
		{name: "unicode digit slug", manifest: code(func(m *Manifest) { m.UniqueID = "mod.٣" })},
		{name: "valid dependencies", manifest: code(func(m *Manifest) { m.Dependencies = []Dependency{{UniqueID: "a.b"}, {UniqueID: "c_d-e"}} })},
		{name: "nil manifest", manifest: nil, want: "manifest is missing."},
		{name: "both dll and content pack", manifest: code(func(m *Manifest) { m.ContentPackFor = &ContentPackFor{UniqueID: "x.y"} }), want: "manifest sets both EntryDll and ContentPackFor, which are mutually exclusive."},
		{name: "dll and blank content pack", manifest: code(func(m *Manifest) { m.ContentPackFor = &ContentPackFor{} }), want: "manifest sets both EntryDll and ContentPackFor, which are mutually exclusive."},
		{name: "neither dll nor content pack", manifest: code(func(m *Manifest) { m.EntryDll = " " }), want: "manifest has no EntryDll or ContentPackFor field; must specify one."},
		{name: "dll with colon", manifest: code(func(m *Manifest) { m.EntryDll = "bad:name.dll" }), want: "manifest has invalid filename 'bad:name.dll' for the EntryDll field."},
		{name: "dll with slash", manifest: code(func(m *Manifest) { m.EntryDll = "sub/Mod.dll" }), want: "manifest has invalid filename 'sub/Mod.dll' for the EntryDll field."},
		{name: "dll with backslash", manifest: code(func(m *Manifest) { m.EntryDll = `sub\Mod.dll` }), want: `manifest has invalid filename 'sub\Mod.dll' for the EntryDll field.`},
		{name: "dll with control character", manifest: code(func(m *Manifest) { m.EntryDll = "Mod\x01.dll" }), want: "manifest has invalid filename 'Mod\x01.dll' for the EntryDll field."},
		{name: "dll with wildcard", manifest: code(func(m *Manifest) { m.EntryDll = "Mod*.dll" }), want: "manifest has invalid filename 'Mod*.dll' for the EntryDll field."},
		{name: "content pack without id", manifest: code(func(m *Manifest) { m.EntryDll = ""; m.ContentPackFor = &ContentPackFor{UniqueID: " "} }), want: "manifest declares ContentPackFor without its required UniqueID field."},
		{name: "missing name", manifest: code(func(m *Manifest) { m.Name = " " }), want: "manifest is missing required fields (Name)."},
		{name: "missing version", manifest: code(func(m *Manifest) { m.Version = Version{} }), want: "manifest is missing required fields (Version)."},
		{name: "missing everything", manifest: code(func(m *Manifest) { m.Name, m.Version, m.UniqueID = "", Version{}, "" }), want: "manifest is missing required fields (Name, Version, UniqueID)."},
		{name: "id with space", manifest: code(func(m *Manifest) { m.UniqueID = "author mod" }), want: "manifest specifies an invalid ID (IDs must only contain letters, numbers, underscores, periods, or hyphens)."},
		{name: "id with slash", manifest: code(func(m *Manifest) { m.UniqueID = "author/mod" }), want: "manifest specifies an invalid ID (IDs must only contain letters, numbers, underscores, periods, or hyphens)."},
		{name: "dependency without id", manifest: code(func(m *Manifest) { m.Dependencies = []Dependency{{UniqueID: "a.b"}, {UniqueID: " "}} }), want: "manifest has a Dependencies entry with no UniqueID field."},
		{name: "dependency with invalid id", manifest: code(func(m *Manifest) { m.Dependencies = []Dependency{{UniqueID: "a b"}} }), want: "manifest has a Dependencies entry with an invalid UniqueID field (IDs must only contain letters, numbers, underscores, periods, or hyphens)."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.manifest.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("Validate() = %v, want ErrInvalidManifest", err)
			}
			if !strings.HasSuffix(err.Error(), ": "+tc.want) {
				t.Fatalf("Validate() = %q, want message %q", err, tc.want)
			}
		})
	}
}

// TestRequiredDependencies verifies optional dependencies are dropped and ContentPackFor is appended as required.
func TestRequiredDependencies(t *testing.T) {
	m := &Manifest{
		ContentPackFor: &ContentPackFor{UniqueID: "Pathoschild.ContentPatcher", MinimumVersion: versionPtr("2.0")},
		Dependencies: []Dependency{
			{UniqueID: "a.required", IsRequired: true, MinimumVersion: versionPtr("1.0")},
			{UniqueID: "b.optional"},
		},
	}
	want := []Dependency{
		{UniqueID: "a.required", IsRequired: true, MinimumVersion: versionPtr("1.0")},
		{UniqueID: "Pathoschild.ContentPatcher", IsRequired: true, MinimumVersion: versionPtr("2.0")},
	}
	if got := m.RequiredDependencies(); !reflect.DeepEqual(got, want) {
		t.Fatalf("RequiredDependencies() = %#v, want %#v", got, want)
	}
	var nilManifest *Manifest
	if got := nilManifest.RequiredDependencies(); got != nil {
		t.Fatalf("nil RequiredDependencies() = %#v, want nil", got)
	}
}

// TestNexusIDs verifies Nexus update keys are parsed like SMAPI's UpdateKey.Parse.
func TestNexusIDs(t *testing.T) {
	cases := []struct {
		name string
		keys []string
		want []int
	}{
		{name: "nil", keys: nil, want: nil},
		{name: "plain", keys: []string{"Nexus:1915"}, want: []int{1915}},
		{name: "case-insensitive site", keys: []string{"nexus:1915", "NEXUS:2"}, want: []int{1915, 2}},
		{name: "subkey", keys: []string{"Nexus:1915@main"}, want: []int{1915}},
		{name: "whitespace", keys: []string{"  Nexus : 1915 @ main "}, want: []int{1915}},
		{name: "deduplicated", keys: []string{"Nexus:1", "nexus:1@x", "Nexus:2"}, want: []int{1, 2}},
		{name: "other sites and invalid ids", keys: []string{"GitHub:a/b", "ModDrop:123", "Nexus:abc", "Nexus:", "Nexus:-5", "Nexus:0", "Nexus:1:2", "Nexus:12@a@b", "Nexus", ""}, want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NexusIDs(tc.keys); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("NexusIDs(%q) = %v, want %v", tc.keys, got, tc.want)
			}
		})
	}
}

// TestSameID verifies mod IDs compare trimmed and case-insensitively.
func TestSameID(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{a: "Pathoschild.ContentPatcher", b: "pathoschild.contentpatcher", want: true},
		{a: " a.b ", b: "A.B", want: true},
		{a: "a.b", b: "a.c", want: false},
		{a: "", b: " ", want: true},
	}
	for _, tc := range cases {
		if got := SameID(tc.a, tc.b); got != tc.want {
			t.Fatalf("SameID(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

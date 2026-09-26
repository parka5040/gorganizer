package smapi

import (
	"encoding/json"
	"errors"
	"testing"
)

// TestParseVersionValid verifies SemanticVersionReader accepts the supported forms and String formats them.
func TestParseVersionValid(t *testing.T) {
	cases := []struct {
		input       string
		nonStandard bool
		want        Version
		text        string
	}{
		{input: "1.0", want: Version{Major: 1}, text: "1.0.0"},
		{input: "1.0.0", want: Version{Major: 1}, text: "1.0.0"},
		{input: "  1.2.3\t", want: Version{Major: 1, Minor: 2, Patch: 3}, text: "1.2.3"},
		{input: "0.1", want: Version{Minor: 1}, text: "0.1.0"},
		{input: "0.0.1", want: Version{Patch: 1}, text: "0.0.1"},
		{input: "10.20.30", want: Version{Major: 10, Minor: 20, Patch: 30}, text: "10.20.30"},
		{input: "1.2.3-beta", want: Version{Major: 1, Minor: 2, Patch: 3, Prerelease: "beta"}, text: "1.2.3-beta"},
		{input: "1.2-alpha", want: Version{Major: 1, Minor: 2, Prerelease: "alpha"}, text: "1.2.0-alpha"},
		{input: "1.0.0-beta.2", want: Version{Major: 1, Prerelease: "beta.2"}, text: "1.0.0-beta.2"},
		{input: "1.0.0-beta-2", want: Version{Major: 1, Prerelease: "beta-2"}, text: "1.0.0-beta-2"},
		{input: "1.0.0-beta.", want: Version{Major: 1, Prerelease: "beta."}, text: "1.0.0-beta."},
		{input: "1.0.0-unofficial.1-pathoschild", want: Version{Major: 1, Prerelease: "unofficial.1-pathoschild"}, text: "1.0.0-unofficial.1-pathoschild"},
		{input: "1.2.3+build.5", want: Version{Major: 1, Minor: 2, Patch: 3, Build: "build.5"}, text: "1.2.3+build.5"},
		{input: "1.2.3-rc.1+sha.abc", want: Version{Major: 1, Minor: 2, Patch: 3, Prerelease: "rc.1", Build: "sha.abc"}, text: "1.2.3-rc.1+sha.abc"},
		{input: "1.2-RC1", want: Version{Major: 1, Minor: 2, Prerelease: "RC1"}, text: "1.2.0-RC1"},
		{input: "2147483647.0", want: Version{Major: 2147483647}, text: "2147483647.0.0"},
		{input: "1.6.15.24356", nonStandard: true, want: Version{Major: 1, Minor: 6, Patch: 15, Platform: 24356}, text: "1.6.15.24356"},
		{input: "1.6.15.0", nonStandard: true, want: Version{Major: 1, Minor: 6, Patch: 15}, text: "1.6.15"},
		{input: "1.6.15.3-beta+b", nonStandard: true, want: Version{Major: 1, Minor: 6, Patch: 15, Platform: 3, Prerelease: "beta", Build: "b"}, text: "1.6.15.3-beta+b"},
		{input: "1.6", nonStandard: true, want: Version{Major: 1, Minor: 6}, text: "1.6.0"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, err := ParseVersion(tc.input, tc.nonStandard)
			if err != nil {
				t.Fatalf("ParseVersion(%q, %v): %v", tc.input, tc.nonStandard, err)
			}
			if got != tc.want {
				t.Fatalf("ParseVersion(%q, %v) = %#v, want %#v", tc.input, tc.nonStandard, got, tc.want)
			}
			if got.String() != tc.text {
				t.Fatalf("ParseVersion(%q).String() = %q, want %q", tc.input, got.String(), tc.text)
			}
		})
	}
}

// TestParseVersionInvalid verifies malformed versions are rejected with ErrInvalidVersion.
func TestParseVersionInvalid(t *testing.T) {
	cases := []struct {
		input       string
		nonStandard bool
	}{
		{input: ""},
		{input: "   "},
		{input: "1"},
		{input: "1."},
		{input: "1.0."},
		{input: ".1.0"},
		{input: "v1.0"},
		{input: "-1.0"},
		{input: "01.0"},
		{input: "1.01"},
		{input: "1.0.01"},
		{input: "0.0"},
		{input: "0.0.0"},
		{input: "0.0.0.5", nonStandard: true},
		{input: "1.0.0.1"},
		{input: "1.6.15.24356"},
		{input: "1.0.0.1.2", nonStandard: true},
		{input: "1.0.0.01", nonStandard: true},
		{input: "1.0-"},
		{input: "1.0+"},
		{input: "1.0.0--beta"},
		{input: "1.0.0-.beta"},
		{input: "1.0.0-beta..2"},
		{input: "1.0.0-beta.-2"},
		{input: "1.0.0-béta"},
		{input: "1.0.0-beta_1"},
		{input: "1.0.0-beta+"},
		{input: "1.0.0+build+x"},
		{input: "1.0.0 beta"},
		{input: "1.0.0-beta 2"},
		{input: "2147483648.0"},
		{input: "1.0.99999999999"},
		{input: "1.٣"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, err := ParseVersion(tc.input, tc.nonStandard)
			if !errors.Is(err, ErrInvalidVersion) {
				t.Fatalf("ParseVersion(%q, %v) = %v, %v; want ErrInvalidVersion", tc.input, tc.nonStandard, got, err)
			}
		})
	}
}

// TestVersionCompare verifies the SemanticVersion.CompareTo ordering rules.
func TestVersionCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{a: "1.0", b: "1.0.0", want: 0},
		{a: "1.0.0", b: "1.0.0", want: 0},
		{a: "0.1", b: "1.0", want: -1},
		{a: "2.0", b: "1.99.99", want: 1},
		{a: "1.10", b: "1.9", want: 1},
		{a: "1.0.1", b: "1.0.0", want: 1},
		{a: "1.0.0-alpha", b: "1.0.0", want: -1},
		{a: "1.0.0", b: "1.0.0-rc.9", want: 1},
		{a: "1.0.0-beta.2", b: "1.0.0-beta.10", want: -1},
		{a: "1.0.0-beta.10", b: "1.0.0-beta.2", want: 1},
		{a: "1.0.0-alpha", b: "1.0.0-beta", want: -1},
		{a: "1.0.0-rc.1", b: "1.0.0-beta.5", want: 1},
		{a: "1.0.0-2", b: "1.0.0-10", want: -1},
		{a: "1.0.0-a1", b: "1.0.0-a10", want: -1},
		{a: "1.0.0-beta", b: "1.0.0-beta.1", want: -1},
		{a: "1.0.0-beta.1", b: "1.0.0-beta.1.1", want: -1},
		{a: "1.2.3-beta", b: "1.2.3-beta-2", want: -1},
		{a: "1.0.0-beta.", b: "1.0.0-beta", want: 1},
		{a: "1.0.0-Beta", b: "1.0.0-beta", want: 0},
		{a: "1.0.0-beta.01", b: "1.0.0-beta.1", want: 0},
		{a: "1.0.0-beta.01.5", b: "1.0.0-beta.1.2", want: 0},
		{a: "1.0.0-unofficial.1-pathoschild", b: "1.0.0-beta", want: -1},
		{a: "1.0.0-beta", b: "1.0.0-unofficial.1-pathoschild", want: 1},
		{a: "1.0.0-unofficial", b: "1.0.0-alpha", want: -1},
		{a: "1.0.0-beta.unofficial", b: "1.0.0-beta.1", want: -1},
		{a: "1.0.0-unofficial", b: "1.0.0-Unofficial", want: 1},
		{a: "1.0.0-Unofficial", b: "1.0.0-unofficial", want: 1},
		{a: "1.0.0+build1", b: "1.0.0+build2", want: 0},
		{a: "1.0.0-alpha+x", b: "1.0.0-alpha+y", want: 0},
		{a: "1.6.15.24356", b: "1.6.15", want: 1},
		{a: "1.6.15", b: "1.6.15.24356", want: -1},
		{a: "1.6.15.24356", b: "1.6.16", want: -1},
		{a: "1.6.15.2", b: "1.6.15.10", want: -1},
		{a: "1.6.15.0", b: "1.6.15", want: 0},
		{a: "1.6.15.1-beta", b: "1.6.15.1", want: -1},
	}
	for _, tc := range cases {
		t.Run(tc.a+" vs "+tc.b, func(t *testing.T) {
			a, b := MustParseVersion(tc.a), MustParseVersion(tc.b)
			if got := a.Compare(b); got != tc.want {
				t.Fatalf("%s.Compare(%s) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
			if got := a.IsNewerThan(b); got != (tc.want > 0) {
				t.Fatalf("%s.IsNewerThan(%s) = %v, want %v", tc.a, tc.b, got, tc.want > 0)
			}
		})
	}
}

// TestVersionIsZero verifies only the unset version reports zero.
func TestVersionIsZero(t *testing.T) {
	if !(Version{}).IsZero() {
		t.Fatal("Version{}.IsZero() = false, want true")
	}
	for _, v := range []Version{{Major: 1}, {Platform: 1}, {Prerelease: "x"}, {Build: "x"}} {
		if v.IsZero() {
			t.Fatalf("%#v.IsZero() = true, want false", v)
		}
	}
}

// TestMustParseVersionPanics verifies MustParseVersion panics on invalid input.
func TestMustParseVersionPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustParseVersion(\"nope\") did not panic")
		}
	}()
	MustParseVersion("nope")
}

// TestVersionUnmarshalJSON verifies the string, legacy object and null JSON forms.
func TestVersionUnmarshalJSON(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    Version
		wantErr bool
	}{
		{name: "string", input: `"1.2.3-beta"`, want: Version{Major: 1, Minor: 2, Patch: 3, Prerelease: "beta"}},
		{name: "short string", input: `"1.2"`, want: Version{Major: 1, Minor: 2}},
		{name: "null", input: `null`},
		{name: "empty string", input: `""`},
		{name: "blank string", input: `"   "`},
		{name: "non-standard string rejected", input: `"1.6.15.24356"`, wantErr: true},
		{name: "invalid string", input: `"abc"`, wantErr: true},
		{name: "legacy object", input: `{"MajorVersion":1,"MinorVersion":2,"PatchVersion":3,"PrereleaseTag":"beta"}`, want: Version{Major: 1, Minor: 2, Patch: 3, Prerelease: "beta"}},
		{name: "legacy object case-insensitive keys", input: `{"majorversion":1,"MINORVERSION":2}`, want: Version{Major: 1, Minor: 2}},
		{name: "legacy object ignores Build", input: `{"MajorVersion":1,"MinorVersion":2,"PatchVersion":3,"Build":"beta"}`, want: Version{Major: 1, Minor: 2, Patch: 3}},
		{name: "legacy object numeric strings", input: `{"MajorVersion":"2","MinorVersion":"1"}`, want: Version{Major: 2, Minor: 1}},
		{name: "legacy object null tag", input: `{"MajorVersion":3,"PrereleaseTag":null}`, want: Version{Major: 3}},
		{name: "legacy object absent fields default to zero", input: `{"PatchVersion":4}`, want: Version{Patch: 4}},
		{name: "legacy object null major", input: `{"MajorVersion":null,"MinorVersion":2}`, wantErr: true},
		{name: "legacy object null minor", input: `{"MajorVersion":3,"MinorVersion":null}`, wantErr: true},
		{name: "legacy object null patch", input: `{"MajorVersion":3,"PatchVersion":null}`, wantErr: true},
		{name: "legacy object fraction rounds half to even", input: `{"MajorVersion":1.5,"MinorVersion":2.5,"PatchVersion":0.4}`, want: Version{Major: 2, Minor: 2}},
		{name: "legacy object fraction rounds up", input: `{"MajorVersion":1.6}`, want: Version{Major: 2}},
		{name: "legacy object exponent", input: `{"MajorVersion":1e1}`, want: Version{Major: 10}},
		{name: "legacy object booleans", input: `{"MajorVersion":true,"MinorVersion":false}`, want: Version{Major: 1}},
		{name: "legacy object signed numeric string", input: `{"MajorVersion":" +2 "}`, want: Version{Major: 2}},
		{name: "legacy object fractional string", input: `{"MajorVersion":"1.5"}`, wantErr: true},
		{name: "legacy object empty string", input: `{"MajorVersion":""}`, wantErr: true},
		{name: "legacy object integer overflow", input: `{"MajorVersion":2147483648}`, wantErr: true},
		{name: "legacy object fraction overflow", input: `{"MajorVersion":2147483647.5}`, wantErr: true},
		{name: "legacy object largest fraction", input: `{"MajorVersion":2147483647.4}`, want: Version{Major: 2147483647}},
		{name: "legacy object array field", input: `{"MajorVersion":[1]}`, wantErr: true},
		{name: "legacy object object field", input: `{"MajorVersion":{"value":1}}`, wantErr: true},
		{name: "legacy object blank tag", input: `{"MajorVersion":1,"PrereleaseTag":"  "}`, want: Version{Major: 1}},
		{name: "legacy object invalid tag", input: `{"MajorVersion":1,"PrereleaseTag":"be ta"}`, wantErr: true},
		{name: "legacy object all zero", input: `{"MajorVersion":0}`, wantErr: true},
		{name: "legacy object negative", input: `{"MajorVersion":-1,"MinorVersion":2}`, wantErr: true},
		{name: "legacy object non-integer", input: `{"MajorVersion":"one"}`, wantErr: true},
		{name: "number", input: `5`, wantErr: true},
		{name: "array", input: `[1,2]`, wantErr: true},
		{name: "boolean", input: `true`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Version{Major: 9, Prerelease: "stale"}
			err := got.UnmarshalJSON([]byte(tc.input))
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidVersion) {
					t.Fatalf("UnmarshalJSON(%s) error = %v, want ErrInvalidVersion", tc.input, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("UnmarshalJSON(%s): %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("UnmarshalJSON(%s) = %#v, want %#v", tc.input, got, tc.want)
			}
		})
	}
}

// TestVersionUnmarshalJSONField verifies Version decodes as a struct field through encoding/json.
func TestVersionUnmarshalJSONField(t *testing.T) {
	var doc struct {
		Current Version
		Missing Version
		Nothing *Version
	}
	if err := json.Unmarshal([]byte(`{"current":"4.5.2","Nothing":null}`), &doc); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if doc.Current != (Version{Major: 4, Minor: 5, Patch: 2}) || !doc.Missing.IsZero() || doc.Nothing != nil {
		t.Fatalf("decoded = %#v", doc)
	}
}

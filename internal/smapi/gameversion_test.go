package smapi

import (
	"errors"
	"testing"
)

// TestGameVersionFromDeps verifies the game build is read from targets or libraries in a deps.json document.
func TestGameVersionFromDeps(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		pkg     string
		want    Version
		wantErr bool
	}{
		{
			name:  "target entry",
			input: `{"targets":{".NETCoreApp,Version=v6.0":{"Stardew Valley/1.6.15.24356":{}}}}`,
			pkg:   "Stardew Valley",
			want:  Version{Major: 1, Minor: 6, Patch: 15, Platform: 24356},
		},
		{
			name: "realistic document",
			input: "\xef\xbb\xbf" + `{
  "runtimeTarget": {"name": ".NETCoreApp,Version=v6.0", "signature": ""},
  "compilationOptions": {},
  "targets": {
    ".NETCoreApp,Version=v6.0": {
      "MonoGame.Framework/3.8.0": {"runtime": {"MonoGame.Framework.dll": {}}},
      "Stardew Valley/1.6.14.24317": {"dependencies": {"MonoGame.Framework": "3.8.0"}},
      "StardewValley.GameData/1.6.14": {}
    }
  },
  "libraries": {
    "Stardew Valley/1.6.14.24317": {"type": "project", "serviceable": false, "sha512": ""}
  }
}`,
			pkg:  "Stardew Valley",
			want: Version{Major: 1, Minor: 6, Patch: 14, Platform: 24317},
		},
		{
			name:  "libraries only",
			input: `{"libraries":{"Other/1.0.0":{},"Stardew Valley/1.5.6":{}}}`,
			pkg:   "Stardew Valley",
			want:  Version{Major: 1, Minor: 5, Patch: 6},
		},
		{
			name:  "package name case-insensitive",
			input: `{"targets":{"t":{"stardew valley/1.6.8":{}}}}`,
			pkg:   "Stardew Valley",
			want:  Version{Major: 1, Minor: 6, Patch: 8},
		},
		{
			name:  "prefix of another package ignored",
			input: `{"targets":{"t":{"Stardew Valley.GameData/9.9.9":{},"Stardew Valley/1.6.9":{}}}}`,
			pkg:   "Stardew Valley",
			want:  Version{Major: 1, Minor: 6, Patch: 9},
		},
		{
			name:  "non-object target skipped",
			input: `{"targets":{"a":"oops","b":{"Stardew Valley/1.6.10":{}}}}`,
			pkg:   "Stardew Valley",
			want:  Version{Major: 1, Minor: 6, Patch: 10},
		},
		{name: "package missing", input: `{"targets":{"t":{"Other/1.0.0":{}}},"libraries":{}}`, pkg: "Stardew Valley", wantErr: true},
		{name: "invalid version", input: `{"targets":{"t":{"Stardew Valley/abc":{}}}}`, pkg: "Stardew Valley", wantErr: true},
		{name: "empty document", input: `{}`, pkg: "Stardew Valley", wantErr: true},
		{name: "invalid JSON", input: `{"targets":`, pkg: "Stardew Valley", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := GameVersionFromDeps([]byte(tc.input), tc.pkg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("GameVersionFromDeps = %v, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("GameVersionFromDeps: %v", err)
			}
			if got != tc.want {
				t.Fatalf("GameVersionFromDeps = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestGameVersionFromDepsMissingIsInvalidVersion verifies a missing package is reported as ErrInvalidVersion.
func TestGameVersionFromDepsMissingIsInvalidVersion(t *testing.T) {
	_, err := GameVersionFromDeps([]byte(`{"targets":{}}`), "Stardew Valley")
	if !errors.Is(err, ErrInvalidVersion) {
		t.Fatalf("GameVersionFromDeps error = %v, want ErrInvalidVersion", err)
	}
}

package fsutil

import (
	"path/filepath"
	"testing"
)

// TestSafeJoin verifies safe relative paths are joined to their root.
func TestSafeJoin(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name     string
		relative string
		allowDot bool
		want     string
		wantErr  string
	}{
		{name: "plain relative path", relative: "a/b", want: filepath.Join(root, "a", "b")},
		{name: "empty", relative: "", wantErr: "path must be non-empty and relative"},
		{name: "absolute", relative: filepath.Join(string(filepath.Separator), "absolute"), wantErr: "path must be non-empty and relative"},
		{name: "parent", relative: "..", wantErr: "path escapes its root"},
		{name: "parent escape", relative: "../escape", wantErr: "path escapes its root"},
		{name: "nested parent escape", relative: "a/../../escape", wantErr: "path escapes its root"},
		{name: "backslash normalized", relative: `a\b`, want: filepath.Join(root, "a", "b")},
		{name: "dot rejected", relative: ".", wantErr: "path must name an extracted entry"},
		{name: "dot accepted", relative: ".", allowDot: true, want: root},
		{name: "surrounding whitespace", relative: "  a/b  ", want: filepath.Join(root, "a", "b")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SafeJoin(root, tc.relative, tc.allowDot)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("SafeJoin(%q) error = %v, want %q", tc.relative, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SafeJoin(%q): %v", tc.relative, err)
			}
			if got != tc.want {
				t.Fatalf("SafeJoin(%q) = %q, want %q", tc.relative, got, tc.want)
			}
		})
	}
}

// TestContainedBy verifies containment for roots, children, and siblings.
func TestContainedBy(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name      string
		candidate string
		want      bool
	}{
		{name: "child", candidate: filepath.Join(root, "child"), want: true},
		{name: "sibling", candidate: filepath.Join(filepath.Dir(root), "sibling"), want: false},
		{name: "root", candidate: root, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ContainedBy(root, tc.candidate); got != tc.want {
				t.Fatalf("ContainedBy(%q, %q) = %t, want %t", root, tc.candidate, got, tc.want)
			}
		})
	}
}

// TestValidateName verifies single path segment validation.
func TestValidateName(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr string
	}{
		{name: "valid", value: "My Mod.7z"},
		{name: "empty", value: "   ", wantErr: "name must not be empty"},
		{name: "relative element", value: ".", wantErr: "name must not be a relative path element"},
		{name: "parent element", value: "..", wantErr: "name must not be a relative path element"},
		{name: "separator", value: "mods/file", wantErr: "name must not contain a path separator"},
		{name: "backslash separator", value: `mods\file`, wantErr: "name must not contain a path separator"},
		{name: "nul", value: "mod\x00name", wantErr: "name must not contain control characters"},
		{name: "newline", value: "mod\nname", wantErr: "name must not contain control characters"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateName(tc.value)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateName(%q): %v", tc.value, err)
				}
				return
			}
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("ValidateName(%q) error = %v, want %q", tc.value, err, tc.wantErr)
			}
		})
	}
}

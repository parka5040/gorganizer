package config

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestRuntimeDirRules checks absolute XDG selection and the absolute temporary fallback.
func TestRuntimeDirRules(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := t.TempDir()
	fallback := filepath.Join(base, "gorganizer-"+strconv.Itoa(os.Getuid()))
	cases := []struct {
		name string
		xdg  string
		tmp  string
		want string
	}{
		{name: "absolute XDG", xdg: base, tmp: "relative", want: filepath.Join(base, "gorganizer")},
		{name: "empty XDG", tmp: base, want: fallback},
		{name: "relative XDG", xdg: "relative", tmp: base, want: fallback},
		{name: "relative TMPDIR", xdg: "relative", tmp: "relative", want: filepath.Join("/tmp", "gorganizer-"+strconv.Itoa(os.Getuid()))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_RUNTIME_DIR", tc.xdg)
			t.Setenv("TMPDIR", tc.tmp)
			if got := RuntimeDir(); got != tc.want {
				t.Errorf("RuntimeDir() = %q, want %q", got, tc.want)
			}
		})
	}
}

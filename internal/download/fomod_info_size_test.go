package download

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLegacyInfoXMLSizeCap verifies oversized info.xml is ignored without failing the install.
func TestLegacyInfoXMLSizeCap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct {
		name string
		data string
		want string
	}{
		{name: "within cap", data: "<fomod><Name>Mod title</Name></fomod>", want: "Mod title"},
		{name: "above cap", data: "<fomod><Name>" + strings.Repeat("x", (1<<20)+1) + "</Name></fomod>", want: "Sample"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "Sample")
			if err := os.MkdirAll(filepath.Join(root, "fomod"), 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "fomod", "info.xml")
			if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			if got := ParseLegacyFomodInfo(root); got.Name != tc.want || got.Description != "" {
				t.Fatalf("ParseLegacyFomodInfo() = %+v, want name %q", got, tc.want)
			}
		})
	}
}

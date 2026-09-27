package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestCheckExistingPath refuses linked components and non-directory ancestors beneath a trusted root.
func TestCheckExistingPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		rel  string
		make func(t *testing.T, root string)
		want error
	}{
		{"missing child", "missing/child", nil, nil},
		{"linked root", "child", func(t *testing.T, root string) {
			if err := os.Remove(root); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), root); err != nil {
				t.Fatal(err)
			}
		}, ErrExistingLink},
		{"linked ancestor", "link/child", func(t *testing.T, root string) {
			if err := os.Symlink(t.TempDir(), filepath.Join(root, "link")); err != nil {
				t.Fatal(err)
			}
		}, ErrExistingLink},
		{"linked final", "link", func(t *testing.T, root string) {
			if err := os.Symlink(t.TempDir(), filepath.Join(root, "link")); err != nil {
				t.Fatal(err)
			}
		}, ErrExistingLink},
		{"file ancestor", "file/child", func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "file"), []byte("original"), 0644); err != nil {
				t.Fatal(err)
			}
		}, ErrExistingNonDirectory},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "root")
			if err := os.Mkdir(root, 0755); err != nil {
				t.Fatal(err)
			}
			if tc.make != nil {
				tc.make(t, root)
			}
			if err := CheckExistingPath(root, tc.rel); (tc.want == nil && err != nil) || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Errorf("CheckExistingPath(%q) = %v, want %v", tc.rel, err, tc.want)
			}
		})
	}
}

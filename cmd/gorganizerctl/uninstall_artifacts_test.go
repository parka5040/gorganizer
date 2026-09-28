package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/vfs"
)

// TestUninstallCheckDistinguishesRetainedBatchesFromUnfinishedTransitions checks both classes without removing user files.
func TestUninstallCheckDistinguishesRetainedBatchesFromUnfinishedTransitions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(*testing.T, *uninstallFixture) string
		wantOK  bool
	}{
		{
			name: "retained preserved batch",
			prepare: func(t *testing.T, f *uninstallFixture) string {
				path := filepath.Join(vfs.PreservedDir(f.data), "batch", "files", "saved.txt")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("keep this output"), 0600); err != nil {
					t.Fatal(err)
				}
				return path
			},
			wantOK: true,
		},
		{
			name: "interrupted restore record",
			prepare: func(t *testing.T, f *uninstallFixture) string {
				path := f.data + ".gorganizer-restoring"
				if err := os.WriteFile(path, []byte("unfinished"), 0600); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUninstallFixture(t)
			path := tc.prepare(t, f)
			code := runUninstallWith([]string{"--check"}, f.deps)
			if (code == 0) != tc.wantOK || len(f.removed) != 0 {
				t.Fatalf("check exit = %d, deletions = %v, error = %q", code, f.removed, f.errOut.String())
			}
			if !tc.wantOK && !strings.Contains(f.errOut.String(), ".gorganizer-restoring") {
				t.Errorf("check did not explain unfinished restore: %q", f.errOut.String())
			}
			body, err := os.ReadFile(path)
			if err != nil || len(body) == 0 {
				t.Fatalf("retained artifact changed: %q, %v", body, err)
			}
			if _, err := os.Lstat(f.data + ".orig"); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("check created a backup: %v", err)
			}
		})
	}
}

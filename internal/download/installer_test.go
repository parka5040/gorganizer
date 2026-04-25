package download

import (
	"os"
	"path/filepath"
	"testing"
)

// findContentRoot must NOT strip a single top-level dir that is a known
// Bethesda Data/ subdirectory. Stripping `nvse/` was the bug that put
// xNVSE plugin DLLs at Data/plugins/foo.dll instead of Data/nvse/plugins/foo.dll.
func TestFindContentRoot_PreservesKnownDataSubdir(t *testing.T) {
	cases := []struct {
		name      string
		layout    []string
		wantInner string // path component the returned root should END with
	}{
		{
			name:      "lowercase nvse wrapper preserved",
			layout:    []string{"nvse/plugins/foo.dll"},
			wantInner: "extract", // root should equal the extract dir
		},
		{
			name:      "uppercase NVSE wrapper preserved",
			layout:    []string{"NVSE/Plugins/foo.dll"},
			wantInner: "extract",
		},
		{
			name:      "skse wrapper preserved",
			layout:    []string{"skse/plugins/bar.dll"},
			wantInner: "extract",
		},
		{
			name:      "edit scripts wrapper preserved",
			layout:    []string{"Edit Scripts/foo.pas"},
			wantInner: "extract",
		},
		{
			name:      "ModName + Data dives into Data",
			layout:    []string{"MyMod/Data/foo.esp", "MyMod/Data/meshes/x.nif"},
			wantInner: "Data",
		},
		{
			name:      "ModName wrapper without Data still stripped",
			layout:    []string{"MyMod/foo.esp", "MyMod/meshes/x.nif"},
			wantInner: "MyMod",
		},
		{
			name:      "Data folder still stripped",
			layout:    []string{"Data/foo.esp"},
			wantInner: "Data",
		},
		{
			name:      "multiple top-level dirs, no strip",
			layout:    []string{"nvse/plugins/foo.dll", "textures/x.dds"},
			wantInner: "extract",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			extract := filepath.Join(tmp, "extract")
			if err := os.MkdirAll(extract, 0755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			for _, p := range tc.layout {
				full := filepath.Join(extract, p)
				if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
					t.Fatalf("mkdir parent: %v", err)
				}
				if err := os.WriteFile(full, []byte("x"), 0644); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
			got := findContentRoot(extract)
			if filepath.Base(got) != tc.wantInner {
				t.Errorf("findContentRoot(%v): got %s, want basename %s",
					tc.layout, got, tc.wantInner)
			}
		})
	}
}

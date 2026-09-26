package download

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestInstallMergeAlignsPlannedFolderCase verifies a planned folder differing only by case merges into the existing folder spelling.
func TestInstallMergeAlignsPlannedFolderCase(t *testing.T) {
	cases := []struct {
		name         string
		existing     func(t *testing.T, modDir string)
		destName     string
		wantFiles    []string
		wantManifest string
		wantErr      bool
	}{
		{
			name:         "case variant merges into existing folder",
			existing:     func(t *testing.T, modDir string) { writeTree(t, modDir, "SampleMod/old.txt") },
			destName:     "samplemod",
			wantFiles:    []string{"SampleMod/manifest.json", "SampleMod/old.txt"},
			wantManifest: "SampleMod/manifest.json",
		},
		{
			name:         "exact spelling is kept",
			existing:     func(t *testing.T, modDir string) { writeTree(t, modDir, "samplemod/old.txt") },
			destName:     "samplemod",
			wantFiles:    []string{"samplemod/manifest.json", "samplemod/old.txt"},
			wantManifest: "samplemod/manifest.json",
		},
		{
			name:         "exact spelling wins over an existing case-folded twin",
			existing:     func(t *testing.T, modDir string) { writeTree(t, modDir, "samplemod/old.txt", "SampleMod/other.txt") },
			destName:     "samplemod",
			wantFiles:    []string{"SampleMod/other.txt", "samplemod/manifest.json", "samplemod/old.txt"},
			wantManifest: "samplemod/manifest.json",
		},
		{
			name:         "no existing folder keeps the planned name",
			existing:     func(t *testing.T, modDir string) { writeTree(t, modDir, "Other/old.txt") },
			destName:     "SampleMod",
			wantFiles:    []string{"Other/old.txt", "SampleMod/manifest.json"},
			wantManifest: "SampleMod/manifest.json",
		},
		{
			name:     "ambiguous case-folded twins are refused",
			existing: func(t *testing.T, modDir string) { writeTree(t, modDir, "samplemod/a.txt", "SAMPLEMOD/b.txt") },
			destName: "SampleMod",
			wantErr:  true,
		},
		{
			name:     "existing file with the folded name is refused",
			existing: func(t *testing.T, modDir string) { writeTree(t, modDir, "SAMPLEMOD") },
			destName: "SampleMod",
			wantErr:  true,
		},
		{
			name: "existing symlink with the folded name is refused",
			existing: func(t *testing.T, modDir string) {
				if err := os.MkdirAll(modDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), filepath.Join(modDir, "SAMPLEMOD")); err != nil {
					t.Fatal(err)
				}
			},
			destName: "SampleMod",
			wantErr:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			modsDir := useModsDir(t)
			modDir := filepath.Join(modsDir, "Target")
			tc.existing(t, modDir)
			before := listFiles(t, modDir)
			extract := t.TempDir()
			writeTree(t, extract, "Pack/manifest.json")

			_, err := Install(InstallRequest{
				GameID: "stardewvalley", ExtractedRoot: extract, Mode: ModeMergeIntoMod, TargetMod: "Target",
				Layout: &fakePlanner{copies: []PlannedCopy{{SourceRel: "Pack", DestName: tc.destName}}},
			})
			if tc.wantErr {
				if !errors.Is(err, ErrUnsafeArchive) {
					t.Fatalf("Install error = %v, want ErrUnsafeArchive", err)
				}
				if after := listFiles(t, modDir); !reflect.DeepEqual(after, before) {
					t.Errorf("refused merge changed the mod: %v, want %v", after, before)
				}
				return
			}
			if err != nil {
				t.Fatalf("Install: %v", err)
			}
			got := listFiles(t, modDir)
			want := append(append([]string{}, tc.wantFiles...), "metadata.yaml")
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("merged files = %v, want %v", got, want)
			}
			meta, err := LoadModMetadata(modDir)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(meta.Files, []string{tc.wantManifest}) {
				t.Errorf("metadata files = %v, want [%s]", meta.Files, tc.wantManifest)
			}
		})
	}
}

// TestInstallNewModDoesNotAlignPlannedFolders verifies a new-mod install keeps planned names untouched.
func TestInstallNewModDoesNotAlignPlannedFolders(t *testing.T) {
	modsDir := useModsDir(t)
	extract := t.TempDir()
	writeTree(t, extract, "Pack/manifest.json")
	if _, err := Install(InstallRequest{
		GameID: "stardewvalley", ExtractedRoot: extract, Mode: ModeNewMod, TargetMod: "Fresh",
		Layout: &fakePlanner{copies: []PlannedCopy{{SourceRel: "Pack", DestName: "samplemod"}}},
	}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	want := []string{"metadata.yaml", "samplemod/manifest.json"}
	if got := listFiles(t, filepath.Join(modsDir, "Fresh")); !reflect.DeepEqual(got, want) {
		t.Errorf("installed files = %v, want %v", got, want)
	}
}

// TestNormalizeDerivedModName verifies derived folder names become valid, non-reserved mod names.
func TestNormalizeDerivedModName(t *testing.T) {
	cases := map[string]string{
		".NET Script Framework": "NET Script Framework",
		"  ..Hidden Mod  ":      "Hidden Mod",
		". .NET":                "NET",
		"Overwrite":             "Overwrite_",
		"downloads":             "downloads_",
		".Overwrite":            "Overwrite_",
		"...":                   "Mod",
		"":                      "Mod",
		"   ":                   "Mod",
		"SkyUI 5.2":             "SkyUI 5.2",
		"Mod.":                  "Mod.",
	}
	for in, want := range cases {
		got := NormalizeDerivedModName(in)
		if got != want {
			t.Errorf("NormalizeDerivedModName(%q) = %q, want %q", in, got, want)
		}
		if err := ValidateTargetModName(got); err != nil {
			t.Errorf("NormalizeDerivedModName(%q) = %q fails validation: %v", in, got, err)
		}
	}
}

// TestPatchModMetadataFieldIsSurgical verifies only the named top-level key changes and a missing file is never created.
func TestPatchModMetadataFieldIsSurgical(t *testing.T) {
	modDir := t.TempDir()
	original := "# header\nname: \"Mod\"\nfolder: \"Mod\"\ntrue_index: \"0x10\"\nextra_key: \"kept\"\nsource_archives:\n  - path: \"Downloads/a.zip\"\n    true_index: \"nested\"\nfiles:\n  - a.esp\n"
	if err := os.WriteFile(filepath.Join(modDir, "metadata.yaml"), []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if ok, err := PatchModMetadataField(modDir, "true_index", "0x20"); !ok || err != nil {
		t.Fatalf("PatchModMetadataField = %v, %v", ok, err)
	}
	got, err := os.ReadFile(filepath.Join(modDir, "metadata.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(original, "true_index: \"0x10\"", "true_index: \"0x20\"", 1)
	if string(got) != want {
		t.Errorf("patched file =\n%s\nwant\n%s", got, want)
	}

	if ok, err := PatchModMetadataField(modDir, "visual_index", "0x30"); !ok || err != nil {
		t.Fatalf("PatchModMetadataField(insert) = %v, %v", ok, err)
	}
	got, _ = os.ReadFile(filepath.Join(modDir, "metadata.yaml"))
	want = strings.Replace(want, "source_archives:\n", "visual_index: \"0x30\"\nsource_archives:\n", 1)
	if string(got) != want {
		t.Errorf("inserted file =\n%s\nwant\n%s", got, want)
	}

	missing := t.TempDir()
	if ok, err := PatchModMetadataField(missing, "true_index", "0x10"); ok || err != nil {
		t.Fatalf("PatchModMetadataField(missing) = %v, %v, want false, nil", ok, err)
	}
	if _, err := os.Stat(filepath.Join(missing, "metadata.yaml")); !os.IsNotExist(err) {
		t.Errorf("missing metadata.yaml was created: %v", err)
	}
}

package daemon

import (
	"archive/zip"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/gamedef"
	"github.com/parka/gorganizer/internal/mod"
	"github.com/parka/gorganizer/internal/smapi"
)

const sampleManifest = `{"Name":"Sample","UniqueID":"Example.Sample","EntryDll":"Sample.dll"}`

// writeZipFiles writes a zip archive at path holding the given slash-separated files.
func writeZipFiles(t *testing.T, path string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// writeManifestArchive writes a zip holding one SMAPI-style manifest folder to path.
func writeManifestArchive(t *testing.T, path string) {
	t.Helper()
	writeZipFiles(t, path, map[string]string{"SampleMod/manifest.json": sampleManifest})
}

// modFiles returns the sorted slash-separated regular files under dir.
func modFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// newStardewDaemon builds an isolated daemon with Stardew Valley and Skyrim SE configured.
func newStardewDaemon(t *testing.T) *Daemon {
	t.Helper()
	return newIsolatedDaemon(t, map[string]config.GameConfig{
		"stardewvalley": {InstallPath: t.TempDir(), DataSubpath: "Mods"},
		"skyrimse":      {InstallPath: t.TempDir(), DataSubpath: "Data"},
	})
}

func TestLayoutPlannerRegistry(t *testing.T) {
	for _, def := range gamedef.All {
		if err := checkInstallLayout(def.ID); err != nil {
			t.Errorf("checkInstallLayout(%q) = %v, want nil", def.ID, err)
		}
		planner := layoutPlannerFor(def.ID)
		switch def.Layout {
		case gamedef.LayoutSMAPIManifest:
			if _, ok := planner.(smapiLayout); !ok {
				t.Errorf("layoutPlannerFor(%q) = %T, want smapiLayout", def.ID, planner)
			}
		default:
			if planner != nil {
				t.Errorf("layoutPlannerFor(%q) = %T, want nil", def.ID, planner)
			}
		}
	}
	if err := checkInstallLayout("unknowngame"); err != nil || layoutPlannerFor("unknowngame") != nil {
		t.Errorf("unknown game: check = %v planner = %v, want nil and nil", err, layoutPlannerFor("unknowngame"))
	}

	empty := map[gamedef.InstallLayout]func() download.LayoutPlanner{}
	for _, def := range gamedef.All {
		planner, err := resolveLayoutPlanner(empty, def.ID)
		if def.Layout == gamedef.LayoutDataRoot {
			if err != nil || planner != nil {
				t.Errorf("resolveLayoutPlanner(empty, %q) = %v, %v, want nil, nil", def.ID, planner, err)
			}
			continue
		}
		var layoutErr *download.LayoutUnsupportedError
		if !errors.As(err, &layoutErr) || layoutErr.GameID != def.ID || layoutErr.Layout != def.Layout.String() {
			t.Errorf("resolveLayoutPlanner(empty, %q) error = %v, want LayoutUnsupportedError", def.ID, err)
		}
	}
}

func TestInstallEntryPointsRefuseLayoutsWithoutPlanner(t *testing.T) {
	d := newStardewDaemon(t)
	previous := layoutPlanners
	layoutPlanners = map[gamedef.InstallLayout]func() download.LayoutPlanner{
		gamedef.LayoutDataRoot: func() download.LayoutPlanner { return nil },
	}
	t.Cleanup(func() { layoutPlanners = previous })

	writeManifestArchive(t, filepath.Join(config.DownloadsDir("stardewvalley"), "SampleMod.zip"))
	existing := filepath.Join(config.ModsDir("stardewvalley"), "Existing", "Inner", "manifest.json")
	writeFixture(t, existing)

	var layoutErr *download.LayoutUnsupportedError
	if _, _, err := d.StartInstall(dto.StartInstallRequest{
		GameID: "stardewvalley", ArchiveRelPath: "SampleMod.zip", Mode: dto.InstallAsNewMod,
	}); !errors.As(err, &layoutErr) {
		t.Errorf("StartInstall error = %v, want LayoutUnsupportedError", err)
	}
	if _, err := d.PreviewInstall(dto.PreviewInstallRequest{GameID: "stardewvalley", ArchiveRelPath: "SampleMod.zip"}); !errors.As(err, &layoutErr) {
		t.Errorf("PreviewInstall error = %v, want LayoutUnsupportedError", err)
	}
	if _, _, _, err := d.ReinstallMod("stardewvalley", "Existing"); !errors.As(err, &layoutErr) {
		t.Errorf("ReinstallMod error = %v, want LayoutUnsupportedError", err)
	}
	if layoutErr == nil || layoutErr.GameID != "stardewvalley" || layoutErr.Layout != "smapi_manifest" {
		t.Errorf("LayoutUnsupportedError = %+v", layoutErr)
	}
	if _, err := os.Lstat(filepath.Join(config.ModsDir("stardewvalley"), "SampleMod")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("mod folder after refused install: %v, want not exist", err)
	}
	if _, err := os.Stat(existing); err != nil {
		t.Errorf("existing mod file touched by refused reinstall: %v", err)
	}
}

func TestStartInstallAppliesGameLayout(t *testing.T) {
	cases := []struct {
		name      string
		gameID    string
		archive   map[string]string
		wantFiles []string
	}{
		{
			name:      "stardew keeps the manifest folder",
			gameID:    "stardewvalley",
			archive:   map[string]string{"SampleMod/manifest.json": sampleManifest, "SampleMod/Sample.dll": "dll"},
			wantFiles: []string{"SampleMod/Sample.dll", "SampleMod/manifest.json", "metadata.yaml"},
		},
		{
			name:      "stardew wraps a root manifest",
			gameID:    "stardewvalley",
			archive:   map[string]string{"manifest.json": sampleManifest, "Sample.dll": "dll"},
			wantFiles: []string{"Sample/Sample.dll", "Sample/manifest.json", "metadata.yaml"},
		},
		{
			name:    "stardew ignores a fomod installer",
			gameID:  "stardewvalley",
			archive: map[string]string{"fomod/ModuleConfig.xml": "<config/>", "SampleMod/manifest.json": sampleManifest},
			wantFiles: []string{
				"SampleMod/manifest.json", "metadata.yaml",
			},
		},
		{
			name:      "skyrim flattens the wrapper",
			gameID:    "skyrimse",
			archive:   map[string]string{"SampleMod/manifest.json": sampleManifest},
			wantFiles: []string{"manifest.json", "metadata.yaml"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newStardewDaemon(t)
			writeZipFiles(t, filepath.Join(config.DownloadsDir(tc.gameID), "SampleMod.zip"), tc.archive)
			folder, count, err := d.StartInstall(dto.StartInstallRequest{
				GameID: tc.gameID, ArchiveRelPath: "SampleMod.zip", Mode: dto.InstallAsNewMod,
			})
			if err != nil {
				t.Fatalf("StartInstall: %v", err)
			}
			if folder != "SampleMod" || count != len(tc.wantFiles)-1 {
				t.Errorf("StartInstall = %q, %d", folder, count)
			}
			if got := modFiles(t, filepath.Join(config.ModsDir(tc.gameID), folder)); !reflect.DeepEqual(got, tc.wantFiles) {
				t.Errorf("installed files = %v, want %v", got, tc.wantFiles)
			}
			entries, err := d.GetModList(tc.gameID, "Default")
			if err != nil || len(entries) != 1 || entries[0].ModName != folder || entries[0].Enabled {
				t.Errorf("modlist after install = %+v, %v", entries, err)
			}
		})
	}
}

func TestStartInstallRefusesInvalidSMAPIArchives(t *testing.T) {
	cases := []struct {
		name       string
		archive    map[string]string
		fomod      []dto.FomodFileResult
		wantReason string
		wantFomod  bool
	}{
		{name: "no manifest", archive: map[string]string{"readme.txt": "hello"}, wantReason: smapi.ReasonNoManifest},
		{name: "loader installer", archive: map[string]string{"SMAPI 4.0/internal/linux/install.dat": "x"}, wantReason: smapi.ReasonLoaderInstaller},
		{
			name:      "fomod selections",
			archive:   map[string]string{"SampleMod/manifest.json": sampleManifest},
			fomod:     []dto.FomodFileResult{{Source: "SampleMod", Destination: "SampleMod", IsFolder: true}},
			wantFomod: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newStardewDaemon(t)
			writeZipFiles(t, filepath.Join(config.DownloadsDir("stardewvalley"), "Bad.zip"), tc.archive)
			_, _, err := d.StartInstall(dto.StartInstallRequest{
				GameID: "stardewvalley", ArchiveRelPath: "Bad.zip", Mode: dto.InstallAsNewMod,
				FomodSelectedFiles: tc.fomod,
			})
			if tc.wantFomod {
				if !errors.Is(err, download.ErrFomodNotSupportedForLayout) {
					t.Fatalf("StartInstall error = %v, want ErrFomodNotSupportedForLayout", err)
				}
			} else {
				var notAMod smapi.NotAModError
				if !errors.Is(err, smapi.ErrNotAMod) || !errors.As(err, &notAMod) || notAMod.Reason != tc.wantReason {
					t.Fatalf("StartInstall error = %v, want not_a_mod reason %s", err, tc.wantReason)
				}
			}
			if _, err := os.Lstat(filepath.Join(config.ModsDir("stardewvalley"), "Bad")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("mod folder after refused install: %v, want not exist", err)
			}
		})
	}
}

// nestedFomodBytes returns a real zip archive that ExpandNestedFomods can extract.
func nestedFomodBytes(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nested.fomod")
	writeZipFiles(t, path, map[string]string{"inner.txt": "inner"})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPreviewInstallListsPlannedFiles(t *testing.T) {
	d := newStardewDaemon(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	nested := nestedFomodBytes(t)
	control := t.TempDir()
	writeZipFiles(t, filepath.Join(control, "Core", "nested.fomod"), map[string]string{"inner.txt": "inner"})
	download.ExpandNestedFomods(control)
	if _, err := os.Stat(filepath.Join(control, "Core", "nested", "inner.txt")); err != nil {
		t.Fatalf("fixture is not a nested FOMOD that ExpandNestedFomods expands: %v", err)
	}
	writeZipFiles(t, filepath.Join(config.DownloadsDir("stardewvalley"), "Pack.zip"), map[string]string{
		"fomod/ModuleConfig.xml":       "<config/>",
		"Pack/[CP] Pack/manifest.json": `{"Name":"CP Pack","UniqueID":"Example.CP","ContentPackFor":{"UniqueID":"Pathoschild.ContentPatcher"}}`,
		"Pack/[CP] Pack/content.json":  "{}",
		"Core/manifest.json":           sampleManifest,
		"Core/assets/sprite.png":       "png",
		"Core/nested.fomod":            nested,
		"Core/i18n/default.json":       "{}",
		"Core/i18n/de.json":            "{}",
		"Core/Sample.dll":              "dll",
		"Core/Sample.pdb":              "pdb",
		"Core/config.json":             "{}",
		"Core/assets/nested/x.png":     "png",
	})

	res, err := d.PreviewInstall(dto.PreviewInstallRequest{GameID: "stardewvalley", ArchiveRelPath: "Pack.zip"})
	if err != nil {
		t.Fatalf("PreviewInstall: %v", err)
	}
	want := []string{
		"Core/Sample.dll", "Core/Sample.pdb", "Core/assets/nested/x.png", "Core/assets/sprite.png",
		"Core/config.json", "Core/i18n/de.json", "Core/i18n/default.json", "Core/manifest.json", "Core/nested.fomod",
		"[CP] Pack/content.json", "[CP] Pack/manifest.json",
	}
	if res.HasFomod || res.Plan != nil {
		t.Errorf("preview HasFomod = %v plan = %+v, want no FOMOD", res.HasFomod, res.Plan)
	}
	if !reflect.DeepEqual(res.FlatFileList, want) {
		t.Errorf("FlatFileList = %v, want %v", res.FlatFileList, want)
	}

	folder, _, err := d.StartInstall(dto.StartInstallRequest{
		GameID: "stardewvalley", ArchiveRelPath: "Pack.zip", Mode: dto.InstallAsNewMod, PreviewID: res.PreviewID,
	})
	if err != nil {
		t.Fatalf("StartInstall from preview: %v", err)
	}
	got := modFiles(t, filepath.Join(config.ModsDir("stardewvalley"), folder))
	if !reflect.DeepEqual(got, append(append([]string{}, want...), "metadata.yaml")) {
		t.Errorf("installed files = %v", got)
	}
	installedNested, err := os.ReadFile(filepath.Join(config.ModsDir("stardewvalley"), folder, "Core", "nested.fomod"))
	if err != nil || string(installedNested) != nested {
		t.Errorf("nested FOMOD archive not copied verbatim: %v", err)
	}

	writeZipFiles(t, filepath.Join(config.DownloadsDir("stardewvalley"), "Readme.zip"), map[string]string{"readme.txt": "hi"})
	if _, err := d.PreviewInstall(dto.PreviewInstallRequest{GameID: "stardewvalley", ArchiveRelPath: "Readme.zip"}); !errors.Is(err, smapi.ErrNotAMod) {
		t.Fatalf("PreviewInstall(no manifest) error = %v, want ErrNotAMod", err)
	}
	root, err := extractionRoot()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(root) != tmp {
		t.Errorf("extraction root %s is outside the temporary directory %s", root, tmp)
	}
	if leftovers := installEntries(t, root); len(leftovers) != 0 {
		t.Errorf("preview temp dirs left behind: %v", leftovers)
	}
}

func TestRegisterManualInstallValidatesManifestLayout(t *testing.T) {
	cases := []struct {
		name       string
		gameID     string
		files      []string
		wantReason string
	}{
		{name: "manifest folder accepted", gameID: "stardewvalley", files: []string{"Local/Inner/manifest.json"}},
		{name: "root manifest refused", gameID: "stardewvalley", files: []string{"Local/manifest.json", "Local/Local.dll"}, wantReason: manifestReasonRootManifest},
		{name: "root manifest alone refused", gameID: "stardewvalley", files: []string{"Local/manifest.json"}, wantReason: manifestReasonRootManifest},
		{name: "root manifest above a nested manifest refused", gameID: "stardewvalley", files: []string{"Local/manifest.json", "Local/Inner/manifest.json"}, wantReason: manifestReasonRootManifest},
		{name: "case-variant root manifest refused", gameID: "stardewvalley", files: []string{"Local/Manifest.JSON", "Local/Inner/manifest.json"}, wantReason: manifestReasonRootManifest},
		{name: "no manifest refused", gameID: "stardewvalley", files: []string{"Local/Inner/readme.txt"}, wantReason: manifestReasonNoManifest},
		{name: "data-root game not validated", gameID: "skyrimse", files: []string{"Local/manifest.json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newStardewDaemon(t)
			modsDir := config.ModsDir(tc.gameID)
			for _, rel := range tc.files {
				writeFixture(t, filepath.Join(modsDir, filepath.FromSlash(rel)))
			}
			updated, err := d.RegisterManualInstall(tc.gameID, "Local", "")
			entries, listErr := d.GetModList(tc.gameID, "Default")
			if listErr != nil {
				t.Fatal(listErr)
			}
			if tc.wantReason == "" {
				if err != nil || updated != 1 || len(entries) != 1 || entries[0].ModName != "Local" {
					t.Fatalf("RegisterManualInstall = %d, %v; modlist %+v", updated, err, entries)
				}
				return
			}
			var layoutErr *download.ManifestLayoutError
			if !errors.As(err, &layoutErr) || layoutErr.Mod != "Local" || layoutErr.Reason != tc.wantReason {
				t.Fatalf("RegisterManualInstall error = %v, want ManifestLayoutError reason %s", err, tc.wantReason)
			}
			if len(entries) != 0 {
				t.Errorf("refused registration touched the modlist: %+v", entries)
			}
			var want []string
			for _, rel := range tc.files {
				want = append(want, strings.TrimPrefix(rel, "Local/"))
			}
			sort.Strings(want)
			if got := modFiles(t, filepath.Join(modsDir, "Local")); !reflect.DeepEqual(got, want) {
				t.Errorf("files after refused registration = %v, want %v", got, want)
			}
		})
	}
}

func TestBuildLayersSkipsManifestLayoutModsWithARootManifest(t *testing.T) {
	d := newStardewDaemon(t)
	status := d.WatchStatus()
	cases := []struct {
		gameID     string
		files      []string
		wantLayers []string
		wantInfo   []string
	}{
		{
			gameID:     "stardewvalley",
			files:      []string{"Good/Inner/manifest.json", "Bad/manifest.json", "Bad/Bad.dll", "Folded/MANIFEST.json", "Off/manifest.json"},
			wantLayers: []string{"__base__", "Good", "Overwrite"},
			wantInfo: []string{
				"[layout] skipped Bad: manifest.json at the mod root; reinstall it",
				"[layout] skipped Folded: manifest.json at the mod root; reinstall it",
			},
		},
		{
			gameID:     "skyrimse",
			files:      []string{"Good/Inner/manifest.json", "Bad/manifest.json", "Folded/MANIFEST.json", "Off/manifest.json"},
			wantLayers: []string{"__base__", "Good", "Bad", "Folded", "Overwrite"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.gameID, func(t *testing.T) {
			for _, rel := range tc.files {
				writeFixture(t, filepath.Join(config.ModsDir(tc.gameID), filepath.FromSlash(rel)))
			}
			entries := []mod.ModListEntry{
				{Name: "Good", Enabled: true}, {Name: "Bad", Enabled: true}, {Name: "Folded", Enabled: true}, {Name: "Off"},
			}
			layers := d.buildLayers(tc.gameID, d.config.Games[tc.gameID], entries)
			var names []string
			for _, layer := range layers {
				names = append(names, layer.Name)
			}
			if !reflect.DeepEqual(names, tc.wantLayers) {
				t.Errorf("layers = %v, want %v", names, tc.wantLayers)
			}
			var infos []string
			deadline := time.After(2 * time.Second)
			for len(infos) < len(tc.wantInfo) {
				select {
				case evt := <-status:
					if strings.HasPrefix(evt.Info, "[layout]") {
						infos = append(infos, evt.Info)
					}
				case <-deadline:
					t.Fatalf("layout infos = %v, want %v", infos, tc.wantInfo)
				}
			}
			if !reflect.DeepEqual(infos, tc.wantInfo) {
				t.Errorf("layout infos = %v, want %v", infos, tc.wantInfo)
			}
		})
	}
	select {
	case evt := <-status:
		if strings.HasPrefix(evt.Info, "[layout]") {
			t.Errorf("unexpected layout info for a Data-root game: %q", evt.Info)
		}
	case <-time.After(200 * time.Millisecond):
	}
}

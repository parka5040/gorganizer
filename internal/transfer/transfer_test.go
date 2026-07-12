package transfer

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
)

const testGame = "skyrimse"

// setRoot points GORGANIZER_ROOT and XDG_DATA_HOME at a scratch instance root.
func setRoot(t *testing.T, root string) {
	t.Helper()
	t.Setenv("GORGANIZER_ROOT", filepath.Join(root, "instance"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "xdg"))
}

// writeFileT writes a file, creating parents.
func writeFileT(t *testing.T, path string, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// buildInstance materializes 3 mods, a profile, Overwrite files, and game settings under the active env roots.
func buildInstance(t *testing.T) {
	t.Helper()
	modsDir := config.ModsDir(testGame)

	writeFileT(t, filepath.Join(modsDir, "Alpha Mod", "AlphaMod.esp"), "alpha-esp-payload")
	writeFileT(t, filepath.Join(modsDir, "Alpha Mod", "meshes", "deep", "alpha.nif"), "alpha-mesh-payload")
	writeFileT(t, filepath.Join(modsDir, "Alpha Mod", "metadata.yaml"),
		"# Gorganizer mod metadata — auto-generated\nname: \"Alpha Mod\"\nfolder: \"Alpha Mod\"\n")

	writeFileT(t, filepath.Join(modsDir, "Beta", "textures", "sub", "beta.dds"), "beta-texture-payload")
	writeFileT(t, filepath.Join(modsDir, "Beta", "Beta.esp"), "beta-esp-payload")

	writeFileT(t, filepath.Join(modsDir, "Gamma", "Gamma.esm"), "gamma-esm-payload")
	writeFileT(t, filepath.Join(modsDir, "Gamma", ".gorganizer-root", "SkyrimSELauncher.exe"), "root-layer-payload")
	if err := download.SaveModMetadata(filepath.Join(modsDir, "Gamma"), &download.ModMetadata{
		Name:   "Gamma Display Name",
		Folder: "Gamma",
		SourceArchives: []download.SourceArchiveRef{
			{Path: "Downloads/gamma.7z", ModID: 111, FileID: 222},
		},
	}); err != nil {
		t.Fatalf("SaveModMetadata: %v", err)
	}

	profDir := filepath.Join(config.ProfilesDir(testGame), "Default")
	writeFileT(t, filepath.Join(profDir, "profile.json"),
		"{\n  \"name\": \"Default\",\n  \"game_id\": \"skyrimse\",\n  \"created_at\": \"2026-01-02T03:04:05Z\"\n}")
	writeFileT(t, filepath.Join(profDir, "modlist.txt"),
		"# Gorganizer modlist — do not edit while daemon is running\n+Alpha Mod\n-Beta\n+Gamma\n")
	writeFileT(t, filepath.Join(profDir, "plugin_order.txt"),
		"# gorganizer plugin order — highest-priority first.\nSkyrim.esm\nGamma.esm\n")
	writeFileT(t, filepath.Join(profDir, "plugin_state.txt"),
		"# gorganizer plugin activation state; + enabled, - disabled.\n+Skyrim.esm\n-Gamma.esm\n")
	writeFileT(t, filepath.Join(profDir, "separators.yaml"),
		"# Gorganizer separators — visual grouping only. Safe to delete.\nview_enabled: true\nseparators:\n  - name: \"Core\"\n    visual_index: \"0000000000000010\"\n    collapsed: false\n")
	writeFileT(t, filepath.Join(profDir, "ini", "SkyrimCustom.ini"), "[Display]\niShadowMapResolution=4096\n")

	writeFileT(t, filepath.Join(modsDir, "Overwrite", "textures", "generated.dds"), "overwrite-payload")
	writeFileT(t, filepath.Join(modsDir, ".gorganizer-game.yaml"),
		"# Gorganizer per-game settings — auto-generated\nauto_install: true\n")
}

// exportTestArchive builds a fresh instance and exports it, returning the archive path.
func exportTestArchive(t *testing.T) string {
	t.Helper()
	archive := filepath.Join(t.TempDir(), "instance.tar.zst")
	setRoot(t, t.TempDir())
	buildInstance(t)
	_, err := Export(context.Background(), ExportOptions{
		GameID:              testGame,
		OutputPath:          archive,
		IncludeOverwrite:    true,
		IncludeGameSettings: true,
	}, nil)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	return archive
}

// snapshotTree maps rel path → contents for every regular file under dir.
func snapshotTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = string(data)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("walking %s: %v", dir, err)
	}
	return out
}

// assertNoStagingLeftovers fails if any import staging dirs remain under the mods or profiles dirs.
func assertNoStagingLeftovers(t *testing.T) {
	t.Helper()
	for _, dir := range []string{config.ModsDir(testGame), config.ProfilesDir(testGame)} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if len(e.Name()) >= len(".gorganizer-import-") && e.Name()[:len(".gorganizer-import-")] == ".gorganizer-import-" {
				t.Errorf("staging leftover %s in %s", e.Name(), dir)
			}
		}
	}
}

// TestExportImportRoundTrip locks that a full export → import reproduces every mod and profile file byte-for-byte.
func TestExportImportRoundTrip(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "instance.tar.zst")
	setRoot(t, t.TempDir())
	buildInstance(t)

	var progress []dto.TransferProgress
	sum, err := Export(context.Background(), ExportOptions{
		GameID:              testGame,
		OutputPath:          archive,
		IncludeOverwrite:    true,
		IncludeGameSettings: true,
	}, func(p dto.TransferProgress) { progress = append(progress, p) })
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if sum.ModsExported != 3 || sum.ProfilesTransferred != 1 {
		t.Errorf("export summary = %+v, want 3 mods / 1 profile", sum)
	}
	if sum.OutputPath != archive {
		t.Errorf("OutputPath = %q, want %q", sum.OutputPath, archive)
	}
	if len(progress) == 0 {
		t.Errorf("no export progress emitted")
	}
	wantMods := snapshotTree(t, config.ModsDir(testGame))
	wantProfiles := snapshotTree(t, config.ProfilesDir(testGame))

	setRoot(t, t.TempDir())
	progress = nil
	isum, err := Import(context.Background(), ImportOptions{
		GameID:      testGame,
		ArchivePath: archive,
		Policy:      dto.PolicyAbort,
	}, func(p dto.TransferProgress) { progress = append(progress, p) })
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if isum.ModsImported != 3 || isum.ProfilesTransferred != 1 {
		t.Errorf("import summary = %+v, want 3 mods / 1 profile", isum)
	}
	if len(isum.Skipped) != 0 || len(isum.Renamed) != 0 {
		t.Errorf("unexpected skips/renames: %+v", isum)
	}
	if len(progress) == 0 {
		t.Errorf("no import progress emitted")
	}

	gotMods := snapshotTree(t, config.ModsDir(testGame))
	gotProfiles := snapshotTree(t, config.ProfilesDir(testGame))
	if !reflect.DeepEqual(gotMods, wantMods) {
		t.Errorf("mods tree mismatch:\n got: %v\nwant: %v", keysOf(gotMods), keysOf(wantMods))
		for rel, want := range wantMods {
			if got, ok := gotMods[rel]; ok && got != want {
				t.Errorf("content mismatch at %s", rel)
			}
		}
	}
	if !reflect.DeepEqual(gotProfiles, wantProfiles) {
		t.Errorf("profiles tree mismatch:\n got: %v\nwant: %v", keysOf(gotProfiles), keysOf(wantProfiles))
	}
	assertNoStagingLeftovers(t)
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestRerunWithSkipIsIdempotent locks that re-importing with SKIP changes nothing and skips every item.
func TestRerunWithSkipIsIdempotent(t *testing.T) {
	archive := exportTestArchive(t)
	setRoot(t, t.TempDir())
	if _, err := Import(context.Background(), ImportOptions{
		GameID: testGame, ArchivePath: archive, Policy: dto.PolicyAbort,
	}, nil); err != nil {
		t.Fatalf("first Import: %v", err)
	}
	before := snapshotTree(t, config.ModsDir(testGame))

	sum, err := Import(context.Background(), ImportOptions{
		GameID: testGame, ArchivePath: archive, Policy: dto.PolicySkip,
	}, nil)
	if err != nil {
		t.Fatalf("second Import: %v", err)
	}
	if sum.ModsImported != 0 || sum.ProfilesTransferred != 0 {
		t.Errorf("rerun summary = %+v, want nothing imported", sum)
	}
	if len(sum.Skipped) != 4 {
		t.Errorf("skipped = %v, want 4 entries", sum.Skipped)
	}
	after := snapshotTree(t, config.ModsDir(testGame))
	if !reflect.DeepEqual(before, after) {
		t.Errorf("rerun with SKIP mutated the mods tree")
	}
	assertNoStagingLeftovers(t)
}

// TestExportSelection locks that explicit mod/profile selections narrow the archive.
func TestExportSelection(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "subset.tar.zst")
	setRoot(t, t.TempDir())
	buildInstance(t)
	sum, err := Export(context.Background(), ExportOptions{
		GameID:       testGame,
		OutputPath:   archive,
		ModFolders:   []string{"Beta"},
		ProfileNames: []string{"Default"},
	}, nil)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if sum.ModsExported != 1 || sum.ProfilesTransferred != 1 {
		t.Errorf("summary = %+v, want 1 mod / 1 profile", sum)
	}
	preview, err := Preview(testGame, archive)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if len(preview.Mods) != 1 || preview.Mods[0].Folder != "Beta" {
		t.Errorf("preview mods = %+v, want only Beta", preview.Mods)
	}
	if preview.IncludesOverwrite || preview.IncludesGameSettings {
		t.Errorf("preview flags = %+v, want overwrite/settings excluded", preview)
	}
}

// TestExportMissingModFails locks that naming a nonexistent mod folder fails the export.
func TestExportMissingModFails(t *testing.T) {
	setRoot(t, t.TempDir())
	buildInstance(t)
	_, err := Export(context.Background(), ExportOptions{
		GameID:     testGame,
		OutputPath: filepath.Join(t.TempDir(), "x.tar.zst"),
		ModFolders: []string{"NoSuchMod"},
	}, nil)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want not-exist", err)
	}
}

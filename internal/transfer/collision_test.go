package transfer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/mod"
)

// seedCollisions creates a pre-existing "Alpha Mod" and "Default" profile with sentinel content.
func seedCollisions(t *testing.T) {
	t.Helper()
	writeFileT(t, filepath.Join(config.ModsDir(testGame), "Alpha Mod", "AlphaMod.esp"), "sentinel-alpha")
	writeFileT(t, filepath.Join(config.ProfilesDir(testGame), "Default", "profile.json"),
		"{\"name\": \"Default\", \"game_id\": \"skyrimse\"}")
	writeFileT(t, filepath.Join(config.ProfilesDir(testGame), "Default", "modlist.txt"),
		"+SentinelMod\n")
}

func readFileT(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// TestImportPolicyAbort locks that ABORT fails before any write when a collision exists.
func TestImportPolicyAbort(t *testing.T) {
	archive := exportTestArchive(t)
	setRoot(t, t.TempDir())
	seedCollisions(t)

	_, err := Import(context.Background(), ImportOptions{
		GameID: testGame, ArchivePath: archive, Policy: dto.PolicyAbort,
	}, nil)
	var collision *TransferCollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("err = %v, want TransferCollisionError", err)
	}
	if collision.Name != "Alpha Mod" {
		t.Errorf("collision name = %q, want Alpha Mod", collision.Name)
	}
	if got := readFileT(t, filepath.Join(config.ModsDir(testGame), "Alpha Mod", "AlphaMod.esp")); got != "sentinel-alpha" {
		t.Errorf("existing mod content changed to %q", got)
	}
	if _, err := os.Stat(filepath.Join(config.ModsDir(testGame), "Beta")); !os.IsNotExist(err) {
		t.Errorf("ABORT wrote mod Beta before failing")
	}
	assertNoStagingLeftovers(t)
}

// TestImportPolicySkip locks that SKIP preserves existing items and imports the rest.
func TestImportPolicySkip(t *testing.T) {
	archive := exportTestArchive(t)
	setRoot(t, t.TempDir())
	seedCollisions(t)

	sum, err := Import(context.Background(), ImportOptions{
		GameID: testGame, ArchivePath: archive, Policy: dto.PolicySkip,
	}, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if sum.ModsImported != 2 || sum.ProfilesTransferred != 0 {
		t.Errorf("summary = %+v, want 2 mods / 0 profiles", sum)
	}
	skipped := append([]string{}, sum.Skipped...)
	sort.Strings(skipped)
	if !reflect.DeepEqual(skipped, []string{"Alpha Mod", "Default"}) {
		t.Errorf("skipped = %v", skipped)
	}
	if got := readFileT(t, filepath.Join(config.ModsDir(testGame), "Alpha Mod", "AlphaMod.esp")); got != "sentinel-alpha" {
		t.Errorf("SKIP overwrote existing mod: %q", got)
	}
	if got := readFileT(t, filepath.Join(config.ProfilesDir(testGame), "Default", "modlist.txt")); got != "+SentinelMod\n" {
		t.Errorf("SKIP overwrote existing profile: %q", got)
	}
	if got := readFileT(t, filepath.Join(config.ModsDir(testGame), "Beta", "Beta.esp")); got != "beta-esp-payload" {
		t.Errorf("Beta not imported: %q", got)
	}
	assertNoStagingLeftovers(t)
}

// TestImportPolicyRename locks folder increment, renamed map, and modlist rewrite through the rename map.
func TestImportPolicyRename(t *testing.T) {
	archive := exportTestArchive(t)
	setRoot(t, t.TempDir())
	seedCollisions(t)

	sum, err := Import(context.Background(), ImportOptions{
		GameID: testGame, ArchivePath: archive, Policy: dto.PolicyRename,
	}, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if sum.ModsImported != 3 || sum.ProfilesTransferred != 1 {
		t.Errorf("summary = %+v, want 3 mods / 1 profile", sum)
	}
	want := map[string]string{"Alpha Mod": "Alpha Mod (2)", "Default": "Default (2)"}
	if !reflect.DeepEqual(sum.Renamed, want) {
		t.Errorf("renamed = %v, want %v", sum.Renamed, want)
	}
	if got := readFileT(t, filepath.Join(config.ModsDir(testGame), "Alpha Mod", "AlphaMod.esp")); got != "sentinel-alpha" {
		t.Errorf("RENAME overwrote existing mod: %q", got)
	}
	if got := readFileT(t, filepath.Join(config.ModsDir(testGame), "Alpha Mod (2)", "AlphaMod.esp")); got != "alpha-esp-payload" {
		t.Errorf("renamed mod content = %q", got)
	}

	modlist := readFileT(t, filepath.Join(config.ProfilesDir(testGame), "Default (2)", "modlist.txt"))
	if !strings.Contains(modlist, "+Alpha Mod (2)\n") {
		t.Errorf("modlist not rewritten through rename map:\n%s", modlist)
	}
	if strings.Contains(modlist, "+Alpha Mod\n") {
		t.Errorf("modlist still references original folder:\n%s", modlist)
	}
	if !strings.Contains(modlist, "-Beta\n") || !strings.Contains(modlist, "+Gamma\n") {
		t.Errorf("modlist lost unrelated entries:\n%s", modlist)
	}
	if got := readFileT(t, filepath.Join(config.ProfilesDir(testGame), "Default", "modlist.txt")); got != "+SentinelMod\n" {
		t.Errorf("RENAME touched existing profile: %q", got)
	}
	if pj := readFileT(t, filepath.Join(config.ProfilesDir(testGame), "Default (2)", "profile.json")); !strings.Contains(pj, "\"Default (2)\"") {
		t.Errorf("renamed profile.json not relabeled:\n%s", pj)
	}
	assertNoStagingLeftovers(t)
}

// TestImportPolicyRenameIncrements locks that the rename counter skips already-taken candidates.
func TestImportPolicyRenameIncrements(t *testing.T) {
	archive := exportTestArchive(t)
	setRoot(t, t.TempDir())
	seedCollisions(t)
	writeFileT(t, filepath.Join(config.ModsDir(testGame), "Alpha Mod (2)", "x.esp"), "taken")

	sum, err := Import(context.Background(), ImportOptions{
		GameID: testGame, ArchivePath: archive, Policy: dto.PolicyRename,
	}, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if sum.Renamed["Alpha Mod"] != "Alpha Mod (3)" {
		t.Errorf("renamed = %v, want Alpha Mod (3)", sum.Renamed)
	}
	if got := readFileT(t, filepath.Join(config.ModsDir(testGame), "Alpha Mod (3)", "AlphaMod.esp")); got != "alpha-esp-payload" {
		t.Errorf("incremented rename content = %q", got)
	}
}

// TestImportPolicyOverwrite locks that OVERWRITE replaces the existing mod dir and profile.
func TestImportPolicyOverwrite(t *testing.T) {
	archive := exportTestArchive(t)
	setRoot(t, t.TempDir())
	seedCollisions(t)
	writeFileT(t, filepath.Join(config.ModsDir(testGame), "Alpha Mod", "stale-extra.esp"), "stale")

	sum, err := Import(context.Background(), ImportOptions{
		GameID: testGame, ArchivePath: archive, Policy: dto.PolicyOverwrite,
	}, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if sum.ModsImported != 3 || sum.ProfilesTransferred != 1 {
		t.Errorf("summary = %+v", sum)
	}
	if got := readFileT(t, filepath.Join(config.ModsDir(testGame), "Alpha Mod", "AlphaMod.esp")); got != "alpha-esp-payload" {
		t.Errorf("OVERWRITE kept old content: %q", got)
	}
	if _, err := os.Stat(filepath.Join(config.ModsDir(testGame), "Alpha Mod", "stale-extra.esp")); !os.IsNotExist(err) {
		t.Errorf("OVERWRITE merged instead of replacing the mod dir")
	}
	modlist := readFileT(t, filepath.Join(config.ProfilesDir(testGame), "Default", "modlist.txt"))
	if !strings.Contains(modlist, "+Alpha Mod\n") {
		t.Errorf("profile not replaced:\n%s", modlist)
	}
	assertNoStagingLeftovers(t)
}

// TestImportPolicyOverridePerMod locks that a per-mod override resolves a collision the default policy would abort on.
func TestImportPolicyOverridePerMod(t *testing.T) {
	archive := exportTestArchive(t)
	setRoot(t, t.TempDir())
	writeFileT(t, filepath.Join(config.ModsDir(testGame), "Alpha Mod", "AlphaMod.esp"), "sentinel-alpha")

	sum, err := Import(context.Background(), ImportOptions{
		GameID:      testGame,
		ArchivePath: archive,
		Policy:      dto.PolicyAbort,
		ModPolicyOverrides: map[string]dto.CollisionPolicy{
			"Alpha Mod": dto.PolicySkip,
		},
	}, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if sum.ModsImported != 2 {
		t.Errorf("summary = %+v, want 2 mods imported", sum)
	}
	if !reflect.DeepEqual(sum.Skipped, []string{"Alpha Mod"}) {
		t.Errorf("skipped = %v", sum.Skipped)
	}
	if got := readFileT(t, filepath.Join(config.ModsDir(testGame), "Alpha Mod", "AlphaMod.esp")); got != "sentinel-alpha" {
		t.Errorf("override SKIP overwrote existing mod: %q", got)
	}
}

// TestImportModFolderFilter locks that mod_folders narrows the import to the requested subset.
func TestImportModFolderFilter(t *testing.T) {
	archive := exportTestArchive(t)
	setRoot(t, t.TempDir())

	sum, err := Import(context.Background(), ImportOptions{
		GameID:       testGame,
		ArchivePath:  archive,
		Policy:       dto.PolicyAbort,
		ModFolders:   []string{"Gamma"},
		ProfileNames: []string{"Default"},
	}, nil)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if sum.ModsImported != 1 || sum.ProfilesTransferred != 1 {
		t.Errorf("summary = %+v, want 1 mod / 1 profile", sum)
	}
	if _, err := os.Stat(filepath.Join(config.ModsDir(testGame), "Beta")); !os.IsNotExist(err) {
		t.Errorf("unselected mod Beta was imported")
	}
	if _, err := os.Stat(filepath.Join(config.ModsDir(testGame), "Gamma", "Gamma.esm")); err != nil {
		t.Errorf("selected mod Gamma missing: %v", err)
	}
}

// TestRenameCandidate locks the "<base> (n)" increment behavior.
func TestRenameCandidate(t *testing.T) {
	taken := map[string]bool{"Mod (2)": true, "Mod (3)": true}
	got := renameCandidate("Mod", func(c string) bool { return taken[c] })
	if got != "Mod (4)" {
		t.Errorf("renameCandidate = %q, want Mod (4)", got)
	}
	got = renameCandidate("Fresh", func(c string) bool { return false })
	if got != "Fresh (2)" {
		t.Errorf("renameCandidate = %q, want Fresh (2)", got)
	}
}

// TestReservedImportStagePrefix locks that staging dirs never surface as mods.
func TestReservedImportStagePrefix(t *testing.T) {
	setRoot(t, t.TempDir())
	modsDir := config.ModsDir(testGame)
	writeFileT(t, filepath.Join(modsDir, "RealMod", "a.esp"), "x")
	writeFileT(t, filepath.Join(modsDir, mod.ImportStagePrefix+"deadbeef", "Ghost", "g.esp"), "x")
	mods, err := mod.ListMods(modsDir, testGame)
	if err != nil {
		t.Fatalf("ListMods: %v", err)
	}
	if len(mods) != 1 || mods[0].Name != "RealMod" {
		t.Errorf("mods = %+v, want only RealMod", mods)
	}
}

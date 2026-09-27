package daemon

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/steam"
	"github.com/parka/gorganizer/internal/vfs"
)

// steamFixture writes a manifest and a vanilla Data file into a disposable library.
func steamFixture(t *testing.T, appID int, dir string) (string, string) {
	t.Helper()
	steamapps := filepath.Join(t.TempDir(), "steamapps")
	install := filepath.Join(steamapps, "common", dir)
	writeFixture(t, filepath.Join(install, "Data", "original.esm"))
	manifest := filepath.Join(steamapps, "appmanifest_"+strconv.Itoa(appID)+".acf")
	body := `"AppState" { "appid" "` + strconv.Itoa(appID) + `" "installdir" "` + dir + `" "buildid" "123" "StateFlags" "4" "UpdateResult" "0" "LastUpdated" "100" "InstalledDepots" { "111" { "manifest" "456" } } }`
	if err := os.WriteFile(manifest, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return install, manifest
}

func TestActivationRecordsSteamBaseline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	install, manifest := steamFixture(t, 489830, "Skyrim Special Edition")
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		"skyrimse": {InstallPath: install, DataSubpath: "Data", SteamAppID: 489830},
	})
	var paths []string
	d.readSteamAppState = func(path string, id int) (steam.AppState, error) {
		paths = append(paths, path)
		return steam.ReadAppState(path, id)
	}
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Errorf("Steam state was never read for %q", install)
	}
	for _, path := range paths {
		if path != install {
			t.Errorf("Steam state read path = %q; want %q", path, install)
		}
	}
	baseline, err := d.mountMgrs["skyrimse"].StorefrontBaseline()
	if err != nil || baseline == nil {
		t.Fatalf("baseline = %+v, %v", baseline, err)
	}
	if baseline.Store != "steam" || baseline.AppID != 489830 || baseline.BuildID != "123" ||
		baseline.StateFlags != 4 || baseline.UpdateResult != "0" || baseline.LastUpdated != 100 ||
		baseline.DepotFingerprint == "" || baseline.CapturedAt.IsZero() {
		t.Errorf("incomplete baseline: %+v", baseline)
	}
	change, err := d.steamStateFor("skyrimse")
	if err != nil || change != vfs.StorefrontUnchanged {
		t.Errorf("state after mount = %s, %v", change, err)
	}
	writeFixture(t, filepath.Join(config.ModsDir("skyrimse"), "Added", "Added.esp"))
	if err := d.SetModList("skyrimse", "Default", []dto.ModListEntryResult{{ModName: "Added", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if err := d.RebuildVFS("skyrimse"); err != nil {
		t.Fatal(err)
	}
	applied, err := d.mountMgrs["skyrimse"].StorefrontBaseline()
	if err != nil || !reflect.DeepEqual(applied, baseline) {
		t.Errorf("baseline after Apply = %+v, %v; want %+v", applied, err, baseline)
	}
	body, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte(strings.Replace(string(body), `"buildid" "123"`, `"buildid" "124"`, 1)), 0644); err != nil {
		t.Fatal(err)
	}
	change, err = d.steamStateFor("skyrimse")
	if err != nil || change != vfs.StorefrontChanged {
		t.Errorf("state after Steam update = %s, %v; want changed", change, err)
	}
	if err := d.UnmountVFS("skyrimse"); err != nil {
		t.Fatal(err)
	}
}

func TestSyntheticGameUsesParentManifest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	parent, _ := steamFixture(t, 22380, "Fallout New Vegas")
	child := filepath.Join(t.TempDir(), "not-the-steam-install")
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		"falloutnv": {InstallPath: parent, SteamAppID: 22380, DataSubpath: "Data"},
		"ttw":       {InstallPath: child, SteamAppID: 0, LinkedFromGameID: "falloutnv", DataSubpath: "Data"},
	})
	var gotPath string
	var gotID int
	d.readSteamAppState = func(path string, id int) (steam.AppState, error) {
		gotPath, gotID = path, id
		return steam.ReadAppState(path, id)
	}
	if _, err := d.MountVFS("ttw", "Default"); err != nil {
		t.Fatal(err)
	}
	if gotPath != parent || gotID != 22380 {
		t.Errorf("TTW used Steam manifest for %d at %q; want 22380 at %q", gotID, gotPath, parent)
	}
	baseline, err := d.mountMgrs["ttw"].StorefrontBaseline()
	if err != nil || baseline == nil || baseline.AppID != 22380 || baseline.BuildID != "123" {
		t.Fatalf("TTW baseline = %+v, %v; want parent's install state", baseline, err)
	}
	if change, err := d.steamStateFor("ttw"); err != nil || change != vfs.StorefrontUnchanged {
		t.Errorf("TTW comparison = %s, %v; want unchanged", change, err)
	}
	if err := d.UnmountVFS("ttw"); err != nil {
		t.Fatal(err)
	}
}

func TestMissingManifestRecordsNoBaseline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	install := filepath.Join(t.TempDir(), "steamapps", "common", "Skyrim Special Edition")
	writeFixture(t, filepath.Join(install, "Data", "original.esm"))
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		"skyrimse": {InstallPath: install, DataSubpath: "Data", SteamAppID: 489830},
	})
	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatalf("missing manifest blocked mount: %v", err)
	}
	baseline, err := d.mountMgrs["skyrimse"].StorefrontBaseline()
	if err != nil || baseline != nil {
		t.Errorf("baseline = %+v, %v; want none", baseline, err)
	}
	if change, err := d.steamStateFor("skyrimse"); err != nil || change != vfs.StorefrontUnknown {
		t.Errorf("state = %s, %v; want unknown without a baseline", change, err)
	}
	if err := d.UnmountVFS("skyrimse"); err != nil {
		t.Fatal(err)
	}
}

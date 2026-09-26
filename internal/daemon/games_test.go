package daemon

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/gamedef"
	"github.com/parka/gorganizer/internal/tools"
)

func TestConfigureGameDefaultsDataSubpath(t *testing.T) {
	for _, tc := range []struct {
		gameID      string
		dataSubpath string
		want        string
	}{
		{gameID: "stardewvalley", want: "Mods"},
		{gameID: "morrowind", want: "Data Files"},
		{gameID: "skyrimse", want: "Data"},
		{gameID: "unknowngame", want: "Data"},
		{gameID: "stardewvalley", dataSubpath: "Custom", want: "Custom"},
	} {
		t.Run(tc.gameID+"/"+tc.want, func(t *testing.T) {
			d := newIsolatedDaemon(t, nil)
			install := t.TempDir()
			if err := d.ConfigureGame(tc.gameID, tc.gameID, 0, install, tc.dataSubpath); err != nil {
				t.Fatalf("ConfigureGame: %v", err)
			}
			if got := d.config.Games[tc.gameID].DataSubpath; got != tc.want {
				t.Errorf("DataSubpath = %q, want %q", got, tc.want)
			}
			if got, want := d.mountMgrs[tc.gameID].DataPath(), filepath.Join(install, tc.want); got != want {
				t.Errorf("mount DataPath = %q, want %q", got, want)
			}
		})
	}
}

// TestCapabilitiesFor locks the registry-derived capability set per game.
func TestCapabilitiesFor(t *testing.T) {
	for _, tc := range []struct {
		gameID string
		want   *dto.GameCapabilities
	}{
		{gameID: "skyrimse", want: &dto.GameCapabilities{
			Plugins: true, Ini: true, Loot: true,
			ModLoader: dto.ModLoaderKindNone, InstallLayout: dto.InstallLayoutDataRoot,
		}},
		{gameID: "stardewvalley", want: &dto.GameCapabilities{
			ModLoader: dto.ModLoaderKindSMAPI, InstallLayout: dto.InstallLayoutSMAPIManifest,
			ManifestDependencies: true,
		}},
		{gameID: "ttw", want: &dto.GameCapabilities{
			Plugins: true, Ini: true, Loot: true,
			ModLoader: dto.ModLoaderKindNone, InstallLayout: dto.InstallLayoutDataRoot,
		}},
		{gameID: "unknowngame", want: nil},
	} {
		t.Run(tc.gameID, func(t *testing.T) {
			if got := capabilitiesFor(tc.gameID); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("capabilitiesFor(%q) = %+v, want %+v", tc.gameID, got, tc.want)
			}
		})
	}
}

// TestListConfiguredGamesCarriesCapabilities checks configured games report their concrete capability sets.
func TestListConfiguredGamesCarriesCapabilities(t *testing.T) {
	d := newIsolatedDaemon(t, nil)
	for _, id := range []string{"stardewvalley", "skyrimse"} {
		if err := d.ConfigureGame(id, id, 0, t.TempDir(), ""); err != nil {
			t.Fatalf("ConfigureGame(%s): %v", id, err)
		}
	}
	games, err := d.ListConfiguredGames()
	if err != nil {
		t.Fatalf("ListConfiguredGames: %v", err)
	}
	assertGameCapabilities(t, games, map[string]*dto.GameCapabilities{
		"stardewvalley": stardewCapabilities(),
		"skyrimse":      skyrimSECapabilities(),
	})
}

// TestDetectInstalledGamesCarriesCapabilities checks games found in a fake Steam library report concrete capabilities.
func TestDetectInstalledGamesCarriesCapabilities(t *testing.T) {
	d := newIsolatedDaemon(t, nil)
	steamRoot := filepath.Join(os.Getenv("XDG_DATA_HOME"), "Steam")
	writeTestFile(t, filepath.Join(steamRoot, "steamapps", "libraryfolders.vdf"),
		"\"libraryfolders\"\n{\n\t\"0\"\n\t{\n\t\t\"path\"\t\t\""+steamRoot+"\"\n\t}\n}\n")
	writeTestFile(t, filepath.Join(steamRoot, "steamapps", "appmanifest_413150.acf"),
		"\"AppState\"\n{\n\t\"appid\"\t\t\"413150\"\n\t\"name\"\t\t\"Stardew Valley\"\n\t\"installdir\"\t\t\"Stardew Valley\"\n}\n")
	writeTestFile(t, filepath.Join(steamRoot, "steamapps", "common", "Stardew Valley", "StardewValley"), "")
	writeTestFile(t, filepath.Join(steamRoot, "steamapps", "appmanifest_489830.acf"),
		"\"AppState\"\n{\n\t\"appid\"\t\t\"489830\"\n\t\"name\"\t\t\"Skyrim Special Edition\"\n\t\"installdir\"\t\t\"Skyrim Special Edition\"\n}\n")
	writeTestFile(t, filepath.Join(steamRoot, "steamapps", "common", "Skyrim Special Edition", "SkyrimSE.exe"), "")
	writeTestFile(t, filepath.Join(steamRoot, "steamapps", "common", "Skyrim Special Edition", "Data", "Skyrim.esm"), "")

	games, err := d.DetectInstalledGames()
	if err != nil {
		t.Fatalf("DetectInstalledGames: %v", err)
	}
	assertGameCapabilities(t, games, map[string]*dto.GameCapabilities{
		"stardewvalley": stardewCapabilities(),
		"skyrimse":      skyrimSECapabilities(),
	})
}

// TestCapabilitiesForDataRootRegistry checks every data-root registry game keeps the Bethesda feature set.
func TestCapabilitiesForDataRootRegistry(t *testing.T) {
	for _, def := range gamedef.All {
		if def.Layout != gamedef.LayoutDataRoot {
			continue
		}
		t.Run(def.ID, func(t *testing.T) {
			caps := capabilitiesFor(def.ID)
			if caps == nil {
				t.Fatal("capabilities unset")
			}
			_, wantLoot := tools.LOOTGameID(def.ID)
			if !caps.Plugins || !caps.Ini || caps.Loot != wantLoot {
				t.Errorf("plugins=%v ini=%v loot=%v, want true true %v", caps.Plugins, caps.Ini, caps.Loot, wantLoot)
			}
			if caps.InstallLayout != dto.InstallLayoutDataRoot {
				t.Errorf("InstallLayout = %d, want DataRoot", caps.InstallLayout)
			}
		})
	}
}

// TestInstallLayoutResultRegistry checks no non-data-root layout in the registry maps to the DataRoot wire value.
func TestInstallLayoutResultRegistry(t *testing.T) {
	seen := map[gamedef.InstallLayout]bool{}
	for _, def := range gamedef.All {
		seen[def.Layout] = true
	}
	for layout := range seen {
		got := installLayoutResult(layout)
		if got == dto.InstallLayoutUnspecified {
			t.Errorf("layout %s maps to Unspecified", layout)
		}
		if (layout == gamedef.LayoutDataRoot) != (got == dto.InstallLayoutDataRoot) {
			t.Errorf("layout %s maps to %d", layout, got)
		}
	}
	if got := installLayoutResult(gamedef.InstallLayout(250)); got != dto.InstallLayoutUnspecified {
		t.Errorf("unknown layout maps to %d, want Unspecified", got)
	}
}

// stardewCapabilities returns the expected Stardew Valley capability set.
func stardewCapabilities() *dto.GameCapabilities {
	return &dto.GameCapabilities{
		ModLoader: dto.ModLoaderKindSMAPI, InstallLayout: dto.InstallLayoutSMAPIManifest,
		ManifestDependencies: true,
	}
}

// skyrimSECapabilities returns the expected Skyrim Special Edition capability set.
func skyrimSECapabilities() *dto.GameCapabilities {
	return &dto.GameCapabilities{
		Plugins: true, Ini: true, Loot: true,
		ModLoader: dto.ModLoaderKindNone, InstallLayout: dto.InstallLayoutDataRoot,
	}
}

// assertGameCapabilities checks the listed games are exactly want and carry the expected capabilities.
func assertGameCapabilities(t *testing.T, games []dto.GameInfo, want map[string]*dto.GameCapabilities) {
	t.Helper()
	if len(games) != len(want) {
		t.Fatalf("got %d games, want %d: %+v", len(games), len(want), games)
	}
	for _, g := range games {
		w, ok := want[g.GameID]
		if !ok {
			t.Errorf("unexpected game %q", g.GameID)
			continue
		}
		if !reflect.DeepEqual(g.Capabilities, w) {
			t.Errorf("%s capabilities = %+v, want %+v", g.GameID, g.Capabilities, w)
		}
	}
}

// writeTestFile creates path with content, making parent directories as needed.
func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

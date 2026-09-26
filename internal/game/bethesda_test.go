package game

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKnownGames(t *testing.T) {
	if len(KnownGames) != 11 {
		t.Errorf("expected 11 known games, got %d", len(KnownGames))
	}

	expectedIDs := map[uint32]string{
		22320:   "morrowind",
		22330:   "oblivion",
		72850:   "skyrim",
		489830:  "skyrimse",
		22370:   "fallout3",
		22380:   "falloutnv",
		377160:  "fallout4",
		1716740: "starfield",
		2623190: "oblivionremastered",
		413150:  "stardewvalley",
	}

	for appID, expectedID := range expectedIDs {
		g, ok := FindByAppID(appID)
		if !ok {
			t.Errorf("FindByAppID(%d) not found", appID)
			continue
		}
		if g.ID != expectedID {
			t.Errorf("FindByAppID(%d).ID = %q, want %q", appID, g.ID, expectedID)
		}
	}

	ttw, ok := FindByID("ttw")
	if !ok {
		t.Fatal("FindByID(ttw) not found")
	}
	if !ttw.Synthetic {
		t.Errorf("TTW must be marked synthetic")
	}
	if ttw.ParentGameID != "falloutnv" {
		t.Errorf("TTW parent = %q, want %q", ttw.ParentGameID, "falloutnv")
	}
	if _, ok := FindByAppID(0); ok {
		t.Errorf("FindByAppID(0) must not return the synthetic TTW entry")
	}
}

func TestParseAppManifestNestedGameLayout(t *testing.T) {
	for _, marker := range []string{
		"OblivionRemastered.exe",
		"OblivionRemastered/Binaries/Win64/OblivionRemastered-Win64-Shipping.exe",
	} {
		t.Run(filepath.Base(marker), func(t *testing.T) {
			library := t.TempDir()
			installDir := filepath.Join(library, "steamapps", "common", "Oblivion Remastered")
			dataDir := filepath.Join(installDir, "OblivionRemastered", "Content", "Dev", "ObvData", "Data")
			writeTestFile(t, filepath.Join(dataDir, "Oblivion.esm"))
			writeTestFile(t, filepath.Join(installDir, filepath.FromSlash(marker)))

			manifest := filepath.Join(library, "steamapps", "appmanifest_2623190.acf")
			writeTestManifest(t, manifest, 2623190, "Oblivion Remastered")
			got, err := parseAppManifest(manifest, library)
			if err != nil {
				t.Fatalf("parseAppManifest: %v", err)
			}
			if got == nil {
				t.Fatal("parseAppManifest returned nil for valid remaster layout")
			}
			if got.ID != "oblivionremastered" {
				t.Fatalf("ID = %q, want oblivionremastered", got.ID)
			}
			if got.InstallPath != installDir {
				t.Fatalf("InstallPath = %q, want %q", got.InstallPath, installDir)
			}
			if got.DataPath != dataDir {
				t.Fatalf("DataPath = %q, want %q", got.DataPath, dataDir)
			}
		})
	}
}

func TestParseAppManifestRejectsIncompleteRemaster(t *testing.T) {
	library := t.TempDir()
	installDir := filepath.Join(library, "steamapps", "common", "Oblivion Remastered")
	writeTestFile(t, filepath.Join(installDir, "OblivionRemastered.exe"))
	if err := os.MkdirAll(filepath.Join(installDir, "OblivionRemastered", "Content", "Dev", "ObvData", "Data"), 0755); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(library, "steamapps", "appmanifest_2623190.acf")
	writeTestManifest(t, manifest, 2623190, "Oblivion Remastered")

	got, err := parseAppManifest(manifest, library)
	if err != nil {
		t.Fatalf("parseAppManifest: %v", err)
	}
	if got != nil {
		t.Fatalf("expected missing Oblivion.esm to reject install, got %+v", got)
	}
}

func TestParseAppManifestUsesMorrowindDataFiles(t *testing.T) {
	library := t.TempDir()
	installDir := filepath.Join(library, "steamapps", "common", "Morrowind")
	dataDir := filepath.Join(installDir, "Data Files")
	writeTestFile(t, filepath.Join(installDir, "Morrowind.exe"))
	writeTestFile(t, filepath.Join(dataDir, "Morrowind.esm"))
	manifest := filepath.Join(library, "steamapps", "appmanifest_22320.acf")
	writeTestManifest(t, manifest, 22320, "Morrowind")

	got, err := parseAppManifest(manifest, library)
	if err != nil {
		t.Fatalf("parseAppManifest: %v", err)
	}
	if got == nil || got.DataPath != dataDir {
		t.Fatalf("DataPath = %v, want %q", got, dataDir)
	}
}

func TestParseAppManifestStardewOptionalMods(t *testing.T) {
	nativeMarkers := []string{"Stardew Valley", "StardewValley"}
	for _, tc := range []struct {
		name      string
		mods      string
		markers   []string
		wantFound bool
	}{
		{name: "absent mods accepted", markers: nativeMarkers, wantFound: true},
		{name: "real mods directory accepted", mods: "dir", markers: nativeMarkers, wantFound: true},
		{name: "regular mods file rejected", mods: "file", markers: nativeMarkers},
		{name: "symlink mods rejected", mods: "symlink", markers: nativeMarkers},
		{name: "windows depot decoy rejected", markers: []string{"Stardew Valley.exe"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			library := t.TempDir()
			installDir := filepath.Join(library, "steamapps", "common", "Stardew Valley")
			if err := os.MkdirAll(installDir, 0755); err != nil {
				t.Fatal(err)
			}
			for _, marker := range tc.markers {
				writeTestFile(t, filepath.Join(installDir, marker))
			}
			modsPath := filepath.Join(installDir, "Mods")
			switch tc.mods {
			case "dir":
				if err := os.Mkdir(modsPath, 0755); err != nil {
					t.Fatal(err)
				}
			case "file":
				writeTestFile(t, modsPath)
			case "symlink":
				target := filepath.Join(library, "mods-target")
				if err := os.Mkdir(target, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, modsPath); err != nil {
					t.Fatal(err)
				}
			}
			manifest := filepath.Join(library, "steamapps", "appmanifest_413150.acf")
			writeTestManifest(t, manifest, 413150, "Stardew Valley")

			got, err := parseAppManifest(manifest, library)
			if err != nil {
				t.Fatalf("parseAppManifest: %v", err)
			}
			if tc.wantFound {
				if got == nil {
					t.Fatal("parseAppManifest returned nil for valid Stardew layout")
				}
				if got.DataPath != modsPath || !strings.HasSuffix(got.DataPath, "/Mods") {
					t.Errorf("DataPath = %q, want %q", got.DataPath, modsPath)
				}
				return
			}
			if got != nil {
				t.Fatalf("parseAppManifest returned %+v, want rejection", got)
			}
		})
	}
}

func TestParseAppManifestRejectsBethesdaWithoutData(t *testing.T) {
	library := t.TempDir()
	installDir := filepath.Join(library, "steamapps", "common", "Skyrim Special Edition")
	writeTestFile(t, filepath.Join(installDir, "SkyrimSE.exe"))
	manifest := filepath.Join(library, "steamapps", "appmanifest_489830.acf")
	writeTestManifest(t, manifest, 489830, "Skyrim Special Edition")

	got, err := parseAppManifest(manifest, library)
	if err != nil {
		t.Fatalf("parseAppManifest: %v", err)
	}
	if got != nil {
		t.Fatalf("parseAppManifest returned %+v, want rejection", got)
	}
}

func writeTestManifest(t *testing.T, path string, appID uint32, installDir string) {
	t.Helper()
	contents := fmt.Sprintf(`"AppState"
{
	"appid"	"%d"
	"name"	"Test Game"
	"installdir"	"%s"
}
`, appID, installDir)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}
}

func writeTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture"), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestFindByID(t *testing.T) {
	g, ok := FindByID("skyrimse")
	if !ok {
		t.Fatal("FindByID(skyrimse) not found")
	}
	if g.SteamAppID != 489830 {
		t.Errorf("SteamAppID = %d, want 489830", g.SteamAppID)
	}

	_, ok = FindByID("nonexistent")
	if ok {
		t.Error("FindByID(nonexistent) should return false")
	}
}

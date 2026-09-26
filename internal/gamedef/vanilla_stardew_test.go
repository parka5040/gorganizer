package gamedef

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const gameOwnedSteamAppID = "steam_appid.txt"

var upstreamSMAPIUninstallPaths = []string{
	"StardewModdingAPI", "StardewModdingAPI.deps.json", "StardewModdingAPI.dll", "StardewModdingAPI.exe",
	"StardewModdingAPI.exe.config", "StardewModdingAPI.exe.mdb", "StardewModdingAPI.pdb",
	"StardewModdingAPI.runtimeconfig.json", "StardewModdingAPI.xml", "smapi-internal", "steam_appid.txt",
	"libgdiplus.dylib", "Mods/.cache", "Mods/ErrorHandler", "Mods/TrainerMod", "Mono.Cecil.Rocks.dll",
	"StardewModdingAPI-settings.json", "StardewModdingAPI.AssemblyRewriters.dll", "0Harmony.dll", "0Harmony.pdb",
	"Mono.Cecil.dll", "Newtonsoft.Json.dll", "StardewModdingAPI.config.json", "StardewModdingAPI.crash.marker",
	"StardewModdingAPI.metadata.json", "StardewModdingAPI.update.marker", "StardewModdingAPI.Toolkit.dll",
	"StardewModdingAPI.Toolkit.pdb", "StardewModdingAPI.Toolkit.xml", "StardewModdingAPI.Toolkit.CoreInterfaces.dll",
	"StardewModdingAPI.Toolkit.CoreInterfaces.pdb", "StardewModdingAPI.Toolkit.CoreInterfaces.xml",
	"StardewModdingAPI-x64.exe",
}

// vanillaStardewRoot returns the case-folded top-level names of a vanilla Stardew Valley 1.6.15 Linux install.
func vanillaStardewRoot(t *testing.T) map[string]bool {
	t.Helper()
	file, err := os.Open(filepath.Join("testdata", "stardew_vanilla_root.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	names := map[string]bool{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if name := strings.TrimSpace(scanner.Text()); name != "" {
			names[strings.ToLower(name)] = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(names) < 200 || !names["stardew valley.dll"] || !names["content"] {
		t.Fatalf("vanilla listing has %d names, want the full Stardew Valley install", len(names))
	}
	return names
}

// topLevel returns the case-folded first segment of a slash-separated game-root path.
func topLevel(rel string) string {
	first, _, _ := strings.Cut(rel, "/")
	return strings.ToLower(first)
}

// TestSMAPIUninstallNeverRemovesAVanillaGameFile locks that the loader's uninstall paths never name a vanilla game file, and that the game's own steam_appid.txt, which upstream SMAPI deletes, is left to the originals store.
func TestSMAPIUninstallNeverRemovesAVanillaGameFile(t *testing.T) {
	vanilla := vanillaStardewRoot(t)
	def, ok := ByID("stardewvalley")
	if !ok || def.ModLoader == nil {
		t.Fatal("stardewvalley has no mod loader row")
	}
	loader := def.ModLoader
	removed := append(append([]string(nil), loader.UninstallPaths...), loader.LauncherBackupName)
	for _, rel := range removed {
		if vanilla[topLevel(rel)] {
			t.Errorf("uninstall path %q is a vanilla game file", rel)
		}
	}
	if !vanilla[gameOwnedSteamAppID] {
		t.Fatalf("the vanilla listing lacks the game-owned %s", gameOwnedSteamAppID)
	}
	for _, rel := range loader.UninstallPaths {
		if strings.EqualFold(rel, gameOwnedSteamAppID) {
			t.Errorf("uninstall paths name the game-owned %s, which only its saved original may restore", rel)
		}
	}

	var upstreamVanilla []string
	for _, rel := range upstreamSMAPIUninstallPaths {
		if vanilla[topLevel(rel)] {
			upstreamVanilla = append(upstreamVanilla, rel)
		}
	}
	if len(upstreamVanilla) != 1 || upstreamVanilla[0] != gameOwnedSteamAppID {
		t.Errorf("upstream SMAPI uninstall removes vanilla files %v, want only %s", upstreamVanilla, gameOwnedSteamAppID)
	}

	var mirrored []string
	for _, rel := range upstreamSMAPIUninstallPaths {
		if rel != gameOwnedSteamAppID {
			mirrored = append(mirrored, rel)
		}
	}
	got := append([]string(nil), loader.UninstallPaths...)
	sort.Strings(got)
	sort.Strings(mirrored)
	if strings.Join(got, "\n") != strings.Join(mirrored, "\n") {
		t.Errorf("uninstall paths = %v, want upstream's list without %s: %v", got, gameOwnedSteamAppID, mirrored)
	}
}

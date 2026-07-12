package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReservePrefixPathRejectsConcurrentPreparation(t *testing.T) {
	m := &Manager{}
	release, err := m.reservePrefixPath("/tmp/gorganizer-prefix-test")
	if err != nil {
		t.Fatalf("first reservation: %v", err)
	}
	if _, err := m.reservePrefixPath("/tmp/gorganizer-prefix-test"); err == nil {
		t.Fatal("second reservation unexpectedly succeeded")
	}
	release()
	release()
	releaseAgain, err := m.reservePrefixPath("/tmp/gorganizer-prefix-test")
	if err != nil {
		t.Fatalf("reservation after release: %v", err)
	}
	releaseAgain()
}

// fakeSteamRoot lays out a minimal Steam directory tree for ResolveProtonRuntime tests.
func fakeSteamRoot(t *testing.T, opts struct {
	manifestRequiresAppID string
	withAppManifest       bool
	withEntryPoint        bool
}) (steamRoot, protonPath string) {
	t.Helper()
	steamRoot = t.TempDir()

	protonDir := filepath.Join(steamRoot, "steamapps", "common", "Proton 11.0")
	if err := os.MkdirAll(protonDir, 0755); err != nil {
		t.Fatal(err)
	}
	protonPath = filepath.Join(protonDir, "proton")
	if err := os.WriteFile(protonPath, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}

	tm := `"manifest"
{
  "version" "2"
  "commandline" "/proton %verb%"
`
	if opts.manifestRequiresAppID != "" {
		tm += `  "require_tool_appid" "` + opts.manifestRequiresAppID + `"` + "\n"
	}
	tm += `}` + "\n"
	if err := os.WriteFile(filepath.Join(protonDir, "toolmanifest.vdf"), []byte(tm), 0644); err != nil {
		t.Fatal(err)
	}

	if opts.withAppManifest {
		appManifest := `"AppState"
{
  "appid"		"4183110"
  "name"		"Steam Linux Runtime 4.0"
  "installdir"		"SteamLinuxRuntime_4"
}
`
		if err := os.WriteFile(
			filepath.Join(steamRoot, "steamapps", "appmanifest_4183110.acf"),
			[]byte(appManifest), 0644); err != nil {
			t.Fatal(err)
		}
	}

	if opts.withEntryPoint {
		runtimeDir := filepath.Join(steamRoot, "steamapps", "common", "SteamLinuxRuntime_4")
		if err := os.MkdirAll(runtimeDir, 0755); err != nil {
			t.Fatal(err)
		}
		ep := filepath.Join(runtimeDir, "_v2-entry-point")
		if err := os.WriteFile(ep, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}

	return steamRoot, protonPath
}

func TestResolveProtonRuntime_HappyPath(t *testing.T) {
	steamRoot, protonPath := fakeSteamRoot(t, struct {
		manifestRequiresAppID string
		withAppManifest       bool
		withEntryPoint        bool
	}{
		manifestRequiresAppID: "4183110",
		withAppManifest:       true,
		withEntryPoint:        true,
	})

	entry, name := ResolveProtonRuntime(protonPath, steamRoot)
	wantEntry := filepath.Join(steamRoot, "steamapps", "common", "SteamLinuxRuntime_4", "_v2-entry-point")
	if entry != wantEntry {
		t.Errorf("entryPoint = %q, want %q", entry, wantEntry)
	}
	if name != "SteamLinuxRuntime_4" {
		t.Errorf("runtimeName = %q, want %q", name, "SteamLinuxRuntime_4")
	}
}

func TestResolveProtonRuntime_NoRequireAppID(t *testing.T) {
	steamRoot, protonPath := fakeSteamRoot(t, struct {
		manifestRequiresAppID string
		withAppManifest       bool
		withEntryPoint        bool
	}{
		manifestRequiresAppID: "",
		withAppManifest:       false,
		withEntryPoint:        false,
	})

	entry, name := ResolveProtonRuntime(protonPath, steamRoot)
	if entry != "" || name != "" {
		t.Errorf("expected (\"\",\"\"), got (%q,%q)", entry, name)
	}
}

func TestResolveProtonRuntime_RuntimeNotInstalled(t *testing.T) {
	steamRoot, protonPath := fakeSteamRoot(t, struct {
		manifestRequiresAppID string
		withAppManifest       bool
		withEntryPoint        bool
	}{
		manifestRequiresAppID: "4183110",
		withAppManifest:       false,
		withEntryPoint:        false,
	})

	entry, _ := ResolveProtonRuntime(protonPath, steamRoot)
	if entry != "" {
		t.Errorf("expected empty entry when appmanifest missing, got %q", entry)
	}
}

func TestResolveProtonRuntime_EntryPointMissing(t *testing.T) {
	steamRoot, protonPath := fakeSteamRoot(t, struct {
		manifestRequiresAppID string
		withAppManifest       bool
		withEntryPoint        bool
	}{
		manifestRequiresAppID: "4183110",
		withAppManifest:       true,
		withEntryPoint:        false,
	})

	entry, _ := ResolveProtonRuntime(protonPath, steamRoot)
	if entry != "" {
		t.Errorf("expected empty entry when _v2-entry-point missing, got %q", entry)
	}
}

func TestReadVDFKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest")
	contents := `"manifest"
{
  "version" "2"
  "require_tool_appid" "1628350"
  "use_sessions" "1"
}

func TestLaunchWorkingDirUsesLoaderDirectory(t *testing.T) {
	gameRoot := filepath.Join(string(filepath.Separator), "games", "Oblivion Remastered")
	loader := filepath.Join(gameRoot, "OblivionRemastered", "Binaries", "Win64", "obse64_loader.exe")
	want := filepath.Dir(loader)
	if got := launchWorkingDir(gameRoot, loader, true); got != want {
		t.Errorf("launchWorkingDir = %q, want %q", got, want)
	}
	if got := launchWorkingDir(gameRoot, loader, false); got != gameRoot {
		t.Errorf("non-tool launch working dir = %q, want game root %q", got, gameRoot)
	}
}
`
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := readVDFKey(path, "require_tool_appid")
	if err != nil {
		t.Fatalf("readVDFKey: %v", err)
	}
	if got != "1628350" {
		t.Errorf("got %q, want %q", got, "1628350")
	}

	missing, err := readVDFKey(path, "not_present")
	if err != nil {
		t.Fatalf("readVDFKey for missing key: %v", err)
	}
	if missing != "" {
		t.Errorf("missing key should yield empty string, got %q", missing)
	}
}

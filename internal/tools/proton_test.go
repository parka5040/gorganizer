package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parka/gorganizer/internal/config"
)

// TestProtonResolvesFromOwningRoot checks prefixes, client data and versions for a second Steam installation.
func TestProtonResolvesFromOwningRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	native := filepath.Join(home, ".local", "share", "Steam")
	flatpak := filepath.Join(home, ".var", "app", "com.valvesoftware.Steam", ".local", "share", "Steam")
	extra := filepath.Join(home, "other-library")
	for _, root := range []string{native, flatpak, extra} {
		if err := os.MkdirAll(filepath.Join(root, "steamapps"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	vdf := `"libraryfolders" { "0" { "path" "` + extra + `" } }`
	if err := os.WriteFile(filepath.Join(flatpak, "steamapps", "libraryfolders.vdf"), []byte(vdf), 0644); err != nil {
		t.Fatal(err)
	}
	install := filepath.Join(extra, "steamapps", "common", "Test Game")
	for _, path := range []string{
		install,
		filepath.Join(native, "steamapps", "compatdata", "123", "pfx"),
		filepath.Join(extra, "steamapps", "compatdata", "123", "pfx"),
		filepath.Join(flatpak, "steamapps", "common", "Proton 11"),
	} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	proton := filepath.Join(flatpak, "steamapps", "common", "Proton 11", "proton")
	if err := os.WriteFile(proton, []byte("fixture"), 0755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.GameConfig{InstallPath: install, SteamAppID: 123, SteamLibraryPath: native}
	root, library, err := owningSteamLibrary(cfg)
	if err != nil || root != flatpak || library != extra {
		t.Fatalf("owningSteamLibrary = (%q, %q, %v), want (%q, %q)", root, library, err, flatpak, extra)
	}
	compat, err := ResolveCompatDataPath(cfg, 0)
	if err != nil || compat != filepath.Join(extra, "steamapps", "compatdata", "123") {
		t.Fatalf("compatdata = %q, err = %v", compat, err)
	}
	resolvedLibrary, err := ResolveSteamLibrary(cfg)
	if err != nil || resolvedLibrary != extra {
		t.Fatalf("resolved library = %q, err = %v, want %q", resolvedLibrary, err, extra)
	}
	versions := detectProtonVersionsAllLibraries(root)
	if len(versions) != 1 || versions[0].Path != proton {
		t.Fatalf("versions = %+v, want %s", versions, proton)
	}
	env := buildSteamParityEnv(compat, root, "123", install, "")
	if !containsEnv(env, "STEAM_COMPAT_CLIENT_INSTALL_PATH="+flatpak) {
		t.Fatalf("client path not set to owning root: %v", env)
	}
}

// TestProtonUsesUnlistedConfiguredLibrary checks an existing configured library remains the prefix location without a VDF entry.
func TestProtonUsesUnlistedConfiguredLibrary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	root := filepath.Join(home, ".local", "share", "Steam")
	library := filepath.Join(home, "unlisted-library")
	alias := filepath.Join(home, "library-alias")
	install := filepath.Join(library, "steamapps", "common", "Game")
	for _, path := range []string{
		filepath.Join(root, "steamapps", "compatdata", "123", "pfx"),
		filepath.Join(library, "steamapps", "compatdata", "123", "pfx"),
		install,
	} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(library, alias); err != nil {
		t.Fatal(err)
	}
	cfg := &config.GameConfig{InstallPath: install, SteamLibraryPath: alias, SteamAppID: 123}
	owner, selected, err := owningSteamLibrary(cfg)
	if err != nil || owner != root || selected != library {
		t.Fatalf("owningSteamLibrary = (%q, %q, %v), want (%q, %q)", owner, selected, err, root, library)
	}
	compat, err := ResolveCompatDataPath(cfg, 0)
	if err != nil || compat != filepath.Join(library, "steamapps", "compatdata", "123") {
		t.Fatalf("compatdata = %q, err = %v, want unlisted library prefix", compat, err)
	}
}

func containsEnv(env []string, want string) bool {
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}

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

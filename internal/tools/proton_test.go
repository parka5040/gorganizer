package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeSteamRoot lays out a minimal Steam directory tree resembling what
// our ResolveProtonRuntime parses on the user's actual install: a Proton
// dir with a toolmanifest declaring require_tool_appid, an appmanifest
// for that appid pointing at a runtime install dir, and that runtime's
// _v2-entry-point script. Each test specifies which subset of those
// pieces should exist so we can exercise both the happy and skip paths.
func fakeSteamRoot(t *testing.T, opts struct {
	manifestRequiresAppID string // "" → omit the require_tool_appid line entirely
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
	// Older Protons / Proton-GE may omit the require_tool_appid field.
	// We don't guess: return ("","") so the caller falls back to direct
	// invocation rather than maybe-running inside the wrong runtime.
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
	// Proton declares it needs a runtime, but the user hasn't installed it
	// (no appmanifest). Return empty so the caller falls back with a
	// useful warning.
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
	// Appmanifest present but the runtime install is incomplete (no
	// _v2-entry-point). Same fallback as above.
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

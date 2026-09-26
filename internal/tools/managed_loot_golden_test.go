package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

var installedAtJSON = regexp.MustCompile(`"installed_at": "[^"]+"`)

func TestLOOTInstallerManifestGoldenBytes(t *testing.T) {
	payload := []byte("fake archive")
	digest := sha256.Sum256(payload)
	installer := NewLOOTInstaller(t.TempDir(), testHTTPClient(string(payload)))
	installer.extract = func(_ context.Context, _, destination string) error {
		return os.WriteFile(filepath.Join(destination, "LOOT.exe"), []byte("MZ"), 0755)
	}
	release := func(version string) LOOTRelease {
		return LOOTRelease{
			Tag: version, Version: version, AssetID: 1, AssetName: "loot_" + version + "-win64.7z",
			URL: "https://example.test/loot.7z", SHA256: hex.EncodeToString(digest[:]),
		}
	}
	lootRoot := filepath.Join(installer.Root, "loot")
	manifest := func(version string) string {
		return "{\n" +
			"  \"schema_version\": 1,\n" +
			"  \"tool_id\": \"loot\",\n" +
			"  \"tag\": \"" + version + "\",\n" +
			"  \"version\": \"" + version + "\",\n" +
			"  \"asset_id\": 1,\n" +
			"  \"asset_name\": \"loot_" + version + "-win64.7z\",\n" +
			"  \"asset_url\": \"https://example.test/loot.7z\",\n" +
			"  \"sha256\": \"" + hex.EncodeToString(digest[:]) + "\",\n" +
			"  \"executable_rel\": \"LOOT.exe\",\n" +
			"  \"installed_at\": \"<installed-at>\",\n" +
			"  \"distribution\": \"official GitHub portable archive\",\n" +
			"  \"license\": \"GPL-3.0-or-later\"\n" +
			"}"
	}
	assertManifest := func(version string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(lootRoot, version, "gorganizer-manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		var parsed struct {
			InstalledAt string `json:"installed_at"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatal(err)
		}
		if parsed.InstalledAt == "" {
			t.Fatal("installed_at is missing")
		}
		if _, err := time.Parse(time.RFC3339, parsed.InstalledAt); err != nil {
			t.Fatalf("installed_at is not RFC3339: %v", err)
		}
		if got := string(installedAtJSON.ReplaceAll(data, []byte(`"installed_at": "<installed-at>"`))); got != manifest(version) {
			t.Fatalf("manifest bytes for %s = %q; want %q", version, got, manifest(version))
		}
	}

	if _, err := installer.Install(t.Context(), release("0.29.0")); err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(filepath.Join(lootRoot, "current.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(current), "{\n  \"schema_version\": 1,\n  \"active_version\": \"0.29.0\"\n}"; got != want {
		t.Fatalf("first current.json bytes = %q; want %q", got, want)
	}
	assertManifest("0.29.0")

	if _, err := installer.Install(t.Context(), release("0.29.1")); err != nil {
		t.Fatal(err)
	}
	current, err = os.ReadFile(filepath.Join(lootRoot, "current.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(current), "{\n  \"schema_version\": 1,\n  \"active_version\": \"0.29.1\",\n  \"previous_version\": \"0.29.0\"\n}"; got != want {
		t.Fatalf("second current.json bytes = %q; want %q", got, want)
	}
	assertManifest("0.29.1")

	if _, err := installer.Rollback(); err != nil {
		t.Fatal(err)
	}
	current, err = os.ReadFile(filepath.Join(lootRoot, "current.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(current), "{\n  \"schema_version\": 1,\n  \"active_version\": \"0.29.0\",\n  \"previous_version\": \"0.29.1\"\n}"; got != want {
		t.Fatalf("rolled-back current.json bytes = %q; want %q", got, want)
	}
}

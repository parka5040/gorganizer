package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func testHTTPClient(body string) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(body)),
		}, nil
	})}
}

func TestLOOTGameIDs(t *testing.T) {
	tests := map[string]string{
		"morrowind": "Morrowind", "oblivion": "Oblivion", "skyrim": "Skyrim",
		"skyrimse": "Skyrim Special Edition", "fallout3": "Fallout3", "falloutnv": "FalloutNV",
		"fallout4": "Fallout4", "starfield": "Starfield", "oblivionremastered": "Oblivion Remastered",
	}
	for gameID, want := range tests {
		if got, ok := LOOTGameID(gameID); !ok || got != want {
			t.Fatalf("LOOTGameID(%q) = %q, %v; want %q, true", gameID, got, ok, want)
		}
	}
	if LOOTAutoSortSupported("ttw") {
		t.Fatal("TTW automatic sorting must remain disabled")
	}
}

func TestLOOTInstallerLatestReleaseRequiresDigest(t *testing.T) {
	client := testHTTPClient(`{"tag_name":"0.29.1","assets":[{"id":7,"name":"loot_0.29.1-win64.7z","browser_download_url":"https://example.invalid/loot.7z"}]}`)
	installer := NewLOOTInstaller(t.TempDir(), client)
	if _, err := installer.LatestRelease(context.Background()); err == nil {
		t.Fatal("LatestRelease accepted an asset without a published digest")
	}
}

func TestLOOTInstallerInstallAndRollback(t *testing.T) {
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
	first, err := installer.Install(context.Background(), release("0.29.0"))
	if err != nil {
		t.Fatal(err)
	}
	if !first.Installed || first.ActiveVersion != "0.29.0" {
		t.Fatalf("unexpected first status: %+v", first)
	}
	second, err := installer.Install(context.Background(), release("0.29.1"))
	if err != nil {
		t.Fatal(err)
	}
	if second.ActiveVersion != "0.29.1" || second.PreviousVersion != "0.29.0" {
		t.Fatalf("unexpected upgraded status: %+v", second)
	}
	rolled, err := installer.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if rolled.ActiveVersion != "0.29.0" || rolled.PreviousVersion != "0.29.1" {
		t.Fatalf("unexpected rollback status: %+v", rolled)
	}
	third, err := installer.Install(context.Background(), release("0.29.2"))
	if err != nil {
		t.Fatal(err)
	}
	if third.PreviousVersion != "0.29.0" {
		t.Fatalf("unexpected retained rollback version: %+v", third)
	}
	if _, err := os.Stat(filepath.Join(installer.Root, "loot", "0.29.1")); !os.IsNotExist(err) {
		t.Fatalf("obsolete LOOT version was not pruned: %v", err)
	}
}

func TestLOOTInstallerRejectsDigestMismatch(t *testing.T) {
	installer := NewLOOTInstaller(t.TempDir(), testHTTPClient("tampered"))
	installer.extract = func(context.Context, string, string) error {
		t.Fatal("extract called after digest mismatch")
		return nil
	}
	_, err := installer.Install(context.Background(), LOOTRelease{
		Tag: "0.29.1", Version: "0.29.1", AssetName: "loot_0.29.1-win64.7z",
		URL: "https://example.test/loot.7z", SHA256: string(make([]byte, 64)),
	})
	if err == nil {
		t.Fatal("Install accepted a digest mismatch")
	}
}

func TestLOOTInstallerRejectsUnsafeReleasePaths(t *testing.T) {
	installer := NewLOOTInstaller(t.TempDir(), http.DefaultClient)
	_, err := installer.Install(t.Context(), LOOTRelease{
		Version: "../escape", AssetName: "../loot.7z", URL: "https://example.invalid/loot.7z",
		SHA256: strings.Repeat("0", 64),
	})
	if err == nil {
		t.Fatal("unsafe managed LOOT release path was accepted")
	}
}

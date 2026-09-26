package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/parka/gorganizer/internal/ghrelease"
)

// TestLOOTInstallerDownloadsThenRejectsWrongDigest verifies a well-formed but wrong digest is caught after download and before extraction.
func TestLOOTInstallerDownloadsThenRejectsWrongDigest(t *testing.T) {
	var downloads atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		downloads.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader("tampered")),
		}, nil
	})}
	installer := NewLOOTInstaller(t.TempDir(), client)
	var extracted atomic.Bool
	installer.extract = func(context.Context, string, string) error {
		extracted.Store(true)
		return errors.New("extract called after digest mismatch")
	}
	_, err := installer.Install(t.Context(), LOOTRelease{
		Tag: "0.29.1", Version: "0.29.1", AssetName: "loot_0.29.1-win64.7z",
		URL: "https://example.test/loot.7z", SHA256: strings.Repeat("0", 64),
	})
	if !errors.Is(err, ghrelease.ErrDigestMismatch) {
		t.Fatalf("Install error = %v; want digest mismatch", err)
	}
	if !strings.HasPrefix(err.Error(), "LOOT SHA-256 mismatch: expected ") {
		t.Fatalf("Install error = %q; want LOOT mismatch message", err)
	}
	if got := downloads.Load(); got != 1 {
		t.Fatalf("downloads = %d; want 1", got)
	}
	if extracted.Load() {
		t.Fatal("extract was called after a digest mismatch")
	}
	entries, err := os.ReadDir(filepath.Join(installer.Root, "loot"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed install left entries behind: %v", entries)
	}
}

// TestLOOTInstallerRollbackMissingPrevious verifies rollback reports an unavailable previous directory with LOOT's historical message.
func TestLOOTInstallerRollbackMissingPrevious(t *testing.T) {
	installer := installedLOOT(t, "0.29.0", "0.29.1")
	if err := os.RemoveAll(filepath.Join(installer.Root, "loot", "0.29.0")); err != nil {
		t.Fatal(err)
	}
	_, err := installer.Rollback()
	if !errors.Is(err, ghrelease.ErrPreviousUnavailable) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Rollback error = %v; want previous unavailable wrapping not-exist", err)
	}
	if !strings.HasPrefix(err.Error(), "previous LOOT version is unavailable: ") {
		t.Fatalf("Rollback error = %q; want LOOT's historical message", err)
	}
	status, err := installer.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.ActiveVersion != "0.29.1" || status.PreviousVersion != "0.29.0" {
		t.Fatalf("failed rollback changed status: %+v", status)
	}
}

// TestLOOTInstallerInstallFailsClosedOnCorruptCurrent verifies a corrupt current.json blocks activation and pruning.
func TestLOOTInstallerInstallFailsClosedOnCorruptCurrent(t *testing.T) {
	installer := installedLOOT(t, "0.29.0")
	currentPath := filepath.Join(installer.Root, "loot", "current.json")
	if err := os.WriteFile(currentPath, []byte("{"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := installer.Install(t.Context(), lootTestRelease("0.29.1"))
	if !errors.Is(err, ghrelease.ErrInvalidCurrent) {
		t.Fatalf("Install error = %v; want invalid current", err)
	}
	if !strings.HasPrefix(err.Error(), "LOOT current manifest is invalid or unsupported") {
		t.Fatalf("Install error = %q; want LOOT's historical message", err)
	}
	data, err := os.ReadFile(currentPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{" {
		t.Fatalf("failed install rewrote current.json to %q", data)
	}
	if _, err := os.Stat(filepath.Join(installer.Root, "loot", "0.29.0", "LOOT.exe")); err != nil {
		t.Fatalf("failed install pruned the active version: %v", err)
	}
}

// installedLOOT returns an installer with the given fake versions installed in order.
func installedLOOT(t *testing.T, versions ...string) *LOOTInstaller {
	t.Helper()
	installer := NewLOOTInstaller(t.TempDir(), testHTTPClient("fake archive"))
	installer.extract = func(_ context.Context, _, destination string) error {
		return os.WriteFile(filepath.Join(destination, "LOOT.exe"), []byte("MZ"), 0755)
	}
	for _, version := range versions {
		if _, err := installer.Install(t.Context(), lootTestRelease(version)); err != nil {
			t.Fatal(err)
		}
	}
	return installer
}

// lootTestRelease returns release metadata for the fake archive served by installedLOOT.
func lootTestRelease(version string) LOOTRelease {
	digest := sha256.Sum256([]byte("fake archive"))
	return LOOTRelease{
		Tag: version, Version: version, AssetID: 1, AssetName: "loot_" + version + "-win64.7z",
		URL: "https://example.test/loot.7z", SHA256: hex.EncodeToString(digest[:]),
	}
}

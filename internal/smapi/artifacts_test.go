package smapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sync/atomic"
	"testing"

	"github.com/parka/gorganizer/internal/ghrelease"
)

// newTestArtifactStore serves asset bytes over HTTP and returns a store plus the release describing them and a download counter.
func newTestArtifactStore(t *testing.T, asset []byte) (*ArtifactStore, ghrelease.Release, *atomic.Int32) {
	t.Helper()
	var downloads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		_, _ = w.Write(asset)
	}))
	t.Cleanup(server.Close)
	sum := sha256.Sum256(asset)
	store := &ArtifactStore{
		Store:   &ghrelease.Store{Root: filepath.Join(t.TempDir(), "tools", "smapi"), Label: "SMAPI"},
		Fetcher: ghrelease.NewFetcher(server.Client()),
		Source:  ghrelease.Source{APIURL: server.URL + "/latest", AssetPattern: regexp.MustCompile(`^SMAPI-(\d+\.\d+\.\d+)-installer\.zip$`), UserAgent: "test", Label: "SMAPI"},
	}
	rel := ghrelease.Release{Tag: "4.5.2", Version: "4.5.2", AssetID: 7, AssetName: "SMAPI-4.5.2-installer.zip", URL: server.URL + "/asset", SHA256: hex.EncodeToString(sum[:])}
	return store, rel, &downloads
}

// TestArtifactStoreFetch verifies download, reuse, re-download of a damaged archive, and the manifest format.
func TestArtifactStoreFetch(t *testing.T) {
	asset := []byte("pretend this is the installer zip")
	store, rel, downloads := newTestArtifactStore(t, asset)
	art, err := store.Fetch(context.Background(), rel)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if art.Version != "4.5.2" || art.SHA256 != rel.SHA256 || art.AssetName != rel.AssetName || art.Tag != "4.5.2" {
		t.Fatalf("artifact = %+v", art)
	}
	if err := ghrelease.VerifyFile(art.ZipPath, rel.SHA256); err != nil {
		t.Fatalf("stored archive: %v", err)
	}
	var manifest map[string]any
	data, err := os.ReadFile(filepath.Join(filepath.Dir(art.ZipPath), artifactManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"schema_version", "version", "tag", "asset_id", "asset_name", "url", "sha256", "fetched_at"} {
		if _, ok := manifest[key]; !ok {
			t.Fatalf("manifest lacks %q: %s", key, data)
		}
	}
	if _, err := store.Fetch(context.Background(), rel); err != nil || downloads.Load() != 1 {
		t.Fatalf("second Fetch = %v with %d downloads", err, downloads.Load())
	}
	if err := os.WriteFile(art.ZipPath, []byte("tampered"), 0644); err != nil {
		t.Fatal(err)
	}
	art, err = store.Fetch(context.Background(), rel)
	if err != nil || downloads.Load() != 2 {
		t.Fatalf("Fetch after tampering = %v with %d downloads", err, downloads.Load())
	}
	if err := ghrelease.VerifyFile(art.ZipPath, rel.SHA256); err != nil {
		t.Fatalf("re-downloaded archive: %v", err)
	}
	active, err := store.ActiveVersion()
	if err != nil || active != "" {
		t.Fatalf("ActiveVersion before activation = %q, %v", active, err)
	}
}

// TestArtifactStoreRejectsBadInput verifies inconsistent releases and a wrong digest never produce an artifact.
func TestArtifactStoreRejectsBadInput(t *testing.T) {
	store, rel, _ := newTestArtifactStore(t, []byte("asset"))
	bad := rel
	bad.AssetName = "SMAPI-9.9.9-installer.zip"
	if _, err := store.Fetch(context.Background(), bad); !errors.Is(err, ghrelease.ErrInvalidRelease) {
		t.Fatalf("Fetch with inconsistent release = %v", err)
	}
	wrong := rel
	wrong.SHA256 = hex.EncodeToString(make([]byte, 32))
	if _, err := store.Fetch(context.Background(), wrong); !errors.Is(err, ghrelease.ErrDigestMismatch) {
		t.Fatalf("Fetch with wrong digest = %v", err)
	}
	if _, err := store.Artifact("4.5.2"); err == nil {
		t.Fatal("a failed fetch left an artifact behind")
	}
	if _, err := store.Artifact("../escape"); err == nil {
		t.Fatal("Artifact accepted an unsafe version")
	}
}

// TestArtifactStoreActivation verifies activation requires a stored artifact and moves the pointers.
func TestArtifactStoreActivation(t *testing.T) {
	store, rel, _ := newTestArtifactStore(t, []byte("asset"))
	if _, err := store.Activate("4.5.2"); err == nil {
		t.Fatal("Activate accepted a version that was never fetched")
	}
	if _, err := store.Fetch(context.Background(), rel); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Activate("4.5.2"); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	active, err := store.ActiveVersion()
	if err != nil || active != "4.5.2" {
		t.Fatalf("ActiveVersion = %q, %v", active, err)
	}
	previous, err := store.PreviousVersion()
	if err != nil || previous != "" {
		t.Fatalf("PreviousVersion = %q, %v", previous, err)
	}
	if _, err := store.Rollback(); !errors.Is(err, ghrelease.ErrNoPrevious) {
		t.Fatalf("Rollback without previous = %v", err)
	}
}

// TestArtifactStoreFetchRequiresManifestDigest verifies a retained artifact is reused only when its manifest names the release digest.
func TestArtifactStoreFetchRequiresManifestDigest(t *testing.T) {
	store, rel, downloads := newTestArtifactStore(t, []byte("installer bytes"))
	art, err := store.Fetch(context.Background(), rel)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(filepath.Dir(art.ZipPath), artifactManifestName)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["sha256"] = hex.EncodeToString(make([]byte, 32))
	data, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0644); err != nil {
		t.Fatal(err)
	}
	art, err = store.Fetch(context.Background(), rel)
	if err != nil || downloads.Load() != 2 || art.SHA256 != rel.SHA256 {
		t.Fatalf("Fetch with a stale manifest digest = %+v, %v after %d downloads", art, err, downloads.Load())
	}
}

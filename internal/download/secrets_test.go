package download

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/config"
)

// TestLedgerIsPrivateIncludingExistingFile checks new and old ledgers without chmodding symlink targets.
func TestLedgerIsPrivateIncludingExistingFile(t *testing.T) {
	isolatedDownloadRoot(t)
	const gameID = "skyrimse"
	path := filepath.Join(config.DownloadsDir(gameID), ledgerFilename)
	uri := "nxm://skyrimspecialedition/mods/7/files/8?key=private-key&expires=123"
	if err := SaveLedger(gameID, []LedgerEntry{{ID: "test", NXMURI: uri}}); err != nil {
		t.Fatal(err)
	}
	checkMode := func(path string, want os.FileMode) {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("mode of %q = %04o, want %04o", path, got, want)
		}
	}
	checkMode(path, 0o600)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := LoadLedger(gameID)
	if err != nil || len(entries) != 1 || entries[0].NXMURI != uri {
		t.Fatalf("LoadLedger() = %+v, %v; want original full URI", entries, err)
	}
	checkMode(path, 0o600)

	target := filepath.Join(t.TempDir(), "symlink-target.yaml")
	if err := os.WriteFile(target, []byte("inflight:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLedger(gameID); err != nil {
		t.Fatal(err)
	}
	checkMode(target, 0o644)
}

// TestRedactURL checks credential removal from NXM links and CDN URLs.
func TestRedactURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"NXM", "nxm://game/mods/7/files/8?key=secret&expires=123&user_id=42#fragment", "nxm://game/mods/7/files/8"},
		{"NXM extra path and user-info", "nxm://user:pass@game/mods/7/files/8/extra?key=secret", "nxm://game/mods/7/files/8"},
		{"invalid NXM ID", "nxm://game/mods/key=secret/files/8", "<redacted>"},
		{"CDN", "https://user:pass@cdn.example/archive.zip?md5=secret&expires=123#fragment", "https://cdn.example/archive.zip"},
		{"garbage", "://not a URL?key=secret", "<redacted>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redactURL(tt.raw); got != tt.want {
				t.Errorf("redactURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// TestDownloadDiagnosticsContainNoCredentials checks pipeline logs, errors and expired-link errors.
func TestDownloadDiagnosticsContainNoCredentials(t *testing.T) {
	isolatedDownloadRoot(t)
	var logs bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })

	const nxmURI = "nxm://skyrimspecialedition/mods/7/files/8?key=nxm-secret&expires=1&user_id=uid-secret"
	const cdnURL = "https://user-secret:pass-secret@cdn.example/archive.zip?md5=cdn-secret&expires=123#frag-secret"
	const gameID = "skyrimse"
	var snapshot DownloadSnapshot
	requestFailure := errors.New("transport unavailable")
	calls := 0
	m := &Manager{
		nexus: destinationResolver{filename: "archive.zip", url: cdnURL},
		httpClient: &http.Client{Transport: destinationTransport(func(req *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				if got := req.Header.Get("Range"); got != "bytes=7-" {
					t.Errorf("Range = %q, want bytes=7-", got)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("archive data")), Header: make(http.Header), Request: req}, nil
			}
			return nil, requestFailure
		})},
		hooks: ManagerHooks{OnDownloadProgress: func(s DownloadSnapshot) { snapshot = s }},
	}
	partPath := PartPath(filepath.Join(config.DownloadsDir(gameID), "7_Example", "archive.zip"))
	if err := os.MkdirAll(filepath.Dir(partPath), 0o700); err != nil {
		t.Fatal(err)
	}
	seedPart := func() {
		t.Helper()
		if err := os.WriteFile(partPath, []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	seedPart()
	m.runPipeline(context.Background(), &Download{ID: "first", GameID: gameID, NXMURI: nxmURI})
	if snapshot.Status != StatusDownloaded {
		t.Fatalf("first pipeline status = %v, error = %q", snapshot.Status, snapshot.Error)
	}
	seedPart()
	m.runPipeline(context.Background(), &Download{ID: "second", GameID: gameID, NXMURI: nxmURI})
	if snapshot.Status != StatusFailed {
		t.Fatalf("second pipeline status = %v, error = %q", snapshot.Status, snapshot.Error)
	}
	if !strings.Contains(logs.String(), "server ignored Range header") || !strings.Contains(logs.String(), "https://cdn.example/archive.zip") {
		t.Fatalf("expected redacted CDN URL in Range warning: %s", logs.String())
	}
	if !strings.Contains(snapshot.Error, "https://cdn.example/archive.zip") {
		t.Errorf("transport failure lost the redacted URL: %s", snapshot.Error)
	}
	if calls != 2 {
		t.Errorf("HTTP requests = %d, want 2", calls)
	}

	transportErr := m.streamToFile(context.Background(), cdnURL, partPath, 7, &Download{GameID: gameID, ArchiveRel: "7_Example/archive.zip"})
	var urlErr *url.Error
	if !errors.Is(transportErr, ErrDownloadFailed) || !errors.Is(transportErr, requestFailure) || !errors.As(transportErr, &urlErr) || urlErr.URL != "https://cdn.example/archive.zip" {
		t.Errorf("streamToFile error = %v, want typed failure with redacted URL and original cause", transportErr)
	}

	nexus := NewNexusClient("api-secret")
	nexus.baseURL = "https://api.example"
	nexus.httpClient = &http.Client{Transport: destinationTransport(func(*http.Request) (*http.Response, error) { return nil, requestFailure })}
	_, apiErr := nexus.ResolveDownloadURL(&NXMLink{GameSlug: "skyrimspecialedition", ModID: 7, FileID: 8, Key: "nxm-secret", Expires: 1})
	if !errors.Is(apiErr, requestFailure) || !errors.As(apiErr, &urlErr) || urlErr.URL != "https://api.example/v1/games/skyrimspecialedition/mods/7/files/8/download_link" {
		t.Errorf("Nexus transport error = %v, want typed failure with redacted URL and original cause", apiErr)
	}

	_, _, expired := m.StartDownloadForGame(nxmURI, gameID)
	var nxmExpired *NXMExpiredError
	if !errors.As(expired, &nxmExpired) || nxmExpired.URI != "nxm://skyrimspecialedition/mods/7/files/8" {
		t.Errorf("expired link = %v, want query-free URI", expired)
	}
	if err := SaveLedger(gameID, []LedgerEntry{{ID: "expired", GameID: gameID, NXMURI: nxmURI, ArchiveRelPath: "7_Example/archive.zip", Status: LedgerQueued}}); err != nil {
		t.Fatal(err)
	}
	m.RehydrateLedger([]string{gameID})
	if !strings.Contains(logs.String(), "nxm://skyrimspecialedition/mods/7/files/8") {
		t.Errorf("expected redacted NXM URI in ledger warning: %s", logs.String())
	}
	_, retryErr := m.RetryDownload("expired", []string{gameID})
	if !errors.As(retryErr, &nxmExpired) || nxmExpired.URI != "nxm://skyrimspecialedition/mods/7/files/8" {
		t.Errorf("retry expired link = %v, want query-free URI", retryErr)
	}
	for _, diagnostic := range []string{logs.String(), snapshot.Error, transportErr.Error(), apiErr.Error(), expired.Error(), retryErr.Error()} {
		for _, secret := range []string{"key=", "expires=", "md5=", "nxm-secret", "uid-secret", "cdn-secret", "user-secret", "pass-secret", "api-secret", "frag-secret"} {
			if strings.Contains(diagnostic, secret) {
				t.Errorf("diagnostic contains %q: %s", secret, diagnostic)
			}
		}
	}
}

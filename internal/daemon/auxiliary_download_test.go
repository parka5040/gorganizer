package daemon

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type auxiliaryRoundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip returns the fake auxiliary download response.
func (f auxiliaryRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// TestAuxiliaryDownloadsIgnoreRemoteFileNames verifies remote names cannot escape the private folder or replace existing files.
func TestAuxiliaryDownloadsIgnoreRemoteFileNames(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	outside := filepath.Join(root, "escape.7z")
	if err := os.WriteFile(outside, []byte("marker"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: auxiliaryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := "archive"
		if req.URL.Host == "api.github.com" {
			body = `{"tag_name":"v1","assets":[{"name":"../../escape.7z","browser_download_url":"https://cdn.example/asset"}]}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	for _, tc := range []struct {
		name string
		run  func(string) (string, error)
	}{
		{name: "Nexus patcher", run: func(dir string) (string, error) {
			path := fnv4gbArchivePath(dir)
			return path, streamToWithClient(client, "https://cdn.example/patcher", path)
		}},
		{name: "GitHub extender", run: func(dir string) (string, error) {
			path, _, err := fetchLatestGitHubReleaseWithClient(client, "owner/repo", ".7z", dir)
			return path, err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(root, "private", strings.ReplaceAll(tc.name, " ", "-"))
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path, err := tc.run(dir)
			if err != nil || filepath.Dir(path) != dir {
				t.Fatalf("download = %q, %v; want destination in %q", path, err, dir)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal(err)
			}
			if got, err := os.ReadFile(outside); err != nil || string(got) != "marker" {
				t.Fatalf("outside marker changed: %q, %v", got, err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}
			if _, err := tc.run(dir); err == nil {
				t.Fatal("download replaced a symlink")
			}
			if got, err := os.ReadFile(outside); err != nil || string(got) != "marker" {
				t.Fatalf("symlink target changed: %q, %v", got, err)
			}
		})
	}
}

// TestStreamToRedactsErrors verifies signed URLs and echoed credentials never appear in errors.
func TestStreamToRedactsErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const signedURL = "https://cdn.example/asset?key=SIGNED&expires=123&user_id=77"
	for _, tc := range []struct {
		name      string
		transport auxiliaryRoundTripFunc
	}{
		{name: "transport", transport: func(req *http.Request) (*http.Response, error) {
			return nil, &url.Error{Op: "Get", URL: signedURL, Err: errors.New("connection refused")}
		}},
		{name: "403 body", transport: func(req *http.Request) (*http.Response, error) {
			body := "key=SECRET expires=123 user_id=77 " + signedURL + strings.Repeat("x", 8192)
			return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: tc.transport}
			err := streamToWithClient(client, signedURL, filepath.Join(t.TempDir(), "archive"))
			if err == nil {
				t.Fatal("expected download error")
			}
			for _, secret := range []string{"SIGNED", "SECRET", "expires=123", "user_id=77", strings.Repeat("x", 8192)} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("download error exposed %q: %s", secret, err)
				}
			}
		})
	}
}

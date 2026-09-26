package ghrelease

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const smapiDigest = "dd01ddca7b566bfe0d3b3d2d03833496abc56c53da976241f2ab443f5484acc4"

var (
	smapiPattern      = regexp.MustCompile(`^SMAPI-(\d+\.\d+\.\d+)-installer\.zip$`)
	permissivePattern = regexp.MustCompile(`^SMAPI-(.+)-installer\.zip$`)
	unsafeVersions    = []string{"../x", "a/b", `a\b`, ".", "..", ".hidden", ".stage-x", "current.json", "1.0\x00", "1.0\x7f"}
	validReleaseJSON  = `{"tag_name":"4.5.2","assets":[{"id":2,"name":"SMAPI-4.5.2-installer.zip","browser_download_url":"https://example.test/smapi.zip","digest":"sha256:` + smapiDigest + `"}]}`
)

type roundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip calls f with the request.
func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// TestLatestSelectsSMAPIAssetAndNormalizesDigest verifies asset selection, request headers, and digest normalization.
func TestLatestSelectsSMAPIAssetAndNormalizesDigest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Header.Get("Accept"), "application/vnd.github+json"; got != want {
			t.Errorf("Accept = %q; want %q", got, want)
		}
		if got, want := r.Header.Get("X-GitHub-Api-Version"), "2022-11-28"; got != want {
			t.Errorf("X-GitHub-Api-Version = %q; want %q", got, want)
		}
		if got, want := r.Header.Get("User-Agent"), "gorganizer-test"; got != want {
			t.Errorf("User-Agent = %q; want %q", got, want)
		}
		_, _ = io.WriteString(w, `{"tag_name":"4.5.2","assets":[{"id":1,"name":"SMAPI-4.5.2-installer-double-zipped.zip","browser_download_url":"https://example.test/double.zip","digest":"sha256:00"},{"id":2,"name":"SMAPI-4.5.2-installer.zip","browser_download_url":"https://example.test/smapi.zip","digest":"sha256:DD01DDCA7B566BFE0D3B3D2D03833496ABC56C53DA976241F2AB443F5484ACC4"}]}`)
	}))
	defer server.Close()

	release, err := NewFetcher(server.Client()).Latest(t.Context(), Source{
		APIURL: server.URL, AssetPattern: smapiPattern, UserAgent: "gorganizer-test", Label: "SMAPI",
	})
	if err != nil {
		t.Fatal(err)
	}
	if release.Tag != "4.5.2" || release.Version != "4.5.2" || release.AssetID != 2 || release.AssetName != "SMAPI-4.5.2-installer.zip" || release.URL != "https://example.test/smapi.zip" || release.SHA256 != smapiDigest {
		t.Fatalf("release = %+v", release)
	}
}

// TestLatestAcceptsUnprefixedUppercaseDigest verifies a bare uppercase digest is normalized.
func TestLatestAcceptsUnprefixedUppercaseDigest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"assets":[{"name":"SMAPI-4.5.2-installer.zip","browser_download_url":"https://example.test/smapi.zip","digest":"DD01DDCA7B566BFE0D3B3D2D03833496ABC56C53DA976241F2AB443F5484ACC4"}]}`)
	}))
	defer server.Close()
	release, err := NewFetcher(server.Client()).Latest(t.Context(), Source{
		APIURL: server.URL, AssetPattern: smapiPattern, Label: "SMAPI",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := release.SHA256, smapiDigest; got != want {
		t.Fatalf("SHA256 = %q; want %q", got, want)
	}
}

// TestLatestRejectsUnusableReleases verifies every rejection path of Latest against otherwise acceptable metadata.
func TestLatestRejectsUnusableReleases(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		status   int
		pattern  *regexp.Regexp
		wantIs   error
		wantText string
	}{
		{
			name:     "draft",
			body:     `{"draft":true,"assets":[{"name":"SMAPI-4.5.2-installer.zip","browser_download_url":"https://example.test/smapi.zip","digest":"sha256:` + smapiDigest + `"}]}`,
			status:   http.StatusOK,
			wantIs:   ErrNotStable,
			wantText: "latest SMAPI release is not stable",
		},
		{
			name:     "prerelease",
			body:     `{"prerelease":true,"assets":[{"name":"SMAPI-4.5.2-installer.zip","browser_download_url":"https://example.test/smapi.zip","digest":"sha256:` + smapiDigest + `"}]}`,
			status:   http.StatusOK,
			wantIs:   ErrNotStable,
			wantText: "latest SMAPI release is not stable",
		},
		{
			name:     "missing digest",
			body:     `{"assets":[{"name":"SMAPI-4.5.2-installer.zip","browser_download_url":"https://example.test/smapi.zip"}]}`,
			status:   http.StatusOK,
			wantIs:   ErrNoDigest,
			wantText: "has no published SHA-256 digest",
		},
		{
			name:     "invalid digest",
			body:     `{"assets":[{"name":"SMAPI-4.5.2-installer.zip","browser_download_url":"https://example.test/smapi.zip","digest":"sha256:` + strings.Repeat("z", 64) + `"}]}`,
			status:   http.StatusOK,
			wantIs:   ErrInvalidRelease,
			wantText: "has an invalid SHA-256 digest",
		},
		{
			name:     "no matching asset",
			body:     `{"assets":[{"name":"SMAPI-4.5.2-installer-double-zipped.zip","browser_download_url":"https://example.test/smapi.zip","digest":"sha256:` + smapiDigest + `"}]}`,
			status:   http.StatusOK,
			wantIs:   ErrNoMatchingAsset,
			wantText: "latest SMAPI release has no matching asset",
		},
		{
			name:     "missing download URL",
			body:     `{"assets":[{"name":"SMAPI-4.5.2-installer.zip","digest":"sha256:` + smapiDigest + `"}]}`,
			status:   http.StatusOK,
			wantIs:   ErrInvalidRelease,
			wantText: "incomplete SMAPI release metadata",
		},
		{
			name:     "traversal version",
			body:     `{"assets":[{"name":"SMAPI-../x-installer.zip","browser_download_url":"https://example.test/smapi.zip","digest":"sha256:` + smapiDigest + `"}]}`,
			status:   http.StatusOK,
			pattern:  permissivePattern,
			wantIs:   ErrInvalidRelease,
			wantText: "invalid SMAPI release version",
		},
		{
			name:     "non-200",
			body:     validReleaseJSON,
			status:   http.StatusInternalServerError,
			wantText: "fetching SMAPI release metadata: HTTP 500",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			pattern := test.pattern
			if pattern == nil {
				pattern = smapiPattern
			}
			_, err := NewFetcher(server.Client()).Latest(t.Context(), Source{
				APIURL: server.URL, AssetPattern: pattern, Label: "SMAPI",
			})
			if err == nil {
				t.Fatal("Latest succeeded")
			}
			if test.wantIs != nil && !errors.Is(err, test.wantIs) {
				t.Fatalf("error %q does not match %v", err, test.wantIs)
			}
			if !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("error %q does not contain %q", err, test.wantText)
			}
		})
	}
}

// TestLatestMetadataSizeCap verifies the metadata Content-Length check uses the metadata cap, not the asset cap.
func TestLatestMetadataSizeCap(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(validReleaseJSON)))
		_, _ = io.WriteString(w, validReleaseJSON)
	}))
	defer server.Close()
	size := int64(len(validReleaseJSON))
	_, err := NewFetcher(server.Client()).Latest(t.Context(), Source{
		APIURL: server.URL, AssetPattern: smapiPattern, Label: "SMAPI", MaxMetadataBytes: size - 1,
	})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Latest error = %v; want too large", err)
	}
	if !strings.Contains(err.Error(), "SMAPI release metadata is unexpectedly large") {
		t.Fatalf("Latest error = %q; want unexpectedly large", err)
	}
	release, err := NewFetcher(server.Client()).Latest(t.Context(), Source{
		APIURL: server.URL, AssetPattern: smapiPattern, Label: "SMAPI", MaxMetadataBytes: size, MaxAssetBytes: 1,
	})
	if err != nil {
		t.Fatalf("Latest applied the asset cap to metadata: %v", err)
	}
	if release.Version != "4.5.2" {
		t.Fatalf("release = %+v", release)
	}
}

// TestDownloadRemovesDigestMismatchAndDoesNotOverwrite verifies mismatch cleanup and O_EXCL creation.
func TestDownloadRemovesDigestMismatchAndDoesNotOverwrite(t *testing.T) {
	payload := []byte("payload")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	destination := filepath.Join(t.TempDir(), "archive.zip")
	source := Source{UserAgent: "gorganizer-test", Label: "SMAPI"}
	release := Release{URL: server.URL, SHA256: strings.Repeat("0", 64)}
	err := NewFetcher(server.Client()).Download(t.Context(), source, release, destination)
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("Download error = %v; want digest mismatch", err)
	}
	if !strings.HasPrefix(err.Error(), "SMAPI SHA-256 mismatch: expected ") {
		t.Fatalf("Download error = %q; want labeled mismatch", err)
	}
	assertMissing(t, destination)
	if err := os.WriteFile(destination, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	release.SHA256 = digestOf(payload)
	if err := NewFetcher(server.Client()).Download(t.Context(), source, release, destination); err == nil {
		t.Fatal("Download overwrote an existing destination")
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "existing"; got != want {
		t.Fatalf("destination = %q; want %q", got, want)
	}
}

// TestDownloadEnforcesSizeCap verifies declared, streamed, and misdeclared oversized bodies are rejected and removed.
func TestDownloadEnforcesSizeCap(t *testing.T) {
	payload := bytes.Repeat([]byte("abcdefgh"), 512)
	declared := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer declared.Close()
	streamed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload[:len(payload)/2])
		w.(http.Flusher).Flush()
		_, _ = w.Write(payload[len(payload)/2:])
	}))
	defer streamed.Close()
	lying := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), ContentLength: 1,
			Body: io.NopCloser(bytes.NewReader(payload)),
		}, nil
	})}
	tests := []struct {
		name     string
		client   *http.Client
		url      string
		limit    int64
		wantText string
	}{
		{name: "declared length over cap", client: declared.Client(), url: declared.URL, limit: int64(len(payload)) - 1, wantText: "SMAPI archive is unexpectedly large"},
		{name: "streamed body over cap", client: streamed.Client(), url: streamed.URL, limit: int64(len(payload)) - 1, wantText: "SMAPI archive exceeds"},
		{name: "misdeclared length over cap", client: lying, url: "https://example.test/smapi.zip", limit: int64(len(payload)) - 1, wantText: "SMAPI archive exceeds"},
		{name: "body at cap", client: declared.Client(), url: declared.URL, limit: int64(len(payload))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "archive.zip")
			source := Source{UserAgent: "gorganizer-test", Label: "SMAPI", MaxAssetBytes: test.limit}
			release := Release{URL: test.url, SHA256: digestOf(payload)}
			err := NewFetcher(test.client).Download(t.Context(), source, release, destination)
			if test.wantText == "" {
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(destination)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(data, payload) {
					t.Fatal("downloaded bytes differ from payload")
				}
				return
			}
			if !errors.Is(err, ErrTooLarge) || errors.Is(err, ErrDigestMismatch) {
				t.Fatalf("Download error = %v; want too large without digest mismatch", err)
			}
			if !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("Download error = %q; want %q", err, test.wantText)
			}
			assertMissing(t, destination)
		})
	}
}

// TestDownloadRejectsNon200 verifies a non-200 response is rejected even when its body is the expected payload.
func TestDownloadRejectsNon200(t *testing.T) {
	payload := []byte("payload")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	destination := filepath.Join(t.TempDir(), "archive.zip")
	err := NewFetcher(server.Client()).Download(t.Context(), Source{Label: "SMAPI"}, Release{URL: server.URL, SHA256: digestOf(payload)}, destination)
	if err == nil || !strings.Contains(err.Error(), "downloading SMAPI: HTTP 500") {
		t.Fatalf("Download error = %v; want HTTP 500", err)
	}
	assertMissing(t, destination)
}

// TestValidateRelease verifies completeness, consistency, digest, and version-name checks.
func TestValidateRelease(t *testing.T) {
	valid := Release{
		Version: "4.5.2", AssetName: "SMAPI-4.5.2-installer.zip", URL: "https://example.test/smapi.zip",
		SHA256: strings.Repeat("a", 64),
	}
	for _, pattern := range []*regexp.Regexp{smapiPattern, permissivePattern} {
		if err := ValidateRelease(Source{AssetPattern: pattern, Label: "SMAPI"}, valid); err != nil {
			t.Fatalf("ValidateRelease(%s) = %v", pattern, err)
		}
	}
	for _, test := range []struct {
		name    string
		release Release
	}{
		{name: "mismatched version", release: Release{Version: "4.5.1", AssetName: valid.AssetName, URL: valid.URL, SHA256: valid.SHA256}},
		{name: "short digest", release: Release{Version: valid.Version, AssetName: valid.AssetName, URL: valid.URL, SHA256: "abc"}},
		{name: "non-hex digest", release: Release{Version: valid.Version, AssetName: valid.AssetName, URL: valid.URL, SHA256: strings.Repeat("z", 64)}},
		{name: "missing URL", release: Release{Version: valid.Version, AssetName: valid.AssetName, SHA256: valid.SHA256}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateRelease(Source{AssetPattern: smapiPattern, Label: "SMAPI"}, test.release); !errors.Is(err, ErrInvalidRelease) {
				t.Fatalf("ValidateRelease error = %v; want invalid release", err)
			}
		})
	}
	for _, version := range unsafeVersions {
		t.Run("unsafe "+strconv.Quote(version), func(t *testing.T) {
			release := Release{Version: version, AssetName: "SMAPI-" + version + "-installer.zip", URL: valid.URL, SHA256: valid.SHA256}
			if match := permissivePattern.FindStringSubmatch(release.AssetName); len(match) != 2 || match[1] != version {
				t.Fatalf("permissive pattern does not capture %q", version)
			}
			err := ValidateRelease(Source{AssetPattern: permissivePattern, Label: "SMAPI"}, release)
			if !errors.Is(err, ErrInvalidRelease) || !strings.Contains(err.Error(), "invalid SMAPI release version") {
				t.Fatalf("ValidateRelease error = %v; want invalid version", err)
			}
		})
	}
}

// TestOpenVerified verifies the returned handle and every rejection path of OpenVerified and VerifyFile.
func TestOpenVerified(t *testing.T) {
	dir := t.TempDir()
	payload := []byte("verified payload")
	path := filepath.Join(dir, "archive.zip")
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	digest := digestOf(payload)
	file, size, err := OpenVerified(path, strings.ToUpper(digest))
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(file)
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	if size != int64(len(payload)) || !bytes.Equal(data, payload) {
		t.Fatalf("OpenVerified returned size %d and %q", size, data)
	}
	if err := VerifyFile(path, digest); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFile(path, strings.Repeat("0", 64)); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("VerifyFile error = %v; want digest mismatch", err)
	}
	if _, _, err := OpenVerified(path, strings.Repeat("0", 64)); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("OpenVerified error = %v; want digest mismatch", err)
	}
	for _, bad := range []string{"abc", strings.Repeat("z", 64)} {
		if _, _, err := OpenVerified(path, bad); !errors.Is(err, ErrInvalidRelease) {
			t.Fatalf("OpenVerified(%q) error = %v; want invalid release", bad, err)
		}
	}
	link := filepath.Join(dir, "link.zip")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenVerified(link, digest); !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("OpenVerified(symlink) error = %v; want ELOOP", err)
	}
	fifo := filepath.Join(dir, "fifo.zip")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := OpenVerified(fifo, digest)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "is not a regular file") {
			t.Fatalf("OpenVerified(fifo) error = %v; want not a regular file", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OpenVerified blocked on a FIFO")
	}
	if _, _, err := OpenVerified(dir, digest); err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Fatalf("OpenVerified(directory) error = %v; want not a regular file", err)
	}
}

// digestOf returns the lowercase hexadecimal SHA-256 digest of data.
func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// assertMissing fails the test unless path does not exist.
func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s exists or is unreadable: %v", path, err)
	}
}

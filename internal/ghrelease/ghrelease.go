package ghrelease

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/sys/unix"

	"github.com/parka/gorganizer/internal/httpx"
)

const (
	defaultMaxMetadataBytes int64 = 4 << 20
	defaultMaxAssetBytes    int64 = 512 << 20
)

type Source struct {
	APIURL           string
	AssetPattern     *regexp.Regexp
	UserAgent        string
	Label            string
	MaxMetadataBytes int64
	MaxAssetBytes    int64
}

type Release struct {
	Tag       string
	Version   string
	AssetID   int64
	AssetName string
	URL       string
	SHA256    string
}

type Fetcher struct {
	HTTP *http.Client
}

type githubRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		ID                 int64  `json:"id"`
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Digest             string `json:"digest"`
	} `json:"assets"`
}

// NewFetcher returns a Fetcher that uses client, or httpx.DownloadClient when client is nil.
func NewFetcher(client *http.Client) *Fetcher {
	if client == nil {
		client = httpx.DownloadClient()
	}
	return &Fetcher{HTTP: client}
}

// Latest resolves the newest stable release asset matching src and returns its validated metadata.
func (f *Fetcher) Latest(ctx context.Context, src Source) (Release, error) {
	if err := validateSource(src); err != nil {
		return Release{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.APIURL, nil)
	if err != nil {
		return Release{}, fmt.Errorf("creating %s release metadata request: %w", label(src), err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", src.UserAgent)
	resp, err := f.client().Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("fetching %s release metadata: %w", label(src), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("fetching %s release metadata: HTTP %s", label(src), resp.Status)
	}
	if resp.ContentLength > maxMetadataBytes(src) {
		return Release{}, tagged(ErrTooLarge, "%s release metadata is unexpectedly large: %d bytes", label(src), resp.ContentLength)
	}
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxMetadataBytes(src))).Decode(&release); err != nil {
		return Release{}, fmt.Errorf("decoding %s release metadata: %w", label(src), err)
	}
	if release.Draft || release.Prerelease {
		return Release{}, tagged(ErrNotStable, "latest %s release is not stable", label(src))
	}
	for _, asset := range release.Assets {
		match := src.AssetPattern.FindStringSubmatch(asset.Name)
		if len(match) != 2 {
			continue
		}
		digest := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(asset.Digest)), "sha256:")
		if len(digest) != sha256.Size*2 {
			return Release{}, tagged(ErrNoDigest, "%s asset %q has no published SHA-256 digest", label(src), asset.Name)
		}
		if _, err := hex.DecodeString(digest); err != nil {
			return Release{}, tagged(ErrInvalidRelease, "%s asset %q has an invalid SHA-256 digest: %w", label(src), asset.Name, err)
		}
		result := Release{
			Tag: release.TagName, Version: match[1], AssetID: asset.ID, AssetName: asset.Name,
			URL: asset.BrowserDownloadURL, SHA256: digest,
		}
		if err := ValidateRelease(src, result); err != nil {
			return Release{}, err
		}
		return result, nil
	}
	return Release{}, tagged(ErrNoMatchingAsset, "latest %s release has no matching asset", label(src))
}

// Download streams rel to a new file at destination, enforcing the size cap and the published SHA-256 digest.
func (f *Fetcher) Download(ctx context.Context, src Source, rel Release, destination string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rel.URL, nil)
	if err != nil {
		return fmt.Errorf("creating %s download request: %w", label(src), err)
	}
	req.Header.Set("User-Agent", src.UserAgent)
	resp, err := f.client().Do(req)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", label(src), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: HTTP %s", label(src), resp.Status)
	}
	if resp.ContentLength > maxAssetBytes(src) {
		return tagged(ErrTooLarge, "%s archive is unexpectedly large: %d bytes", label(src), resp.ContentLength)
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if err := receive(file, resp.Body, src, rel); err != nil {
		if removeErr := os.Remove(destination); removeErr != nil {
			return errors.Join(err, fmt.Errorf("removing %s after failed download: %w", destination, removeErr))
		}
		return err
	}
	return nil
}

// ValidateRelease checks that rel is complete, names a safe version, and is consistent with src.
func ValidateRelease(src Source, rel Release) error {
	if err := validateSource(src); err != nil {
		return err
	}
	if rel.Version == "" || rel.URL == "" || len(rel.SHA256) != sha256.Size*2 {
		return fmt.Errorf("incomplete %s release metadata: %w", label(src), ErrInvalidRelease)
	}
	if err := validateVersionName(rel.Version); err != nil {
		return tagged(ErrInvalidRelease, "invalid %s release version: %w", label(src), err)
	}
	match := src.AssetPattern.FindStringSubmatch(rel.AssetName)
	if len(match) != 2 || match[1] != rel.Version {
		return fmt.Errorf("%s release asset name and version are inconsistent: %w", label(src), ErrInvalidRelease)
	}
	if _, err := hex.DecodeString(rel.SHA256); err != nil {
		return tagged(ErrInvalidRelease, "invalid %s release SHA-256 digest: %w", label(src), err)
	}
	return nil
}

// OpenVerified opens the regular file at path without following symlinks, verifies its SHA-256 digest, and returns it rewound with its size.
func OpenVerified(path, sha256hex string) (*os.File, int64, error) {
	if err := validateDigest(sha256hex); err != nil {
		return nil, 0, err
	}
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, 0, fmt.Errorf("opening %s for SHA-256 verification: %w", path, err)
	}
	size, err := verifyOpenFile(file, path, sha256hex)
	if err != nil {
		if closeErr := file.Close(); closeErr != nil {
			return nil, 0, errors.Join(err, fmt.Errorf("closing %s after failed verification: %w", path, closeErr))
		}
		return nil, 0, err
	}
	return file, size, nil
}

// VerifyFile checks that the regular file at path matches the SHA-256 digest sha256hex.
func VerifyFile(path, sha256hex string) error {
	file, _, err := OpenVerified(path, sha256hex)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing %s after SHA-256 verification: %w", path, err)
	}
	return nil
}

// receive copies body into file under the size cap, syncs and closes it, and verifies the digest.
func receive(file *os.File, body io.Reader, src Source, rel Release) error {
	limit := maxAssetBytes(src)
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(body, limit+1))
	if copyErr != nil {
		copyErr = fmt.Errorf("downloading %s: %w", label(src), copyErr)
	} else if written > limit {
		copyErr = tagged(ErrTooLarge, "%s archive exceeds %d bytes", label(src), limit)
	} else if err := file.Sync(); err != nil {
		copyErr = fmt.Errorf("syncing downloaded %s archive: %w", label(src), err)
	}
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return fmt.Errorf("closing downloaded %s archive: %w", label(src), closeErr)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, rel.SHA256) {
		return digestMismatchError{label: label(src), expected: rel.SHA256, actual: actual}
	}
	return nil
}

// verifyOpenFile requires file to be a regular file whose content matches sha256hex and rewinds it.
func verifyOpenFile(file *os.File, path, sha256hex string) (int64, error) {
	info, err := file.Stat()
	if err != nil {
		return 0, fmt.Errorf("inspecting %s for SHA-256 verification: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("%s is not a regular file", path)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return 0, fmt.Errorf("hashing %s: %w", path, err)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, sha256hex) {
		return 0, digestMismatchError{label: path, expected: sha256hex, actual: actual}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, fmt.Errorf("rewinding %s after SHA-256 verification: %w", path, err)
	}
	return info.Size(), nil
}

// validateDigest requires sha256hex to be a 64-character hexadecimal SHA-256 digest.
func validateDigest(sha256hex string) error {
	if len(sha256hex) != sha256.Size*2 {
		return fmt.Errorf("invalid SHA-256 digest: %w", ErrInvalidRelease)
	}
	if _, err := hex.DecodeString(sha256hex); err != nil {
		return tagged(ErrInvalidRelease, "invalid SHA-256 digest: %w", err)
	}
	return nil
}

// validateVersionName rejects version names that are empty, hidden, reserved, contain path separators, or contain control characters.
func validateVersionName(version string) error {
	switch {
	case version == "":
		return errors.New("version name is empty")
	case version == "." || version == ".." || strings.HasPrefix(version, "."):
		return fmt.Errorf("version name %q starts with a dot", version)
	case version == currentFileName:
		return fmt.Errorf("version name %q is reserved", version)
	case strings.ContainsAny(version, `/\`):
		return fmt.Errorf("version name %q contains a path separator", version)
	case strings.ContainsFunc(version, unicode.IsControl):
		return fmt.Errorf("version name %q contains a control character", version)
	}
	return nil
}

// client returns the configured HTTP client or the shared download client.
func (f *Fetcher) client() *http.Client {
	if f == nil || f.HTTP == nil {
		return httpx.DownloadClient()
	}
	return f.HTTP
}

// validateSource requires an asset pattern with exactly one capture group.
func validateSource(src Source) error {
	if src.AssetPattern == nil || src.AssetPattern.NumSubexp() != 1 {
		return fmt.Errorf("%s asset pattern must have exactly one capture group: %w", label(src), ErrInvalidRelease)
	}
	return nil
}

// label returns the human-readable source name used in error messages.
func label(src Source) string {
	if src.Label == "" {
		return "release"
	}
	return src.Label
}

// maxMetadataBytes returns the metadata size cap for src.
func maxMetadataBytes(src Source) int64 {
	if src.MaxMetadataBytes <= 0 {
		return defaultMaxMetadataBytes
	}
	return src.MaxMetadataBytes
}

// maxAssetBytes returns the asset size cap for src.
func maxAssetBytes(src Source) int64 {
	if src.MaxAssetBytes <= 0 {
		return defaultMaxAssetBytes
	}
	return src.MaxAssetBytes
}

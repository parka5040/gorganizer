package tools

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
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bodgit/sevenzip"
	"github.com/google/uuid"

	"github.com/parka/gorganizer/internal/atomicfile"
)

const lootManifestSchema = 1

var lootPortableName = regexp.MustCompile(`(?i)^loot[_-]v?([0-9]+\.[0-9]+\.[0-9]+)[_-]win64\.7z$`)

// LOOTGameID returns the case-sensitive game identifier accepted by LOOT.
func LOOTGameID(gameID string) (string, bool) {
	name, ok := map[string]string{
		"morrowind":          "Morrowind",
		"oblivion":           "Oblivion",
		"skyrim":             "Skyrim",
		"skyrimse":           "Skyrim Special Edition",
		"fallout3":           "Fallout3",
		"falloutnv":          "FalloutNV",
		"ttw":                "FalloutNV",
		"fallout4":           "Fallout4",
		"starfield":          "Starfield",
		"oblivionremastered": "Oblivion Remastered",
	}[gameID]
	return name, ok
}

// LOOTAutoSortSupported reports whether Gorganizer permits automatic sorting for a game.
func LOOTAutoSortSupported(gameID string) bool {
	_, supported := LOOTGameID(gameID)
	return supported && gameID != "ttw"
}

type ManagedToolStatus struct {
	ID              string
	Installed       bool
	ActiveVersion   string
	PreviousVersion string
	ExecutablePath  string
	UpdateAvailable string
}

type LOOTRelease struct {
	Tag       string
	Version   string
	AssetID   int64
	AssetName string
	URL       string
	SHA256    string
}

type lootVersionManifest struct {
	SchemaVersion int       `json:"schema_version"`
	ToolID        string    `json:"tool_id"`
	Tag           string    `json:"tag"`
	Version       string    `json:"version"`
	AssetID       int64     `json:"asset_id"`
	AssetName     string    `json:"asset_name"`
	AssetURL      string    `json:"asset_url"`
	SHA256        string    `json:"sha256"`
	ExecutableRel string    `json:"executable_rel"`
	InstalledAt   time.Time `json:"installed_at"`
	Distribution  string    `json:"distribution"`
	License       string    `json:"license"`
}

type lootCurrentManifest struct {
	SchemaVersion   int    `json:"schema_version"`
	ActiveVersion   string `json:"active_version"`
	PreviousVersion string `json:"previous_version,omitempty"`
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

type LOOTInstaller struct {
	Root       string
	HTTPClient *http.Client
	APIURL     string
	extract    func(context.Context, string, string) error
	mu         sync.Mutex
}

// NewLOOTInstaller constructs an installer rooted at the global managed-tools directory.
func NewLOOTInstaller(root string, client *http.Client) *LOOTInstaller {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	return &LOOTInstaller{
		Root: root, HTTPClient: client,
		APIURL:  "https://api.github.com/repos/loot/loot/releases/latest",
		extract: extract7Zip,
	}
}

// LatestRelease resolves the newest stable official win64 portable archive.
func (i *LOOTInstaller) LatestRelease(ctx context.Context) (LOOTRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, i.APIURL, nil)
	if err != nil {
		return LOOTRelease{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "gorganizer-managed-loot")
	resp, err := i.HTTPClient.Do(req)
	if err != nil {
		return LOOTRelease{}, fmt.Errorf("fetching LOOT release metadata: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return LOOTRelease{}, fmt.Errorf("fetching LOOT release metadata: HTTP %s", resp.Status)
	}
	if resp.ContentLength > 512<<20 {
		return LOOTRelease{}, fmt.Errorf("LOOT release metadata is unexpectedly large: %d bytes", resp.ContentLength)
	}
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&release); err != nil {
		return LOOTRelease{}, fmt.Errorf("decoding LOOT release metadata: %w", err)
	}
	if release.Draft || release.Prerelease {
		return LOOTRelease{}, errors.New("latest LOOT release is not stable")
	}
	for _, asset := range release.Assets {
		match := lootPortableName.FindStringSubmatch(asset.Name)
		if len(match) != 2 {
			continue
		}
		digest := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(asset.Digest)), "sha256:")
		if len(digest) != sha256.Size*2 {
			return LOOTRelease{}, fmt.Errorf("LOOT asset %q has no published SHA-256 digest", asset.Name)
		}
		if _, err := hex.DecodeString(digest); err != nil {
			return LOOTRelease{}, fmt.Errorf("LOOT asset %q has an invalid SHA-256 digest: %w", asset.Name, err)
		}
		return LOOTRelease{
			Tag: release.TagName, Version: match[1], AssetID: asset.ID, AssetName: asset.Name,
			URL: asset.BrowserDownloadURL, SHA256: digest,
		}, nil
	}
	return LOOTRelease{}, errors.New("latest LOOT release has no win64 portable .7z asset")
}

// InstallLatest downloads, verifies, stages, and activates the latest stable release.
func (i *LOOTInstaller) InstallLatest(ctx context.Context) (ManagedToolStatus, error) {
	release, err := i.LatestRelease(ctx)
	if err != nil {
		return ManagedToolStatus{}, err
	}
	return i.Install(ctx, release)
}

// Install installs an already-resolved official release and retains the prior version for rollback.
func (i *LOOTInstaller) Install(ctx context.Context, release LOOTRelease) (ManagedToolStatus, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if release.Version == "" || release.URL == "" || len(release.SHA256) != sha256.Size*2 {
		return ManagedToolStatus{}, errors.New("incomplete LOOT release metadata")
	}
	assetMatch := lootPortableName.FindStringSubmatch(release.AssetName)
	if len(assetMatch) != 2 || assetMatch[1] != release.Version {
		return ManagedToolStatus{}, errors.New("LOOT release asset name and version are inconsistent")
	}
	lootRoot := filepath.Join(i.Root, "loot")
	if err := os.MkdirAll(lootRoot, 0755); err != nil {
		return ManagedToolStatus{}, fmt.Errorf("creating LOOT tools directory: %w", err)
	}
	stage := filepath.Join(lootRoot, ".stage-"+uuid.NewString())
	if err := os.Mkdir(stage, 0700); err != nil {
		return ManagedToolStatus{}, fmt.Errorf("creating LOOT staging directory: %w", err)
	}
	defer os.RemoveAll(stage)

	archive := filepath.Join(stage, release.AssetName)
	if err := i.downloadAndVerify(ctx, release, archive); err != nil {
		return ManagedToolStatus{}, err
	}
	extracted := filepath.Join(stage, "extracted")
	if err := os.Mkdir(extracted, 0755); err != nil {
		return ManagedToolStatus{}, err
	}
	if err := i.extract(ctx, archive, extracted); err != nil {
		return ManagedToolStatus{}, fmt.Errorf("extracting LOOT portable archive: %w", err)
	}
	exeRel, err := findLOOTExecutable(extracted)
	if err != nil {
		return ManagedToolStatus{}, err
	}
	manifest := lootVersionManifest{
		SchemaVersion: lootManifestSchema, ToolID: "loot", Tag: release.Tag, Version: release.Version,
		AssetID: release.AssetID, AssetName: release.AssetName, AssetURL: release.URL, SHA256: release.SHA256,
		ExecutableRel: exeRel, InstalledAt: time.Now().UTC(), Distribution: "official GitHub portable archive",
		License: "GPL-3.0-or-later",
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return ManagedToolStatus{}, err
	}
	if err := atomicfile.WriteFile(filepath.Join(extracted, "gorganizer-manifest.json"), manifestBytes, 0644); err != nil {
		return ManagedToolStatus{}, err
	}

	versionDir := filepath.Join(lootRoot, release.Version)
	if _, err := os.Stat(versionDir); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(extracted, versionDir); err != nil {
			return ManagedToolStatus{}, fmt.Errorf("activating LOOT version directory: %w", err)
		}
	} else if err != nil {
		return ManagedToolStatus{}, err
	}
	current, _ := i.readCurrent()
	next := lootCurrentManifest{SchemaVersion: lootManifestSchema, ActiveVersion: release.Version}
	if current.ActiveVersion != "" && current.ActiveVersion != release.Version {
		next.PreviousVersion = current.ActiveVersion
	} else {
		next.PreviousVersion = current.PreviousVersion
	}
	if err := i.writeCurrent(next); err != nil {
		return ManagedToolStatus{}, err
	}
	i.pruneVersions(next)
	return i.Status()
}

// Rollback atomically reactivates the retained previous version.
func (i *LOOTInstaller) Rollback() (ManagedToolStatus, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	current, err := i.readCurrent()
	if err != nil {
		return ManagedToolStatus{}, err
	}
	if current.PreviousVersion == "" {
		return ManagedToolStatus{}, errors.New("LOOT has no previous version to roll back to")
	}
	if _, err := os.Stat(filepath.Join(i.Root, "loot", current.PreviousVersion)); err != nil {
		return ManagedToolStatus{}, fmt.Errorf("previous LOOT version is unavailable: %w", err)
	}
	next := lootCurrentManifest{
		SchemaVersion: lootManifestSchema, ActiveVersion: current.PreviousVersion,
		PreviousVersion: current.ActiveVersion,
	}
	if err := i.writeCurrent(next); err != nil {
		return ManagedToolStatus{}, err
	}
	return i.Status()
}

// Status reads the active installation without making a network request.
func (i *LOOTInstaller) Status() (ManagedToolStatus, error) {
	current, err := i.readCurrent()
	if errors.Is(err, os.ErrNotExist) {
		return ManagedToolStatus{ID: "loot"}, nil
	}
	if err != nil {
		return ManagedToolStatus{}, err
	}
	manifestPath := filepath.Join(i.Root, "loot", current.ActiveVersion, "gorganizer-manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return ManagedToolStatus{}, fmt.Errorf("reading active LOOT manifest: %w", err)
	}
	var manifest lootVersionManifest
	if err := json.Unmarshal(data, &manifest); err != nil || manifest.SchemaVersion != lootManifestSchema {
		return ManagedToolStatus{}, errors.New("active LOOT manifest is invalid or unsupported")
	}
	exe := filepath.Join(i.Root, "loot", current.ActiveVersion, manifest.ExecutableRel)
	if info, err := os.Stat(exe); err != nil || info.IsDir() {
		return ManagedToolStatus{}, errors.New("active LOOT executable is missing")
	}
	return ManagedToolStatus{
		ID: "loot", Installed: true, ActiveVersion: current.ActiveVersion,
		PreviousVersion: current.PreviousVersion, ExecutablePath: exe,
	}, nil
}

func (i *LOOTInstaller) downloadAndVerify(ctx context.Context, release LOOTRelease, destination string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, release.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "gorganizer-managed-loot")
	resp, err := i.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading LOOT: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading LOOT: HTTP %s", resp.Status)
	}
	if resp.ContentLength > 512<<20 {
		return fmt.Errorf("LOOT archive is unexpectedly large: %d bytes", resp.ContentLength)
	}
	f, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(f, hash), io.LimitReader(resp.Body, 512<<20))
	closeErr := f.Close()
	if copyErr != nil {
		return fmt.Errorf("downloading LOOT: %w", copyErr)
	}
	if closeErr != nil {
		return closeErr
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, release.SHA256) {
		return fmt.Errorf("LOOT SHA-256 mismatch: expected %s, got %s", release.SHA256, actual)
	}
	return nil
}

func (i *LOOTInstaller) readCurrent() (lootCurrentManifest, error) {
	data, err := os.ReadFile(filepath.Join(i.Root, "loot", "current.json"))
	if err != nil {
		return lootCurrentManifest{}, err
	}
	var current lootCurrentManifest
	if err := json.Unmarshal(data, &current); err != nil {
		return lootCurrentManifest{}, err
	}
	if current.SchemaVersion != lootManifestSchema || current.ActiveVersion == "" {
		return lootCurrentManifest{}, errors.New("LOOT current manifest is invalid or unsupported")
	}
	return current, nil
}

func (i *LOOTInstaller) writeCurrent(current lootCurrentManifest) error {
	data, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(i.Root, "loot", "current.json"), data, 0644)
}

func (i *LOOTInstaller) pruneVersions(current lootCurrentManifest) {
	root := filepath.Join(i.Root, "loot")
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == current.ActiveVersion || entry.Name() == current.PreviousVersion || strings.HasPrefix(entry.Name(), ".stage-") {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, entry.Name()))
	}
}

func findLOOTExecutable(root string) (string, error) {
	var found string
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !strings.EqualFold(info.Name(), "LOOT.exe") {
			return nil
		}
		if found != "" {
			return errors.New("LOOT archive contains multiple LOOT.exe files")
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		found = rel
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", errors.New("LOOT portable archive does not contain LOOT.exe")
	}
	return found, nil
}

func extract7Zip(ctx context.Context, archive, destination string) error {
	reader, err := sevenzip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, file := range reader.File {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		name := filepath.Clean(filepath.FromSlash(file.Name))
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe LOOT archive path %q", file.Name)
		}
		target := filepath.Join(destination, name)
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		in, err := file.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, file.Mode().Perm())
		if err != nil {
			in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeOutErr := out.Close()
		closeInErr := in.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeOutErr != nil {
			return closeOutErr
		}
		if closeInErr != nil {
			return closeInErr
		}
	}
	return nil
}

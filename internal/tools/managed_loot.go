package tools

import (
	"context"
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

	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/ghrelease"
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
	release, err := (&ghrelease.Fetcher{HTTP: i.HTTPClient}).Latest(ctx, i.source())
	if errors.Is(err, ghrelease.ErrNoMatchingAsset) {
		return LOOTRelease{}, errors.New("latest LOOT release has no win64 portable .7z asset")
	}
	if err != nil {
		return LOOTRelease{}, err
	}
	return LOOTRelease{
		Tag: release.Tag, Version: release.Version, AssetID: release.AssetID, AssetName: release.AssetName,
		URL: release.URL, SHA256: release.SHA256,
	}, nil
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
	if release.Version == "" || release.URL == "" || len(release.SHA256) != 64 {
		return ManagedToolStatus{}, errors.New("incomplete LOOT release metadata")
	}
	assetMatch := lootPortableName.FindStringSubmatch(release.AssetName)
	if len(assetMatch) != 2 || assetMatch[1] != release.Version {
		return ManagedToolStatus{}, errors.New("LOOT release asset name and version are inconsistent")
	}
	source := i.source()
	sharedRelease := ghrelease.Release{
		Tag: release.Tag, Version: release.Version, AssetID: release.AssetID, AssetName: release.AssetName,
		URL: release.URL, SHA256: release.SHA256,
	}
	if err := ghrelease.ValidateRelease(source, sharedRelease); err != nil {
		return ManagedToolStatus{}, err
	}
	store := i.store()
	stage, cleanup, err := store.NewStage()
	if err != nil {
		return ManagedToolStatus{}, err
	}
	defer cleanup()

	archive := filepath.Join(stage, release.AssetName)
	if err := (&ghrelease.Fetcher{HTTP: i.HTTPClient}).Download(ctx, source, sharedRelease, archive); err != nil {
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
	if err := store.Install(release.Version, extracted, false); err != nil {
		return ManagedToolStatus{}, err
	}
	if _, err := store.Activate(release.Version); err != nil {
		return ManagedToolStatus{}, err
	}
	return i.status(store)
}

// Rollback atomically reactivates the retained previous version.
func (i *LOOTInstaller) Rollback() (ManagedToolStatus, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	store := i.store()
	if _, err := store.Rollback(); err != nil {
		return ManagedToolStatus{}, err
	}
	return i.status(store)
}

// Status reads the active installation without making a network request.
func (i *LOOTInstaller) Status() (ManagedToolStatus, error) {
	return i.status(i.store())
}

func (i *LOOTInstaller) status(store *ghrelease.Store) (ManagedToolStatus, error) {
	current, err := store.ReadCurrent()
	if errors.Is(err, os.ErrNotExist) {
		return ManagedToolStatus{ID: "loot"}, nil
	}
	if err != nil {
		return ManagedToolStatus{}, err
	}
	activeDir, err := store.VersionDir(current.ActiveVersion)
	if err != nil {
		return ManagedToolStatus{}, err
	}
	data, err := os.ReadFile(filepath.Join(activeDir, "gorganizer-manifest.json"))
	if err != nil {
		return ManagedToolStatus{}, fmt.Errorf("reading active LOOT manifest: %w", err)
	}
	var manifest lootVersionManifest
	if err := json.Unmarshal(data, &manifest); err != nil || manifest.SchemaVersion != lootManifestSchema {
		return ManagedToolStatus{}, errors.New("active LOOT manifest is invalid or unsupported")
	}
	exe := filepath.Join(activeDir, manifest.ExecutableRel)
	if info, err := os.Stat(exe); err != nil || info.IsDir() {
		return ManagedToolStatus{}, errors.New("active LOOT executable is missing")
	}
	return ManagedToolStatus{
		ID: "loot", Installed: true, ActiveVersion: current.ActiveVersion,
		PreviousVersion: current.PreviousVersion, ExecutablePath: exe,
	}, nil
}

func (i *LOOTInstaller) source() ghrelease.Source {
	return ghrelease.Source{
		APIURL: i.APIURL, AssetPattern: lootPortableName, UserAgent: "gorganizer-managed-loot", Label: "LOOT",
	}
}

func (i *LOOTInstaller) store() *ghrelease.Store {
	return &ghrelease.Store{Root: filepath.Join(i.Root, "loot"), Label: "LOOT"}
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

package smapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/ghrelease"
)

const (
	artifactManifestName   = "artifact.json"
	artifactManifestSchema = 1
	maxArtifactManifest    = 1 << 20
)

type ArtifactStore struct {
	Store   *ghrelease.Store
	Fetcher *ghrelease.Fetcher
	Source  ghrelease.Source
}

type Artifact struct {
	Version   string
	Tag       string
	AssetName string
	SHA256    string
	ZipPath   string
}

type artifactManifest struct {
	SchemaVersion int       `json:"schema_version"`
	Version       string    `json:"version"`
	Tag           string    `json:"tag"`
	AssetID       int64     `json:"asset_id"`
	AssetName     string    `json:"asset_name"`
	URL           string    `json:"url"`
	SHA256        string    `json:"sha256"`
	FetchedAt     time.Time `json:"fetched_at"`
}

// Latest resolves the newest stable upstream release for the store's source.
func (a *ArtifactStore) Latest(ctx context.Context) (ghrelease.Release, error) {
	return a.Fetcher.Latest(ctx, a.Source)
}

// Fetch returns the retained verified artifact for rel, downloading and storing it first when it is missing or damaged.
func (a *ArtifactStore) Fetch(ctx context.Context, rel ghrelease.Release) (Artifact, error) {
	if err := ghrelease.ValidateRelease(a.Source, rel); err != nil {
		return Artifact{}, err
	}
	if err := validateAssetName(rel.AssetName); err != nil {
		return Artifact{}, err
	}
	if existing, err := a.Artifact(rel.Version); err == nil && existing.AssetName == rel.AssetName && strings.EqualFold(existing.SHA256, rel.SHA256) {
		if ghrelease.VerifyFile(existing.ZipPath, rel.SHA256) == nil {
			return existing, nil
		}
	}
	stage, cleanup, err := a.Store.NewStage()
	if err != nil {
		return Artifact{}, err
	}
	defer cleanup()
	if err := a.Fetcher.Download(ctx, a.Source, rel, filepath.Join(stage, rel.AssetName)); err != nil {
		return Artifact{}, err
	}
	manifest := artifactManifest{
		SchemaVersion: artifactManifestSchema,
		Version:       rel.Version,
		Tag:           rel.Tag,
		AssetID:       rel.AssetID,
		AssetName:     rel.AssetName,
		URL:           rel.URL,
		SHA256:        rel.SHA256,
		FetchedAt:     time.Now().UTC(),
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Artifact{}, err
	}
	if err := atomicfile.WriteFile(filepath.Join(stage, artifactManifestName), data, 0644); err != nil {
		return Artifact{}, fmt.Errorf("writing SMAPI artifact manifest: %w", err)
	}
	if err := a.Store.Install(rel.Version, stage, true); err != nil {
		return Artifact{}, err
	}
	return a.Artifact(rel.Version)
}

// Artifact loads and validates the retained artifact manifest for version.
func (a *ArtifactStore) Artifact(version string) (Artifact, error) {
	dir, err := a.Store.VersionDir(version)
	if err != nil {
		return Artifact{}, err
	}
	data, err := readSmallRegular(filepath.Join(dir, artifactManifestName), maxArtifactManifest)
	if err != nil {
		return Artifact{}, fmt.Errorf("reading SMAPI %s artifact manifest: %w", version, err)
	}
	var manifest artifactManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Artifact{}, fmt.Errorf("decoding SMAPI %s artifact manifest: %w", version, err)
	}
	if manifest.SchemaVersion != artifactManifestSchema {
		return Artifact{}, fmt.Errorf("SMAPI %s artifact manifest has unsupported schema %d", version, manifest.SchemaVersion)
	}
	if manifest.Version != version {
		return Artifact{}, fmt.Errorf("SMAPI artifact manifest in %s names version %q", version, manifest.Version)
	}
	if err := validateAssetName(manifest.AssetName); err != nil {
		return Artifact{}, err
	}
	if len(manifest.SHA256) != 64 {
		return Artifact{}, fmt.Errorf("SMAPI %s artifact manifest has an invalid SHA-256 digest", version)
	}
	if _, err := hex.DecodeString(manifest.SHA256); err != nil {
		return Artifact{}, fmt.Errorf("SMAPI %s artifact manifest has an invalid SHA-256 digest: %w", version, err)
	}
	zipPath := filepath.Join(dir, manifest.AssetName)
	info, err := os.Lstat(zipPath)
	if err != nil {
		return Artifact{}, fmt.Errorf("SMAPI %s artifact archive: %w", version, err)
	}
	if !info.Mode().IsRegular() {
		return Artifact{}, fmt.Errorf("SMAPI %s artifact archive is not a regular file", version)
	}
	return Artifact{
		Version:   manifest.Version,
		Tag:       manifest.Tag,
		AssetName: manifest.AssetName,
		SHA256:    manifest.SHA256,
		ZipPath:   zipPath,
	}, nil
}

// ActiveVersion returns the active artifact version, or an empty string when none has been activated.
func (a *ArtifactStore) ActiveVersion() (string, error) {
	current, err := a.readCurrent()
	return current.ActiveVersion, err
}

// PreviousVersion returns the rollback artifact version, or an empty string when there is none.
func (a *ArtifactStore) PreviousVersion() (string, error) {
	current, err := a.readCurrent()
	return current.PreviousVersion, err
}

// Activate marks a retained, valid artifact version as active.
func (a *ArtifactStore) Activate(version string) (ghrelease.Current, error) {
	if _, err := a.Artifact(version); err != nil {
		return ghrelease.Current{}, err
	}
	return a.Store.Activate(version)
}

// Rollback swaps the active and previous artifact versions.
func (a *ArtifactStore) Rollback() (ghrelease.Current, error) {
	return a.Store.Rollback()
}

// readCurrent loads the store pointers, treating a missing current.json as empty.
func (a *ArtifactStore) readCurrent() (ghrelease.Current, error) {
	current, err := a.Store.ReadCurrent()
	if errors.Is(err, os.ErrNotExist) {
		return ghrelease.Current{}, nil
	}
	return current, err
}

// validateAssetName requires the asset name to be a safe file name distinct from the artifact manifest.
func validateAssetName(name string) error {
	if err := validateSingleName(name); err != nil {
		return fmt.Errorf("invalid SMAPI asset name: %w", err)
	}
	if name == artifactManifestName || name[0] == '.' {
		return fmt.Errorf("invalid SMAPI asset name %q", name)
	}
	return nil
}

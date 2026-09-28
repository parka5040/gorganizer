package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/parka/gorganizer/internal/atomicfile"
	"golang.org/x/sys/unix"
)

const CopyStagePrefix = ".copy-"

// Copy stages a complete profile and publishes it under a new name.
func (pm *Manager) Copy(gameID, sourceName, newName string) (copied *Profile, resultErr error) {
	if err := validateProfileIdentity(gameID, sourceName); err != nil {
		return nil, err
	}
	if err := validateProfileIdentity(gameID, newName); err != nil {
		return nil, err
	}
	profilesDir := filepath.Dir(pm.ProfileDir(gameID, sourceName))
	source := pm.ProfileDir(gameID, sourceName)
	target := pm.ProfileDir(gameID, newName)
	if err := refuseSymlinkedProfileDir(source); err != nil {
		return nil, err
	}
	info, err := os.Lstat(source)
	if err != nil {
		return nil, fmt.Errorf("checking source profile %s: %w", source, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("source profile %s is not a directory", source)
	}
	if err := refuseSymlinkedProfileDir(target); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(target); err == nil {
		return nil, fmt.Errorf("profile %q already exists for %s", newName, gameID)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("checking target profile %s: %w", target, err)
	}

	id, err := uuid.NewRandom()
	if err != nil {
		return nil, fmt.Errorf("creating profile copy ID: %w", err)
	}
	stage := filepath.Join(profilesDir, CopyStagePrefix+id.String())
	if err := os.Mkdir(stage, 0700); err != nil {
		return nil, fmt.Errorf("creating profile copy stage %s: %w", stage, err)
	}
	defer func() {
		if err := os.RemoveAll(stage); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("removing profile copy stage %s: %w", stage, err))
		}
	}()
	if err := pm.copyProfileTree(source, stage); err != nil {
		return nil, err
	}
	profilePath := filepath.Join(stage, "profile.json")
	metadata, err := os.Stat(profilePath)
	if err != nil {
		return nil, fmt.Errorf("checking copied profile %s: %w", profilePath, err)
	}
	if metadata.Size() > 1<<20 {
		return nil, fmt.Errorf("copied profile %s is too large", profilePath)
	}
	data, err := os.ReadFile(profilePath)
	if err != nil {
		return nil, fmt.Errorf("reading copied profile %s: %w", profilePath, err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing copied profile %s: %w", profilePath, err)
	}
	if raw == nil {
		return nil, fmt.Errorf("parsing copied profile %s: expected a JSON object", profilePath)
	}
	var settings Profile
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parsing copied profile %s: %w", profilePath, err)
	}
	created := time.Now()
	for key, value := range map[string]any{"name": newName, "game_id": gameID, "created_at": created} {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encoding copied profile %s: %w", key, err)
		}
		raw[key] = encoded
	}
	updated, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling copied profile: %w", err)
	}
	if err := atomicfile.WriteFile(profilePath, updated, 0644); err != nil {
		return nil, fmt.Errorf("writing copied profile %s: %w", profilePath, err)
	}
	if err := atomicfile.SyncDir(stage); err != nil {
		return nil, fmt.Errorf("syncing profile copy stage: %w", err)
	}
	if err := unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, target, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("profile %q already exists for %s: %w", newName, gameID, err)
		}
		return nil, fmt.Errorf("publishing profile copy %s: %w", target, err)
	}
	if err := atomicfile.SyncDir(profilesDir); err != nil {
		if cleanupErr := os.RemoveAll(target); cleanupErr != nil {
			return nil, errors.Join(fmt.Errorf("syncing profile copy %s: %w", target, err),
				fmt.Errorf("removing profile copy %s: %w", target, cleanupErr))
		}
		return nil, fmt.Errorf("syncing profile copy %s: %w", target, err)
	}
	return &Profile{Name: newName, GameID: gameID, CreatedAt: created, UseCustomIni: settings.UseCustomIni}, nil
}

// copyProfileTree copies regular files and directories without following links.
func (pm *Manager) copyProfileTree(source, stage string) error {
	var dirs []string
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walking profile %s: %w", path, walkErr)
		}
		if path == source {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("checking profile entry %s: %w", path, err)
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return fmt.Errorf("locating profile entry %s: %w", path, err)
		}
		dest := filepath.Join(stage, rel)
		switch {
		case entry.IsDir():
			if err := os.Mkdir(dest, 0700); err != nil {
				return fmt.Errorf("creating copied directory %s: %w", dest, err)
			}
			dirs = append(dirs, dest)
		case info.Mode().IsRegular():
			if _, err := pm.CopyFileDurable(path, dest, info.Mode().Perm(), false); err != nil {
				return fmt.Errorf("copying profile file %s: %w", path, err)
			}
		default:
			return fmt.Errorf("profile entry %s is not a regular file or directory", path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := atomicfile.SyncDir(dirs[i]); err != nil {
			return fmt.Errorf("syncing copied directory %s: %w", dirs[i], err)
		}
	}
	return nil
}

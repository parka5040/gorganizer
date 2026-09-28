package daemon

import (
	"os"
	"path/filepath"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/fsutil"
)

// resolveModDir resolves a validated mod directory for gameID.
func resolveModDir(gameID, modName string) (string, error) {
	if err := fsutil.ValidateName(modName); err != nil {
		return "", &UnsafePathError{Field: "mod_name"}
	}
	return filepath.Join(config.ModsDir(gameID), modName), nil
}

// resolveExistingModDir returns an existing real mod folder with a valid, non-reserved name.
func resolveExistingModDir(gameID, name string) (string, error) {
	if err := download.ValidateTargetModName(name); err != nil {
		return "", err
	}
	modDir := filepath.Join(config.ModsDir(gameID), name)
	if err := requireRealModDir(gameID, name, modDir); err != nil {
		return "", err
	}
	return modDir, nil
}

// requireRealModDir returns ModNotFoundError for a missing mod folder and refuses a non-directory or symlink.
func requireRealModDir(gameID, name, modDir string) error {
	info, err := os.Lstat(modDir)
	if err != nil {
		if os.IsNotExist(err) {
			return &ModNotFoundError{GameID: gameID, Name: name}
		}
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return &download.InvalidTargetModError{Name: name, Reason: "not a real mod folder"}
	}
	return nil
}

// archivePath resolves a validated archive path under downloadsDir.
func archivePath(downloadsDir, archiveRelPath string) (string, error) {
	path, err := fsutil.SafeJoin(downloadsDir, archiveRelPath, false)
	if err != nil {
		return "", &UnsafePathError{Field: "archive_rel_path"}
	}
	return path, nil
}

// validateProfileName validates a profile name at the daemon boundary.
func validateProfileName(profileName string) error {
	if err := fsutil.ValidateName(profileName); err != nil {
		return &UnsafePathError{Field: "profile_name"}
	}
	return nil
}

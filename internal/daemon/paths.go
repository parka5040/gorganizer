package daemon

import (
	"path/filepath"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/fsutil"
)

// resolveModDir resolves a validated mod directory for gameID.
func resolveModDir(gameID, modName string) (string, error) {
	if err := fsutil.ValidateName(modName); err != nil {
		return "", &UnsafePathError{Field: "mod_name"}
	}
	return filepath.Join(config.ModsDir(gameID), modName), nil
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

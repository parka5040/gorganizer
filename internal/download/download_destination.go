package download

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/fsutil"
)

// SafeArchiveFilename reduces an untrusted archive name to a usable final path segment.
func SafeArchiveFilename(name string) (string, bool) {
	parts := strings.Split(strings.ReplaceAll(name, `\`, `/`), "/")
	base := strings.TrimSpace(parts[len(parts)-1])
	if len(base) > 200 || strings.HasPrefix(base, ".") || fsutil.ValidateName(base) != nil {
		return "", false
	}
	return base, true
}

// resolveArchiveDestination confines a ledger path to one folder and one file under Downloads.
func resolveArchiveDestination(gameID, rel string) (string, error) {
	rejected := &ArchiveRejectedError{Reason: ArchiveRejectedDestination, Detail: rel}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 2 || strings.TrimSpace(rel) != rel {
		return "", rejected
	}
	for _, part := range parts {
		if strings.HasPrefix(part, ".") || fsutil.ValidateName(part) != nil {
			return "", rejected
		}
	}
	path, err := fsutil.SafeJoin(config.DownloadsDir(gameID), rel, false)
	if err != nil {
		return "", rejected
	}
	return path, nil
}

// checkArchiveFolder refuses a replaced or symlinked download folder.
func checkArchiveFolder(archivePath, rel string) error {
	fi, err := os.Lstat(filepath.Dir(archivePath))
	if err != nil {
		return fmt.Errorf("checking archive directory: %w", err)
	}
	if !fi.IsDir() {
		return &ArchiveRejectedError{Reason: ArchiveRejectedDestination, Detail: rel}
	}
	return nil
}

// ensureArchiveFolder creates the download folder and refuses symlinked or non-directory folders.
func ensureArchiveFolder(archivePath, rel string) error {
	if err := os.MkdirAll(filepath.Dir(archivePath), 0755); err != nil {
		return fmt.Errorf("creating archive directory: %w", err)
	}
	return checkArchiveFolder(archivePath, rel)
}

// partSize returns the size of a regular .part file or zero if it does not exist.
func partSize(partPath, rel string) (int64, error) {
	fi, err := os.Lstat(partPath)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("checking archive part: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return 0, &ArchiveRejectedError{Reason: ArchiveRejectedDestination, Detail: rel}
	}
	return fi.Size(), nil
}

// openArchivePart opens a regular .part file without following a symlink.
func openArchivePart(partPath, rel string, appendData bool) (*os.File, error) {
	if _, err := partSize(partPath, rel); err != nil {
		return nil, err
	}
	flags := syscall.O_WRONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if appendData {
		flags |= syscall.O_APPEND
	} else {
		flags |= syscall.O_CREAT
	}
	fd, err := syscall.Open(partPath, flags, 0644)
	if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENXIO) || errors.Is(err, syscall.EISDIR) {
		return nil, &ArchiveRejectedError{Reason: ArchiveRejectedDestination, Detail: rel}
	}
	if err != nil {
		return nil, fmt.Errorf("opening archive part: %w", err)
	}
	out := os.NewFile(uintptr(fd), partPath)
	fi, err := out.Stat()
	if err != nil {
		out.Close()
		return nil, fmt.Errorf("checking open archive part: %w", err)
	}
	if !fi.Mode().IsRegular() {
		out.Close()
		return nil, &ArchiveRejectedError{Reason: ArchiveRejectedDestination, Detail: rel}
	}
	return out, nil
}

// validateLedgerDestination checks a resumed entry against its owning game and Downloads folder.
func validateLedgerDestination(gameID string, e LedgerEntry) error {
	if e.GameID != gameID || e.ArchiveRelPath == "" && e.Status != LedgerQueued {
		return &ArchiveRejectedError{Reason: ArchiveRejectedDestination, Detail: e.ArchiveRelPath}
	}
	if e.ArchiveRelPath == "" {
		return nil
	}
	_, err := resolveArchiveDestination(gameID, e.ArchiveRelPath)
	return err
}

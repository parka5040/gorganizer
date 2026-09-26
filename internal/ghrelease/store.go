package ghrelease

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	"golang.org/x/sys/unix"

	"github.com/parka/gorganizer/internal/atomicfile"
)

const (
	CurrentSchema   = 1
	currentFileName = "current.json"
	stagePrefix     = ".stage-"
)

var (
	rootLocksMu sync.Mutex
	rootLocks   = map[string]*sync.Mutex{}
)

type Current struct {
	SchemaVersion   int    `json:"schema_version"`
	ActiveVersion   string `json:"active_version"`
	PreviousVersion string `json:"previous_version,omitempty"`
}

type Store struct {
	Root  string
	Label string
}

// NewStage creates a private staging directory under Root and returns it with a cleanup function.
func (s *Store) NewStage() (string, func(), error) {
	if err := os.MkdirAll(s.Root, 0755); err != nil {
		return "", nil, fmt.Errorf("creating %s tools directory: %w", s.label(), err)
	}
	stage := filepath.Join(s.Root, stagePrefix+uuid.NewString())
	if err := os.Mkdir(stage, 0700); err != nil {
		return "", nil, fmt.Errorf("creating %s staging directory: %w", s.label(), err)
	}
	return stage, func() { _ = os.RemoveAll(stage) }, nil
}

// VersionDir returns the directory that holds version, rejecting unsafe version names.
func (s *Store) VersionDir(version string) (string, error) {
	if err := validateVersionName(version); err != nil {
		return "", tagged(ErrInvalidRelease, "invalid %s version: %w", s.label(), err)
	}
	return filepath.Join(s.Root, version), nil
}

// Install moves prepared into place as version, keeping an existing version unless replace is set.
func (s *Store) Install(version, prepared string, replace bool) error {
	versionDir, err := s.VersionDir(version)
	if err != nil {
		return err
	}
	unlock := lockRoot(s.Root)
	defer unlock()
	err = unix.Renameat2(unix.AT_FDCWD, prepared, unix.AT_FDCWD, versionDir, unix.RENAME_NOREPLACE)
	switch {
	case err == nil:
		return s.syncRoot()
	case !errors.Is(err, unix.EEXIST):
		return fmt.Errorf("activating %s version directory: %w", s.label(), err)
	case !replace:
		return nil
	}
	if err := unix.Renameat2(unix.AT_FDCWD, prepared, unix.AT_FDCWD, versionDir, unix.RENAME_EXCHANGE); err != nil {
		return fmt.Errorf("replacing %s version directory: %w", s.label(), err)
	}
	syncErr := s.syncRoot()
	if err := os.RemoveAll(prepared); err != nil {
		return errors.Join(syncErr, fmt.Errorf("removing replaced %s version directory: %w", s.label(), err))
	}
	return syncErr
}

// ReadCurrent loads and validates current.json, returning the os error unchanged when it cannot be read.
func (s *Store) ReadCurrent() (Current, error) {
	data, err := os.ReadFile(filepath.Join(s.Root, currentFileName))
	if err != nil {
		return Current{}, err
	}
	var current Current
	if err := json.Unmarshal(data, &current); err != nil {
		return Current{}, tagged(ErrInvalidCurrent, "%s current manifest is invalid or unsupported: %w", s.label(), err)
	}
	if current.SchemaVersion != CurrentSchema || current.ActiveVersion == "" {
		return Current{}, tagged(ErrInvalidCurrent, "%s current manifest is invalid or unsupported", s.label())
	}
	if err := validateVersionName(current.ActiveVersion); err != nil {
		return Current{}, tagged(ErrInvalidCurrent, "%s current manifest is invalid or unsupported: active %w", s.label(), err)
	}
	if current.PreviousVersion != "" {
		if err := validateVersionName(current.PreviousVersion); err != nil {
			return Current{}, tagged(ErrInvalidCurrent, "%s current manifest is invalid or unsupported: previous %w", s.label(), err)
		}
	}
	return current, nil
}

// Activate records version as active, retaining the prior active version for rollback, and prunes the rest.
func (s *Store) Activate(version string) (Current, error) {
	if _, err := s.VersionDir(version); err != nil {
		return Current{}, err
	}
	unlock := lockRoot(s.Root)
	defer unlock()
	current, err := s.ReadCurrent()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Current{}, err
	}
	next := Current{SchemaVersion: CurrentSchema, ActiveVersion: version}
	if current.ActiveVersion != "" && current.ActiveVersion != version {
		next.PreviousVersion = current.ActiveVersion
	} else {
		next.PreviousVersion = current.PreviousVersion
	}
	if err := s.writeCurrent(next); err != nil {
		return Current{}, err
	}
	s.prune(next)
	return next, nil
}

// Rollback swaps the active and previous versions after confirming the previous version is still present.
func (s *Store) Rollback() (Current, error) {
	unlock := lockRoot(s.Root)
	defer unlock()
	current, err := s.ReadCurrent()
	if err != nil {
		return Current{}, err
	}
	if current.PreviousVersion == "" {
		return Current{}, tagged(ErrNoPrevious, "%s has no previous version to roll back to", s.label())
	}
	previousDir, err := s.VersionDir(current.PreviousVersion)
	if err != nil {
		return Current{}, err
	}
	if _, err := os.Stat(previousDir); err != nil {
		return Current{}, tagged(ErrPreviousUnavailable, "previous %s version is unavailable: %w", s.label(), err)
	}
	next := Current{
		SchemaVersion:   CurrentSchema,
		ActiveVersion:   current.PreviousVersion,
		PreviousVersion: current.ActiveVersion,
	}
	if err := s.writeCurrent(next); err != nil {
		return Current{}, err
	}
	return next, nil
}

// Prune removes version directories other than the active, previous, and staging directories.
func (s *Store) Prune(cur Current) {
	unlock := lockRoot(s.Root)
	defer unlock()
	s.prune(cur)
}

// prune removes stale version directories and must be called with the root lock held.
func (s *Store) prune(cur Current) {
	if validateVersionName(cur.ActiveVersion) != nil {
		return
	}
	if cur.PreviousVersion != "" && validateVersionName(cur.PreviousVersion) != nil {
		return
	}
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == cur.ActiveVersion || entry.Name() == cur.PreviousVersion || strings.HasPrefix(entry.Name(), stagePrefix) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(s.Root, entry.Name()))
	}
}

// writeCurrent atomically replaces current.json with current.
func (s *Store) writeCurrent(current Current) error {
	data, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(s.Root, currentFileName), data, 0644)
}

// syncRoot flushes the store root directory entry changes to disk.
func (s *Store) syncRoot() error {
	dir, err := os.Open(s.Root)
	if err != nil {
		return fmt.Errorf("syncing %s tools directory: %w", s.label(), err)
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		return fmt.Errorf("syncing %s tools directory: %w", s.label(), syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("closing %s tools directory: %w", s.label(), closeErr)
	}
	return nil
}

// label returns the human-readable store name used in error messages.
func (s *Store) label() string {
	if s.Label == "" {
		return "managed release"
	}
	return s.Label
}

// lockRoot serializes store mutations that share the same cleaned root and returns the unlock function.
func lockRoot(root string) func() {
	key := filepath.Clean(root)
	if abs, err := filepath.Abs(key); err == nil {
		key = abs
	}
	rootLocksMu.Lock()
	mu, ok := rootLocks[key]
	if !ok {
		mu = &sync.Mutex{}
		rootLocks[key] = mu
	}
	rootLocksMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

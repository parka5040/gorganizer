package vfs

import (
	"errors"
	"fmt"
)

type RetargetCommittedError struct {
	Cause error
}

// Error reports that the new farm is live but its transition needs recovery.
func (e *RetargetCommittedError) Error() string {
	return fmt.Sprintf("the new mod farm is active, but its transition needs recovery: %v", e.Cause)
}

// Unwrap returns the operation that prevented the profile switch from finishing cleanly.
func (e *RetargetCommittedError) Unwrap() error {
	return e.Cause
}

type RetargetCleanupError struct {
	Cause error
}

// Error reports that the previous farm was restored but its transition needs recovery.
func (e *RetargetCleanupError) Error() string {
	return fmt.Sprintf("the previous mod farm is active, but its transition needs recovery: %v", e.Cause)
}

// Unwrap returns the operation that prevented the previous farm from finishing cleanup.
func (e *RetargetCleanupError) Unwrap() error {
	return e.Cause
}

var (
	ErrAlreadyMounted       = errors.New("vfs: already mounted")
	ErrNotMounted           = errors.New("vfs: not mounted")
	ErrBackupExists         = errors.New("vfs: backup directory already exists (possible crash recovery needed)")
	ErrDataDirMissing       = errors.New("vfs: game Data directory does not exist")
	ErrCaptureFailed        = errors.New("vfs: capturing new writes into overwrite failed — teardown aborted to avoid data loss")
	ErrManifestInvalid      = errors.New("vfs: farm manifest invalid")
	errDeactivationMismatch = errors.New("vfs: deactivation folders do not match the journal")
)

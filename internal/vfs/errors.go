package vfs

import "errors"

var (
	ErrAlreadyMounted = errors.New("vfs: already mounted")
	ErrNotMounted     = errors.New("vfs: not mounted")
	ErrBackupExists   = errors.New("vfs: backup directory already exists (possible crash recovery needed)")
	ErrDataDirMissing = errors.New("vfs: game Data directory does not exist")
	// ErrCaptureFailed means new writes (tool/game output) could not be moved
	// into the Overwrite mod, so teardown was aborted rather than destroying
	// them with RemoveAll. The farm is left mounted and intact (H-1).
	ErrCaptureFailed = errors.New("vfs: capturing new writes into overwrite failed — teardown aborted to avoid data loss")
)

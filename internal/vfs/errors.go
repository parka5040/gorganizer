package vfs

import "errors"

var (
	ErrAlreadyMounted = errors.New("vfs: already mounted")
	ErrNotMounted     = errors.New("vfs: not mounted")
	ErrBackupExists   = errors.New("vfs: backup directory already exists (possible crash recovery needed)")
	ErrDataDirMissing = errors.New("vfs: game Data directory does not exist")
)

package vfs

import (
	"golang.org/x/sys/unix"
)

// renameExchange atomically swaps two paths using renameat2(RENAME_EXCHANGE):
func renameExchange(a, b string) error {
	return unix.Renameat2(unix.AT_FDCWD, a, unix.AT_FDCWD, b, unix.RENAME_EXCHANGE)
}

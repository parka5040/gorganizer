package vfs

import (
	"golang.org/x/sys/unix"
)

// renameExchange atomically swaps two paths using renameat2(RENAME_EXCHANGE):
// after it returns, a refers to what b was and vice versa. Both paths must
// exist and be on the same filesystem. Callers fall back to a two-rename
// sequence when this returns an error (e.g. ENOSYS/EINVAL on exotic filesystems).
func renameExchange(a, b string) error {
	return unix.Renameat2(unix.AT_FDCWD, a, unix.AT_FDCWD, b, unix.RENAME_EXCHANGE)
}

package fsutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// EnsurePrivateDir creates a 0700 directory, or tightens an existing real directory this user owns to 0700, refusing symlinks, non-directories and directories owned by others.
func EnsurePrivateDir(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("creating private directory %s: %w", path, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("checking private directory %s: %w", path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is not a directory owned by this user", path)
	}
	if info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("making %s private: %w", path, err)
	}
	info, err = os.Lstat(path)
	if err != nil {
		return fmt.Errorf("checking private directory %s: %w", path, err)
	}
	st, ok = info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || int(st.Uid) != os.Getuid() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is not a private directory owned by this user", path)
	}
	return nil
}

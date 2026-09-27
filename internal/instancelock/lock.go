package instancelock

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/fsutil"
)

// Acquire takes an exclusive flock so only one service or offline recovery runs at a time.
func Acquire() (release func(), err error) {
	lockPath := config.LockPath()
	dir := filepath.Dir(lockPath)
	if err := fsutil.EnsurePrivateDir(dir); err != nil {
		return nil, fmt.Errorf("Gorganizer cannot safely use its runtime folder %s: %v. No existing files were removed.", dir, err)
	}

	fd, err := syscall.Open(lockPath, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening lock %s: %w", lockPath, err)
	}
	f := os.NewFile(uintptr(fd), lockPath)
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("checking lock %s: %w", lockPath, err)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || int(st.Uid) != os.Getuid() || st.Nlink != 1 {
		_ = f.Close()
		return nil, fmt.Errorf("lock %s is not a regular file owned by this user with one link", lockPath)
	}

	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, &heldError{path: lockPath}
		}
		return nil, fmt.Errorf("acquiring lock %s: %w", lockPath, err)
	}

	release = func() {
		if err := syscall.Flock(fd, syscall.LOCK_UN); err != nil {
			slog.Warn("releasing flock failed", "path", lockPath, "err", err)
		}
		if err := f.Close(); err != nil {
			slog.Warn("closing lock file failed", "path", lockPath, "err", err)
		}
	}
	if err := f.Truncate(0); err != nil {
		release()
		return nil, fmt.Errorf("truncating lock %s: %w", lockPath, err)
	}
	if _, err := fmt.Fprintf(f, "%d\n", os.Getpid()); err != nil {
		release()
		return nil, fmt.Errorf("writing lock %s: %w", lockPath, err)
	}
	if err := f.Sync(); err != nil {
		release()
		return nil, fmt.Errorf("syncing lock %s: %w", lockPath, err)
	}
	return release, nil
}

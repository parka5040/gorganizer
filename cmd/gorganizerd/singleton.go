package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"

	"github.com/parka/gorganizer/internal/config"
)

// acquireSingleInstanceLock takes an exclusive flock so only one daemon runs at a time.
func acquireSingleInstanceLock() (release func(), err error) {
	lockPath := config.LockPath()
	dir := filepath.Dir(lockPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("creating runtime dir %s: %w", dir, err)
	}

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("opening lock %s: %w", lockPath, err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, fmt.Errorf("another gorganizerd is already running (lock held at %s). "+
				"Kill the existing process with `pkill -x gorganizerd` before starting a new one", lockPath)
		}
		return nil, fmt.Errorf("acquiring lock %s: %w", lockPath, err)
	}

	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Sync()

	release = func() {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
			slog.Warn("releasing flock failed", "path", lockPath, "err", err)
		}
		if err := f.Close(); err != nil {
			slog.Warn("closing lock file failed", "path", lockPath, "err", err)
		}
		_ = os.Remove(lockPath)
	}
	return release, nil
}

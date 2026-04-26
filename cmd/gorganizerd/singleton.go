package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"

	"github.com/parka/gorganizer/internal/config"
)

// acquireSingleInstanceLock takes an exclusive flock on the daemon's
// lock file. Returns a cleanup function that releases the lock on
// daemon shutdown, plus an error if another live instance already
// holds the lock.
//
// Without this, the socket-listen path at internal/ipc/server.go
// silently os.Remove()s any existing gorganizer.sock and binds a new
// one. An older daemon keeps its listening fd but loses the filesystem
// entry — it becomes an orphan that never exits. Every restart stacks
// another orphan; the user ends up with N daemons fighting over mount
// handles, download directories, and status streams.
//
// Lock lives alongside the socket (e.g. $XDG_RUNTIME_DIR/gorganizer/
// gorganizerd.lock). LOCK_EX|LOCK_NB fails fast when someone else
// already holds it; if the previous owner died, the lock is released
// automatically by the kernel and we take it.
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

	// Record our pid so `ps`/`cat` can identify who's holding the lock.
	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Sync()

	release = func() {
		// Log unlock failures: if the kernel can't release the flock,
		// the next daemon may hit EWOULDBLOCK on a lock no one really
		// owns. Rare on local fs; useful breadcrumb if it ever happens
		// (NFS, weird overlay) so a user can `rm` the file by hand.
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

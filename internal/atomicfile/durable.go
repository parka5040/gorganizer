package atomicfile

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

type Outcome int

const (
	NotPublished Outcome = iota
	PublishedUncertain
	Durable
)

var (
	renameFile = os.Rename
	openDir    = os.Open
	syncDir    = func(dir *os.File) error { return dir.Sync() }
	copyData   = io.Copy
	closeFile  = func(file *os.File) error { return file.Close() }
)

type DirectoryIdentity struct {
	Dev uint64 `json:"dev"`
	Ino uint64 `json:"ino"`
}

// Identity returns the device and inode of a real directory without following a final symlink.
func Identity(path string) (DirectoryIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return DirectoryIdentity{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok {
		return DirectoryIdentity{}, fmt.Errorf("atomicfile: %s is not a real directory", path)
	}
	return DirectoryIdentity{Dev: uint64(stat.Dev), Ino: stat.Ino}, nil
}

// SyncFilesystem flushes all filesystem data and metadata containing a staging directory.
func SyncFilesystem(path string) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("atomicfile: opening staging directory %s: %w", path, err)
	}
	defer unix.Close(fd)
	if err := unix.Syncfs(fd); err != nil {
		return fmt.Errorf("atomicfile: syncing filesystem at %s: %w", path, err)
	}
	return nil
}

// SyncDir flushes directory entries to disk.
func SyncDir(dir string) error {
	d, err := openDir(dir)
	if err != nil {
		return fmt.Errorf("atomicfile: opening directory %s: %w", dir, err)
	}
	defer d.Close()
	if err := syncDir(d); err != nil {
		return fmt.Errorf("atomicfile: fsync directory %s: %w", dir, err)
	}
	return nil
}

// WriteFileDurable writes data and reports whether the replacement is durable.
func WriteFileDurable(path string, data []byte, perm os.FileMode) (Outcome, error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return NotPublished, fmt.Errorf("atomicfile: creating temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.Write(data); err != nil {
		return NotPublished, fmt.Errorf("atomicfile: writing temp %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(perm); err != nil {
		return NotPublished, fmt.Errorf("atomicfile: chmod temp %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		return NotPublished, fmt.Errorf("atomicfile: fsync temp %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return NotPublished, fmt.Errorf("atomicfile: closing temp %s: %w", tmpName, err)
	}

	d, err := openDir(dir)
	if err != nil {
		return NotPublished, fmt.Errorf("atomicfile: opening directory %s: %w", dir, err)
	}
	defer d.Close()
	if err := renameFile(tmpName, path); err != nil {
		return NotPublished, fmt.Errorf("atomicfile: renaming %s to %s: %w", tmpName, path, err)
	}
	if err := syncDir(d); err != nil {
		return PublishedUncertain, fmt.Errorf("atomicfile: fsync directory %s: %w", dir, err)
	}
	return Durable, nil
}

// CopyFileDurable copies a regular file and reports whether the destination is durable.
func CopyFileDurable(src, dst string, perm os.FileMode, replace bool) (Outcome, error) {
	return copyFileDurable(src, dst, perm, replace, nil)
}

type progressWriter struct {
	writer    io.Writer
	onWritten func(int64)
}

// Write copies bytes and reports the amount written to the destination.
func (w progressWriter) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	if n > 0 {
		w.onWritten(int64(n))
	}
	return n, err
}

// CopyFileDurableWithProgress copies a regular file while reporting written bytes.
func CopyFileDurableWithProgress(src, dst string, perm os.FileMode, replace bool, onWritten func(int64)) (Outcome, error) {
	return copyFileDurable(src, dst, perm, replace, onWritten)
}

// copyFileDurable copies a regular file and optionally reports bytes written.
func copyFileDurable(src, dst string, perm os.FileMode, replace bool, onWritten func(int64)) (Outcome, error) {
	from, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return NotPublished, fmt.Errorf("atomicfile: opening source %s: %w", src, err)
	}
	defer from.Close()
	info, err := from.Stat()
	if err != nil {
		return NotPublished, fmt.Errorf("atomicfile: stat source %s: %w", src, err)
	}
	if !info.Mode().IsRegular() {
		return NotPublished, fmt.Errorf("atomicfile: source %s is not a regular file: %w", src, fs.ErrInvalid)
	}

	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(dst)+"-*")
	if err != nil {
		return NotPublished, fmt.Errorf("atomicfile: creating temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	var destination io.Writer = tmp
	if onWritten != nil {
		destination = progressWriter{writer: tmp, onWritten: onWritten}
	}
	if _, err := copyData(destination, from); err != nil {
		return NotPublished, fmt.Errorf("atomicfile: copying %s to %s: %w", src, tmpName, err)
	}
	if err := closeFile(from); err != nil {
		return NotPublished, fmt.Errorf("atomicfile: closing source %s: %w", src, err)
	}
	if err := tmp.Chmod(perm); err != nil {
		return NotPublished, fmt.Errorf("atomicfile: chmod temp %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		return NotPublished, fmt.Errorf("atomicfile: fsync temp %s: %w", tmpName, err)
	}
	if err := closeFile(tmp); err != nil {
		return NotPublished, fmt.Errorf("atomicfile: closing temp %s: %w", tmpName, err)
	}

	d, err := openDir(dir)
	if err != nil {
		return NotPublished, fmt.Errorf("atomicfile: opening directory %s: %w", dir, err)
	}
	defer d.Close()
	published := false
	if replace {
		err = renameFile(tmpName, dst)
		published = err == nil
	} else {
		published, err = renameNoReplace(tmpName, dst)
	}
	if err != nil {
		if published {
			return PublishedUncertain, fmt.Errorf("atomicfile: cleaning temp %s after publishing %s: %w", tmpName, dst, err)
		}
		if errors.Is(err, syscall.EEXIST) {
			return NotPublished, fmt.Errorf("atomicfile: destination %s: %w", dst, fs.ErrExist)
		}
		return NotPublished, fmt.Errorf("atomicfile: publishing %s to %s: %w", tmpName, dst, err)
	}
	if err := syncDir(d); err != nil {
		return PublishedUncertain, fmt.Errorf("atomicfile: fsync directory %s: %w", dir, err)
	}
	return Durable, nil
}

// renameNoReplace publishes a temp file only when the destination is absent.
func renameNoReplace(from, to string) (bool, error) {
	err := unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) {
		if err := os.Link(from, to); err != nil {
			return false, err
		}
		return true, os.Remove(from)
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// RemoveDurable removes a path and syncs its parent directory.
func RemoveDurable(path string) error {
	if err := os.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("atomicfile: removing %s: %w", path, err)
	}
	return SyncDir(filepath.Dir(path))
}

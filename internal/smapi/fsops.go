package smapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	renameProbeName = "rename-probe"
	lockWait        = time.Second
	lockPoll        = 10 * time.Millisecond
)

type PayloadFile struct {
	SHA256 string
	Size   int64
}

type FileID struct {
	Dev uint64 `json:"dev"`
	Ino uint64 `json:"ino"`
}

type mountKey struct {
	dev uint64
	mnt uint64
}

// openRegular opens path read-only without following a final symlink and requires a regular file.
func openRegular(path string) (*os.File, os.FileInfo, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, fmt.Errorf("%s is not a regular file", path)
	}
	return file, info, nil
}

// readSmallRegular reads a regular file of at most limit bytes without following symlinks.
func readSmallRegular(path string, limit int64) ([]byte, error) {
	file, info, err := openRegular(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info.Size() > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, limit)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, limit)
	}
	return data, nil
}

// hashRegular returns the digest and size of a regular file, optionally flushing it to disk.
func hashRegular(path string, flush bool) (PayloadFile, os.FileInfo, error) {
	file, info, err := openRegular(path)
	if err != nil {
		return PayloadFile{}, nil, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return PayloadFile{}, nil, fmt.Errorf("hashing %s: %w", path, err)
	}
	if flush {
		if err := file.Sync(); err != nil {
			return PayloadFile{}, nil, fmt.Errorf("flushing %s: %w", path, err)
		}
	}
	return PayloadFile{SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size}, info, nil
}

// copyRegular copies the regular file src to the new file dst with perm and returns the copied digest.
func copyRegular(src, dst string, perm os.FileMode) (PayloadFile, error) {
	in, _, err := openRegular(src)
	if err != nil {
		return PayloadFile{}, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, perm)
	if err != nil {
		return PayloadFile{}, err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(out, hash), in)
	if copyErr == nil {
		copyErr = out.Chmod(perm)
	}
	closeErr := out.Close()
	if copyErr != nil {
		return PayloadFile{}, fmt.Errorf("copying %s: %w", src, copyErr)
	}
	if closeErr != nil {
		return PayloadFile{}, fmt.Errorf("closing %s: %w", dst, closeErr)
	}
	return PayloadFile{SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size}, nil
}

// lstatOptional returns the Lstat result for path, or nil when it does not exist.
func lstatOptional(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return info, err
}

// isRegularFile reports whether path is a regular file and not a symlink.
func isRegularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

// isPlainDir reports whether path is a directory and not a symlink.
func isPlainDir(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}

// syncDir flushes the entries of directory path to disk.
func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		return fmt.Errorf("syncing %s: %w", path, syncErr)
	}
	return closeErr
}

// removeIfEmpty deletes directory path when it exists and is empty.
func removeIfEmpty(path string) {
	_ = unix.Rmdir(path)
}

// renameNoReplace renames src to dst, failing if dst already exists.
func renameNoReplace(src, dst string) error {
	if err := unix.Renameat2(unix.AT_FDCWD, src, unix.AT_FDCWD, dst, unix.RENAME_NOREPLACE); err != nil {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: err}
	}
	return nil
}

// sortedSet returns the members of a set in ascending order.
func sortedSet(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// relToSlash converts a path below root into a slash-separated relative path.
func relToSlash(root, full string) (string, error) {
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

// digestBytes returns the SHA-256 digest and size of data.
func digestBytes(data []byte) PayloadFile {
	sum := sha256.Sum256(data)
	return PayloadFile{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
}

// identify returns the device and inode numbers of info.
func identify(info os.FileInfo) FileID {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return FileID{Dev: uint64(stat.Dev), Ino: stat.Ino}
	}
	return FileID{}
}

// mountOf returns the filesystem device and mount identity of path without following a final symlink.
func mountOf(path string) (mountKey, error) {
	var stx unix.Statx_t
	err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_TYPE|unix.STATX_MNT_ID, &stx)
	if err == nil {
		key := mountKey{dev: unix.Mkdev(stx.Dev_major, stx.Dev_minor)}
		if stx.Mask&unix.STATX_MNT_ID != 0 {
			key.mnt = stx.Mnt_id
		}
		return key, nil
	}
	if !errors.Is(err, unix.ENOSYS) {
		return mountKey{}, &os.PathError{Op: "statx", Path: path, Err: err}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return mountKey{}, err
	}
	return mountKey{dev: identify(info).Dev}, nil
}

// requireMount fails with ErrCrossDevice unless path lies on the given mount.
func requireMount(path string, want mountKey) error {
	got, err := mountOf(path)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("%s: %w", path, ErrCrossDevice)
	}
	return nil
}

// guardAncestors verifies that gameDir and every existing directory between it and full are real directories on the game mount.
func guardAncestors(gameDir string, mount mountKey, full string) error {
	rel, err := filepath.Rel(gameDir, full)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return &UnsafeTargetError{Path: full, Reason: "path is not inside the game directory"}
	}
	dir := gameDir
	segments := strings.Split(rel, string(filepath.Separator))
	for i := 0; i < len(segments); i++ {
		info, err := lstatOptional(dir)
		if err != nil {
			return err
		}
		if info == nil {
			return nil
		}
		if !info.IsDir() {
			return &UnsafeTargetError{Path: filepath.ToSlash(rel), Reason: fmt.Sprintf("ancestor %s is not a real directory", dir)}
		}
		if err := requireMount(dir, mount); err != nil {
			return err
		}
		dir = filepath.Join(dir, segments[i])
	}
	return nil
}

// lockGame takes an exclusive advisory lock on gameDir and returns its release, retrying briefly and then failing with ErrTransactionActive while another holder exists.
func lockGame(gameDir string) (func(), error) {
	dir, err := os.OpenFile(gameDir, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("opening game directory for locking: %w", err)
	}
	deadline := time.Now().Add(lockWait)
	for {
		err = unix.Flock(int(dir.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if !errors.Is(err, unix.EWOULDBLOCK) || time.Now().After(deadline) {
			break
		}
		time.Sleep(lockPoll)
	}
	if errors.Is(err, unix.EWOULDBLOCK) {
		_ = dir.Close()
		return nil, fmt.Errorf("%s is locked: %w", gameDir, ErrTransactionActive)
	}
	return func() { _ = dir.Close() }, nil
}

// probeRenameNoReplace proves inside dir that renameat2 honours RENAME_NOREPLACE, failing with ErrRenameUnsupported otherwise.
func probeRenameNoReplace(dir string) error {
	probe := filepath.Join(dir, renameProbeName)
	if err := os.Mkdir(probe, 0700); err != nil {
		return fmt.Errorf("creating rename probe: %w", err)
	}
	defer func() { _ = os.RemoveAll(probe) }()
	first, second, third := filepath.Join(probe, "a"), filepath.Join(probe, "b"), filepath.Join(probe, "c")
	for _, name := range []string{first, second} {
		if _, err := writeNewFile(name, nil, 0600); err != nil {
			return fmt.Errorf("creating rename probe: %w", err)
		}
	}
	err := unix.Renameat2(unix.AT_FDCWD, first, unix.AT_FDCWD, second, unix.RENAME_NOREPLACE)
	switch {
	case err == nil:
		return fmt.Errorf("renameat2 replaced an existing file: %w", ErrRenameUnsupported)
	case errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP):
		return fmt.Errorf("renameat2: %v: %w", err, ErrRenameUnsupported)
	case !errors.Is(err, unix.EEXIST):
		return fmt.Errorf("probing renameat2: %w", err)
	}
	err = unix.Renameat2(unix.AT_FDCWD, first, unix.AT_FDCWD, third, unix.RENAME_NOREPLACE)
	switch {
	case errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP):
		return fmt.Errorf("renameat2: %v: %w", err, ErrRenameUnsupported)
	case err != nil:
		return fmt.Errorf("probing renameat2: %w", err)
	}
	return nil
}

// removeOwnedTree deletes path, granting the owner full access to every directory beneath it first when a plain removal fails.
func removeOwnedTree(path string) error {
	if err := os.RemoveAll(path); err == nil {
		return nil
	}
	_ = filepath.WalkDir(path, func(current string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return nil
		}
		if info, err := entry.Info(); err == nil {
			_ = os.Chmod(current, info.Mode().Perm()|0700)
		}
		return nil
	})
	return os.RemoveAll(path)
}

// syncDirs flushes every directory in dirs that still exists, reporting the first failure.
func syncDirs(dirs map[string]bool) error {
	var first error
	for _, dir := range sortedSet(dirs) {
		if !isPlainDir(dir) {
			continue
		}
		if err := syncDir(dir); err != nil && first == nil {
			first = err
		}
	}
	return first
}

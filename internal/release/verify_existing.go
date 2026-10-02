package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// fileHash hashes a regular release file without following its final symlink.
func fileHash(ctx context.Context, name string) (string, error) {
	fd, err := os.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer fd.Close()
	var digest hash.Hash = sha256.New()
	if _, err := io.Copy(digest, contextReader{ctx, fd}); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// checkFileHash hashes a regular release file without following its final symlink.
func checkFileHash(ctx context.Context, name, rel, expected string) error {
	actual, err := fileHash(ctx, name)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("release file does not match manifest: %s", rel)
	}
	return nil
}

type treeEntry struct {
	mode   fs.FileMode
	target string
	hash   string
}

// releaseTree records the paths, modes, links and file hashes in a release tree.
func releaseTree(ctx context.Context, root string) (map[string]treeEntry, error) {
	entries := make(map[string]treeEntry)
	err := filepath.WalkDir(root, func(name string, _ fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		info, err := os.Lstat(name)
		if err != nil {
			return err
		}
		mode := info.Mode()
		entry := treeEntry{mode: mode & (os.ModeType | os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)}
		switch {
		case mode.IsDir():
		case mode.IsRegular():
			entry.hash, err = fileHash(ctx, name)
		case mode&os.ModeSymlink != 0:
			entry.target, err = os.Readlink(name)
		default:
			return fmt.Errorf("unsupported release entry: %s", rel)
		}
		if err == nil {
			entries[rel] = entry
		}
		return err
	})
	return entries, err
}

// existingMatches confirms that an installed release is identical to the staged tree.
func existingMatches(ctx context.Context, stage, destination string) error {
	mismatch := func() error { return fmt.Errorf("the installed release does not match this download") }
	staged, err := releaseTree(ctx, stage)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return mismatch()
	}
	installed, err := releaseTree(ctx, destination)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return mismatch()
	}
	if len(staged) != len(installed) {
		return mismatch()
	}
	for path, entry := range staged {
		if err := ctx.Err(); err != nil {
			return err
		}
		if installed[path] != entry {
			return mismatch()
		}
	}
	return nil
}

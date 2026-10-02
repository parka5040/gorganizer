package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// checkFileHash hashes a regular release file without following its final symlink.
func checkFileHash(ctx context.Context, name, rel, expected string) error {
	fd, err := os.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer fd.Close()
	var digest hash.Hash = sha256.New()
	if _, err := io.Copy(digest, contextReader{ctx, fd}); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if hex.EncodeToString(digest.Sum(nil)) != expected {
		return fmt.Errorf("release file does not match manifest: %s", rel)
	}
	return nil
}

// readManifest reads a bounded regular manifest without following symlinks.
func readManifest(dir string) ([]byte, error) {
	name := filepath.Join(dir, "MANIFEST.sha256")
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return nil, fmt.Errorf("invalid release manifest")
	}
	fd, err := os.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer fd.Close()
	return io.ReadAll(io.LimitReader(fd, 4<<20+1))
}

// existingMatches confirms that an installed release still contains the staged manifest files.
func existingMatches(ctx context.Context, stage, destination string) error {
	mismatch := func() error { return fmt.Errorf("the installed release does not match this download") }
	staged, err := readManifest(stage)
	if err != nil {
		return err
	}
	installed, err := readManifest(destination)
	if err != nil || !bytes.Equal(staged, installed) {
		return mismatch()
	}
	entries, err := manifestLines(staged)
	if err != nil {
		return err
	}
	for relative, expected := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := plainParents(destination, relative); err != nil {
			return mismatch()
		}
		name := filepath.Join(destination, filepath.FromSlash(relative))
		info, err := os.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || checkFileHash(ctx, name, relative, expected) != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return mismatch()
		}
	}
	return nil
}

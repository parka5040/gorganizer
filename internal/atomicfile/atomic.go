// Package atomicfile provides crash-safe file writes: data is written to a
// temporary file in the same directory, fsync'd, then atomically renamed over
// the destination so a reader (or a crash) never observes a torn or partial
// file. This is the shared implementation behind config.json, profile files,
// mod metadata.yaml, the downloads index, and the in-flight ledger.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFile atomically writes data to path with the given permissions.
//
// It creates a temp file in path's directory, writes and fsyncs it, sets perm
// (independent of umask), fsyncs, renames it over path, and best-effort fsyncs
// the parent directory so the rename itself is durable. On any error before the
// rename the temp file is removed and path is left untouched.
//
// perm is applied verbatim — callers that need 0600 (e.g. the config file,
// which holds the Nexus API key) must pass 0600.
func WriteFile(path string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(path)

	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("atomicfile: creating temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()

	// Ensure the temp file is never left behind on a failure path.
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	if _, err = tmp.Write(data); err != nil {
		return fmt.Errorf("atomicfile: writing temp %s: %w", tmpName, err)
	}
	// Set the final permissions before the rename so the destination never
	// briefly exists with the wrong mode. CreateTemp makes the file 0600.
	if err = tmp.Chmod(perm); err != nil {
		return fmt.Errorf("atomicfile: chmod temp %s: %w", tmpName, err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("atomicfile: fsync temp %s: %w", tmpName, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("atomicfile: closing temp %s: %w", tmpName, err)
	}

	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("atomicfile: renaming %s to %s: %w", tmpName, path, err)
	}

	// Best-effort: fsync the directory so the rename survives a crash. A
	// failure here does not invalidate the write, so it is not fatal.
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

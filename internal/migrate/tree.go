package migrate

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
	"syscall"

	"github.com/parka/gorganizer/internal/atomicfile"
)

var hashFile = digestFile

// digestFile hashes a regular file after checking its identity before and after reading.
func digestFile(path string, expected entry) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", fmt.Errorf("opening %s: %w", path, err)
	}
	opened, statErr := f.Stat()
	if statErr != nil {
		_ = f.Close()
		return "", fmt.Errorf("checking %s: %w", path, statErr)
	}
	if !sameFileSnapshot(opened, expected) {
		_ = f.Close()
		return "", fmt.Errorf("file %s changed before hashing", path)
	}
	digest := sha256.New()
	_, copyErr := io.Copy(digest, f)
	finished, statErr := f.Stat()
	closeErr := f.Close()
	if copyErr != nil {
		return "", fmt.Errorf("reading %s: %w", path, copyErr)
	}
	if statErr != nil {
		return "", fmt.Errorf("checking %s: %w", path, statErr)
	}
	if !sameFileSnapshot(finished, expected) {
		return "", fmt.Errorf("file %s changed during hashing", path)
	}
	if closeErr != nil {
		return "", fmt.Errorf("closing %s: %w", path, closeErr)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// verifyTree compares every entry against its recorded identity and any available digest.
func verifyTree(root string, expected []entry, identity bool) error {
	actual, _, _, _, _, err := inventory(root)
	if err != nil {
		return fmt.Errorf("checking folder %s: %w", root, err)
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("folder %s no longer matches the planned files", root)
	}
	for i, want := range expected {
		if !matching(actual[i], want, identity, identity) {
			return fmt.Errorf("file %s in %s has changed", want.Path, root)
		}
		if err := verifyDigest(root, actual[i], want); err != nil {
			return err
		}
	}
	return nil
}

// matching checks entry metadata and optionally the original device, inode and modification time.
func matching(got, want entry, identity, mtime bool) bool {
	if got.Path != want.Path || got.Kind != want.Kind || got.Mode != want.Mode || got.Size != want.Size || got.Target != want.Target {
		return false
	}
	if identity && (got.Dev != want.Dev || got.Ino != want.Ino) {
		return false
	}
	return !mtime || got.Mtime == want.Mtime
}

// verifyDigest checks a copied file against its source digest when one was recorded.
func verifyDigest(root string, got, want entry) error {
	if want.Kind != "file" || want.SHA256 == "" {
		return nil
	}
	path := filepath.Join(root, want.Path)
	digest, err := hashFile(path, got)
	if err != nil {
		return fmt.Errorf("checking file %s: %w", path, err)
	}
	if digest != want.SHA256 {
		return fmt.Errorf("file %s has changed", path)
	}
	return nil
}

// verifyRemaining checks that a partly removed parked source contains no new or changed entries.
func verifyRemaining(root string, expected []entry) error {
	actual, _, _, _, _, err := inventory(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking parked folder: %w", err)
	}
	byPath := make(map[string]entry, len(expected))
	for _, e := range expected {
		byPath[e.Path] = e
	}
	for _, e := range actual {
		want, ok := byPath[e.Path]
		if !ok || !matching(e, want, true, e.Kind != "dir") {
			return fmt.Errorf("parked file %s has changed", e.Path)
		}
		if err := verifyDigest(root, e, want); err != nil {
			return err
		}
	}
	return nil
}

// buildStage hashes source files and makes a verified copy in the unpublished staging directory.
func buildStage(j *journal, item *journalItem, stage string, checkpoint func(string) error, progress func(int64)) error {
	if err := verifyTree(item.Source, item.Entries, true); err != nil {
		return fmt.Errorf("old folder changed since planning; nothing was removed: %w", err)
	}
	if err := prepareStageParent(stage); err != nil {
		return err
	}
	if pathExists(stage) {
		info, err := os.Lstat(stage)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("the staging folder is not a real directory: %w", err)
		}
		if err := os.RemoveAll(stage); err != nil {
			return fmt.Errorf("clearing incomplete copy: %w", err)
		}
		if err := atomicfile.SyncDir(filepath.Dir(stage)); err != nil {
			return err
		}
		if err := checkpoint("stage-cleared"); err != nil {
			return err
		}
	}
	if err := os.Mkdir(stage, 0o700); err != nil {
		return fmt.Errorf("creating staging folder: %w", err)
	}
	if err := atomicfile.SyncDir(filepath.Dir(stage)); err != nil {
		return err
	}
	if err := checkpoint("stage-created"); err != nil {
		return err
	}
	var dirs []entry
	for i := range item.Entries {
		e := &item.Entries[i]
		path := filepath.Join(stage, e.Path)
		switch e.Kind {
		case "dir":
			dirs = append(dirs, *e)
			if e.Path != "." {
				if err := os.Mkdir(path, 0o700); err != nil {
					return fmt.Errorf("creating copied folder: %w", err)
				}
				if err := atomicfile.SyncDir(filepath.Dir(path)); err != nil {
					return err
				}
				if err := checkpoint("directory-created"); err != nil {
					return err
				}
			}
		case "file":
			source := filepath.Join(item.Source, e.Path)
			digest, err := hashFile(source, *e)
			if err != nil {
				return fmt.Errorf("checking old file %s: %w", e.Path, err)
			}
			if e.SHA256 != "" && e.SHA256 != digest {
				return fmt.Errorf("old file %s has changed", e.Path)
			}
			e.SHA256 = digest
			if err := saveJournal(j); err != nil {
				return err
			}
			if _, err := atomicfile.CopyFileDurableWithProgress(source, path, os.FileMode(e.Mode), false, progress); err != nil {
				return fmt.Errorf("copying %s: %w", e.Path, err)
			}
			if err := checkpoint("file-copied"); err != nil {
				return err
			}
		case "link":
			if err := os.Symlink(e.Target, path); err != nil {
				return fmt.Errorf("copying link %s: %w", e.Path, err)
			}
			if err := atomicfile.SyncDir(filepath.Dir(path)); err != nil {
				return err
			}
			if err := checkpoint("link-copied"); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported file in planned copy")
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i].Path) > len(dirs[j].Path) })
	for _, e := range dirs {
		if err := os.Chmod(filepath.Join(stage, e.Path), os.FileMode(e.Mode)); err != nil {
			return fmt.Errorf("setting copied folder permissions: %w", err)
		}
		if err := atomicfile.SyncDir(filepath.Join(stage, e.Path)); err != nil {
			return err
		}
		if err := checkpoint("directory-synced"); err != nil {
			return err
		}
	}
	if err := verifyTree(stage, item.Entries, false); err != nil {
		return err
	}
	return verifyTree(item.Source, item.Entries, true)
}

// prepareStageParent creates and syncs the new folder's parent without replacing links.
func prepareStageParent(stage string) error {
	return ensureDirDurable(filepath.Dir(stage))
}

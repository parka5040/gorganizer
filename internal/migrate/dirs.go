package migrate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/parka/gorganizer/internal/atomicfile"
)

// ensureDirDurable creates each missing real directory and syncs its parent.
func ensureDirDurable(path string) error {
	var missing []string
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("%s is not a real directory", current)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("checking destination folder %s: %w", current, err)
		}
		if current == filepath.Dir(current) {
			return fmt.Errorf("cannot find an existing destination parent")
		}
		missing = append(missing, current)
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0o755); err != nil {
			return fmt.Errorf("creating destination folder %s: %w", missing[i], err)
		}
		if err := atomicfile.SyncDir(filepath.Dir(missing[i])); err != nil {
			return err
		}
	}
	return nil
}

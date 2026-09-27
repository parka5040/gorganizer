package fsutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrExistingLink = errors.New("existing path contains a symlink")
var ErrExistingNonDirectory = errors.New("existing path contains a non-directory ancestor")

// CheckExistingPath checks the root and each existing component of a relative destination.
func CheckExistingPath(root, rel string) error {
	if rel == "" || rel == "." || filepath.IsAbs(rel) || filepath.Clean(rel) != rel || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("invalid relative path %q", rel)
	}
	parts := append([]string{""}, strings.Split(rel, string(filepath.Separator))...)
	current := root
	for i, part := range parts {
		if i > 0 {
			current = filepath.Join(current, part)
		}
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("checking %s: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s: %w", current, ErrExistingLink)
		}
		if i < len(parts)-1 && !info.IsDir() {
			return fmt.Errorf("%s: %w", current, ErrExistingNonDirectory)
		}
	}
	return nil
}

package fsutil

import (
	"errors"
	"path/filepath"
	"strings"
)

// ContainedBy reports whether candidate is contained by root.
func ContainedBy(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// SafeJoin joins a trusted root with a safe relative path.
func SafeJoin(root, relative string, allowDot bool) (string, error) {
	relative = filepath.FromSlash(strings.ReplaceAll(strings.TrimSpace(relative), `\`, `/`))
	if relative == "" || filepath.IsAbs(relative) {
		return "", errors.New("path must be non-empty and relative")
	}
	clean := filepath.Clean(relative)
	if clean == "." && !allowDot {
		return "", errors.New("path must name an extracted entry")
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes its root")
	}
	joined := filepath.Join(root, clean)
	if !ContainedBy(root, joined) {
		return "", errors.New("path escapes its root")
	}
	return joined, nil
}

// ValidateName returns an error when name is not a safe single path segment.
func ValidateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("name must not be empty")
	}
	if name == "." || name == ".." {
		return errors.New("name must not be a relative path element")
	}
	if strings.ContainsAny(name, `/\`) {
		return errors.New("name must not contain a path separator")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return errors.New("name must not contain control characters")
		}
	}
	return nil
}

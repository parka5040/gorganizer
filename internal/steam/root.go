package steam

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/parka/gorganizer/internal/fsutil"
)

type Root struct {
	Path      string
	Libraries []string
}

// FindRoots returns existing Steam installations and their libraries in candidate order.
func FindRoots() ([]Root, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("finding home directory: %w", err)
	}

	candidates := []string{
		filepath.Join(home, ".local", "share", "Steam"),
		filepath.Join(home, ".steam", "steam"),
		filepath.Join(home, ".steam", "root"),
		filepath.Join(home, ".var", "app", "com.valvesoftware.Steam", ".local", "share", "Steam"),
		filepath.Join(home, "snap", "steam", "common", ".local", "share", "Steam"),
	}
	if dataHome := os.Getenv("XDG_DATA_HOME"); dataHome != "" {
		candidates = append(candidates, filepath.Join(dataHome, "Steam"))
	}

	var roots []Root
	seen := make(map[string]bool)
	for _, candidate := range candidates {
		if !fsutil.DirExists(filepath.Join(candidate, "steamapps")) {
			continue
		}
		path, err := filepath.EvalSymlinks(candidate)
		if err != nil || seen[path] {
			continue
		}
		seen[path] = true
		roots = append(roots, Root{Path: path, Libraries: Libraries(path)})
	}
	if len(roots) == 0 {
		return nil, ErrRootNotFound
	}
	return roots, nil
}

// FindRoot returns the first existing Steam installation.
func FindRoot() (string, error) {
	roots, err := FindRoots()
	if err != nil {
		return "", err
	}
	return roots[0].Path, nil
}

// Libraries returns the root and its existing library folders in Steam's numbered order.
func Libraries(steamRoot string) []string {
	root, err := filepath.EvalSymlinks(steamRoot)
	if err != nil {
		return nil
	}
	libraries := []string{root}
	parsed, err := ParseVDFFromFile(filepath.Join(root, "steamapps", "libraryfolders.vdf"))
	if err != nil {
		return libraries
	}
	entries, ok := parsed["libraryfolders"].(map[string]interface{})
	if !ok {
		return libraries
	}
	type numberedLibrary struct {
		number int
		key    string
	}
	keys := make([]numberedLibrary, 0, len(entries))
	for key := range entries {
		if n, err := strconv.Atoi(key); err == nil && n >= 0 {
			keys = append(keys, numberedLibrary{number: n, key: key})
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].number != keys[j].number {
			return keys[i].number < keys[j].number
		}
		return keys[i].key < keys[j].key
	})
	seen := map[string]bool{root: true}
	for _, key := range keys {
		var path string
		switch entry := entries[key.key].(type) {
		case map[string]interface{}:
			path, _ = entry["path"].(string)
		case string:
			path = entry
		}
		if !filepath.IsAbs(path) || !fsutil.DirExists(filepath.Join(path, "steamapps")) {
			continue
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || seen[resolved] {
			continue
		}
		seen[resolved] = true
		libraries = append(libraries, resolved)
	}
	return libraries
}

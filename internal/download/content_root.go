package download

import (
	"os"
	"path/filepath"
	"strings"
)

// DetectContentRoot identifies a clear game-data root or reports that the archive layout is ambiguous.
func DetectContentRoot(extractDir, gameID string) (string, bool) {
	entries, err := os.ReadDir(extractDir)
	if err != nil {
		return "", true
	}
	var dirs []string
	var hasGameFiles bool
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			dirs = append(dirs, name)
			continue
		}
		if entry.Type().IsRegular() {
			switch strings.ToLower(filepath.Ext(name)) {
			case ".esp", ".esm", ".esl", ".bsa", ".ba2":
				hasGameFiles = true
			}
		}
	}
	if gameID == "oblivionremastered" && hasOblivionRemasteredRootMarkers(extractDir) {
		return "", false
	}
	for _, dir := range dirs {
		if strings.EqualFold(dir, "Data") {
			return dir, false
		}
	}
	if len(dirs) == 1 {
		wrapper := filepath.Join(extractDir, dirs[0])
		children, err := os.ReadDir(wrapper)
		if err == nil {
			for _, child := range children {
				if child.IsDir() && strings.EqualFold(child.Name(), "Data") {
					return filepath.ToSlash(filepath.Join(dirs[0], child.Name())), false
				}
			}
		}
	}
	for _, dir := range dirs {
		switch strings.ToLower(dir) {
		case "textures", "meshes", "scripts", "sound", "interface", "skse", "nvse", "fose", "f4se":
			hasGameFiles = true
		}
	}
	if hasGameFiles {
		return "", false
	}
	if len(dirs) == 1 {
		return dirs[0], false
	}
	fallback, err := filepath.Rel(extractDir, FindContentRoot(extractDir))
	if err != nil || fallback == "." {
		return "", true
	}
	return filepath.ToSlash(fallback), true
}

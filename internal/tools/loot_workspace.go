package tools

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type LOOTWorkspace struct {
	Root     string
	GameRoot string
	DataPath string
}

type LOOTWorkspaceOptions struct {
	SteamLibrary string
	AppID        int
	RunID        string
	GameID       string
	InstallPath  string
	DataPath     string
	DataSubpath  string
}

// BuildLOOTWorkspace creates a same-library fake game root without exposing source plugins to writes.
func BuildLOOTWorkspace(options LOOTWorkspaceOptions) (*LOOTWorkspace, error) {
	if options.SteamLibrary == "" || options.RunID == "" || options.InstallPath == "" || options.DataPath == "" {
		return nil, errors.New("LOOT workspace requires Steam library, run ID, install path, and Data path")
	}
	if options.DataSubpath == "" {
		options.DataSubpath = "Data"
	}
	if !safeRelativePath(options.DataSubpath) {
		return nil, fmt.Errorf("unsafe LOOT Data subpath %q", options.DataSubpath)
	}
	workspaceRoot := filepath.Join(
		options.SteamLibrary, "steamapps", "common", ".gorganizer-workspaces",
		fmt.Sprintf("%d", options.AppID), options.RunID,
	)
	gameRoot := filepath.Join(workspaceRoot, "game")
	dataPath := filepath.Join(gameRoot, options.DataSubpath)
	if err := os.MkdirAll(dataPath, 0700); err != nil {
		return nil, fmt.Errorf("creating LOOT workspace: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(workspaceRoot)
		}
	}()

	marker, ok := lootGameMarker(options.GameID)
	if !ok {
		return nil, fmt.Errorf("LOOT has no game marker for %s", options.GameID)
	}
	markerSource := filepath.Join(options.InstallPath, marker)
	markerDestination := filepath.Join(gameRoot, marker)
	if err := linkOrCopy(markerSource, markerDestination, false); err != nil {
		return nil, fmt.Errorf("projecting game marker %s: %w", marker, err)
	}
	if options.GameID == "morrowind" {
		iniSource := filepath.Join(options.InstallPath, "Morrowind.ini")
		if _, err := os.Stat(iniSource); err == nil {
			if err := linkOrCopy(iniSource, filepath.Join(gameRoot, "Morrowind.ini"), true); err != nil {
				return nil, fmt.Errorf("projecting Morrowind.ini: %w", err)
			}
		}
	}
	if err := projectLOOTData(options.DataPath, dataPath); err != nil {
		return nil, err
	}
	cleanup = false
	return &LOOTWorkspace{Root: workspaceRoot, GameRoot: gameRoot, DataPath: dataPath}, nil
}

// Remove deletes the ephemeral workspace.
func (w *LOOTWorkspace) Remove() error {
	if w == nil || w.Root == "" {
		return nil
	}
	return os.RemoveAll(w.Root)
}

// SweepLOOTWorkspaces removes interrupted tool workspaces for one game.
func SweepLOOTWorkspaces(steamLibrary string, appID int) error {
	if steamLibrary == "" || appID <= 0 {
		return nil
	}
	root := filepath.Join(steamLibrary, "steamapps", "common", ".gorganizer-workspaces", fmt.Sprintf("%d", appID))
	return os.RemoveAll(root)
}

func lootGameMarker(gameID string) (string, bool) {
	marker, ok := map[string]string{
		"morrowind":          "Morrowind.exe",
		"oblivion":           "Oblivion.exe",
		"skyrim":             "TESV.exe",
		"skyrimse":           "SkyrimSE.exe",
		"fallout3":           "Fallout3.exe",
		"falloutnv":          "FalloutNV.exe",
		"ttw":                "FalloutNV.exe",
		"fallout4":           "Fallout4.exe",
		"starfield":          "Starfield.exe",
		"oblivionremastered": "OblivionRemastered.exe",
	}[gameID]
	return marker, ok
}

func safeRelativePath(path string) bool {
	clean := filepath.Clean(path)
	return clean != "." && !filepath.IsAbs(clean) && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func projectLOOTData(sourceRoot, destinationRoot string) error {
	return filepath.Walk(sourceRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourceRoot, path)
		if err != nil || rel == "." {
			return err
		}
		if !safeRelativePath(rel) {
			return fmt.Errorf("unsafe Data entry %q", rel)
		}
		destination := filepath.Join(destinationRoot, rel)
		if info.IsDir() {
			return os.MkdirAll(destination, 0700)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return fmt.Errorf("resolving Data symlink %s: %w", rel, err)
			}
			path = resolved
			info, err = os.Stat(path)
			if err != nil || info.IsDir() {
				return fmt.Errorf("unsupported Data symlink target %s", rel)
			}
		}
		privateCopy := isPrivateLOOTFile(rel)
		return linkOrCopy(path, destination, privateCopy)
	})
}

func isPrivateLOOTFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".esm", ".esp", ".esl":
		return true
	}
	base := strings.ToLower(filepath.Base(path))
	return base == "plugins.txt" || base == "loadorder.txt"
}

func linkOrCopy(source, destination string, forceCopy bool) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	if !forceCopy {
		if err := os.Link(source, destination); err == nil {
			return nil
		}
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chtimes(destination, info.ModTime(), info.ModTime())
}

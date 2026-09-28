package vfs

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

type FarmClassification struct {
	Owned          []string
	Output         []string
	UnknownSymlink []string
	Special        []string
}

// ClassifyFarm groups farm entries by their exact placed identity without following symlinks.
func ClassifyFarm(dataDir string, manifest *FarmManifest) (*FarmClassification, error) {
	if manifest == nil {
		return nil, fmt.Errorf("%w: nil farm manifest", ErrManifestInvalid)
	}
	classification := &FarmClassification{}
	err := filepath.WalkDir(dataDir, func(farmPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if farmPath == dataDir {
			return nil
		}
		if IsFarmMetadataFile(entry.Name()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dataDir, farmPath)
		if err != nil {
			return err
		}
		if filepath.Dir(rel) == "." && (strings.EqualFold(rel, "plugins.txt") || strings.EqualFold(rel, "loadorder.txt")) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		rel = filepath.ToSlash(rel)
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("lstat farm entry %q: %w", farmPath, err)
		}
		if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			classification.Special = append(classification.Special, rel)
			slog.Warn("special entry in farm left in place", "path", farmPath, "mode", info.Mode())
			return nil
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("%w: no file identity for %q", ErrManifestInvalid, farmPath)
		}
		placed, exists := manifest.Entries[rel]
		matches := exists && placed.Dev == uint64(stat.Dev) && placed.Ino == stat.Ino
		if info.Mode().IsRegular() {
			if matches && placed.Type == "f" {
				classification.Owned = append(classification.Owned, rel)
			} else {
				classification.Output = append(classification.Output, rel)
			}
			return nil
		}
		link, err := os.Readlink(farmPath)
		if err != nil {
			return fmt.Errorf("reading farm symlink %q: %w", farmPath, err)
		}
		if matches && placed.Type == "l" && placed.LinkTarget == link {
			classification.Owned = append(classification.Owned, rel)
		} else {
			classification.UnknownSymlink = append(classification.UnknownSymlink, rel)
			slog.Warn("unknown symlink in farm left in place", "path", farmPath, "link_target", link)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return classification, nil
}

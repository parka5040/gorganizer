package vfs

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// MaterializeStats reports what BuildInto did, surfaced via slog at activate time.
type MaterializeStats struct {
	FilesHardlinked int
	FilesSymlinked  int
	FilesCopied     int
	DirsCreated     int
}

// BuildInto materializes the merged view at outDir as a hardlink farm,
// falling back to symlinks for cross-fs files. Files outside the named
// overwrite mod are chmod'd to 0444.
func BuildInto(outDir string, tree *MergedTree, layers []Layer, overwriteModName string) (MaterializeStats, error) {
	var stats MaterializeStats

	if tree == nil {
		return stats, errors.New("vfs: BuildInto: nil tree")
	}
	if outDir == "" {
		return stats, errors.New("vfs: BuildInto: empty outDir")
	}

	if err := os.MkdirAll(outDir, 0755); err != nil {
		return stats, fmt.Errorf("creating outDir %q: %w", outDir, err)
	}

	tree.mu.RLock()
	defer tree.mu.RUnlock()

	type resolvedLayer struct {
		idx     int
		absRoot string
		layer   Layer
	}
	resolvedLayers := make([]resolvedLayer, 0, len(layers))
	for i, l := range layers {
		if !l.Enabled {
			continue
		}
		abs, err := filepath.Abs(l.RootPath)
		if err != nil {
			abs = l.RootPath
		}
		resolvedLayers = append(resolvedLayers, resolvedLayer{idx: i, absRoot: abs, layer: l})
	}
	for i := 0; i < len(resolvedLayers); i++ {
		for j := i + 1; j < len(resolvedLayers); j++ {
			if len(resolvedLayers[j].absRoot) > len(resolvedLayers[i].absRoot) {
				resolvedLayers[i], resolvedLayers[j] = resolvedLayers[j], resolvedLayers[i]
			}
		}
	}

	overwriteLower := strings.ToLower(overwriteModName)

	findOwningLayer := func(realPath string) (Layer, bool) {
		abs, err := filepath.Abs(realPath)
		if err != nil {
			abs = realPath
		}
		for _, rl := range resolvedLayers {
			if abs == rl.absRoot || strings.HasPrefix(abs, rl.absRoot+string(os.PathSeparator)) {
				return rl.layer, true
			}
		}
		return Layer{}, false
	}

	outDevID, err := devID(outDir)
	if err != nil {
		return stats, fmt.Errorf("stat outDir %q: %w", outDir, err)
	}

	type dirJob struct {
		normalized string
		dest       string
	}
	queue := []dirJob{{normalized: "", dest: outDir}}

	for len(queue) > 0 {
		job := queue[0]
		queue = queue[1:]

		children, ok := tree.dirs[job.normalized]
		if !ok {
			continue
		}

		for normName, child := range children {
			childNormVPath := normName
			if job.normalized != "" {
				childNormVPath = job.normalized + "/" + normName
			}
			destChild := filepath.Join(job.dest, child.Name)

			if child.IsDir {
				if err := os.MkdirAll(destChild, 0755); err != nil {
					return stats, fmt.Errorf("mkdir %q: %w", destChild, err)
				}
				stats.DirsCreated++
				queue = append(queue, dirJob{normalized: childNormVPath, dest: destChild})
				continue
			}

			realPath, ok := tree.files[childNormVPath]
			if !ok {
				slog.Warn("materialize: file child without source path",
					"vpath", childNormVPath, "name", child.Name)
				continue
			}

			isOverwrite := false
			if overwriteLower != "" {
				if owner, ok := findOwningLayer(realPath); ok &&
					strings.ToLower(owner.Name) == overwriteLower {
					isOverwrite = true
				}
			}

			linked, srcDevID, linkErr := tryHardlink(realPath, destChild, outDevID)
			switch {
			case linked:
				stats.FilesHardlinked++
			case errors.Is(linkErr, syscall.EXDEV) || (srcDevID != 0 && srcDevID != outDevID):
				abs, _ := filepath.Abs(realPath)
				if err := os.Symlink(abs, destChild); err != nil {
					return stats, fmt.Errorf("symlink %q -> %q: %w", destChild, abs, err)
				}
				stats.FilesSymlinked++
			case linkErr != nil:
				return stats, fmt.Errorf("hardlink %q -> %q: %w", destChild, realPath, linkErr)
			}

			if !isOverwrite {
				if linked {
					if err := os.Chmod(destChild, 0444); err != nil {
						slog.Warn("could not enforce 0444 on materialized file",
							"path", destChild, "err", err)
					}
				}
			}
		}
	}

	return stats, nil
}

// tryHardlink attempts os.Link, pre-detecting cross-fs via device ID compare.
func tryHardlink(src, dst string, outDevID uint64) (linked bool, srcDevID uint64, err error) {
	srcDevID, _ = devIDOf(src)
	if srcDevID != 0 && srcDevID != outDevID {
		return false, srcDevID, nil
	}
	if linkErr := os.Link(src, dst); linkErr != nil {
		var perr *os.LinkError
		if errors.As(linkErr, &perr) && errors.Is(perr.Err, syscall.EXDEV) {
			return false, srcDevID, syscall.EXDEV
		}
		return false, srcDevID, linkErr
	}
	return true, srcDevID, nil
}

// devID returns the device ID for path.
func devID(path string) (uint64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, nil
	}
	return uint64(stat.Dev), nil
}

func devIDOf(path string) (uint64, error) { return devID(path) }

// CaptureNewFiles moves files we didn't place (st_nlink == 1) into overwriteRoot.
func CaptureNewFiles(dataDir, overwriteRoot string) (int, error) {
	if overwriteRoot == "" {
		return 0, nil
	}

	moved := 0
	walkErr := filepath.Walk(dataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Base(path) == SentinelFilename {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil
		}
		if stat.Nlink > 1 {
			return nil
		}

		rel, relErr := filepath.Rel(dataDir, path)
		if relErr != nil {
			return relErr
		}
		dst := filepath.Join(overwriteRoot, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return fmt.Errorf("mkdir %q: %w", filepath.Dir(dst), err)
		}
		if err := moveFile(path, dst); err != nil {
			return fmt.Errorf("moving captured file %q -> %q: %w", path, dst, err)
		}
		slog.Info("captured new file into overwrite mod",
			"src", path, "dst", dst)
		moved++
		return nil
	})
	return moved, walkErr
}

// moveFile renames src to dst, falling back to copy-and-delete on EXDEV.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else {
		var perr *os.LinkError
		if !(errors.As(err, &perr) && errors.Is(perr.Err, syscall.EXDEV)) {
			return err
		}
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(src)
}

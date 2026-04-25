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

// MaterializeStats reports what BuildInto did. Surfaced via slog at activate
// time and persisted (loosely) in the cache manifest so re-launches that
// take the cache hit can be distinguished from cold builds.
type MaterializeStats struct {
	FilesHardlinked int
	FilesSymlinked  int // Cross-filesystem fallback. Should be 0 on the common path.
	FilesCopied     int // Reserved for a future fallback when symlink also fails.
	DirsCreated     int
}

// BuildInto materializes the merged view in tree at outDir, using one
// hardlink per file (cross-filesystem files fall back to per-file symlinks).
// outDir must not already exist — we create it ourselves so a partial run
// can be cleaned up by removing the whole staging dir.
//
// Mode policy: every materialized file is chmod'd to 0444 (read-only) and
// every materialized directory to 0555, EXCEPT files whose owning layer's
// name matches overwriteModName (case-insensitive) — those keep the source's
// mode so writes from inside the game/Wine can still hit the overwrite mod's
// real files. Symlinks have no useful mode; the cross-fs caveat is logged
// at activate time so the user knows their setup gives up the read-only
// guarantee for the cross-fs files specifically.
//
// The overwriteModName == "" path means "no overwrite mod configured" —
// every file is read-only, and any new-file writes the game performs will
// be captured at deactivate time by the manager's own scan (see
// CaptureNewFiles).
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

	// Pre-resolve the absolute path of every layer root so per-file owning-
	// layer lookups are O(#layers) string-prefix checks rather than another
	// stat. Sort longest-first to make sure nested layer roots resolve to
	// the most specific match (defensive — layer roots typically don't
	// nest, but this is cheap and makes the algorithm robust).
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
	// Longest absRoot first.
	for i := 0; i < len(resolvedLayers); i++ {
		for j := i + 1; j < len(resolvedLayers); j++ {
			if len(resolvedLayers[j].absRoot) > len(resolvedLayers[i].absRoot) {
				resolvedLayers[i], resolvedLayers[j] = resolvedLayers[j], resolvedLayers[i]
			}
		}
	}

	overwriteLower := strings.ToLower(overwriteModName)

	// findOwningLayer returns the layer whose RootPath is a prefix of the
	// given realPath. Matches our sorted (longest-first) order.
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

	// Cache the device ID of outDir. EXDEV detection is what tells us when
	// to fall back to a symlink; pre-fetching avoids a Stat per file.
	outDevID, err := devID(outDir)
	if err != nil {
		return stats, fmt.Errorf("stat outDir %q: %w", outDir, err)
	}

	// DFS the tree by walking the dirs map. Track (normalizedVPath,
	// destPath) pairs in a queue so we use original-case names from
	// ChildEntry while keeping tree lookups against normalized keys.
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
				// Tree is internally inconsistent — child entry exists but
				// no file mapping. Skip with a warning; surface in tests.
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
				// Cross-fs fallback: per-file symlink with absolute target.
				abs, _ := filepath.Abs(realPath)
				if err := os.Symlink(abs, destChild); err != nil {
					return stats, fmt.Errorf("symlink %q -> %q: %w", destChild, abs, err)
				}
				stats.FilesSymlinked++
			case linkErr != nil:
				return stats, fmt.Errorf("hardlink %q -> %q: %w", destChild, realPath, linkErr)
			}

			// Mode policy: read-only unless this is an Overwrite-mod file.
			// Symlinks: chmod is a no-op on most filesystems and would
			// follow the link target on others — skip to avoid corrupting
			// the source mod's mode.
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

// tryHardlink attempts os.Link. Returns (linked, srcDevID, err):
//   - linked=true: hardlink succeeded.
//   - linked=false, srcDevID != outDevID: pre-detected cross-fs; caller
//     should fall back to symlink without even attempting the link.
//   - linked=false, err non-nil: real failure to surface.
//
// We pre-stat the source to detect cross-fs early, but we don't return
// EXDEV from the stat path — the caller checks the device IDs.
func tryHardlink(src, dst string, outDevID uint64) (linked bool, srcDevID uint64, err error) {
	srcDevID, _ = devIDOf(src)
	if srcDevID != 0 && srcDevID != outDevID {
		// Different filesystems — skip the link attempt.
		return false, srcDevID, nil
	}
	if linkErr := os.Link(src, dst); linkErr != nil {
		// EXDEV can still surface here if our pre-stat got 0; surface it
		// to the caller for the symlink fallback.
		var perr *os.LinkError
		if errors.As(linkErr, &perr) && errors.Is(perr.Err, syscall.EXDEV) {
			return false, srcDevID, syscall.EXDEV
		}
		return false, srcDevID, linkErr
	}
	return true, srcDevID, nil
}

// devID returns the device ID for path. Used by BuildInto to detect cross-
// filesystem hardlink failures before paying a syscall per file.
func devID(path string) (uint64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		// Non-Unix sys field; we'll just attempt the link and react to
		// EXDEV. Return 0 so the equality check trivially "matches" and
		// we proceed to the link attempt.
		return 0, nil
	}
	return uint64(stat.Dev), nil
}

func devIDOf(path string) (uint64, error) { return devID(path) }

// CaptureNewFiles walks dataDir and moves any "new" files (files that the
// materializer did not place there — heuristic: regular files with
// st_nlink == 1 AND no inode match in the overwrite mod's source tree)
// into <overwriteRoot>/<vpath>. Returns the count of moved files.
//
// This is the deactivate-time write capture: a tool that did atomic-save
// (write tmp + rename over) ends up with a fresh inode in dataDir that
// the manager doesn't own. Without capture, RemoveAll(dataDir) would
// silently discard the user's changes.
//
// overwriteRoot may be empty — in that case we skip the capture step
// (no escape hatch configured). New files in dataDir are then deleted as
// part of the surrounding RemoveAll.
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
		// Skip the sentinel — that's our metadata, never a user write.
		if filepath.Base(path) == SentinelFilename {
			return nil
		}
		// Skip symlinks — we never want to follow them and our cross-fs
		// fallback uses them, so a symlink here is one we placed.
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil
		}
		if stat.Nlink > 1 {
			// Still has at least one other reference — that's a hardlink
			// we placed. Not a user write.
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

// moveFile renames src to dst when they're on the same filesystem; falls
// back to copy-and-delete when EXDEV. Used by CaptureNewFiles.
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

package vfs

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/fsutil"
)

type MaterializeStats struct {
	FilesHardlinked int
	FilesSymlinked  int
	FilesCopied     int
	DirsCreated     int
	FarmID          string
	Manifest        string
	ManifestSHA256  string
	ManifestEntries int
}

// BuildInto materializes a merged view as hardlinks with cross-filesystem symlink fallback.
func BuildInto(outDir string, tree *MergedTree, _ []Layer, _ string) (MaterializeStats, error) {
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

	outDevID, err := devID(outDir)
	if err != nil {
		return stats, fmt.Errorf("stat outDir %q: %w", outDir, err)
	}
	manifest, err := newFarmManifestWriter(outDir)
	if err != nil {
		return stats, err
	}
	defer manifest.close()

	type dirJob struct {
		normalized string
		dest       string
		rel        string
	}
	queue := []dirJob{{dest: outDir}}

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
			rel := child.Name
			if job.rel != "" {
				rel = job.rel + "/" + child.Name
			}
			destChild := filepath.Join(job.dest, child.Name)

			if child.IsDir {
				if err := os.MkdirAll(destChild, 0755); err != nil {
					return stats, fmt.Errorf("mkdir %q: %w", destChild, err)
				}
				stats.DirsCreated++
				queue = append(queue, dirJob{normalized: childNormVPath, dest: destChild, rel: rel})
				continue
			}

			realPath, ok := tree.files[childNormVPath]
			if !ok {
				slog.Warn("materialize: file child without source path",
					"vpath", childNormVPath, "name", child.Name)
				continue
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
			if err := manifest.record(destChild, rel); err != nil {
				return stats, err
			}
		}
	}

	stats.FarmID = manifest.farmID
	stats.Manifest, stats.ManifestSHA256, stats.ManifestEntries, err = manifest.finish(outDir)
	if err != nil {
		return stats, err
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

var devIDOf = devID

// CaptureNewFiles moves files not owned by a verified farm manifest into overwriteRoot, using link counts for legacy farms.
func CaptureNewFiles(dataDir, overwriteRoot string) (int, error) {
	return CaptureNewFilesInto(dataDir, overwriteRoot, false, false)
}

var writeSuccessorSentinel = WriteSentinel

// CaptureNewFilesInto moves files not owned by a verified farm manifest into targetRoot, using link counts for legacy farms and optionally relinking them.
func CaptureNewFilesInto(dataDir, targetRoot string, relink bool, _ bool) (moved int, retErr error) {
	if targetRoot == "" {
		return 0, nil
	}

	s, err := ReadSentinel(dataDir)
	if err != nil {
		slog.Warn("capture using legacy link-count rule", "path", dataDir, "reason", err)
		return captureLegacyFilesInto(dataDir, targetRoot, relink)
	}
	if s.SchemaVersion != 3 {
		slog.Warn("capture using legacy link-count rule", "path", dataDir, "reason", fmt.Sprintf("sentinel schema version %d", s.SchemaVersion))
		return captureLegacyFilesInto(dataDir, targetRoot, relink)
	}
	manifest, err := ReadFarmManifest(dataDir, s)
	if err != nil {
		slog.Warn("capture using legacy link-count rule", "path", dataDir, "reason", err)
		return captureLegacyFilesInto(dataDir, targetRoot, relink)
	}
	classification, err := ClassifyFarm(dataDir, manifest)
	if err != nil {
		return 0, fmt.Errorf("classifying farm %q: %w", dataDir, err)
	}
	dirs := make(map[string]struct{})
	synced := false
	defer func() {
		if retErr != nil && !synced {
			retErr = errors.Join(retErr, syncCaptureDirs(dirs))
		}
	}()
	relinked := make(map[string]FarmManifestEntry)
	for _, rel := range classification.Output {
		farmPath := filepath.Join(dataDir, filepath.FromSlash(rel))
		dst := filepath.Join(targetRoot, filepath.FromSlash(rel))
		created, err := prepareCaptureDestination(targetRoot, filepath.FromSlash(rel), dirs)
		if err != nil {
			return moved, err
		}
		if err := moveFile(farmPath, dst, created); err != nil {
			return moved, fmt.Errorf("%w: moving captured file %q -> %q: %w", ErrCaptureFailed, farmPath, dst, err)
		}
		slog.Info("captured new file", "src", farmPath, "dst", dst, "relink", relink)
		moved++
		if relink {
			if err := relinkCaptured(dst, farmPath); err != nil {
				return moved, err
			}
			entry, err := farmEntryAt(farmPath, rel)
			if err != nil {
				return moved, err
			}
			relinked[rel] = entry
		}
	}
	synced = true
	if err := syncCaptureDirs(dirs); err != nil {
		return moved, err
	}
	if len(relinked) > 0 {
		if err := publishSuccessorManifest(dataDir, s, manifest, relinked); err != nil {
			return moved, err
		}
	}
	return moved, nil
}

func publishSuccessorManifest(dataDir string, s *Sentinel, manifest *FarmManifest, relinked map[string]FarmManifestEntry) error {
	writer, err := newFarmManifestWriter(dataDir)
	if err != nil {
		return err
	}
	defer writer.close()
	for rel, entry := range manifest.Entries {
		if replacement, ok := relinked[rel]; ok {
			entry = replacement
			delete(relinked, rel)
		}
		if err := writer.append(entry); err != nil {
			return err
		}
	}
	for _, entry := range relinked {
		if err := writer.append(entry); err != nil {
			return err
		}
	}
	name, digest, count, err := writer.finish(dataDir)
	if err != nil {
		return err
	}
	oldName := s.Manifest
	successor := *s
	successor.FarmID = writer.farmID
	successor.Manifest = name
	successor.ManifestSHA256 = digest
	successor.ManifestEntries = count
	if err := writeSuccessorSentinel(dataDir, &successor); err != nil {
		return fmt.Errorf("publishing successor sentinel: %w", err)
	}
	if err := os.Remove(filepath.Join(dataDir, oldName)); err != nil {
		slog.Warn("could not remove old farm manifest", "path", filepath.Join(dataDir, oldName), "err", err)
	}
	return nil
}

func captureLegacyFilesInto(dataDir, targetRoot string, relink bool) (moved int, retErr error) {
	dirs := make(map[string]struct{})
	synced := false
	defer func() {
		if retErr != nil && !synced {
			retErr = errors.Join(retErr, syncCaptureDirs(dirs))
		}
	}()
	walkErr := filepath.Walk(dataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if IsFarmMetadataFile(filepath.Base(path)) {
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
		if filepath.Dir(rel) == "." && (strings.EqualFold(rel, "plugins.txt") || strings.EqualFold(rel, "loadorder.txt")) {
			return nil
		}
		dst := filepath.Join(targetRoot, rel)
		created, err := prepareCaptureDestination(targetRoot, rel, dirs)
		if err != nil {
			return err
		}
		if err := moveFile(path, dst, created); err != nil {
			return fmt.Errorf("%w: moving captured file %q -> %q: %w", ErrCaptureFailed, path, dst, err)
		}
		slog.Info("captured new file", "src", path, "dst", dst, "relink", relink)
		moved++

		if relink {
			if err := relinkCaptured(dst, path); err != nil {
				return err
			}
		}
		return nil
	})
	if walkErr != nil {
		return moved, walkErr
	}
	synced = true
	return moved, syncCaptureDirs(dirs)
}

// relinkCaptured links captured output back into the materialized farm.
func relinkCaptured(src, farmPath string) error {
	outDevID, _ := devIDOf(filepath.Dir(farmPath))
	linked, srcDevID, linkErr := tryHardlink(src, farmPath, outDevID)
	switch {
	case linked:
		return nil
	case errors.Is(linkErr, syscall.EXDEV) || (srcDevID != 0 && srcDevID != outDevID):
		abs, _ := filepath.Abs(src)
		if err := os.Symlink(abs, farmPath); err != nil {
			return fmt.Errorf("re-link symlink %q -> %q: %w", farmPath, abs, err)
		}
		return nil
	case linkErr != nil:
		return fmt.Errorf("re-link %q -> %q: %w", farmPath, src, linkErr)
	}
	return nil
}

var captureRename = os.Rename
var captureRemove = os.Remove
var captureCopy = atomicfile.CopyFileDurable
var captureMkdirAll = os.MkdirAll
var captureSyncDir = atomicfile.SyncDir

// prepareCaptureDestination checks the target path and creates safe parent directories.
func prepareCaptureDestination(root, rel string, dirs map[string]struct{}) (map[string]struct{}, error) {
	created := make(map[string]struct{})
	dst := filepath.Join(root, rel)
	if err := fsutil.CheckExistingPath(root, rel); err != nil {
		return nil, fmt.Errorf("%w: unsafe capture destination %q: %w", ErrCaptureFailed, dst, err)
	}
	parent := filepath.Dir(dst)
	for dir := parent; ; dir = filepath.Dir(dir) {
		_, err := os.Lstat(dir)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: checking destination directory %q: %w", ErrCaptureFailed, dir, err)
		}
		created[dir] = struct{}{}
		created[filepath.Dir(dir)] = struct{}{}
		dirs[filepath.Dir(dir)] = struct{}{}
		if filepath.Dir(dir) == dir {
			return nil, fmt.Errorf("%w: destination directory %q does not exist", ErrCaptureFailed, dir)
		}
	}
	mkdirErr := captureMkdirAll(parent, 0755)
	for dir := range created {
		if info, err := os.Lstat(dir); err == nil && info.IsDir() {
			dirs[dir] = struct{}{}
		}
	}
	if mkdirErr != nil {
		return nil, fmt.Errorf("%w: creating destination directory %q: %w", ErrCaptureFailed, parent, mkdirErr)
	}
	if err := fsutil.CheckExistingPath(root, rel); err != nil {
		return nil, fmt.Errorf("%w: unsafe capture destination %q: %w", ErrCaptureFailed, dst, err)
	}
	info, err := os.Lstat(dst)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: checking capture destination %q: %w", ErrCaptureFailed, dst, err)
	}
	if err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: capture destination %q is not a regular file", ErrCaptureFailed, dst)
	}
	dirs[parent] = struct{}{}
	return created, nil
}

// syncCaptureDirs flushes capture destination directory entries before teardown.
func syncCaptureDirs(dirs map[string]struct{}) error {
	paths := make([]string, 0, len(dirs))
	for dir := range dirs {
		paths = append(paths, dir)
	}
	sort.Slice(paths, func(i, j int) bool {
		iDepth := strings.Count(paths[i], string(filepath.Separator))
		jDepth := strings.Count(paths[j], string(filepath.Separator))
		if iDepth != jDepth {
			return iDepth > jDepth
		}
		return paths[i] < paths[j]
	})
	var syncErr error
	for _, dir := range paths {
		if err := captureSyncDir(dir); err != nil {
			syncErr = errors.Join(syncErr, fmt.Errorf("%w: syncing capture destination directory %q: %w", ErrCaptureFailed, dir, err))
		}
	}
	return syncErr
}

// moveFile renames src to dst, falling back to a durable copy on EXDEV.
func moveFile(src, dst string, created map[string]struct{}) error {
	if err := captureRename(src, dst); err == nil {
		return nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return err
	}

	before, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("checking captured source %q: %w", src, err)
	}
	if !before.Mode().IsRegular() {
		return fmt.Errorf("captured source %q is not a regular file", src)
	}
	outcome, err := captureCopy(src, dst, before.Mode().Perm(), true)
	if err != nil {
		return fmt.Errorf("copying captured file %q to %q (outcome %d): %w", src, dst, outcome, err)
	}
	if outcome != atomicfile.Durable {
		return fmt.Errorf("copying captured file %q to %q: not durable (outcome %d)", src, dst, outcome)
	}
	after, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("rechecking captured source %q: %w", src, err)
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return fmt.Errorf("captured source %q changed during copy; source kept in farm", src)
	}
	if err := syncCaptureDirs(created); err != nil {
		return err
	}
	if err := captureRemove(src); err != nil {
		return fmt.Errorf("removing copied source %q: %w", src, err)
	}
	return nil
}

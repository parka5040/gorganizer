package vfs

import (
	"path/filepath"
	"sync"
)

// ChildEntry represents a single child in a directory listing.
// Name preserves the original case (taken from the highest-priority layer
// that contributed the entry) so the materialized tree mirrors what mods
// shipped with — important under Wine, which case-folds at lookup time
// but expects the on-disk casing to match what tools wrote.
type ChildEntry struct {
	Name  string
	IsDir bool
}

// MergedTree is a precomputed merged view of all layers (base game + enabled
// mods). The materializer walks it to produce the on-disk hardlink farm.
// Read locks are taken for tree queries so concurrent stats / reads are
// cheap; Rebuild grabs the write lock when the user toggles mods.
type MergedTree struct {
	mu     sync.RWMutex
	dirs   map[string]map[string]ChildEntry // normalized vpath -> (normalized child name -> ChildEntry)
	files  map[string]string                // normalized vpath -> real file path on disk
	layers []Layer
}

// NewMergedTree creates an empty MergedTree.
func NewMergedTree() *MergedTree {
	return &MergedTree{
		dirs:  make(map[string]map[string]ChildEntry),
		files: make(map[string]string),
	}
}

// Build walks all enabled layers in priority order and populates the dirs
// and files maps. Layer 0 is lowest priority; higher indices overwrite.
func (t *MergedTree) Build(layers []Layer) error {
	t.layers = layers

	// Ensure root directory entry exists.
	t.dirs[""] = make(map[string]ChildEntry)

	return WalkLayers(layers, func(vpath, realPath string, _ int, _ Layer, isDir bool) error {
		parentVPath, childName := splitVPath(vpath)
		originalName := filepath.Base(realPath)
		normalizedChild := NormalizeName(childName)

		// Ensure parent directory map exists.
		if _, ok := t.dirs[parentVPath]; !ok {
			t.dirs[parentVPath] = make(map[string]ChildEntry)
		}

		if isDir {
			// Directories merge children across layers.
			t.dirs[parentVPath][normalizedChild] = ChildEntry{
				Name:  originalName,
				IsDir: true,
			}
			// Ensure this directory's own children map exists.
			if _, ok := t.dirs[vpath]; !ok {
				t.dirs[vpath] = make(map[string]ChildEntry)
			}
		} else {
			// Files: higher priority overwrites lower.
			t.files[vpath] = realPath
			t.dirs[parentVPath][normalizedChild] = ChildEntry{
				Name:  originalName,
				IsDir: false,
			}
		}
		return nil
	})
}

// Rebuild acquires the write lock, clears maps, and rebuilds from the given layers.
// Called when the user changes the mod list (never during gameplay).
func (t *MergedTree) Rebuild(layers []Layer) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.dirs = make(map[string]map[string]ChildEntry)
	t.files = make(map[string]string)
	return t.Build(layers)
}

// LookupFile returns the real path for a file at the given virtual path.
// Callers must hold at least an RLock.
func (t *MergedTree) LookupFile(vpath string) (string, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	realPath, ok := t.files[NormalizePath(vpath)]
	return realPath, ok
}

// Children returns all children of a directory at the given virtual path.
func (t *MergedTree) Children(vpath string) (map[string]ChildEntry, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	children, ok := t.dirs[NormalizePath(vpath)]
	return children, ok
}

// IsDir returns true if the given virtual path is a known directory.
func (t *MergedTree) IsDir(vpath string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	_, ok := t.dirs[NormalizePath(vpath)]
	return ok
}

// Stats returns the total file count and directory count for VFSStatus reporting.
func (t *MergedTree) Stats() (fileCount int, dirCount int) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return len(t.files), len(t.dirs)
}

// Layers returns the current layer list.
func (t *MergedTree) Layers() []Layer {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.layers
}

// splitVPath splits a normalized virtual path into parent and child components.
// "textures/sky/sky.dds" -> "textures/sky", "sky.dds"
// "sky.dds" -> "", "sky.dds"
func splitVPath(vpath string) (parent, child string) {
	for i := len(vpath) - 1; i >= 0; i-- {
		if vpath[i] == '/' {
			return vpath[:i], vpath[i+1:]
		}
	}
	return "", vpath
}

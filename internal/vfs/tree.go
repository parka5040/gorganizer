package vfs

import (
	"path/filepath"
	"sync"
)

type ChildEntry struct {
	Name  string
	IsDir bool
}

type MergedTree struct {
	mu     sync.RWMutex
	dirs   map[string]map[string]ChildEntry
	files  map[string]string
	layers []Layer
}

func NewMergedTree() *MergedTree {
	return &MergedTree{
		dirs:  make(map[string]map[string]ChildEntry),
		files: make(map[string]string),
	}
}

// Build walks enabled layers in priority order; layer 0 is lowest priority.
func (t *MergedTree) Build(layers []Layer) error {
	t.layers = layers

	t.dirs[""] = make(map[string]ChildEntry)

	return WalkLayers(layers, func(vpath, realPath string, _ int, _ Layer, isDir bool) error {
		parentVPath, childName := splitVPath(vpath)
		originalName := filepath.Base(realPath)
		normalizedChild := NormalizeName(childName)

		if _, ok := t.dirs[parentVPath]; !ok {
			t.dirs[parentVPath] = make(map[string]ChildEntry)
		}

		if isDir {
			if _, exists := t.dirs[parentVPath][normalizedChild]; !exists {
				t.dirs[parentVPath][normalizedChild] = ChildEntry{
					Name:  originalName,
					IsDir: true,
				}
			}
			if _, ok := t.dirs[vpath]; !ok {
				t.dirs[vpath] = make(map[string]ChildEntry)
			}
		} else {
			t.files[vpath] = realPath
			if _, exists := t.dirs[parentVPath][normalizedChild]; !exists {
				t.dirs[parentVPath][normalizedChild] = ChildEntry{
					Name:  originalName,
					IsDir: false,
				}
			}
		}
		return nil
	})
}

// Rebuild acquires the write lock, clears maps, and rebuilds from the given layers.
func (t *MergedTree) Rebuild(layers []Layer) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.dirs = make(map[string]map[string]ChildEntry)
	t.files = make(map[string]string)
	return t.Build(layers)
}

// LookupFile returns the real path for a file at the given virtual path.
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

func (t *MergedTree) IsDir(vpath string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	_, ok := t.dirs[NormalizePath(vpath)]
	return ok
}

func (t *MergedTree) Stats() (fileCount int, dirCount int) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return len(t.files), len(t.dirs)
}

func (t *MergedTree) Layers() []Layer {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.layers
}

// splitVPath splits a normalized virtual path into parent and child.
func splitVPath(vpath string) (parent, child string) {
	for i := len(vpath) - 1; i >= 0; i-- {
		if vpath[i] == '/' {
			return vpath[:i], vpath[i+1:]
		}
	}
	return "", vpath
}

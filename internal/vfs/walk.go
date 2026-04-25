package vfs

import (
	"fmt"
	"os"
	"path/filepath"
)

// Layer represents a single filesystem layer in the VFS overlay.
// Layer 0 is the base game (lowest priority). Higher indices = higher priority.
type Layer struct {
	Name     string // mod name or "__base__"
	RootPath string // absolute path to layer root on disk
	Enabled  bool
}

// LayerVisitor is called for each file discovered during layer walking.
// vpath is the normalized virtual path. realPath is the absolute path on disk.
// layerIdx is the index in the layers slice. isDir indicates if the entry is a directory.
type LayerVisitor func(vpath, realPath string, layerIdx int, layer Layer, isDir bool) error

// WalkLayers walks all enabled layers in order and calls visitor for each
// file and directory found. Uses os.ReadDir per directory for efficiency.
// This is the single shared walk function used by both MergedTree.Build
// and conflict.BuildConflictMap (DRY).
func WalkLayers(layers []Layer, visitor LayerVisitor) error {
	for i, layer := range layers {
		if !layer.Enabled {
			continue
		}
		if err := walkDir(layer.RootPath, "", i, layer, visitor); err != nil {
			return fmt.Errorf("walking layer %q: %w", layer.Name, err)
		}
	}
	return nil
}

// modRootInternalNames are gorganizer-managed sidecar files that live at
// the root of every mod folder. They must NOT participate in the merged
// view — leaking them into the game's Data dir is harmless for vanilla
// Bethesda engines (they ignore unknown files) but pollutes the install,
// confuses tools that scan Data/ (LOOT, xEdit), and breaks the contract
// that Data/ contains only game-readable assets.
var modRootInternalNames = map[string]struct{}{
	"metadata.yaml": {},
}

// walkDir recursively reads a directory and invokes visitor for each entry.
func walkDir(rootPath, relPath string, layerIdx int, layer Layer, visitor LayerVisitor) error {
	absDir := rootPath
	if relPath != "" {
		absDir = filepath.Join(rootPath, relPath)
	}

	entries, err := os.ReadDir(absDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading directory %q: %w", absDir, err)
	}

	for _, entry := range entries {
		// Skip gorganizer-internal sidecars at the mod-folder root only.
		// We deliberately don't filter inside subdirectories — a mod that
		// genuinely ships a "metadata.yaml" under (e.g.) Data/Scripts/ is
		// rare but legitimate, and only a layer-root match can be ours.
		if relPath == "" && layer.Name != "__base__" {
			if _, internal := modRootInternalNames[entry.Name()]; internal {
				continue
			}
		}

		childRel := entry.Name()
		if relPath != "" {
			childRel = relPath + "/" + entry.Name()
		}
		vpath := NormalizePath(childRel)
		realPath := filepath.Join(rootPath, childRel)

		if err := visitor(vpath, realPath, layerIdx, layer, entry.IsDir()); err != nil {
			return err
		}

		if entry.IsDir() {
			if err := walkDir(rootPath, childRel, layerIdx, layer, visitor); err != nil {
				return err
			}
		}
	}
	return nil
}

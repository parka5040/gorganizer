package vfs

import (
	"fmt"
	"os"
	"path/filepath"
)

type Layer struct {
	Name     string
	RootPath string
	Enabled  bool
}

type LayerVisitor func(vpath, realPath string, layerIdx int, layer Layer, isDir bool) error

// WalkLayers walks enabled layers in order, invoking visitor for each entry.
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

var modRootInternalNames = map[string]struct{}{
	"metadata.yaml":    {},
	RootContentDirName: {},
}

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
		if relPath == "" && layer.Name != "__base__" {
			if _, internal := modRootInternalNames[NormalizeName(entry.Name())]; internal {
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

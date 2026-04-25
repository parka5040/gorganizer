package mod

import (
	"github.com/parka/gorganizer/internal/vfs"
)

// FileConflict describes a file path where multiple mods provide the same file.
type FileConflict struct {
	VirtualPath string   // normalized, e.g., "textures/sky/sky.dds"
	Winner      string   // mod that provides this file (highest priority)
	Losers      []string // overridden mod names, lowest priority first
}

// ConflictMap holds all file conflicts across the enabled mod layers.
type ConflictMap struct {
	Conflicts map[string]FileConflict // normalized vpath -> conflict
}

// BuildConflictMap walks all layers and records which mods conflict on which
// files. Uses the shared WalkLayers function (DRY with MergedTree.Build).
func BuildConflictMap(layers []vfs.Layer) (*ConflictMap, error) {
	// Track which mod provides each file path as we walk layers in priority order.
	owners := make(map[string]string) // normalized vpath -> mod name
	cm := &ConflictMap{
		Conflicts: make(map[string]FileConflict),
	}

	err := vfs.WalkLayers(layers, func(vpath, _ string, _ int, layer vfs.Layer, isDir bool) error {
		if isDir {
			return nil
		}

		prev, exists := owners[vpath]
		if exists && prev != layer.Name {
			// Conflict: this file was already provided by a lower-priority mod.
			if conflict, ok := cm.Conflicts[vpath]; ok {
				// Existing conflict: current winner becomes a loser, new mod wins.
				conflict.Losers = append(conflict.Losers, conflict.Winner)
				conflict.Winner = layer.Name
				cm.Conflicts[vpath] = conflict
			} else {
				cm.Conflicts[vpath] = FileConflict{
					VirtualPath: vpath,
					Winner:      layer.Name,
					Losers:      []string{prev},
				}
			}
		}
		owners[vpath] = layer.Name
		return nil
	})
	if err != nil {
		return nil, err
	}

	return cm, nil
}

// ForMod returns all conflicts involving a specific mod (as winner or loser).
func (cm *ConflictMap) ForMod(modName string) []FileConflict {
	var result []FileConflict
	for _, c := range cm.Conflicts {
		if c.Winner == modName {
			result = append(result, c)
			continue
		}
		for _, loser := range c.Losers {
			if loser == modName {
				result = append(result, c)
				break
			}
		}
	}
	return result
}

// WinnerCount returns how many files a mod wins.
func (cm *ConflictMap) WinnerCount(modName string) int {
	count := 0
	for _, c := range cm.Conflicts {
		if c.Winner == modName {
			count++
		}
	}
	return count
}

// LoserCount returns how many files a mod loses.
func (cm *ConflictMap) LoserCount(modName string) int {
	count := 0
	for _, c := range cm.Conflicts {
		for _, loser := range c.Losers {
			if loser == modName {
				count++
				break
			}
		}
	}
	return count
}

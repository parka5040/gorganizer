package mod

import (
	"github.com/parka/gorganizer/internal/vfs"
)

// FileConflict describes a virtual path where multiple mods provide the same file.
type FileConflict struct {
	VirtualPath string
	Winner      string
	Losers      []string
}

// ConflictMap holds all file conflicts across the enabled mod layers.
type ConflictMap struct {
	Conflicts map[string]FileConflict
}

// BuildConflictMap walks layers and records per-file winners and losers.
func BuildConflictMap(layers []vfs.Layer) (*ConflictMap, error) {
	owners := make(map[string]string)
	cm := &ConflictMap{
		Conflicts: make(map[string]FileConflict),
	}

	err := vfs.WalkLayers(layers, func(vpath, _ string, _ int, layer vfs.Layer, isDir bool) error {
		if isDir {
			return nil
		}

		prev, exists := owners[vpath]
		if exists && prev != layer.Name {
			if conflict, ok := cm.Conflicts[vpath]; ok {
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

// ForMod returns all conflicts involving modName as winner or loser.
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

func (cm *ConflictMap) WinnerCount(modName string) int {
	count := 0
	for _, c := range cm.Conflicts {
		if c.Winner == modName {
			count++
		}
	}
	return count
}

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

// Package plugins discovers ESP/ESM/ESL files and writes plugins.txt / loadorder.txt.
package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Spec describes where and how to write plugins.txt / loadorder.txt for a Bethesda game.
type Spec struct {
	AppDataSubdir     string
	PluginsFileName   string
	LoadOrderFileName string
	DLCListFileName   string
	StarPrefix        bool
	ImplicitMasters   []string
	CanonicalDLCOrder []string
}

var specs = map[string]Spec{
	"oblivion": {
		AppDataSubdir:   "Oblivion",
		PluginsFileName: "plugins.txt",
		StarPrefix:      false,
		ImplicitMasters: []string{"Oblivion.esm"},
	},
	"skyrim": {
		AppDataSubdir:   "Skyrim",
		PluginsFileName: "plugins.txt",
		StarPrefix:      false,
		ImplicitMasters: []string{"Skyrim.esm", "Update.esm"},
	},
	"skyrimse": {
		AppDataSubdir:     "Skyrim Special Edition",
		PluginsFileName:   "plugins.txt",
		LoadOrderFileName: "loadorder.txt",
		StarPrefix:        true,
		ImplicitMasters: []string{
			"Skyrim.esm", "Update.esm", "Dawnguard.esm",
			"HearthFires.esm", "Dragonborn.esm",
		},
	},
	"fallout3": {
		AppDataSubdir:     "Fallout3",
		PluginsFileName:   "plugins.txt",
		DLCListFileName:   "DLCList.txt",
		StarPrefix:        false,
		ImplicitMasters:   []string{"Fallout3.esm"},
		CanonicalDLCOrder: []string{
			"Anchorage.esm",
			"ThePitt.esm",
			"BrokenSteel.esm",
			"PointLookout.esm",
			"Zeta.esm",
		},
	},
	"falloutnv": {
		AppDataSubdir:   "FalloutNV",
		PluginsFileName: "plugins.txt",
		DLCListFileName: "NVDLCList.txt",
		StarPrefix:      false,
		ImplicitMasters: []string{"FalloutNV.esm"},
		CanonicalDLCOrder: []string{
			"DeadMoney.esm",
			"HonestHearts.esm",
			"OldWorldBlues.esm",
			"LonesomeRoad.esm",
			"GunRunnersArsenal.esm",
			"ClassicPack.esm",
			"MercenaryPack.esm",
			"TribalPack.esm",
			"CaravanPack.esm",
		},
	},
	"ttw": {
		AppDataSubdir:   "FalloutNV",
		PluginsFileName: "plugins.txt",
		DLCListFileName: "NVDLCList.txt",
		StarPrefix:      false,
		ImplicitMasters: []string{"FalloutNV.esm"},
		CanonicalDLCOrder: []string{
			"DeadMoney.esm",
			"HonestHearts.esm",
			"OldWorldBlues.esm",
			"LonesomeRoad.esm",
			"GunRunnersArsenal.esm",
			"ClassicPack.esm",
			"MercenaryPack.esm",
			"TribalPack.esm",
			"CaravanPack.esm",
			"Fallout3.esm",
			"Anchorage.esm",
			"ThePitt.esm",
			"BrokenSteel.esm",
			"PointLookout.esm",
			"Zeta.esm",
			"TaleOfTwoWastelands.esm",
		},
	},
	"fallout4": {
		AppDataSubdir:     "Fallout4",
		PluginsFileName:   "plugins.txt",
		LoadOrderFileName: "loadorder.txt",
		StarPrefix:        true,
		ImplicitMasters: []string{
			"Fallout4.esm", "DLCRobot.esm", "DLCworkshop01.esm",
			"DLCCoast.esm", "DLCworkshop02.esm", "DLCworkshop03.esm",
			"DLCNukaWorld.esm",
		},
	},
	"starfield": {
		AppDataSubdir:   "Starfield",
		PluginsFileName: "Plugins.txt",
		StarPrefix:      true,
		ImplicitMasters: []string{
			"Starfield.esm", "Constellation.esm", "OldMars.esm",
			"SFBGS003.esm", "SFBGS006.esm", "SFBGS007.esm", "SFBGS008.esm",
		},
	},
}

// SpecFor returns the plugins.txt spec for a gameID; (zero, false) if none.
func SpecFor(gameID string) (Spec, bool) {
	s, ok := specs[gameID]
	return s, ok
}

// Plugin is one ESP/ESM/ESL discovered in a mod or the base Data dir.
type Plugin struct {
	Filename string
	Ext      string
	Source   string
	FromMod  string
	Enabled  bool
}

// TypeOrder ranks .esm < .esl < .esp.
func (p Plugin) TypeOrder() int {
	switch strings.ToLower(p.Ext) {
	case ".esm":
		return 0
	case ".esl":
		return 1
	case ".esp":
		return 2
	default:
		return 3
	}
}

// ApplyUserOrder reorders the slice in place so that plugins listed in
// `userOrder` (case-insensitive filenames) appear in that order, while
// plugins absent from `userOrder` keep their relative natural order at
// the end. Canonical DLC ESMs in spec.CanonicalDLCOrder are pinned to
// their canonical positions even if the user order omits them or places
// them elsewhere — the engine refuses to load otherwise. Empty userOrder
// is a no-op.
func ApplyUserOrder(plugins []Plugin, spec Spec, userOrder []string) {
	if len(userOrder) == 0 || len(plugins) == 0 {
		return
	}
	canonical := make(map[string]struct{}, len(spec.CanonicalDLCOrder))
	for _, name := range spec.CanonicalDLCOrder {
		canonical[strings.ToLower(name)] = struct{}{}
	}
	rank := make(map[string]int, len(userOrder))
	for i, name := range userOrder {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			continue
		}
		if _, isCanonical := canonical[key]; isCanonical {
			continue
		}
		if _, dup := rank[key]; dup {
			continue
		}
		rank[key] = i
	}
	if len(rank) == 0 {
		return
	}

	type indexed struct {
		p     Plugin
		stamp int
	}
	indexed_ := make([]indexed, len(plugins))
	for i, p := range plugins {
		indexed_[i] = indexed{p: p, stamp: i}
	}
	notRanked := len(userOrder) + 1
	sort.SliceStable(indexed_, func(i, j int) bool {
		ai, aOk := rank[strings.ToLower(indexed_[i].p.Filename)]
		bi, bOk := rank[strings.ToLower(indexed_[j].p.Filename)]
		if !aOk && !bOk {
			return indexed_[i].stamp < indexed_[j].stamp
		}
		if !aOk {
			ai = notRanked
		}
		if !bOk {
			bi = notRanked
		}
		return ai < bi
	})
	// Reapply canonical DLC pinning AFTER user order so canonical ESMs
	// are guaranteed to be at the top of the ESM band regardless of what
	// the user dragged around.
	for i := range indexed_ {
		plugins[i] = indexed_[i].p
	}
	ApplyCanonicalOrder(plugins, spec)
}

// ApplyCanonicalOrder sorts ESMs in spec.CanonicalDLCOrder first, then other ESMs.
func ApplyCanonicalOrder(plugins []Plugin, spec Spec) {
	if len(spec.CanonicalDLCOrder) == 0 {
		return
	}
	dlcRank := make(map[string]int, len(spec.CanonicalDLCOrder))
	for i, name := range spec.CanonicalDLCOrder {
		dlcRank[strings.ToLower(name)] = i
	}
	sort.SliceStable(plugins, func(i, j int) bool {
		a, b := plugins[i], plugins[j]
		ai := a.TypeOrder()
		bi := b.TypeOrder()
		if ai != bi {
			return ai < bi
		}
		if ai == 0 {
			ar, aOk := dlcRank[strings.ToLower(a.Filename)]
			br, bOk := dlcRank[strings.ToLower(b.Filename)]
			switch {
			case aOk && bOk:
				return ar < br
			case aOk:
				return true
			case bOk:
				return false
			}
		}
		return false
	})
}

// DiscoverPlugins walks base Data plus enabled mods, returning the combined ordered list.
func DiscoverPlugins(baseDataDir string, enabledMods []ModEntry) ([]Plugin, error) {
	seen := map[string]int{}
	var out []Plugin

	scan := func(dir, modName string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if ext != ".esp" && ext != ".esm" && ext != ".esl" {
				continue
			}
			key := strings.ToLower(e.Name())
			p := Plugin{
				Filename: e.Name(),
				Ext:      ext,
				Source:   dir,
				FromMod:  modName,
				Enabled:  true,
			}
			if i, ok := seen[key]; ok {
				out[i] = p
				continue
			}
			seen[key] = len(out)
			out = append(out, p)
		}
		return nil
	}

	if err := scan(baseDataDir, ""); err != nil {
		return nil, err
	}
	for _, m := range enabledMods {
		if err := scan(m.Path, m.Name); err != nil {
			return nil, err
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		return out[i].TypeOrder() < out[j].TypeOrder()
	})
	return out, nil
}

// ModEntry is the minimal mod info DiscoverPlugins needs.
type ModEntry struct {
	Name string
	Path string
}

// Write emits plugins.txt (plus loadorder.txt / DLCList.txt when defined) atomically into destDir.
func Write(spec Spec, destDir string, plugins []Plugin) error {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("creating %s: %w", destDir, err)
	}

	implicit := map[string]bool{}
	for _, m := range spec.ImplicitMasters {
		implicit[strings.ToLower(m)] = true
	}
	visible := plugins[:0:0]
	for _, p := range plugins {
		if implicit[strings.ToLower(p.Filename)] {
			continue
		}
		visible = append(visible, p)
	}

	ApplyCanonicalOrder(visible, spec)

	var b strings.Builder
	for _, p := range visible {
		if spec.StarPrefix {
			if p.Enabled {
				b.WriteString("*")
			}
			b.WriteString(p.Filename)
		} else {
			if !p.Enabled {
				continue
			}
			b.WriteString(p.Filename)
		}
		b.WriteString("\r\n")
	}
	pluginsPath := filepath.Join(destDir, spec.PluginsFileName)
	if err := writeAtomic(pluginsPath, []byte(b.String())); err != nil {
		return err
	}

	if spec.LoadOrderFileName != "" {
		var lb strings.Builder
		for _, p := range visible {
			lb.WriteString(p.Filename)
			lb.WriteString("\r\n")
		}
		loPath := filepath.Join(destDir, spec.LoadOrderFileName)
		if err := writeAtomic(loPath, []byte(lb.String())); err != nil {
			return err
		}
	}

	if spec.DLCListFileName != "" {
		var db strings.Builder
		for _, p := range visible {
			if !p.Enabled {
				continue
			}
			if strings.ToLower(p.Ext) != ".esm" {
				continue
			}
			db.WriteString(p.Filename)
			db.WriteString("\r\n")
		}
		dlcPath := filepath.Join(destDir, spec.DLCListFileName)
		if err := writeAtomic(dlcPath, []byte(db.String())); err != nil {
			return err
		}
	}
	return nil
}

// writeAtomic writes to a temp sibling and renames into place.
func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

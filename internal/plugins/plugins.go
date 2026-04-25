// Package plugins is responsible for discovering ESP/ESM/ESL files belonging
// to a profile's enabled mods and writing the plugins.txt / loadorder.txt
// files the Bethesda engine actually reads at launch time.
//
// The FUSE mount gives the game a merged Data/ view, but the engine only
// *loads* plugins listed in the per-user plugins.txt. Without this writer,
// enabled mods show up in Data/ but never get activated.
package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Spec describes where and how to write plugins.txt / loadorder.txt for a
// particular Bethesda game.
type Spec struct {
	// AppDataSubdir is the folder name beneath AppData/Local/ (inside the
	// Proton pfx) where the game reads plugins.txt.
	AppDataSubdir string
	// PluginsFileName is "plugins.txt" for every game except Starfield which
	// uses "Plugins.txt" (capital P). Bethesda's engines are case-insensitive
	// about the read, but we match the shipped casing so `ls` is readable.
	PluginsFileName string
	// LoadOrderFileName is the loadorder.txt companion file, or "" when the
	// game doesn't use one. FNV/FO3/Oblivion/Skyrim LE use plugins.txt only;
	// newer engines (SSE/FO4/Starfield) also read loadorder.txt via LOOT
	// conventions but the base engine only requires plugins.txt.
	LoadOrderFileName string
	// DLCListFileName is the auxiliary DLC selection file the legacy
	// FNV/FO3 launcher writes (NVDLCList.txt / DLCList.txt). When the
	// engine launches without this file the official DLCs may fail to
	// load even when listed in plugins.txt — gorganizer mirrors the .esm
	// entries here so the game-launcher and xNVSE paths both load DLCs.
	DLCListFileName string
	// StarPrefix indicates the `*`-prefix convention (Skyrim SE / Fallout 4
	// / Starfield): enabled plugins have `*` prepended, disabled plugins
	// are listed without it. When false, presence alone means enabled and
	// disabled plugins are simply omitted.
	StarPrefix bool
	// ImplicitMasters are plugins the engine auto-loads regardless of what
	// plugins.txt says (e.g. Skyrim.esm for Skyrim). We skip writing these
	// — duplicating them is harmless on most engines but confuses some
	// tools (LOOT, xEdit) that expect the Bethesda convention.
	ImplicitMasters []string
	// CanonicalDLCOrder lists the official DLC ESMs in the order the
	// retail launcher writes them. Discovered .esm files matching this
	// list are emitted in this exact order ahead of any other ESMs;
	// non-DLC ESMs and ESPs follow in modlist order. Without this, an
	// alphabetical sort produces real load-order violations — for FNV
	// LonesomeRoad.esm precedes OldWorldBlues.esm but references its
	// forms — which in turn produce the "MASTERFILE: Could not find
	// referenced object" cascade you see in falloutnv_error.log.
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
		AppDataSubdir:   "Fallout3",
		PluginsFileName: "plugins.txt",
		DLCListFileName: "DLCList.txt",
		StarPrefix:      false,
		ImplicitMasters: []string{"Fallout3.esm"},
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
		// Canonical FNV DLC order (release date). LonesomeRoad references
		// content from DeadMoney/HonestHearts/OldWorldBlues, so the four
		// story DLCs must come before it. Pre-order packs follow.
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

// SpecFor returns the plugins.txt spec for a gameID, or (zero, false) if
// the game has no plugins.txt convention (e.g. Morrowind — uses ini sections).
func SpecFor(gameID string) (Spec, bool) {
	s, ok := specs[gameID]
	return s, ok
}

// Plugin is one ESP/ESM/ESL file discovered in a mod or the base Data dir.
type Plugin struct {
	Filename string // e.g. "SkyUI.esp"
	Ext      string // ".esp" | ".esm" | ".esl"
	Source   string // absolute directory the plugin lives in — mod dir, or base Data
	FromMod  string // mod name that contributed this plugin, "" for base game
	Enabled  bool   // checkbox state (mirrors mod enabled for now)
}

// TypeOrder returns a relative rank so .esm < .esl < .esp sort naturally
// (the order the engine loads them in).
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

// DiscoverPlugins walks the base Data directory plus every enabled mod's
// folder to produce the combined plugin list. Enabled mods override base
// files by filename (case-insensitive). Ordering within the returned slice
// is: (1) ESMs then ESLs then ESPs; (2) within each group, modlist order
// (later entries win later in load order).
func DiscoverPlugins(baseDataDir string, enabledMods []ModEntry) ([]Plugin, error) {
	seen := map[string]int{} // lowercase-filename → index in `out`
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
				// Later mod wins — overwrite the earlier entry in place so
				// load-order position stays where it was.
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

// ModEntry is what DiscoverPlugins needs to know about each enabled mod.
// Kept deliberately minimal so we don't import the mod package here.
type ModEntry struct {
	Name string
	Path string
}

// Write writes plugins.txt (and loadorder.txt / DLCList.txt when the spec
// defines them) into `destDir`, using the spec's format rules. `destDir`
// is the game's AppData/Local/{AppDataSubdir}/ path inside the Proton
// prefix. Missing directories are created; existing files are overwritten
// atomically.
//
// Format note: the file is plain CRLF-terminated lines, ASCII, no header
// comment. Earlier revisions wrote a "# managed by Gorganizer" line at
// the top, which the FNV/FO3/Oblivion engines do NOT treat as a comment
// — they parse it as a plugin filename, fail to find it, and (depending
// on the patch level) either silently skip the rest of the file or
// register an empty load order. Symptom on the user side: every plugin
// shows as "checked" in the list but no forms register and BSAs don't
// auto-load. Match the bare-bones format the official launcher writes.
func Write(spec Spec, destDir string, plugins []Plugin) error {
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("creating %s: %w", destDir, err)
	}

	// Filter out implicit masters — Bethesda engines auto-load them.
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

	// Re-sort: official DLC ESMs in their canonical release-date order
	// first, then any other ESMs (alphabetical fallback), then ESLs,
	// then ESPs in modlist order. The earlier ext-only sort placed
	// LonesomeRoad before OldWorldBlues even though LonesomeRoad
	// declares OldWorldBlues as a master — Bethesda engines try to
	// pre-resolve masters on load, but a wrong ordering still produces
	// "MASTERFILE: Could not find referenced object" errors that can
	// silently invalidate downstream plugins.
	if len(spec.CanonicalDLCOrder) > 0 {
		dlcRank := make(map[string]int, len(spec.CanonicalDLCOrder))
		for i, name := range spec.CanonicalDLCOrder {
			dlcRank[strings.ToLower(name)] = i
		}
		sort.SliceStable(visible, func(i, j int) bool {
			a, b := visible[i], visible[j]
			ai := a.TypeOrder()
			bi := b.TypeOrder()
			if ai != bi {
				return ai < bi
			}
			// Within the .esm bucket, canonical DLCs first (in canonical
			// order), everything else after (preserving stable order).
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

	// plugins.txt — bare list, CRLF, no comment header.
	var b strings.Builder
	for _, p := range visible {
		if spec.StarPrefix {
			if p.Enabled {
				b.WriteString("*")
			}
			b.WriteString(p.Filename)
		} else {
			// Legacy engines: listing == enabling. Disabled plugins are
			// omitted entirely.
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

	// loadorder.txt (SSE/FO4 only — plain list with no `*`).
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

	// NVDLCList.txt / DLCList.txt (FNV / FO3) — the legacy launcher's
	// "Data Files" UI writes this, and the engine reads it as the
	// authoritative DLC selection list. When the file is absent or
	// empty (a fresh Steam install that never ran the launcher), the
	// official DLC ESMs may fail to load even when listed in
	// plugins.txt — and missing-master errors then cascade to silently
	// dropping every dependent plugin from the load. Mirror the
	// active .esm entries here so xNVSE-only sessions get a usable
	// DLC selection without round-tripping through the launcher UI.
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

// writeAtomic writes to a temp sibling and renames — avoids leaving a
// truncated plugins.txt if the daemon is killed mid-write.
func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

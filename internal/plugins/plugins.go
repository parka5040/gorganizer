package plugins

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/gamedef"
)

type Spec = gamedef.PluginSpec

// SpecFor returns the plugins.txt spec for a gameID; (zero, false) if none.
func SpecFor(gameID string) (Spec, bool) {
	g, ok := gamedef.ByID(gameID)
	if !ok || g.Plugins == nil {
		return Spec{}, false
	}
	return *g.Plugins, true
}

type Plugin struct {
	Filename string
	Ext      string
	Source   string
	FromMod  string
	Enabled  bool
}

// ApplyActivationState applies case-insensitive profile activation while preserving pinned plugins.
func ApplyActivationState(list []Plugin, spec Spec, state map[string]bool) {
	implicit := make(map[string]struct{}, len(spec.ImplicitMasters))
	for _, name := range spec.ImplicitMasters {
		implicit[strings.ToLower(name)] = struct{}{}
	}
	defaultDisabled := make(map[string]struct{}, len(spec.DefaultDisabled))
	for _, name := range spec.DefaultDisabled {
		defaultDisabled[strings.ToLower(name)] = struct{}{}
	}
	for i := range list {
		key := strings.ToLower(list[i].Filename)
		_, pinned := implicit[key]
		if !pinned {
			for _, prefix := range spec.PinnedPrefixes {
				if strings.HasPrefix(key, strings.ToLower(prefix)) {
					pinned = true
					break
				}
			}
		}
		if pinned {
			list[i].Enabled = true
			continue
		}
		if enabled, ok := state[key]; ok {
			list[i].Enabled = enabled
		} else if _, disabled := defaultDisabled[key]; disabled {
			list[i].Enabled = false
		}
	}
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
		if _, isCanonical := canonical[key]; isCanonical && !spec.PreserveOrder {
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
	for i := range indexed_ {
		plugins[i] = indexed_[i].p
	}
	ApplyCanonicalOrder(plugins, spec)
}

// ApplyCanonicalOrder sorts ESMs in spec.CanonicalDLCOrder first, then other ESMs.
func ApplyCanonicalOrder(plugins []Plugin, spec Spec) {
	if spec.PreserveOrder || len(spec.CanonicalDLCOrder) == 0 {
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

// ApplyDefaultOrder applies pinned-master and canonical DLC order without type sorting.
func ApplyDefaultOrder(list []Plugin, spec Spec) {
	rank := make(map[string]int, len(spec.ImplicitMasters)+len(spec.CanonicalDLCOrder))
	next := 0
	for _, names := range [][]string{spec.ImplicitMasters, spec.CanonicalDLCOrder} {
		for _, name := range names {
			key := strings.ToLower(name)
			if _, exists := rank[key]; exists {
				continue
			}
			rank[key] = next
			next++
		}
	}
	if len(rank) == 0 {
		return
	}
	sort.SliceStable(list, func(i, j int) bool {
		ir, iKnown := rank[strings.ToLower(list[i].Filename)]
		jr, jKnown := rank[strings.ToLower(list[j].Filename)]
		switch {
		case iKnown && jKnown:
			return ir < jr
		case iKnown:
			return true
		case jKnown:
			return false
		default:
			return false
		}
	})
}

// DiscoverPlugins walks base Data plus enabled mods, returning the combined ordered list.
func DiscoverPlugins(baseDataDir string, enabledMods []ModEntry, spec Spec) ([]Plugin, error) {
	seen := map[string]int{}
	var out []Plugin
	supported := make(map[string]struct{}, len(spec.SupportedExts))
	for _, ext := range spec.SupportedExts {
		ext = strings.ToLower(strings.TrimSpace(ext))
		if ext != "" {
			supported[ext] = struct{}{}
		}
	}

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
			if len(supported) > 0 {
				if _, ok := supported[ext]; !ok {
					continue
				}
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

	if !spec.PreserveOrder {
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].TypeOrder() < out[j].TypeOrder()
		})
	}
	return out, nil
}

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
		if implicit[strings.ToLower(p.Filename)] && !spec.PreserveOrder {
			continue
		}
		visible = append(visible, p)
	}
	if spec.StateLocation == gamedef.PluginStateGameRootIni {
		return writeMorrowindINI(filepath.Join(destDir, spec.PluginsFileName), visible)
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
			if !p.Enabled && spec.DisabledPrefix == "" {
				continue
			}
			if !p.Enabled {
				b.WriteString(spec.DisabledPrefix)
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

type EngineLoadoutEntry struct {
	Filename string
	Enabled  bool
}

// ReadEngineLoadout parses engine-facing plugin state and treats missing files as empty.
func ReadEngineLoadout(spec Spec, dir string) ([]EngineLoadoutEntry, error) {
	path := filepath.Join(dir, spec.PluginsFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading engine plugin state %s: %w", path, err)
	}
	if spec.StateLocation == gamedef.PluginStateGameRootIni {
		return readMorrowindINI(data, spec), nil
	}

	supported := make(map[string]struct{}, len(spec.SupportedExts))
	for _, ext := range spec.SupportedExts {
		supported[strings.ToLower(ext)] = struct{}{}
	}
	var out []EngineLoadoutEntry
	seen := make(map[string]struct{})
	for _, raw := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
		if line == "" {
			continue
		}
		enabled := true
		if spec.StarPrefix {
			enabled = strings.HasPrefix(line, "*")
			line = strings.TrimSpace(strings.TrimPrefix(line, "*"))
		} else if spec.DisabledPrefix != "" && strings.HasPrefix(line, spec.DisabledPrefix) {
			enabled = false
			line = strings.TrimSpace(strings.TrimPrefix(line, spec.DisabledPrefix))
		} else if strings.HasPrefix(line, "#") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(line))
		if ext != ".esm" && ext != ".esp" && ext != ".esl" {
			continue
		}
		if len(supported) > 0 {
			if _, ok := supported[ext]; !ok {
				continue
			}
		}
		key := strings.ToLower(line)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, EngineLoadoutEntry{Filename: line, Enabled: enabled})
	}
	return out, nil
}

func readMorrowindINI(data []byte, spec Spec) []EngineLoadoutEntry {
	inGameFiles := false
	seen := make(map[string]bool)
	out := make([]EngineLoadoutEntry, 0)
	for _, raw := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inGameFiles = strings.EqualFold(line, "[Game Files]")
			continue
		}
		if !inGameFiles {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(key)), "gamefile") {
			continue
		}
		name := strings.TrimSpace(value)
		if name == "" || !isSupportedEnginePlugin(spec, name) || seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		out = append(out, EngineLoadoutEntry{Filename: name, Enabled: true})
	}
	return out
}

func writeMorrowindINI(path string, plugins []Plugin) error {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines)+len(plugins)+2)
	inGameFiles := false
	foundSection := false
	inserted := false
	insert := func() {
		if inserted {
			return
		}
		for _, plugin := range plugins {
			if plugin.Enabled {
				out = append(out, fmt.Sprintf("GameFile%d=%s", lenGameFileLines(out), plugin.Filename))
			}
		}
		inserted = true
	}
	for _, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			if inGameFiles {
				insert()
			}
			inGameFiles = strings.EqualFold(trimmed, "[Game Files]")
			if inGameFiles {
				foundSection = true
			}
			out = append(out, raw)
			continue
		}
		if inGameFiles {
			key, _, ok := strings.Cut(trimmed, "=")
			if ok && strings.HasPrefix(strings.ToLower(strings.TrimSpace(key)), "gamefile") {
				continue
			}
		}
		out = append(out, raw)
	}
	if inGameFiles {
		insert()
	}
	if !foundSection {
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		out = append(out, "[Game Files]")
		insert()
	}
	return writeAtomic(path, []byte(strings.Join(out, "\r\n")))
}

func lenGameFileLines(lines []string) int {
	count := 0
	for _, line := range lines {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), "gamefile") {
			count++
		}
	}
	return count
}

func isSupportedEnginePlugin(spec Spec, name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	if ext != ".esm" && ext != ".esp" && ext != ".esl" {
		return false
	}
	if len(spec.SupportedExts) == 0 {
		return true
	}
	for _, supported := range spec.SupportedExts {
		if strings.EqualFold(ext, supported) {
			return true
		}
	}
	return false
}

// writeAtomic writes to a temp sibling and renames into place.
func writeAtomic(path string, data []byte) error {
	return atomicfile.WriteFile(path, data, 0644)
}

package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// DepKind classifies a single dependency issue; values mirror the gRPC enum.
type DepKind int

const (
	DepOK               DepKind = 0
	DepMasterAbsent     DepKind = 1
	DepMasterDisabled   DepKind = 2
	DepMasterOutOfOrder DepKind = 3
	DepSoftMissing      DepKind = 4
)

// SoftDepRef identifies a soft dependency target the user can act on.
type SoftDepRef struct {
	ModName string
	ModID   int
	URL     string
}

// DepIssue is one entry on a plugin's issues list.
type DepIssue struct {
	Kind    DepKind
	Master  string
	SoftRef *SoftDepRef
}

// PluginStatus is the analyzer's verdict for one enabled plugin.
type PluginStatus struct {
	Plugin      Plugin
	IsLight     bool
	HardIssues  []DepIssue
	SoftIssues  []DepIssue
	SoftPending bool
}

// AnalyzeHardDeps produces a PluginStatus per ordered plugin, populating HardIssues from MAST entries.
func AnalyzeHardDeps(
	ctx context.Context,
	cache *HeaderCache,
	ordered []Plugin,
	allModFolders []ModEntry,
	spec Spec,
	extraMasters []string,
) []PluginStatus {
	type slot struct {
		idx      int
		implicit bool
	}
	active := make(map[string]slot, len(ordered)+len(spec.ImplicitMasters)+len(extraMasters))
	for _, m := range spec.ImplicitMasters {
		active[strings.ToLower(m)] = slot{idx: -1, implicit: true}
	}
	for _, m := range extraMasters {
		active[strings.ToLower(m)] = slot{idx: -1, implicit: true}
	}
	for i, p := range ordered {
		if existing, ok := active[strings.ToLower(p.Filename)]; ok && existing.implicit {
			continue
		}
		active[strings.ToLower(p.Filename)] = slot{idx: i}
	}

	disabled := make(map[string]bool)
	for _, m := range allModFolders {
		entries, err := readPluginNames(m.Path)
		if err != nil {
			continue
		}
		for _, name := range entries {
			lower := strings.ToLower(name)
			if _, isActive := active[lower]; isActive {
				continue
			}
			disabled[lower] = true
		}
	}

	out := make([]PluginStatus, 0, len(ordered))
	for i, p := range ordered {
		ps := PluginStatus{Plugin: p}
		path := filepath.Join(p.Source, p.Filename)
		hdr, err := cache.Get(ctx, path)
		if err != nil || hdr == nil {
			out = append(out, ps)
			continue
		}
		ps.IsLight = hdr.IsLight

		for _, masterName := range hdr.Masters {
			if masterName == "" {
				continue
			}
			lower := strings.ToLower(masterName)
			s, isActive := active[lower]
			switch {
			case !isActive && disabled[lower]:
				ps.HardIssues = append(ps.HardIssues, DepIssue{
					Kind:   DepMasterDisabled,
					Master: masterName,
				})
			case !isActive:
				ps.HardIssues = append(ps.HardIssues, DepIssue{
					Kind:   DepMasterAbsent,
					Master: masterName,
				})
			case !s.implicit && s.idx >= i:
				ps.HardIssues = append(ps.HardIssues, DepIssue{
					Kind:   DepMasterOutOfOrder,
					Master: masterName,
				})
			}
		}
		out = append(out, ps)
	}
	return out
}

// readPluginNames returns the .esp/.esm/.esl filenames directly under dir.
func readPluginNames(dir string) ([]string, error) {
	d, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	entries, err := d.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, name := range entries {
		ext := strings.ToLower(filepath.Ext(name))
		if ext == ".esp" || ext == ".esm" || ext == ".esl" {
			names = append(names, name)
		}
	}
	return names, nil
}

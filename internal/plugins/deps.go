package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// DepKind classifies a single dependency issue against an enabled plugin.
// The values mirror the gRPC enum exactly so they can be sent over the wire
// as int32s without translation.
type DepKind int

const (
	DepOK                  DepKind = 0
	DepMasterAbsent        DepKind = 1 // red    — master file not found in any mod folder
	DepMasterDisabled      DepKind = 2 // orange — master found, but its mod is disabled
	DepMasterOutOfOrder    DepKind = 3 // red    — master loads after the dependent plugin
	DepSoftMissing         DepKind = 4 // yellow — Nexus-declared soft dep is not installed
	// DepSoftPending is not an issue, just a transient marker on PluginStatus.
)

// SoftDepRef identifies a soft dependency target the user can act on
// (download / enable). Filled in by the soft-dep fetcher.
type SoftDepRef struct {
	ModName string
	ModID   int
	URL     string
}

// DepIssue is one entry on a plugin's issues list. Master is set for the
// master-* kinds; SoftRef is set for DepSoftMissing.
type DepIssue struct {
	Kind    DepKind
	Master  string
	SoftRef *SoftDepRef
}

// PluginStatus is the analyzer's verdict for one enabled plugin.
type PluginStatus struct {
	Plugin      Plugin
	IsLight     bool       // ESL flag from header — overrides extension when .esp + 0x200
	HardIssues  []DepIssue // master-related issues — computed locally and instantly
	SoftIssues  []DepIssue // populated asynchronously by softdeps
	SoftPending bool       // true while the Nexus check is still in flight
}

// AnalyzeHardDeps walks ordered (load-order from DiscoverPlugins) and
// produces a PluginStatus for each entry, populating HardIssues from header
// MAST entries.
//
// allModFolders enumerates every mod folder regardless of whether it's
// enabled in the active profile — needed to distinguish absent (file
// nowhere) from disabled (file present in a disabled mod). Pass the same
// list you'd get from listing every folder under ModsDir(gameID).
//
// extraMasters lets profile-level features (e.g. TTW) declare additional
// always-present masters on top of spec.ImplicitMasters. Pass nil for the
// default case.
func AnalyzeHardDeps(
	ctx context.Context,
	cache *HeaderCache,
	ordered []Plugin,
	allModFolders []ModEntry,
	spec Spec,
	extraMasters []string,
) []PluginStatus {
	// active = case-insensitive set of every plugin filename the engine
	// will load (implicit + every discovered plugin). Also tracks the
	// load-order position so we can detect MASTER_OUT_OF_ORDER.
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
		// Don't clobber implicit slots — implicit always wins on order check.
		if existing, ok := active[strings.ToLower(p.Filename)]; ok && existing.implicit {
			continue
		}
		active[strings.ToLower(p.Filename)] = slot{idx: i}
	}

	// disabled = filenames present in *some* mod folder but not in active.
	// We surface these as "master is one click away" so the user can fix
	// without hunting through Nexus.
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
		// Parse the header — failures don't block the row, just skip dep checks.
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
// Mirrors plugins.go's discovery filter without descending into subdirs —
// Bethesda engines only load plugins from the Data root.
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

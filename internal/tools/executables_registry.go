package tools

import (
	"os"
	"path/filepath"
	"strings"
)

// KnownToolStem describes a well-known external modding tool for autodetection.
// It is data-only: adding a tool needs no new per-game code (plan §9).
type KnownToolStem struct {
	Title              string
	Basenames          []string // case-insensitive exe basenames to match
	NeedsVFSMounted    bool
	CaptureOutputToMod string // "" => Overwrite
	ExtraRWScratch     bool   // needs a large writable scratch dir (LOD generators)
}

// KnownExternalTools is the 2026 Bethesda-tool matrix used by DetectExecutables.
var KnownExternalTools = []KnownToolStem{
	{Title: "SSEEdit / xEdit", Basenames: []string{"SSEEdit.exe", "SSEEditx64.exe", "xEdit.exe", "TES5Edit.exe", "FNVEdit.exe", "FO3Edit.exe", "FO4Edit.exe", "SF1Edit.exe"}, NeedsVFSMounted: true},
	{Title: "LOOT", Basenames: []string{"LOOT.exe"}, NeedsVFSMounted: true},
	{Title: "DynDOLOD", Basenames: []string{"DynDOLODx64.exe"}, NeedsVFSMounted: true, CaptureOutputToMod: "DynDOLOD Output", ExtraRWScratch: true},
	{Title: "xLODGen", Basenames: []string{"xLODGenx64.exe", "xLODGen.exe"}, NeedsVFSMounted: true, CaptureOutputToMod: "xLODGen Output", ExtraRWScratch: true},
	{Title: "Nemesis", Basenames: []string{"Nemesis Unlimited Behavior Engine.exe"}, NeedsVFSMounted: true, CaptureOutputToMod: "Nemesis Output"},
	{Title: "Pandora", Basenames: []string{"Pandora Behaviour Engine+.exe"}, NeedsVFSMounted: true, CaptureOutputToMod: "Pandora Output"},
	{Title: "BodySlide", Basenames: []string{"BodySlide x64.exe"}, NeedsVFSMounted: true, CaptureOutputToMod: "BodySlide Output"},
	{Title: "Wrye Bash", Basenames: []string{"Wrye Bash.exe"}, NeedsVFSMounted: true},
	{Title: "Synthesis", Basenames: []string{"Synthesis.exe"}, NeedsVFSMounted: true, CaptureOutputToMod: "Synthesis Output"},
}

// DetectedTool is a ready-to-register candidate produced by DetectExecutables.
type DetectedTool struct {
	Title              string
	ExePath            string
	NeedsVFSMounted    bool
	CaptureOutputToMod string
	ExtraRWScratch     bool
}

// DetectExecutables scans the given roots (typically Data.orig plus each enabled
// mod folder) for known tool executables and returns candidate entries. Matching
// is by case-insensitive basename; the first path found for a given exe wins.
func DetectExecutables(roots []string) []DetectedTool {
	stemByLower := make(map[string]KnownToolStem)
	for _, s := range KnownExternalTools {
		for _, b := range s.Basenames {
			stemByLower[strings.ToLower(b)] = s
		}
	}

	var found []DetectedTool
	seenExe := make(map[string]bool)
	for _, root := range roots {
		if root == "" {
			continue
		}
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			s, ok := stemByLower[strings.ToLower(info.Name())]
			if !ok {
				return nil
			}
			if seenExe[path] {
				return nil
			}
			seenExe[path] = true
			found = append(found, DetectedTool{
				Title:              s.Title,
				ExePath:            path,
				NeedsVFSMounted:    s.NeedsVFSMounted,
				CaptureOutputToMod: s.CaptureOutputToMod,
				ExtraRWScratch:     s.ExtraRWScratch,
			})
			return nil
		})
	}
	return found
}

package tools

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/parka/gorganizer/internal/config"
)

type RunnerKind string

const (
	RunnerProton RunnerKind = "proton"
	RunnerNative RunnerKind = "native"
	RunnerJava   RunnerKind = "java"
)

type OutputPolicy string

const (
	OutputNone                OutputPolicy = "none"
	OutputReadOnly            OutputPolicy = "read_only"
	OutputProfileSync         OutputPolicy = "profile_sync"
	OutputScratchImport       OutputPolicy = "scratch_import"
	OutputSelectedCopyUp      OutputPolicy = "selected_copy_up"
	OutputNamedMod            OutputPolicy = "named_output_mod"
	OutputExclusiveSourceEdit OutputPolicy = "exclusive_source_edit"
)

type ExternalToolDefinition struct {
	ID                 string
	Title              string
	Basenames          []string
	Games              []string
	Runner             RunnerKind
	PrefixAppID        int
	NeedsVFSMounted    bool
	OutputPolicy       OutputPolicy
	DefaultOutputMod   string
	ExtraRWScratch     bool
	DefaultArgsByGame  map[string][]string
	PathContains       []string
	RuntimePackages    []string
	WorkingDirGameRoot bool
}

var allBethesdaGames = []string{
	"morrowind", "oblivion", "skyrim", "skyrimse", "fallout3",
	"falloutnv", "ttw", "fallout4", "starfield", "oblivionremastered",
}

var skyrimGames = []string{"skyrim", "skyrimse"}

var ExternalToolCatalog = []ExternalToolDefinition{
	{
		ID: "skyrim-launcher", Title: "Skyrim Launcher", Games: skyrimGames, Runner: RunnerProton,
		Basenames:       []string{"SkyrimLauncher.exe", "SkyrimSELauncher.exe"},
		NeedsVFSMounted: true, OutputPolicy: OutputProfileSync, WorkingDirGameRoot: true,
	},
	{
		ID: "loot", Title: "LOOT", Basenames: []string{"LOOT.exe"}, Games: allBethesdaGames,
		Runner: RunnerProton, NeedsVFSMounted: true, OutputPolicy: OutputProfileSync,
		RuntimePackages: []string{"vcrun2022"}, PathContains: []string{"gorganizer/tools/loot"},
	},
	{
		ID: "xedit", Title: "SSEEdit / xEdit", Games: allBethesdaGames, Runner: RunnerProton,
		Basenames: []string{
			"xEdit.exe", "xEdit64.exe", "SSEEdit.exe", "SSEEdit64.exe", "SSEEditx64.exe",
			"TES3Edit.exe", "TES3Edit64.exe", "TES4Edit.exe", "TES4Edit64.exe",
			"TES5Edit.exe", "TES5Edit64.exe", "TES5Editx64.exe", "FO3Edit.exe", "FO3Edit64.exe",
			"FNVEdit.exe", "FNVEdit64.exe", "FO4Edit.exe", "FO4Edit64.exe", "SF1Edit.exe", "SF1Edit64.exe",
			"SSEEditQuickAutoClean.exe", "SSEEditQuickClean.exe", "SSEEditQuickShowConflicts.exe",
			"TES5EditQuickAutoClean.exe", "FO3EditQuickAutoClean.exe", "FNVEditQuickAutoClean.exe",
			"FO4EditQuickAutoClean.exe", "SF1EditQuickAutoClean.exe",
		},
		NeedsVFSMounted: true, OutputPolicy: OutputSelectedCopyUp, DefaultOutputMod: "xEdit Output",
		DefaultArgsByGame: map[string][]string{"skyrim": {"-tes5"}, "skyrimse": {"-sse"}},
	},
	{
		ID: "creation-kit", Title: "Creation Kit", Games: []string{"skyrimse"}, Runner: RunnerProton,
		Basenames: []string{"CreationKit.exe"}, PrefixAppID: 1946180, NeedsVFSMounted: true,
		OutputPolicy: OutputSelectedCopyUp, DefaultOutputMod: "Creation Kit Output", WorkingDirGameRoot: true,
	},
	{
		ID: "papyrus-compiler", Title: "Papyrus Compiler", Games: skyrimGames, Runner: RunnerProton,
		Basenames: []string{"PapyrusCompiler.exe", "Papyrus Compiler.exe"}, PrefixAppID: 1946180,
		NeedsVFSMounted: true, OutputPolicy: OutputNamedMod, DefaultOutputMod: "Papyrus Output",
	},
	{
		ID: "archive", Title: "Bethesda Archive", Games: skyrimGames, Runner: RunnerProton,
		Basenames: []string{"Archive.exe"}, PrefixAppID: 1946180, NeedsVFSMounted: true,
		OutputPolicy: OutputScratchImport, DefaultOutputMod: "Archive Output",
	},
	{
		ID: "pandora", Title: "Pandora Behaviour Engine", Games: skyrimGames, Runner: RunnerProton,
		Basenames:       []string{"Pandora Behaviour Engine+.exe", "Pandora Behaviour Engine.exe", "Pandora.exe"},
		NeedsVFSMounted: true, OutputPolicy: OutputNamedMod, DefaultOutputMod: "Pandora Output",
		DefaultArgsByGame: map[string][]string{
			"skyrim":   {"--tesv", "%WIN_GAME_DIR%", "-o", "%WIN_OUTPUT_DIR%"},
			"skyrimse": {"--tesv", "%WIN_GAME_DIR%", "-o", "%WIN_OUTPUT_DIR%"},
		},
	},
	{
		ID: "pandora-native", Title: "Pandora Behaviour Engine (native)", Games: skyrimGames, Runner: RunnerNative,
		Basenames:       []string{"Pandora Behaviour Engine+", "Pandora Behaviour Engine", "pandora"},
		NeedsVFSMounted: true, OutputPolicy: OutputNamedMod, DefaultOutputMod: "Pandora Output",
		DefaultArgsByGame: map[string][]string{
			"skyrim":   {"--tesv", "%GAME_DIR%", "-o", "%OUTPUT_DIR%"},
			"skyrimse": {"--tesv", "%GAME_DIR%", "-o", "%OUTPUT_DIR%"},
		},
	},
	{
		ID: "nemesis", Title: "Nemesis", Games: skyrimGames, Runner: RunnerProton,
		Basenames: []string{"Nemesis Unlimited Behavior Engine.exe"}, PathContains: []string{"Nemesis_Engine"},
		NeedsVFSMounted: true, OutputPolicy: OutputNamedMod, DefaultOutputMod: "Nemesis Output",
	},
	{
		ID: "fnis", Title: "FNIS", Games: skyrimGames, Runner: RunnerProton,
		Basenames: []string{"GenerateFNISforUsers.exe"}, PathContains: []string{"GenerateFNIS_for_Users"},
		NeedsVFSMounted: true, OutputPolicy: OutputNamedMod, DefaultOutputMod: "FNIS Output",
	},
	{
		ID: "bodyslide", Title: "BodySlide", Games: skyrimGames, Runner: RunnerProton,
		Basenames:       []string{"BodySlide x64.exe", "BodySlide.exe", "OutfitStudio x64.exe", "OutfitStudio.exe"},
		NeedsVFSMounted: true, OutputPolicy: OutputNamedMod, DefaultOutputMod: "BodySlide Output",
	},
	{
		ID: "dyndolod", Title: "DynDOLOD", Games: skyrimGames, Runner: RunnerProton,
		Basenames:       []string{"DynDOLODx64.exe", "DynDOLOD.exe", "TexGenx64.exe", "TexGen.exe"},
		NeedsVFSMounted: true, OutputPolicy: OutputScratchImport, DefaultOutputMod: "DynDOLOD Output", ExtraRWScratch: true,
		DefaultArgsByGame: map[string][]string{
			"skyrim":   {"-tes5", "-o:%WIN_OUTPUT_DIR%", "-t:%WIN_SCRATCH_DIR%"},
			"skyrimse": {"-sse", "-o:%WIN_OUTPUT_DIR%", "-t:%WIN_SCRATCH_DIR%"},
		},
	},
	{
		ID: "xlodgen", Title: "xLODGen", Games: allBethesdaGames, Runner: RunnerProton,
		Basenames:       []string{"xLODGenx64.exe", "xLODGen.exe", "SSELODGenx64.exe", "TES5LODGenx64.exe"},
		NeedsVFSMounted: true, OutputPolicy: OutputScratchImport, DefaultOutputMod: "xLODGen Output", ExtraRWScratch: true,
		DefaultArgsByGame: map[string][]string{
			"skyrim":   {"-tes5", "-o:%WIN_OUTPUT_DIR%", "-t:%WIN_SCRATCH_DIR%"},
			"skyrimse": {"-sse", "-o:%WIN_OUTPUT_DIR%", "-t:%WIN_SCRATCH_DIR%"},
		},
	},
	{
		ID: "synthesis", Title: "Synthesis", Games: []string{"skyrimse"}, Runner: RunnerProton,
		Basenames: []string{"Synthesis.exe"}, NeedsVFSMounted: true, OutputPolicy: OutputNamedMod,
		DefaultOutputMod: "Synthesis Output",
	},
	{
		ID: "wrye-bash", Title: "Wrye Bash", Games: allBethesdaGames, Runner: RunnerProton,
		Basenames: []string{"Wrye Bash.exe"}, NeedsVFSMounted: true,
		OutputPolicy: OutputNamedMod, DefaultOutputMod: "Bashed Patch",
	},
	{
		ID: "bethini", Title: "BethINI", Games: skyrimGames, Runner: RunnerProton,
		Basenames: []string{"Bethini.exe", "BethINI.exe", "Bethini Pie.exe"}, OutputPolicy: OutputProfileSync,
	},
	{
		ID: "easynpc", Title: "EasyNPC", Games: []string{"skyrimse"}, Runner: RunnerProton,
		Basenames: []string{"EasyNPC.exe"}, NeedsVFSMounted: true, OutputPolicy: OutputScratchImport,
		DefaultOutputMod: "EasyNPC Output",
	},
	{
		ID: "cathedral-assets-optimizer", Title: "Cathedral Assets Optimizer", Games: skyrimGames, Runner: RunnerProton,
		Basenames: []string{"Cathedral Assets Optimizer.exe"}, OutputPolicy: OutputExclusiveSourceEdit,
	},
	{
		ID: "nifskope", Title: "NifSkope", Games: skyrimGames, Runner: RunnerProton,
		Basenames: []string{"NifSkope.exe"}, OutputPolicy: OutputExclusiveSourceEdit,
	},
	{
		ID: "nifskope-native", Title: "NifSkope (native)", Games: skyrimGames, Runner: RunnerNative,
		Basenames: []string{"nifskope"}, OutputPolicy: OutputExclusiveSourceEdit,
	},
	{
		ID: "resaver", Title: "ReSaver", Games: skyrimGames, Runner: RunnerProton,
		Basenames: []string{"ReSaver.exe"}, OutputPolicy: OutputSelectedCopyUp,
	},
	{
		ID: "resaver-java", Title: "ReSaver (Java)", Games: skyrimGames, Runner: RunnerJava,
		Basenames: []string{"ReSaver.jar"}, OutputPolicy: OutputSelectedCopyUp,
	},
	{
		ID: "zedit", Title: "zEdit", Games: skyrimGames, Runner: RunnerProton,
		Basenames: []string{"zEdit.exe"}, NeedsVFSMounted: true, OutputPolicy: OutputNamedMod, DefaultOutputMod: "zEdit Output",
	},
	{
		ID: "mator-smash", Title: "Mator Smash", Games: skyrimGames, Runner: RunnerProton,
		Basenames: []string{"MatorSmash.exe", "Mator Smash.exe"}, NeedsVFSMounted: true,
		OutputPolicy: OutputNamedMod, DefaultOutputMod: "Smashed Patch",
	},
}

type KnownToolStem struct {
	Title              string
	Basenames          []string
	NeedsVFSMounted    bool
	CaptureOutputToMod string
	ExtraRWScratch     bool
}

var KnownExternalTools = legacyToolStems()

func legacyToolStems() []KnownToolStem {
	out := make([]KnownToolStem, 0, len(ExternalToolCatalog))
	for _, entry := range ExternalToolCatalog {
		out = append(out, KnownToolStem{
			Title: entry.Title, Basenames: entry.Basenames, NeedsVFSMounted: entry.NeedsVFSMounted,
			CaptureOutputToMod: entry.DefaultOutputMod, ExtraRWScratch: entry.ExtraRWScratch,
		})
	}
	return out
}

// ToolForID returns a catalog entry by its stable trusted identifier.
func ToolForID(id string) (ExternalToolDefinition, bool) {
	for _, entry := range ExternalToolCatalog {
		if entry.ID == id {
			return entry, true
		}
	}
	return ExternalToolDefinition{}, false
}

// ValidateCatalogMatch verifies that an executable path actually matches the claimed trusted catalog entry.
func ValidateCatalogMatch(id, gameID, path string) (ExternalToolDefinition, bool) {
	entry, ok := ToolForID(id)
	if !ok || !supportsGame(entry, gameID) || !pathMatches(entry, path) {
		return ExternalToolDefinition{}, false
	}
	if entry.ID == "loot" && !pathInsideCatalogRoot(filepath.Join(config.ToolsDir(), "loot"), path) {
		return ExternalToolDefinition{}, false
	}
	base := filepath.Base(path)
	for _, candidate := range entry.Basenames {
		if strings.EqualFold(base, candidate) {
			return entry, true
		}
	}
	return ExternalToolDefinition{}, false
}

func pathInsideCatalogRoot(root, path string) bool {
	rootAbs, rootErr := filepath.Abs(root)
	pathAbs, pathErr := filepath.Abs(path)
	if rootErr != nil || pathErr != nil {
		return false
	}
	relative, err := filepath.Rel(rootAbs, pathAbs)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func supportsGame(entry ExternalToolDefinition, gameID string) bool {
	if gameID == "" {
		return true
	}
	for _, supported := range entry.Games {
		if supported == gameID {
			return true
		}
	}
	return false
}

type DetectedTool struct {
	CatalogID          string
	Title              string
	ExePath            string
	Runner             RunnerKind
	PrefixAppID        int
	NeedsVFSMounted    bool
	OutputPolicy       OutputPolicy
	CaptureOutputToMod string
	ExtraRWScratch     bool
	DefaultArgs        []string
}

// DetectExecutables preserves unfiltered historical detection.
func DetectExecutables(roots []string) []DetectedTool {
	return DetectExecutablesForGame("", roots)
}

// DetectExecutablesForGame scans the supplied roots for catalog entries compatible with gameID.
func DetectExecutablesForGame(gameID string, roots []string) []DetectedTool {
	definitions := make(map[string][]ExternalToolDefinition)
	for _, entry := range ExternalToolCatalog {
		if !supportsGame(entry, gameID) {
			continue
		}
		for _, basename := range entry.Basenames {
			key := strings.ToLower(basename)
			definitions[key] = append(definitions[key], entry)
		}

	}

	found := make([]DetectedTool, 0)
	seenPath := make(map[string]bool)
	for _, root := range roots {
		if root == "" {
			continue
		}
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				return nil
			}
			entries := definitions[strings.ToLower(info.Name())]
			for _, entry := range entries {
				if !pathMatches(entry, path) || seenPath[path] {
					continue
				}
				seenPath[path] = true
				found = append(found, DetectedTool{
					CatalogID: entry.ID, Title: entry.Title, ExePath: path, Runner: entry.Runner,
					PrefixAppID: entry.PrefixAppID, NeedsVFSMounted: entry.NeedsVFSMounted,
					OutputPolicy: entry.OutputPolicy, CaptureOutputToMod: entry.DefaultOutputMod,
					ExtraRWScratch: entry.ExtraRWScratch,
					DefaultArgs:    append([]string(nil), entry.DefaultArgsByGame[gameID]...),
				})
			}
			return nil
		})
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].Title != found[j].Title {
			return found[i].Title < found[j].Title
		}
		return found[i].ExePath < found[j].ExePath
	})
	return found
}

func pathMatches(entry ExternalToolDefinition, path string) bool {
	if len(entry.PathContains) == 0 {
		return true
	}
	lower := strings.ToLower(filepath.ToSlash(path))
	for _, fragment := range entry.PathContains {
		if strings.Contains(lower, strings.ToLower(filepath.ToSlash(fragment))) {
			return true
		}
	}
	return false
}

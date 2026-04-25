package tools

import (
	"os"
	"path/filepath"
	"strings"
)

// ToolDefinition describes a script extender for a Bethesda game.
type ToolDefinition struct {
	ID        string
	Name      string
	LoaderExe string
	GameIDs   []string

	// DllPrefixes are case-insensitive filename prefixes for DLLs that
	// must be loaded in "native then builtin" mode under Proton/Wine.
	// Without native override, Wine loads its own stub in place of the
	// extender's DLL and the injection hook never fires — the game boots
	// past the splash screens but menus/mods that depend on the extender
	// silently fail. The launcher enumerates the game dir at launch and
	// sets WINEDLLOVERRIDES for every matching file.
	//
	// Example for xNVSE: "nvse_" matches nvse_1_4.dll,
	// nvse_editor_1_4.dll, nvse_steam_loader.dll, etc. — version-robust.
	DllPrefixes []string

	// ExtraDlls are explicit filenames that should always be forced
	// native when present in the game dir, even if they don't match
	// DllPrefixes. xNVSE's d3dx9_38.dll is the canonical case.
	ExtraDlls []string

	// LogName is the filename the extender writes its bootstrap log to
	// ("nvse.log", "skse64.log", etc.). Used by the daemon's post-launch
	// log probe to confirm injection actually happened.
	LogName string

	// MyGamesSubdir is the game's "Documents/My Games/{subdir}/" folder
	// name inside the Wine prefix. Extenders in their 6.x+ builds write
	// their logs under this dir (the older convention was the game-dir
	// root); probing both covers every xNVSE/SKSE version shipped in the
	// last five years.
	MyGamesSubdir string
}

// KnownTools is the registry of supported script extenders.
//
// ExtraDlls: each DirectX-9 Bethesda game ships a specific d3dx9_XX.dll
// redist whose version tracks the era the game shipped in. On Proton,
// Wine's built-in d3dx9 implementation is incomplete — mods, ENB, and
// body replacers that call DX9 extension functions crash or silently
// fail unless the native redist DLL loads first. Script-extender
// launchers are the right hook point to apply this override because
// the extender is always the canonical "modded launch" path.
//
//	Oblivion (2006, DX9)  → d3dx9_27.dll + older d3dx9_9.dll (both ship with the game)
//	Fallout 3 (2008, DX9) → d3dx9_38.dll
//	New Vegas (2010, DX9) → d3dx9_38.dll
//	Skyrim LE (2011, DX9) → d3dx9_42.dll
//	Skyrim SE (2016, DX11), Fallout 4 (2015, DX11), Starfield (2023, DX12) → no d3dx9
//
// Missing ExtraDlls are skipped silently by ScanNativeDlls, so a clean
// install without one of these redists still launches fine.
var KnownTools = map[string]ToolDefinition{
	"skse64": {
		ID: "skse64", Name: "SKSE64", LoaderExe: "skse64_loader.exe",
		GameIDs:       []string{"skyrimse"},
		DllPrefixes:   []string{"skse64_"},
		LogName:       "skse64.log",
		MyGamesSubdir: "Skyrim Special Edition",
	},
	"skse": {
		ID: "skse", Name: "SKSE", LoaderExe: "skse_loader.exe",
		GameIDs:       []string{"skyrim"},
		DllPrefixes:   []string{"skse_"},
		ExtraDlls:     []string{"d3dx9_42.dll"},
		LogName:       "skse.log",
		MyGamesSubdir: "Skyrim",
	},
	"xnvse": {
		ID: "xnvse", Name: "xNVSE", LoaderExe: "nvse_loader.exe",
		GameIDs:       []string{"falloutnv"},
		DllPrefixes:   []string{"nvse_"},
		ExtraDlls:     []string{"d3dx9_38.dll"},
		LogName:       "nvse.log",
		MyGamesSubdir: "FalloutNV",
	},
	"fose": {
		ID: "fose", Name: "FOSE", LoaderExe: "fose_loader.exe",
		GameIDs:       []string{"fallout3"},
		DllPrefixes:   []string{"fose_"},
		ExtraDlls:     []string{"d3dx9_38.dll"},
		LogName:       "fose.log",
		MyGamesSubdir: "Fallout3",
	},
	"f4se": {
		ID: "f4se", Name: "F4SE", LoaderExe: "f4se_loader.exe",
		GameIDs:       []string{"fallout4"},
		DllPrefixes:   []string{"f4se_"},
		LogName:       "f4se.log",
		MyGamesSubdir: "Fallout4",
	},
	"obse": {
		ID: "obse", Name: "OBSE", LoaderExe: "obse_loader.exe",
		GameIDs:       []string{"oblivion"},
		DllPrefixes:   []string{"obse_"},
		ExtraDlls:     []string{"d3dx9_27.dll", "d3dx9_9.dll"},
		LogName:       "obse.log",
		MyGamesSubdir: "Oblivion",
	},
	"sfse": {
		ID: "sfse", Name: "SFSE", LoaderExe: "sfse_loader.exe",
		GameIDs:       []string{"starfield"},
		DllPrefixes:   []string{"sfse_"},
		LogName:       "sfse.log",
		MyGamesSubdir: "Starfield",
	},
}

// ScanNativeDlls returns the filenames in gameInstallDir that should be
// forced to "native,builtin" under Wine for this extender. Results are
// deterministic (sorted by discovery order): DllPrefixes matches first,
// then ExtraDlls that exist on disk. Missing files are skipped silently.
func (t ToolDefinition) ScanNativeDlls(gameInstallDir string) []string {
	var out []string
	seen := map[string]struct{}{}

	if len(t.DllPrefixes) > 0 {
		entries, err := os.ReadDir(gameInstallDir)
		if err == nil {
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				name := e.Name()
				lower := strings.ToLower(name)
				if !strings.HasSuffix(lower, ".dll") {
					continue
				}
				for _, prefix := range t.DllPrefixes {
					if strings.HasPrefix(lower, strings.ToLower(prefix)) {
						if _, dup := seen[lower]; !dup {
							out = append(out, name)
							seen[lower] = struct{}{}
						}
						break
					}
				}
			}
		}
	}

	for _, extra := range t.ExtraDlls {
		full := filepath.Join(gameInstallDir, extra)
		if _, err := os.Stat(full); err != nil {
			continue
		}
		lower := strings.ToLower(extra)
		if _, dup := seen[lower]; dup {
			continue
		}
		out = append(out, extra)
		seen[lower] = struct{}{}
	}
	return out
}

// BuildDllOverrides formats a list of DLL filenames into a Wine
// WINEDLLOVERRIDES value. Each entry becomes "name=n,b" (native then
// builtin). The ".dll" suffix is stripped because that's what Wine
// expects for overrides. Returns "" for an empty input.
func BuildDllOverrides(dlls []string) string {
	if len(dlls) == 0 {
		return ""
	}
	parts := make([]string, 0, len(dlls))
	for _, d := range dlls {
		stem := strings.TrimSuffix(d, ".dll")
		stem = strings.TrimSuffix(stem, ".DLL")
		parts = append(parts, stem+"=n,b")
	}
	return strings.Join(parts, ";")
}

// DetectTool checks if any known tool's loader exe exists in the game directory.
func DetectTool(gameInstallDir string, gameID string) (*ToolDefinition, bool) {
	for _, tool := range KnownTools {
		for _, gid := range tool.GameIDs {
			if gid != gameID {
				continue
			}
			loaderPath := filepath.Join(gameInstallDir, tool.LoaderExe)
			if _, err := os.Stat(loaderPath); err == nil {
				return &tool, true
			}
		}
	}
	return nil, false
}

// ToolsForGame returns all known tools for a game ID.
func ToolsForGame(gameID string) []ToolDefinition {
	var result []ToolDefinition
	for _, tool := range KnownTools {
		for _, gid := range tool.GameIDs {
			if gid == gameID {
				result = append(result, tool)
			}
		}
	}
	return result
}

package tools

import (
	"os"
	"path/filepath"
	"strings"
)

// ToolDefinition describes a script extender for a Bethesda game.
type ToolDefinition struct {
	ID            string
	Name          string
	LoaderExe     string
	GameIDs       []string
	DllPrefixes   []string
	ExtraDlls     []string
	LogName       string
	MyGamesSubdir string
}

// KnownTools is the registry of supported script extenders.
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
		GameIDs:       []string{"falloutnv", "ttw"},
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

// ScanNativeDlls returns filenames in gameInstallDir that should be forced native under Wine.
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

// BuildDllOverrides formats a list of DLL filenames into a Wine WINEDLLOVERRIDES value.
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

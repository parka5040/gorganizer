package tools

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/parka/gorganizer/internal/gamedef"
)

type ToolDefinition struct {
	ID             string
	Name           string
	LoaderExe      string
	InstallSubpath string
	DllPrefixes    []string
	ExtraDlls      []string
	LogName        string
	LogSubpath     string
	MyGamesSubdir  string
}

var KnownTools = map[string]ToolDefinition{
	"skse64": {
		ID: "skse64", Name: "SKSE64", LoaderExe: "skse64_loader.exe",
		DllPrefixes:   []string{"skse64_"},
		LogName:       "skse64.log",
		MyGamesSubdir: "Skyrim Special Edition",
	},
	"skse": {
		ID: "skse", Name: "SKSE", LoaderExe: "skse_loader.exe",
		DllPrefixes:   []string{"skse_"},
		ExtraDlls:     []string{"d3dx9_42.dll"},
		LogName:       "skse.log",
		MyGamesSubdir: "Skyrim",
	},
	"xnvse": {
		ID: "xnvse", Name: "xNVSE", LoaderExe: "nvse_loader.exe",
		DllPrefixes:   []string{"nvse_"},
		ExtraDlls:     []string{"d3dx9_38.dll"},
		LogName:       "nvse.log",
		MyGamesSubdir: "FalloutNV",
	},
	"fose": {
		ID: "fose", Name: "FOSE", LoaderExe: "fose_loader.exe",
		DllPrefixes:   []string{"fose_"},
		ExtraDlls:     []string{"d3dx9_38.dll"},
		LogName:       "fose.log",
		MyGamesSubdir: "Fallout3",
	},
	"f4se": {
		ID: "f4se", Name: "F4SE", LoaderExe: "f4se_loader.exe",
		DllPrefixes:   []string{"f4se_"},
		LogName:       "f4se.log",
		MyGamesSubdir: "Fallout4",
	},
	"obse": {
		ID: "obse", Name: "OBSE", LoaderExe: "obse_loader.exe",
		DllPrefixes:   []string{"obse_"},
		ExtraDlls:     []string{"d3dx9_27.dll", "d3dx9_9.dll"},
		LogName:       "obse.log",
		MyGamesSubdir: "Oblivion",
	},
	"sfse": {
		ID: "sfse", Name: "SFSE", LoaderExe: "sfse_loader.exe",
		DllPrefixes:   []string{"sfse_"},
		LogName:       "sfse.log",
		MyGamesSubdir: "Starfield",
	},
	"obse64": {
		ID: "obse64", Name: "OBSE64", LoaderExe: "obse64_loader.exe",
		InstallSubpath: "OblivionRemastered/Binaries/Win64",
		DllPrefixes:    []string{"obse64_"},
		LogName:        "obse64.log",
		LogSubpath:     "OBSE/Logs",
		MyGamesSubdir:  "Oblivion Remastered",
	},
}

// InstallDir returns the directory containing the loader and extender DLLs.
func (t ToolDefinition) InstallDir(gameInstallDir string) string {
	if t.InstallSubpath == "" {
		return gameInstallDir
	}
	return filepath.Join(gameInstallDir, filepath.FromSlash(t.InstallSubpath))
}

// LoaderPath returns the absolute loader path for a game installation.
func (t ToolDefinition) LoaderPath(gameInstallDir string) string {
	return filepath.Join(t.InstallDir(gameInstallDir), t.LoaderExe)
}

// LoaderRelativePath returns the stable install-root-relative loader path.
func (t ToolDefinition) LoaderRelativePath() string {
	if t.InstallSubpath == "" {
		return t.LoaderExe
	}
	return filepath.ToSlash(filepath.Join(filepath.FromSlash(t.InstallSubpath), t.LoaderExe))
}

// ScanNativeDlls returns filenames beside the loader that should be forced native under Wine.
func (t ToolDefinition) ScanNativeDlls(gameInstallDir string) []string {
	var out []string
	seen := map[string]struct{}{}
	toolDir := t.InstallDir(gameInstallDir)

	if len(t.DllPrefixes) > 0 {
		entries, err := os.ReadDir(toolDir)
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
		full := filepath.Join(toolDir, extra)
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
	for _, tool := range ToolsForGame(gameID) {
		loaderPath := tool.LoaderPath(gameInstallDir)
		if _, err := os.Stat(loaderPath); err == nil {
			return &tool, true
		}
	}
	return nil, false
}

// ToolsForGame returns all known tools for a game ID.
func ToolsForGame(gameID string) []ToolDefinition {
	g, ok := gamedef.ByID(gameID)
	if !ok || g.ScriptExtenderToolID == "" {
		return nil
	}
	tool, ok := KnownTools[g.ScriptExtenderToolID]
	if !ok {
		return nil
	}
	return []ToolDefinition{tool}
}

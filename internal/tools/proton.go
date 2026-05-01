package tools

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/ipc"
	"github.com/parka/gorganizer/internal/steam"
)

// ProtonVersion describes an installed Proton version.
type ProtonVersion struct {
	Name string
	Path string
}

const loaderExeSizeCap = 500 * 1024

// Manager handles script extender detection and game launching via Proton.
type Manager struct {
	config *config.Config
}

func NewManager(cfg *config.Config) *Manager {
	return &Manager{config: cfg}
}

// DetectProton scans steamapps/common/Proton*/proton for available versions.
func (m *Manager) DetectProton() ([]ipc.ProtonVersionResult, error) {
	steamRoot, err := findSteamRoot()
	if err != nil {
		return nil, err
	}

	versions := detectProtonVersions(steamRoot)
	var results []ipc.ProtonVersionResult
	for _, v := range versions {
		results = append(results, ipc.ProtonVersionResult{
			Name: v.Name,
			Path: v.Path,
		})
	}
	return results, nil
}

// LaunchHandle tracks a Proton-launched process; Done closes when the whole tree has exited.
type LaunchHandle struct {
	PID  int
	Done <-chan struct{}
}

// LaunchGame launches a game through Proton with the correct environment.
func (m *Manager) LaunchGame(gameID string, useTool bool, gameCfg *config.GameConfig, preferredProton string) (*LaunchHandle, error) {
	steamRoot, err := findSteamRoot()
	if err != nil {
		return nil, fmt.Errorf("finding Steam root: %w", err)
	}

	var exePath string
	var activeTool *ToolDefinition
	if useTool && gameCfg.ToolExe != "" {
		exePath = filepath.Join(gameCfg.InstallPath, gameCfg.ToolExe)
		configured := filepath.Base(gameCfg.ToolExe)
		for _, t := range KnownTools {
			if strings.EqualFold(t.LoaderExe, configured) {
				tt := t
				activeTool = &tt
				break
			}
		}
	} else if useTool {
		tool, found := DetectTool(gameCfg.InstallPath, gameID)
		if found {
			exePath = filepath.Join(gameCfg.InstallPath, tool.LoaderExe)
			activeTool = tool
		}
	}

	if useTool && activeTool == nil {
		for _, t := range KnownTools {
			for _, gid := range t.GameIDs {
				if gid == gameID {
					tt := t
					activeTool = &tt
					break
				}
			}
			if activeTool != nil {
				break
			}
		}
		if activeTool != nil {
			slog.Info("resolved script extender via gameID fallback",
				"game", gameID, "tool", activeTool.ID, "loader", activeTool.LoaderExe,
				"configured_exe", gameCfg.ToolExe)
		}
	}

	if exePath == "" {
		return nil, &ipc.LoaderMissingError{
			GameID:      gameID,
			InstallPath: gameCfg.InstallPath,
			Reason:      "no-loader-configured",
		}
	}

	if info, statErr := os.Stat(exePath); statErr != nil {
		slog.Warn("configured loader exe missing, probing for alternative",
			"exe", exePath, "err", statErr)
		if alt, found := DetectTool(gameCfg.InstallPath, gameID); found {
			exePath = filepath.Join(gameCfg.InstallPath, alt.LoaderExe)
			activeTool = alt
			slog.Info("self-heal: found alternative loader", "exe", exePath, "tool", alt.ID)
		} else {
			return nil, &ipc.LoaderMissingError{
				GameID:        gameID,
				ConfiguredExe: filepath.Base(gameCfg.ToolExe),
				InstallPath:   gameCfg.InstallPath,
				Reason:        "missing",
			}
		}
	} else if info.Size() > loaderExeSizeCap {
		return nil, &ipc.LoaderMissingError{
			GameID:        gameID,
			ConfiguredExe: filepath.Base(exePath),
			InstallPath:   gameCfg.InstallPath,
			Reason:        "looks-like-vanilla-launcher",
		}
	}

	protonPath := gameCfg.ProtonPath
	if protonPath == "" {
		protonPath = preferredProton
	}
	if protonPath == "" {
		versions := detectProtonVersions(steamRoot)
		if len(versions) == 0 {
			return nil, fmt.Errorf("no Proton versions found")
		}
		protonPath = versions[0].Path
	}

	appID := strconv.Itoa(gameCfg.SteamAppID)
	compatDataPath := filepath.Join(steamRoot, "steamapps", "compatdata", appID)

	var dllOverrides string
	if activeTool != nil {
		removeLegacySteamAppIDFile(gameCfg.InstallPath, appID)
		nativeDlls := activeTool.ScanNativeDlls(gameCfg.InstallPath)
		dllOverrides = BuildDllOverrides(nativeDlls)
		if dllOverrides == "" {
			slog.Warn("script extender tool matched but no DLLs found to force-native — extender probably won't inject",
				"tool", activeTool.ID,
				"game_dir", gameCfg.InstallPath,
				"prefixes", activeTool.DllPrefixes,
				"extras", activeTool.ExtraDlls)
		} else {
			slog.Info("forcing native DLL overrides for script extender",
				"tool", activeTool.ID,
				"count", len(nativeDlls),
				"dlls", nativeDlls,
				"WINEDLLOVERRIDES", dllOverrides)
		}
	} else if useTool {
		slog.Warn("tool launch requested but no KnownTools entry matched — WINEDLLOVERRIDES will be empty",
			"game", gameID, "tool_exe", gameCfg.ToolExe)
	}

	env := append([]string{}, os.Environ()...)
	env = append(env, buildSteamParityEnv(
		compatDataPath, steamRoot, appID, gameCfg.InstallPath, dllOverrides,
	)...)

	args := []string{"waitforexitandrun", exePath}
	bin := protonPath
	if entryPoint, runtimeName := ResolveProtonRuntime(protonPath, steamRoot); entryPoint != "" {
		args = append([]string{"--verb=waitforexitandrun", "--", protonPath}, args...)
		bin = entryPoint
		slog.Info("invoking Proton through its Steam Linux Runtime container",
			"runtime", runtimeName, "entry_point", entryPoint, "proton", protonPath)
	} else {
		slog.Warn("no Steam Linux Runtime container found — Proton will run on the host directly. "+
			"Expect input/audio/perf issues (cursor frozen, no sound, snail's pace). "+
			"Install the runtime Steam offers when this Proton version is added to a game's compat tools.",
			"proton", protonPath, "steam_root", steamRoot)
	}

	cmd := exec.Command(bin, args...)
	cmd.Env = env
	cmd.Dir = gameCfg.InstallPath

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	slog.Info("launching game via Proton",
		"game", gameID,
		"exe", exePath,
		"proton", protonPath)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting Proton: %w", err)
	}

	go logProtonOutput(gameID, "stdout", stdoutPipe)
	go logProtonOutput(gameID, "stderr", stderrPipe)

	if activeTool != nil {
		go probeExtenderLog(gameID, activeTool, gameCfg, compatDataPath)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := cmd.Wait(); err != nil {
			slog.Debug("game process exited", "game", gameID, "err", err)
		}
	}()

	return &LaunchHandle{PID: cmd.Process.Pid, Done: done}, nil
}

// probeExtenderLog samples the script-extender's log file 8 seconds after launch.
func probeExtenderLog(gameID string, tool *ToolDefinition, gameCfg *config.GameConfig, compatDataPath string) {
	time.Sleep(8 * time.Second)

	if tool.LogName == "" {
		return
	}

	prefixDocs := filepath.Join(compatDataPath, "pfx", "drive_c", "users", "steamuser")
	candidates := []string{
		filepath.Join(gameCfg.InstallPath, tool.LogName),
	}
	if tool.MyGamesSubdir != "" {
		for _, docDir := range []string{"My Documents", "Documents"} {
			base := filepath.Join(prefixDocs, docDir, "My Games", tool.MyGamesSubdir)
			candidates = append(candidates,
				filepath.Join(base, tool.LogName),
				filepath.Join(base, strings.ToUpper(tool.ID), tool.LogName),
				filepath.Join(base, "NVSE", tool.LogName),
			)
		}
	}

	for _, p := range candidates {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if time.Since(info.ModTime()) > time.Hour {
			slog.Warn("extender log is stale — extender likely did NOT inject this run",
				"game", gameID, "tool", tool.ID, "log", p, "modtime", info.ModTime())
			return
		}
		lines, readErr := readFirstLines(p, 20)
		if readErr != nil {
			slog.Warn("could not read extender log", "log", p, "err", readErr)
			return
		}
		slog.Info("script extender log (first lines)",
			"game", gameID, "tool", tool.ID, "log", p,
			"lines", strings.Join(lines, " | "))
		return
	}
	slog.Warn("script extender log not found — extender did NOT inject (check WINEDLLOVERRIDES and loader exe)",
		"game", gameID, "tool", tool.ID, "probed_paths", candidates)
}

func readFirstLines(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var lines []string
	for scanner.Scan() && len(lines) < n {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

// logProtonOutput drains a pipe into slog line-by-line.
func logProtonOutput(gameID, stream string, r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		slog.Info("proton", "game", gameID, "stream", stream, "line", scanner.Text())
	}
}

// detectProtonVersions scans for Proton installations and returns them in preference order.
func detectProtonVersions(steamRoot string) []ProtonVersion {
	commonDir := filepath.Join(steamRoot, "steamapps", "common")
	entries, err := os.ReadDir(commonDir)
	if err != nil {
		return nil
	}

	var versions []ProtonVersion
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "Proton") {
			continue
		}
		protonScript := filepath.Join(commonDir, e.Name(), "proton")
		if _, err := os.Stat(protonScript); err == nil {
			versions = append(versions, ProtonVersion{
				Name: e.Name(),
				Path: protonScript,
			})
		}
	}

	type rank struct {
		bucket int
		major  int
	}
	scoreOf := func(name string) rank {
		low := strings.ToLower(name)
		if strings.Contains(low, "experimental") {
			return rank{bucket: 1}
		}
		if strings.Contains(low, "hotfix") {
			return rank{bucket: 2}
		}
		tail := strings.TrimPrefix(name, "Proton")
		tail = strings.TrimSpace(tail)
		var maj strings.Builder
		for _, r := range tail {
			if r >= '0' && r <= '9' {
				maj.WriteRune(r)
				continue
			}
			break
		}
		if maj.Len() == 0 {
			return rank{bucket: 3}
		}
		n := 0
		for _, r := range maj.String() {
			n = n*10 + int(r-'0')
		}
		return rank{bucket: 0, major: -n}
	}
	sort.Slice(versions, func(i, j int) bool {
		ri, rj := scoreOf(versions[i].Name), scoreOf(versions[j].Name)
		if ri.bucket != rj.bucket {
			return ri.bucket < rj.bucket
		}
		if ri.major != rj.major {
			return ri.major < rj.major
		}
		return versions[i].Name < versions[j].Name
	})
	return versions
}

// buildSteamParityEnv reproduces the STEAM_COMPAT_* environment Steam sets when launching a Proton game.
func buildSteamParityEnv(compatDataPath, steamRoot, appID, installPath, dllOverrides string) []string {
	env := []string{
		"STEAM_COMPAT_DATA_PATH=" + compatDataPath,
		"STEAM_COMPAT_CLIENT_INSTALL_PATH=" + steamRoot,
		"STEAM_COMPAT_INSTALL_PATH=" + installPath,
		"STEAM_COMPAT_APP_ID=" + appID,
		"SteamAppId=" + appID,
		"SteamGameId=" + appID,
	}

	if libs := parseLibraryFolders(steamRoot); len(libs) > 0 {
		joined := strings.Join(libs, ":")
		env = append(env,
			"STEAM_COMPAT_MOUNTS="+joined,
			"STEAM_COMPAT_LIBRARY_PATHS="+joined,
		)
	}

	if tools := detectCompatToolPaths(steamRoot); len(tools) > 0 {
		env = append(env, "STEAM_COMPAT_TOOL_PATHS="+strings.Join(tools, ":"))
	}

	if dllOverrides != "" {
		merged := mergeDllOverrides(os.Getenv("WINEDLLOVERRIDES"), dllOverrides)
		env = append(env, "WINEDLLOVERRIDES="+merged)
	}

	if logDir := os.Getenv("PROTON_LOG_DIR"); logDir != "" {
		env = append(env, "PROTON_LOG=1", "PROTON_LOG_DIR="+logDir)
	}

	slog.Debug("proton parity env",
		"compat_data_path", compatDataPath,
		"install_path", installPath,
		"app_id", appID,
		"has_wine_dll_overrides", dllOverrides != "")
	return env
}

// mergeDllOverrides combines inherited and our WINEDLLOVERRIDES values; ours wins on key collision.
func mergeDllOverrides(inherited, ours string) string {
	if ours == "" {
		return inherited
	}
	if inherited == "" {
		return ours
	}
	parsed := map[string]string{}
	order := []string{}
	put := func(entry string) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return
		}
		key, val, ok := strings.Cut(entry, "=")
		if !ok {
			return
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if key == "" {
			return
		}
		if _, seen := parsed[key]; !seen {
			order = append(order, key)
		}
		parsed[key] = val
	}
	for _, e := range strings.Split(inherited, ";") {
		put(e)
	}
	for _, e := range strings.Split(ours, ";") {
		put(e)
	}
	parts := make([]string, 0, len(order))
	for _, k := range order {
		parts = append(parts, k+"="+parsed[k])
	}
	return strings.Join(parts, ";")
}

// parseLibraryFolders extracts Steam library root paths from steamapps/libraryfolders.vdf.
func parseLibraryFolders(steamRoot string) []string {
	vdfPath := filepath.Join(steamRoot, "steamapps", "libraryfolders.vdf")
	f, err := os.Open(vdfPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	var out []string
	seen := map[string]struct{}{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, `"path"`) {
			continue
		}
		firstClose := strings.Index(line[1:], `"`) + 1
		if firstClose <= 0 {
			continue
		}
		rest := strings.TrimSpace(line[firstClose+1:])
		open := strings.Index(rest, `"`)
		if open < 0 {
			continue
		}
		close := strings.Index(rest[open+1:], `"`)
		if close < 0 {
			continue
		}
		p := rest[open+1 : open+1+close]
		if p == "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// ResolveProtonRuntime returns the Steam Linux Runtime entry point script and name for the given Proton.
func ResolveProtonRuntime(protonPath, steamRoot string) (entryPoint, runtimeName string) {
	if protonPath == "" || steamRoot == "" {
		return "", ""
	}
	manifest := filepath.Join(filepath.Dir(protonPath), "toolmanifest.vdf")
	requireID, err := readVDFKey(manifest, "require_tool_appid")
	if err != nil || requireID == "" {
		return "", ""
	}
	appManifest := filepath.Join(steamRoot, "steamapps", "appmanifest_"+requireID+".acf")
	installDir, err := readVDFKey(appManifest, "installdir")
	if err != nil || installDir == "" {
		return "", ""
	}
	candidate := filepath.Join(steamRoot, "steamapps", "common", installDir, "_v2-entry-point")
	info, err := os.Stat(candidate)
	if err != nil || info.IsDir() {
		return "", ""
	}
	return candidate, installDir
}

// readVDFKey scans a VDF file line-by-line for the first quoted top-level key matching wantKey.
func readVDFKey(path, wantKey string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	keyTok := `"` + wantKey + `"`
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, keyTok) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, keyTok))
		open := strings.Index(rest, `"`)
		if open < 0 {
			continue
		}
		close := strings.Index(rest[open+1:], `"`)
		if close < 0 {
			continue
		}
		return rest[open+1 : open+1+close], nil
	}
	return "", nil
}

// detectCompatToolPaths returns the list of Steam compatibility tool roots the sandbox should expose.
func detectCompatToolPaths(steamRoot string) []string {
	commonDir := filepath.Join(steamRoot, "steamapps", "common")
	var out []string
	for _, name := range []string{
		"SteamLinuxRuntime_sniper",
		"SteamLinuxRuntime_soldier",
		"SteamLinuxRuntime",
	} {
		p := filepath.Join(commonDir, name)
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			out = append(out, p)
		}
	}
	return out
}

// removeLegacySteamAppIDFile deletes a steam_appid.txt left by older gorganizer versions.
func removeLegacySteamAppIDFile(gameInstallDir, appID string) {
	path := filepath.Join(gameInstallDir, "steam_appid.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if strings.TrimSpace(string(data)) != appID {
		return
	}
	if err := os.Remove(path); err != nil {
		slog.Warn("could not remove stale steam_appid.txt", "path", path, "err", err)
		return
	}
	slog.Info("removed legacy steam_appid.txt written by older gorganizer", "path", path)
}

func findSteamRoot() (string, error) {
	return steam.FindRoot()
}

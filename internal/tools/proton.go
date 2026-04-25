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
)

// ProtonVersion describes an installed Proton version.
type ProtonVersion struct {
	Name string
	Path string // path to the proton script
}

// Loader exes are tiny — always under this threshold. Anything larger is
// almost certainly the Bethesda launcher from a Steam-update restore; the
// "rename loader to FalloutNVLauncher.exe" Proton workaround produces this
// exact failure mode after an update.
const loaderExeSizeCap = 500 * 1024 // 500 KB

// Manager handles script extender detection and game launching via Proton.
type Manager struct {
	config *config.Config
}

// NewManager creates a tools Manager.
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

// LaunchHandle is the tracking record for a Proton-launched process. The
// Done channel closes once the launched tree (Proton wrapper → game exe)
// has fully exited — the daemon watches this to defer VFS unmount until
// the user has actually finished playing.
type LaunchHandle struct {
	PID  int
	Done <-chan struct{}
}

// LaunchGame launches a game through Proton with the correct environment.
// If useTool is true and a tool is configured, launches the tool's loader
// instead of the game exe. `preferredProton` is the user's global default
// (empty string → auto-pick via detection ranking).
func (m *Manager) LaunchGame(gameID string, useTool bool, gameCfg *config.GameConfig, preferredProton string) (*LaunchHandle, error) {
	steamRoot, err := findSteamRoot()
	if err != nil {
		return nil, fmt.Errorf("finding Steam root: %w", err)
	}

	// Determine which exe to launch and which tool (if any) is driving
	// the launch. We keep the tool so we can set its DLL overrides below.
	var exePath string
	var activeTool *ToolDefinition
	if useTool && gameCfg.ToolExe != "" {
		exePath = filepath.Join(gameCfg.InstallPath, gameCfg.ToolExe)
		// Try to match the configured exe name against a known tool so
		// we can still find the right DllPrefixes. Match against the
		// basename because users sometimes configure a full path or a
		// renamed exe (e.g. nvse_loader.exe → FalloutNVLauncher.exe is
		// a common Proton workaround).
		configured := filepath.Base(gameCfg.ToolExe)
		for _, t := range KnownTools {
			if strings.EqualFold(t.LoaderExe, configured) {
				tt := t
				activeTool = &tt
				break
			}
		}
	} else if useTool {
		// Auto-detect tool.
		tool, found := DetectTool(gameCfg.InstallPath, gameID)
		if found {
			exePath = filepath.Join(gameCfg.InstallPath, tool.LoaderExe)
			activeTool = tool
		}
	}

	// gameID fallback — covers the "renamed loader exe" workaround where
	// the ToolExe string doesn't look like any known LoaderExe. Without
	// this the launch still works (we exec the exe) but WINEDLLOVERRIDES
	// is skipped, so the extender silently fails to inject and the user
	// sees the "splash plays but main menu never appears" symptom.
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
		// No tool, can't launch via Proton directly. Surface a structured
		// error so the frontend can pitch the user toward "Install xNVSE".
		return nil, &ipc.LoaderMissingError{
			GameID:      gameID,
			InstallPath: gameCfg.InstallPath,
			Reason:      "no-loader-configured",
		}
	}

	// Validate the loader exe actually exists and is a loader (not a game
	// launcher restored by Steam). One self-heal attempt via DetectTool —
	// handles "user moved the file / config drift" — before giving up.
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
		// Over the sanity cap — almost certainly a game launcher. Run it and
		// the user sees the Bethesda launcher instead of xNVSE: the exact
		// post-Steam-update symptom we're trying to kill.
		return nil, &ipc.LoaderMissingError{
			GameID:        gameID,
			ConfiguredExe: filepath.Base(exePath),
			InstallPath:   gameCfg.InstallPath,
			Reason:        "looks-like-vanilla-launcher",
		}
	}

	// Proton selection priority:
	//   1. Per-game override (gameCfg.ProtonPath)
	//   2. Global preferred (preferredProton arg — set via Settings)
	//   3. Auto-pick: detectProtonVersions returns the preferred build at [0]
	//      (Proton 11 > 10 > 9 > Experimental > Hotfix).
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

	// Build environment.
	appID := strconv.Itoa(gameCfg.SteamAppID)
	compatDataPath := filepath.Join(steamRoot, "steamapps", "compatdata", appID)

	// Compute the WINEDLLOVERRIDES string from the actual extender DLLs
	// present in the game dir. Clean up any stale steam_appid.txt a
	// previous build wrote — Valve's docs say that file is for dev
	// testing OUTSIDE Steam; its presence while Steam is running the
	// game via Proton creates a DRM conflict where Steam sees the app
	// "running" but no game window ever appears.
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

	// Order matters: Go's exec resolves duplicate env keys to the LAST
	// value, so our Proton/Wine-critical vars must appear AFTER the
	// inherited shell environment. Otherwise a user who happens to have
	// WINEDLLOVERRIDES, SteamAppId, or STEAM_COMPAT_* exported from their
	// shell clobbers our merged script-extender overrides — the extender
	// then silently fails to inject, the game boots vanilla, and no log
	// line explains why.
	env := append([]string{}, os.Environ()...)
	env = append(env, buildSteamParityEnv(
		compatDataPath, steamRoot, appID, gameCfg.InstallPath, dllOverrides,
	)...)

	// Wrap Proton in its required Steam Linux Runtime container when one
	// is available. Steam itself launches every Proton title via
	//
	//   reaper SteamLaunch AppId=N -- steam-launch-wrapper --
	//   <runtime>/_v2-entry-point --verb=waitforexitandrun --
	//   <proton>/proton waitforexitandrun <game.exe>
	//
	// pressure-vessel inside that entry point bind-mounts the input,
	// audio, and GPU device nodes the game expects, plus exposes a
	// glibc/X11 stack matching what Proton was built against. Skipping
	// it (which is what an in-place `proton waitforexitandrun` does)
	// surfaces as: cursor frozen, audio gone, frame pacing collapses to
	// snail's pace — the game still boots its renderer because DXVK is
	// statically robust, but everything else fails. This is exactly the
	// "menu loads but mouse + audio + perf are dead" report.
	//
	// Falls back to direct invocation only when no runtime is found —
	// the user gets a warning explaining the symptoms they should expect.
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

	// Capture Proton's stdout + stderr so launch failures are diagnosable
	// from the daemon log. Previously these went to /dev/null, which
	// meant when Steam reported "running" but no game window appeared
	// the user had no way to see why. Piped in goroutines so long-running
	// processes don't block on a full pipe buffer.
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

	// Post-launch extender-log probe. If the extender injected, it writes
	// nvse.log / skse.log / etc. either into the game dir or into the Wine
	// prefix's Documents/My Games/{subdir}/ (location varies by extender
	// version). Sampled once ~8s after launch — enough time for the loader
	// to kick off the game, not so long that we miss early-crash diagnosis.
	if activeTool != nil {
		go probeExtenderLog(gameID, activeTool, gameCfg, compatDataPath)
	}

	// Don't block the caller — the game runs independently. The done
	// channel closes when Proton's whole tree has exited so the daemon
	// can defer VFS unmount until the user is actually done playing.
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := cmd.Wait(); err != nil {
			slog.Debug("game process exited", "game", gameID, "err", err)
		}
	}()

	return &LaunchHandle{PID: cmd.Process.Pid, Done: done}, nil
}

// probeExtenderLog samples the script-extender's log file 8 seconds after
// launch and drops its first few lines into slog. Absence of the log is
// itself diagnostic: it means the extender DLL never loaded, which points
// to either a DLL-override failure (WINEDLLOVERRIDES not reaching the
// process) or a wrong loader exe.
func probeExtenderLog(gameID string, tool *ToolDefinition, gameCfg *config.GameConfig, compatDataPath string) {
	time.Sleep(8 * time.Second)

	if tool.LogName == "" {
		return // unknown log naming — nothing to probe
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
				filepath.Join(base, "NVSE", tool.LogName), // historical xNVSE subdir
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

// logProtonOutput drains a pipe into slog line-by-line. Proton and Wine
// are chatty even on successful launches, so this lands at Info so a
// single journalctl --follow -u gorganizerd gives the full picture.
func logProtonOutput(gameID, stream string, r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		slog.Info("proton", "game", gameID, "stream", stream, "line", scanner.Text())
	}
}

// detectProtonVersions scans for Proton installations and returns them in
// "prefer-this-one-first" order: stable numeric versions descending, then
// Experimental, then Hotfix, then anything else. Callers pick [0] when no
// per-game override is set — this way "Proton 11" wins over "Proton Hotfix"
// on a system that has both.
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

	// rank(name) is lower for preferred builds. Numeric versions are ranked
	// by negative major version (so newer wins); suffixes like "(BETA)" are
	// accepted but demoted a notch. Experimental / Hotfix / anything else
	// sit below every numeric build.
	type rank struct {
		bucket int     // 0 = numeric, 1 = experimental, 2 = hotfix, 3 = other
		major  int     // parsed major version, negated so higher = earlier
	}
	scoreOf := func(name string) rank {
		low := strings.ToLower(name)
		if strings.Contains(low, "experimental") {
			return rank{bucket: 1}
		}
		if strings.Contains(low, "hotfix") {
			return rank{bucket: 2}
		}
		// "Proton 11.0", "Proton 10.1", "Proton 9.0 (Beta)" — grab the
		// first run of digits after the space.
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
		// Tie-breaker: lexicographic on the full name so "Proton 10.0"
		// beats "Proton 10.0 (Beta)".
		return versions[i].Name < versions[j].Name
	})
	return versions
}

// buildSteamParityEnv reproduces the environment Steam itself sets when
// launching a Proton game. Steam's own launch chain is
//
//	reaper → steam-launch-wrapper → proton waitforexitandrun → game exe
//
// and along the way it exports a handful of STEAM_COMPAT_* vars that the
// Proton wrapper script reads to set up the Wine prefix, Steam Linux Runtime
// (Sniper) bubble, and filesystem mounts. Gorganizer's previous
// implementation set only four of those vars; the missing
// STEAM_COMPAT_INSTALL_PATH / STEAM_COMPAT_MOUNTS / STEAM_COMPAT_TOOL_PATHS
// in particular left the Sniper container with an incomplete filesystem view,
// which manifests as xNVSE running but the main menu failing to render (DX9
// init fails inside the sandbox).
//
// All of these vars are cheap to compute and idempotent — Proton ignores ones
// it doesn't recognize.
func buildSteamParityEnv(compatDataPath, steamRoot, appID, installPath, dllOverrides string) []string {
	env := []string{
		"STEAM_COMPAT_DATA_PATH=" + compatDataPath,
		"STEAM_COMPAT_CLIENT_INSTALL_PATH=" + steamRoot,
		"STEAM_COMPAT_INSTALL_PATH=" + installPath,
		"STEAM_COMPAT_APP_ID=" + appID,
		"SteamAppId=" + appID,
		"SteamGameId=" + appID,
	}

	// Library roots the sandbox needs to see. Steam feeds this so the game
	// can read files that live in a non-default Steam library (common for
	// users who keep games on a second drive). Both spellings exist across
	// Steam versions — set both so Proton picks whichever it reads.
	if libs := parseLibraryFolders(steamRoot); len(libs) > 0 {
		joined := strings.Join(libs, ":")
		env = append(env,
			"STEAM_COMPAT_MOUNTS="+joined,
			"STEAM_COMPAT_LIBRARY_PATHS="+joined,
		)
	}

	// Steam Linux Runtime (Sniper) bubble. Some DX9 titles fail to init
	// without the Sniper overlay — this is part of why xNVSE's splash
	// plays but the main menu never renders for some users.
	if tools := detectCompatToolPaths(steamRoot); len(tools) > 0 {
		env = append(env, "STEAM_COMPAT_TOOL_PATHS="+strings.Join(tools, ":"))
	}

	if dllOverrides != "" {
		merged := mergeDllOverrides(os.Getenv("WINEDLLOVERRIDES"), dllOverrides)
		env = append(env, "WINEDLLOVERRIDES="+merged)
	}

	// Opt into Proton's own log when the user sets a debug log dir. This is
	// diagnostic-only: a user debugging a launch failure can set
	// PROTON_LOG_DIR in the environment before starting gorganizerd, and
	// Proton will dump `steam-{appID}.log` there. Off by default because the
	// log files are large and noisy.
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

// mergeDllOverrides combines an inherited WINEDLLOVERRIDES string with our
// script-extender-driven defaults. Gorganizer's entries win on key collision
// — the user forcing an extender DLL to builtin would defeat the point of
// using an extender at all, and this way the merge is deterministic.
//
// WINEDLLOVERRIDES syntax: "key=value;key2=value2", where key is a DLL
// stem (no .dll) optionally with comma-separated aliases, and value is one
// or more of n|b|n,b|d (native, builtin, native+builtin, disabled).
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
	// Inherited first so we keep the user's ordering for unrelated keys.
	for _, e := range strings.Split(inherited, ";") {
		put(e)
	}
	// Our entries overwrite on collision.
	for _, e := range strings.Split(ours, ";") {
		put(e)
	}
	parts := make([]string, 0, len(order))
	for _, k := range order {
		parts = append(parts, k+"="+parsed[k])
	}
	return strings.Join(parts, ";")
}

// parseLibraryFolders extracts every Steam library root path from
// steamapps/libraryfolders.vdf. Returns an absolute-path list, deduped in
// declaration order. Empty (not an error) when the file is missing or
// parseable but contains zero libraries — callers should fall through to a
// sensible default in that case.
//
// libraryfolders.vdf is Valve's custom KeyValues format, not JSON. The
// relevant shape:
//
//	"libraryfolders"
//	{
//	    "0" { "path" "/home/user/.local/share/Steam" ... }
//	    "1" { "path" "/mnt/games/SteamLibrary"         ... }
//	}
//
// We only care about the "path" values; everything else (contentstatsid,
// apps map) is ignored.
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
		// Look for a line like:  "path"  "/some/dir"
		if !strings.HasPrefix(line, `"path"`) {
			continue
		}
		// Split into two quoted chunks.
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

// ResolveProtonRuntime returns the absolute path to the Steam Linux Runtime
// `_v2-entry-point` script Proton at protonPath was built to run inside,
// plus the runtime's directory name for logging. Empty strings when no
// matching runtime can be located.
//
// Lookup chain (matches what Steam itself does):
//
//  1. Read <proton>/toolmanifest.vdf, extract require_tool_appid.
//  2. Read steamapps/appmanifest_<appid>.acf, extract installdir.
//  3. Use steamapps/common/<installdir>/_v2-entry-point.
//
// Each step is best-effort: a missing toolmanifest, an unparseable
// appmanifest, or a missing entry-point script all fall through to "",
// causing the caller to log a warning and run Proton directly. We do
// NOT try to guess the runtime when require_tool_appid is missing —
// guessing wrong (e.g. running a Proton 11 title inside Sniper instead
// of SLR4) is worse than running on the host.
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

// readVDFKey scans Valve's KeyValues format (used by both toolmanifest.vdf
// and appmanifest_<id>.acf) for the first occurrence of a quoted top-level
// "key" "value" pair matching wantKey. Case-sensitive on the key. Returns
// the value with surrounding whitespace trimmed.
//
// Not a full VDF parser — these manifest files are flat enough that line-
// based matching is correct in practice, and pulling in a real parser for
// two fields isn't worth the dependency.
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
		// Expect: "value" — find the first quote, then the matching close.
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

// detectCompatToolPaths returns the list of Steam compatibility tool roots
// the sandbox should expose. At minimum: the Steam Linux Runtime (Sniper)
// directory, which several DX9 games need to pick up the right glibc / X11
// stack when Proton runs them. Silently empty when Sniper isn't installed —
// that's fine for games that don't require it, but xNVSE's "main menu never
// renders" symptom points right at a missing Sniper overlay.
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

// removeLegacySteamAppIDFile deletes a steam_appid.txt that an older
// version of gorganizer wrote into the game dir. When Proton launches a
// Steam-owned game, Steam provides the app ID via SteamAppId / SteamGameId
// env vars; a local steam_appid.txt is a DEVELOPMENT escape hatch for
// running a game OUTSIDE Steam. If both coexist, Steam's DRM binds to
// the local file instead of the env, and the launch hangs with "game
// running" in Steam but no window ever appearing.
//
// Conservative about what we delete: only wipes a file whose trimmed
// content is exactly the app ID. That matches what the previous release
// wrote (single app-ID number, optionally followed by a newline) but
// leaves anything with comments, extra lines, or different IDs intact
// in case a user placed the file themselves for a different reason.
func removeLegacySteamAppIDFile(gameInstallDir, appID string) {
	path := filepath.Join(gameInstallDir, "steam_appid.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		return // no file — nothing to do
	}
	if strings.TrimSpace(string(data)) != appID {
		// Not ours — leave it alone.
		return
	}
	if err := os.Remove(path); err != nil {
		slog.Warn("could not remove stale steam_appid.txt", "path", path, "err", err)
		return
	}
	slog.Info("removed legacy steam_appid.txt written by older gorganizer", "path", path)
}

// findSteamRoot delegates to the game package's detection.
func findSteamRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}

	candidates := []string{
		filepath.Join(dataHome, "Steam"),
		filepath.Join(home, ".steam", "steam"),
		filepath.Join(home, ".var", "app", "com.valvesoftware.Steam", ".local", "share", "Steam"),
	}

	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, "steamapps")); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("Steam root not found")
}

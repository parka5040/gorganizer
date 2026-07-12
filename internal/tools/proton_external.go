package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/parka/gorganizer/internal/config"
)

var dropEnvKeys = map[string]bool{
	"MANGOHUD":         true,
	"MANGOHUD_DLSYM":   true,
	"MANGOHUD_CONFIG":  true,
	"DXVK_HUD":         true,
	"OBS_VKCAPTURE":    true,
	"ENABLE_VKBASALT":  true,
	"LD_PRELOAD":       true,
	"WINEDLLOVERRIDES": true,
}

type ExternalLaunchOpts struct {
	PrefixGameID    string
	GameCfg         *config.GameConfig
	ExePath         string
	Args            []string
	ExtraEnv        []string
	PreferredProton string
	SanitizeEnv     bool
	RWPaths         []string
	WorkingDir      string
	PrefixAppID     int
	ROPaths         []string
	PrefixReserved  bool
}

// LaunchExternal launches a Windows executable inside a Proton prefix for
func (m *Manager) LaunchExternal(
	prefixGameID string,
	gameCfg *config.GameConfig,
	exePath string,
	args []string,
	extraEnv []string,
	preferredProton string,
	sanitizeEnv bool,
	rwPaths []string,
) (*ExternalLaunchHandle, error) {
	return m.LaunchExternalWithOptions(ExternalLaunchOpts{
		PrefixGameID:    prefixGameID,
		GameCfg:         gameCfg,
		ExePath:         exePath,
		Args:            args,
		ExtraEnv:        extraEnv,
		PreferredProton: preferredProton,
		SanitizeEnv:     sanitizeEnv,
		RWPaths:         rwPaths,
	})
}

// LaunchExternalWithOptions is the general external-tool launcher: any Windows
func (m *Manager) LaunchExternalWithOptions(o ExternalLaunchOpts) (*ExternalLaunchHandle, error) {
	if o.GameCfg == nil {
		return nil, fmt.Errorf("LaunchExternal: gameCfg is nil")
	}

	steamRoot, err := findSteamRoot()
	if err != nil {
		return nil, fmt.Errorf("finding Steam root: %w", err)
	}

	prefixAppID := o.GameCfg.SteamAppID
	if o.PrefixAppID > 0 {
		prefixAppID = o.PrefixAppID
	}
	appID := strconv.Itoa(prefixAppID)
	libraryRoot := resolveSteamLibrary(steamRoot, o.GameCfg)
	compatDataPath := findCompatDataPath(steamRoot, libraryRoot, appID)
	prefixPath := filepath.Join(compatDataPath, "pfx")

	if _, err := os.Stat(prefixPath); err != nil {
		return nil, &ErrPrefixMissing{
			GameID:       o.PrefixGameID,
			ExpectedPath: prefixPath,
		}
	}

	if !steamIsRunning() {
		return nil, &ErrSteamNotRunning{}
	}

	var prefixLock *sync.Mutex
	locked := false
	if !o.PrefixReserved {
		lockValue, _ := m.prefixLocks.LoadOrStore(prefixPath, &sync.Mutex{})
		prefixLock = lockValue.(*sync.Mutex)
		prefixLock.Lock()
		locked = true
	}
	defer func() {
		if locked {
			prefixLock.Unlock()
		}
	}()

	protonPath := o.GameCfg.ProtonPath
	if protonPath == "" {
		protonPath = o.PreferredProton
	}
	if protonPath == "" {
		versions := detectProtonVersionsAllLibraries(steamRoot)
		if len(versions) == 0 {
			return nil, fmt.Errorf("no Proton versions found")
		}
		protonPath = versions[0].Path
	}

	roPaths := append([]string(nil), o.ROPaths...)
	roPaths = append(roPaths, symlinkTargets(o.RWPaths)...)
	env := buildExternalEnv(
		o.SanitizeEnv, compatDataPath, steamRoot, appID,
		o.GameCfg.InstallPath, normalizedPaths(o.RWPaths), normalizedPaths(roPaths), o.ExtraEnv,
	)

	bin := protonPath
	cmdArgs := append([]string{"waitforexitandrun", o.ExePath}, o.Args...)
	if entryPoint, runtimeName := ResolveProtonRuntime(protonPath, steamRoot); entryPoint != "" {
		cmdArgs = append([]string{"--verb=waitforexitandrun", "--", protonPath}, cmdArgs...)
		bin = entryPoint
		slog.Info("LaunchExternal: invoking Proton through Steam Linux Runtime",
			"runtime", runtimeName, "entry_point", entryPoint, "proton", protonPath)
	}

	workDir := o.WorkingDir
	if workDir == "" {
		workDir = filepath.Dir(o.ExePath)
	}

	cmd := exec.Command(bin, cmdArgs...)
	cmd.Env = env
	cmd.Dir = workDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	slog.Info("launching external exe via Proton",
		"prefix_game", o.PrefixGameID, "exe", o.ExePath, "proton", protonPath)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting Proton wrapper: %w", err)
	}
	releaseOnWait := locked
	locked = false

	go logProtonOutput(o.PrefixGameID+":external:stdout", "stdout", stdoutPipe)
	go logProtonOutput(o.PrefixGameID+":external:stderr", "stderr", stderrPipe)

	done := make(chan struct{})
	exitCh := make(chan int, 1)
	go func() {
		if releaseOnWait {
			defer prefixLock.Unlock()
		}
		err := cmd.Wait()
		code := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				code = exitErr.ExitCode()
			} else {
				code = -1
			}
		}
		exitCh <- code
		close(done)
	}()

	return &ExternalLaunchHandle{
		PID:        cmd.Process.Pid,
		Done:       done,
		ExitCode:   exitCh,
		cmd:        cmd,
		prefixPath: prefixPath,
	}, nil
}

func (m *Manager) ReservePrefix(gameCfg *config.GameConfig, prefixAppID int) (func(), error) {
	compatDataPath, err := ResolveCompatDataPath(gameCfg, prefixAppID)
	if err != nil {
		return nil, err
	}
	prefixPath := filepath.Join(compatDataPath, "pfx")
	if info, statErr := os.Stat(prefixPath); statErr != nil || !info.IsDir() {
		return nil, fmt.Errorf("Proton prefix is missing: %s", prefixPath)
	}
	return m.reservePrefixPath(prefixPath)
}

func (m *Manager) reservePrefixPath(prefixPath string) (func(), error) {
	lockValue, _ := m.prefixLocks.LoadOrStore(prefixPath, &sync.Mutex{})
	prefixLock := lockValue.(*sync.Mutex)
	if !prefixLock.TryLock() {
		return nil, fmt.Errorf("Proton prefix is already in use by another managed tool: %s", prefixPath)
	}
	var once sync.Once
	return func() {
		once.Do(prefixLock.Unlock)
	}, nil
}

type ExternalLaunchHandle struct {
	PID      int
	Done     <-chan struct{}
	ExitCode <-chan int

	cmd        *exec.Cmd
	prefixPath string
}

type NativeLaunchOpts struct {
	ToolID      string
	ExePath     string
	Args        []string
	ExtraEnv    []string
	WorkingDir  string
	JavaArchive bool
	SanitizeEnv bool
}

// LaunchNative starts a waitable host-native tool using the same lifecycle handle as Proton tools.
func (m *Manager) LaunchNative(options NativeLaunchOpts) (*ExternalLaunchHandle, error) {
	binary := options.ExePath
	args := append([]string(nil), options.Args...)
	if options.JavaArchive {
		java, err := exec.LookPath("java")
		if err != nil {
			return nil, errors.New("Java runtime is required for this tool")
		}
		binary = java
		args = append([]string{"-jar", options.ExePath}, args...)
	}
	workingDir := options.WorkingDir
	if workingDir == "" {
		workingDir = filepath.Dir(options.ExePath)
	}
	cmd := exec.Command(binary, args...)
	cmd.Dir = workingDir
	baseEnv := os.Environ()
	if options.SanitizeEnv {
		baseEnv = sanitizedHostEnv(baseEnv)
	}
	cmd.Env = append(baseEnv, options.ExtraEnv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting native tool: %w", err)
	}
	go logProtonOutput(options.ToolID+":native:stdout", "stdout", stdoutPipe)
	go logProtonOutput(options.ToolID+":native:stderr", "stderr", stderrPipe)
	done := make(chan struct{})
	exitCh := make(chan int, 1)
	go func() {
		code := 0
		if err := cmd.Wait(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				code = exitErr.ExitCode()
			} else {
				code = -1
			}
		}
		exitCh <- code
		close(done)
	}()
	return &ExternalLaunchHandle{PID: cmd.Process.Pid, Done: done, ExitCode: exitCh, cmd: cmd}, nil
}

func sanitizedHostEnv(environment []string) []string {
	out := make([]string, 0, len(environment))
	for _, value := range environment {
		separator := strings.IndexByte(value, '=')
		if separator < 0 || dropEnvKeys[value[:separator]] {
			continue
		}
		out = append(out, value)
	}
	return out
}

// Cancel runs the three-step Wine-aware shutdown: SIGTERM, wineserver -k, SIGKILL.
func (h *ExternalLaunchHandle) Cancel(ctx context.Context) {
	if h.cmd == nil || h.cmd.Process == nil {
		return
	}
	pid := h.cmd.Process.Pid
	pgid, _ := syscall.Getpgid(pid)
	if pgid <= 0 {
		pgid = pid
	}

	slog.Info("Cancel: sending SIGTERM to Proton wrapper", "pid", pid, "pgid", pgid)
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	if waitWithTimeout(h.Done, 5*time.Second, ctx) {
		return
	}

	if h.prefixPath != "" {
		slog.Warn("Cancel: SIGTERM did not take, running wineserver -k", "prefix", h.prefixPath)
	}
	if wineserver, err := exec.LookPath("wineserver"); h.prefixPath != "" && err == nil {
		ks := exec.Command(wineserver, "-k")
		ks.Env = append(os.Environ(), "WINEPREFIX="+h.prefixPath)
		if err := ks.Run(); err != nil {
			slog.Warn("wineserver -k failed", "err", err)
		}
	} else if h.prefixPath != "" {
		slog.Warn("wineserver not on PATH — skipping step 2", "err", err)
	}
	if waitWithTimeout(h.Done, 5*time.Second, ctx) {
		return
	}

	slog.Warn("Cancel: process still alive, sending SIGKILL", "pid", pid, "pgid", pgid)
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

// waitWithTimeout returns true if done closed before the timeout or ctx was cancelled.
func waitWithTimeout(done <-chan struct{}, d time.Duration, ctx context.Context) bool {
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	case <-ctx.Done():
		return false
	}
}

// buildExternalEnv constructs the env slice for a LaunchExternal call.
func buildExternalEnv(
	sanitizeEnv bool,
	compatDataPath, steamRoot, appID, installPath string,
	rwPaths, roPaths []string,
	extraEnv []string,
) []string {
	var env []string
	if sanitizeEnv {
		for _, kv := range os.Environ() {
			eq := strings.IndexByte(kv, '=')
			if eq < 0 {
				continue
			}
			if dropEnvKeys[kv[:eq]] {
				continue
			}
			env = append(env, kv)
		}
	} else {
		env = append(env, os.Environ()...)
	}

	env = append(env, buildSteamParityEnv(
		compatDataPath, steamRoot, appID, installPath, "",
	)...)

	if sanitizeEnv {
		env = append(env,
			"DXVK_LOG_LEVEL=none",
			"PROTON_NO_ESYNC=1",
			"PROTON_NO_FSYNC=1",
		)
	}

	if len(rwPaths) > 0 {
		env = append(env, "PRESSURE_VESSEL_FILESYSTEMS_RW="+strings.Join(rwPaths, ":"))
	}
	if len(roPaths) > 0 {
		env = append(env, "PRESSURE_VESSEL_FILESYSTEMS_RO="+strings.Join(roPaths, ":"))
	}

	env = append(env, extraEnv...)
	return env
}

func resolveSteamLibrary(steamRoot string, gameCfg *config.GameConfig) string {
	if gameCfg != nil && gameCfg.SteamLibraryPath != "" {
		if info, err := os.Stat(filepath.Join(gameCfg.SteamLibraryPath, "steamapps")); err == nil && info.IsDir() {
			return gameCfg.SteamLibraryPath
		}
	}
	if gameCfg != nil {
		install, _ := filepath.Abs(gameCfg.InstallPath)
		for _, library := range steamLibraries(steamRoot) {
			common, _ := filepath.Abs(filepath.Join(library, "steamapps", "common"))
			if install == common || strings.HasPrefix(install, common+string(filepath.Separator)) {
				return library
			}
		}
	}
	return steamRoot
}

// ResolveSteamLibrary returns the Steam library that owns a configured game installation.
func ResolveSteamLibrary(gameCfg *config.GameConfig) (string, error) {
	steamRoot, err := findSteamRoot()
	if err != nil {
		return "", err
	}
	return resolveSteamLibrary(steamRoot, gameCfg), nil
}

// ResolveCompatDataPath returns the compatdata directory for a game or tool-specific Steam app ID.
func ResolveCompatDataPath(gameCfg *config.GameConfig, prefixAppID int) (string, error) {
	steamRoot, err := findSteamRoot()
	if err != nil {
		return "", err
	}
	if gameCfg == nil {
		return "", errors.New("game config is required")
	}
	appID := gameCfg.SteamAppID
	if prefixAppID > 0 {
		appID = prefixAppID
	}
	library := resolveSteamLibrary(steamRoot, gameCfg)
	return findCompatDataPath(steamRoot, library, strconv.Itoa(appID)), nil
}

func steamLibraries(steamRoot string) []string {
	seen := map[string]bool{}
	out := make([]string, 0)
	for _, library := range append([]string{steamRoot}, parseLibraryFolders(steamRoot)...) {
		clean := filepath.Clean(library)
		if clean == "." || seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, clean)
	}
	return out
}

func findCompatDataPath(steamRoot, preferredLibrary, appID string) string {
	libraries := append([]string{preferredLibrary}, steamLibraries(steamRoot)...)
	seen := map[string]bool{}
	for _, library := range libraries {
		candidate := filepath.Join(library, "steamapps", "compatdata", appID)
		if seen[candidate] {
			continue
		}
		seen[candidate] = true
		if info, err := os.Stat(filepath.Join(candidate, "pfx")); err == nil && info.IsDir() {
			return candidate
		}
	}
	return filepath.Join(preferredLibrary, "steamapps", "compatdata", appID)
}

func normalizedPaths(paths []string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(paths))
	add := func(path string) {
		if path == "" {
			return
		}
		abs, err := filepath.Abs(path)
		if err != nil || seen[abs] {
			return
		}
		seen[abs] = true
		out = append(out, abs)
	}
	for _, path := range paths {
		add(path)
	}
	sort.Strings(out)
	return out
}

func symlinkTargets(paths []string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0)
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			continue
		}
		_ = filepath.Walk(path, func(walkPath string, info os.FileInfo, walkErr error) error {
			if walkErr != nil || info == nil || info.Mode()&os.ModeSymlink == 0 {
				return nil
			}
			target, err := filepath.EvalSymlinks(walkPath)
			if err != nil || seen[target] {
				return nil
			}
			seen[target] = true
			out = append(out, target)
			return nil
		})
	}
	sort.Strings(out)
	return out
}

// steamIsRunning reports whether a process named exactly "steam" is currently running.
func steamIsRunning() bool {
	pgrep, err := exec.LookPath("pgrep")
	if err != nil {
		slog.Warn("pgrep not on PATH — skipping Steam-running pre-check")
		return true
	}
	cmd := exec.Command(pgrep, "-x", "steam")
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

// WineTranslatePath converts a Linux path to its Wine equivalent inside the prefix.
func (m *Manager) WineTranslatePath(prefixGameID string, gameCfg *config.GameConfig, unixPath string) (string, error) {
	if gameCfg == nil {
		return "", fmt.Errorf("WineTranslatePath: gameCfg is nil")
	}
	if unixPath == "" {
		return "", nil
	}
	abs, err := filepath.Abs(unixPath)
	if err != nil {
		return "", fmt.Errorf("absolute path of %s: %w", unixPath, err)
	}

	steamRoot, err := findSteamRoot()
	if err == nil {
		appID := strconv.Itoa(gameCfg.SteamAppID)
		libraryRoot := resolveSteamLibrary(steamRoot, gameCfg)
		prefixPath := filepath.Join(findCompatDataPath(steamRoot, libraryRoot, appID), "pfx")
		if winepath, lerr := exec.LookPath("winepath"); lerr == nil {
			cmd := exec.Command(winepath, "-w", abs)
			cmd.Env = append(os.Environ(), "WINEPREFIX="+prefixPath)
			if out, runErr := cmd.Output(); runErr == nil {
				translated := strings.TrimSpace(string(out))
				if translated != "" {
					return translated, nil
				}
			} else {
				slog.Debug("winepath -w failed, falling back to Z:\\ rewrite",
					"path", abs, "err", runErr)
			}
		}
	}

	return `Z:` + strings.ReplaceAll(abs, `/`, `\`), nil
}

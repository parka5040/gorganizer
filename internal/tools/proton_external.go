package tools

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/ipc"
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

// ExternalLaunchOpts parameterizes LaunchExternalWithOptions. It is a superset
// of the historical LaunchExternal arguments plus WorkingDir.
type ExternalLaunchOpts struct {
	PrefixGameID    string
	GameCfg         *config.GameConfig
	ExePath         string
	Args            []string
	ExtraEnv        []string
	PreferredProton string
	SanitizeEnv     bool
	RWPaths         []string
	// WorkingDir is the process CWD; empty preserves the historical behavior of
	// filepath.Dir(ExePath).
	WorkingDir string
}

// LaunchExternal launches a Windows executable inside a Proton prefix for
// installers and helper tools. Preserved shim over LaunchExternalWithOptions so
// existing callers (the TTW installer) keep byte-identical behavior.
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
// .exe run through the game's Proton prefix against the mounted VFS.
func (m *Manager) LaunchExternalWithOptions(o ExternalLaunchOpts) (*ExternalLaunchHandle, error) {
	if o.GameCfg == nil {
		return nil, fmt.Errorf("LaunchExternal: gameCfg is nil")
	}

	steamRoot, err := findSteamRoot()
	if err != nil {
		return nil, fmt.Errorf("finding Steam root: %w", err)
	}

	appID := strconv.Itoa(o.GameCfg.SteamAppID)
	compatDataPath := filepath.Join(steamRoot, "steamapps", "compatdata", appID)
	prefixPath := filepath.Join(compatDataPath, "pfx")

	if _, err := os.Stat(prefixPath); err != nil {
		return nil, &ipc.ErrPrefixMissing{
			GameID:       o.PrefixGameID,
			ExpectedPath: prefixPath,
		}
	}

	if !steamIsRunning() {
		return nil, &ipc.ErrSteamNotRunning{}
	}

	protonPath := o.GameCfg.ProtonPath
	if protonPath == "" {
		protonPath = o.PreferredProton
	}
	if protonPath == "" {
		versions := detectProtonVersions(steamRoot)
		if len(versions) == 0 {
			return nil, fmt.Errorf("no Proton versions found")
		}
		protonPath = versions[0].Path
	}

	env := buildExternalEnv(
		o.SanitizeEnv, compatDataPath, steamRoot, appID,
		o.GameCfg.InstallPath, o.RWPaths, o.ExtraEnv,
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

	go logProtonOutput(o.PrefixGameID+":external:stdout", "stdout", stdoutPipe)
	go logProtonOutput(o.PrefixGameID+":external:stderr", "stderr", stderrPipe)

	done := make(chan struct{})
	exitCh := make(chan int, 1)
	go func() {
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

// ExternalLaunchHandle is LaunchExternal's counterpart to LaunchHandle, with Cancel and ExitCode.
type ExternalLaunchHandle struct {
	PID      int
	Done     <-chan struct{}
	ExitCode <-chan int

	cmd        *exec.Cmd
	prefixPath string
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

	slog.Warn("Cancel: SIGTERM did not take, running wineserver -k", "prefix", h.prefixPath)
	if wineserver, err := exec.LookPath("wineserver"); err == nil {
		ks := exec.Command(wineserver, "-k")
		ks.Env = append(os.Environ(), "WINEPREFIX="+h.prefixPath)
		if err := ks.Run(); err != nil {
			slog.Warn("wineserver -k failed", "err", err)
		}
	} else {
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
	rwPaths []string,
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

	env = append(env, extraEnv...)
	return env
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
		prefixPath := filepath.Join(steamRoot, "steamapps", "compatdata", appID, "pfx")
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

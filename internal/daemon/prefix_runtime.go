package daemon

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/parka/gorganizer/internal/config"
)

var (
	prefixRuntimeInstalled   = map[int]bool{}
	prefixRuntimeInstalledMu sync.Mutex
)

var prefixRuntimePackages = map[string][]string{
	"falloutnv": {"vcrun2022", "d3dx9", "xact"},
	"fallout3":  {"vcrun2022", "d3dx9", "xact"},
	"oblivion":  {"vcrun2022", "d3dx9", "xact"},
	"skyrim":    {"vcrun2022", "d3dx9"},
	"skyrimse":  {"vcrun2022"},
	"fallout4":  {"vcrun2022"},
	"starfield": {"vcrun2022"},
}

// ensurePrefixRuntime runs protontricks against the game's Proton
// prefix to install the Windows redistributables heavy mod loadouts
func (d *Daemon) ensurePrefixRuntime(gameID string, gc config.GameConfig) {
	pkgs, ok := prefixRuntimePackages[gameID]
	if !ok || len(pkgs) == 0 {
		return
	}

	appID := gc.SteamAppID
	if appID == 0 {
		return
	}

	prefixRuntimeInstalledMu.Lock()
	if prefixRuntimeInstalled[appID] {
		prefixRuntimeInstalledMu.Unlock()
		return
	}
	prefixRuntimeInstalledMu.Unlock()

	if _, err := exec.LookPath("protontricks"); err != nil {
		slog.Warn("protontricks not found on PATH — heavy mod loadouts may crash without DX9/VC++ redists; install protontricks from your distro (pacman/emerge/apt/flatpak) to silence this",
			"game", gameID, "packages_needed", pkgs)
		return
	}

	script := "winetricks -q " + joinPkgs(pkgs)
	args := []string{"--no-bwrap", "-c", script, strconv.Itoa(appID)}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "protontricks", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	slog.Info("installing Proton prefix runtime via protontricks",
		"game", gameID, "app_id", appID, "packages", pkgs)

	if err := cmd.Run(); err != nil {
		slog.Warn("protontricks failed — modded launches may crash until the missing redists are installed manually",
			"game", gameID, "err", err,
			"stdout", trimForLog(stdout.String()),
			"stderr", trimForLog(stderr.String()),
			"hint", fmt.Sprintf("try: protontricks %d %s", appID, joinPkgs(pkgs)))
		return
	}

	prefixRuntimeInstalledMu.Lock()
	prefixRuntimeInstalled[appID] = true
	prefixRuntimeInstalledMu.Unlock()

	slog.Info("Proton prefix runtime ready",
		"game", gameID, "app_id", appID, "packages", pkgs)
}

// joinPkgs is a local space-join so we don't pull in strings just for
// one call inside prefix_runtime.
func joinPkgs(pkgs []string) string {
	out := ""
	for i, p := range pkgs {
		if i > 0 {
			out += " "
		}
		out += p
	}
	return out
}

// trimForLog clamps a winetricks buffer to something that fits comfortably
// in a single slog line while still being diagnostic.
func trimForLog(s string) string {
	const max = 1024
	if len(s) <= max {
		return s
	}
	return s[:max] + "…(truncated)"
}

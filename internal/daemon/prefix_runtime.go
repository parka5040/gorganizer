package daemon

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/gamedef"
	"github.com/parka/gorganizer/internal/tools"
)

var (
	prefixRuntimeInstalled   = map[string]bool{}
	prefixRuntimeInstalledMu sync.Mutex
)

// ensurePrefixRuntime runs protontricks against the game's Proton
func (ls *LaunchService) ensurePrefixRuntime(gameID string, gc config.GameConfig) {
	g, ok := gamedef.ByID(gameID)
	if !ok || len(g.RedistPackages) == 0 {
		return
	}
	pkgs := g.RedistPackages

	appID := gc.SteamAppID
	if appID == 0 {
		return
	}
	compatData, err := tools.ResolveCompatDataPath(&gc, 0)
	if err != nil {
		slog.Warn("could not resolve Proton prefix for runtime setup", "game", gameID, "err", err)
		return
	}

	prefixRuntimeInstalledMu.Lock()
	if prefixRuntimeInstalled[compatData] {
		prefixRuntimeInstalledMu.Unlock()
		return
	}
	prefixRuntimeInstalledMu.Unlock()

	if _, err := exec.LookPath("protontricks"); err != nil {
		slog.Warn("protontricks not found on PATH — heavy mod loadouts may crash without DX9/VC++ redists; install protontricks from your distro (pacman/emerge/apt/flatpak) to silence this",
			"game", gameID, "packages_needed", pkgs)
		return
	}

	args := []string{"--no-bwrap", fmt.Sprintf("%d", appID), "-q"}
	args = append(args, pkgs...)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "protontricks", args...)
	cmd.Env = append(os.Environ(), "STEAM_COMPAT_DATA_PATH="+compatData)
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
	prefixRuntimeInstalled[compatData] = true
	prefixRuntimeInstalledMu.Unlock()

	slog.Info("Proton prefix runtime ready",
		"game", gameID, "app_id", appID, "packages", pkgs)
}

// joinPkgs is a local space-join so we don't pull in strings just for
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
func trimForLog(s string) string {
	const max = 1024
	if len(s) <= max {
		return s
	}
	return s[:max] + "…(truncated)"
}

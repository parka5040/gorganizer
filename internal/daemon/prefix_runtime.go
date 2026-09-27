package daemon

import (
	"bytes"
	"context"
	"log/slog"
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

// ensurePrefixRuntime installs the game's declared redistributables into its Proton prefix via protontricks.
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

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	invocation, err := ls.s.protontricksInvocation(ctx)
	if err != nil {
		slog.Warn("protontricks not installed; skipping prefix runtime install", "game", gameID, "packages", pkgs)
		return
	}
	library, err := tools.ResolveSteamLibrary(&gc)
	if err != nil {
		slog.Warn("could not resolve Steam library for runtime setup", "game", gameID, "err", err)
		return
	}
	cmd := invocation.Command(ctx, appID, compatData, []string{library}, pkgs)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	slog.Info("installing Proton prefix runtime via protontricks",
		"game", gameID, "app_id", appID, "packages", pkgs, "kind", invocation.Kind())

	if err := cmd.Run(); err != nil {
		slog.Warn("protontricks failed — modded launches may crash until the missing redists are installed manually",
			"game", gameID, "err", err,
			"stdout", trimForLog(stdout.String()),
			"stderr", trimForLog(stderr.String()))
		return
	}

	prefixRuntimeInstalledMu.Lock()
	prefixRuntimeInstalled[compatData] = true
	prefixRuntimeInstalledMu.Unlock()

	slog.Info("Proton prefix runtime ready",
		"game", gameID, "app_id", appID, "packages", pkgs)
}

// trimForLog truncates winetricks output to a length that fits in a single log line.
func trimForLog(s string) string {
	const max = 1024
	if len(s) <= max {
		return s
	}
	return s[:max] + "…(truncated)"
}

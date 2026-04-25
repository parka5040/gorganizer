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

// prefixRuntimeInstalled memoizes successful protontricks runs per app
// ID so clicking "Install xNVSE" repeatedly (or two launches in quick
// succession) doesn't re-hit Wine's MSI installer — winetricks is slow
// and re-runs would stack dialogs on a non-quiet build. Persistent state
// isn't needed: a daemon restart re-checks, but winetricks with `-q`
// short-circuits already-installed packages in a few seconds.
var (
	prefixRuntimeInstalled   = map[int]bool{}
	prefixRuntimeInstalledMu sync.Mutex
)

// prefixRuntimePackages maps internal gameIDs to the winetricks packages
// a heavy modded loadout of that title needs inside the Proton prefix.
// The DX9 Bethesda engines (Fallout 3/NV, Oblivion, Skyrim LE) drive
// shader pipelines through d3dx9_XX.dll — Wine ships a stub that works
// for vanilla but breaks under mods that call extension functions (ENB,
// most body replacers, script-extender plugins that hook rendering).
// vcrun2022 covers the MSVC redist xNVSE + extender plugins link against.
// xact covers the audio redist FNV reaches for on first launch.
//
// DX11 titles (Skyrim SE, Fallout 4, Starfield) only need the MSVC
// redistributable — Proton's DXVK handles the graphics stack natively.
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
// depend on (VC++ runtime, DirectX 9, XAudio). Safe to call multiple
// times: winetricks in quiet mode skips anything already installed, and
// we memoize successful runs for the daemon's lifetime.
//
// protontricks is declared as a runtime dependency — its absence is a
// loud warning pointing the user at their package manager (the project
// targets Artix and Gentoo alongside systemd distros, so we can't assume
// any specific packaging).
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

	// Invoke winetricks through protontricks. The `-c` form hands a
	// shell command to winetricks in-prefix; the `-q` flag inside that
	// command puts winetricks in unattended mode so no GUI dialogs
	// appear. `--no-bwrap` bypasses the bubblewrap sandbox, which
	// often misfires on Arch/Artix kernels with restricted user NS.
	script := "winetricks -q " + joinPkgs(pkgs)
	args := []string{"--no-bwrap", "-c", script, strconv.Itoa(appID)}

	// 15 minutes is generous — cold first-run winetricks can download
	// ~300 MB of redists. Subsequent runs finish in seconds.
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

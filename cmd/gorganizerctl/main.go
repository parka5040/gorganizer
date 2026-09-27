package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/daemon"
	"github.com/parka/gorganizer/internal/game"
	"github.com/parka/gorganizer/internal/instancelock"
	"github.com/parka/gorganizer/internal/procscan"
	"github.com/parka/gorganizer/internal/vfs"
)

var version = "dev"

// main dispatches the first argument to its subcommand and exits with its status.
func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	subcommand := os.Args[1]
	args := os.Args[2:]

	switch subcommand {
	case "ping":
		os.Exit(runPing(args))
	case "wait-ready":
		os.Exit(runWaitReady(args))
	case "stop":
		os.Exit(runStop(args))
	case "session":
		os.Exit(runSession(args))
	case "recover":
		os.Exit(runRecover(args))
	case "recover-confirm":
		os.Exit(runRecoverConfirm(args))
	case "export":
		os.Exit(runExport(args))
	case "import":
		os.Exit(runImport(args))
	case "--version":
		printVersion(os.Stdout)
		return
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", subcommand)
		usage()
		os.Exit(2)
	}
}

// printVersion prints the maintenance command's build version.
func printVersion(out io.Writer) {
	fmt.Fprintf(out, "gorganizerctl %s\n", version)
}

type recoveryDeps struct {
	procRoot string
	out      io.Writer
	errOut   io.Writer
}

type recoveryTarget struct {
	dataPath    string
	installPath string
	name        string
	appIDs      []int
	config      *config.Config
}

// runRecoverConfirm restores a Data backup after confirmation while the daemon and game are stopped.
func runRecoverConfirm(args []string) int {
	return runRecoverConfirmWith(args, recoveryDeps{procRoot: "/proc", out: os.Stdout, errOut: os.Stderr})
}

// runRecoverConfirmWith performs the confirmed restore using the supplied process table and output streams.
func runRecoverConfirmWith(args []string, deps recoveryDeps) int {
	fs := flag.NewFlagSet("recover-confirm", flag.ContinueOnError)
	fs.SetOutput(deps.errOut)
	dataPath := fs.String("data-path", "", "absolute path to the Data folder to restore")
	_ = fs.String("socket-path", "", "legacy option; the shared instance lock is used instead")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dataPath == "" {
		fmt.Fprintln(deps.errOut, "error: --data-path is required")
		return 2
	}
	return withOfflineRecovery(deps, "", *dataPath, func(target recoveryTarget) int {
		if err := vfs.RestoreFromBackup(target.dataPath); err != nil {
			fmt.Fprintf(deps.errOut, "error: %v\n", err)
			return 1
		}
		fmt.Fprintln(deps.out, "The Data folder was restored from its backup.")
		return 0
	})
}

// usage prints the subcommand help to stderr.
func usage() {
	fmt.Fprint(os.Stderr, `gorganizerctl — gorganizer maintenance CLI

Subcommands:
  ping                         Check whether Gorganizer is running.
  wait-ready [--timeout 60s]   Wait for startup to finish.
  stop [--timeout 46s]         Ask Gorganizer to stop and wait for it to exit.
  session [--daemon PATH] [--gui PATH] [--socket-path P] [-- GUI_ARGS…]
                               Open Gorganizer and supervise its background service.
  recover --game <id>          Repair interrupted SMAPI, game-root files and
                               the Data folder for a configured game.
  recover --data-path <path>   Check only the specified Data folder.
  recover-confirm --data-path <path>
                               Restore a Data backup after inspecting it.
  export --game <id> --out <file>
                               Export the game's instance via the daemon.
                               Optional: --mods a,b  --profiles p1,p2
                               --no-overwrite  --no-game-settings
  import --game <id> --archive <file>
                               Import an archive via the daemon. Optional:
                               --policy abort|skip|rename|overwrite
                               --dry-run (preview contents and conflicts)

Close Gorganizer and the game before recovery. Recovery holds the same
instance lock as the daemon; export/import require the daemon to be running.

Examples:
  gorganizerctl recover --game falloutnv
  gorganizerctl recover --data-path "$HOME/.local/share/Steam/steamapps/common/Fallout New Vegas/Data"
  gorganizerctl export --game skyrimse --out ~/skyrimse-instance.tar.zst
  gorganizerctl import --game skyrimse --archive ~/skyrimse-instance.tar.zst --policy rename
`)
}

// runRecover runs offline crash recovery while the daemon and game are stopped.
func runRecover(args []string) int {
	return runRecoverWith(args, recoveryDeps{procRoot: "/proc", out: os.Stdout, errOut: os.Stderr})
}

// runRecoverWith checks the Data folder or configured game's recovery state using the supplied process table.
func runRecoverWith(args []string, deps recoveryDeps) int {
	fs := flag.NewFlagSet("recover", flag.ContinueOnError)
	fs.SetOutput(deps.errOut)
	gameID := fs.String("game", "", "configured game id (for example falloutnv or skyrimse)")
	dataPath := fs.String("data-path", "", "absolute path to the game's Data folder (Data-only recovery)")
	_ = fs.String("socket-path", "", "legacy option; the shared instance lock is used instead")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *gameID == "" && *dataPath == "" {
		fmt.Fprintln(deps.errOut, "error: one of --game or --data-path is required")
		fs.Usage()
		return 2
	}
	return withOfflineRecovery(deps, *gameID, *dataPath, func(target recoveryTarget) int {
		if *dataPath != "" {
			outcome, err := vfs.CleanupStale(target.dataPath)
			if err != nil {
				fmt.Fprintf(deps.errOut, "error: recovery failed: %v\n", err)
				return 1
			}
			printRecoveryStep(deps.out, "Data folder", daemon.OfflineRecoveryStep{Recovered: outcome.Restored || outcome.FuseUnmounted, Pending: pendingReason(outcome.Pending)})
			fmt.Fprintln(deps.out, "Only the Data folder was checked. Use --game to also repair root files and SMAPI.")
			if outcome.Pending != nil {
				printConfirmationHint(deps.out, target.dataPath)
				return 2
			}
			return 0
		}
		report, err := daemon.RecoverGameOffline(target.config, *gameID)
		printRecoveryStep(deps.out, "SMAPI", report.Loader)
		printRecoveryStep(deps.out, "Game-root files", report.Root)
		printRecoveryStep(deps.out, "Data folder", report.Data)
		if err != nil {
			fmt.Fprintf(deps.errOut, "error: recovery failed: %v\n", err)
			return 1
		}
		if report.Data.Pending != "" {
			printConfirmationHint(deps.out, target.dataPath)
		}
		if report.Loader.Pending != "" || report.Root.Pending != "" || report.Data.Pending != "" {
			return 2
		}
		return 0
	})
}

// withOfflineRecovery holds the instance lock and checks for a live game before calling the recovery operation.
func withOfflineRecovery(deps recoveryDeps, gameID, dataPath string, recoverFn func(recoveryTarget) int) int {
	release, err := instancelock.Acquire()
	if errors.Is(err, instancelock.ErrHeld) {
		fmt.Fprintln(deps.errOut, "Gorganizer is running. Close it first, then run this command again.")
		return 1
	}
	if err != nil {
		fmt.Fprintf(deps.errOut, "error: %v\n", err)
		return 1
	}
	defer release()

	target, err := resolveRecoveryTarget(gameID, dataPath)
	if err != nil {
		fmt.Fprintf(deps.errOut, "error: %v\n", err)
		return 1
	}
	running, err := procscan.RunningIn(deps.procRoot, target.installPath, target.appIDs)
	if running || err != nil || daemon.LaunchTicketBlocksRecovery(target.dataPath, time.Now()) != "" {
		fmt.Fprintf(deps.errOut, "%s is running. Close the game, then run this command again. Nothing was changed.\n", target.name)
		return 1
	}
	return recoverFn(target)
}

// resolveRecoveryTarget finds the game's install and Steam app IDs from config or the existing Steam discovery fallback.
func resolveRecoveryTarget(gameID, dataPath string) (recoveryTarget, error) {
	if dataPath != "" {
		absolute, err := filepath.Abs(dataPath)
		if err != nil {
			return recoveryTarget{}, fmt.Errorf("resolving Data folder: %w", err)
		}
		dataPath = absolute
	}
	cfg, err := config.Load()
	if err != nil {
		return recoveryTarget{}, fmt.Errorf("reading game settings: %w", err)
	}
	target := recoveryTarget{config: cfg, name: "The game"}
	if dataPath != "" {
		target.dataPath = dataPath
		target.installPath = filepath.Dir(dataPath)
		ids := make([]string, 0, len(cfg.Games))
		for id := range cfg.Games {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			gc, err := cfg.EffectiveGameConfig(id)
			if err == nil && filepath.Clean(filepath.Join(gc.InstallPath, dataSubpath(gc))) == filepath.Clean(dataPath) {
				target.installPath = gc.InstallPath
				target.name = gameName(id, cfg.Games[id].Name)
				target.appIDs = appendAppID(target.appIDs, gc.SteamAppID)
			}
		}
		return target, nil
	}
	if gc, ok := cfg.Games[gameID]; ok {
		effective, err := cfg.EffectiveGameConfig(gameID)
		if err != nil {
			return recoveryTarget{}, err
		}
		if effective.InstallPath == "" {
			return recoveryTarget{}, fmt.Errorf("no install path is configured for %s", gameID)
		}
		target.dataPath = filepath.Join(effective.InstallPath, dataSubpath(effective))
		target.installPath = effective.InstallPath
		target.name = gameName(gameID, gc.Name)
		target.appIDs = appendAppID(target.appIDs, effective.SteamAppID)
		if gc.LinkedFromGameID != "" {
			target.appIDs = appendAppID(target.appIDs, cfg.Games[gc.LinkedFromGameID].SteamAppID)
		}
		return target, nil
	}
	detected, err := game.DetectInstalledGames()
	if err != nil {
		return recoveryTarget{}, fmt.Errorf("detecting installed games: %w", err)
	}
	for _, g := range detected {
		if g.ID == gameID {
			target.dataPath = g.DataPath
			target.installPath = g.InstallPath
			target.name = gameName(gameID, g.Name)
			target.appIDs = appendAppID(target.appIDs, int(g.SteamAppID))
			gc := config.GameConfig{Name: g.Name, InstallPath: g.InstallPath, DataSubpath: g.DataSubpath, SteamAppID: int(g.SteamAppID)}
			if g.ParentGameID != "" {
				for _, parent := range detected {
					if parent.ID == g.ParentGameID {
						cfg.Games[parent.ID] = config.GameConfig{Name: parent.Name, InstallPath: parent.InstallPath, DataSubpath: parent.DataSubpath, SteamAppID: int(parent.SteamAppID)}
						target.appIDs = appendAppID(target.appIDs, int(parent.SteamAppID))
						gc.LinkedFromGameID = parent.ID
						break
					}
				}
			}
			cfg.Games[gameID] = gc
			return target, nil
		}
	}
	return recoveryTarget{}, fmt.Errorf("could not resolve %q: not in config and no Steam-detected install matches; pass --data-path explicitly", gameID)
}

// resolveDataPath resolves the recover flags to a Data folder.
func resolveDataPath(gameID, dataPathFlag string) (string, error) {
	if dataPathFlag != "" {
		return dataPathFlag, nil
	}
	target, err := resolveRecoveryTarget(gameID, "")
	return target.dataPath, err
}

// dataSubpath returns a game's configured Data-folder name.
func dataSubpath(gc config.GameConfig) string {
	if gc.DataSubpath == "" {
		return "Data"
	}
	return gc.DataSubpath
}

// appendAppID adds a nonzero Steam app ID only once.
func appendAppID(ids []int, id int) []int {
	if id > 0 {
		for _, existing := range ids {
			if existing == id {
				return ids
			}
		}
		return append(ids, id)
	}
	return ids
}

// gameName returns the configured display name, the registered name, or a generic fallback.
func gameName(gameID, configured string) string {
	if configured != "" {
		return configured
	}
	if definition, ok := game.FindByID(gameID); ok {
		return definition.Name
	}
	return "The game"
}

// pendingReason extracts the description of a Data recovery that needs confirmation.
func pendingReason(pending *vfs.RecoveryPending) string {
	if pending != nil {
		return pending.Reason
	}
	return ""
}

// printRecoveryStep describes one recovery step in plain words.
func printRecoveryStep(out io.Writer, name string, step daemon.OfflineRecoveryStep) {
	switch {
	case step.Pending != "":
		fmt.Fprintf(out, "%s: needs confirmation (%s).\n", name, step.Pending)
	case step.Recovered:
		fmt.Fprintf(out, "%s: recovered.\n", name)
	default:
		fmt.Fprintf(out, "%s: nothing to do.\n", name)
	}
}

// printConfirmationHint shows how to restore a Data backup after inspecting it.
func printConfirmationHint(out io.Writer, dataPath string) {
	fmt.Fprintf(out, "Inspect the Data folder and its backup before proceeding. To restore the backup, run:\n  gorganizerctl recover-confirm --data-path %q\n", dataPath)
}

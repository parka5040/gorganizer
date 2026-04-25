package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/game"
	"github.com/parka/gorganizer/internal/vfs"
)

// gorganizerctl is the offline maintenance CLI for gorganizer. It deliberately
// talks to the same on-disk state as the daemon but never to the daemon
// itself — the most common reason to reach for this tool is "the daemon won't
// start because of a stale FUSE mount", and an IPC client would deadlock on
// the same socket.
func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	subcommand := os.Args[1]
	args := os.Args[2:]

	switch subcommand {
	case "recover":
		os.Exit(runRecover(args))
	case "recover-confirm":
		os.Exit(runRecoverConfirm(args))
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", subcommand)
		usage()
		os.Exit(2)
	}
}

// runRecoverConfirm performs the destructive Data → Data.orig restore the
// user explicitly confirmed via the prompt printed by `recover`. Kept
// separate so a misclick in the terminal can't escalate a diagnostic
// `recover` into data loss.
func runRecoverConfirm(args []string) int {
	fs := flag.NewFlagSet("recover-confirm", flag.ExitOnError)
	dataPath := fs.String("data-path", "", "absolute path to the Data dir to restore")
	socketPath := fs.String("socket-path", "",
		"path to the daemon socket (default: $XDG_RUNTIME_DIR/gorganizer/gorganizer.sock)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dataPath == "" {
		fmt.Fprintln(os.Stderr, "error: --data-path is required")
		return 2
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	sock := *socketPath
	if sock == "" {
		sock = config.SocketPath()
	}
	if isSocketLive(sock) {
		fmt.Fprintf(os.Stderr,
			"error: gorganizerd is currently running at %s\n"+
				"Stop the daemon before running recover-confirm.\n", sock)
		return 1
	}

	if err := vfs.RestoreFromBackup(*dataPath); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func usage() {
	fmt.Fprint(os.Stderr, `gorganizerctl — gorganizer offline maintenance

Subcommands:
  recover --game <id>          Tear down a stale FUSE mount and restore
                               Data.orig to Data for the given game.
  recover --data-path <path>   Same, but operate on a specific Data dir
                               without consulting the gorganizer config —
                               useful when the daemon was never set up.

Either form requires that gorganizerd not be currently running.

Examples:
  gorganizerctl recover --game falloutnv
  gorganizerctl recover --data-path "/home/me/.steam/steamapps/common/Fallout New Vegas/Data"
`)
}

func runRecover(args []string) int {
	fs := flag.NewFlagSet("recover", flag.ExitOnError)
	gameID := fs.String("game", "", "internal game id (e.g. falloutnv, skyrimse)")
	dataPath := fs.String("data-path", "", "absolute path to the game's Data dir (bypasses config)")
	socketPath := fs.String("socket-path", "",
		"path to the daemon socket (default: $XDG_RUNTIME_DIR/gorganizer/gorganizer.sock)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *gameID == "" && *dataPath == "" {
		fmt.Fprintln(os.Stderr, "error: one of --game or --data-path is required")
		fs.Usage()
		return 2
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// Refuse to run while the daemon is live — the daemon owns the mount
	// state and a concurrent fusermount would race against an active
	// activate/deactivate. Probe the socket rather than just check for
	// existence; a stale leftover socket file shouldn't block recovery.
	sock := *socketPath
	if sock == "" {
		sock = config.SocketPath()
	}
	if isSocketLive(sock) {
		fmt.Fprintf(os.Stderr,
			"error: gorganizerd is currently running at %s\n"+
				"Stop the daemon (close the GUI, or `pkill gorganizerd`) before running recovery.\n",
			sock)
		return 1
	}

	resolvedPath, err := resolveDataPath(*gameID, *dataPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	slog.Info("starting recovery", "game", *gameID, "data_path", resolvedPath)
	outcome, err := vfs.CleanupStale(resolvedPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: recovery failed: %v\n", err)
		return 1
	}
	if outcome.Pending != nil {
		// CLI path: the user IS the human consenting. Print the reason
		// and the explicit follow-up command rather than auto-restoring,
		// so destructive action stays gated on a deliberate second
		// invocation. (Future: add a `--restore-from-backup` flag.)
		fmt.Fprintf(os.Stderr,
			"\nrecovery is pending and requires manual confirmation:\n  %s\n\n"+
				"Inspect %q before deciding. To proceed with the\n"+
				"destructive restore (rm -rf Data, mv Data.orig Data), run:\n"+
				"  gorganizerctl recover-confirm --data-path %q\n",
			outcome.Pending.Reason, outcome.Pending.DataPath, outcome.Pending.DataPath)
		return 2
	}
	slog.Info("recovery finished", "data_path", resolvedPath,
		"fuse_unmounted", outcome.FuseUnmounted, "restored", outcome.Restored)
	return 0
}

// resolveDataPath turns the recover subcommand's flags into an absolute Data
// dir. Direct --data-path wins. Otherwise --game is resolved against the
// daemon config; if config is missing or doesn't know the game, fall back to
// scanning Steam libraries for an installed Bethesda title with the matching
// internal id. The Steam scan is what makes recovery work on a fresh install
// where the user never started the daemon.
func resolveDataPath(gameID, dataPathFlag string) (string, error) {
	if dataPathFlag != "" {
		return dataPathFlag, nil
	}

	cfg, err := config.Load()
	if err == nil {
		if path, gerr := cfg.GameDataPath(gameID); gerr == nil {
			return path, nil
		}
	}

	detected, err := game.DetectInstalledGames()
	if err != nil {
		return "", fmt.Errorf("detecting installed games: %w", err)
	}
	for _, g := range detected {
		if g.ID == gameID {
			return g.DataPath, nil
		}
	}
	return "", fmt.Errorf("could not resolve %q: not in config and no Steam-detected install matches; pass --data-path explicitly", gameID)
}

// isSocketLive returns true when something is actively accepting on the
// daemon socket. A stale socket file (left over from a kill -9) isn't a
// blocker — the connect will fail with ECONNREFUSED and we proceed.
func isSocketLive(sockPath string) bool {
	if _, err := os.Stat(sockPath); err != nil {
		return false
	}
	conn, err := net.DialTimeout("unix", sockPath, 200*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

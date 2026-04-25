package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	pb "github.com/parka/gorganizer/api/proto"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/daemon"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	configPath := flag.String("config", "", "Path to config.json (default: $XDG_CONFIG_HOME/gorganizer/config.json)")
	socketPath := flag.String("socket-path", "", "Path to Unix domain socket (default: $XDG_RUNTIME_DIR/gorganizer/gorganizer.sock)")
	logLevel := flag.String("log-level", "", "Log level (debug, info, warn, error)")
	handleNXM := flag.String("handle-nxm", "", "Forward NXM URI to running daemon and exit")
	flag.Parse()

	// Handle NXM mode: act as a gRPC client, not a server.
	if *handleNXM != "" {
		if err := forwardNXM(*handleNXM, *socketPath); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Load configuration.
	_ = configPath // Reserved for future use.
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading config: %v\n", err)
		os.Exit(1)
	}

	// Apply log level (flag overrides config).
	level := parseLogLevel(cfg.LogLevel)
	if *logLevel != "" {
		level = parseLogLevel(*logLevel)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
	})))

	// Resolve socket path.
	sock := config.SocketPath()
	if *socketPath != "" {
		sock = *socketPath
	}

	// Refuse to start if another gorganizerd is already running. Without
	// this, every invocation stacks another orphaned process because the
	// IPC layer silently rebinds the socket (see internal/ipc/server.go).
	// After 10 restarts the user ends up with 10 daemons quietly
	// competing over mount state, download dirs, and status streams.
	releaseLock, err := acquireSingleInstanceLock()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer releaseLock()

	// Create daemon.
	d, err := daemon.New(cfg)
	if err != nil {
		slog.Error("failed to create daemon", "err", err)
		os.Exit(1)
	}

	// Crash recovery and Steam scan now run inside the daemon's
	// warmupAsync goroutine, kicked off after the gRPC socket binds.
	// This lets the frontend's splash screen connect immediately and
	// poll Health() for progress, instead of blocking the user on a
	// silent multi-second startup.

	// Protontricks is a runtime dependency for heavy mod loadouts —
	// surface its absence at startup, once, before the user discovers
	// it via a mid-game crash. The daemon still starts: a vanilla or
	// lightly-modded install runs fine without it.
	checkProtontricksAvailable()

	// Signal handling.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		slog.Info("received signal, shutting down", "signal", sig)
		d.Shutdown()
	}()

	// Run daemon (blocks until shutdown).
	slog.Info("starting gorganizerd", "socket", sock)
	if err := d.Run(sock); err != nil {
		slog.Error("daemon failed", "err", err)
		os.Exit(1)
	}
}

// forwardNXM connects to the running daemon and sends an NXM URI.
func forwardNXM(uri, socketPath string) error {
	if socketPath == "" {
		socketPath = config.SocketPath()
	}

	target := "unix://" + socketPath
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("connecting to daemon at %s: %w", socketPath, err)
	}
	defer conn.Close()

	client := pb.NewGorganizerClient(conn)
	resp, err := client.StartDownload(ctx, &pb.StartDownloadRequest{NxmUri: uri})
	if err != nil {
		return fmt.Errorf("StartDownload RPC: %w", err)
	}

	fmt.Printf("Download started: %s (queued ahead: %d)\n",
		resp.GetDownloadId(), resp.GetQueuedAhead())
	return nil
}

// checkProtontricksAvailable warns once at startup when protontricks
// is missing. We declare it as a runtime dependency for fully-modded
// loadouts: the daemon auto-installs DX9/VC++/XAudio redists into the
// Proton prefix via protontricks after each script-extender install,
// and modded launches crash without those redists. Distro-agnostic
// hint (pacman/emerge/apt/flatpak) so Artix and Gentoo users aren't
// pointed at apt.
func checkProtontricksAvailable() {
	if _, err := exec.LookPath("protontricks"); err != nil {
		slog.Warn("protontricks not found on PATH — required for heavy mod loadouts (DX9/VC++/XAudio redists). Install via your package manager: pacman -S protontricks (Arch/Artix), emerge protontricks (Gentoo), apt install protontricks (Debian/Ubuntu), or flatpak install com.github.Matoking.protontricks")
		return
	}
	slog.Info("protontricks available — Proton prefix redists will auto-install on script extender install")
}

func parseLogLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

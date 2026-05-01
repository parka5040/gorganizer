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

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	configPath := flag.String("config", "", "Path to config.json (default: $XDG_CONFIG_HOME/gorganizer/config.json)")
	socketPath := flag.String("socket-path", "", "Path to Unix domain socket (default: $XDG_RUNTIME_DIR/gorganizer/gorganizer.sock)")
	logLevel := flag.String("log-level", "", "Log level (debug, info, warn, error)")
	handleNXM := flag.String("handle-nxm", "", "Forward NXM URI to running daemon and exit")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("gorganizerd %s (commit %s, built %s)\n", version, commit, buildDate)
		return
	}

	if *handleNXM != "" {
		if err := forwardNXM(*handleNXM, *socketPath); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	_ = configPath
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading config: %v\n", err)
		os.Exit(1)
	}

	level := parseLogLevel(cfg.LogLevel)
	if *logLevel != "" {
		level = parseLogLevel(*logLevel)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
	})))

	sock := config.SocketPath()
	if *socketPath != "" {
		sock = *socketPath
	}

	releaseLock, err := acquireSingleInstanceLock()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer releaseLock()

	d, err := daemon.New(cfg)
	if err != nil {
		slog.Error("failed to create daemon", "err", err)
		os.Exit(1)
	}

	checkProtontricksAvailable()

	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		slog.Info("received signal, shutting down", "signal", sig)

		const watchdogTimeout = 45 * time.Second
		watchdog := time.AfterFunc(watchdogTimeout, func() {
			slog.Error("shutdown watchdog fired — forcing exit",
				"timeout", watchdogTimeout)
			hardExit(sock, 2)
		})
		defer watchdog.Stop()

		d.Shutdown()

		sig2 := <-sigCh
		slog.Warn("received second signal, exiting immediately", "signal", sig2)
		hardExit(sock, 130)
	}()

	slog.Info("starting gorganizerd", "version", version, "commit", commit, "socket", sock)
	if err := d.Run(sock); err != nil {
		slog.Error("daemon failed", "err", err)
		os.Exit(1)
	}
}

// hardExit is the last-resort cleanup path; replicates releaseLock's socket+lock removal best-effort.
func hardExit(socketPath string, code int) {
	_ = os.Remove(socketPath)
	lockPath := config.LockPath()
	if lockPath != "" {
		_ = os.Remove(lockPath)
	}
	os.Exit(code)
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

// checkProtontricksAvailable warns once at startup when protontricks is missing.
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

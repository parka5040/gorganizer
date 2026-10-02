package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	pb "github.com/parka/gorganizer/api/proto"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/daemon"
	"github.com/parka/gorganizer/internal/instancelock"
	"github.com/parka/gorganizer/internal/ipc"
	"github.com/parka/gorganizer/internal/migrate"
	"github.com/parka/gorganizer/internal/protontricks"
	"github.com/parka/gorganizer/internal/release"
	"github.com/parka/gorganizer/internal/transfer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

// main parses flags, then either forwards an NXM URI or runs the daemon until shutdown.
func main() {
	socketPath := flag.String("socket-path", "", "Path to Unix domain socket (default: $XDG_RUNTIME_DIR/gorganizer/gorganizer.sock)")
	logLevel := flag.String("log-level", "", "Log level (debug, info, warn, error)")
	handleNXM := flag.String("handle-nxm", "", "Forward NXM URI to running daemon and exit")
	showVersion := flag.Bool("version", false, "Print version and exit")
	showReleaseConfig := flag.Bool("release-config", false, "Print release verification configuration and exit")
	printSocket := flag.Bool("print-socket-path", false, "Print the daemon socket path and exit")
	flag.Parse()

	if *printSocket {
		printSocketPath(os.Stdout, *socketPath)
		return
	}
	if *showVersion {
		fmt.Printf("gorganizerd %s (commit %s, built %s)\n", version, commit, buildDate)
		return
	}

	if *showReleaseConfig {
		if err := release.DescribeConfig(os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if *handleNXM != "" {
		if err := forwardNXM(*handleNXM, *socketPath); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	transfer.GorganizerVersion = version

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

	sock := socketPathFor(*socketPath)

	releaseLock, err := instancelock.Acquire()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer releaseLock()

	if err := checkMigrationBeforeStart(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	d, err := daemon.NewWithVersion(cfg, version)
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

		watchdog := time.AfterFunc(daemon.ShutdownWatchdogTimeout, func() {
			slog.Error("shutdown watchdog fired — forcing exit",
				"timeout", daemon.ShutdownWatchdogTimeout)
			hardExit(sock, 2)
		})
		defer watchdog.Stop()

		d.Shutdown()

		sig2 := <-sigCh
		slog.Warn("received second signal, exiting immediately", "signal", sig2)
		hardExit(sock, 130)
	}()

	slog.Info("starting gorganizerd", "version", version, "commit", commit, "socket", sock)
	srv := ipc.NewServer(sock, d)
	if err := srv.Start(); err != nil {
		slog.Error("daemon failed", "err", fmt.Errorf("starting IPC server: %w", err))
		os.Exit(1)
	}
	if err := d.Run(srv.Stop); err != nil {
		slog.Error("daemon failed", "err", err)
		os.Exit(1)
	}
}

// socketPathFor resolves the daemon socket path without creating directories.
func socketPathFor(override string) string {
	if override != "" {
		return override
	}
	return config.SocketPath()
}

// printSocketPath prints the socket path without starting the daemon.
func printSocketPath(out io.Writer, override string) {
	fmt.Fprintln(out, socketPathFor(override))
}

// checkMigrationBeforeStart refuses to start the daemon until an interrupted move is finished.
func checkMigrationBeforeStart() error {
	exists, err := migrate.JournalExists()
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("A move of your mods is unfinished. Run gorganizerctl migrate-data --resume.")
	}
	return nil
}

// hardExit removes the daemon socket before exiting immediately.
func hardExit(socketPath string, code int) {
	_ = os.Remove(socketPath)
	os.Exit(code)
}

// forwardNXM connects to the running daemon and sends an NXM URI.
func forwardNXM(uri, socketPath string) error {
	if socketPath == "" {
		socketPath = config.SocketPath()
	}

	conn, err := grpc.NewClient("unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("connecting to daemon at %s: %w", socketPath, err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := pb.NewGorganizerClient(conn)
	_, err = client.StartDownload(ctx, &pb.StartDownloadRequest{NxmUri: uri})
	if err != nil {
		return errors.New("Gorganizer could not add the download")
	}

	fmt.Println("Download added to Gorganizer.")
	return nil
}

// checkProtontricksAvailable reports whether Protontricks can install Windows runtime components.
func checkProtontricksAvailable() {
	invocation, err := protontricks.Resolve(context.Background(), protontricks.Options{})
	if err != nil {
		slog.Warn("protontricks not installed; Windows runtime components cannot be added automatically")
		return
	}
	slog.Info("protontricks available (" + string(invocation.Kind()) + ")")
}

// parseLogLevel maps a log level name to its slog level, defaulting to info.
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

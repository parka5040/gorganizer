package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	pb "github.com/parka/gorganizer/api/proto"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/fsutil"
)

const sessionStopTimeout = 46 * time.Second

const sessionStillStopping = "Gorganizer's background service is still finishing up. It will exit on its own; please don't shut down your computer for a minute."

type sessionDeps struct {
	out      io.Writer
	errOut   io.Writer
	procRoot string
}

// runSession supervises a GUI session and its optional background service.
func runSession(args []string) int {
	return runSessionWith(args, sessionDeps{out: os.Stdout, errOut: os.Stderr, procRoot: "/proc"})
}

// runSessionWith runs a supervised session using the supplied output and process table.
func runSessionWith(args []string, deps sessionDeps) int {
	fs := flag.NewFlagSet("session", flag.ContinueOnError)
	fs.SetOutput(deps.errOut)
	daemonFlag := fs.String("daemon", "", "background service executable")
	guiFlag := fs.String("gui", "", "GUI executable")
	socketFlag := fs.String("socket-path", "", "daemon socket path")
	if fs.Parse(args) != nil {
		return 2
	}
	socket := *socketFlag
	if socket == "" {
		socket = config.SocketPath()
	}
	release, err := acquireSessionLock()
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		message := "Gorganizer is already open. Switch to its window."
		fmt.Fprintln(deps.errOut, message)
		notifySession(message)
		return 3
	}
	if err != nil {
		fmt.Fprintf(deps.errOut, "Gorganizer could not open its session: %v\n", err)
		return 1
	}
	defer release()

	guiPath, err := sessionBinary(*guiFlag, filepath.Join("build", "src", "gorganizer"), "gorganizer")
	if err != nil {
		fmt.Fprintf(deps.errOut, "Gorganizer could not find its window: %v\n", err)
		return 1
	}

	var child *exec.Cmd
	var childDone chan struct{}
	health, ok := sessionHealth(socket)
	if ok && health.GetStopping() {
		pid := int(health.GetPid())
		if pid <= 0 || health.GetInstanceId() == "" {
			fmt.Fprintln(deps.errOut, "Gorganizer could not verify its background service while it shuts down.")
			return 1
		}
		start, err := processStartTime(deps.procRoot, pid)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(deps.errOut, "Gorganizer could not verify its background service: %v\n", err)
			return 1
		}
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), sessionStopTimeout)
			exited, waitErr := waitProcessExit(ctx, deps.procRoot, pid, start)
			cancel()
			if waitErr != nil || !exited {
				fmt.Fprintln(deps.errOut, sessionStillStopping)
				return 1
			}
		}
		health, ok = sessionHealth(socket)
		if ok && health.GetStopping() {
			fmt.Fprintln(deps.errOut, sessionStillStopping)
			return 1
		}
	}
	if !ok {
		logPath, logFile, err := openSessionLog()
		if err != nil {
			fmt.Fprintf(deps.errOut, "Gorganizer could not open its background service log: %v\n", err)
			return 1
		}
		daemonPath, err := sessionBinary(*daemonFlag, "gorganizerd", "gorganizerd")
		if err == nil {
			child = exec.Command(daemonPath, "--log-level", "info")
			child.Stdout = logFile
			child.Stderr = logFile
			err = child.Start()
		}
		if err != nil {
			_, _ = fmt.Fprintf(logFile, "Could not start the background service: %v\n", err)
		}
		_ = logFile.Close()
		if err != nil {
			sessionStartupFailure(deps.errOut, logPath)
			return 1
		}
		childDone = make(chan struct{})
		go func() { _ = child.Wait(); close(childDone) }()
		readyCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		ready := waitSessionReady(readyCtx, socket, childDone, child.Process.Pid)
		cancel()
		if !ready {
			if !childFinished(childDone) {
				stopOwnedSession(socket, child.Process.Pid, childDone, deps.errOut)
			}
			sessionStartupFailure(deps.errOut, logPath)
			return 1
		}
	}

	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	gui := exec.Command(guiPath, fs.Args()...)
	owned := "0"
	if child != nil {
		owned = "1"
	}
	gui.Env = append(os.Environ(), "GORGANIZER_SUPERVISED=1", "GORGANIZER_SOCKET="+socket, "GORGANIZER_DAEMON_OWNED="+owned)
	gui.Stdin = os.Stdin
	gui.Stdout = deps.out
	gui.Stderr = deps.errOut
	if err := gui.Start(); err != nil {
		fmt.Fprintf(deps.errOut, "Gorganizer could not open its window: %v\n", err)
		if child != nil {
			stopOwnedSession(socket, child.Process.Pid, childDone, deps.errOut)
		}
		return 1
	}
	guiDone := make(chan error, 1)
	go func() { guiDone <- gui.Wait() }()
	var guiErr error
	waiting := true
	for waiting {
		select {
		case sig := <-signals:
			_ = gui.Process.Signal(sig)
		case guiErr = <-guiDone:
			waiting = false
		}
	}
	if child != nil {
		stopOwnedSession(socket, child.Process.Pid, childDone, deps.errOut)
	}
	if guiErr == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(guiErr, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal())
		}
		return exitErr.ExitCode()
	}
	fmt.Fprintf(deps.errOut, "Gorganizer's window closed unexpectedly: %v\n", guiErr)
	return 1
}

// acquireSessionLock holds an exclusive private flock until its release function runs.
func acquireSessionLock() (func(), error) {
	dir := config.RuntimeDir()
	if err := fsutil.EnsurePrivateDir(dir); err != nil {
		return nil, fmt.Errorf("preparing runtime folder: %w", err)
	}
	path := filepath.Join(dir, "session.lock")
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening session lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("checking session lock: %w", err)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || int(st.Uid) != os.Getuid() || st.Nlink != 1 || st.Mode&0o077 != 0 {
		_ = file.Close()
		return nil, errors.New("session lock is not a private file owned by this user")
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("locking session: %w", err)
	}
	return func() { _ = file.Close() }, nil
}

// sessionBinary locates an executable beside the supervisor or on PATH.
func sessionBinary(explicit, relative, inPath string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locating Gorganizer: %w", err)
	}
	candidate := filepath.Join(filepath.Dir(self), relative)
	if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
		return candidate, nil
	}
	return exec.LookPath(inPath)
}

// sessionHealth returns the daemon's Health response when its socket answers.
func sessionHealth(socket string) (*pb.Readiness, bool) {
	conn, err := dialDaemon(socket)
	if err != nil {
		return nil, false
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resp, err := pb.NewGorganizerClient(conn).Health(ctx, &pb.HealthRequest{})
	return resp, err == nil
}

// waitSessionReady polls Health until the new daemon is ready or exits.
func waitSessionReady(ctx context.Context, socket string, done <-chan struct{}, pid int) bool {
	for ctx.Err() == nil {
		select {
		case <-done:
			return false
		default:
		}
		resp, ok := sessionHealth(socket)
		if ok && int(resp.GetPid()) == pid && resp.GetRecoveryDone() && resp.GetGamesWarmed() && !resp.GetStopping() {
			return true
		}
		select {
		case <-done:
			return false
		case <-ctx.Done():
		case <-time.After(lifecyclePollInterval):
		}
	}
	return false
}

// childFinished reports whether the supervisor has already reaped its daemon.
func childFinished(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// stopOwnedSession requests shutdown only from the supervisor's child and waits for it to exit.
func stopOwnedSession(socket string, pid int, done <-chan struct{}, out io.Writer) {
	if childFinished(done) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionStopTimeout)
	defer cancel()
	conn, err := dialDaemon(socket)
	if err == nil {
		probeCtx, stopProbe := context.WithTimeout(ctx, 2*time.Second)
		client := pb.NewGorganizerClient(conn)
		resp, probeErr := client.Health(probeCtx, &pb.HealthRequest{})
		stopProbe()
		if probeErr == nil && int(resp.GetPid()) == pid {
			shutdownCtx, stopShutdown := context.WithTimeout(ctx, 2*time.Second)
			_, _ = client.Shutdown(shutdownCtx, &pb.ShutdownRequest{})
			stopShutdown()
		}
		_ = conn.Close()
	}
	select {
	case <-done:
	case <-ctx.Done():
		fmt.Fprintln(out, sessionStillStopping)
	}
}

// openSessionLog rotates three prior daemon logs and opens the current log for appending.
func openSessionLog() (string, *os.File, error) {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", nil, fmt.Errorf("locating home folder: %w", err)
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(stateHome, "gorganizer")
	if err := os.MkdirAll(stateHome, 0o700); err != nil {
		return "", nil, fmt.Errorf("creating state folder: %w", err)
	}
	if err := fsutil.EnsurePrivateDir(dir); err != nil {
		return "", nil, fmt.Errorf("preparing log folder: %w", err)
	}
	path := filepath.Join(dir, "daemon.log")
	for i := 3; i >= 1; i-- {
		from := path
		if i > 1 {
			from = fmt.Sprintf("%s.%d", path, i-1)
		}
		if err := os.Rename(from, fmt.Sprintf("%s.%d", path, i)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", nil, fmt.Errorf("rotating background service log: %w", err)
		}
	}
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_APPEND|syscall.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return "", nil, fmt.Errorf("opening background service log: %w", err)
	}
	return path, os.NewFile(uintptr(fd), path), nil
}

// sessionStartupFailure reports where to find the daemon log and its last twenty lines.
func sessionStartupFailure(out io.Writer, path string) {
	message := fmt.Sprintf("Gorganizer's background service could not start. Details are in %s.", path)
	fmt.Fprintln(out, message)
	file, err := os.Open(path)
	if err == nil {
		info, statErr := file.Stat()
		if statErr == nil && info.Size() > 128*1024 {
			_, _ = file.Seek(-128*1024, io.SeekEnd)
		}
		data, readErr := io.ReadAll(file)
		_ = file.Close()
		if readErr == nil {
			lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			if len(lines) > 20 {
				lines = lines[len(lines)-20:]
			}
			for _, line := range lines {
				if line != "" {
					fmt.Fprintln(out, line)
				}
			}
		}
	}
	notifySession(message)
}

// notifySession sends an optional desktop notification without blocking the supervisor.
func notifySession(message string) {
	if _, err := exec.LookPath("notify-send"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "notify-send", "--app-name=Gorganizer", "Gorganizer", message)
		_ = cmd.Run()
	}
}

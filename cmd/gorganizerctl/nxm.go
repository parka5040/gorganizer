package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	pb "github.com/parka/gorganizer/api/proto"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	nxmInvalid       = "This link is not a Nexus Mods download link."
	nxmStartupFailed = "Gorganizer could not start. Open it from the menu and try again."
	nxmUnknown       = "Gorganizer may have added the download, but did not confirm it in time. Check the Downloads tab before trying again."
)

type nxmDeps struct {
	socket      string
	sessionPath string
	daemonPath  string
	guiPath     string
	out         io.Writer
	notify      func(string) bool
	readyWait   time.Duration
	submitWait  time.Duration
}

// runNXM validates a link and submits it to a running or newly supervised daemon.
func runNXM(args []string) int {
	return runNXMWith(args, nxmDeps{socket: config.SocketPath(), out: os.Stdout, notify: notifyNXM})
}

// runNXMWith submits one validated link using the supplied executables and output streams.
func runNXMWith(args []string, deps nxmDeps) int {
	if len(args) != 1 || !validNXM(args[0]) {
		return nxmReport(deps, nxmInvalid, 2)
	}
	uri := args[0]
	if deps.socket == "" {
		deps.socket = config.SocketPath()
	}
	readyWait := deps.readyWait
	if readyWait <= 0 {
		readyWait = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), readyWait)
	defer cancel()
	ready, ok := sessionHealth(deps.socket)
	if ok && ready.GetStopping() {
		ready, ok = waitNXMStop(ctx, deps.socket)
	}
	if ctx.Err() != nil {
		return nxmReport(deps, nxmStartupFailed, 1)
	}
	if !ok {
		unlock, err := acquireSessionFileLock("nxm-start.lock")
		if err == nil {
			ready, ok = sessionHealth(deps.socket)
			if ok && ready.GetStopping() {
				ready, ok = waitNXMStop(ctx, deps.socket)
			}
			if ctx.Err() != nil {
				unlock()
				return nxmReport(deps, nxmStartupFailed, 1)
			}
			if !ok {
				var sessionDone <-chan error
				free, lockErr := acquireSessionLock()
				if lockErr == nil {
					free()
					var startErr error
					sessionDone, startErr = startNXMSession(deps)
					if startErr != nil {
						unlock()
						return nxmReport(deps, nxmStartupFailed, 1)
					}
				} else if !errors.Is(lockErr, syscall.EWOULDBLOCK) && !errors.Is(lockErr, syscall.EAGAIN) {
					unlock()
					return nxmReport(deps, nxmStartupFailed, 1)
				}
				ready, ok = waitNXMReady(ctx, deps.socket, sessionDone)
			}
			unlock()
		} else if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			ready, ok = waitNXMReady(ctx, deps.socket, nil)
		} else {
			return nxmReport(deps, nxmStartupFailed, 1)
		}
	} else if ready.GetStopping() || !ready.GetRecoveryDone() || !ready.GetGamesWarmed() {
		ready, ok = waitNXMReady(ctx, deps.socket, nil)
	}
	if !ok || ready.GetStopping() || !ready.GetRecoveryDone() || !ready.GetGamesWarmed() {
		return nxmReport(deps, nxmStartupFailed, 1)
	}

	conn, err := dialDaemon(deps.socket)
	if err != nil {
		return nxmReport(deps, nxmStartupFailed, 1)
	}
	defer conn.Close()
	submitWait := deps.submitWait
	if submitWait <= 0 {
		submitWait = 10 * time.Second
	}
	callCtx, stop := context.WithTimeout(context.Background(), submitWait)
	defer stop()
	_, err = pb.NewGorganizerClient(conn).StartDownload(callCtx, &pb.StartDownloadRequest{NxmUri: uri})
	if err == nil {
		return nxmReport(deps, "Download added to Gorganizer.", 0)
	}
	if errors.Is(err, context.DeadlineExceeded) || status.Code(err) == codes.DeadlineExceeded || status.Code(err) == codes.Unavailable {
		return nxmReport(deps, nxmUnknown, 3)
	}
	message := nxmErrorMessage(err)
	return nxmReport(deps, message, 1)
}

// validNXM checks the Nexus download link's path and required request parameters.
func validNXM(raw string) bool {
	link, err := download.ParseNXM(raw)
	if err != nil || link.GameSlug == "" || link.ModID <= 0 || link.FileID <= 0 {
		return false
	}
	if _, err := link.GameID(); err != nil {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.Port() != "" || u.Host != link.GameSlug || u.Path != fmt.Sprintf("/mods/%d/files/%d", link.ModID, link.FileID) {
		return false
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["key"]) != 1 || link.Key == "" || len(query["expires"]) != 1 || link.Expires <= 0 || len(query["user_id"]) != 1 {
		return false
	}
	userID, err := strconv.ParseUint(query.Get("user_id"), 10, 64)
	return err == nil && userID > 0
}

// startNXMSession starts a detached supervisor without forwarding the download link.
func startNXMSession(deps nxmDeps) (<-chan error, error) {
	sessionPath := deps.sessionPath
	if sessionPath == "" {
		var err error
		sessionPath, err = os.Executable()
		if err != nil {
			return nil, err
		}
	}
	daemonPath, err := sessionBinary(deps.daemonPath, "gorganizerd", "gorganizerd")
	if err != nil {
		return nil, err
	}
	guiPath, err := sessionBinary(deps.guiPath, filepath.Join("build", "src", "gorganizer"), "gorganizer")
	if err != nil {
		return nil, err
	}
	args := []string{"session", "--daemon", daemonPath, "--gui", guiPath}
	if deps.socket != config.SocketPath() {
		args = append(args, "--socket-path", deps.socket)
	}
	cmd := exec.Command(sessionPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer null.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, null
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	return done, nil
}

// waitNXMStop waits until a shutting-down daemon exits or can accept downloads again.
func waitNXMStop(ctx context.Context, socket string) (*pb.Readiness, bool) {
	for ctx.Err() == nil {
		resp, ok := sessionHealth(socket)
		if !ok || !resp.GetStopping() {
			return resp, ok
		}
		select {
		case <-ctx.Done():
		case <-time.After(lifecyclePollInterval):
		}
	}
	return nil, false
}

// waitNXMReady polls Health until startup finishes or its deadline passes.
func waitNXMReady(ctx context.Context, socket string, sessionDone <-chan error) (*pb.Readiness, bool) {
	for ctx.Err() == nil {
		resp, ok := sessionHealth(socket)
		if ok && resp.GetRecoveryDone() && resp.GetGamesWarmed() && !resp.GetStopping() {
			return resp, true
		}
		select {
		case <-sessionDone:
			sessionDone = nil
			free, err := acquireSessionLock()
			if err == nil {
				free()
				return nil, false
			}
			if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
				return nil, false
			}
		case <-ctx.Done():
		case <-time.After(lifecyclePollInterval):
		}
	}
	return nil, false
}

// nxmErrorMessage translates download errors without displaying their arguments.
func nxmErrorMessage(err error) string {
	message := status.Convert(err).Message()
	switch {
	case strings.HasPrefix(message, "nxm_expired:"):
		return "This download link has expired. Download the file again from Nexus Mods."
	case message == "download manager not initialized (set nexus_api_key in config)":
		return "Add your Nexus Mods API key in Gorganizer's settings first."
	case strings.HasPrefix(message, "daemon_shutting_down:"):
		return "Gorganizer is shutting down. Open it again and try the download."
	default:
		return "Gorganizer could not add the download. Check the Downloads tab before trying again."
	}
}

// nxmReport delivers a plain-language result without exposing the download link.
func nxmReport(deps nxmDeps, message string, code int) int {
	if deps.notify != nil && deps.notify(message) {
		return code
	}
	fmt.Fprintln(deps.out, message)
	return code
}

// notifyNXM sends a bounded desktop notification when the notification tool is available.
func notifyNXM(message string) bool {
	path, err := exec.LookPath("notify-send")
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, path, "--app-name=Gorganizer", "Gorganizer", message).Run() == nil
}

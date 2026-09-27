package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	pb "github.com/parka/gorganizer/api/proto"
	"google.golang.org/grpc"
)

type sessionFakeServer struct {
	pb.UnimplementedGorganizerServer
	stopping bool
	stop     func()
}

// Health reports the fake daemon's PID and readiness flags.
func (s *sessionFakeServer) Health(context.Context, *pb.HealthRequest) (*pb.Readiness, error) {
	return &pb.Readiness{Pid: int32(os.Getpid()), InstanceId: "session-test", RecoveryDone: true, GamesWarmed: true, Stopping: s.stopping}, nil
}

// Shutdown requests that the fake daemon exit.
func (s *sessionFakeServer) Shutdown(context.Context, *pb.ShutdownRequest) (*pb.ShutdownResponse, error) {
	s.stop()
	return &pb.ShutdownResponse{}, nil
}

// TestSessionFakeDaemon serves Health and Shutdown as a child process for the session tests.
func TestSessionFakeDaemon(t *testing.T) {
	if os.Getenv("FAKE_SESSION_DAEMON") != "1" {
		return
	}
	socket := os.Getenv("FAKE_SESSION_SOCKET")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(socket)
	var once sync.Once
	stopped := make(chan struct{})
	stop := func() { once.Do(func() { close(stopped) }) }
	server := grpc.NewServer()
	stopping := os.Getenv("FAKE_SESSION_STOPPING") == "1"
	pb.RegisterGorganizerServer(server, &sessionFakeServer{stopping: stopping, stop: stop})
	go func() { _ = server.Serve(listener) }()
	if err := appendSessionEvent("start " + strconv.Itoa(os.Getpid()) + " " + os.Getenv("GORGANIZER_ROOT")); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(os.Stdout, "fake daemon started")
	if stopping {
		time.AfterFunc(600*time.Millisecond, stop)
	}
	<-stopped
	if err := appendSessionEvent("stop " + strconv.Itoa(os.Getpid())); err != nil {
		t.Fatal(err)
	}
	server.Stop()
	_ = listener.Close()
}

// TestSessionFakeSupervisor runs a real supervisor in a child process for signal forwarding.
func TestSessionFakeSupervisor(t *testing.T) {
	if os.Getenv("FAKE_SESSION_SUPERVISOR") != "1" {
		return
	}
	code := runSession([]string{"--daemon", os.Getenv("FAKE_SESSION_DAEMON_SCRIPT"), "--gui", os.Getenv("FAKE_SESSION_GUI_SCRIPT"), "--socket-path", os.Getenv("FAKE_SESSION_SOCKET")})
	os.Exit(code)
}

// appendSessionEvent writes a single fake daemon event to its isolated log.
func appendSessionEvent(event string) error {
	file, err := os.OpenFile(os.Getenv("FAKE_SESSION_EVENTS"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = fmt.Fprintln(file, event)
	return err
}

type sessionFixture struct {
	dir          string
	socket       string
	daemonScript string
	guiScript    string
	events       string
	guiEvents    string
	out          *bytes.Buffer
	errOut       *bytes.Buffer
}

// newSessionFixture isolates the session socket, children, notifications, state and home.
func newSessionFixture(t *testing.T) *sessionFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	short, err := os.MkdirTemp("", "gz")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	t.Setenv("XDG_RUNTIME_DIR", short)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	t.Setenv("GORGANIZER_ROOT", root)
	shims := t.TempDir()
	writeSessionScript(t, filepath.Join(shims, "notify-send"), "exit 0")
	t.Setenv("PATH", shims+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_SESSION_DAEMON", "1")
	t.Setenv("FAKE_SESSION_EVENTS", filepath.Join(root, "daemon-events"))
	t.Setenv("FAKE_SESSION_GUI_EVENTS", filepath.Join(root, "gui-events"))
	socket := filepath.Join(short, "s")
	t.Setenv("FAKE_SESSION_SOCKET", socket)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_SESSION_TEST_BINARY", self)
	f := &sessionFixture{dir: root, socket: socket, events: filepath.Join(root, "daemon-events"), guiEvents: filepath.Join(root, "gui-events"), out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
	f.daemonScript = filepath.Join(root, "daemon")
	f.guiScript = filepath.Join(root, "gui")
	writeSessionScript(t, f.daemonScript, `exec "$FAKE_SESSION_TEST_BINARY" -test.run=^TestSessionFakeDaemon$`)
	writeSessionScript(t, f.guiScript, `printf 'gui %s %s %s %s\n' "$GORGANIZER_SUPERVISED" "$GORGANIZER_SOCKET" "$GORGANIZER_ROOT" "$GORGANIZER_DAEMON_OWNED" >> "$FAKE_SESSION_GUI_EVENTS"
printf 'arg %s\n' "$@" >> "$FAKE_SESSION_GUI_EVENTS"
exit 7`)
	return f
}

// writeSessionScript creates a fake executable under the test fixture.
func writeSessionScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// runFixtureSession starts the supervisor with the fixture's fake executables.
func (f *sessionFixture) runFixtureSession(guiArgs ...string) int {
	args := []string{"--daemon", f.daemonScript, "--gui", f.guiScript, "--socket-path", f.socket, "--"}
	args = append(args, guiArgs...)
	return runSessionWith(args, sessionDeps{out: f.out, errOut: f.errOut, procRoot: "/proc"})
}

// waitSessionFile waits for a fake child to write its first event.
func waitSessionFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			return string(data)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("waiting for fake child at %s", path)
	return ""
}

// startSessionDaemon launches an independently owned fake daemon.
func startSessionDaemon(t *testing.T, f *sessionFixture, stopping bool) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(f.daemonScript)
	cmd.Env = append(os.Environ(), "FAKE_SESSION_DAEMON=1")
	if stopping {
		cmd.Env = append(cmd.Env, "FAKE_SESSION_STOPPING=1")
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if health, ok := sessionHealth(f.socket); ok && int(health.GetPid()) == cmd.Process.Pid {
			return cmd
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fake daemon did not answer: %s", f.errOut.String())
	return nil
}

// TestSessionLockRejectsSecondHolder verifies that the lock is private and refuses a concurrent session.
func TestSessionLockRejectsSecondHolder(t *testing.T) {
	newSessionFixture(t)
	release, err := acquireSessionLock()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	path := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "gorganizer", "session.lock")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("session lock permissions = %v, error = %v", info, err)
	}
	if second, err := acquireSessionLock(); !errors.Is(err, syscall.EWOULDBLOCK) {
		if second != nil {
			second()
		}
		t.Fatalf("second session lock = %v", err)
	}
}

// TestSessionLogRotation verifies the daemon log retains the three previous files.
func TestSessionLogRotation(t *testing.T) {
	newSessionFixture(t)
	state := filepath.Join(os.Getenv("XDG_STATE_HOME"), "gorganizer")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"daemon.log": "current", "daemon.log.1": "first", "daemon.log.2": "second", "daemon.log.3": "discard"} {
		if err := os.WriteFile(filepath.Join(state, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, log, err := openSessionLog()
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"daemon.log.1": "current", "daemon.log.2": "first", "daemon.log.3": "second"} {
		data, err := os.ReadFile(filepath.Join(state, name))
		if err != nil || string(data) != want {
			t.Fatalf("rotated %s = %q, error = %v", name, data, err)
		}
	}
}

// TestSessionSpawnsAndStopsOwnedDaemon verifies readiness, child environment, log rotation and graceful shutdown.
func TestSessionSpawnsAndStopsOwnedDaemon(t *testing.T) {
	f := newSessionFixture(t)
	state := filepath.Join(os.Getenv("XDG_STATE_HOME"), "gorganizer")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"daemon.log": "previous log\n", "daemon.log.1": "older log\n", "daemon.log.2": "oldest log\n"} {
		if err := os.WriteFile(filepath.Join(state, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if code := f.runFixtureSession("nxm://example/mod", "with spaces"); code != 7 {
		t.Fatalf("session = %d, errors = %s", code, f.errOut.String())
	}
	events := waitSessionFile(t, f.events)
	if !strings.Contains(events, "start ") || !strings.Contains(events, "stop ") || !strings.Contains(events, f.dir) {
		t.Fatalf("daemon was not started and stopped with its root: %s", events)
	}
	gui := waitSessionFile(t, f.guiEvents)
	if !strings.Contains(gui, "gui 1 "+f.socket+" "+f.dir+" 1\n") || !strings.Contains(gui, "arg nxm://example/mod\narg with spaces\n") {
		t.Fatalf("GUI did not receive supervised environment and arguments: %s", gui)
	}
	for name, want := range map[string]string{"daemon.log.1": "previous log\n", "daemon.log.2": "older log\n", "daemon.log.3": "oldest log\n"} {
		previous, err := os.ReadFile(filepath.Join(state, name))
		if err != nil || string(previous) != want {
			t.Fatalf("log rotation at %s: %q, %v", name, previous, err)
		}
	}
	log, err := os.ReadFile(filepath.Join(state, "daemon.log"))
	if err != nil || !strings.Contains(string(log), "fake daemon started") {
		t.Fatalf("daemon output: %q, %v", log, err)
	}
}

// TestSessionReusesRunningDaemonAndLeavesItRunning verifies that an answering daemon is not owned.
func TestSessionReusesRunningDaemonAndLeavesItRunning(t *testing.T) {
	f := newSessionFixture(t)
	child := startSessionDaemon(t, f, false)
	if code := f.runFixtureSession(); code != 7 {
		t.Fatalf("session = %d, errors = %s", code, f.errOut.String())
	}
	events := waitSessionFile(t, f.events)
	if strings.Contains(events, "stop ") || strings.Count(events, "start ") != 1 {
		t.Fatalf("reused daemon was stopped or respawned: %s", events)
	}
	if _, ok := sessionHealth(f.socket); !ok {
		t.Fatal("reused daemon stopped answering")
	}
	if child.ProcessState != nil {
		t.Fatal("reused daemon exited")
	}
	if gui := waitSessionFile(t, f.guiEvents); !strings.Contains(gui, "gui 1 "+f.socket+" "+f.dir+" 0\n") {
		t.Fatalf("GUI was told a reused daemon is owned: %s", gui)
	}
}

// TestSecondSessionRefused verifies the session lock rejects another GUI without starting children.
func TestSecondSessionRefused(t *testing.T) {
	f := newSessionFixture(t)
	release := filepath.Join(f.dir, "release")
	writeSessionScript(t, f.guiScript, `printf 'waiting\n' >> "$FAKE_SESSION_GUI_EVENTS"
while [ ! -e "$FAKE_SESSION_GUI_RELEASE" ]; do sleep 0.05; done`)
	t.Setenv("FAKE_SESSION_GUI_RELEASE", release)
	firstDone := make(chan int, 1)
	go func() { firstDone <- f.runFixtureSession() }()
	waitSessionFile(t, f.guiEvents)
	var errOut bytes.Buffer
	code := runSessionWith([]string{"--daemon", f.daemonScript, "--gui", f.guiScript, "--socket-path", f.socket}, sessionDeps{out: &bytes.Buffer{}, errOut: &errOut, procRoot: "/proc"})
	if code != 3 || !strings.Contains(errOut.String(), "Gorganizer is already open. Switch to its window.") {
		t.Fatalf("second session = %d: %s", code, errOut.String())
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-firstDone:
		if code != 0 {
			t.Fatalf("first session = %d: %s", code, f.errOut.String())
		}
	case <-time.After(8 * time.Second):
		t.Fatal("first session did not finish")
	}
	if events := waitSessionFile(t, f.events); strings.Count(events, "start ") != 1 {
		t.Fatalf("second session started a daemon: %s", events)
	}
}

// TestDaemonStartupFailureReportsLog verifies early exit prints the last twenty daemon log lines.
func TestDaemonStartupFailureReportsLog(t *testing.T) {
	f := newSessionFixture(t)
	writeSessionScript(t, f.daemonScript, `i=1
while [ "$i" -le 25 ]; do printf 'log line %s\n' "$i"; i=$((i+1)); done
exit 12`)
	if code := f.runFixtureSession(); code != 1 {
		t.Fatalf("session = %d: %s", code, f.errOut.String())
	}
	message := f.errOut.String()
	if !strings.Contains(message, "Gorganizer's background service could not start. Details are in ") || !strings.Contains(message, "log line 6") || !strings.Contains(message, "log line 25") || strings.Contains(message, "log line 1\n") {
		t.Fatalf("startup report: %s", message)
	}
	if _, err := os.Stat(f.guiEvents); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("GUI started after daemon failed: %v", err)
	}
}

// TestGuiCrashStillStopsOwnedDaemon verifies a signal-terminated GUI yields 128 plus its signal.
func TestGuiCrashStillStopsOwnedDaemon(t *testing.T) {
	f := newSessionFixture(t)
	writeSessionScript(t, f.guiScript, `kill -TERM $$`)
	if code := f.runFixtureSession(); code != 128+int(syscall.SIGTERM) {
		t.Fatalf("session = %d, errors = %s", code, f.errOut.String())
	}
	if events := waitSessionFile(t, f.events); !strings.Contains(events, "stop ") {
		t.Fatalf("daemon remained after GUI crash: %s", events)
	}
}

// TestSupervisorForwardsSigtermToGui verifies SIGTERM reaches the GUI and not the daemon.
func TestSupervisorForwardsSigtermToGui(t *testing.T) {
	f := newSessionFixture(t)
	writeSessionScript(t, f.guiScript, `trap 'printf "term\n" >> "$FAKE_SESSION_GUI_EVENTS"; exit 42' TERM
printf 'ready\n' >> "$FAKE_SESSION_GUI_EVENTS"
while :; do sleep 1; done`)
	cmd := exec.Command(os.Getenv("FAKE_SESSION_TEST_BINARY"), "-test.run=^TestSessionFakeSupervisor$")
	cmd.Env = append(os.Environ(), "FAKE_SESSION_SUPERVISOR=1", "FAKE_SESSION_DAEMON_SCRIPT="+f.daemonScript, "FAKE_SESSION_GUI_SCRIPT="+f.guiScript)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	waitSessionFile(t, f.guiEvents)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil || cmd.ProcessState.ExitCode() != 42 {
		t.Fatalf("supervisor = %v, status = %d, output = %s", err, cmd.ProcessState.ExitCode(), output.String())
	}
	if gui := waitSessionFile(t, f.guiEvents); !strings.Contains(gui, "term") {
		t.Fatalf("GUI missed SIGTERM: %s", gui)
	}
	if events := waitSessionFile(t, f.events); !strings.Contains(events, "stop ") {
		t.Fatalf("daemon did not shut down through RPC: %s", events)
	}
}

// TestStoppingDaemonIsAwaitedBeforeSpawn verifies a shutting-down daemon exits before replacement.
func TestStoppingDaemonIsAwaitedBeforeSpawn(t *testing.T) {
	f := newSessionFixture(t)
	old := startSessionDaemon(t, f, true)
	start := time.Now()
	if code := f.runFixtureSession(); code != 7 {
		t.Fatalf("session = %d, errors = %s", code, f.errOut.String())
	}
	if elapsed := time.Since(start); elapsed < 500*time.Millisecond {
		t.Fatalf("new daemon started before old daemon exited: %v", elapsed)
	}
	events := waitSessionFile(t, f.events)
	oldStop := strings.Index(events, fmt.Sprintf("stop %d", old.Process.Pid))
	lastStart := strings.LastIndex(events, "start ")
	if oldStop < 0 || lastStart < oldStop || strings.Count(events, "start ") != 2 {
		t.Fatalf("incorrect daemon handoff: %s", events)
	}
}

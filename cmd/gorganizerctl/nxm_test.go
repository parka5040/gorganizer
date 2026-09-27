package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	pb "github.com/parka/gorganizer/api/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const nxmTestURI = "nxm://skyrimspecialedition/mods/12/files/34?key=private-NXM-key&expires=9999999999&user_id=56"

type nxmFakeServer struct {
	pb.UnimplementedGorganizerServer
	stop func()
}

// Health reports readiness only after the fake daemon's startup gate opens.
func (s *nxmFakeServer) Health(context.Context, *pb.HealthRequest) (*pb.Readiness, error) {
	_, err := os.Stat(os.Getenv("FAKE_NXM_READY"))
	return &pb.Readiness{Pid: int32(os.Getpid()), InstanceId: "nxm-test", RecoveryDone: err == nil, GamesWarmed: err == nil}, nil
}

// StartDownload records a submitted link or waits past the client's deadline.
func (s *nxmFakeServer) StartDownload(ctx context.Context, req *pb.StartDownloadRequest) (*pb.StartDownloadResponse, error) {
	file, err := os.OpenFile(os.Getenv("FAKE_NXM_CALLS"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	_, err = fmt.Fprintln(file, req.GetNxmUri())
	_ = file.Close()
	if err != nil {
		return nil, err
	}
	if os.Getenv("FAKE_NXM_TIMEOUT") == "1" {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &pb.StartDownloadResponse{DownloadId: "dl-fake"}, nil
}

// Shutdown stops the fake background service after the GUI closes.
func (s *nxmFakeServer) Shutdown(context.Context, *pb.ShutdownRequest) (*pb.ShutdownResponse, error) {
	s.stop()
	return &pb.ShutdownResponse{}, nil
}

// TestNxmFakeDaemon serves a disposable socket for the link handler tests.
func TestNxmFakeDaemon(t *testing.T) {
	if os.Getenv("FAKE_NXM_DAEMON") != "1" {
		return
	}
	listener, err := net.Listen("unix", os.Getenv("FAKE_NXM_SOCKET"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var once sync.Once
	stopped := make(chan struct{})
	server := grpc.NewServer()
	pb.RegisterGorganizerServer(server, &nxmFakeServer{stop: func() { once.Do(func() { close(stopped) }) }})
	go func() { _ = server.Serve(listener) }()
	if err := appendSessionEvent("nxm-start " + fmt.Sprint(os.Getpid())); err != nil {
		t.Fatal(err)
	}
	<-stopped
	server.Stop()
	_ = appendSessionEvent("nxm-stop")
}

// TestNxmFakeSupervisor runs the real session supervisor in a detached test child.
func TestNxmFakeSupervisor(t *testing.T) {
	if os.Getenv("FAKE_NXM_SUPERVISOR") != "1" {
		return
	}
	group, err := syscall.Getpgid(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("FAKE_NXM_GROUP"), []byte(fmt.Sprintf("%d %d", os.Getpid(), group)), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(os.Getenv("FAKE_NXM_SUPERVISORS"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintln(file, os.Getpid())
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	code := runSessionWith([]string{"--daemon", os.Getenv("FAKE_NXM_DAEMON_SCRIPT"), "--gui", os.Getenv("FAKE_NXM_GUI_SCRIPT"), "--socket-path", os.Getenv("FAKE_NXM_SOCKET")}, sessionDeps{out: io.Discard, errOut: io.Discard, procRoot: "/proc"})
	os.Exit(code)
}

type nxmFixture struct {
	socket, calls, events, ready, release, notifyLog, daemonScript, guiScript, ctlScript string
	group, guiEvents, supervisors                                                        string
	out                                                                                  *bytes.Buffer
}

// newNxmFixture creates isolated binaries, notifications, process state and a short Unix socket.
func newNxmFixture(t *testing.T) *nxmFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	short, err := os.MkdirTemp("", "nx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	t.Setenv("XDG_RUNTIME_DIR", short)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	t.Setenv("GORGANIZER_ROOT", root)
	f := &nxmFixture{socket: filepath.Join(short, "s"), calls: filepath.Join(root, "calls"), events: filepath.Join(root, "events"), ready: filepath.Join(root, "ready"), release: filepath.Join(root, "release"), notifyLog: filepath.Join(root, "notifications"), group: filepath.Join(root, "group"), guiEvents: filepath.Join(root, "gui-events"), supervisors: filepath.Join(root, "supervisors"), out: &bytes.Buffer{}}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_NXM_BINARY", self)
	t.Setenv("FAKE_NXM_SOCKET", f.socket)
	t.Setenv("FAKE_NXM_CALLS", f.calls)
	t.Setenv("FAKE_NXM_READY", f.ready)
	t.Setenv("FAKE_NXM_GUI_RELEASE", f.release)
	t.Setenv("FAKE_NXM_GUI_EVENTS", f.guiEvents)
	t.Setenv("FAKE_NXM_GROUP", f.group)
	t.Setenv("FAKE_NXM_SUPERVISORS", f.supervisors)
	t.Setenv("FAKE_SESSION_EVENTS", f.events)
	t.Setenv("FAKE_NXM_DAEMON_SCRIPT", filepath.Join(root, "daemon"))
	t.Setenv("FAKE_NXM_GUI_SCRIPT", filepath.Join(root, "gui"))
	f.daemonScript = filepath.Join(root, "daemon")
	f.guiScript = filepath.Join(root, "gui")
	f.ctlScript = filepath.Join(root, "ctl")
	writeSessionScript(t, f.daemonScript, `FAKE_NXM_SUPERVISOR= FAKE_NXM_DAEMON=1 exec "$FAKE_NXM_BINARY" -test.run=^TestNxmFakeDaemon$`)
	writeSessionScript(t, f.ctlScript, `FAKE_NXM_SUPERVISOR=1 exec "$FAKE_NXM_BINARY" -test.run=^TestNxmFakeSupervisor$`)
	writeSessionScript(t, f.guiScript, `printf 'gui started\n' > "$FAKE_NXM_GUI_EVENTS"
for arg in "$@"; do printf 'arg %s\n' "$arg" >> "$FAKE_NXM_GUI_EVENTS"; done
while [ ! -e "$FAKE_NXM_GUI_RELEASE" ]; do sleep 0.05; done`)
	shims := t.TempDir()
	writeSessionScript(t, filepath.Join(shims, "notify-send"), `printf '%s\n' "$@" >> "$FAKE_NXM_NOTIFY_LOG"`)
	t.Setenv("FAKE_NXM_NOTIFY_LOG", f.notifyLog)
	t.Setenv("PATH", shims+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() {
		_ = os.WriteFile(f.release, nil, 0o600)
		conn, err := dialDaemon(f.socket)
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_, _ = pb.NewGorganizerClient(conn).Shutdown(ctx, &pb.ShutdownRequest{})
			cancel()
			_ = conn.Close()
		}
		if _, err := os.Stat(f.group); err == nil {
			if data, err := os.ReadFile(f.events); err == nil && strings.Contains(string(data), "nxm-start ") {
				deadline := time.Now().Add(8 * time.Second)
				for time.Now().Before(deadline) {
					data, _ := os.ReadFile(f.events)
					if strings.Contains(string(data), "nxm-stop") {
						break
					}
					time.Sleep(20 * time.Millisecond)
				}
			}
		}
	})
	return f
}

// runNxmFixture invokes the link handler with disposable executables and captured output.
func (f *nxmFixture) runNxmFixture(uri string) int {
	return runNXMWith([]string{uri}, nxmDeps{socket: f.socket, sessionPath: f.ctlScript, daemonPath: f.daemonScript, guiPath: f.guiScript, out: f.out, submitWait: time.Second, notify: func(message string) bool {
		return notifyNXM(message)
	}})
}

// startNxmDaemon launches the fake service independently of a GUI session.
func startNxmDaemon(t *testing.T, f *nxmFixture) {
	t.Helper()
	cmd := exec.Command(f.daemonScript)
	cmd.Env = append(os.Environ(), "FAKE_NXM_DAEMON=1")
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	waitNxmFile(t, f.events)
}

// waitNxmFile waits until the fake service writes a nonempty event or download record.
func waitNxmFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			return string(data)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for a fake child at %s", path)
	return ""
}

// readyNxmDaemon completes the fake daemon's startup gates.
func readyNxmDaemon(t *testing.T, f *nxmFixture) {
	t.Helper()
	if err := os.WriteFile(f.ready, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestNxmSubmitsToRunningDaemonOnce verifies a ready service receives one link without starting a session.
func TestNxmSubmitsToRunningDaemonOnce(t *testing.T) {
	f := newNxmFixture(t)
	readyNxmDaemon(t, f)
	startNxmDaemon(t, f)
	if code := f.runNxmFixture(nxmTestURI); code != 0 {
		t.Fatalf("exit = %d, output = %s", code, f.out.String())
	}
	if got := waitNxmFile(t, f.calls); got != nxmTestURI+"\n" {
		t.Fatal("expected one matching download request")
	}
	if got := waitNxmFile(t, f.notifyLog); !strings.Contains(got, "Download added to Gorganizer.") {
		t.Fatalf("notification = %q", got)
	}
}

// TestNxmStartsSessionWhenClosed verifies startup reaches Health readiness before one submission.
func TestNxmStartsSessionWhenClosed(t *testing.T) {
	f := newNxmFixture(t)
	result := make(chan int, 1)
	go func() { result <- f.runNxmFixture(nxmTestURI) }()
	waitNxmFile(t, f.events)
	if data, err := os.ReadFile(f.calls); err == nil && len(data) > 0 {
		t.Fatalf("submitted before readiness: %q", data)
	}
	readyNxmDaemon(t, f)
	select {
	case code := <-result:
		if code != 0 {
			t.Fatalf("exit = %d, errors = %s", code, f.out.String())
		}
	case <-time.After(8 * time.Second):
		t.Fatal("handler did not return after the daemon became ready")
	}
	if got := waitNxmFile(t, f.calls); got != nxmTestURI+"\n" {
		t.Fatal("expected one matching download request")
	}
	if events := waitNxmFile(t, f.events); strings.Count(events, "nxm-start ") != 1 {
		t.Fatalf("daemon starts = %q", events)
	}
	if gui := waitNxmFile(t, f.guiEvents); gui != "gui started\n" {
		t.Fatal("session passed arguments to the GUI")
	}
	var pid, group int
	if _, err := fmt.Sscanf(waitNxmFile(t, f.group), "%d %d", &pid, &group); err != nil || pid != group {
		t.Fatal("session did not start in its own process group")
	}
}

// TestNxmRejectsMalformedLink verifies invalid links never launch a session or reach a daemon.
func TestNxmRejectsMalformedLink(t *testing.T) {
	for _, tc := range []struct{ name, uri string }{
		{"wrong scheme", "http://example.com"},
		{"unsupported game", "nxm://unknown/mods/1/files/2?key=k&expires=9999999999&user_id=1"},
		{"bad path", "nxm://skyrim/bad/path?key=private-NXM-key"},
		{"missing credentials", "nxm://skyrim/mods/1/files/2"},
		{"invalid expiration", "nxm://skyrim/mods/1/files/2?key=k&expires=no&user_id=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNxmFixture(t)
			if code := f.runNxmFixture(tc.uri); code != 2 {
				t.Fatalf("exit = %d", code)
			}
			if got := waitNxmFile(t, f.notifyLog); !strings.Contains(got, nxmInvalid) {
				t.Fatalf("notification = %q", got)
			}
			if _, err := os.Stat(f.events); !os.IsNotExist(err) {
				t.Fatalf("invalid link started daemon: %v", err)
			}
		})
	}
}

// TestNxmStartupFailure reports a failed supervisor without disclosing the link.
func TestNxmStartupFailure(t *testing.T) {
	f := newNxmFixture(t)
	code := runNXMWith([]string{nxmTestURI}, nxmDeps{socket: f.socket, sessionPath: f.ctlScript, daemonPath: filepath.Join(t.TempDir(), "missing"), guiPath: f.guiScript, out: f.out, notify: notifyNXM, readyWait: 500 * time.Millisecond})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if got := waitNxmFile(t, f.notifyLog); !strings.Contains(got, nxmStartupFailed) || strings.Contains(got, "private-NXM-key") {
		t.Fatal("startup failure notification was unsafe or missing")
	}
}

// TestNxmTimeoutAfterSendIsUnknownNotRetried verifies an unanswered RPC has exit code three and one submission.
func TestNxmTimeoutAfterSendIsUnknownNotRetried(t *testing.T) {
	f := newNxmFixture(t)
	t.Setenv("FAKE_NXM_TIMEOUT", "1")
	readyNxmDaemon(t, f)
	startNxmDaemon(t, f)
	if code := f.runNxmFixture(nxmTestURI); code != 3 {
		t.Fatalf("exit = %d, output = %s", code, f.out.String())
	}
	if got := waitNxmFile(t, f.calls); got != nxmTestURI+"\n" {
		t.Fatal("expected one matching download request")
	}
	if got := waitNxmFile(t, f.notifyLog); !strings.Contains(got, nxmUnknown) {
		t.Fatalf("notification = %q", got)
	}
}

// captureNxmStderr records handler diagnostics without changing a child's output streams.
func captureNxmStderr(t *testing.T, run func() int) (int, string) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	previous := os.Stderr
	os.Stderr = writer
	defer func() { os.Stderr = previous }()
	code := run()
	os.Stderr = previous
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return code, string(data)
}

// TestNxmNeverPrintsTheKey verifies all visible outputs and notifications omit the secret.
func TestNxmNeverPrintsTheKey(t *testing.T) {
	for _, tc := range []struct {
		name, uri string
		expected  int
	}{
		{"success", nxmTestURI, 0},
		{"invalid", "nxm://skyrim/bad/path?key=private-NXM-key", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNxmFixture(t)
			if tc.expected == 0 {
				readyNxmDaemon(t, f)
				startNxmDaemon(t, f)
			}
			code, stderr := captureNxmStderr(t, func() int { return f.runNxmFixture(tc.uri) })
			if code != tc.expected {
				t.Fatalf("exit = %d, want %d", code, tc.expected)
			}
			visible := f.out.String() + stderr + waitNxmFile(t, f.notifyLog)
			if strings.Contains(visible, "private-NXM-key") || strings.Contains(visible, "nxm://") {
				t.Fatalf("link appeared in output")
			}
		})
	}
}

// TestTwoLinksStartOneSession verifies concurrent clicks share one startup and submit separately.
func TestTwoLinksStartOneSession(t *testing.T) {
	f := newNxmFixture(t)
	second := strings.Replace(nxmTestURI, "files/34", "files/35", 1)
	run := func(uri string) int {
		return runNXMWith([]string{uri}, nxmDeps{socket: f.socket, sessionPath: f.ctlScript, daemonPath: f.daemonScript, guiPath: f.guiScript, out: io.Discard, notify: notifyNXM})
	}
	results := make(chan int, 2)
	go func() { results <- run(nxmTestURI) }()
	waitNxmFile(t, f.events)
	go func() { results <- run(second) }()
	readyNxmDaemon(t, f)
	for i := 0; i < 2; i++ {
		select {
		case code := <-results:
			if code != 0 {
				t.Fatalf("click %d exit = %d", i, code)
			}
		case <-time.After(8 * time.Second):
			t.Fatal("concurrent clicks did not finish")
		}
	}
	calls := waitNxmFile(t, f.calls)
	if strings.Count(calls, nxmTestURI) != 1 || strings.Count(calls, second) != 1 {
		t.Fatal("each link must be submitted exactly once")
	}
	if events := waitNxmFile(t, f.events); strings.Count(events, "nxm-start ") != 1 {
		t.Fatalf("daemon starts = %q", events)
	}
	if supervisors := waitNxmFile(t, f.supervisors); len(strings.Fields(supervisors)) != 1 {
		t.Fatal("more than one session started for concurrent links")
	}
}

// TestNxmErrorMessages reports expired links, missing keys and unknown failures without revealing their details.
func TestNxmErrorMessages(t *testing.T) {
	for _, tc := range []struct {
		name, errorMessage, want string
	}{
		{"expired", "nxm_expired:uri=" + nxmTestURI, "This download link has expired. Download the file again from Nexus Mods."},
		{"missing API key", "download manager not initialized (set nexus_api_key in config)", "Add your Nexus Mods API key in Gorganizer's settings first."},
		{"other", "server error: " + nxmTestURI, "Gorganizer could not add the download. Check the Downloads tab before trying again."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := nxmErrorMessage(status.Error(codes.Internal, tc.errorMessage))
			if got != tc.want || strings.Contains(got, "private-NXM-key") {
				t.Fatal("error message was not a safe plain-language explanation")
			}
		})
	}
}

// TestNxmStdoutFallback prints safe feedback when desktop notifications are unavailable.
func TestNxmStdoutFallback(t *testing.T) {
	var out bytes.Buffer
	if code := nxmReport(nxmDeps{out: &out, notify: func(string) bool { return false }}, nxmInvalid, 2); code != 2 || out.String() != nxmInvalid+"\n" {
		t.Fatal("missing notification did not fall back to standard output")
	}
}

// TestNxmLauncherExecsCtl verifies the shell handler forwards the link only to the supervisor command.
func TestNxmLauncherExecsCtl(t *testing.T) {
	f := newNxmFixture(t)
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(t.TempDir(), "ctl")
	log := filepath.Join(t.TempDir(), "args")
	writeSessionScript(t, shim, `printf '%s\n' "$@" > "$FAKE_NXM_ARGS"`)
	t.Setenv("FAKE_NXM_ARGS", log)
	cmd := exec.Command("bash", "-c", `. "$1/gorganizer.sh"; CTL_BIN="$2"; DAEMON_BIN="$3"; cmd_nxm "$4"`, "bash", root, shim, f.daemonScript, nxmTestURI)
	cmd.Env = append(os.Environ(), "GORGANIZER_SH_SOURCE_ONLY=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("shell handler failed: %v (output length %d)", err, len(output))
	}
	if got := waitNxmFile(t, log); got != "nxm\n"+nxmTestURI+"\n" {
		t.Fatal("launcher did not exec gorganizerctl nxm with one link")
	}
}

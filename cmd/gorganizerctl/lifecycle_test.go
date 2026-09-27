package main

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/parka/gorganizer/api/proto"
	"google.golang.org/grpc"
)

type lifecycleServer struct {
	pb.UnimplementedGorganizerServer
	health   func() *pb.Readiness
	shutdown func()
}

// Health returns the fake daemon's current readiness response.
func (s *lifecycleServer) Health(context.Context, *pb.HealthRequest) (*pb.Readiness, error) {
	return s.health(), nil
}

// Shutdown records the fake daemon's shutdown request.
func (s *lifecycleServer) Shutdown(context.Context, *pb.ShutdownRequest) (*pb.ShutdownResponse, error) {
	if s.shutdown != nil {
		s.shutdown()
	}
	return &pb.ShutdownResponse{}, nil
}

// lifecycleFixture creates a short isolated runtime path and captured command output.
func lifecycleFixture(t *testing.T) (string, lifecycleDeps, *bytes.Buffer) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir, err := os.MkdirTemp("", "gz")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("XDG_RUNTIME_DIR", dir)
	out := &bytes.Buffer{}
	return filepath.Join(dir, "s"), lifecycleDeps{out: out, errOut: &bytes.Buffer{}, procRoot: "/proc"}, out
}

// serveLifecycle starts an in-process gRPC daemon on the isolated socket.
func serveLifecycle(t *testing.T, socket string, fake *lifecycleServer) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	pb.RegisterGorganizerServer(server, fake)
	go server.Serve(listener)
	t.Cleanup(func() {
		server.Stop()
		listener.Close()
	})
}

// TestPingNotRunning verifies the probe reports an absent daemon and preserves the socket path.
func TestPingNotRunning(t *testing.T) {
	path, deps, out := lifecycleFixture(t)
	if code := runPingWith([]string{"--socket-path", path}, deps); code != 1 || out.String() != "Gorganizer is not running.\n" {
		t.Fatalf("ping = %d, output = %q", code, out.String())
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("ping changed socket: %v", err)
	}
}

// TestPingRunning verifies identity output and the distinct shutdown status.
func TestPingRunning(t *testing.T) {
	for _, tc := range []struct {
		name     string
		stopping bool
		code     int
		want     string
	}{
		{"running", false, 0, "running version 1.2.3 (instance test-id, pid 42)\n"},
		{"stopping", true, 3, "Gorganizer is shutting down.\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, deps, out := lifecycleFixture(t)
			serveLifecycle(t, path, &lifecycleServer{health: func() *pb.Readiness {
				return &pb.Readiness{Version: "1.2.3", InstanceId: "test-id", Pid: 42, Stopping: tc.stopping}
			}})
			if code := runPingWith([]string{"--socket-path", path}, deps); code != tc.code || out.String() != tc.want {
				t.Fatalf("ping = %d, output = %q, want %d and %q", code, out.String(), tc.code, tc.want)
			}
		})
	}
}

// TestWaitReadyTimesOut verifies a daemon that never warms fails within the requested timeout.
func TestWaitReadyTimesOut(t *testing.T) {
	path, deps, out := lifecycleFixture(t)
	serveLifecycle(t, path, &lifecycleServer{health: func() *pb.Readiness { return &pb.Readiness{RecoveryDone: true} }})
	if code := runWaitReadyWith([]string{"--socket-path", path, "--timeout", "300ms"}, deps); code != 1 || out.String() != "Gorganizer did not become ready in time.\n" {
		t.Fatalf("wait-ready = %d, output = %q", code, out.String())
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("wait-ready removed socket: %v", err)
	}
}

// TestWaitReadySucceeds verifies both warmup flags must be set while shutdown is false.
func TestWaitReadySucceeds(t *testing.T) {
	path, deps, out := lifecycleFixture(t)
	var calls atomic.Int32
	serveLifecycle(t, path, &lifecycleServer{health: func() *pb.Readiness {
		switch calls.Add(1) {
		case 1:
			return &pb.Readiness{RecoveryDone: true, GamesWarmed: true, Stopping: true}
		case 2:
			return &pb.Readiness{RecoveryDone: true}
		default:
			return &pb.Readiness{RecoveryDone: true, GamesWarmed: true}
		}
	}})
	if code := runWaitReadyWith([]string{"--socket-path", path, "--timeout", "2s"}, deps); code != 0 || out.Len() != 0 || calls.Load() < 3 {
		t.Fatalf("wait-ready = %d, output = %q, probes = %d", code, out.String(), calls.Load())
	}
}

// TestStopWaitsForThatPid verifies shutdown waits for the identified child rather than socket removal.
func TestStopWaitsForThatPid(t *testing.T) {
	path, deps, out := lifecycleFixture(t)
	child := exec.Command("sleep", "10")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	shutdown := make(chan struct{})
	serveLifecycle(t, path, &lifecycleServer{
		health: func() *pb.Readiness {
			return &pb.Readiness{InstanceId: "child-instance", Pid: int32(child.Process.Pid)}
		},
		shutdown: func() { close(shutdown) },
	})
	go func() {
		<-shutdown
		time.Sleep(320 * time.Millisecond)
		_ = child.Process.Kill()
	}()
	started := time.Now()
	if code := runStopWith([]string{"--socket-path", path, "--timeout", "2s"}, deps); code != 0 || out.Len() != 0 {
		t.Fatalf("stop = %d, output = %q", code, out.String())
	}
	if elapsed := time.Since(started); elapsed < 300*time.Millisecond {
		t.Fatalf("stop returned after %v, before child exited", elapsed)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("stop removed socket while fake daemon still serves: %v", err)
	}
}

// TestStopTimesOut verifies an unfinished daemon retains its process and reports the timeout.
func TestStopTimesOut(t *testing.T) {
	path, deps, out := lifecycleFixture(t)
	child := exec.Command("sleep", "10")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	var shutdowns atomic.Int32
	serveLifecycle(t, path, &lifecycleServer{
		health: func() *pb.Readiness {
			return &pb.Readiness{InstanceId: "child-instance", Pid: int32(child.Process.Pid)}
		},
		shutdown: func() { shutdowns.Add(1) },
	})
	if code := runStopWith([]string{"--socket-path", path, "--timeout", "250ms"}, deps); code != 1 ||
		out.String() != "Gorganizer is still finishing a mod operation. It will stop on its own; check again in a minute.\n" || shutdowns.Load() != 1 {
		t.Fatalf("stop = %d, output = %q, shutdowns = %d", code, out.String(), shutdowns.Load())
	}
	if alive, err := processAlive("/proc", child.Process.Pid, mustProcessStart(t, child.Process.Pid)); err != nil || !alive {
		t.Fatalf("stop sent a signal to the child: alive = %v, err = %v", alive, err)
	}
}

// mustProcessStart reads the test child's process start time.
func mustProcessStart(t *testing.T, pid int) uint64 {
	t.Helper()
	start, err := processStartTime("/proc", pid)
	if err != nil {
		t.Fatal(err)
	}
	return start
}

// TestStopNeverSignals verifies the stop implementation contains no process signal calls.
func TestStopNeverSignals(t *testing.T) {
	path := "lifecycle.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.ImportSpec:
			if strings.Contains(n.Path.Value, "syscall") || strings.Contains(n.Path.Value, "unix") {
				t.Errorf("stop imports signal-capable package %s", n.Path.Value)
			}
		case *ast.SelectorExpr:
			if n.Sel.Name == "Signal" || n.Sel.Name == "Kill" || n.Sel.Name == "Interrupt" {
				t.Errorf("stop invokes %s at %s", n.Sel.Name, fset.Position(n.Pos()))
			}
		}
		return true
	})
}

// TestStopHandlesMissingAndRecycledProcesses verifies that stop succeeds for an absent daemon and never waits on a recycled PID.
func TestStopHandlesMissingAndRecycledProcesses(t *testing.T) {
	path, deps, out := lifecycleFixture(t)
	if code := runStopWith([]string{"--socket-path", path}, deps); code != 0 || out.String() != "Gorganizer is not running.\n" {
		t.Fatalf("missing stop = %d, output = %q", code, out.String())
	}
	procRoot := t.TempDir()
	pid := 4321
	pidDir := filepath.Join(procRoot, fmt.Sprint(pid))
	if err := os.Mkdir(pidDir, 0o700); err != nil {
		t.Fatal(err)
	}
	stat := filepath.Join(pidDir, "stat")
	writeStat := func(start int) {
		t.Helper()
		if err := os.WriteFile(stat, []byte(fmt.Sprintf("%d (name with ) parentheses) S %s %d\n", pid, strings.Repeat("0 ", 18), start)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeStat(111)
	start, err := processStartTime(procRoot, pid)
	if err != nil || start != 111 {
		t.Fatalf("start = %d, err = %v", start, err)
	}
	writeStat(222)
	if alive, err := processAlive(procRoot, pid, start); err != nil || alive {
		t.Fatalf("recycled pid alive = %v, err = %v", alive, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stop changed socket: %v", err)
	}
}

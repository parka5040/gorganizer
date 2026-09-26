package smapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const pdeathsigHelperEnv = "GORGANIZER_SMAPI_PDEATHSIG_HELPER"

// TestExecRunnerCapturesOutput verifies stdout and stderr are combined and the start callback sees the pid.
func TestExecRunnerCapturesOutput(t *testing.T) {
	var started int
	result, err := ExecRunner{}.Run(context.Background(), Command{
		Path: "/bin/sh",
		Args: []string{"-c", "echo hi; echo err >&2; exit 7"},
		Env:  []string{"PATH=/usr/bin:/bin"},
	}, func(pid int) { started = pid })
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ExitCode != 7 {
		t.Fatalf("exit code = %d", result.ExitCode)
	}
	if got := string(result.Output); !strings.Contains(got, "hi\n") || !strings.Contains(got, "err\n") {
		t.Fatalf("output = %q", got)
	}
	if started <= 0 {
		t.Fatalf("onStart pid = %d", started)
	}
}

// TestExecRunnerEnvIsExact verifies the child sees exactly the configured environment.
func TestExecRunnerEnvIsExact(t *testing.T) {
	env := []string{"A=1", "B=two words", "HOME=/nonexistent"}
	result, err := ExecRunner{}.Run(context.Background(), Command{Path: "/usr/bin/env", Env: env, Dir: t.TempDir()}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(result.Output)), "\n")
	if !reflect.DeepEqual(got, env) {
		t.Fatalf("env = %q, want %q", got, env)
	}
	result, err = ExecRunner{}.Run(context.Background(), Command{Path: "/usr/bin/env"}, nil)
	if err != nil || len(strings.TrimSpace(string(result.Output))) != 0 {
		t.Fatalf("empty env run = %q, %v", result.Output, err)
	}
}

// TestExecRunnerTimeoutKillsGroup verifies a timeout terminates the whole process group.
func TestExecRunnerTimeoutKillsGroup(t *testing.T) {
	start := time.Now()
	result, err := ExecRunner{}.Run(context.Background(), Command{
		Path:    "/bin/sh",
		Args:    []string{"-c", "sleep 30 & echo $!; wait"},
		Env:     []string{"PATH=/usr/bin:/bin"},
		Timeout: 300 * time.Millisecond,
	}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
	assertChildGone(t, result.Output)
}

// TestExecRunnerKillsStragglers verifies group members left behind by an exited leader are killed and cannot hold the output pipe.
func TestExecRunnerKillsStragglers(t *testing.T) {
	start := time.Now()
	result, err := ExecRunner{}.Run(context.Background(), Command{
		Path: "/bin/sh",
		Args: []string{"-c", "sleep 30 & echo $!"},
		Env:  []string{"PATH=/usr/bin:/bin"},
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("run waited %s for a straggler", elapsed)
	}
	assertChildGone(t, result.Output)
}

// TestExecRunnerCanceledContext verifies cancellation stops the child.
func TestExecRunnerCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	_, err := ExecRunner{}.Run(ctx, Command{Path: "/bin/sleep", Args: []string{"30"}}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v", err)
	}
	if _, err := (ExecRunner{}).Run(ctx, Command{Path: "/bin/true"}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run with canceled context = %v", err)
	}
}

// assertChildGone parses the first output line as a pid and requires that process to be dead.
func assertChildGone(t *testing.T, output []byte) {
	t.Helper()
	line := strings.TrimSpace(strings.SplitN(string(output), "\n", 2)[0])
	pid, err := strconv.Atoi(line)
	if err != nil {
		t.Fatalf("parsing child pid from %q: %v", output, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		state, _, err := procStat(pid)
		if err != nil || state == 'Z' || state == 'X' {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("child %d still alive in state %c", pid, state)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestTailBufferKeepsTail verifies the capture buffer keeps only the newest bytes.
func TestTailBufferKeepsTail(t *testing.T) {
	buf := &tailBuffer{limit: 8}
	for _, chunk := range []string{"abc", "defgh", "ijklm"} {
		if n, err := buf.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	if got := string(buf.Bytes()); got != "fghijklm" {
		t.Fatalf("tail = %q", got)
	}
}

// TestParseProcStat verifies the start time is found after the last parenthesis of the comm field.
func TestParseProcStat(t *testing.T) {
	fields := make([]string, 0, 40)
	for i := 3; i <= 44; i++ {
		fields = append(fields, strconv.Itoa(i))
	}
	fields[0] = "S"
	fields[19] = "123456"
	line := "4242 (weird) name (x)) " + strings.Join(fields, " ") + "\n"
	state, start, err := parseProcStat(line)
	if err != nil || state != 'S' || start != 123456 {
		t.Fatalf("parseProcStat = %c, %d, %v", state, start, err)
	}
	if _, _, err := parseProcStat("garbage"); err == nil {
		t.Fatal("parseProcStat accepted garbage")
	}
}

// TestKillProcessGroupIfSame verifies the killer checks the start time, kills a live group, and ignores dead pids.
func TestKillProcessGroupIfSame(t *testing.T) {
	child := exec.Command("/bin/sleep", "30")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan struct{})
	go func() {
		_ = child.Wait()
		close(waited)
	}()
	t.Cleanup(func() {
		_ = child.Process.Kill()
		<-waited
	})
	pid := child.Process.Pid
	start, err := ProcessStartTime(pid)
	if err != nil || start == 0 {
		t.Fatalf("ProcessStartTime = %d, %v", start, err)
	}
	killed, err := KillProcessGroupIfSame(pid, start+1)
	if killed || err != nil {
		t.Fatalf("mismatched start: killed=%t err=%v", killed, err)
	}
	if !processAlive(pid, start) {
		t.Fatal("process died although the start time did not match")
	}
	killed, err = KillProcessGroupIfSame(pid, start)
	if !killed || err != nil {
		t.Fatalf("matching start: killed=%t err=%v", killed, err)
	}
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("process survived KillProcessGroupIfSame")
	}
	killed, err = KillProcessGroupIfSame(pid, start)
	if killed || err != nil {
		t.Fatalf("dead pid: killed=%t err=%v", killed, err)
	}
	if killed, _ := KillProcessGroupIfSame(0, 0); killed {
		t.Fatal("pid 0 was reported killed")
	}
}

// TestPdeathsigHelperProcess starts an installer stand-in through ExecRunner and blocks, when re-executed as the Pdeathsig helper.
func TestPdeathsigHelperProcess(t *testing.T) {
	pidFile := os.Getenv(pdeathsigHelperEnv)
	if pidFile == "" {
		t.Skip("runs only as a re-executed helper")
	}
	_, _ = ExecRunner{}.Run(context.Background(), Command{Path: "/bin/sleep", Args: []string{"60"}, Env: []string{"PATH=/usr/bin:/bin"}}, func(pid int) {
		start, err := ProcessStartTime(pid)
		if err != nil {
			return
		}
		tmp := pidFile + ".tmp"
		if os.WriteFile(tmp, []byte(fmt.Sprintf("%d %d", pid, start)), 0644) == nil {
			_ = os.Rename(tmp, pidFile)
		}
	})
}

// TestPdeathsigKillsInstallerChild SIGKILLs a helper process that started an installer child and requires the child to die with it.
func TestPdeathsigKillsInstallerChild(t *testing.T) {
	if os.Getenv(pdeathsigHelperEnv) != "" {
		t.Skip("helper mode")
	}
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	helper := exec.Command(os.Args[0], "-test.run=^TestPdeathsigHelperProcess$", "-test.count=1", "-test.timeout=60s")
	helper.Env = append(os.Environ(), pdeathsigHelperEnv+"="+pidFile)
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	helperDone := make(chan struct{})
	go func() {
		_ = helper.Wait()
		close(helperDone)
	}()
	var pid int
	var start uint64
	deadline := time.Now().Add(30 * time.Second)
	for {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			if _, err := fmt.Sscanf(string(data), "%d %d", &pid, &start); err == nil {
				break
			}
		}
		if time.Now().After(deadline) {
			_ = helper.Process.Kill()
			t.Fatal("helper never reported its installer child")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if !processAlive(pid, start) {
		t.Fatalf("installer child %d is not running", pid)
	}
	if err := helper.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	<-helperDone
	deadline = time.Now().Add(5 * time.Second)
	for processAlive(pid, start) {
		if time.Now().After(deadline) {
			t.Fatalf("installer child %d outlived its SIGKILLed parent", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

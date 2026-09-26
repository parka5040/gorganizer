package smapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	maxRunnerOutput  = 256 << 10
	terminateGrace   = 5 * time.Second
	pipeDrainTimeout = 5 * time.Second
	killPollTimeout  = 5 * time.Second
	killPollInterval = 20 * time.Millisecond
)

type Command struct {
	Path    string
	Args    []string
	Dir     string
	Env     []string
	Timeout time.Duration
}

type Result struct {
	ExitCode int
	Output   []byte
}

type Runner interface {
	Run(ctx context.Context, cmd Command, onStart func(pid int)) (Result, error)
}

type ExecRunner struct{}

type runOutcome struct {
	result Result
	err    error
}

// Run starts cmd in its own process group on a dedicated locked OS thread and waits for it, killing the group on timeout or cancellation.
func (ExecRunner) Run(ctx context.Context, cmd Command, onStart func(pid int)) (Result, error) {
	if cmd.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cmd.Timeout)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return Result{ExitCode: -1}, fmt.Errorf("running %s: %w", cmd.Path, err)
	}
	done := make(chan runOutcome, 1)
	go func() {
		runtime.LockOSThread()
		result, err := runLocked(ctx, cmd, onStart)
		done <- runOutcome{result: result, err: err}
	}()
	outcome := <-done
	return outcome.result, outcome.err
}

// runLocked forks, supervises, and reaps the child on the calling locked OS thread.
func runLocked(ctx context.Context, cmd Command, onStart func(pid int)) (Result, error) {
	output := &tailBuffer{limit: maxRunnerOutput}
	child := exec.Command(cmd.Path, cmd.Args...)
	child.Dir = cmd.Dir
	child.Env = append([]string{}, cmd.Env...)
	child.Stdin = nil
	child.Stdout = output
	child.Stderr = output
	child.WaitDelay = pipeDrainTimeout
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if err := child.Start(); err != nil {
		return Result{ExitCode: -1}, fmt.Errorf("starting %s: %w", cmd.Path, err)
	}
	pid := child.Process.Pid
	if onStart != nil {
		onStart(pid)
	}
	exited := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			_ = syscall.Kill(-pid, syscall.SIGTERM)
			timer := time.NewTimer(terminateGrace)
			defer timer.Stop()
			select {
			case <-exited:
			case <-timer.C:
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		case <-exited:
		}
	}()
	waitExitNoReap(pid)
	close(exited)
	<-watcherDone
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	waitErr := child.Wait()
	result := Result{ExitCode: -1, Output: output.Bytes()}
	if child.ProcessState != nil {
		result.ExitCode = child.ProcessState.ExitCode()
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, fmt.Errorf("running %s: %w", cmd.Path, ctxErr)
	}
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) {
		return result, fmt.Errorf("waiting for %s: %w", cmd.Path, waitErr)
	}
	return result, nil
}

// waitExitNoReap blocks until pid has exited while leaving it unreaped so its process group id cannot be reused.
func waitExitNoReap(pid int) {
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err != unix.EINTR {
			return
		}
	}
}

type tailBuffer struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

// Write appends p and discards the oldest bytes beyond the limit.
func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if over := len(b.data) - b.limit; over > 0 {
		b.data = append(b.data[:0], b.data[over:]...)
	}
	return len(p), nil
}

// Bytes returns a copy of the retained tail.
func (b *tailBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.data...)
}

// ProcessStartTime returns the kernel start time of pid in clock ticks since boot.
func ProcessStartTime(pid int) (uint64, error) {
	_, start, err := procStat(pid)
	return start, err
}

// procStat reads the state letter and start time of pid from /proc/<pid>/stat.
func procStat(pid int) (byte, uint64, error) {
	if pid <= 0 {
		return 0, 0, fmt.Errorf("invalid pid %d", pid)
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, 0, err
	}
	state, start, err := parseProcStat(string(data))
	if err != nil {
		return 0, 0, fmt.Errorf("/proc/%d/stat: %w", pid, err)
	}
	return state, start, nil
}

// parseProcStat extracts the state letter and start time from a /proc stat line whose comm may contain spaces or parentheses.
func parseProcStat(text string) (byte, uint64, error) {
	closing := strings.LastIndexByte(text, ')')
	if closing < 0 {
		return 0, 0, errors.New("malformed stat line")
	}
	fields := strings.Fields(text[closing+1:])
	if len(fields) < 20 || len(fields[0]) != 1 {
		return 0, 0, errors.New("malformed stat line")
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("malformed start time: %w", err)
	}
	return fields[0][0], start, nil
}

// processAlive reports whether pid is a live, non-zombie process with the given start time.
func processAlive(pid int, start uint64) bool {
	state, current, err := procStat(pid)
	if err != nil {
		return false
	}
	return current == start && state != 'Z' && state != 'X'
}

// KillProcessGroupIfSame kills the process group led by pid only when pid still has the recorded start time, then waits for it to exit.
func KillProcessGroupIfSame(pid int, start uint64) (bool, error) {
	if pid <= 0 || pid == os.Getpid() {
		return false, nil
	}
	if !processAlive(pid, start) {
		return false, nil
	}
	pgid, err := syscall.Getpgid(pid)
	if err == nil && pgid == pid && pgid != syscall.Getpgrp() {
		err = syscall.Kill(-pgid, syscall.SIGKILL)
	} else {
		err = syscall.Kill(pid, syscall.SIGKILL)
	}
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		return false, fmt.Errorf("killing process %d: %w", pid, err)
	}
	deadline := time.Now().Add(killPollTimeout)
	for processAlive(pid, start) {
		if time.Now().After(deadline) {
			return true, fmt.Errorf("process %d is still alive after SIGKILL", pid)
		}
		time.Sleep(killPollInterval)
	}
	return true, nil
}

// killRecorded is the default recovery killer that terminates a still-live recorded installer process group.
func killRecorded(pid int, start uint64) error {
	_, err := KillProcessGroupIfSame(pid, start)
	return err
}

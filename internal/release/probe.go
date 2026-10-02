package release

import (
	"bytes"
	"context"
	"os/exec"
	"runtime"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type probeResult struct {
	output []byte
	err    error
}

// probeVersion runs a release version check in a supervised process group.
func probeVersion(ctx context.Context, binary string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result := make(chan probeResult, 1)
	go func() {
		runtime.LockOSThread()
		output, err := probeLocked(ctx, binary)
		result <- probeResult{output, err}
	}()
	outcome := <-result
	return outcome.output, outcome.err
}

// probeLocked starts and reaps a version probe on a dedicated thread.
func probeLocked(ctx context.Context, binary string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	child := exec.Command(binary, "--version")
	child.Stdout = &output
	child.Stderr = &output
	child.WaitDelay = time.Second
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if err := child.Start(); err != nil {
		return nil, err
	}
	pid := child.Process.Pid
	exited := make(chan struct{})
	watcher := make(chan struct{})
	go func() {
		defer close(watcher)
		select {
		case <-ctx.Done():
			_ = syscall.Kill(-pid, syscall.SIGTERM)
			timer := time.NewTimer(200 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-exited:
			case <-timer.C:
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		case <-exited:
		}
	}()
	for {
		var info unix.Siginfo
		if err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil); err != unix.EINTR {
			break
		}
	}
	close(exited)
	<-watcher
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	err := child.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return output.Bytes(), err
}

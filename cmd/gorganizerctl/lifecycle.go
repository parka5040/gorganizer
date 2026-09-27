package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	pb "github.com/parka/gorganizer/api/proto"
)

const lifecyclePollInterval = 150 * time.Millisecond

type lifecycleDeps struct {
	out      io.Writer
	errOut   io.Writer
	procRoot string
}

// lifecycleOutput supplies output streams and the process table for lifecycle commands.
func lifecycleOutput() lifecycleDeps {
	return lifecycleDeps{out: os.Stdout, errOut: os.Stderr, procRoot: "/proc"}
}

// runPing reports the currently answering daemon or a shutdown in progress.
func runPing(args []string) int {
	return runPingWith(args, lifecycleOutput())
}

// runPingWith probes Health once and writes the result to the supplied streams.
func runPingWith(args []string, deps lifecycleDeps) int {
	fs := flag.NewFlagSet("ping", flag.ContinueOnError)
	fs.SetOutput(deps.errOut)
	socket := fs.String("socket-path", "", "daemon socket path")
	if fs.Parse(args) != nil || fs.NArg() != 0 {
		return 2
	}
	conn, err := dialDaemon(*socket)
	if err != nil {
		fmt.Fprintln(deps.out, "Gorganizer is not running.")
		return 1
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := pb.NewGorganizerClient(conn).Health(ctx, &pb.HealthRequest{})
	if err != nil {
		fmt.Fprintln(deps.out, "Gorganizer is not running.")
		return 1
	}
	if resp.GetStopping() {
		fmt.Fprintln(deps.out, "Gorganizer is shutting down.")
		return 3
	}
	fmt.Fprintf(deps.out, "running version %s (instance %s, pid %d)\n", resp.GetVersion(), resp.GetInstanceId(), resp.GetPid())
	return 0
}

// runWaitReady waits until the answering daemon completes startup.
func runWaitReady(args []string) int {
	return runWaitReadyWith(args, lifecycleOutput())
}

// runWaitReadyWith polls Health up to the requested timeout using the supplied streams.
func runWaitReadyWith(args []string, deps lifecycleDeps) int {
	fs := flag.NewFlagSet("wait-ready", flag.ContinueOnError)
	fs.SetOutput(deps.errOut)
	socket := fs.String("socket-path", "", "daemon socket path")
	timeout := fs.Duration("timeout", 60*time.Second, "maximum time to wait")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *timeout < 0 {
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	conn, err := dialDaemon(*socket)
	if err == nil {
		defer conn.Close()
		client := pb.NewGorganizerClient(conn)
		for ctx.Err() == nil {
			callCtx, stop := context.WithTimeout(ctx, time.Second)
			resp, callErr := client.Health(callCtx, &pb.HealthRequest{})
			stop()
			if callErr == nil && resp.GetRecoveryDone() && resp.GetGamesWarmed() && !resp.GetStopping() {
				return 0
			}
			select {
			case <-ctx.Done():
			case <-time.After(lifecyclePollInterval):
			}
		}
	}
	fmt.Fprintln(deps.out, "Gorganizer did not become ready in time.")
	return 1
}

// runStop requests a graceful shutdown and waits for the responding process to exit.
func runStop(args []string) int {
	return runStopWith(args, lifecycleOutput())
}

// runStopWith tracks the responding daemon's process identity until it exits or the timeout expires.
func runStopWith(args []string, deps lifecycleDeps) int {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	fs.SetOutput(deps.errOut)
	socket := fs.String("socket-path", "", "daemon socket path")
	timeout := fs.Duration("timeout", 46*time.Second, "maximum time to wait")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *timeout < 0 {
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	conn, err := dialDaemon(*socket)
	if err != nil {
		fmt.Fprintln(deps.out, "Gorganizer is not running.")
		return 0
	}
	defer conn.Close()
	client := pb.NewGorganizerClient(conn)
	probeCtx, stopProbe := context.WithTimeout(ctx, 2*time.Second)
	resp, err := client.Health(probeCtx, &pb.HealthRequest{})
	stopProbe()
	if err != nil {
		fmt.Fprintln(deps.out, "Gorganizer is not running.")
		return 0
	}
	if resp.GetInstanceId() == "" || resp.GetPid() <= 0 {
		fmt.Fprintln(deps.out, "Could not verify Gorganizer's process.")
		return 1
	}
	pid := int(resp.GetPid())
	start, err := processStartTime(deps.procRoot, pid)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(deps.out, "Could not verify Gorganizer's process.")
		return 1
	}
	alreadyExited := errors.Is(err, os.ErrNotExist)
	shutdownCtx, stopShutdown := context.WithTimeout(ctx, 2*time.Second)
	_, _ = client.Shutdown(shutdownCtx, &pb.ShutdownRequest{})
	stopShutdown()
	if alreadyExited {
		fmt.Fprintln(deps.out, "Gorganizer is not running.")
		return 0
	}
	exited, err := waitProcessExit(ctx, deps.procRoot, pid, start)
	if err != nil {
		fmt.Fprintln(deps.out, "Could not verify Gorganizer's process.")
		return 1
	}
	if !exited {
		fmt.Fprintln(deps.out, "Gorganizer is still finishing a mod operation. It will stop on its own; check again in a minute.")
		return 1
	}
	return 0
}

// waitProcessExit waits for a process with a verified start time to exit.
func waitProcessExit(ctx context.Context, procRoot string, pid int, start uint64) (bool, error) {
	for {
		alive, err := processAlive(procRoot, pid, start)
		if err != nil || !alive {
			return !alive, err
		}
		select {
		case <-ctx.Done():
			return false, nil
		case <-time.After(lifecyclePollInterval):
		}
	}
}

// processStartTime reads the kernel start time of a process from its stat file.
func processStartTime(procRoot string, pid int) (uint64, error) {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, fmt.Errorf("reading process %d: %w", pid, err)
	}
	_, start, err := parseProcessStat(string(data))
	return start, err
}

// processAlive checks that a process still has its original start time and is not a zombie.
func processAlive(procRoot string, pid int, start uint64) (bool, error) {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading process %d: %w", pid, err)
	}
	state, current, err := parseProcessStat(string(data))
	if err != nil {
		return false, err
	}
	return current == start && state != 'Z' && state != 'X', nil
}

// parseProcessStat extracts the process state and start time from a Linux stat line.
func parseProcessStat(text string) (byte, uint64, error) {
	closing := strings.LastIndexByte(text, ')')
	if closing < 0 {
		return 0, 0, errors.New("invalid process stat")
	}
	fields := strings.Fields(text[closing+1:])
	if len(fields) < 20 || len(fields[0]) != 1 {
		return 0, 0, errors.New("invalid process stat")
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid process start time: %w", err)
	}
	return fields[0][0], start, nil
}

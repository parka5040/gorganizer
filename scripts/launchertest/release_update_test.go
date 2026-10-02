package launchertest

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// updateExitCode returns the shell status from a rejected update.
func updateExitCode(t *testing.T, err error) int {
	t.Helper()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("unexpected update error: %v", err)
	}
	return exitErr.ExitCode()
}

// TestReleaseUpdateExecsAndForwardsTag checks release updates replace the launcher and forward a selected tag.
func TestReleaseUpdateExecsAndForwardsTag(t *testing.T) {
	for _, tc := range []struct {
		name, command, want string
	}{
		{name: "selected tag", command: "cmd_update --tag v1.2.3", want: "<release>\n<update>\n<--tag>\n<v1.2.3>\n"},
		{name: "latest release", command: "cmd_update", want: "<release>\n<update>\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := releaseFixture(t)
			callerPID := filepath.Join(t.TempDir(), "caller-pid")
			ctlPID := filepath.Join(t.TempDir(), "ctl-pid")
			calls := filepath.Join(t.TempDir(), "calls")
			writeFixtureFile(t, filepath.Join(f.root, "bin", "gorganizerctl"), []byte("#!/bin/bash\nprintf '%s\\n' \"$$\" > \"$FAKE_CTL_PID\"\nprintf '<%s>\\n' \"$@\" > \"$FAKE_CTL_CALLS\"\n"), 0o755)
			out, err := f.run(t, "printf '%s\\n' \"$$\" > \"$FAKE_CALLER_PID\"; "+tc.command, "FAKE_CALLER_PID="+callerPID, "FAKE_CTL_PID="+ctlPID, "FAKE_CTL_CALLS="+calls)
			if err != nil || out != "" {
				t.Fatalf("update: %v: %q", err, out)
			}
			if got := string(readFixtureFile(t, calls)); got != tc.want {
				t.Fatalf("arguments = %q, want %q", got, tc.want)
			}
			if got, want := string(readFixtureFile(t, ctlPID)), string(readFixtureFile(t, callerPID)); got != want {
				t.Fatalf("ctl pid = %q, caller pid = %q", got, want)
			}
		})
	}
}

// TestReleaseUpdateRestartKeepsReminder checks restart mode runs the update before printing its reminder.
func TestReleaseUpdateRestartKeepsReminder(t *testing.T) {
	f := releaseFixture(t)
	calls := filepath.Join(t.TempDir(), "calls")
	writeFixtureFile(t, filepath.Join(f.root, "bin", "gorganizerctl"), []byte("#!/bin/sh\nprintf '<%s>\\n' \"$@\" > \"$FAKE_CTL_CALLS\"\n"), 0o755)
	out, err := f.run(t, "cmd_update --restart --tag v1.2.3", "FAKE_CTL_CALLS="+calls)
	if err != nil || !strings.Contains(out, "Close Gorganizer and open it again to use the new version now.") {
		t.Fatalf("restart update: %v: %q", err, out)
	}
	if got := string(readFixtureFile(t, calls)); got != "<release>\n<update>\n<--tag>\n<v1.2.3>\n" {
		t.Fatalf("arguments = %q", got)
	}
}

// TestReleaseUpdateRestartForwardsSIGTERM checks the update child receives TERM and no reminder is printed.
func TestReleaseUpdateRestartForwardsSIGTERM(t *testing.T) {
	f := releaseFixture(t)
	ready := filepath.Join(t.TempDir(), "ready")
	terminated := filepath.Join(t.TempDir(), "terminated")
	writeFixtureFile(t, filepath.Join(f.root, "bin", "gorganizerctl"), []byte("#!/bin/bash\ntrap 'printf term > \"$FAKE_CTL_TERM\"; exit 143' TERM\nprintf ready > \"$FAKE_CTL_READY\"\nfor i in {1..30}; do sleep 0.05; done\n"), 0o755)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", `. "$1"; cmd_update --restart`, "bash", filepath.Join(f.root, "gorganizer.sh"))
	cmd.Dir = f.root
	cmd.Env = append(os.Environ(), "GORGANIZER_SH_SOURCE_ONLY=1", "FAKE_CTL_READY="+ready, "FAKE_CTL_TERM="+terminated)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if ctx.Err() != nil {
			t.Fatal("ctl did not start before timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(cmd.Process.Pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	if ctx.Err() != nil || err == nil || !strings.Contains(string(readFixtureFile(t, terminated)), "term") || strings.Contains(output.String(), "Close Gorganizer and open it again") {
		t.Fatalf("signal update: %v: %q; context=%v", err, output.String(), ctx.Err())
	}
}

// TestReleaseUpdateRejectsInvalidTags checks invalid or repeated selected tags never invoke the control binary.
func TestReleaseUpdateRejectsInvalidTags(t *testing.T) {
	for _, command := range []string{
		"cmd_update --tag 1.2.3",
		"cmd_update --tag v1.2",
		"cmd_update --tag v1.2.3.4",
		"cmd_update --tag v1234567890.0.0",
		"cmd_update --tag v1.2.3 --tag v1.2.4",
		`cmd_update --tag ""`,
		`cmd_update --tag "" --tag v1.2.3`,
	} {
		t.Run(command, func(t *testing.T) {
			f := releaseFixture(t)
			calls := filepath.Join(t.TempDir(), "calls")
			writeFixtureFile(t, filepath.Join(f.root, "bin", "gorganizerctl"), []byte("#!/bin/sh\nprintf called > \"$FAKE_CTL_CALLS\"\n"), 0o755)
			out, err := f.run(t, command, "FAKE_CTL_CALLS="+calls)
			if err == nil || updateExitCode(t, err) != 2 || !strings.Contains(out, "--tag") {
				t.Fatalf("invalid update: %v: %q", err, out)
			}
			if _, err := os.Stat(calls); !os.IsNotExist(err) {
				t.Fatalf("control binary was called: %v", err)
			}
		})
	}
}

// TestSourceUpdateRejectsTag checks source checkouts reject the release-only tag option.
func TestSourceUpdateRejectsTag(t *testing.T) {
	for _, command := range []string{"cmd_update --tag v1.2.3", `cmd_update --tag ""`} {
		t.Run(command, func(t *testing.T) {
			f := newFixture(t)
			calls := filepath.Join(t.TempDir(), "calls")
			writeFixtureFile(t, filepath.Join(f.root, "gorganizerctl"), []byte("#!/bin/sh\nprintf called > \"$FAKE_CTL_CALLS\"\n"), 0o755)
			out, err := f.run(t, command, "FAKE_CTL_CALLS="+calls)
			if err == nil || updateExitCode(t, err) != 2 || !strings.Contains(out, "--tag is only available for prebuilt installs.") {
				t.Fatalf("source update: %v: %q", err, out)
			}
			if _, err := os.Stat(calls); !os.IsNotExist(err) {
				t.Fatalf("control binary was called: %v", err)
			}
		})
	}
}

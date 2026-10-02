package launchertest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

// TestReleaseUpdateRejectsInvalidTags checks invalid or repeated selected tags never invoke the control binary.
func TestReleaseUpdateRejectsInvalidTags(t *testing.T) {
	for _, command := range []string{
		"cmd_update --tag 1.2.3",
		"cmd_update --tag v1.2",
		"cmd_update --tag v1.2.3.4",
		"cmd_update --tag v1234567890.0.0",
		"cmd_update --tag v1.2.3 --tag v1.2.4",
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
	f := newFixture(t)
	calls := filepath.Join(t.TempDir(), "calls")
	writeFixtureFile(t, filepath.Join(f.root, "gorganizerctl"), []byte("#!/bin/sh\nprintf called > \"$FAKE_CTL_CALLS\"\n"), 0o755)
	out, err := f.run(t, "cmd_update --tag v1.2.3", "FAKE_CTL_CALLS="+calls)
	if err == nil || updateExitCode(t, err) != 2 || !strings.Contains(out, "--tag is only available for prebuilt installs.") {
		t.Fatalf("source update: %v: %q", err, out)
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatalf("control binary was called: %v", err)
	}
}

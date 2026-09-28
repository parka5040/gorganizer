package testsafe

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLauncherShimsRecordInvocations verifies every launcher shim is executable, logs its arguments, and fails.
func TestLauncherShimsRecordInvocations(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	logPath, err := InstallLauncherShims(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range launcherNames {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name)
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode()&0o111 == 0 {
				t.Fatalf("%s is not executable: %v", name, info.Mode())
			}
			err = exec.Command(path, "first", "two words").Run()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() == 0 {
				t.Fatalf("%s exit error = %v, want a non-zero exit", name, err)
			}
		})
	}

	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(lines) != len(launcherNames) {
		t.Fatalf("launcher calls = %q, want %d invocations", calls, len(launcherNames))
	}
	for i, name := range launcherNames {
		if want := name + " first two words"; lines[i] != want {
			t.Errorf("launcher call %d = %q, want %q", i, lines[i], want)
		}
	}
}

// TestNotificationShimsStaySilent verifies desktop notifications succeed without reaching the desktop or the launcher log.
func TestNotificationShimsStaySilent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	logPath, err := InstallLauncherShims(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range quietNames {
		if err := exec.Command(filepath.Join(dir, name), "Gorganizer", "a message").Run(); err != nil {
			t.Fatalf("%s = %v, want success", name, err)
		}
	}
	if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("notification reached the launcher log: %v", err)
	}
	calls, err := os.ReadFile(filepath.Join(dir, "notification-calls.log"))
	if err != nil || !strings.Contains(string(calls), "notify-send Gorganizer a message") {
		t.Fatalf("notification calls = %q, %v", calls, err)
	}
}

package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/parka/gorganizer/internal/testsafe"
)

var launcherShims = []string{"xdg-open", "steam", "gio", "kde-open", "gnome-open", "flatpak", "protontricks"}

// TestMain runs the package's tests with launcher shims, an empty process table, and a failing Steam opener.
func TestMain(m *testing.M) {
	os.Exit(runWithSafeDaemonEnvironment(m))
}

// runWithSafeDaemonEnvironment runs the tests and fails if a test reaches the default Steam opener.
func runWithSafeDaemonEnvironment(m *testing.M) int {
	dir, err := os.MkdirTemp("", "gorganizer-daemon-tests-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating the daemon test directory: %v\n", err)
		return 2
	}
	defer os.RemoveAll(dir)
	logPath := filepath.Join(dir, "steam-opener.log")
	emptyProc := filepath.Join(dir, "proc")
	if err := os.Mkdir(emptyProc, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "creating the empty process table: %v\n", err)
		return 2
	}
	processTableRoot = emptyProc
	defaultSteamOpener = func(url string) (int, error) {
		if f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintf(f, "steam opener %s\n", url)
			f.Close()
		}
		fmt.Fprintf(os.Stderr, "FAIL: a test tried to open %s through the real Steam opener\n", url)
		return 0, fmt.Errorf("tests never open %s", url)
	}
	code := testsafe.RunWithSafeEnvironment(m)
	if calls, err := os.ReadFile(logPath); err == nil && len(calls) > 0 {
		fmt.Fprintf(os.Stderr, "FAIL: tests invoked a desktop launcher:\n%s", calls)
		if code == 0 {
			code = 1
		}
	}
	return code
}

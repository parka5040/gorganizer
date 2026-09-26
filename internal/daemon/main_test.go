package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

var launcherShims = []string{"xdg-open", "steam", "gio", "kde-open", "gnome-open"}

// TestMain runs the package's tests with every desktop launcher shimmed to fail and log, no real process table, and a Steam opener that logs, failing the run if any test reached a launcher.
func TestMain(m *testing.M) {
	os.Exit(runWithLauncherShims(m))
}

// runWithLauncherShims installs the launcher shims and the failing default Steam opener, runs the tests, and turns any recorded launcher call into a failed run.
func runWithLauncherShims(m *testing.M) int {
	dir, err := os.MkdirTemp("", "gorganizer-launcher-shims-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating launcher shims: %v\n", err)
		return 2
	}
	defer os.RemoveAll(dir)
	logPath := filepath.Join(dir, "launcher-calls.log")
	for _, name := range launcherShims {
		script := fmt.Sprintf("#!/bin/sh\nprintf '%%s %%s\\n' %q \"$*\" >> %q\nexit 97\n", name, logPath)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "writing the %s shim: %v\n", name, err)
			return 2
		}
	}
	emptyProc := filepath.Join(dir, "proc")
	if err := os.Mkdir(emptyProc, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "creating the empty process table: %v\n", err)
		return 2
	}
	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		fmt.Fprintf(os.Stderr, "prepending the launcher shims to PATH: %v\n", err)
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
	code := m.Run()
	if calls, err := os.ReadFile(logPath); err == nil && len(calls) > 0 {
		fmt.Fprintf(os.Stderr, "FAIL: tests invoked a desktop launcher:\n%s", calls)
		if code == 0 {
			code = 1
		}
	}
	return code
}

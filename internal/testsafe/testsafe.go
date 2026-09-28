package testsafe

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var launcherNames = []string{"xdg-open", "steam", "gio", "kde-open", "gnome-open", "flatpak", "protontricks"}

var quietNames = []string{"notify-send"}

// InstallLauncherShims writes failing launcher scripts and silent desktop-notification scripts into dir and returns the launcher log path.
func InstallLauncherShims(dir string) (string, error) {
	logPath := filepath.Join(dir, "launcher-calls.log")
	quotedLogPath := "'" + strings.ReplaceAll(logPath, "'", "'\\''") + "'"
	for _, name := range launcherNames {
		script := fmt.Sprintf("#!/bin/sh\nprintf '%%s %%s\\n' '%s' \"$*\" >> %s\nexit 97\n", name, quotedLogPath)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			return "", fmt.Errorf("writing the %s shim: %w", name, err)
		}
	}
	quotedNotificationPath := "'" + strings.ReplaceAll(filepath.Join(dir, "notification-calls.log"), "'", "'\\''") + "'"
	for _, name := range quietNames {
		script := fmt.Sprintf("#!/bin/sh\nprintf '%%s %%s\\n' '%s' \"$*\" >> %s\nexit 0\n", name, quotedNotificationPath)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			return "", fmt.Errorf("writing the %s shim: %w", name, err)
		}
	}
	return logPath, nil
}

// RunWithSafeEnvironment runs tests with isolated directories, failing launcher shims and silent notification shims.
func RunWithSafeEnvironment(m *testing.M) int {
	root, err := os.MkdirTemp("", "gzt")
	if err != nil {
		fmt.Fprintf(os.Stderr, "creating the test root: %v\n", err)
		return 2
	}
	defer os.RemoveAll(root)

	for _, name := range []string{"config", "data", "state", "cache", "tmp", "shims"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			fmt.Fprintf(os.Stderr, "creating the test %s directory: %v\n", name, err)
			return 2
		}
	}

	shimDir := filepath.Join(root, "shims")
	logPath, err := InstallLauncherShims(shimDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "installing launcher shims: %v\n", err)
		return 2
	}

	settings := []struct{ name, value string }{
		{"HOME", root},
		{"XDG_CONFIG_HOME", filepath.Join(root, "config")},
		{"XDG_DATA_HOME", filepath.Join(root, "data")},
		{"XDG_STATE_HOME", filepath.Join(root, "state")},
		{"XDG_CACHE_HOME", filepath.Join(root, "cache")},
		{"XDG_RUNTIME_DIR", root},
		{"TMPDIR", filepath.Join(root, "tmp")},
		{"GORGANIZER_ROOT", root},
		{"PATH", shimDir + string(os.PathListSeparator) + os.Getenv("PATH")},
	}
	for _, setting := range settings {
		if err := os.Setenv(setting.name, setting.value); err != nil {
			fmt.Fprintf(os.Stderr, "setting %s for tests: %v\n", setting.name, err)
			return 2
		}
	}

	code := m.Run()
	calls, err := os.ReadFile(logPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "reading launcher calls: %v\n", err)
		return 2
	}
	if len(calls) > 0 {
		fmt.Fprintf(os.Stderr, "FAIL: tests invoked a desktop launcher:\n%s", calls)
		if code == 0 {
			code = 1
		}
	}
	return code
}

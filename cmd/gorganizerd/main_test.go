package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/testsafe"
)

// TestMain runs the command tests with isolated directories and failing launcher shims.
func TestMain(m *testing.M) {
	os.Exit(testsafe.RunWithSafeEnvironment(m))
}

// TestPrintSocketPath verifies socket overrides and runtime fallbacks without creating state or taking a lock.
func TestPrintSocketPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	for _, tc := range []struct {
		name     string
		runtime  string
		override string
		want     string
	}{
		{"XDG runtime", filepath.Join(root, "runtime"), "", filepath.Join(root, "runtime", "gorganizer", "gorganizer.sock")},
		{"temporary fallback", "", "", filepath.Join(root, "gorganizer-"+strconv.Itoa(os.Getuid()), "gorganizer.sock")},
		{"override", filepath.Join(root, "runtime"), filepath.Join(root, "custom.sock"), filepath.Join(root, "custom.sock")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_RUNTIME_DIR", tc.runtime)
			var out bytes.Buffer
			printSocketPath(&out, tc.override)
			if got := out.String(); got != tc.want+"\n" {
				t.Fatalf("path = %q, want %q", got, tc.want+"\n")
			}
			if _, err := os.Stat(config.RuntimeDir()); !os.IsNotExist(err) {
				t.Fatalf("runtime directory was created or inaccessible: %v", err)
			}
			if _, err := os.Stat(config.LockPath()); !os.IsNotExist(err) {
				t.Fatalf("lock was created or inaccessible: %v", err)
			}
		})
	}
}

// TestParseLogLevel verifies supported levels and the default for unrecognized names.
func TestParseLogLevel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct {
		name string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
		{"unknown", slog.LevelInfo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseLogLevel(tc.name); got != tc.want {
				t.Fatalf("parseLogLevel(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

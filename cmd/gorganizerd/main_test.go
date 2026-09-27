package main

import (
	"log/slog"
	"os"
	"testing"

	"github.com/parka/gorganizer/internal/testsafe"
)

// TestMain runs the command tests with isolated directories and failing launcher shims.
func TestMain(m *testing.M) {
	os.Exit(testsafe.RunWithSafeEnvironment(m))
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

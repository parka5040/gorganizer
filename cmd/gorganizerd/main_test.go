package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/migrate"

	"github.com/parka/gorganizer/internal/testsafe"
)

// TestMain runs the command tests with isolated directories and failing launcher shims.
func TestMain(m *testing.M) {
	os.Exit(testsafe.RunWithSafeEnvironment(m))
}

// TestDaemonRefusesToStartDuringMigration checks an unfinished journal blocks daemon startup even when it is unreadable.
func TestDaemonRefusesToStartDuringMigration(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("GORGANIZER_ROOT", filepath.Join(root, "checkout"))
	if err := checkMigrationBeforeStart(); err != nil {
		t.Fatal(err)
	}
	path := migrate.JournalPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not even valid JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkMigrationBeforeStart(); err == nil || !strings.Contains(err.Error(), "migrate-data --resume") {
		t.Fatalf("startup check = %v", err)
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

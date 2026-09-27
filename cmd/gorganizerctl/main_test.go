package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parka/gorganizer/internal/testsafe"
)

// TestMain runs the command tests with isolated directories and failing launcher shims.
func TestMain(m *testing.M) {
	os.Exit(testsafe.RunWithSafeEnvironment(m))
}

// TestResolveDataPathExplicitPath verifies a supplied path does not require config or Steam discovery.
func TestResolveDataPathExplicitPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "Data")
	got, err := resolveDataPath("", path)
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("resolveDataPath() = %q, want %q", got, path)
	}
}

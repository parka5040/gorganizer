package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/diag"
	"github.com/parka/gorganizer/internal/steam"
)

// TestDoctorExitStatus checks a stopped service is informational and a private-folder problem fails.
func TestDoctorExitStatus(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", root)
	t.Setenv("GORGANIZER_ROOT", "")
	proc := filepath.Join(root, "proc")
	if err := os.Mkdir(proc, 0o700); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	deps := doctorDeps{out: &out, errOut: &stderr, options: diag.Options{Version: "test", ProcRoot: proc,
		Health:         func(context.Context) (diag.Health, error) { return diag.Health{}, errors.New("stopped") },
		FindSteamRoots: func() ([]steam.Root, error) { return nil, os.ErrNotExist },
		LookPath:       func(string) (string, error) { return "", os.ErrNotExist }}}
	if got := runDoctorWith(nil, deps); got != 0 || !strings.Contains(out.String(), "Note — Background service: Not running.") {
		t.Fatalf("healthy doctor = %d, output = %s", got, out.String())
	}
	out.Reset()
	runtime := filepath.Join(root, "gorganizer")
	if err := os.Mkdir(runtime, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(runtime, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := runDoctorWith(nil, deps); got != 1 || !strings.Contains(out.String(), "Problem — Runtime folder:") {
		t.Fatalf("unsafe runtime doctor = %d, output = %s", got, out.String())
	}
}

// TestBugReportCommandConfirmsNoUpload checks that the command prints the saved path and sharing notice.
func TestBugReportCommandConfirmsNoUpload(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", root)
	t.Setenv("GORGANIZER_ROOT", "")
	var out, stderr bytes.Buffer
	deps := doctorDeps{out: &out, errOut: &stderr, options: diag.Options{Version: "test",
		FindSteamRoots: func() ([]steam.Root, error) { return nil, os.ErrNotExist },
		LookPath:       func(string) (string, error) { return "", os.ErrNotExist }}}
	if got := runBugReportWith([]string{"--out", root}, deps); got != 0 {
		t.Fatalf("bug-report = %d, error = %s", got, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(filepath.Base(lines[0]), "gorganizer-report-") || lines[1] != "Nothing was uploaded. Attach this file to your report if you want to share it." {
		t.Fatalf("unexpected output: %q", out.String())
	}
	if info, err := os.Stat(lines[0]); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("report = %v, error = %v", info, err)
	}
}

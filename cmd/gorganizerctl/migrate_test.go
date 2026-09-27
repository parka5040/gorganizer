package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/instancelock"
	"github.com/parka/gorganizer/internal/migrate"
)

// migrateFixture creates an isolated runtime, old mods folder and output streams.
func migrateFixture(t *testing.T) (string, string, migrateDeps, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "runtime"))
	t.Setenv("GORGANIZER_ROOT", filepath.Join(root, "checkout"))
	if err := os.Mkdir(filepath.Join(root, "runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	from := filepath.Join(root, "checkout")
	mods := filepath.Join(from, "SkyrimSE_Mods")
	if err := os.MkdirAll(filepath.Join(mods, "Downloads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mods, "Downloads", "archive.zip"), []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	return from, mods, migrateDeps{in: strings.NewReader("n\n"), out: &out, errOut: &errOut}, &out, &errOut
}

// TestMigrateDataDryRunChangesNothing checks a dry run and a noninteractive refusal leave all persistent user files untouched.
func TestMigrateDataDryRunChangesNothing(t *testing.T) {
	from, mods, deps, out, errOut := migrateFixture(t)
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"dry run", []string{"--from", from, "--dry-run"}, 0},
		{"no confirmation", []string{"--from", from}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out.Reset()
			errOut.Reset()
			if got := runMigrateDataWith(tc.args, deps); got != tc.want {
				t.Fatalf("exit = %d, want %d: %s", got, tc.want, errOut.String())
			}
			if !strings.Contains(out.String(), "archive") && !strings.Contains(out.String(), "Skyrim Special Edition") {
				t.Fatalf("no move plan: %s", out.String())
			}
			if body, err := os.ReadFile(filepath.Join(mods, "Downloads", "archive.zip")); err != nil || string(body) != "archive" {
				t.Fatalf("source = %q, %v", body, err)
			}
			if _, err := os.Lstat(migrate.JournalPath()); !os.IsNotExist(err) {
				t.Fatalf("journal changed: %v", err)
			}
			if _, err := os.Lstat(config.XDGModsDir("skyrimse")); !os.IsNotExist(err) {
				t.Fatalf("destination changed: %v", err)
			}
		})
	}
}

// TestMigrateDataJSONDryRun lists only registered old folder paths without moving them.
func TestMigrateDataJSONDryRun(t *testing.T) {
	from, mods, deps, out, errOut := migrateFixture(t)
	for _, tc := range []struct {
		name    string
		prepare func(*testing.T)
		want    int
	}{
		{"available", func(*testing.T) {}, 0},
		{"blocked", func(t *testing.T) {
			path := filepath.Join(config.XDGModsDir("skyrimse"), "existing.esp")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := atomicfile.WriteFile(path, []byte("existing"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out.Reset()
			errOut.Reset()
			tc.prepare(t)
			if code := runMigrateDataWith([]string{"--from", from, "--dry-run", "--json"}, deps); code != tc.want {
				t.Fatalf("exit = %d, want %d: %s", code, tc.want, errOut.String())
			}
			var got struct {
				Sources []string `json:"sources"`
			}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got.Sources) != 1 || got.Sources[0] != mods {
				t.Fatalf("sources = %q, %v", out.String(), err)
			}
			if _, err := os.Lstat(mods); err != nil {
				t.Fatalf("old folder changed: %v", err)
			}
		})
	}
}

// TestMigrateDataStatus checks journal presence without taking the daemon lock or changing files.
func TestMigrateDataStatus(t *testing.T) {
	_, _, deps, out, errOut := migrateFixture(t)
	release, err := instancelock.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, tc := range []struct {
		name    string
		prepare func(*testing.T)
		want    string
	}{
		{"none", func(*testing.T) {}, "none\n"},
		{"pending", func(t *testing.T) {
			if err := os.MkdirAll(config.DataDir(), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := atomicfile.WriteFile(migrate.JournalPath(), []byte("pending"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "pending\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out.Reset()
			errOut.Reset()
			tc.prepare(t)
			if code := runMigrateDataWith([]string{"--status"}, deps); code != 0 || out.String() != tc.want || errOut.Len() != 0 {
				t.Fatalf("status = %d, output = %q, error = %q", code, out.String(), errOut.String())
			}
		})
	}
}

// TestMigrateDataJSONDryRunNoFoldersWhileDaemonRuns checks a clean checkout can be detected without locking the active daemon.
func TestMigrateDataJSONDryRunNoFoldersWhileDaemonRuns(t *testing.T) {
	from, mods, deps, out, errOut := migrateFixture(t)
	if err := os.RemoveAll(mods); err != nil {
		t.Fatal(err)
	}
	release, err := instancelock.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if code := runMigrateDataWith([]string{"--from", from, "--dry-run", "--json"}, deps); code != 0 || out.String() != "{\"sources\":[]}\n" || errOut.Len() != 0 {
		t.Fatalf("empty plan = %d, output = %q, error = %q", code, out.String(), errOut.String())
	}
}

// TestMigrateDataRefusesWhileDaemonRuns checks a held daemon lock blocks even a dry run.
func TestMigrateDataRefusesWhileDaemonRuns(t *testing.T) {
	from, mods, deps, _, errOut := migrateFixture(t)
	release, err := instancelock.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if code := runMigrateDataWith([]string{"--from", from, "--yes"}, deps); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "Close Gorganizer first") {
		t.Fatalf("refusal = %q", errOut.String())
	}
	if _, err := os.Stat(mods); err != nil {
		t.Fatalf("old mods changed: %v", err)
	}
}

// TestMigrateDataResumeWithoutJournal checks a completed move needs no additional work.
func TestMigrateDataResumeWithoutJournal(t *testing.T) {
	_, mods, deps, out, errOut := migrateFixture(t)
	if code := runMigrateDataWith([]string{"--resume"}, deps); code != 0 {
		t.Fatalf("exit = %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "No unfinished move") {
		t.Fatalf("misleading completion message = %q", out.String())
	}
	if _, err := os.Stat(mods); err != nil {
		t.Fatalf("old mods changed: %v", err)
	}
}

// TestCopyProgressLimitsOutput checks copied bytes appear no more than once per second.
func TestCopyProgressLimitsOutput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	current := time.Unix(100, 0)
	progress := printCopyProgress(&out, func() time.Time { return current })
	progress(1200000000, 48000000000)
	current = current.Add(500 * time.Millisecond)
	progress(2200000000, 48000000000)
	current = current.Add(500 * time.Millisecond)
	progress(3200000000, 48000000000)
	if got, want := out.String(), "Copied 1.2 GB of 48.0 GB…\nCopied 3.2 GB of 48.0 GB…\n"; got != want {
		t.Fatalf("progress = %q, want %q", got, want)
	}
}

// TestMigrateDataConfirmedMove checks --yes moves files without a terminal and a later run does nothing.
func TestMigrateDataConfirmedMove(t *testing.T) {
	from, mods, deps, out, errOut := migrateFixture(t)
	if code := runMigrateDataWith([]string{"--from", from, "--yes"}, deps); code != 0 {
		t.Fatalf("exit = %d: %s", code, errOut.String())
	}
	if _, err := os.Lstat(mods); !os.IsNotExist(err) {
		t.Fatalf("source remains: %v", err)
	}
	if strings.Contains(out.String(), "Copied") {
		t.Fatalf("rename reported copy progress: %s", out.String())
	}
	if body, err := os.ReadFile(filepath.Join(config.XDGModsDir("skyrimse"), "Downloads", "archive.zip")); err != nil || string(body) != "archive" {
		t.Fatalf("download = %q, %v", body, err)
	}
	if code := runMigrateDataWith([]string{"--from", from, "--yes"}, deps); code != 0 {
		t.Fatalf("repeat exit = %d: %s", code, errOut.String())
	}
}

// TestMigrateDataCountDryRun prints only the number of old folders for the launcher.
func TestMigrateDataCountDryRun(t *testing.T) {
	from, _, deps, out, errOut := migrateFixture(t)
	if code := runMigrateDataWith([]string{"--from", from, "--dry-run", "--count"}, deps); code != 0 || out.String() != "1\n" || errOut.Len() != 0 {
		t.Fatalf("count = %d, output = %q, error = %q", code, out.String(), errOut.String())
	}
}

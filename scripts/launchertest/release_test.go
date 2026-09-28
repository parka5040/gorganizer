package launchertest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// releaseFixture creates a prebuilt launcher with only the release binaries and metadata.
func releaseFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	writeFixtureFile(t, filepath.Join(f.root, "release.json"), []byte(`{"version":"0.1.0","commit":"example"}`), 0o644)
	for _, name := range []string{"gorganizerd", "gorganizerctl", "gorganizer-gui"} {
		writeFixtureFile(t, filepath.Join(f.root, "bin", name), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	}
	return f
}

// TestReleaseLaunchUsesBundledBinariesWithoutBuild checks the session uses bin and skips checkout migration and builds.
func TestReleaseLaunchUsesBundledBinariesWithoutBuild(t *testing.T) {
	f := releaseFixture(t)
	log := filepath.Join(f.root, "session-args")
	writeFixtureFile(t, filepath.Join(f.root, "bin", "gorganizerctl"), []byte("#!/bin/bash\nprintf '<%s>\\n' \"$@\" > \"$SHIM_LOG\"\nprintf 'root=<%s>\\n' \"${GORGANIZER_ROOT-<unset>}\" >> \"$SHIM_LOG\"\n"), 0o755)
	output, err := f.run(t, `cmd_launch 'argument with spaces'`, "SHIM_LOG="+log)
	if err != nil || output != "" {
		t.Fatalf("release launch: %v: %q", err, output)
	}
	calls := string(readFixtureFile(t, log))
	want := strings.Join([]string{"<session>", "<--daemon>", "<" + filepath.Join(f.root, "bin/gorganizerd") + ">", "<--gui>", "<" + filepath.Join(f.root, "bin/gorganizer-gui") + ">", "<-->", "<argument with spaces>", "root=<<unset>>", ""}, "\n")
	if calls != want {
		t.Fatalf("release session args: %q, want %q", calls, want)
	}
	if _, err := os.Stat(f.log); !os.IsNotExist(err) {
		t.Fatalf("a build ran: %v", err)
	}
}

// TestReleaseSkipsEveryBuildPath checks missing tools cannot trigger a build or installation.
func TestReleaseSkipsEveryBuildPath(t *testing.T) {
	f := releaseFixture(t)
	for _, tc := range []struct{ command, want string }{
		{"if needs_build force; then printf build; else printf ready; fi", "ready"},
		{"ensure_register_ctl; printf ready", "ready"},
		{"gorganizer_version", "0.1.0\n"},
	} {
		out, err := f.run(t, tc.command)
		if err != nil || out != tc.want {
			t.Fatalf("%s: %v: %q", tc.command, err, out)
		}
	}
	if _, err := os.Stat(f.log); !os.IsNotExist(err) {
		t.Fatalf("a build ran: %v", err)
	}
}

// TestReleaseCleanerRefuses checks the developer cleanup tool cannot remove a prebuilt bundle.
func TestReleaseCleanerRefuses(t *testing.T) {
	f := releaseFixture(t)
	cmd := exec.Command("bash", filepath.Join(f.root, "cleaner.sh"))
	cmd.Dir = f.root
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "Cleaner is only available in a source checkout.") {
		t.Fatalf("release cleaner: %v: %q", err, out)
	}
}

// TestReleaseUpdateAndImportMessages checks dispatcher refuses checkout-only operations without tools.
func TestReleaseUpdateAndImportMessages(t *testing.T) {
	f := releaseFixture(t)
	for _, tc := range []struct {
		command, want string
		success       bool
	}{
		{"update", "This is a prebuilt copy of Gorganizer. Download the new release to update it.", true},
		{"build", "You do not need to build it.", false},
		{"setup", "You do not need to build it.", false},
		{"import", "not available in this prebuilt copy", false},
	} {
		t.Run(tc.command, func(t *testing.T) {
			cmd := exec.Command("bash", filepath.Join(f.root, "gorganizer.sh"), tc.command)
			cmd.Dir = f.root
			cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "XDG_DATA_HOME="+t.TempDir())
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.success || !strings.Contains(string(out), tc.want) {
				t.Fatalf("%s: %v: %q", tc.command, err, out)
			}
		})
	}
	if _, err := os.Stat(f.log); !os.IsNotExist(err) {
		t.Fatalf("a build ran: %v", err)
	}
}

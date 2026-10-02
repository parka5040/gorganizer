package packagingtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestReleaseE2ELaunchWrapper keeps every relaunch inside the scratch environment.
func TestReleaseE2ELaunchWrapper(t *testing.T) {
	scratch := filepath.Join(t.TempDir(), "scratch with 'quotes'")
	current := filepath.Join(scratch, "data", "gorganizer", "releases", "current")
	if err := os.MkdirAll(current, 0o755); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(current, "gorganizer.sh")
	if err := os.WriteFile(launcher, []byte("#!/bin/bash\nprintf '%s\\n' \"$HOME\" \"$XDG_CONFIG_HOME\" \"$XDG_DATA_HOME\" \"$XDG_STATE_HOME\" \"$XDG_CACHE_HOME\" \"$XDG_RUNTIME_DIR\" \"$TMPDIR\" \"${HOSTILE_TEST_VALUE-unset}\" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	create := exec.Command("bash", "-c", `GORGANIZER_SH_SOURCE_ONLY=1 source "$1"
scratch="$2"
fixture_env=("PATH=$PATH" "HOME=$scratch/home" "XDG_CONFIG_HOME=$scratch/config" "XDG_DATA_HOME=$scratch/data" "XDG_STATE_HOME=$scratch/state" "XDG_CACHE_HOME=$scratch/cache" "XDG_RUNTIME_DIR=$scratch/runtime" "TMPDIR=$scratch/tmp")
write_launch_wrapper`, "bash", filepath.Join("..", "release-e2e", "run-e2e.sh"), scratch)
	if output, err := create.CombinedOutput(); err != nil {
		t.Fatalf("writing launch wrapper: %v: %s", err, output)
	}
	wrapper := filepath.Join(scratch, "launch.sh")
	info, err := os.Stat(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("launch wrapper permissions: %o", info.Mode().Perm())
	}
	content, err := os.ReadFile(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "exec env -i") {
		t.Fatalf("launch wrapper does not clear the environment: %s", content)
	}
	launch := exec.Command(wrapper, "launch", "--tag", "v0.0.9")
	launch.Env = append(os.Environ(), "HOSTILE_TEST_VALUE=leaked")
	output, err := launch.CombinedOutput()
	if err != nil {
		t.Fatalf("running launch wrapper: %v: %s", err, output)
	}
	want := strings.Join([]string{
		filepath.Join(scratch, "home"), filepath.Join(scratch, "config"),
		filepath.Join(scratch, "data"), filepath.Join(scratch, "state"),
		filepath.Join(scratch, "cache"), filepath.Join(scratch, "runtime"),
		filepath.Join(scratch, "tmp"), "unset", "launch", "--tag", "v0.0.9", "",
	}, "\n")
	if string(output) != want {
		t.Fatalf("launch environment and arguments: got %q, want %q", output, want)
	}
}

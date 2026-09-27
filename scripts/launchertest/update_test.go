package launchertest

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fakeUpdateMake = fakeMake + `
cat > "$out/gorganizerctl" <<'CTL'
#!/bin/bash
case "${1:-}" in
    --version) printf 'gorganizerctl %s %s\n' "$FAKE_BUILD_VERSION" "$FAKE_BUILD_MARKER" ;;
    ping) printf 'ping\n' >> "$SHIM_LOG"; [ "${FAKE_RUNNING:-}" = yes ] ;;
    migrate-data)
        printf 'migrate-data %s\n' "${2:-}" >> "$SHIM_LOG"
        [ "${2:-}" = --status ] && printf '%s\n' "${FAKE_MIGRATION_STATUS:-none}" ;;
    stop|Shutdown|kill) printf '%s\n' "$1" >> "$SHIM_LOG"; exit 42 ;;
    *) exit 12 ;;
esac
CTL
chmod 755 "$out/gorganizerctl"
`

const fakeRunningDaemon = `#!/bin/bash
printf 'started\n' > "$FAKE_DAEMON_STARTED"
while [ ! -f "$FAKE_DAEMON_RELEASE" ]; do
    sleep 0.05
done
printf 'old process finished\n' > "$FAKE_DAEMON_FINISHED"
`

const fakeUpdateCtl = `#!/bin/bash
case "${1:-}" in
    --version) printf 'old gorganizerctl 0.1.0\n' ;;
    ping) printf 'ping\n' >> "$SHIM_LOG"; [ "${FAKE_RUNNING:-}" = yes ] ;;
    migrate-data)
        printf 'migrate-data %s\n' "${2:-}" >> "$SHIM_LOG"
        [ "${2:-}" = --status ] && printf '%s\n' "${FAKE_MIGRATION_STATUS:-none}" ;;
    stop|Shutdown|kill) printf '%s\n' "$1" >> "$SHIM_LOG"; exit 42 ;;
    *) exit 12 ;;
esac
`

type updateFixture struct {
	*fixture
	seed   string
	origin string
	calls  string
}

// gitFixtureCommand runs a local Git operation in the update fixture.
func gitFixtureCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, output)
	}
	return strings.TrimSpace(string(output))
}

// newUpdateFixture creates a bare origin and a clone with disposable installed binaries.
func newUpdateFixture(t *testing.T) *updateFixture {
	t.Helper()
	f := newFixture(t)
	seed := f.root
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitFixtureCommand(t, seed, "init", "-q", "-b", "main")
	writeFixtureFile(t, filepath.Join(seed, ".gitignore"), []byte(".build-fingerprint\n.build-staging/\nbuild/\ngorganizerd\ngorganizerctl\nmake.log\ncalls.log\n"), 0o644)
	gitFixtureCommand(t, seed, "add", ".gitignore", "gorganizer.sh", "cleaner.sh", "scripts/deploy-check.sh", "VERSION", "go.mod", "Makefile", "main.go", "CMakeLists.txt", "resources/icons/icon.png", "api/proto/fake.pb.go")
	gitFixtureCommand(t, seed, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "Initial version")
	gitFixtureCommand(t, seed, "init", "--bare", "-q", "-b", "main", origin)
	gitFixtureCommand(t, seed, "remote", "add", "origin", origin)
	gitFixtureCommand(t, seed, "push", "-q", "-u", "origin", "main")
	clone := filepath.Join(t.TempDir(), "checkout")
	gitFixtureCommand(t, seed, "clone", "-q", origin, clone)
	f.root = clone
	f.log = filepath.Join(clone, "make.log")
	f.old["gorganizerctl"] = []byte(fakeUpdateCtl)
	for name, content := range f.old {
		writeFixtureFile(t, filepath.Join(clone, name), content, 0o755)
	}
	writeFixtureFile(t, filepath.Join(clone, ".build-fingerprint"), []byte("prior-fingerprint\n"), 0o600)
	writeFixtureFile(t, filepath.Join(f.shims, "make"), []byte(fakeUpdateMake), 0o755)
	for _, name := range []string{"pgrep", "kill", "stop", "Shutdown"} {
		writeFixtureFile(t, filepath.Join(f.shims, name), []byte("#!/bin/sh\nprintf '%s\\n' '"+name+"' >> \"$SHIM_LOG\"\nexit 42\n"), 0o755)
	}
	return &updateFixture{fixture: f, seed: seed, origin: origin, calls: filepath.Join(clone, "calls.log")}
}

// commitUpdate publishes a source change on the fixture's current source branch.
func (f *updateFixture) commitUpdate(t *testing.T, subject string) string {
	t.Helper()
	writeFixtureFile(t, filepath.Join(f.seed, "main.go"), []byte("package main\n"+subject+"\n"), 0o644)
	gitFixtureCommand(t, f.seed, "add", "main.go")
	gitFixtureCommand(t, f.seed, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", subject)
	gitFixtureCommand(t, f.seed, "push", "-q", "origin", "HEAD")
	return gitFixtureCommand(t, f.seed, "rev-parse", "HEAD")
}

// runUpdate invokes the update function while recording desktop registration and CLI calls.
func (f *updateFixture) runUpdate(t *testing.T, flags string, settings ...string) (string, error) {
	t.Helper()
	settings = append(settings, "SHIM_LOG="+f.calls, "XDG_DATA_HOME="+filepath.Join(f.root, "data"), "XDG_CONFIG_HOME="+filepath.Join(f.root, "config"))
	return f.run(t, `cmd_register() { printf 'register\n' >> "$SHIM_LOG"; [ "${FAKE_REGISTER_FAIL:-}" != yes ]; }; kill() { printf 'kill\n' >> "$SHIM_LOG"; return 42; }; cmd_update `+flags, settings...)
}

// assertNoBuildOrRegistration checks a refused update did not touch installed artifacts.
func (f *updateFixture) assertNoBuildOrRegistration(t *testing.T) {
	t.Helper()
	f.assertInstalledOld(t)
	for _, path := range []string{f.log, f.calls} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("unexpected build or registration at %s: %v", path, err)
		}
	}
}

// TestUpdateAlreadyCurrent checks that matching binaries skip the build and desktop refresh.
func TestUpdateAlreadyCurrent(t *testing.T) {
	f := newUpdateFixture(t)
	fingerprint, err := f.run(t, "build_fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(f.root, ".build-fingerprint"), []byte(fingerprint), 0o600)
	old := gitFixtureCommand(t, f.root, "rev-parse", "--short", "HEAD")
	output, err := f.runUpdate(t, "")
	if err != nil || !strings.Contains(output, "Gorganizer is already up to date ("+old+").") {
		t.Fatalf("current update: %v: %q", err, output)
	}
	f.assertInstalledOld(t)
	if _, err := os.Stat(f.log); !os.IsNotExist(err) {
		t.Errorf("unexpected build: %v", err)
	}
	if got := string(readFixtureFile(t, f.calls)); got != "migrate-data --status\n" {
		t.Errorf("up-to-date update ran unexpected commands: %q", got)
	}
}

// TestUpdateRebuildsStaleBinaries checks that an unchanged branch still rebuilds stale installed binaries.
func TestUpdateRebuildsStaleBinaries(t *testing.T) {
	f := newUpdateFixture(t)
	output, err := f.runUpdate(t, "")
	if err != nil || !strings.Contains(output, "Update installed.") {
		t.Fatalf("stale build: %v: %q", err, output)
	}
	if !strings.Contains(string(readFixtureFile(t, f.log)), "OUT_DIR=") {
		t.Fatal("stale binaries were not rebuilt")
	}
	if got := string(readFixtureFile(t, f.calls)); got != "register\nping\nmigrate-data --status\n" {
		t.Fatalf("calls = %q", got)
	}
}

// TestUpdateFastForwardsTrackedBranch checks a non-main branch advances only from its upstream.
func TestUpdateFastForwardsTrackedBranch(t *testing.T) {
	f := newUpdateFixture(t)
	gitFixtureCommand(t, f.seed, "checkout", "-qb", "feature/update")
	gitFixtureCommand(t, f.seed, "push", "-q", "-u", "origin", "feature/update")
	gitFixtureCommand(t, f.root, "fetch", "-q", "origin")
	gitFixtureCommand(t, f.root, "checkout", "-qb", "feature/update", "--track", "origin/feature/update")
	mainSHA := gitFixtureCommand(t, f.root, "rev-parse", "main")
	newSHA := f.commitUpdate(t, "New feature for this branch")
	output, err := f.runUpdate(t, "")
	if err != nil || !strings.Contains(output, "New feature for this branch") || !strings.Contains(output, "Update installed.") {
		t.Fatalf("feature update: %v: %q", err, output)
	}
	if got := gitFixtureCommand(t, f.root, "rev-parse", "HEAD"); got != newSHA {
		t.Errorf("feature HEAD = %s, want %s", got, newSHA)
	}
	if got := gitFixtureCommand(t, f.root, "rev-parse", "main"); got != mainSHA {
		t.Errorf("main advanced from %s to %s", mainSHA, got)
	}
	if got := string(readFixtureFile(t, f.calls)); got != "register\nping\nmigrate-data --status\n" {
		t.Errorf("calls = %q", got)
	}
}

// TestUpdateFetchesTrackedRemote checks updates use the upstream remote rather than origin.
func TestUpdateFetchesTrackedRemote(t *testing.T) {
	f := newUpdateFixture(t)
	gitFixtureCommand(t, f.root, "remote", "add", "source", f.origin)
	gitFixtureCommand(t, f.root, "fetch", "-q", "source")
	gitFixtureCommand(t, f.root, "branch", "--set-upstream-to=source/main", "main")
	newSHA := f.commitUpdate(t, "Update from the tracked remote")
	output, err := f.runUpdate(t, "")
	if err != nil || !strings.Contains(output, "Update from the tracked remote") {
		t.Fatalf("tracked remote update: %v: %q", err, output)
	}
	if got := gitFixtureCommand(t, f.root, "rev-parse", "HEAD"); got != newSHA {
		t.Errorf("HEAD = %s, want %s", got, newSHA)
	}
	if got := gitFixtureCommand(t, f.root, "rev-parse", "origin/main"); got == newSHA {
		t.Error("the unused origin tracking ref advanced")
	}
}

// TestUpdateRefusesDivergedAndAheadBranches checks that local commits are never merged or downgraded.
func TestUpdateRefusesDivergedAndAheadBranches(t *testing.T) {
	for _, tc := range []struct {
		name     string
		diverged bool
	}{
		{name: "ahead"},
		{name: "diverged", diverged: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpdateFixture(t)
			if tc.diverged {
				f.commitUpdate(t, "Remote changes")
			}
			writeFixtureFile(t, filepath.Join(f.root, "main.go"), []byte("package main\nlocal work\n"), 0o644)
			gitFixtureCommand(t, f.root, "add", "main.go")
			gitFixtureCommand(t, f.root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "Local changes")
			old := gitFixtureCommand(t, f.root, "rev-parse", "HEAD")
			output, err := f.runUpdate(t, "")
			if err == nil || !strings.Contains(output, "Your copy has changes that are not in the update source. Nothing was changed.") {
				t.Fatalf("%s update: %v: %q", tc.name, err, output)
			}
			if got := gitFixtureCommand(t, f.root, "rev-parse", "HEAD"); got != old {
				t.Errorf("HEAD changed from %s to %s", old, got)
			}
			f.assertNoBuildOrRegistration(t)
		})
	}
}

// TestUpdateRefusesDetachedAndUntrackedBranches checks missing branch information stops the update.
func TestUpdateRefusesDetachedAndUntrackedBranches(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		prepare    func(*testing.T, *updateFixture)
	}{
		{"detached", "This copy of Gorganizer is not on a branch, so it cannot be updated automatically.", func(t *testing.T, f *updateFixture) {
			gitFixtureCommand(t, f.root, "checkout", "-q", "--detach")
		}},
		{"no upstream", "This branch has no update source. Ask whoever set it up, or re-download Gorganizer.", func(t *testing.T, f *updateFixture) {
			gitFixtureCommand(t, f.root, "checkout", "-qb", "local-only")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUpdateFixture(t)
			tc.prepare(t, f)
			old := gitFixtureCommand(t, f.root, "rev-parse", "HEAD")
			output, err := f.runUpdate(t, "")
			if err == nil || !strings.Contains(output, tc.want) {
				t.Fatalf("%s update: %v: %q", tc.name, err, output)
			}
			if got := gitFixtureCommand(t, f.root, "rev-parse", "HEAD"); got != old {
				t.Errorf("HEAD changed from %s to %s", old, got)
			}
			f.assertNoBuildOrRegistration(t)
		})
	}
}

// TestUpdateRefusesDirtyTree checks that tracked edits retain the existing refusal message.
func TestUpdateRefusesDirtyTree(t *testing.T) {
	f := newUpdateFixture(t)
	writeFixtureFile(t, filepath.Join(f.root, "main.go"), []byte("package main\nchanged\n"), 0o644)
	old := gitFixtureCommand(t, f.root, "rev-parse", "HEAD")
	output, err := f.runUpdate(t, "")
	if err == nil || !strings.Contains(output, "Working tree has uncommitted changes:") || !strings.Contains(output, "Stash or commit them, then re-run `./gorganizer.sh update`.") {
		t.Fatalf("dirty tree: %v: %q", err, output)
	}
	if got := gitFixtureCommand(t, f.root, "rev-parse", "HEAD"); got != old {
		t.Errorf("HEAD changed from %s to %s", old, got)
	}
	f.assertNoBuildOrRegistration(t)
}

// TestUpdateFailedBuildRestoresCheckout checks that staged build failure rolls back Git without replacing binaries.
func TestUpdateFailedBuildRestoresCheckout(t *testing.T) {
	f := newUpdateFixture(t)
	old := gitFixtureCommand(t, f.root, "rev-parse", "HEAD")
	oldShort := gitFixtureCommand(t, f.root, "rev-parse", "--short", "HEAD")
	f.commitUpdate(t, "Version that cannot build")
	output, err := f.runUpdate(t, "", "FAKE_MAKE_FAIL=yes")
	want := "The update was downloaded but could not be built, so Gorganizer stayed on the previous version (" + oldShort + "). Your mods and settings were not touched."
	if err == nil || !strings.Contains(output, want) {
		t.Fatalf("build failure: %v: %q", err, output)
	}
	if got := gitFixtureCommand(t, f.root, "rev-parse", "HEAD"); got != old {
		t.Errorf("HEAD changed from %s to %s", old, got)
	}
	if got := gitFixtureCommand(t, f.root, "status", "--porcelain"); got != "" {
		t.Errorf("checkout dirty after rollback: %q", got)
	}
	f.assertInstalledOld(t)
	if got := string(readFixtureFile(t, filepath.Join(f.root, ".build-fingerprint"))); got != "prior-fingerprint\n" {
		t.Errorf("fingerprint changed: %q", got)
	}
	if _, err := os.Stat(f.calls); !os.IsNotExist(err) {
		t.Errorf("registration or CLI was called after failure: %v", err)
	}
}

// TestUpdateRestartLeavesRunningSessionAlone checks ping-only detection and inode-safe publication.
func TestUpdateRestartLeavesRunningSessionAlone(t *testing.T) {
	f := newUpdateFixture(t)
	f.commitUpdate(t, "Running-session update")
	oldDaemon := filepath.Join(f.root, "gorganizerd")
	f.old["gorganizerd"] = []byte(fakeRunningDaemon)
	writeFixtureFile(t, oldDaemon, f.old["gorganizerd"], 0o755)
	markers := t.TempDir()
	started := filepath.Join(markers, "daemon-started")
	release := filepath.Join(markers, "daemon-release")
	finished := filepath.Join(markers, "daemon-finished")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	process := exec.CommandContext(ctx, oldDaemon)
	process.Env = append(os.Environ(), "FAKE_DAEMON_STARTED="+started, "FAKE_DAEMON_RELEASE="+release, "FAKE_DAEMON_FINISHED="+finished)
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		writeFixtureFile(t, release, nil, 0o600)
		if !waited {
			_ = process.Wait()
		}
	}()
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake running daemon did not start")
		}
	}
	openDaemon, err := os.Open(oldDaemon)
	if err != nil {
		t.Fatal(err)
	}
	defer openDaemon.Close()
	oldInfo, err := openDaemon.Stat()
	if err != nil {
		t.Fatal(err)
	}
	output, err := f.runUpdate(t, "--restart", "FAKE_RUNNING=yes")
	if err != nil || !strings.Contains(output, "Update installed. It will be used next time you open Gorganizer.") || !strings.Contains(output, "Close Gorganizer and open it again to use the new version now.") {
		t.Fatalf("running update: %v: %q", err, output)
	}
	if got := string(readFixtureFile(t, f.calls)); got != "register\nping\nmigrate-data --status\n" {
		t.Fatalf("update called something other than register, ping, and status: %q", got)
	}
	newInfo, err := os.Stat(oldDaemon)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(oldInfo, newInfo) {
		t.Fatal("published daemon reused the old inode")
	}
	oldContents := make([]byte, oldInfo.Size())
	if _, err := openDaemon.ReadAt(oldContents, 0); err != nil || !bytes.Equal(oldContents, f.old["gorganizerd"]) {
		t.Fatalf("running daemon's open inode changed: %v: %q", err, oldContents)
	}
	writeFixtureFile(t, release, nil, 0o600)
	err = process.Wait()
	waited = true
	if err != nil || string(readFixtureFile(t, finished)) != "old process finished\n" {
		t.Fatalf("old daemon did not finish after the update: %v", err)
	}
}

// TestUpdateRestartWithoutSessionChecksMigrationStatus checks the update never performs migration itself.
func TestUpdateRestartWithoutSessionChecksMigrationStatus(t *testing.T) {
	for _, tc := range []struct {
		status string
		remind bool
	}{
		{status: "none"},
		{status: "pending", remind: true},
	} {
		t.Run(tc.status, func(t *testing.T) {
			f := newUpdateFixture(t)
			f.commitUpdate(t, "Migration reminder")
			output, err := f.runUpdate(t, "--restart", "FAKE_MIGRATION_STATUS="+tc.status)
			if err != nil || !strings.Contains(output, "Update installed.") || strings.Contains(output, "Close Gorganizer and open it again") || strings.Contains(output, "Open Gorganizer to finish moving your mods.") != tc.remind {
				t.Fatalf("migration %s: %v: %q", tc.status, err, output)
			}
			if got := string(readFixtureFile(t, f.calls)); got != "register\nping\nmigrate-data --status\n" {
				t.Fatalf("migration called unexpected command: %q", got)
			}
		})
	}
}

// TestUpdateRegistrationFailureOnlyWarns checks a desktop registration error does not undo installed binaries.
func TestUpdateRegistrationFailureOnlyWarns(t *testing.T) {
	f := newUpdateFixture(t)
	newSHA := f.commitUpdate(t, "Desktop update")
	output, err := f.runUpdate(t, "", "FAKE_REGISTER_FAIL=yes")
	if err != nil || !strings.Contains(output, "Could not refresh the application menu entry. Gorganizer was updated.") || !strings.Contains(output, "Update installed.") {
		t.Fatalf("registration failure: %v: %q", err, output)
	}
	if got := gitFixtureCommand(t, f.root, "rev-parse", "HEAD"); got != newSHA {
		t.Errorf("HEAD = %s, want %s", got, newSHA)
	}
}

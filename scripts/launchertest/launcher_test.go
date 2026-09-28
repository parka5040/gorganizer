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

	"github.com/parka/gorganizer/internal/testsafe"
)

const fakeMake = `#!/bin/bash
set -euo pipefail
printf 'make %s\n' "$*" >> "$FAKE_MAKE_LOG"
out=""
gui=""
for arg in "$@"; do
    case "$arg" in
        OUT_DIR=*) out="${arg#OUT_DIR=}" ;;
        GUI_BUILD_DIR=*) gui="${arg#GUI_BUILD_DIR=}" ;;
    esac
done
[ -n "$out" ] && [ -n "$gui" ] || exit 21
go version >/dev/null
cmake --version >/dev/null
[ "${FAKE_MAKE_FAIL:-}" != yes ] || exit 22
mkdir -p "$out" "$gui/src"
for name in gorganizerd gorganizerctl gorganizer; do
    version="$FAKE_BUILD_VERSION"
    [ "${FAKE_WRONG_BINARY:-}" != "$name" ] || version=wrong-version
    case "$name" in
        gorganizer) dest="$gui/src/$name" ;;
        *) dest="$out/$name" ;;
    esac
    printf '#!/bin/sh\n[ "${1:-}" = "--version" ] || exit 12\nprintf "%%s\\n" "%s %s %s"\n' "$name" "$version" "$FAKE_BUILD_MARKER" > "$dest"
    chmod 755 "$dest"
done
`

const interruptingMove = `#!/bin/bash
/bin/mv "$@" || exit $?
if [ "${@: -1}" = "$FAKE_INTERRUPT_TARGET" ]; then
    kill -KILL "$PPID"
fi
`

const fakeMigrationCtl = `#!/bin/bash
set -euo pipefail
printf '%s\n' "$*" >> "$SHIM_LOG"
case "$1" in
    migrate-data)
        case "$2" in
            --status)
                if [ "${FAKE_STATUS_FAIL:-}" = yes ]; then
                    printf 'Cannot check the move.\n' >&2
                    exit 1
                fi
                printf '%s\n' "${FAKE_STATUS:-none}"
                ;;
            --resume)
                if [ "${FAKE_RESUME_FAIL:-}" = yes ]; then
                    printf 'Could not finish moving your mods.\n' >&2
                    exit 1
                fi
                ;;
            --from)
                if [ "${FAKE_VERIFY_IMPORT:-}" = yes ]; then
                    printf 'argc=%s\n' "$#" >> "$SHIM_LOG"
                fi
                case "$*" in
                    *--dry-run*--count*) if [ "${FAKE_SOURCES:-[]}" = "[]" ]; then echo 0; else echo 1; fi ;;
                    *--dry-run*) printf '{"sources":%s}\n' "${FAKE_SOURCES:-[]}" ;;
                    *--yes*)
                        case "${FAKE_MOVE_FAIL:-}" in
                            yes)
                                printf '  Cannot move yet: Unmount the mods first.\n'
                                exit 2
                                ;;
                            error)
                                printf 'Could not move your mods: The folder changed.\n' >&2
                                exit 1
                                ;;
                        esac
                        ;;
                    *) printf 'Move your mods? [y/N] ' ;;
                esac
                ;;
        esac
        ;;
    session)
        printf 'root=%s\nqt=%s\n' "${GORGANIZER_ROOT-<unset>}" "$QT_LOGGING_RULES" >> "$SHIM_LOG"
        ;;
    ping)
        [ "${FAKE_DAEMON_RUNNING:-}" = yes ]
        ;;
    nxm)
        printf 'root=%s\n' "${GORGANIZER_ROOT-<unset>}" >> "$SHIM_LOG"
        ;;
esac
`

type fixture struct {
	root  string
	shims string
	log   string
	old   map[string][]byte
}

// TestMain runs launcher tests with isolated state and failing desktop launcher shims.
func TestMain(m *testing.M) {
	os.Exit(testsafe.RunWithSafeEnvironment(m))
}

// writeFixtureFile writes a file inside an isolated launcher fixture.
func writeFixtureFile(t *testing.T, path string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
}

// newFixture copies the launcher and creates an isolated repository with fake build tools.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	shims := filepath.Join(t.TempDir(), "shims")
	if err := os.Mkdir(shims, 0o700); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Clean(filepath.Join(cwd, "../.."))
	for _, name := range []string{"gorganizer.sh", "cleaner.sh", "scripts/deploy-check.sh"} {
		data, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatal(err)
		}
		writeFixtureFile(t, filepath.Join(root, name), data, 0o755)
	}
	for name, data := range map[string]string{
		"VERSION":                  "0.1.0\n",
		"go.mod":                   "module fake\n",
		"Makefile":                 "all:\n\t@true\n",
		"main.go":                  "package main\n",
		"CMakeLists.txt":           "project(fake)\n",
		"resources/icons/icon.png": "icon",
		"api/proto/fake.pb.go":     "generated",
	} {
		writeFixtureFile(t, filepath.Join(root, name), []byte(data), 0o644)
	}
	writeFixtureFile(t, filepath.Join(shims, "make"), []byte(fakeMake), 0o755)
	for _, name := range []string{"go", "cmake"} {
		writeFixtureFile(t, filepath.Join(shims, name), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	}
	old := make(map[string][]byte)
	for _, name := range []string{"gorganizerd", "gorganizerctl", "build/src/gorganizer"} {
		content := []byte("#!/bin/sh\nprintf 'old " + name + " 0.1.0\\n'\n")
		writeFixtureFile(t, filepath.Join(root, name), content, 0o755)
		old[name] = content
	}
	writeFixtureFile(t, filepath.Join(root, ".build-fingerprint"), []byte("prior-fingerprint\n"), 0o600)
	return &fixture{root: root, shims: shims, log: filepath.Join(root, "make.log"), old: old}
}

// run invokes sourced launcher functions without starting the dispatcher.
func (f *fixture) run(t *testing.T, command string, settings ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", ". \"$1\"; "+command, "bash", filepath.Join(f.root, "gorganizer.sh"))
	cmd.Dir = f.root
	for _, setting := range os.Environ() {
		if !strings.HasPrefix(setting, "GORGANIZER_ROOT=") {
			cmd.Env = append(cmd.Env, setting)
		}
	}
	cmd.Env = append(cmd.Env,
		"GORGANIZER_SH_SOURCE_ONLY=1",
		"PATH="+f.shims+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_MAKE_LOG="+f.log,
		"FAKE_BUILD_VERSION=0.1.0",
		"FAKE_BUILD_MARKER=new-complete-artifact-with-extra-bytes",
	)
	cmd.Env = append(cmd.Env, settings...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("launcher timed out: %s", output)
	}
	return string(output), err
}

// readFixtureFile reads a fixture artifact or fails the test.
func readFixtureFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// assertInstalledOld checks that all published paths retain their original bytes.
func (f *fixture) assertInstalledOld(t *testing.T) {
	t.Helper()
	for name, want := range f.old {
		if got := readFixtureFile(t, filepath.Join(f.root, name)); !bytes.Equal(got, want) {
			t.Errorf("%s changed after failure: %q", name, got)
		}
	}
}

// installMigrationShim records migration and session calls in an isolated fixture.
func (f *fixture) installMigrationShim(t *testing.T) string {
	t.Helper()
	writeFixtureFile(t, filepath.Join(f.root, "gorganizerctl"), []byte(fakeMigrationCtl), 0o755)
	writeFixtureFile(t, filepath.Join(f.shims, "notify-send"), []byte("#!/bin/sh\nprintf '%s\\n' \"$2\" >> \"$SHIM_NOTIFICATIONS\"\n"), 0o755)
	return filepath.Join(f.root, "supervisor-args")
}

// TestLaunchExecsSupervisor verifies the launcher forwards paths and GUI arguments to the session supervisor.
func TestLaunchExecsSupervisor(t *testing.T) {
	f := newFixture(t)
	logPath := f.installMigrationShim(t)
	output, err := f.run(t, `cmd_launch 'nxm://example/mod?id=1' 'with spaces'`, "SHIM_LOG="+logPath, "QT_LOGGING_RULES=")
	if err != nil || output != "" {
		t.Fatalf("launch = %v: %s", err, output)
	}
	want := strings.Join([]string{"migrate-data --status", "migrate-data --from " + f.root + " --dry-run --count", "session --daemon " + filepath.Join(f.root, "gorganizerd") + " --gui " + filepath.Join(f.root, "build/src/gorganizer") + " -- nxm://example/mod?id=1 with spaces", "root=<unset>", "qt=qt.dbus.*=false;qt.qpa.systemtray.*=false;qt.qpa.theme.dbus.*=false;qt.qpa.theme.debug=false", ""}, "\n")
	if got := string(readFixtureFile(t, logPath)); got != want {
		t.Fatalf("supervisor arguments = %q, want %q", got, want)
	}
}

// TestColdNXMMigratesBeforeForwarding checks that a cold link moves legacy mods before it starts a session.
func TestColdNXMMigratesBeforeForwarding(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "moved", true: "blocked"}[blocked], func(t *testing.T) {
			f := newFixture(t)
			logPath := f.installMigrationShim(t)
			writeFixtureFile(t, filepath.Join(f.root, "SkyrimSE_Mods", "mod.esp"), []byte("mod"), 0600)
			settings := []string{"SHIM_LOG=" + logPath, "SHIM_NOTIFICATIONS=" + filepath.Join(f.root, "notifications"), "FAKE_SOURCES=[\"" + filepath.Join(f.root, "SkyrimSE_Mods") + "\"]"}
			if blocked {
				settings = append(settings, "FAKE_MOVE_FAIL=yes")
			}
			output, err := f.run(t, `cmd_nxm 'nxm://example/mod?id=1'`, settings...)
			if err != nil {
				t.Fatalf("nxm = %v: %s", err, output)
			}
			calls := string(readFixtureFile(t, logPath))
			if !strings.Contains(calls, "migrate-data --status\n") || !strings.Contains(calls, "migrate-data --from "+f.root+" --yes\n") || !strings.Contains(calls, "nxm nxm://example/mod?id=1\n") || strings.Index(calls, " --yes") > strings.Index(calls, "nxm nxm://") {
				t.Fatalf("nxm migration order = %q", calls)
			}
			root := "root=<unset>\n"
			if blocked {
				root = "root=" + f.root + "\n"
			}
			if !strings.Contains(calls, root) {
				t.Fatalf("nxm migration root = %q", calls)
			}
		})
	}
}

// TestRunningNXMForwardsWithoutMigration checks a live session receives the link without migration.
func TestRunningNXMForwardsWithoutMigration(t *testing.T) {
	f := newFixture(t)
	logPath := f.installMigrationShim(t)
	output, err := f.run(t, `cmd_nxm 'nxm://example/mod?id=1'`, "SHIM_LOG="+logPath, "FAKE_DAEMON_RUNNING=yes")
	if err != nil || output != "" {
		t.Fatalf("nxm = %v: %q", err, output)
	}
	if calls := string(readFixtureFile(t, logPath)); calls != "ping\nnxm nxm://example/mod?id=1\nroot=<unset>\n" {
		t.Fatalf("running daemon calls = %q", calls)
	}
}

// TestLaunchWithoutOldFoldersUsesXDG checks a clean checkout never sets the root override.
func TestLaunchWithoutOldFoldersUsesXDG(t *testing.T) {
	f := newFixture(t)
	logPath := f.installMigrationShim(t)
	output, err := f.run(t, "cmd_launch", "SHIM_LOG="+logPath)
	if err != nil || output != "" {
		t.Fatalf("launch = %v: %s", err, output)
	}
	calls := string(readFixtureFile(t, logPath))
	if !strings.Contains(calls, "migrate-data --status\n") || !strings.Contains(calls, "migrate-data --from "+f.root+" --dry-run --count\n") || !strings.Contains(calls, "root=<unset>\n") || strings.Contains(calls, " --yes") {
		t.Fatalf("clean launch calls = %q", calls)
	}
}

// TestLaunchMigratesOldFoldersFirst checks that migration precedes the session without setting a root override.
func TestLaunchMigratesOldFoldersFirst(t *testing.T) {
	f := newFixture(t)
	logPath := f.installMigrationShim(t)
	writeFixtureFile(t, filepath.Join(f.root, "SkyrimSE_Mods", "mod.esp"), []byte("mod"), 0o600)
	settings := []string{"SHIM_LOG=" + logPath, "SHIM_NOTIFICATIONS=" + filepath.Join(f.root, "notifications"), "FAKE_SOURCES=[\"" + filepath.Join(f.root, "SkyrimSE_Mods") + "\"]"}
	output, err := f.run(t, "cmd_launch", settings...)
	if err != nil {
		t.Fatalf("launch = %v: %s", err, output)
	}
	calls := string(readFixtureFile(t, logPath))
	if !(strings.Index(calls, " --dry-run --count") < strings.Index(calls, " --yes") && strings.Index(calls, " --yes") < strings.Index(calls, "session --daemon")) || !strings.Contains(calls, "root=<unset>\n") {
		t.Fatalf("migration order or root = %q", calls)
	}
	notifications := string(readFixtureFile(t, filepath.Join(f.root, "notifications")))
	if !strings.Contains(notifications, "Moving your mods to your personal data folder. This happens once.") || !strings.Contains(notifications, "Your mods are now in ~/.local/share/gorganizer.") {
		t.Fatalf("migration notifications = %q", notifications)
	}
}

// TestLaunchBlockedMigrationFallsBackForThisSession checks a refused move keeps the old folders available.
func TestLaunchBlockedMigrationFallsBackForThisSession(t *testing.T) {
	f := newFixture(t)
	logPath := f.installMigrationShim(t)
	old := filepath.Join(f.root, "SkyrimSE_Mods", "mod.esp")
	writeFixtureFile(t, old, []byte("mod"), 0o600)
	settings := []string{"SHIM_LOG=" + logPath, "SHIM_NOTIFICATIONS=" + filepath.Join(f.root, "notifications"), "FAKE_SOURCES=[\"" + filepath.Join(f.root, "SkyrimSE_Mods") + "\"]", "FAKE_MOVE_FAIL=yes"}
	output, err := f.run(t, "cmd_launch", settings...)
	if err != nil {
		t.Fatalf("launch = %v: %s", err, output)
	}
	if calls := string(readFixtureFile(t, logPath)); !strings.Contains(calls, "migrate-data --from "+f.root+" --yes\nsession ") || !strings.Contains(calls, "root="+f.root+"\n") {
		t.Fatalf("fallback calls = %q", calls)
	}
	if !strings.Contains(output, "Unmount the mods first. It will try again next time.") || !strings.Contains(string(readFixtureFile(t, filepath.Join(f.root, "notifications"))), "Unmount the mods first. It will try again next time.") {
		t.Fatalf("blocker not reported: %q", output)
	}
	if got := string(readFixtureFile(t, old)); got != "mod" {
		t.Fatalf("old mod changed: %q", got)
	}
}

// TestLaunchFailedMigrationFallsBackForThisSession checks an unexpected move failure retains the old mod root.
func TestLaunchFailedMigrationFallsBackForThisSession(t *testing.T) {
	f := newFixture(t)
	logPath := f.installMigrationShim(t)
	writeFixtureFile(t, filepath.Join(f.root, "SkyrimSE_Mods", "mod.esp"), []byte("mod"), 0o600)
	settings := []string{"SHIM_LOG=" + logPath, "SHIM_NOTIFICATIONS=" + filepath.Join(f.root, "notifications"), "FAKE_SOURCES=[\"" + filepath.Join(f.root, "SkyrimSE_Mods") + "\"]", "FAKE_MOVE_FAIL=error"}
	output, err := f.run(t, "cmd_launch", settings...)
	if err != nil || !strings.Contains(output, "The folder changed. It will try again next time.") {
		t.Fatalf("failed migration = %v: %q", err, output)
	}
	if calls := string(readFixtureFile(t, logPath)); !strings.Contains(calls, "root="+f.root+"\n") {
		t.Fatalf("fallback calls = %q", calls)
	}
}

// TestLaunchResumesPendingMigration checks the journal resumes before any new move or session.
func TestLaunchResumesPendingMigration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fail    bool
		started bool
	}{
		{"resumed", false, true},
		{"resume failed", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			logPath := f.installMigrationShim(t)
			settings := []string{"SHIM_LOG=" + logPath, "SHIM_NOTIFICATIONS=" + filepath.Join(f.root, "notifications"), "FAKE_STATUS=pending"}
			if tc.fail {
				settings = append(settings, "FAKE_RESUME_FAIL=yes")
			}
			output, err := f.run(t, "cmd_launch", settings...)
			calls := string(readFixtureFile(t, logPath))
			if (err == nil) != tc.started || !strings.HasPrefix(calls, "migrate-data --status\nmigrate-data --resume\n") || strings.Contains(calls, "session ") != tc.started {
				t.Fatalf("resume = %v, calls = %q, output = %q", err, calls, output)
			}
			if tc.fail && (!strings.Contains(output, "Could not finish moving your mods.") || !strings.Contains(string(readFixtureFile(t, filepath.Join(f.root, "notifications"))), "Could not finish moving your mods.")) {
				t.Fatalf("resume failure was not shown: %q", output)
			}
		})
	}
}

// TestLaunchStatusFailureStopsBeforeSession checks a failed journal check prevents startup.
func TestLaunchStatusFailureStopsBeforeSession(t *testing.T) {
	f := newFixture(t)
	logPath := f.installMigrationShim(t)
	output, err := f.run(t, "cmd_launch", "SHIM_LOG="+logPath, "SHIM_NOTIFICATIONS="+filepath.Join(f.root, "notifications"), "FAKE_STATUS_FAIL=yes")
	if err == nil || !strings.Contains(output, "Cannot check the move.") || !strings.Contains(output, "Could not check whether your mods need moving.") {
		t.Fatalf("failed status = %v: %q", err, output)
	}
	if calls := string(readFixtureFile(t, logPath)); calls != "migrate-data --status\n" {
		t.Fatalf("started after failed status: %q", calls)
	}
}

// TestExplicitRootOverrideIsKept checks developer overrides bypass migration and reach the session unchanged.
func TestExplicitRootOverrideIsKept(t *testing.T) {
	for _, root := range []string{"", "custom root"} {
		t.Run("root="+root, func(t *testing.T) {
			f := newFixture(t)
			logPath := f.installMigrationShim(t)
			output, err := f.run(t, "cmd_launch", "SHIM_LOG="+logPath, "GORGANIZER_ROOT="+root)
			if err != nil || output != "" {
				t.Fatalf("launch = %v: %s", err, output)
			}
			if calls := string(readFixtureFile(t, logPath)); strings.Contains(calls, "migrate-data") || !strings.Contains(calls, "root="+root+"\n") {
				t.Fatalf("override calls = %q", calls)
			}
		})
	}
}

// TestImportDelegatesToMigrateData checks import forwards the supplied path without setting a root override.
func TestImportDelegatesToMigrateData(t *testing.T) {
	f := newFixture(t)
	logPath := f.installMigrationShim(t)
	from := filepath.Join(f.root, "old checkout")
	output, err := f.run(t, `cmd_import --from "$FAKE_FROM"`, "SHIM_LOG="+logPath, "FAKE_FROM="+from, "FAKE_VERIFY_IMPORT=yes")
	if err != nil || !strings.Contains(output, "Move your mods? [y/N]") {
		t.Fatalf("import = %v: %q", err, output)
	}
	if calls := string(readFixtureFile(t, logPath)); calls != "migrate-data --from "+from+"\nargc=3\n" {
		t.Fatalf("import calls = %q", calls)
	}
}

// TestFailedBuildKeepsInstalledBinaries checks that a failed build cannot overwrite installed binaries or their fingerprint.
func TestFailedBuildKeepsInstalledBinaries(t *testing.T) {
	f := newFixture(t)
	output, err := f.run(t, "do_build", "FAKE_MAKE_FAIL=yes")
	if err == nil || !strings.Contains(output, "The new build failed. Your installed version is unchanged.") || !strings.Contains(output, "./gorganizer.sh setup") {
		t.Fatalf("expected build failure and setup hint, got %v: %s", err, output)
	}
	f.assertInstalledOld(t)
	if got := string(readFixtureFile(t, filepath.Join(f.root, ".build-fingerprint"))); got != "prior-fingerprint\n" {
		t.Fatalf("fingerprint changed on failed build: %q", got)
	}
}

// TestInterruptedPublicationKeepsOldOrNewPerFile checks that interruption between renames cannot publish a truncated binary.
func TestInterruptedPublicationKeepsOldOrNewPerFile(t *testing.T) {
	for _, target := range []string{"gorganizerd", "gorganizerctl"} {
		t.Run(target, func(t *testing.T) {
			f := newFixture(t)
			writeFixtureFile(t, filepath.Join(f.shims, "mv"), []byte(interruptingMove), 0o755)
			output, err := f.run(t, "do_build", "FAKE_INTERRUPT_TARGET="+filepath.Join(f.root, target))
			if err == nil {
				t.Fatalf("expected interruption after publishing %s: %s", target, output)
			}
			for name, old := range f.old {
				got := readFixtureFile(t, filepath.Join(f.root, name))
				if bytes.Equal(got, old) {
					continue
				}
				staged := filepath.Join(f.root, ".build-staging", "bin", name)
				if name == "build/src/gorganizer" {
					staged = filepath.Join(f.root, ".build-staging", "gui", "src", "gorganizer")
				}
				if !bytes.Equal(got, readFixtureFile(t, staged)) {
					t.Errorf("%s is neither complete old nor complete new binary: %q", name, got)
				}
			}
			if bytes.Equal(readFixtureFile(t, filepath.Join(f.root, target)), f.old[target]) {
				t.Fatalf("interruption happened before %s was replaced", target)
			}
			if got := string(readFixtureFile(t, filepath.Join(f.root, ".build-fingerprint"))); got != "prior-fingerprint\n" {
				t.Fatalf("fingerprint committed before all binaries: %q", got)
			}
		})
	}
}

// TestRebuildCleansOnlyStaging checks that a forced build leaves installed artifacts and the previous GUI build intact.
func TestRebuildCleansOnlyStaging(t *testing.T) {
	f := newFixture(t)
	writeFixtureFile(t, filepath.Join(f.root, ".build-staging", "stale"), []byte("old cache"), 0o644)
	writeFixtureFile(t, filepath.Join(f.root, "build", "keep"), []byte("old GUI build"), 0o644)
	output, err := f.run(t, "do_build force", "FAKE_MAKE_FAIL=yes")
	if err == nil || !strings.Contains(output, "The new build failed. Your installed version is unchanged.") {
		t.Fatalf("expected failed forced build, got %v: %s", err, output)
	}
	if _, err := os.Stat(filepath.Join(f.root, ".build-staging", "stale")); !os.IsNotExist(err) {
		t.Fatalf("stale staging cache survived rebuild: %v", err)
	}
	f.assertInstalledOld(t)
	if got := string(readFixtureFile(t, filepath.Join(f.root, "build", "keep"))); got != "old GUI build" {
		t.Fatalf("installed GUI build was changed: %q", got)
	}
	if calls := string(readFixtureFile(t, f.log)); strings.Contains(calls, "clean") || !strings.Contains(calls, "OUT_DIR=") || !strings.Contains(calls, "GUI_BUILD_DIR=") {
		t.Fatalf("rebuild did not use staged outputs: %q", calls)
	}
}

// TestFingerprintDetectsGoModAndVersionChanges checks that input contents rather than timestamps determine rebuilds.
func TestFingerprintDetectsGoModAndVersionChanges(t *testing.T) {
	f := newFixture(t)
	output, err := f.run(t, "do_build")
	if err != nil {
		t.Fatalf("initial build: %v: %s", err, output)
	}
	if got, err := f.run(t, "if needs_build; then printf 'needs'; else printf 'current'; fi"); err != nil || got != "current" {
		t.Fatalf("unchanged inputs: %v: %q", err, got)
	}
	writeFixtureFile(t, filepath.Join(f.root, ".build-staging", "ignored.go"), []byte("changed"), 0o644)
	writeFixtureFile(t, filepath.Join(f.root, ".gocache", "ignored.go"), []byte("changed"), 0o644)
	writeFixtureFile(t, filepath.Join(f.root, ".tmp", "ignored.go"), []byte("changed"), 0o644)
	writeFixtureFile(t, filepath.Join(f.root, "api/proto/fake.pb.go"), []byte("changed"), 0o644)
	if got, err := f.run(t, "if needs_build; then printf 'needs'; else printf 'current'; fi"); err != nil || got != "current" {
		t.Fatalf("generated and staged files: %v: %q", err, got)
	}
	for _, change := range []struct{ name, content string }{
		{"go.mod", "module changed\n"},
		{"VERSION", "0.2.0\n"},
		{"Makefile", "changed make rules\n"},
		{"resources/icons/icon.png", "new icon"},
	} {
		t.Run(change.name, func(t *testing.T) {
			path := filepath.Join(f.root, change.name)
			original := readFixtureFile(t, path)
			writeFixtureFile(t, path, []byte(change.content), 0o644)
			if got, err := f.run(t, "if needs_build; then printf 'needs'; else printf 'current'; fi"); err != nil || got != "needs" {
				t.Errorf("%s content change: %v: %q", change.name, err, got)
			}
			writeFixtureFile(t, path, original, 0o644)
		})
	}
	if got, err := f.run(t, "if needs_build; then printf 'needs'; else printf 'current'; fi"); err != nil || got != "current" {
		t.Fatalf("restored inputs: %v: %q", err, got)
	}
	for _, missing := range []string{".build-fingerprint", "gorganizerctl"} {
		t.Run("missing "+missing, func(t *testing.T) {
			path := filepath.Join(f.root, missing)
			contents := readFixtureFile(t, path)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if got, err := f.run(t, "if needs_build; then printf 'needs'; else printf 'current'; fi"); err != nil || got != "needs" {
				t.Errorf("missing %s: %v: %q", missing, err, got)
			}
			writeFixtureFile(t, path, contents, 0o755)
		})
	}
}

// TestVersionMismatchIsNotPublished checks that every staged binary must report the VERSION value before publication.
func TestVersionMismatchIsNotPublished(t *testing.T) {
	for _, name := range []string{"gorganizerd", "gorganizerctl", "gorganizer"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			output, err := f.run(t, "do_build", "FAKE_WRONG_BINARY="+name)
			if err == nil || !strings.Contains(output, "The new build failed. Your installed version is unchanged.") {
				t.Fatalf("expected version validation failure for %s, got %v: %s", name, err, output)
			}
			f.assertInstalledOld(t)
			if got := string(readFixtureFile(t, filepath.Join(f.root, ".build-fingerprint"))); got != "prior-fingerprint\n" {
				t.Fatalf("fingerprint changed after version mismatch: %q", got)
			}
		})
	}
}

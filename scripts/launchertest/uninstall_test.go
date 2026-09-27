package launchertest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const fakeUninstallCtl = `#!/bin/bash
printf '%s\n' "$*" >> "$SHIM_LOG"
case "$*" in
    'uninstall --check') [ "${FAKE_CHECK_FAIL:-}" != yes ] ;;
    uninstall*) [ "${FAKE_UNINSTALL_FAIL:-}" != yes ] ;;
    migrate-data*'--dry-run --json')
        [ "${FAKE_JSON_FAIL:-}" != yes ] || exit 1
        printf '{"sources":%s}\n' "${FAKE_SOURCES:-[]}" ;;
    migrate-data*'--dry-run --list')
        [ "${FAKE_LIST_FAIL:-}" != yes ] || exit 1
        printf '%s\n' "${FAKE_LIST:-}" ;;
    *) exit 2 ;;
esac
`

// installUninstallCtl records all maintenance calls in an isolated checkout.
func (f *fixture) installUninstallCtl(t *testing.T) string {
	t.Helper()
	writeFixtureFile(t, filepath.Join(f.root, "gorganizerctl"), []byte(fakeUninstallCtl), 0o755)
	return filepath.Join(f.root, "uninstall-calls")
}

// uninstallListingCalls returns the expected mod-folder scans for the available JSON parser.
func (f *fixture) uninstallListingCalls() string {
	calls := "migrate-data --from " + f.root + " --dry-run --json\n"
	if _, err := exec.LookPath("jq"); err != nil {
		calls += "migrate-data --from " + f.root + " --dry-run --list\n"
	}
	return calls
}

// TestUninstallDelegatesBeforeRemovingBuildFiles checks that only a successful ctl run deletes artifacts.
func TestUninstallDelegatesBeforeRemovingBuildFiles(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   string
		fail   bool
		remove bool
	}{
		{name: "refused", args: "--purge --yes", fail: true},
		{name: "check only", args: "--check"},
		{name: "success", args: "--keep-data --yes", remove: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			log := f.installUninstallCtl(t)
			writeFixtureFile(t, filepath.Join(f.root, ".build-staging", "keep"), []byte("staging"), 0o600)
			settings := []string{"SHIM_LOG=" + log}
			if tc.fail {
				settings = append(settings, "FAKE_UNINSTALL_FAIL=yes")
			}
			_, err := f.run(t, "cmd_uninstall "+tc.args, settings...)
			if (err == nil) == tc.fail {
				t.Fatalf("command outcome = %v; expected failure = %v", err, tc.fail)
			}
			wantCalls := "uninstall " + tc.args + "\n"
			if tc.args != "--check" {
				wantCalls = f.uninstallListingCalls() + wantCalls
			}
			if got := string(readFixtureFile(t, log)); got != wantCalls {
				t.Errorf("maintenance calls = %q, want %q", got, wantCalls)
			}
			for _, path := range []string{"gorganizerd", "gorganizerctl", ".build-staging/keep"} {
				_, statErr := os.Lstat(filepath.Join(f.root, path))
				if tc.remove != os.IsNotExist(statErr) {
					t.Errorf("%s after uninstall: %v", path, statErr)
				}
			}
		})
	}
}

// TestUninstallWarnsAboutOldMods checks the checkout warning is printed before removing the CLI.
func TestUninstallWarnsAboutOldMods(t *testing.T) {
	f := newFixture(t)
	log := f.installUninstallCtl(t)
	old := filepath.Join(f.root, "SkyrimSE_Mods")
	writeFixtureFile(t, filepath.Join(old, "mod.esp"), []byte("keep"), 0o600)
	output, err := f.run(t, "cmd_uninstall --yes", "SHIM_LOG="+log, "FAKE_SOURCES=[\""+old+"\"]", "FAKE_LIST="+old)
	if err != nil {
		t.Fatalf("uninstall: %v: %q", err, output)
	}
	want := "Your mods are still inside this folder: " + old + ". Move them with ./gorganizer.sh import --from \"" + f.root + "\" before you delete it, or they will be lost."
	if !strings.Contains(output, want) {
		t.Errorf("warning = %q", output)
	}
	if _, err := os.Stat(filepath.Join(old, "mod.esp")); err != nil {
		t.Errorf("old mods were removed: %v", err)
	}
	if calls := string(readFixtureFile(t, log)); calls != f.uninstallListingCalls()+"uninstall --yes\n" {
		t.Errorf("maintenance calls = %q", calls)
	}
}

// TestUninstallWarnsIfOldModScanFails checks a failed listing cannot hide remaining checkout mods.
func TestUninstallWarnsIfOldModScanFails(t *testing.T) {
	f := newFixture(t)
	log := f.installUninstallCtl(t)
	output, err := f.run(t, "cmd_uninstall --yes", "SHIM_LOG="+log, "FAKE_JSON_FAIL=yes")
	if err != nil || !strings.Contains(output, "Your mods may still be inside this folder.") {
		t.Fatalf("uninstall after failed listing: %v, %q", err, output)
	}
	if calls := string(readFixtureFile(t, log)); calls != "migrate-data --from "+f.root+" --dry-run --json\nuninstall --yes\n" {
		t.Errorf("maintenance calls = %q", calls)
	}
}

// TestUninstallRejectsLinkedBuildPath checks that no maintenance call runs for an unsafe artifact.
func TestUninstallRejectsLinkedBuildPath(t *testing.T) {
	f := newFixture(t)
	log := f.installUninstallCtl(t)
	outside := filepath.Join(f.root, "outside")
	writeFixtureFile(t, filepath.Join(outside, "keep"), []byte("keep"), 0o600)
	if err := os.RemoveAll(filepath.Join(f.root, "build")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(f.root, "build")); err != nil {
		t.Fatal(err)
	}
	output, err := f.run(t, "cmd_uninstall --yes", "SHIM_LOG="+log)
	if err == nil || !strings.Contains(output, "Cannot safely remove build files") {
		t.Fatalf("linked build artifact: %v, %q", err, output)
	}
	if _, err := os.Lstat(log); !os.IsNotExist(err) {
		t.Errorf("maintenance ran despite an unsafe build folder: %v", err)
	}
	for _, path := range []string{filepath.Join(outside, "keep"), filepath.Join(f.root, "gorganizerd")} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("cleanup changed %s: %v", path, err)
		}
	}
}

// TestUninstallMissingCtlLeavesBuildAlone checks the missing maintenance binary blocks launcher cleanup.
func TestUninstallMissingCtlLeavesBuildAlone(t *testing.T) {
	f := newFixture(t)
	if err := os.Remove(filepath.Join(f.root, "gorganizerctl")); err != nil {
		t.Fatal(err)
	}
	output, err := f.run(t, "cmd_uninstall --yes")
	if err == nil || !strings.Contains(output, "maintenance tool is missing, so nothing was removed") {
		t.Fatalf("missing tool: %v, %q", err, output)
	}
	if _, err := os.Lstat(filepath.Join(f.root, "gorganizerd")); err != nil {
		t.Errorf("daemon binary changed: %v", err)
	}
}

// runCleaner executes the copied developer reset in an isolated user environment.
func (f *fixture) runCleaner(t *testing.T, settings ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", filepath.Join(f.root, "cleaner.sh"), "--yes")
	cmd.Dir = f.root
	cmd.Env = append(os.Environ(), "HOME="+f.root, "XDG_CONFIG_HOME="+filepath.Join(f.root, "config"), "XDG_DATA_HOME="+filepath.Join(f.root, "share"), "XDG_RUNTIME_DIR="+filepath.Join(f.root, "runtime"), "TMPDIR="+filepath.Join(f.root, "tmp"), "PATH="+f.shims+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd.Env = append(cmd.Env, settings...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("cleaner timed out: %s", output)
	}
	return string(output), err
}

// TestCleanerChecksGamesBeforeDeleting checks that --yes never skips the offline safety gate.
func TestCleanerChecksGamesBeforeDeleting(t *testing.T) {
	f := newFixture(t)
	log := f.installUninstallCtl(t)
	writeFixtureFile(t, filepath.Join(f.root, "Old_Mods", "save"), []byte("keep"), 0o600)
	output, err := f.runCleaner(t, "SHIM_LOG="+log, "FAKE_CHECK_FAIL=yes")
	if err == nil {
		t.Fatalf("cleaner ignored failed check: %q", output)
	}
	if calls := string(readFixtureFile(t, log)); calls != "uninstall --check\n" {
		t.Fatalf("maintenance calls = %q", calls)
	}
	if _, err := os.Stat(filepath.Join(f.root, "Old_Mods", "save")); err != nil {
		t.Errorf("mod deleted after failed check: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.root, "gorganizerd")); err != nil {
		t.Errorf("build deleted after failed check: %v", err)
	}
}

// TestCleanerDeletesOnlyValidatedOldFolders checks that an unlisted folder and extraction are kept.
func TestCleanerDeletesOnlyValidatedOldFolders(t *testing.T) {
	f := newFixture(t)
	log := f.installUninstallCtl(t)
	old := filepath.Join(f.root, "SkyrimSE_Mods")
	unlisted := filepath.Join(f.root, "Personal_Mods")
	for _, path := range []string{old, unlisted} {
		writeFixtureFile(t, filepath.Join(path, "keep"), []byte("mod"), 0o600)
	}
	tmp := filepath.Join(f.root, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	extract := filepath.Join(tmp, "gorganizer-extract-"+strconv.Itoa(os.Getuid())+"-abcdef012345")
	unlistedExtract := filepath.Join(tmp, "gorganizer-extract-"+strconv.Itoa(os.Getuid())+"-not-our-prefix")
	for _, path := range []string{extract, unlistedExtract} {
		writeFixtureFile(t, filepath.Join(path, "keep"), []byte("saved"), 0o600)
	}
	output, err := f.runCleaner(t, "SHIM_LOG="+log, "FAKE_LIST="+old)
	if err != nil {
		t.Fatalf("cleaner: %v: %q", err, output)
	}
	if _, err := os.Lstat(old); !os.IsNotExist(err) {
		t.Errorf("listed folder remained: %v", err)
	}
	if _, err := os.Stat(filepath.Join(unlisted, "keep")); err != nil {
		t.Errorf("unlisted folder changed: %v", err)
	}
	if _, err := os.Lstat(extract); !os.IsNotExist(err) {
		t.Errorf("owned extraction remained: %v", err)
	}
	if _, err := os.Stat(filepath.Join(unlistedExtract, "keep")); err != nil {
		t.Errorf("unlisted extraction changed: %v", err)
	}
	if calls := string(readFixtureFile(t, log)); !strings.HasPrefix(calls, "uninstall --check\nmigrate-data --from "+f.root+" --dry-run --list\n") {
		t.Errorf("maintenance calls = %q", calls)
	}
}

// TestCleanerRefusesLinkedOldFolder checks that a migration source cannot redirect cleanup.
func TestCleanerRefusesLinkedOldFolder(t *testing.T) {
	f := newFixture(t)
	log := f.installUninstallCtl(t)
	if err := os.Mkdir(filepath.Join(f.root, "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(f.root, "outside")
	writeFixtureFile(t, filepath.Join(outside, "save"), []byte("keep"), 0o600)
	linked := filepath.Join(f.root, "SkyrimSE_Mods")
	if err := os.Symlink(outside, linked); err != nil {
		t.Fatal(err)
	}
	output, err := f.runCleaner(t, "SHIM_LOG="+log, "FAKE_LIST="+linked)
	if err == nil || !strings.Contains(output, "Cannot safely check old mod folders") {
		t.Fatalf("linked source: %v: %q", err, output)
	}
	for _, path := range []string{filepath.Join(outside, "save"), filepath.Join(f.root, "gorganizerd")} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("cleanup changed %s: %v", path, err)
		}
	}
}

// TestCleanerRejectsLinkedBuildPath checks that unsafe build artifacts block all deletion.
func TestCleanerRejectsLinkedBuildPath(t *testing.T) {
	f := newFixture(t)
	log := f.installUninstallCtl(t)
	if err := os.Mkdir(filepath.Join(f.root, "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(f.root, "outside")
	writeFixtureFile(t, filepath.Join(outside, "keep"), []byte("keep"), 0o600)
	if err := os.RemoveAll(filepath.Join(f.root, "build")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(f.root, "build")); err != nil {
		t.Fatal(err)
	}
	output, err := f.runCleaner(t, "SHIM_LOG="+log)
	if err == nil || !strings.Contains(output, "Cannot safely clean") {
		t.Fatalf("linked build artifact: %v, %q", err, output)
	}
	for _, path := range []string{filepath.Join(outside, "keep"), filepath.Join(f.root, "gorganizerd")} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("cleanup changed %s: %v", path, err)
		}
	}
}

// TestCleanerRejectsForeignDesktopEntry checks that a same-named registration is never deleted.
func TestCleanerRejectsForeignDesktopEntry(t *testing.T) {
	f := newFixture(t)
	log := f.installUninstallCtl(t)
	if err := os.Mkdir(filepath.Join(f.root, "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(f.root, "share", "applications", "gorganizer.desktop")
	writeFixtureFile(t, entry, []byte("[Desktop Entry]\nName=Someone else\nExec=/other/program\n"), 0o600)
	output, err := f.runCleaner(t, "SHIM_LOG="+log)
	if err == nil || !strings.Contains(output, "desktop entry does not belong to this checkout") {
		t.Fatalf("unrelated desktop entry: %v, %q", err, output)
	}
	if body := string(readFixtureFile(t, entry)); !strings.Contains(body, "Someone else") {
		t.Errorf("unrelated desktop entry changed: %q", body)
	}
	if _, err := os.Stat(filepath.Join(f.root, "gorganizerd")); err != nil {
		t.Errorf("build deleted after failed ownership check: %v", err)
	}
}

// TestCleanerRefusesLinkedExtract checks that a link using the extractor's name stops cleanup.
func TestCleanerRefusesLinkedExtract(t *testing.T) {
	f := newFixture(t)
	log := f.installUninstallCtl(t)
	outside := filepath.Join(f.root, "outside")
	writeFixtureFile(t, filepath.Join(outside, "save"), []byte("keep"), 0o600)
	tmp := filepath.Join(f.root, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(tmp, "gorganizer-extract-"+strconv.Itoa(os.Getuid())+"-abcdef012345")
	if err := os.Symlink(outside, linked); err != nil {
		t.Fatal(err)
	}
	output, err := f.runCleaner(t, "SHIM_LOG="+log)
	if err == nil || !strings.Contains(output, "Cannot safely check extraction folder") {
		t.Fatalf("linked extraction: %v: %q", err, output)
	}
	for _, path := range []string{filepath.Join(outside, "save"), filepath.Join(f.root, "gorganizerd")} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("cleanup changed %s: %v", path, err)
		}
	}
}

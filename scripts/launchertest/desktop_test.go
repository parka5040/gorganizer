package launchertest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fakeDesktopCtl = `#!/bin/sh
printf '%s\n' "$*" >> "$SHIM_LOG"
case "$1 $2" in
    'desktop status') exit 1 ;;
    'desktop register'|'desktop unregister') exit 0 ;;
    *) exit 93 ;;
esac
`

// TestRegisterUsesCtlAndNeverClearsDefault checks each desktop helper receives the expected arguments.
func TestRegisterUsesCtlAndNeverClearsDefault(t *testing.T) {
	f := newFixture(t)
	t.Setenv("HOME", t.TempDir())
	data := filepath.Join(t.TempDir(), "data")
	config := filepath.Join(t.TempDir(), "config")
	logPath := filepath.Join(t.TempDir(), "desktop-calls")
	writeFixtureFile(t, filepath.Join(f.root, "resources/icons/tmp_logo.png"), []byte("icon"), 0o644)
	writeFixtureFile(t, filepath.Join(f.root, "gorganizerctl"), []byte(fakeDesktopCtl), 0o755)
	for _, name := range []string{"xdg-mime", "update-desktop-database", "gtk-update-icon-cache", "notify-send"} {
		writeFixtureFile(t, filepath.Join(f.shims, name), []byte("#!/bin/sh\nprintf '%s %s\\n' '"+name+"' \"$*\" >> \"$SHIM_LOG\"\n"), 0o755)
	}
	settings := []string{"HOME=" + os.Getenv("HOME"), "XDG_DATA_HOME=" + data, "XDG_CONFIG_HOME=" + config, "SHIM_LOG=" + logPath}
	if output, err := f.run(t, `build_fingerprint > "$SCRIPT_DIR/.build-fingerprint" && cmd_register; cmd_unregister`, settings...); err != nil {
		t.Fatalf("register/unregister: %v, %s", err, output)
	}
	calls := string(readFixtureFile(t, logPath))
	icon := filepath.Join(data, "icons/hicolor/256x256/apps/gorganizer.png")
	for _, want := range []string{
		"desktop register --checkout " + f.root + " --icon " + icon,
		"desktop unregister --checkout " + f.root,
		"xdg-mime default gorganizer-nxm.desktop x-scheme-handler/nxm",
		"update-desktop-database " + filepath.Join(data, "applications"),
		"gtk-update-icon-cache " + filepath.Join(data, "icons/hicolor"),
	} {
		if !strings.Contains(calls, want+"\n") {
			t.Errorf("missing call %q in %q", want, calls)
		}
	}
	if strings.Contains(calls, "xdg-mime default  x-scheme-handler/nxm") || strings.Count(calls, "xdg-mime default ") != 1 {
		t.Errorf("unregister changed default: %q", calls)
	}
}

// TestRegisterBuildsOnlyMissingCtl checks first-time registration does not require the GUI build.
func TestRegisterBuildsOnlyMissingCtl(t *testing.T) {
	f := newFixture(t)
	if err := os.Remove(filepath.Join(f.root, "gorganizerctl")); err != nil {
		t.Fatal(err)
	}
	output, err := f.run(t, "ensure_register_ctl")
	if err != nil {
		t.Fatalf("build maintenance tool: %v, %s", err, output)
	}
	calls := string(readFixtureFile(t, f.log))
	if !strings.Contains(calls, " ctl\n") || strings.Contains(calls, " all gui") {
		t.Errorf("build calls = %q", calls)
	}
}

// TestRegisterRebuildsStaleCtl checks registration replaces a maintenance tool built from older sources.
func TestRegisterRebuildsStaleCtl(t *testing.T) {
	f := newFixture(t)
	output, err := f.run(t, "ensure_register_ctl")
	if err != nil {
		t.Fatalf("build maintenance tool: %v, %s", err, output)
	}
	calls := string(readFixtureFile(t, f.log))
	if !strings.Contains(calls, " ctl\n") || strings.Contains(calls, " all gui") {
		t.Errorf("build calls = %q", calls)
	}
}

// TestRegisterKeepsCurrentCtl checks registration does not rebuild a maintenance tool that matches the sources.
func TestRegisterKeepsCurrentCtl(t *testing.T) {
	f := newFixture(t)
	output, err := f.run(t, `build_fingerprint > "$SCRIPT_DIR/.build-fingerprint" && ensure_register_ctl`)
	if err != nil {
		t.Fatalf("check maintenance tool: %v, %s", err, output)
	}
	if calls, err := os.ReadFile(f.log); err == nil && strings.Contains(string(calls), " ctl\n") {
		t.Errorf("current maintenance tool was rebuilt: %q", calls)
	}
}

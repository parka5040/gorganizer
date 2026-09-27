package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/instancelock"
	"github.com/parka/gorganizer/internal/vfs"
)

type uninstallFixture struct {
	deps    uninstallDeps
	out     *bytes.Buffer
	errOut  *bytes.Buffer
	root    string
	install string
	data    string
	removed []string
}

// newUninstallFixture isolates settings, user data, runtime state and process information.
func newUninstallFixture(t *testing.T) *uninstallFixture {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "share"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "runtime"))
	t.Setenv("TMPDIR", filepath.Join(root, "tmp"))
	t.Setenv("GORGANIZER_ROOT", root)
	install := filepath.Join(root, "game")
	data := filepath.Join(install, "Data")
	for _, dir := range []string{data, filepath.Join(root, "proc"), filepath.Join(root, "runtime"), filepath.Join(root, "tmp")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.DefaultConfig()
	cfg.Games["skyrimse"] = config.GameConfig{Name: "Skyrim Special Edition", InstallPath: install, SteamAppID: 489830}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	out, errOut := new(bytes.Buffer), new(bytes.Buffer)
	f := &uninstallFixture{root: root, install: install, data: data, out: out, errOut: errOut}
	f.deps = uninstallDeps{in: strings.NewReader(""), out: out, errOut: errOut, procRoot: filepath.Join(root, "proc"), launcher: filepath.Join(root, "gorganizer.sh"), remove: func(path string) error {
		f.removed = append(f.removed, path)
		return os.RemoveAll(path)
	}}
	return f
}

// TestUninstallRefusesWhileDaemonRuns checks that the instance lock prevents any deletion.
func TestUninstallRefusesWhileDaemonRuns(t *testing.T) {
	f := newUninstallFixture(t)
	release, err := instancelock.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if got := runUninstallWith([]string{"--yes"}, f.deps); got != 1 || len(f.removed) != 0 || !strings.Contains(f.errOut.String(), "Close Gorganizer first") {
		t.Fatalf("exit = %d, deletions = %v, error = %q", got, f.removed, f.errOut.String())
	}
}

// TestUninstallRefusesWhileGameRuns checks install processes and Steam wrappers block recovery and deletion.
func TestUninstallRefusesWhileGameRuns(t *testing.T) {
	for _, kind := range []string{"game process", "Steam wrapper"} {
		t.Run(kind, func(t *testing.T) {
			f := newUninstallFixture(t)
			pidDir := filepath.Join(f.deps.procRoot, "4242")
			if err := os.Mkdir(pidDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if kind == "game process" {
				if err := os.Symlink(f.install, filepath.Join(pidDir, "cwd")); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(filepath.Join(pidDir, "cmdline"), []byte("/steam/reaper\x00SteamLaunch\x00AppId=489830\x00--\x00proton\x00"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := runUninstallWith([]string{"--yes"}, f.deps); got != 1 || len(f.removed) != 0 || !strings.Contains(f.errOut.String(), "Close Skyrim Special Edition before removing Gorganizer") {
				t.Fatalf("exit = %d, deletions = %v, error = %q", got, f.removed, f.errOut.String())
			}
		})
	}
}

// TestUninstallRestoresDeployedGameFirst checks that new loose files are captured before deletion.
func TestUninstallRestoresDeployedGameFirst(t *testing.T) {
	f := newUninstallFixture(t)
	if err := os.WriteFile(filepath.Join(f.data, "base.esm"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	overwrite := filepath.Join(f.root, "Overwrite")
	mount := vfs.NewMountManager(f.data, overwrite, "skyrimse")
	if err := mount.Activate([]vfs.Layer{{Name: "__base__", RootPath: f.data, Enabled: true}}, "Default"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.data, "new.esp"), []byte("saved"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.deps.remove = func(path string) error {
		if body, err := os.ReadFile(filepath.Join(f.data, "base.esm")); err != nil || string(body) != "original" {
			t.Errorf("Data was not restored before deleting %s: %s (%v)", path, body, err)
		}
		if body, err := os.ReadFile(filepath.Join(overwrite, "new.esp")); err != nil || string(body) != "saved" {
			t.Errorf("new output was not captured: %s (%v)", body, err)
		}
		f.removed = append(f.removed, path)
		return os.RemoveAll(path)
	}
	if got := runUninstallWith([]string{"--yes"}, f.deps); got != 0 || len(f.removed) == 0 {
		t.Fatalf("exit = %d, deletions = %v, error = %q", got, f.removed, f.errOut.String())
	}
	if _, err := os.Lstat(f.data + ".orig"); !os.IsNotExist(err) {
		t.Errorf("backup remained: %v", err)
	}
}

// TestUninstallCaptureFailureKeepsAllFiles checks that a failed capture leaves the farm and user data intact.
func TestUninstallCaptureFailureKeepsAllFiles(t *testing.T) {
	f := newUninstallFixture(t)
	if err := os.WriteFile(filepath.Join(f.data, "base.esm"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	overwrite := filepath.Join(f.root, "blocked-overwrite")
	mount := vfs.NewMountManager(f.data, overwrite, "skyrimse")
	if err := mount.Activate([]vfs.Layer{{Name: "__base__", RootPath: f.data, Enabled: true}}, "Default"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.data, "new.esp"), []byte("saved"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overwrite, []byte("not a folder"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runUninstallWith([]string{"--yes"}, f.deps); code != 1 || len(f.removed) != 0 || !strings.Contains(f.errOut.String(), uninstallRestoreFailure) {
		t.Fatalf("exit = %d, deletions = %v, error = %q", code, f.removed, f.errOut.String())
	}
	if _, err := vfs.ReadSentinel(f.data); err != nil {
		t.Errorf("farm was removed after capture failed: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(f.data, "new.esp")); err != nil || string(body) != "saved" {
		t.Errorf("uncaptured output = %q, %v", body, err)
	}
}

// TestUninstallKeepsDataByDefault checks that settings and mods remain while logs are removed.
func TestUninstallKeepsDataByDefault(t *testing.T) {
	f := newUninstallFixture(t)
	mod := filepath.Join(config.DataDir(), "skyrimse", "mods", "Example", "mod.esp")
	if err := os.MkdirAll(filepath.Dir(mod), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mod, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(f.root, "state", "gorganizer", "daemon.log")
	if err := os.MkdirAll(filepath.Dir(state), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, []byte("log"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runUninstallWith([]string{"--yes"}, f.deps); got != 0 {
		t.Fatalf("exit = %d, error = %q", got, f.errOut.String())
	}
	for _, path := range []string{mod, filepath.Join(config.ConfigDir(), "config.json")} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("kept path %s: %v", path, err)
		}
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Errorf("log remained: %v", err)
	}
	if !strings.Contains(f.out.String(), "SMAPI stays installed") {
		t.Errorf("SMAPI message missing: %q", f.out.String())
	}
}

// TestUninstallHandlesMissingInstall checks preservation, purge refusal, and explicit permission to forget a game.
func TestUninstallHandlesMissingInstall(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"default", []string{"--yes"}, 0},
		{"check", []string{"--check"}, 0},
		{"purge refused", []string{"--purge", "--yes"}, 1},
		{"purge with forget", []string{"--purge", "--forget-missing-games", "--yes"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUninstallFixture(t)
			if err := os.RemoveAll(f.install); err != nil {
				t.Fatal(err)
			}
			f.deps.isTTY = true
			f.deps.in = strings.NewReader("delete my mods\n")
			got := runUninstallWith(tc.args, f.deps)
			if got != tc.want || (got != 0 && len(f.removed) != 0) || (got == 0 && tc.name != "check" && len(f.removed) == 0) {
				t.Fatalf("exit = %d, deletions = %v, output = %q, error = %q", got, f.removed, f.out.String(), f.errOut.String())
			}
			if tc.name == "purge refused" {
				want := "Skyrim Special Edition cannot be found at " + f.install + ". Connect the drive that holds it, or add --forget-missing-games to remove Gorganizer without restoring it. Nothing has been deleted."
				if !strings.Contains(f.errOut.String(), want) {
					t.Errorf("missing-game refusal = %q", f.errOut.String())
				}
			} else {
				want := "Note: Skyrim Special Edition is not installed at " + f.install + " any more, so there is nothing to restore."
				if !strings.Contains(f.out.String(), want) {
					t.Errorf("missing-game note = %q", f.out.String())
				}
			}
			if _, err := os.Lstat(f.install); !os.IsNotExist(err) {
				t.Errorf("missing install was recreated: %v", err)
			}
		})
	}
}

// TestUninstallPurgeNeedsSecondConfirmation checks that the first approval never purges mods.
func TestUninstallPurgeNeedsSecondConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  int
	}{
		{name: "first approval only", input: "y\n", want: 1},
		{name: "second refused", input: "y\nno\n", want: 1},
		{name: "second accepted", input: "y\ndelete my mods\n", want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUninstallFixture(t)
			f.deps.isTTY = true
			f.deps.in = strings.NewReader(tc.input)
			mod := filepath.Join(config.DataDir(), "skyrimse", "mods", "My Mod", "file")
			if err := os.MkdirAll(filepath.Dir(mod), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(mod, []byte("saved"), 0o600); err != nil {
				t.Fatal(err)
			}
			got := runUninstallWith([]string{"--purge"}, f.deps)
			if got != tc.want || (got != 0 && len(f.removed) != 0) || !strings.Contains(f.out.String(), "This deletes your mods and downloads (5 bytes). This cannot be undone.") || (got != 0 && !strings.Contains(f.out.String(), uninstallRestoredMessage)) {
				t.Fatalf("exit = %d, deletions = %v, output = %q, error = %q", got, f.removed, f.out.String(), f.errOut.String())
			}
		})
	}
}

// TestUninstallDeletesNothingOnAnyFailure checks the deletion seam for each preflight refusal.
func TestUninstallDeletesNothingOnAnyFailure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *uninstallFixture)
		args  []string
	}{
		{name: "missing process table", setup: func(_ *testing.T, f *uninstallFixture) { f.deps.procRoot = filepath.Join(f.root, "missing") }, args: []string{"--yes"}},
		{name: "missing Data", setup: func(t *testing.T, f *uninstallFixture) {
			if err := os.Remove(f.data); err != nil {
				t.Fatal(err)
			}
		}, args: []string{"--yes"}},
		{name: "ambiguous backup", setup: func(t *testing.T, f *uninstallFixture) {
			if err := os.Mkdir(f.data+".orig", 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.data, "loose.esp"), []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, args: []string{"--yes"}},
		{name: "unsafe data folder", setup: func(t *testing.T, f *uninstallFixture) {
			if err := os.Remove(f.data); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(f.root, f.data); err != nil {
				t.Fatal(err)
			}
		}, args: []string{"--yes"}},
		{name: "Steam maintenance with preserved batches", setup: func(t *testing.T, f *uninstallFixture) {
			body := fmt.Sprintf(`{"schema_version":1,"reason":"verify","game_id":"skyrimse","batch_ids":["batch"],"created_at":%q}`, time.Now().UTC().Format(time.RFC3339Nano))
			if err := os.WriteFile(vfs.MaintenancePath(f.data), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}, args: []string{"--yes"}},
		{name: "root deployment marker", setup: func(t *testing.T, f *uninstallFixture) {
			if err := os.WriteFile(filepath.Join(f.install, vfs.RootManifestFilename), []byte("invalid"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, args: []string{"--yes"}},
		{name: "linked NXM startup lock", setup: func(t *testing.T, f *uninstallFixture) {
			if err := os.MkdirAll(config.RuntimeDir(), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(f.install, filepath.Join(config.RuntimeDir(), "nxm-start.lock")); err != nil {
				t.Fatal(err)
			}
		}, args: []string{"--yes"}},
		{name: "no confirmation", setup: func(_ *testing.T, _ *uninstallFixture) {}, args: nil},
		{name: "purge noninteractive", setup: func(_ *testing.T, _ *uninstallFixture) {}, args: []string{"--purge", "--yes"}},
		{name: "invalid settings", setup: func(t *testing.T, f *uninstallFixture) {
			if err := os.WriteFile(filepath.Join(config.ConfigDir(), "config.json"), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, args: []string{"--yes"}},
		{name: "active check", setup: func(t *testing.T, f *uninstallFixture) {
			if err := os.WriteFile(filepath.Join(f.data, vfs.SentinelFilename), []byte("invalid"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, args: []string{"--check"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUninstallFixture(t)
			tc.setup(t, f)
			if code := runUninstallWith(tc.args, f.deps); code == 0 || len(f.removed) != 0 {
				t.Fatalf("exit = %d, deletions = %v, error = %q", code, f.removed, f.errOut.String())
			}
			if tc.name == "no confirmation" || tc.name == "purge noninteractive" {
				output := f.out.String()
				if !strings.Contains(output, "Restoring your games to their original state first…") || !strings.Contains(output, uninstallRestoredMessage) || strings.Index(output, "Restoring your games") > strings.Index(output, "Remove these Gorganizer files") {
					t.Errorf("restoration before confirmation was not explained: %q", output)
				}
			}
		})
	}
}

// TestUninstallNeverFollowsSymlinks checks that linked owned-path ancestors or destinations block deletion.
func TestUninstallNeverFollowsSymlinks(t *testing.T) {
	for _, location := range []string{"data folder", "state folder", "desktop entry", "desktop ancestor", "runtime folder", "config folder"} {
		t.Run(location, func(t *testing.T) {
			f := newUninstallFixture(t)
			external := filepath.Join(f.root, "outside")
			if err := os.WriteFile(external, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			var link string
			switch location {
			case "data folder":
				link = config.DataDir()
			case "state folder":
				link = filepath.Join(f.root, "state", "gorganizer")
			case "desktop entry":
				link = filepath.Join(f.root, "share", "applications", "gorganizer.desktop")
			case "desktop ancestor":
				link = filepath.Join(f.root, "share", "applications")
			case "runtime folder":
				link = config.RuntimeDir()
			case "config folder":
				link = config.ConfigDir()
			}
			if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(link); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(external, link); err != nil {
				t.Fatal(err)
			}
			args := []string{"--yes"}
			if location == "data folder" || location == "config folder" {
				args = append(args, "--purge")
			}
			if code := runUninstallWith(args, f.deps); code == 0 || len(f.removed) != 0 {
				t.Fatalf("exit = %d, deletions = %v, error = %q", code, f.removed, f.errOut.String())
			}
			if body, err := os.ReadFile(external); err != nil || string(body) != "keep" {
				t.Errorf("external file = %q, %v", body, err)
			}
		})
	}
}

// TestUninstallRefusesWhileSessionRuns checks the GUI supervisor's lock also prevents removal.
func TestUninstallRefusesWhileSessionRuns(t *testing.T) {
	f := newUninstallFixture(t)
	release, err := acquireSessionLock()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if code := runUninstallWith([]string{"--yes"}, f.deps); code != 1 || len(f.removed) != 0 || !strings.Contains(f.errOut.String(), "Close Gorganizer first") {
		t.Fatalf("exit = %d, deletions = %v, error = %q", code, f.removed, f.errOut.String())
	}
}

// TestUninstallHonoursLaunchTicket checks that a just-launched Steam game blocks even without a visible process.
func TestUninstallHonoursLaunchTicket(t *testing.T) {
	f := newUninstallFixture(t)
	body := fmt.Sprintf(`{"schema_version":1,"launched_at":%q}`, time.Now().UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(f.data+vfs.RetainedSessionSiblingSuffix, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runUninstallWith([]string{"--yes"}, f.deps); code != 1 || len(f.removed) != 0 || !strings.Contains(f.errOut.String(), "Close Skyrim Special Edition") {
		t.Fatalf("exit = %d, deletions = %v, error = %q", code, f.removed, f.errOut.String())
	}
}

// TestUninstallClearsOldLaunchRecord checks a restored game can shed its expired launch ticket.
func TestUninstallClearsOldLaunchRecord(t *testing.T) {
	f := newUninstallFixture(t)
	path := f.data + vfs.RetainedSessionSiblingSuffix
	body := fmt.Sprintf(`{"schema_version":1,"launched_at":%q}`, time.Now().Add(-5*time.Minute).UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runUninstallWith([]string{"--yes"}, f.deps); code != 0 {
		t.Fatalf("exit = %d, error = %q", code, f.errOut.String())
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("old launch record remains: %v", err)
	}
}

// TestUninstallDeduplicatesSharedInstalls checks linked games share one recovery and both Steam app IDs.
func TestUninstallDeduplicatesSharedInstalls(t *testing.T) {
	f := newUninstallFixture(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Games["ttw"] = config.GameConfig{Name: "Tale of Two Wastelands", LinkedFromGameID: "skyrimse", SteamAppID: 22380}
	targets, err := uninstallTargets(cfg)
	if err != nil || len(targets) != 1 || targets[0].gameID != "skyrimse" || len(targets[0].appIDs) != 1 {
		t.Fatalf("targets = %+v, error = %v", targets, err)
	}
	if code := runUninstallWith([]string{"--check"}, f.deps); code != 0 || len(f.removed) != 0 {
		t.Fatalf("check = %d, deletions = %v, error = %q", code, f.removed, f.errOut.String())
	}
}

// TestUninstallUnregistersOnlyItsHandler checks desktop ownership and surgical NXM association removal.
func TestUninstallUnregistersOnlyItsHandler(t *testing.T) {
	f := newUninstallFixture(t)
	apps := filepath.Join(f.root, "share", "applications")
	if err := os.MkdirAll(apps, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, action := range map[string]string{"gorganizer.desktop": "launch", "gorganizer-nxm.desktop": "nxm %u"} {
		label := "Gorganizer"
		if name == "gorganizer-nxm.desktop" {
			label = "Gorganizer NXM Handler"
		}
		body := "[Desktop Entry]\nName=" + label + "\nExec=" + f.deps.launcher + " " + action + "\n"
		if err := os.WriteFile(filepath.Join(apps, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mime := filepath.Join(f.root, "config", "mimeapps.list")
	body := "[Default Applications]\nx-scheme-handler/nxm=gorganizer-nxm.desktop\nx-scheme-handler/custom=other.desktop\n"
	if err := os.WriteFile(mime, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	nxmLock := filepath.Join(config.RuntimeDir(), "nxm-start.lock")
	if err := os.MkdirAll(config.RuntimeDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nxmLock, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runUninstallWith([]string{"--yes"}, f.deps); code != 0 {
		t.Fatalf("exit = %d, error = %q", code, f.errOut.String())
	}
	if _, err := os.Lstat(nxmLock); !os.IsNotExist(err) {
		t.Errorf("NXM startup lock remains: %v", err)
	}
	result, err := os.ReadFile(mime)
	if err != nil || strings.Contains(string(result), "gorganizer-nxm.desktop") || !strings.Contains(string(result), "x-scheme-handler/custom=other.desktop") {
		t.Errorf("NXM association = %q, %v", result, err)
	}
}

// TestUninstallKeepsForeignDesktopEntry checks an unrelated copy is kept while other files are removed.
func TestUninstallKeepsForeignDesktopEntry(t *testing.T) {
	for _, name := range []string{"gorganizer.desktop", "gorganizer-nxm.desktop"} {
		t.Run(name, func(t *testing.T) {
			f := newUninstallFixture(t)
			path := filepath.Join(f.root, "share", "applications", name)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			body := "[Desktop Entry]\nName=Gorganizer\nExec=/other/copy/gorganizer.sh launch\n"
			if name == "gorganizer-nxm.desktop" {
				body = "[Desktop Entry]\nName=Gorganizer NXM Handler\nExec=/other/copy/gorganizer.sh nxm %u\n"
			}
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(f.root, "state", "gorganizer", "daemon.log")
			if err := os.MkdirAll(filepath.Dir(state), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(state, []byte("log"), 0o600); err != nil {
				t.Fatal(err)
			}
			mime := filepath.Join(f.root, "config", "mimeapps.list")
			if err := os.WriteFile(mime, []byte("[Default Applications]\nx-scheme-handler/nxm=gorganizer-nxm.desktop\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if code := runUninstallWith([]string{"--yes"}, f.deps); code != 0 || len(f.removed) == 0 {
				t.Fatalf("exit = %d, deletions = %v, error = %q", code, f.removed, f.errOut.String())
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != body {
				t.Errorf("other copy changed: %q, %v", got, err)
			}
			if !strings.Contains(f.out.String(), "Kept "+path+" because it belongs to another copy of Gorganizer.") {
				t.Errorf("missing note: %q", f.out.String())
			}
			if _, err := os.Lstat(state); !os.IsNotExist(err) {
				t.Errorf("state not removed: %v", err)
			}
			mimeBody, err := os.ReadFile(mime)
			if err != nil {
				t.Fatal(err)
			}
			if name == "gorganizer-nxm.desktop" && !strings.Contains(string(mimeBody), "gorganizer-nxm.desktop") || name == "gorganizer.desktop" && strings.Contains(string(mimeBody), "gorganizer-nxm.desktop") {
				t.Errorf("NXM association for another copy = %q", mimeBody)
			}
		})
	}
}

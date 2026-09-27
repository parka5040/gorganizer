package procscan

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeProcess writes a /proc-style entry for pid whose exe and cwd links point at the given targets.
func fakeProcess(t *testing.T, procDir, pid, exe, cwd string) {
	t.Helper()
	dir := filepath.Join(procDir, pid)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"exe": exe, "cwd": cwd} {
		if target == "" {
			continue
		}
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScanProcessesInMatchesExecutablesAndWorkingDirectoriesInsideTheInstall(t *testing.T) {
	base := t.TempDir()
	install := filepath.Join(base, "Stardew Valley")
	if err := os.MkdirAll(filepath.Join(install, "Content"), 0755); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(base, "linked")
	if err := os.Symlink(install, linked); err != nil {
		t.Fatal(err)
	}
	self := strconv.Itoa(os.Getpid())
	for _, tc := range []struct {
		name     string
		scanDir  string
		exe, cwd string
		pid      string
		want     bool
	}{
		{name: "unrelated process", scanDir: install, pid: "100", exe: "/usr/bin/bash", cwd: "/var/empty"},
		{name: "working directory inside", scanDir: install, pid: "101", exe: "/usr/bin/bash", cwd: filepath.Join(install, "Content"), want: true},
		{name: "working directory is the install", scanDir: install, pid: "102", cwd: install, want: true},
		{name: "deleted executable inside", scanDir: install, pid: "103", exe: filepath.Join(install, "StardewModdingAPI") + procDeletedSuffix, cwd: "/", want: true},
		{name: "sibling with a shared prefix", scanDir: install, pid: "104", exe: install + "2/game", cwd: install + " Mods"},
		{name: "the daemon itself", scanDir: install, pid: self, exe: filepath.Join(install, "gorganizerd"), cwd: install},
		{name: "non-process entry", scanDir: install, pid: "self", cwd: install},
		{name: "symlinked install path", scanDir: linked, pid: "105", cwd: filepath.Join(install, "Content"), want: true},
		{name: "missing links", scanDir: install, pid: "106"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			procDir := filepath.Join(t.TempDir(), "proc")
			fakeProcess(t, procDir, tc.pid, tc.exe, tc.cwd)
			got, err := RunningIn(procDir, tc.scanDir, nil)
			if err != nil || got != tc.want {
				t.Fatalf("scanProcessesIn = %v (%v), want %v", got, err, tc.want)
			}
		})
	}
	if _, err := RunningIn(filepath.Join(base, "no-proc"), install, nil); err == nil {
		t.Error("scanning a missing process table reported no error")
	}
}

// TestRunningInReportsUnreadableProcess checks that unexpected process-entry errors refuse the scan.
func TestRunningInReportsUnreadableProcess(t *testing.T) {
	install := t.TempDir()
	procRoot := t.TempDir()
	entry := filepath.Join(procRoot, "777")
	if err := os.Mkdir(entry, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(entry, "exe"), []byte("not a link"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RunningIn(procRoot, install, nil); err == nil {
		t.Error("scan accepted an unreadable process executable")
	}
}

// fakeCommandLine writes a /proc-style cmdline for pid with NUL-separated args and optional exe and cwd links.
func fakeCommandLine(t *testing.T, procDir, pid, exe, cwd string, args ...string) {
	t.Helper()
	fakeProcess(t, procDir, pid, exe, cwd)
	cmdline := strings.Join(args, "\x00") + "\x00"
	if err := os.WriteFile(filepath.Join(procDir, pid, "cmdline"), []byte(cmdline), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanProcessesInMatchesSteamsLaunchWrapper(t *testing.T) {
	base := t.TempDir()
	install := filepath.Join(base, "Stardew Valley")
	proton := filepath.Join(base, "Proton")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	reaper := filepath.Join(base, "Steam", "ubuntu12_32", "reaper")
	for _, tc := range []struct {
		name   string
		appIDs []int
		args   []string
		want   bool
	}{
		{name: "reaper of the game", appIDs: []int{413150}, args: []string{reaper, "SteamLaunch", "AppId=413150", "--", filepath.Join(proton, "proton"), "waitforexitandrun"}, want: true},
		{name: "reaper of a sibling on the install", appIDs: []int{22380, 22370}, args: []string{reaper, "SteamLaunch", "AppId=22370", "--", "x"}, want: true},
		{name: "reaper of another game", appIDs: []int{413150}, args: []string{reaper, "SteamLaunch", "AppId=489830", "--", "x"}},
		{name: "reaper without SteamLaunch", appIDs: []int{413150}, args: []string{reaper, "AppId=413150"}},
		{name: "app id only after the separator", appIDs: []int{413150}, args: []string{reaper, "SteamLaunch", "AppId=1", "--", "game", "AppId=413150"}},
		{name: "another program with the same arguments", appIDs: []int{413150}, args: []string{filepath.Join(base, "not-reaper"), "SteamLaunch", "AppId=413150"}},
		{name: "no app ids to match", args: []string{reaper, "SteamLaunch", "AppId=413150"}},
		{name: "empty command line", appIDs: []int{413150}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			procDir := filepath.Join(t.TempDir(), "proc")
			fakeCommandLine(t, procDir, "200", filepath.Join(proton, "files", "bin", "wine64"), filepath.Join(proton, "pfx"), tc.args...)
			got, err := RunningIn(procDir, install, tc.appIDs)
			if err != nil || got != tc.want {
				t.Fatalf("scanProcessesIn = %v (%v), want %v", got, err, tc.want)
			}
		})
	}
}

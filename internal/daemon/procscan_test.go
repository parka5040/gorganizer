package daemon

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
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
		{name: "unrelated process", scanDir: install, pid: "100", exe: "/usr/bin/bash", cwd: "/home"},
		{name: "working directory inside", scanDir: install, pid: "101", exe: "/usr/bin/bash", cwd: filepath.Join(install, "Content"), want: true},
		{name: "working directory is the install", scanDir: install, pid: "102", cwd: install, want: true},
		{name: "deleted executable inside", scanDir: install, pid: "103", exe: filepath.Join(install, "StardewModdingAPI") + procDeletedSuffix, cwd: "/", want: true},
		{name: "sibling with a shared prefix", scanDir: install, pid: "104", exe: install + "2/game", cwd: install + " Mods"},
		{name: "the daemon itself", scanDir: install, pid: self, exe: filepath.Join(install, "gorganizerd"), cwd: install},
		{name: "non-process entry", scanDir: install, pid: "self", cwd: install},
		{name: "symlinked install path", scanDir: linked, pid: "105", cwd: filepath.Join(install, "Content"), want: true},
		{name: "unreadable links", scanDir: install, pid: "106"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			procDir := filepath.Join(t.TempDir(), "proc")
			fakeProcess(t, procDir, tc.pid, tc.exe, tc.cwd)
			got, err := scanProcessesIn(procDir, tc.scanDir, nil)
			if err != nil || got != tc.want {
				t.Fatalf("scanProcessesIn = %v (%v), want %v", got, err, tc.want)
			}
		})
	}
	if _, err := scanProcessesIn(filepath.Join(base, "no-proc"), install, nil); err == nil {
		t.Error("scanning a missing process table reported no error")
	}
}

// startSleeper starts path with a long sleep in dir and stops it when the test ends.
func startSleeper(t *testing.T, path, dir string) *exec.Cmd {
	t.Helper()
	var cmd *exec.Cmd
	var err error
	for attempt := 0; attempt < 50; attempt++ {
		cmd = exec.Command(path, "600")
		cmd.Dir = dir
		if err = cmd.Start(); !errors.Is(err, syscall.ETXTBSY) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		if errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EACCES) {
			t.Skipf("cannot execute %s here: %v", path, err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

func TestScanProcessesInFindsARealProcessRunningFromTheInstall(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep is not available")
	}
	install := filepath.Join(t.TempDir(), "Game")
	if err := os.MkdirAll(install, 0755); err != nil {
		t.Fatal(err)
	}
	if running, err := scanProcessesIn(procRoot, install, nil); err != nil || running {
		t.Fatalf("scan before starting = %v (%v), want nothing running", running, err)
	}

	cwdProcess := startSleeper(t, sleep, install)
	if running, err := scanProcessesIn(procRoot, install, nil); err != nil || !running {
		t.Fatalf("scan with a process working in the install = %v (%v), want running", running, err)
	}
	_ = cwdProcess.Process.Kill()
	_ = cwdProcess.Wait()
	if running, err := scanProcessesIn(procRoot, install, nil); err != nil || running {
		t.Fatalf("scan after the process exited = %v (%v), want nothing running", running, err)
	}

	copied := filepath.Join(install, "game-binary")
	src, err := os.Open(sleep)
	if err != nil {
		t.Fatal(err)
	}
	dst, err := os.OpenFile(copied, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	_ = src.Close()
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	startSleeper(t, copied, t.TempDir())
	if running, err := scanProcessesIn(procRoot, install, nil); err != nil || !running {
		t.Fatalf("scan with a process executing from the install = %v (%v), want running", running, err)
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
			got, err := scanProcessesIn(procDir, install, tc.appIDs)
			if err != nil || got != tc.want {
				t.Fatalf("scanProcessesIn = %v (%v), want %v", got, err, tc.want)
			}
		})
	}
}

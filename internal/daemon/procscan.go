package daemon

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	procRoot          = "/proc"
	procDeletedSuffix = " (deleted)"
	steamReaperName   = "reaper"
	steamLaunchVerb   = "SteamLaunch"
	steamAppIDArg     = "AppId="
	maxCmdlineBytes   = 1 << 16
)

var processTableRoot = procRoot

// processRunningIn reports whether a process other than the daemon runs from dir or is Steam's launch wrapper for one of appIDs, using the injected scanner when one is set.
func (s *session) processRunningIn(dir string, appIDs []int) (bool, error) {
	if s.procScan != nil {
		return s.procScan(dir)
	}
	return scanProcessesIn(processTableRoot, dir, appIDs)
}

// scanProcessesIn reports whether any process listed under procDir, other than the daemon itself, has its executable or working directory inside dir, or is Steam's reaper running SteamLaunch for one of appIDs.
func scanProcessesIn(procDir, dir string, appIDs []int) (bool, error) {
	root := filepath.Clean(dir)
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return false, err
	}
	self := os.Getpid()
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || pid == self {
			continue
		}
		for _, link := range []string{"exe", "cwd"} {
			target, err := os.Readlink(filepath.Join(procDir, entry.Name(), link))
			if err != nil {
				continue
			}
			if pathInside(root, strings.TrimSuffix(target, procDeletedSuffix)) {
				return true, nil
			}
		}
		if len(appIDs) > 0 && steamLaunchOf(filepath.Join(procDir, entry.Name(), "cmdline"), appIDs) {
			return true, nil
		}
	}
	return false, nil
}

// steamLaunchOf reports whether the NUL-separated command line at path is Steam's reaper running SteamLaunch for one of appIDs, reading only the reaper's own arguments before "--".
func steamLaunchOf(path string, appIDs []int) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxCmdlineBytes))
	if err != nil || len(data) == 0 {
		return false
	}
	args := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	if filepath.Base(args[0]) != steamReaperName {
		return false
	}
	launch, appID := false, 0
	for _, arg := range args[1:] {
		if arg == "--" {
			break
		}
		if arg == steamLaunchVerb {
			launch = true
		}
		if value, ok := strings.CutPrefix(arg, steamAppIDArg); ok {
			if id, err := strconv.Atoi(value); err == nil {
				appID = id
			}
		}
	}
	if !launch || appID <= 0 {
		return false
	}
	for _, id := range appIDs {
		if id == appID {
			return true
		}
	}
	return false
}

// pathInside reports whether the absolute path target equals root or lies beneath it.
func pathInside(root, target string) bool {
	if !filepath.IsAbs(target) {
		return false
	}
	target = filepath.Clean(target)
	if root == string(filepath.Separator) {
		return true
	}
	return target == root || strings.HasPrefix(target, root+string(filepath.Separator))
}

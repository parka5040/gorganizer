package procscan

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const (
	procDeletedSuffix = " (deleted)"
	steamReaperName   = "reaper"
	steamLaunchVerb   = "SteamLaunch"
	steamAppIDArg     = "AppId="
	maxCmdlineBytes   = 1 << 16
)

// RunningIn reports whether a process listed under procRoot, other than the caller, runs from dir or is Steam's launch wrapper for one of appIDs.
func RunningIn(procRoot, dir string, appIDs []int) (bool, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return false, fmt.Errorf("resolving game install %s: %w", dir, err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false, fmt.Errorf("resolving game install %s: %w", root, err)
	}
	root = resolved
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return false, err
	}
	self := os.Getpid()
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || pid == self {
			continue
		}
		pidDir := filepath.Join(procRoot, entry.Name())
		info, err := os.Stat(pidDir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("checking process %s: %w", entry.Name(), err)
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Getuid() {
			continue
		}
		for _, link := range []string{"exe", "cwd"} {
			target, err := os.Readlink(filepath.Join(procRoot, entry.Name(), link))
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				return false, fmt.Errorf("reading process %s %s: %w", entry.Name(), link, err)
			}
			if pathInside(root, strings.TrimSuffix(target, procDeletedSuffix)) {
				return true, nil
			}
		}
		if len(appIDs) > 0 {
			match, err := steamLaunchOf(filepath.Join(procRoot, entry.Name(), "cmdline"), appIDs)
			if err != nil {
				return false, fmt.Errorf("reading process %s command line: %w", entry.Name(), err)
			}
			if match {
				return true, nil
			}
		}
	}
	return false, nil
}

// steamLaunchOf reports whether the NUL-separated command line at path is Steam's reaper running SteamLaunch for one of appIDs before "--".
func steamLaunchOf(path string, appIDs []int) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxCmdlineBytes))
	if err != nil {
		return false, err
	}
	if len(data) == 0 {
		return false, nil
	}
	args := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	if filepath.Base(args[0]) != steamReaperName {
		return false, nil
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
		return false, nil
	}
	for _, id := range appIDs {
		if id == appID {
			return true, nil
		}
	}
	return false, nil
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

package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/daemon"
	"github.com/parka/gorganizer/internal/gamedef"
	"github.com/parka/gorganizer/internal/instancelock"
	"github.com/parka/gorganizer/internal/procscan"
	"github.com/parka/gorganizer/internal/vfs"
	"golang.org/x/sys/unix"
)

const uninstallRestoreFailure = "Your games could not be fully restored. Gorganizer and your files have been kept."
const uninstallRestoredMessage = "Your games are back to their original state. Gorganizer will set your mods up again the next time you play."

var errUninstallForeignDesktop = errors.New("desktop entry belongs to another copy of Gorganizer")

type uninstallDeps struct {
	in       io.Reader
	out      io.Writer
	errOut   io.Writer
	procRoot string
	isTTY    bool
	remove   func(string) error
	launcher string
}

type uninstallPath struct {
	path     string
	base     string
	name     string
	file     bool
	launcher string
}

// runUninstall restores configured games and removes only verified Gorganizer paths.
func runUninstall(args []string) int {
	_, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TCGETS)
	return runUninstallWith(args, uninstallDeps{in: os.Stdin, out: os.Stdout, errOut: os.Stderr, procRoot: "/proc", isTTY: err == nil, remove: os.RemoveAll})
}

// runUninstallWith performs the offline checks with injected process data and deletion.
func runUninstallWith(args []string, deps uninstallDeps) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(deps.errOut)
	keep := fs.Bool("keep-data", false, "keep settings, profiles, mods and downloads")
	purge := fs.Bool("purge", false, "also remove settings, profiles, mods and downloads")
	yes := fs.Bool("yes", false, "confirm the first removal prompt")
	check := fs.Bool("check", false, "check that every game is already restored without changing anything")
	forgetMissing := fs.Bool("forget-missing-games", false, "permit purge when a configured game is no longer installed")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *keep && *purge || *check && (*keep || *purge || *yes || *forgetMissing) || *forgetMissing && !*purge {
		fmt.Fprintln(deps.errOut, "Choose either --keep-data or --purge, not both. --forget-missing-games requires --purge. --check cannot be combined with removal options.")
		return 2
	}
	release, err := instancelock.Acquire()
	if errors.Is(err, instancelock.ErrHeld) {
		fmt.Fprintln(deps.errOut, "Close Gorganizer first, then run this again. Nothing has been deleted.")
		return 1
	}
	if err != nil {
		fmt.Fprintf(deps.errOut, "Cannot check Gorganizer: %v. Nothing has been deleted.\n", err)
		return 1
	}
	defer release()
	releaseSession, err := acquireSessionLock()
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		fmt.Fprintln(deps.errOut, "Close Gorganizer first, then run this again. Nothing has been deleted.")
		return 1
	}
	if err != nil {
		fmt.Fprintf(deps.errOut, "Cannot safely check the Gorganizer session: %v. Nothing has been deleted.\n", err)
		return 1
	}
	defer releaseSession()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(deps.errOut, "%s %v\n", uninstallRestoreFailure, err)
		return 1
	}
	targets, err := uninstallTargets(cfg)
	if err != nil {
		fmt.Fprintf(deps.errOut, "%s %v\n", uninstallRestoreFailure, err)
		return 1
	}
	available := make([]recoveryTarget, 0, len(targets))
	for _, target := range targets {
		if err := uninstallRealAncestors(filepath.Dir(target.installPath)); err != nil {
			fmt.Fprintf(deps.errOut, "%s %s: %v\n", uninstallRestoreFailure, target.name, err)
			return 1
		}
		info, err := os.Lstat(target.installPath)
		if errors.Is(err, os.ErrNotExist) {
			if *purge && !*forgetMissing {
				fmt.Fprintf(deps.errOut, "%s cannot be found at %s. Connect the drive that holds it, or add --forget-missing-games to remove Gorganizer without restoring it. Nothing has been deleted.\n", target.name, target.installPath)
				return 1
			}
			fmt.Fprintf(deps.out, "Note: %s is not installed at %s any more, so there is nothing to restore.\n", target.name, target.installPath)
			continue
		}
		if err != nil || !info.IsDir() {
			fmt.Fprintf(deps.errOut, "%s %s: the install folder is not a plain directory: %v\n", uninstallRestoreFailure, target.name, err)
			return 1
		}
		if err := uninstallRealAncestors(filepath.Dir(target.dataPath)); err != nil {
			fmt.Fprintf(deps.errOut, "%s %s: %v\n", uninstallRestoreFailure, target.name, err)
			return 1
		}
		running, scanErr := procscan.RunningIn(deps.procRoot, target.installPath, target.appIDs)
		if running || scanErr != nil || daemon.LaunchTicketBlocksRecovery(target.dataPath, time.Now()) != "" {
			fmt.Fprintf(deps.errOut, "Close %s before removing Gorganizer. Nothing has been deleted.\n", target.name)
			return 1
		}
		available = append(available, target)
	}
	targets = available
	if !*check {
		fmt.Fprintln(deps.out, "Restoring your games to their original state first…")
	}
	for _, target := range targets {
		if !*check {
			report, recoveryErr := daemon.RecoverGameOffline(cfg, target.gameID)
			if recoveryErr != nil || report.Loader.Pending != "" || report.Root.Pending != "" || report.Data.Pending != "" {
				reasons := make([]string, 0, 4)
				if recoveryErr != nil {
					reasons = append(reasons, recoveryErr.Error())
				}
				for _, step := range []struct{ name, reason string }{{"SMAPI", report.Loader.Pending}, {"Game-root files", report.Root.Pending}, {"Data folder", report.Data.Pending}} {
					if step.reason != "" {
						reasons = append(reasons, step.name+": "+step.reason)
					}
				}
				fmt.Fprintf(deps.errOut, "%s %s: %s\n", uninstallRestoreFailure, target.name, strings.Join(reasons, "; "))
				return 1
			}
		}
		if err := verifyUninstallTarget(target, !*check); err != nil {
			fmt.Fprintf(deps.errOut, "%s %s: %v\n", uninstallRestoreFailure, target.name, err)
			return 1
		}
	}
	if !*check {
		for _, target := range targets {
			if err := uninstallClearLaunchTicket(target.dataPath); err != nil {
				fmt.Fprintf(deps.errOut, "%s %s: %v\n", uninstallRestoreFailure, target.name, err)
				return 1
			}
			if err := verifyUninstallTarget(target, false); err != nil {
				fmt.Fprintf(deps.errOut, "%s %s: %v\n", uninstallRestoreFailure, target.name, err)
				return 1
			}
		}
	}
	if *check {
		fmt.Fprintln(deps.out, "All configured games are restored. Nothing has been deleted.")
		return 0
	}

	paths, err := uninstallPaths(*purge, deps.launcher)
	if err != nil {
		fmt.Fprintf(deps.errOut, "Cannot safely remove Gorganizer: %v. Nothing has been deleted.\n", err)
		return 1
	}
	validated := make([]uninstallPath, 0, len(paths))
	foreignNXM := false
	for _, path := range paths {
		if err := validateUninstallPath(path); errors.Is(err, errUninstallForeignDesktop) {
			fmt.Fprintf(deps.out, "Kept %s because it belongs to another copy of Gorganizer.\n", path.path)
			if path.name == "gorganizer-nxm.desktop" {
				foreignNXM = true
			}
			continue
		} else if err != nil {
			fmt.Fprintf(deps.errOut, "Cannot safely remove Gorganizer: %v. Nothing has been deleted.\n", err)
			return 1
		}
		validated = append(validated, path)
	}
	paths = paths[:0]
	for _, path := range validated {
		if foreignNXM && path.name == "mimeapps.list" {
			continue
		}
		paths = append(paths, path)
	}
	fmt.Fprintln(deps.out, "Remove these Gorganizer files and folders:")
	for _, path := range paths {
		if path.name == "mimeapps.list" {
			fmt.Fprintln(deps.out, "  NXM handler in "+path.path)
		} else {
			fmt.Fprintln(deps.out, "  "+path.path)
		}
	}
	if !*purge {
		fmt.Fprintln(deps.out, "Your settings, profiles, mods and downloads will be kept.")
	}
	fmt.Fprintln(deps.out, "SMAPI stays installed. If you want to remove it, use Steam's Verify installed files.")
	deps.in = bufio.NewReader(deps.in)
	if !*yes && !uninstallConfirm(deps, "Remove these? [y/N] ") {
		fmt.Fprintln(deps.errOut, "Nothing has been deleted. Run again with --yes to confirm without a terminal.")
		fmt.Fprintln(deps.out, uninstallRestoredMessage)
		return 1
	}
	if *purge {
		data := config.DataDir()
		size, err := uninstallFolderSize(data)
		if err != nil {
			fmt.Fprintf(deps.errOut, "Cannot measure your mods and downloads: %v. Nothing has been deleted.\n", err)
			return 1
		}
		fmt.Fprintf(deps.out, "This deletes your mods and downloads (%s). This cannot be undone.\n", readableSize(size))
		ids := make([]string, 0, len(cfg.Games))
		for id := range cfg.Games {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			mods := config.XDGModsDir(id)
			modSize, modErr := uninstallFolderSize(mods)
			downloadSize, downloadErr := uninstallFolderSize(filepath.Join(mods, "Downloads"))
			if modErr != nil || downloadErr != nil {
				fmt.Fprintf(deps.errOut, "Cannot measure %s's mods or downloads: %v %v. Nothing has been deleted.\n", gameName(id, cfg.Games[id].Name), modErr, downloadErr)
				return 1
			}
			fmt.Fprintf(deps.out, "  %s: mods %s (including downloads %s)\n", gameName(id, cfg.Games[id].Name), readableSize(modSize), readableSize(downloadSize))
		}
		if !uninstallConfirm(deps, "Type 'delete my mods' to confirm: ", "delete my mods") {
			fmt.Fprintln(deps.errOut, "Nothing has been deleted. The second confirmation requires a terminal.")
			fmt.Fprintln(deps.out, uninstallRestoredMessage)
			return 1
		}
	}
	if deps.remove == nil {
		deps.remove = os.RemoveAll
	}
	for _, path := range paths {
		if err := validateUninstallPath(path); errors.Is(err, errUninstallForeignDesktop) {
			fmt.Fprintf(deps.out, "Kept %s because it belongs to another copy of Gorganizer.\n", path.path)
			continue
		} else if err != nil {
			fmt.Fprintf(deps.errOut, "Removal stopped: %v\n", err)
			return 1
		}
		var err error
		if path.name == "mimeapps.list" {
			err = uninstallNXMAssociation(path.path)
		} else {
			err = deps.remove(path.path)
		}
		if err != nil {
			fmt.Fprintf(deps.errOut, "Removal stopped at %s: %v\n", path.path, err)
			return 1
		}
	}
	fmt.Fprintln(deps.out, "Gorganizer was removed. Your games are restored.")
	return 0
}

// uninstallConfirm reads one explicit confirmation from an interactive terminal.
func uninstallConfirm(deps uninstallDeps, prompt string, answer ...string) bool {
	if !deps.isTTY {
		return false
	}
	fmt.Fprint(deps.out, prompt)
	line, err := deps.in.(*bufio.Reader).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	want := "y"
	if len(answer) != 0 {
		want = answer[0]
	}
	return strings.TrimSpace(line) == want
}

// uninstallTargets collects one recovery target per physical deploy folder.
func uninstallTargets(cfg *config.Config) ([]recoveryTarget, error) {
	ids := make([]string, 0, len(cfg.Games))
	for id := range cfg.Games {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return cfg.Games[ids[i]].LinkedFromGameID == "" && cfg.Games[ids[j]].LinkedFromGameID != "" ||
			(cfg.Games[ids[i]].LinkedFromGameID == "") == (cfg.Games[ids[j]].LinkedFromGameID == "") && ids[i] < ids[j]
	})
	var targets []recoveryTarget
	byPath := make(map[string]int)
	for _, id := range ids {
		gc, err := cfg.EffectiveGameConfig(id)
		if err != nil || gc.InstallPath == "" || !filepath.IsAbs(gc.InstallPath) || filepath.Clean(gc.InstallPath) != gc.InstallPath {
			return nil, fmt.Errorf("cannot find the installation for %s: %v", id, err)
		}
		dataPath := filepath.Join(gc.InstallPath, dataSubpath(gc))
		if !strings.HasPrefix(dataPath, gc.InstallPath+string(os.PathSeparator)) {
			return nil, fmt.Errorf("invalid Data folder for %s", id)
		}
		if n, ok := byPath[dataPath]; ok {
			targets[n].appIDs = appendAppID(targets[n].appIDs, gc.SteamAppID)
			continue
		}
		byPath[dataPath] = len(targets)
		targets = append(targets, recoveryTarget{config: cfg, name: gameName(id, cfg.Games[id].Name), gameID: id, dataPath: dataPath, installPath: gc.InstallPath, appIDs: appendAppID(nil, gc.SteamAppID)})
	}
	return targets, nil
}

// verifyUninstallTarget checks that Data and its game-root state no longer contain a deployment.
func verifyUninstallTarget(target recoveryTarget, allowStaleTicket bool) error {
	info, err := os.Lstat(target.dataPath)
	if errors.Is(err, os.ErrNotExist) {
		definition, ok := gamedef.ByID(target.gameID)
		if !ok || !definition.DataDirOptional {
			return fmt.Errorf("the Data folder is missing")
		}
	} else if err != nil || !info.IsDir() {
		return fmt.Errorf("the Data folder is not a plain directory: %v", err)
	} else if _, err := os.Lstat(filepath.Join(target.dataPath, vfs.SentinelFilename)); err == nil {
		return fmt.Errorf("the Data folder is still deployed")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("checking Data: %w", err)
	}
	for _, suffix := range append(vfs.FarmSiblingSuffixes(), vfs.RetainedSessionSiblingSuffix, ".gorganizer-maintenance") {
		if allowStaleTicket && suffix == vfs.RetainedSessionSiblingSuffix {
			continue
		}
		if _, err := os.Lstat(target.dataPath + suffix); err == nil {
			return fmt.Errorf("a game recovery marker remains (%s)", suffix)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("checking game recovery: %w", err)
		}
	}
	for _, name := range []string{vfs.RootManifestFilename, vfs.RootIntentFilename, ".gorganizer-modloader-intent.json"} {
		if _, err := os.Lstat(filepath.Join(target.installPath, name)); err == nil {
			return fmt.Errorf("a game-root recovery marker remains (%s)", name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("checking game-root recovery: %w", err)
		}
	}
	return nil
}

// uninstallClearLaunchTicket removes an old launch record after its game has been physically restored.
func uninstallClearLaunchTicket(dataPath string) error {
	path := dataPath + vfs.RetainedSessionSiblingSuffix
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking the game launch record: %w", err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || int(st.Uid) != os.Getuid() || st.Nlink != 1 || daemon.LaunchTicketBlocksRecovery(dataPath, time.Now()) != "" {
		return fmt.Errorf("the game launch record cannot safely be removed")
	}
	if err := atomicfile.RemoveDurable(path); err != nil {
		return fmt.Errorf("removing the old game launch record: %w", err)
	}
	return nil
}

// uninstallPaths lists the exact user paths that may be removed.
func uninstallPaths(purge bool, launcher string) ([]uninstallPath, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("finding your home folder: %w", err)
	}
	dataBase := os.Getenv("XDG_DATA_HOME")
	if dataBase == "" {
		dataBase = filepath.Join(home, ".local", "share")
	}
	configBase := os.Getenv("XDG_CONFIG_HOME")
	if configBase == "" {
		configBase = filepath.Join(home, ".config")
	}
	stateBase := os.Getenv("XDG_STATE_HOME")
	if stateBase == "" {
		stateBase = filepath.Join(home, ".local", "state")
	}
	runtime := config.RuntimeDir()
	runtimeBase := filepath.Dir(runtime)
	paths := []uninstallPath{
		{filepath.Join(dataBase, "applications", "gorganizer.desktop"), filepath.Join(dataBase, "applications"), "gorganizer.desktop", true, ""},
		{filepath.Join(dataBase, "applications", "gorganizer-nxm.desktop"), filepath.Join(dataBase, "applications"), "gorganizer-nxm.desktop", true, ""},
		{filepath.Join(dataBase, "icons", "hicolor", "256x256", "apps", "gorganizer.png"), filepath.Join(dataBase, "icons", "hicolor", "256x256", "apps"), "gorganizer.png", true, ""},
		{filepath.Join(stateBase, "gorganizer"), stateBase, "gorganizer", false, ""},
	}
	if purge {
		paths = append(paths, uninstallPath{filepath.Join(dataBase, "gorganizer"), dataBase, "gorganizer", false, ""}, uninstallPath{filepath.Join(configBase, "gorganizer"), configBase, "gorganizer", false, ""})
	}
	paths = append(paths, uninstallPath{filepath.Join(configBase, "mimeapps.list"), configBase, "mimeapps.list", true, ""})
	paths = append(paths, uninstallPath{runtime, runtimeBase, filepath.Base(runtime), false, ""})
	if launcher == "" {
		executable, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("finding the Gorganizer launcher: %w", err)
		}
		launcher = filepath.Join(filepath.Dir(executable), "gorganizer.sh")
	}
	paths[0].launcher = launcher
	paths[1].launcher = launcher
	return paths, nil
}

// uninstallRealAncestors checks every existing folder component without following links.
func uninstallRealAncestors(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("unsafe folder: %s", path)
	}
	for ancestor := path; ; ancestor = filepath.Dir(ancestor) {
		info, err := os.Lstat(ancestor)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("checking %s: %w", ancestor, err)
		}
		if err == nil && !info.IsDir() {
			return fmt.Errorf("%s is not a real folder", ancestor)
		}
		if ancestor == string(os.PathSeparator) {
			break
		}
	}
	return nil
}

// validateUninstallPath rejects symlinks and unexpected names or owners before deletion.
func validateUninstallPath(path uninstallPath) error {
	if !filepath.IsAbs(path.base) || filepath.Clean(path.base) != path.base || path.path != filepath.Join(path.base, path.name) || filepath.Dir(path.path) != path.base || path.name == "." || path.name == ".." {
		return fmt.Errorf("unsafe path: %s", path.path)
	}
	if err := uninstallRealAncestors(path.base); err != nil {
		return err
	}
	info, err := os.Lstat(path.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking %s: %w", path.path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() || info.Mode()&os.ModeSymlink != 0 || path.file && !info.Mode().IsRegular() || !path.file && !info.IsDir() {
		return fmt.Errorf("%s is not a Gorganizer-owned %s", path.path, map[bool]string{true: "file", false: "folder"}[path.file])
	}
	if path.launcher != "" {
		body, err := os.ReadFile(path.path)
		if err != nil {
			return fmt.Errorf("reading desktop entry: %w", err)
		}
		action, name := "launch", "Gorganizer"
		if path.name == "gorganizer-nxm.desktop" {
			action, name = "nxm %u", "Gorganizer NXM Handler"
		}
		if !strings.Contains("\n"+string(body), "\nExec="+path.launcher+" "+action+"\n") || !strings.Contains("\n"+string(body), "\nName="+name+"\n") {
			return errUninstallForeignDesktop
		}
	}
	if path.path == config.RuntimeDir() {
		entries, err := os.ReadDir(path.path)
		if err != nil {
			return fmt.Errorf("checking the runtime folder: %w", err)
		}
		for _, entry := range entries {
			child, err := os.Lstat(filepath.Join(path.path, entry.Name()))
			if err != nil {
				return fmt.Errorf("checking the runtime folder: %w", err)
			}
			owner, ok := child.Sys().(*syscall.Stat_t)
			if !ok || int(owner.Uid) != os.Getuid() ||
				(entry.Name() != "gorganizerd.lock" && entry.Name() != "session.lock" && entry.Name() != "nxm-start.lock" && entry.Name() != "gorganizer.sock") ||
				(entry.Name() == "gorganizer.sock" && child.Mode()&os.ModeSocket == 0) ||
				(entry.Name() != "gorganizer.sock" && !child.Mode().IsRegular()) {
				return fmt.Errorf("unexpected file in Gorganizer's runtime folder: %s", entry.Name())
			}
		}
	}
	return nil
}

// uninstallNXMAssociation removes only Gorganizer's exact handler from the user's MIME settings.
func uninstallNXMAssociation(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking NXM settings: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 4*1024*1024 {
		return fmt.Errorf("NXM settings are not a regular, reasonably sized file")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading NXM settings: %w", err)
	}
	lines := strings.Split(string(body), "\n")
	kept := make([]string, 0, len(lines))
	changed := false
	for _, line := range lines {
		if line == "x-scheme-handler/nxm=gorganizer-nxm.desktop" {
			changed = true
			continue
		}
		kept = append(kept, line)
	}
	if !changed {
		return nil
	}
	if err := atomicfile.WriteFile(path, []byte(strings.Join(kept, "\n")), info.Mode().Perm()); err != nil {
		return fmt.Errorf("updating NXM settings: %w", err)
	}
	return nil
}

// uninstallFolderSize totals real files in a data folder without following symlinks.
func uninstallFolderSize(path string) (uint64, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("%s is not a real folder", path)
	}
	var total uint64
	err = filepath.WalkDir(path, func(_ string, entry fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += uint64(info.Size())
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	return total, err
}

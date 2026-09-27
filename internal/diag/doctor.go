package diag

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/gamedef"
	"github.com/parka/gorganizer/internal/migrate"
	"github.com/parka/gorganizer/internal/procscan"
	"github.com/parka/gorganizer/internal/steam"
	"github.com/parka/gorganizer/internal/vfs"
)

type Health struct {
	Version    string
	InstanceID string
	Stopping   bool
}

type Options struct {
	Version        string
	Executable     string
	Checkout       string
	ProcRoot       string
	Health         func(context.Context) (Health, error)
	BinaryVersion  func(context.Context, string) (string, error)
	FindSteamRoots func() ([]steam.Root, error)
	LookPath       func(string) (string, error)
}

type Check struct {
	Status  string
	Name    string
	Message string
}

type Report struct {
	Checks []Check
}

// Text renders each diagnostic check and the overall result.
func (r Report) Text() string {
	var out strings.Builder
	problems := 0
	for _, check := range r.Checks {
		fmt.Fprintf(&out, "%s — %s: %s\n", check.Status, oneLine(check.Name), oneLine(check.Message))
		if check.Status == "Problem" {
			problems++
		}
	}
	if problems == 0 {
		out.WriteString("Summary: No problems found.\n")
	} else {
		fmt.Fprintf(&out, "Summary: %d problem(s) found.\n", problems)
	}
	return out.String()
}

// oneLine replaces control characters so a check always takes one report line.
func oneLine(text string) string {
	return strings.Map(func(ch rune) rune {
		if ch < ' ' || ch == 127 {
			return ' '
		}
		return ch
	}, text)
}

// HasProblems reports whether any check needs attention.
func (r Report) HasProblems() bool {
	for _, check := range r.Checks {
		if check.Status == "Problem" {
			return true
		}
	}
	return false
}

// Run checks local settings, installation state, and the daemon without changing files.
func Run(opts Options) Report {
	r := Report{}
	add := func(status, name, message string) {
		r.Checks = append(r.Checks, Check{status, name, message})
	}
	if opts.ProcRoot == "" {
		opts.ProcRoot = "/proc"
	}
	if opts.BinaryVersion == nil {
		opts.BinaryVersion = binaryVersion
	}
	if opts.FindSteamRoots == nil {
		opts.FindSteamRoots = steam.FindRoots
	}
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	add("OK", "Command version", "gorganizerctl "+opts.Version)
	checkBinaries(&r, opts)
	if opts.Checkout != "" {
		if info, err := os.Stat(filepath.Join(opts.Checkout, "go.mod")); err == nil && info.Mode().IsRegular() {
			add("Note", "Layout", "Running from a source checkout.")
		} else {
			add("Note", "Layout", "Running from an installed copy.")
		}
	} else {
		add("Note", "Layout", "Running from an installed copy.")
	}
	checkHealth(&r, opts)
	checkDir(&r, "Settings folder", config.ConfigDir())
	checkDir(&r, "Personal data folder", config.DataDir())
	checkDir(&r, "Logs and state folder", StateDir())
	checkRuntime(&r, config.RuntimeDir())
	cfg := config.DefaultConfig()
	configInfo, configErr := os.Lstat(filepath.Join(config.ConfigDir(), "config.json"))
	if configErr == nil && (configInfo.Mode().Perm() != 0o600 || !configInfo.Mode().IsRegular()) {
		add("Problem", "Settings privacy", "Settings are not in a private file. Set config.json permissions to 0600 and use a regular file.")
	} else if configErr != nil && !errors.Is(configErr, os.ErrNotExist) {
		add("Problem", "Settings privacy", "Could not check settings permissions. Check access to the settings folder.")
	}
	if (configErr == nil && configInfo.Mode().IsRegular()) || errors.Is(configErr, os.ErrNotExist) {
		loaded, err := config.Load()
		if err == nil {
			cfg = loaded
			add("OK", "Game settings", "Settings can be read.")
		} else {
			add("Problem", "Game settings", "Could not read settings. Check the settings file and its permissions, then try again.")
		}
	} else {
		add("Problem", "Game settings", "Settings could not be read safely. Replace the settings link or file with a regular file.")
	}
	checkMigration(&r, opts.Checkout)
	ids := make([]string, 0, len(cfg.Games))
	for id := range cfg.Games {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		checkGame(&r, cfg, id, opts.ProcRoot)
	}
	checkSteam(&r, opts, cfg, ids)
	for _, tool := range []string{"protontricks", "7z", "unrar", "notify-send"} {
		if _, err := opts.LookPath(tool); err == nil {
			add("OK", "Optional tool "+tool, "Available.")
		} else {
			add("Note", "Optional tool "+tool, "Not installed; this check does not affect Gorganizer.")
		}
	}
	return r
}

// StateDir returns the folder used for the session's logs and state.
func StateDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "gorganizer")
}

// binaryVersion asks a nearby executable for its version without opening its normal interface.
func binaryVersion(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", err
	}
	if len(out) > 512 {
		return "", fmt.Errorf("version response is too long")
	}
	return strings.TrimSpace(string(out)), nil
}

// checkBinaries checks only executables located alongside the maintenance command.
func checkBinaries(r *Report, opts Options) {
	add := func(status, name, message string) { r.Checks = append(r.Checks, Check{status, name, message}) }
	if opts.Executable == "" {
		add("Note", "Other programs", "Could not locate the maintenance command.")
		return
	}
	windowFound := false
	for _, binary := range []struct{ name, path, prefix string }{
		{"Background service", filepath.Join(filepath.Dir(opts.Executable), "gorganizerd"), "gorganizerd "},
		{"Window", filepath.Join(filepath.Dir(opts.Executable), "build", "src", "gorganizer"), "gorganizer-gui "},
		{"Window", filepath.Join(filepath.Dir(opts.Executable), "gorganizer"), "gorganizer-gui "},
	} {
		if binary.name == "Window" && windowFound {
			continue
		}
		info, err := os.Lstat(binary.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if binary.name == "Window" {
			windowFound = true
		}
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			add("Note", binary.name+" version", "Could not read the nearby program's version.")
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		text, err := opts.BinaryVersion(ctx, binary.path)
		cancel()
		if err != nil || !strings.HasPrefix(text, binary.prefix) {
			add("Note", binary.name+" version", "The nearby program did not report a version.")
			continue
		}
		add("OK", binary.name+" version", text)
	}
}

// checkHealth reports the daemon's availability and its version agreement.
func checkHealth(r *Report, opts Options) {
	add := func(status, name, message string) { r.Checks = append(r.Checks, Check{status, name, message}) }
	if opts.Health == nil {
		add("Note", "Background service", "Not running.")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	response, err := opts.Health(ctx)
	if err != nil {
		add("Note", "Background service", "Not running.")
		return
	}
	if response.Stopping {
		add("Note", "Background service", "Shutting down (instance "+response.InstanceID+").")
	} else {
		add("OK", "Background service", "Running (instance "+response.InstanceID+", version "+response.Version+").")
	}
	if response.Version != opts.Version {
		add("Problem", "Version agreement", "The background service and command have different versions. Restart Gorganizer with matching programs.")
	} else {
		add("OK", "Version agreement", "Background service and command versions match.")
	}
}

// checkDir checks whether a diagnostic location exists without creating it.
func checkDir(r *Report, name, path string) {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		r.Checks = append(r.Checks, Check{"Note", name, path + " has not been created yet."})
	case err != nil || !info.IsDir():
		r.Checks = append(r.Checks, Check{"Problem", name, "Cannot access " + path + ". Check that this location is a readable folder."})
	default:
		r.Checks = append(r.Checks, Check{"OK", name, path})
	}
}

// checkRuntime checks ownership and private permissions of the runtime folder.
func checkRuntime(r *Report, path string) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		r.Checks = append(r.Checks, Check{"Note", "Runtime folder", "Not present while Gorganizer is closed."})
		return
	}
	if err != nil {
		r.Checks = append(r.Checks, Check{"Problem", "Runtime folder", "Could not inspect the runtime folder. Check access to the runtime location."})
		return
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode()&(os.ModePerm|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0o700 || !ok || owner.Uid != uint32(os.Getuid()) {
		r.Checks = append(r.Checks, Check{"Problem", "Runtime folder", "The runtime folder must be owned by you and have 0700 permissions. Close Gorganizer and fix its ownership and permissions before starting it again."})
		return
	}
	r.Checks = append(r.Checks, Check{"OK", "Runtime folder", "Private and owned by you."})
}

// checkMigration looks for an unfinished move and legacy folders without reading their contents.
func checkMigration(r *Report, checkout string) {
	pending, err := migrate.JournalExists()
	switch {
	case err != nil:
		r.Checks = append(r.Checks, Check{"Problem", "Mod folder move", "Could not check for an unfinished move. Check access to your personal data folder."})
	case pending:
		r.Checks = append(r.Checks, Check{"Problem", "Mod folder move", "An unfinished move is waiting. Close Gorganizer and run gorganizerctl migrate-data --resume."})
	default:
		r.Checks = append(r.Checks, Check{"OK", "Mod folder move", "No unfinished move was found."})
	}
	if checkout == "" {
		return
	}
	var old []string
	for id, name := range config.AllModsDirNames() {
		if name == "" {
			continue
		}
		if _, err := os.Lstat(filepath.Join(checkout, name)); err == nil {
			old = append(old, name+" → "+config.XDGModsDir(id))
		} else if !errors.Is(err, os.ErrNotExist) {
			r.Checks = append(r.Checks, Check{"Problem", "Old mod folders", "Could not check old mod folders. Check access to the source checkout."})
			return
		}
	}
	sort.Strings(old)
	if len(old) == 0 {
		r.Checks = append(r.Checks, Check{"OK", "Old mod folders", "No old mod folders were found."})
	}
	for _, item := range old {
		r.Checks = append(r.Checks, Check{"Note", "Old mod folder", item + ". Preview the move with gorganizerctl migrate-data --from <checkout> --dry-run."})
	}
}

// checkGame inspects a configured installation using metadata only.
func checkGame(r *Report, cfg *config.Config, id, procRoot string) {
	label := "Game " + cfg.Games[id].Name
	if cfg.Games[id].Name == "" {
		label = "Game " + id
	}
	if !filepath.IsLocal(id) || filepath.Base(id) != id {
		r.Checks = append(r.Checks, Check{"Problem", label + " settings", "The game has an invalid identifier. Remove it from settings and configure the game again."})
		return
	}
	mods := checkMods(r, label, id)
	gc, err := cfg.EffectiveGameConfig(id)
	if err != nil || gc.InstallPath == "" {
		r.Checks = append(r.Checks, Check{"Problem", label + " install", "No usable game folder is configured. Set the game location in Gorganizer."})
		r.Checks = append(r.Checks, Check{"Note", label + " Data", "Cannot check until the game folder is configured."})
		r.Checks = append(r.Checks, Check{"Note", label + " process", "Cannot check until the game folder is configured."})
		r.Checks = append(r.Checks, Check{"Note", label + " drive", "Cannot compare drives until the game folder is configured."})
		return
	}
	info, err := os.Stat(gc.InstallPath)
	if err != nil || !info.IsDir() {
		r.Checks = append(r.Checks, Check{"Problem", label + " install", "The game folder is missing or unavailable. Check the game location in Gorganizer."})
		r.Checks = append(r.Checks, Check{"Note", label + " Data", "Cannot check until the game folder is available."})
		r.Checks = append(r.Checks, Check{"Note", label + " process", "Cannot check until the game folder is available."})
		r.Checks = append(r.Checks, Check{"Note", label + " drive", "Cannot compare drives until the game folder is available."})
		return
	}
	r.Checks = append(r.Checks, Check{"OK", label + " install", gc.InstallPath})
	dataSubpath := gc.DataSubpath
	if dataSubpath == "" {
		if def, ok := gamedef.ByID(id); ok {
			dataSubpath = def.DataSubpath
		}
		if dataSubpath == "" {
			dataSubpath = "Data"
		}
	}
	data := filepath.Join(gc.InstallPath, dataSubpath)
	checkData(r, label, data, id)
	appIDs := []int{}
	if gc.SteamAppID > 0 {
		appIDs = append(appIDs, gc.SteamAppID)
	}
	if parent := cfg.Games[id].LinkedFromGameID; parent != "" && cfg.Games[parent].SteamAppID > 0 {
		appIDs = append(appIDs, cfg.Games[parent].SteamAppID)
	}
	running, err := procscan.RunningIn(procRoot, gc.InstallPath, appIDs)
	switch {
	case err != nil:
		r.Checks = append(r.Checks, Check{"Problem", label + " process", "Could not check if the game is running. Check access to the process list, then try again."})
	case running:
		r.Checks = append(r.Checks, Check{"Note", label + " process", "The game is running."})
	default:
		r.Checks = append(r.Checks, Check{"Note", label + " process", "The game is not running."})
	}
	modsDev, modsErr := filesystem(mods)
	dataDev, dataErr := filesystem(data)
	if modsErr != nil || dataErr != nil {
		r.Checks = append(r.Checks, Check{"Note", label + " drive", "Cannot compare the mods and Data drives until both folders exist."})
	} else if modsDev != dataDev {
		r.Checks = append(r.Checks, Check{"Note", label + " drive", "Mods and Data are on different drives; Gorganizer will use links instead of hardlinks."})
	} else {
		r.Checks = append(r.Checks, Check{"OK", label + " drive", "Mods and Data are on the same drive; hardlinks are available."})
	}
}

// checkMods measures a game's mods folder using only file metadata.
func checkMods(r *Report, label, id string) string {
	mods := config.ModsDir(id)
	if info, err := os.Lstat(mods); err == nil && info.Mode()&os.ModeSymlink != 0 {
		r.Checks = append(r.Checks, Check{"Note", label + " mods", "The mods folder is a link; its size was not measured."})
		return mods
	}
	bytes, err := folderSize(mods)
	if errors.Is(err, os.ErrNotExist) {
		r.Checks = append(r.Checks, Check{"Note", label + " mods", "No mods folder exists yet: " + mods})
	} else if err != nil {
		r.Checks = append(r.Checks, Check{"Problem", label + " mods", "Could not measure the mods folder. Check that it is a readable folder."})
	} else {
		r.Checks = append(r.Checks, Check{"OK", label + " mods", fmt.Sprintf("%s (%s)", mods, sizeText(bytes))})
	}
	return mods
}

// checkData classifies the Data folder and adjacent markers without opening game files.
func checkData(r *Report, label, data, gameID string) {
	add := func(status, name, message string) {
		r.Checks = append(r.Checks, Check{status, label + " " + name, message})
	}
	unfinished := false
	backup := false
	for _, suffix := range vfs.FarmSiblingSuffixes() {
		present, err := exists(data + suffix)
		if err != nil {
			add("Problem", "Data", "Could not check an unfinished change. Check access to the game folder.")
			unfinished = true
			break
		}
		if suffix == ".orig" {
			backup = present
		} else if present {
			add("Problem", "Data", "An unfinished change is present. Close the game and run gorganizerctl recover --game "+gameID+".")
			unfinished = true
			break
		}
	}
	if !unfinished {
		checkDataState(r, label, data, gameID, backup)
	}
	if present, err := exists(vfs.PreservedDir(data)); err != nil {
		add("Problem", "preserved files", "Could not check preserved files. Check access to the game folder.")
	} else if present {
		add("Note", "preserved files", "Preserved files are present. Review them in Gorganizer before clearing anything.")
	} else {
		add("OK", "preserved files", "No preserved batches were found.")
	}
}

// checkDataState reports the Data folder's farm, maintenance, backup, or plain state.
func checkDataState(r *Report, label, data, gameID string, backup bool) {
	add := func(status, message string) { r.Checks = append(r.Checks, Check{status, label + " Data", message}) }
	maintenance, err := exists(vfs.MaintenancePath(data))
	if err != nil {
		add("Problem", "Could not check the maintenance marker. Check access to the game folder.")
		return
	}
	if maintenance {
		add("Problem", "Maintenance is pending. Open Gorganizer and review the game's maintenance notice before using mods.")
		return
	}
	farm, err := exists(filepath.Join(data, vfs.SentinelFilename))
	if err != nil {
		add("Problem", "Could not check the Data folder. Check access to the game folder.")
		return
	}
	if farm {
		add("OK", "Mod farm active.")
		return
	}
	if backup {
		add("Problem", "A Data backup exists without an active mod farm. Close the game and run gorganizerctl recover --game "+gameID+".")
		return
	}
	info, err := os.Lstat(data)
	if err == nil && info.IsDir() {
		add("OK", "Plain game files.")
	} else if errors.Is(err, os.ErrNotExist) {
		if def, ok := gamedef.ByID(gameID); ok && def.DataDirOptional {
			add("Note", "This game does not need a Data folder.")
		} else {
			add("Problem", "The Data folder is missing. Check the game installation, then run gorganizerctl recover --game "+gameID+" if a change was interrupted.")
		}
	} else {
		add("Problem", "The Data folder cannot be used. Check the game installation and its permissions.")
	}
}

// exists checks a marker without following a symbolic link.
func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// folderSize totals regular file sizes without following symbolic links or reading file contents.
func folderSize(root string) (int64, error) {
	var size int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			size += info.Size()
		}
		return nil
	})
	return size, err
}

// filesystem returns the device of an existing folder.
func filesystem(path string) (uint64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("not a directory")
	}
	if dev, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(dev.Dev), nil
	}
	return 0, fmt.Errorf("device is unavailable")
}

// sizeText shows the total size of a mods folder in plain units.
func sizeText(n int64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	}
	if n >= 1<<20 {
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	if n >= 1<<10 {
		return fmt.Sprintf("%.1f kB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

// checkSteam reports discovered roots and libraries and checks configured app manifests.
func checkSteam(r *Report, opts Options, cfg *config.Config, ids []string) {
	roots, err := opts.FindSteamRoots()
	libraries := make(map[string]bool)
	if err != nil || len(roots) == 0 {
		r.Checks = append(r.Checks, Check{"Note", "Steam", "No Steam installation was found."})
	} else {
		for i, root := range roots {
			r.Checks = append(r.Checks, Check{"OK", fmt.Sprintf("Steam root %d", i+1), "Found Steam at " + root.Path})
			for _, library := range root.Libraries {
				libraries[filepath.Clean(library)] = true
			}
		}
		r.Checks = append(r.Checks, Check{"OK", "Steam libraries", fmt.Sprintf("%d distinct Steam libraries found.", len(libraries))})
	}
	for _, id := range ids {
		gc, err := cfg.EffectiveGameConfig(id)
		if err != nil || gc.SteamAppID <= 0 {
			continue
		}
		found := false
		for library := range libraries {
			if filepath.Clean(gc.InstallPath) != filepath.Join(library, "steamapps", "common", filepath.Base(gc.InstallPath)) {
				continue
			}
			if _, err := steam.ReadAppState(gc.InstallPath, gc.SteamAppID); err == nil {
				found = true
				break
			}
		}
		if found {
			r.Checks = append(r.Checks, Check{"OK", "Game " + id + " Steam record", "Steam installation record is readable."})
		} else {
			r.Checks = append(r.Checks, Check{"Problem", "Game " + id + " Steam record", "Could not read the game's Steam installation record. Check the game's Steam library and installation in Steam."})
		}
	}
}

package migrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/gamedef"
	"github.com/parka/gorganizer/internal/vfs"
)

type MigrationPlan struct {
	FromRoot string
	Items    []Item
}

type Item struct {
	GameID       string
	Name         string
	Source       string
	Destination  string
	Mode         string
	Bytes        int64
	Files        int
	FreeBytes    uint64
	Blockers     []string
	OutsideLinks []string
	References   []string
	Entries      []entry
}

type entry struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size,omitempty"`
	Mtime  int64  `json:"mtime"`
	Dev    uint64 `json:"dev"`
	Ino    uint64 `json:"ino"`
	SHA256 string `json:"sha256,omitempty"`
	Target string `json:"target,omitempty"`
}

// Plan inventories legacy mod folders and reports every reason a move cannot start.
func Plan(fromRoot string) (*MigrationPlan, error) {
	if !filepath.IsAbs(config.DataDir()) || !filepath.IsAbs(config.ConfigDir()) {
		return nil, fmt.Errorf("personal data and settings folders must be absolute paths")
	}
	if !filepath.IsAbs(fromRoot) || filepath.Clean(fromRoot) != fromRoot {
		return nil, fmt.Errorf("use an absolute, clean path for the old folder")
	}
	if _, err := os.Lstat(journalPath()); err == nil {
		return nil, fmt.Errorf("a move is unfinished; run gorganizerctl migrate-data --resume")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("checking move journal: %w", err)
	}
	root, err := os.Lstat(fromRoot)
	if err != nil {
		return nil, fmt.Errorf("checking old folder: %w", err)
	}
	if !root.IsDir() {
		return nil, fmt.Errorf("the old folder must be a real directory")
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("reading game settings: %w", err)
	}
	plan := &MigrationPlan{FromRoot: fromRoot}
	for _, game := range gamedef.All {
		if game.ModsDirName == "" {
			continue
		}
		src := filepath.Join(fromRoot, game.ModsDirName)
		info, err := os.Lstat(src)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("checking %s: %w", src, err)
		}
		item := Item{GameID: game.ID, Name: game.Name, Source: src, Destination: config.XDGModsDir(game.ID)}
		if !info.IsDir() {
			item.Blockers = append(item.Blockers, fmt.Sprintf("%s is not a real folder.", src))
		} else {
			item.Entries, item.Bytes, item.Files, item.OutsideLinks, item.Blockers, err = inventory(src)
			if err != nil {
				return nil, fmt.Errorf("reading %s: %w", src, err)
			}
		}
		if within(item.Destination, src) || within(src, item.Destination) {
			item.Blockers = append(item.Blockers, "The old and new folders overlap; choose a different old folder.")
		}
		if pathExists(item.Destination + ".gorganizer-migrating") {
			item.Blockers = append(item.Blockers, "An earlier copied folder needs attention before moving mods.")
		}
		if !info.IsDir() {
			item.Mode = "blocked"
			plan.Items = append(plan.Items, item)
			continue
		}
		if exists, nonempty, err := directoryState(item.Destination); err != nil {
			return nil, err
		} else if exists && nonempty {
			item.Blockers = append(item.Blockers, fmt.Sprintf("%s already contains files; move them out first.", item.Destination))
		}
		parent, err := existingParent(item.Destination)
		if err != nil {
			return nil, err
		}
		var sourceStat, destStat syscall.Stat_t
		if err := syscall.Stat(src, &sourceStat); err != nil {
			return nil, fmt.Errorf("checking old drive: %w", err)
		}
		if err := syscall.Stat(parent, &destStat); err != nil {
			return nil, fmt.Errorf("checking new drive: %w", err)
		}
		item.Mode = "rename"
		if sourceStat.Dev != destStat.Dev {
			item.Mode = "copy"
		}
		var space syscall.Statfs_t
		if err := syscall.Statfs(parent, &space); err != nil {
			return nil, fmt.Errorf("checking free space: %w", err)
		}
		item.FreeBytes = space.Bavail * uint64(space.Bsize)
		if item.Mode == "copy" && item.FreeBytes < uint64(item.Bytes)+uint64(item.Bytes)/20+1 {
			item.Blockers = append(item.Blockers, fmt.Sprintf("Not enough free space for %s and a safety margin.", game.Name))
		}
		deployed, err := deploymentPresent(cfg, game.ID)
		if err != nil {
			return nil, err
		}
		if deployed || pendingInSource(src) {
			item.Blockers = append(item.Blockers, fmt.Sprintf("Unmount the mods of %s in Gorganizer first.", game.Name))
		}
		item.References, err = referencesFor(cfg, src)
		if err != nil {
			return nil, err
		}
		plan.Items = append(plan.Items, item)
	}
	return plan, nil
}

// HasBlockers reports whether any planned folder must be handled manually first.
func (p *MigrationPlan) HasBlockers() bool {
	for _, item := range p.Items {
		if len(item.Blockers) != 0 {
			return true
		}
	}
	return false
}

// within reports whether path equals root or is a descendant of root.
func within(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// sameFileSnapshot checks that an open file still has its planned identity and metadata.
func sameFileSnapshot(info os.FileInfo, e entry) bool {
	if info == nil || !info.Mode().IsRegular() || info.Size() != e.Size || uint32(info.Mode()&(os.ModePerm|os.ModeSetuid|os.ModeSetgid|os.ModeSticky)) != e.Mode || info.ModTime().UnixNano() != e.Mtime {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && uint64(stat.Dev) == e.Dev && stat.Ino == e.Ino
}

// inventory records entry identities without opening regular files or following links.
func inventory(root string) ([]entry, int64, int, []string, []string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, 0, 0, nil, nil, err
	}
	var entries []entry
	var bytes int64
	var files int
	var outside, blockers []string
	err = filepath.WalkDir(root, func(path string, dirent fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("cannot identify %s", path)
		}
		e := entry{Path: rel, Mode: uint32(info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)), Mtime: info.ModTime().UnixNano(), Dev: uint64(stat.Dev), Ino: stat.Ino}
		switch {
		case info.IsDir():
			e.Kind = "dir"
		case info.Mode().IsRegular():
			e.Kind = "file"
			e.Size = info.Size()
			if e.Size > math.MaxInt64-bytes {
				return fmt.Errorf("old folder is too large to measure")
			}
			bytes += e.Size
			files++
		case info.Mode()&os.ModeSymlink != 0:
			e.Kind = "link"
			e.Target, err = os.Readlink(path)
			if err != nil {
				return err
			}
			target := e.Target
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(path), target)
			}
			inside := within(filepath.Clean(target), root) || within(filepath.Clean(target), realRoot)
			if resolved, err := filepath.EvalSymlinks(target); err == nil {
				inside = within(resolved, realRoot)
			}
			if !filepath.IsAbs(e.Target) && !inside {
				blockers = append(blockers, fmt.Sprintf("%s is a shortcut that points outside the folder being moved. Remove or replace it, then try again.", path))
			} else if filepath.IsAbs(e.Target) && inside {
				blockers = append(blockers, fmt.Sprintf("A link inside %s points inside the old folder; move it manually.", root))
			} else if filepath.IsAbs(e.Target) {
				outside = append(outside, rel+" -> "+e.Target)
			}
		default:
			e.Kind = "special"
			blockers = append(blockers, fmt.Sprintf("%s contains a file that needs to be moved manually (%s).", root, rel))
		}
		entries = append(entries, e)
		return nil
	})
	return entries, bytes, files, outside, blockers, err
}

// directoryState checks whether a destination exists and contains entries.
func directoryState(path string) (bool, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("checking destination %s: %w", path, err)
	}
	if !info.IsDir() {
		return true, true, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return true, true, fmt.Errorf("reading destination %s: %w", path, err)
	}
	return true, len(entries) > 0, nil
}

// existingParent finds a real existing directory without following symlinked ancestors.
func existingParent(path string) (string, error) {
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err == nil {
			if !info.IsDir() {
				return "", fmt.Errorf("%s is not a real directory", parent)
			}
			return parent, nil
		}
		if !errors.Is(err, os.ErrNotExist) || parent == filepath.Dir(parent) {
			return "", fmt.Errorf("checking destination parent %s: %w", parent, err)
		}
	}
}

// deploymentPresent detects active and interrupted game-side mod deployments.
func deploymentPresent(cfg *config.Config, id string) (bool, error) {
	if _, ok := cfg.Games[id]; !ok {
		return false, nil
	}
	gc, err := cfg.EffectiveGameConfig(id)
	if err != nil {
		return false, err
	}
	if gc.InstallPath == "" {
		return false, nil
	}
	subpath := gc.DataSubpath
	if subpath == "" {
		if game, ok := gamedef.ByID(id); ok {
			subpath = game.DataSubpath
		}
		if subpath == "" {
			subpath = "Data"
		}
	}
	data := filepath.Join(gc.InstallPath, subpath)
	paths := []string{filepath.Join(data, vfs.SentinelFilename), filepath.Join(gc.InstallPath, vfs.RootManifestFilename), filepath.Join(gc.InstallPath, vfs.RootIntentFilename)}
	for _, suffix := range append(vfs.FarmSiblingSuffixes(), vfs.RetainedFarmSiblingSuffixes()...) {
		paths = append(paths, data+suffix)
	}
	for _, path := range paths {
		if _, err := os.Lstat(path); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("checking deployment %s: %w", path, err)
		}
	}
	siblings, err := os.ReadDir(filepath.Dir(data))
	if err == nil {
		for _, e := range siblings {
			if strings.HasPrefix(e.Name(), filepath.Base(data)+".gorganizer-") {
				return true, nil
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("checking Data folder siblings: %w", err)
	}
	entries, err := os.ReadDir(gc.InstallPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking game folder: %w", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".gorganizer-maintenance") {
			return true, nil
		}
	}
	return false, nil
}

// pendingInSource checks the known per-game transaction and staging markers.
func pendingInSource(root string) bool {
	entries, err := os.ReadDir(root)
	if err != nil {
		return true
	}
	for _, e := range entries {
		name := e.Name()
		for _, prefix := range []string{".gorganizer-reinstall-intent-", ".reinstall-", ".gorganizer-trash-", ".gorganizer-import-", ".stage-", ".gorganizer-transfer", ".gorganizer-merge", ".gorganizer-maintenance", ".gorganizer-preserved"} {
			if strings.HasPrefix(name, prefix) {
				return true
			}
		}
	}
	requestPath := filepath.Join(root, ".gorganizer-dependency-requests.json")
	if info, err := os.Lstat(requestPath); err == nil {
		if !info.Mode().IsRegular() || info.Size() > 4<<20 {
			return true
		}
		requests, err := os.ReadFile(requestPath)
		if err != nil {
			return true
		}
		var state struct {
			SchemaVersion int               `json:"schema_version"`
			Batches       []json.RawMessage `json:"batches"`
		}
		if json.Unmarshal(requests, &state) != nil || state.SchemaVersion != 1 || len(state.Batches) > 0 {
			return true
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return true
	}
	downloads, err := os.Lstat(filepath.Join(root, "Downloads"))
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		return true
	}
	if !downloads.IsDir() {
		return false
	}
	landing := filepath.Join(root, "Downloads", ".gorganizer-landing")
	if exists, nonempty, err := directoryState(landing); err != nil || exists && nonempty {
		return true
	}
	return false
}

// referencesFor lists config paths inside a source folder.
func referencesFor(cfg *config.Config, source string) ([]string, error) {
	refs, err := configReferences(cfg, source, "")
	if err != nil {
		return nil, err
	}
	sort.Strings(refs)
	return refs, nil
}

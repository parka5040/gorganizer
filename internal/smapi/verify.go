package smapi

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

const maxReportedPaths = 5

// VerifyStage proves the stage holds exactly the payload-derived tree, including a byte-identical carried user config or none, and then normalizes its modes.
func VerifyStage(stageGame string, in StageInput, p Payload, spec LoaderSpec) error {
	expected := expectedStage(in, p, spec)
	extras := map[string]bool{spec.GameAssembly: true, spec.GameVersionFile: true}
	var missing []string
	for _, rel := range sortedFileKeys(expected) {
		got, _, err := hashRegular(filepath.Join(stageGame, filepath.FromSlash(rel)), false)
		switch {
		case err != nil:
			missing = append(missing, rel+" (missing)")
		case got.SHA256 != expected[rel]:
			missing = append(missing, rel+" (content differs)")
		}
	}
	for _, dir := range append([]string{modsDirName}, p.RootDirs...) {
		if !isPlainDir(filepath.Join(stageGame, filepath.FromSlash(dir))) {
			missing = append(missing, dir+"/ (missing)")
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s", ErrStageIncomplete, summarizePaths(missing))
	}
	dirs := map[string]bool{modsDirName: true}
	for _, dir := range p.RootDirs {
		dirs[dir] = true
	}
	for rel := range expected {
		addAncestors(dirs, rel)
	}
	for rel := range extras {
		addAncestors(dirs, rel)
	}
	var unexpected []string
	err := filepath.WalkDir(stageGame, func(full string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if full == stageGame {
			return nil
		}
		rel, err := relToSlash(stageGame, full)
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			if !dirs[rel] {
				unexpected = append(unexpected, rel+"/")
				return filepath.SkipDir
			}
		case entry.Type().IsRegular():
			if _, ok := expected[rel]; !ok && !extras[rel] {
				unexpected = append(unexpected, rel)
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink != 1 {
				unexpected = append(unexpected, rel+" (hard link)")
			}
		default:
			unexpected = append(unexpected, rel+" (not a regular file)")
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walking stage: %w", err)
	}
	if len(unexpected) > 0 {
		return fmt.Errorf("%w: %s", ErrStageUnexpected, summarizePaths(unexpected))
	}
	return normalizeModes(stageGame, spec)
}

// expectedStage maps every stage file the upstream installer must produce to its expected SHA-256 digest.
func expectedStage(in StageInput, p Payload, spec LoaderSpec) map[string]string {
	expected := map[string]string{}
	for rel, file := range p.Root {
		expected[rel] = file.SHA256
	}
	for rel, file := range p.Mods {
		expected[rel] = file.SHA256
	}
	expected[spec.LauncherName] = p.Launcher.SHA256
	expected[spec.LauncherBackupName] = in.VanillaLauncher.SHA256
	expected[spec.LoaderDepsFile] = in.DepsSHA
	if in.CarriedUserConfig {
		expected[userConfigRel] = in.UserConfig.SHA256
	}
	return expected
}

// normalizeModes sets directories and data files to 0755 and 0644 and the launchers and loader executable to 0755.
func normalizeModes(stageGame string, spec LoaderSpec) error {
	executables := map[string]bool{spec.LauncherName: true, spec.LauncherBackupName: true, spec.LoaderExecutable: true}
	return filepath.WalkDir(stageGame, func(full string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := relToSlash(stageGame, full)
		if err != nil {
			return err
		}
		mode := os.FileMode(0644)
		if entry.IsDir() || executables[rel] {
			mode = 0755
		}
		if err := os.Chmod(full, mode); err != nil {
			return fmt.Errorf("normalizing mode of %s: %w", rel, err)
		}
		return nil
	})
}

// addAncestors records every parent directory of the slash path rel.
func addAncestors(dirs map[string]bool, rel string) {
	for dir := path.Dir(rel); dir != "."; dir = path.Dir(dir) {
		dirs[dir] = true
	}
}

// sortedFileKeys returns the keys of a path map in ascending order.
func sortedFileKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// summarizePaths joins the first few paths and counts the rest.
func summarizePaths(paths []string) string {
	sort.Strings(paths)
	if len(paths) <= maxReportedPaths {
		return strings.Join(paths, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(paths[:maxReportedPaths], ", "), len(paths)-maxReportedPaths)
}

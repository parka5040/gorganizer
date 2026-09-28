package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/google/uuid"
	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/vfs"
)

var errSmokeCrash = errors.New("simulated interruption")

// runCrashMatrix recovers three interrupted farm operations on the supplied scratch folder.
func runCrashMatrix(dataPath string) error {
	absolute, err := filepath.Abs(dataPath)
	if err != nil {
		return err
	}
	dataPath = absolute
	info, err := os.Lstat(dataPath)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("crash matrix requires a real scratch Data directory: %v", err)
	}
	for _, suffix := range append(vfs.FarmSiblingSuffixes(), vfs.RetainedFarmSiblingSuffixes()...) {
		if _, err := os.Lstat(dataPath + suffix); err == nil {
			return fmt.Errorf("crash matrix refuses existing farm sibling %s", dataPath+suffix)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("checking farm sibling %s: %w", dataPath+suffix, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dataPath, vfs.SentinelFilename)); err == nil {
		return fmt.Errorf("crash matrix refuses an already deployed Data directory")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("checking Data sentinel: %w", err)
	}
	pre, err := hashTree(dataPath)
	if err != nil {
		return fmt.Errorf("fingerprinting scratch Data: %w", err)
	}
	for _, row := range []struct {
		name string
		run  func(string, string) error
	}{
		{"activation after rename", smokeActivationCrash},
		{"apply after exchange", smokeApplyCrash},
		{"unmount during capture", smokeCaptureCrash},
	} {
		root, err := os.MkdirTemp(filepath.Dir(dataPath), ".gorganizer-smoke-")
		if err != nil {
			return err
		}
		overwrite := filepath.Join(root, "Overwrite")
		if err := os.Mkdir(overwrite, 0700); err != nil {
			_ = os.RemoveAll(root)
			return err
		}
		err = row.run(dataPath, overwrite)
		if err == nil {
			var post string
			post, err = hashTree(dataPath)
			if err == nil && post != pre {
				err = fmt.Errorf("scratch Data changed: %s != %s", post, pre)
			}
		}
		if err == nil {
			for _, suffix := range vfs.FarmSiblingSuffixes() {
				if _, statErr := os.Lstat(dataPath + suffix); !errors.Is(statErr, os.ErrNotExist) {
					err = fmt.Errorf("transition sibling %s remains: %w", suffix, statErr)
					if statErr == nil {
						err = fmt.Errorf("transition sibling %s remains", suffix)
					}
					break
				}
			}
		}
		if err == nil {
			mm := vfs.NewMountManager(dataPath, overwrite, "testgame")
			if err = mm.Activate([]vfs.Layer{{Name: "__base__", RootPath: dataPath, Enabled: true}}, "Default"); err == nil {
				err = mm.Deactivate()
			}
		}
		if removeErr := os.RemoveAll(root); removeErr != nil && err == nil {
			err = removeErr
		}
		if err != nil {
			return fmt.Errorf("%s: %w", row.name, err)
		}
		fmt.Fprintf(os.Stderr, "crash matrix: %s recovered\n", row.name)
	}
	return nil
}

// smokeActivationCrash stops after moving the original Data folder and then rolls the intent back.
func smokeActivationCrash(dataPath, overwrite string) error {
	info, err := os.Stat(dataPath)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("scratch Data has no filesystem identity")
	}
	backup := dataPath + ".orig"
	intent := struct {
		SchemaVersion int    `json:"schema_version"`
		Magic         string `json:"magic"`
		Kind          string `json:"kind"`
		OperationID   string `json:"operation_id"`
		GameID        string `json:"game_id"`
		DataPath      string `json:"data_path"`
		BackupPath    string `json:"backup_path"`
		Original      struct {
			Dev uint64 `json:"dev"`
			Ino uint64 `json:"ino"`
		} `json:"original"`
	}{SchemaVersion: vfs.CurrentIntentSchema, Magic: vfs.IntentMagic, Kind: string(vfs.IntentActivating),
		OperationID: uuid.NewString(), GameID: "testgame", DataPath: dataPath, BackupPath: backup}
	intent.Original.Dev, intent.Original.Ino = uint64(stat.Dev), stat.Ino
	interrupt := func() error {
		body, err := json.Marshal(intent)
		if err != nil {
			return err
		}
		if _, err := atomicfile.WriteFileDurable(dataPath+".gorganizer-activating", body, 0644); err != nil {
			return err
		}
		if err := os.Rename(dataPath, backup); err != nil {
			return err
		}
		return errSmokeCrash
	}
	if err := interrupt(); !errors.Is(err, errSmokeCrash) {
		return fmt.Errorf("activation cut: %w", err)
	}
	outcome, err := vfs.CleanupStale(dataPath)
	if err != nil || !outcome.Restored || outcome.Pending != nil {
		return fmt.Errorf("activation recovery = %+v: %w", outcome, err)
	}
	return nil
}

// smokeApplyCrash leaves a valid former farm beside the exchanged Data farm for recovery.
func smokeApplyCrash(dataPath, overwrite string) error {
	mm := vfs.NewMountManager(dataPath, overwrite, "testgame")
	layers := []vfs.Layer{{Name: "__base__", RootPath: dataPath, Enabled: true}}
	if err := mm.Activate(layers, "Default"); err != nil {
		return err
	}
	old, err := vfs.ReadSentinel(dataPath)
	if err != nil {
		return err
	}
	copyPath, err := os.MkdirTemp(filepath.Dir(dataPath), ".gorganizer-smoke-old-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(copyPath)
	if err := smokeCopyFarm(dataPath, copyPath); err != nil {
		return err
	}
	if err := mm.MarkDirty(layers); err != nil {
		return err
	}
	if err := mm.ReMaterialize(); err != nil {
		return err
	}
	current, err := vfs.ReadSentinel(dataPath)
	if err != nil {
		return err
	}
	interrupt := func() error {
		if err := os.Rename(copyPath, dataPath+".gorganizer-staging"); err != nil {
			return err
		}
		if err := vfs.WriteIntent(dataPath+".gorganizer-applying", &vfs.ActivationIntent{
			SchemaVersion: vfs.CurrentIntentSchema, Magic: vfs.IntentMagic, Kind: vfs.IntentApplying,
			OperationID: uuid.NewString(), GameID: "testgame", DataPath: dataPath,
			BackupPath: dataPath + ".orig", StagingPath: dataPath + ".gorganizer-staging",
			LiveFarmID: old.FarmID, StagingFarmID: current.FarmID,
		}); err != nil {
			return err
		}
		return errSmokeCrash
	}
	if err := interrupt(); !errors.Is(err, errSmokeCrash) {
		return fmt.Errorf("apply cut: %w", err)
	}
	outcome, err := vfs.CleanupStale(dataPath)
	if err != nil || !outcome.Restored || outcome.Pending != nil {
		return fmt.Errorf("apply recovery = %+v: %w", outcome, err)
	}
	return nil
}

// smokeCaptureCrash stops unmount after one captured file and resumes it after removing the obstruction.
func smokeCaptureCrash(dataPath, overwrite string) error {
	mm := vfs.NewMountManager(dataPath, overwrite, "testgame")
	if err := mm.Activate([]vfs.Layer{{Name: "__base__", RootPath: dataPath, Enabled: true}}, "Default"); err != nil {
		return err
	}
	for _, name := range []string{"a-smoke-output", "z-smoke-output"} {
		if err := os.WriteFile(filepath.Join(dataPath, name), []byte(name), 0644); err != nil {
			return err
		}
	}
	blocked := filepath.Join(overwrite, "z-smoke-output")
	if err := os.Symlink(filepath.Join(overwrite, "unrelated"), blocked); err != nil {
		return err
	}
	if err := mm.Deactivate(); !errors.Is(err, vfs.ErrCaptureFailed) {
		return fmt.Errorf("capture cut = %v, want a capture failure", err)
	}
	if err := os.Remove(blocked); err != nil {
		return err
	}
	outcome, err := vfs.CleanupStale(dataPath)
	if err != nil || !outcome.Restored || outcome.Pending != nil {
		return fmt.Errorf("capture recovery = %+v: %w", outcome, err)
	}
	for _, name := range []string{"a-smoke-output", "z-smoke-output"} {
		body, err := os.ReadFile(filepath.Join(overwrite, name))
		if err != nil || string(body) != name {
			return fmt.Errorf("captured %s = %q: %w", name, body, err)
		}
	}
	return nil
}

// smokeCopyFarm duplicates an old farm without following symlinks or modifying its source files.
func smokeCopyFarm(from, to string) error {
	return filepath.WalkDir(from, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil || rel == "." {
			return err
		}
		target := filepath.Join(to, rel)
		if entry.IsDir() {
			return os.Mkdir(target, 0755)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		return os.Link(path, target)
	})
}

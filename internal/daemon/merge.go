package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
)

// prepareMergeStage recreates the original mod under a hidden stage using hardlinks for regular files.
func prepareMergeStage(modDir, stageDir string) error {
	if err := os.Mkdir(stageDir, 0755); err != nil {
		return fmt.Errorf("creating merge stage: %w", err)
	}
	if err := filepath.WalkDir(modDir, func(path string, _ os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == modDir {
			return nil
		}
		rel, err := filepath.Rel(modDir, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		dest := filepath.Join(stageDir, rel)
		switch {
		case info.IsDir():
			return os.Mkdir(dest, info.Mode().Perm())
		case info.Mode().IsRegular():
			if err := os.Link(path, dest); err == nil {
				return nil
			} else if !errors.Is(err, syscall.EXDEV) {
				return fmt.Errorf("linking merge file %q: %w", rel, err)
			}
			outcome, err := atomicfile.CopyFileDurable(path, dest, info.Mode().Perm(), false)
			if err != nil {
				return fmt.Errorf("copying merge file %q: %w", rel, err)
			}
			if outcome != atomicfile.Durable {
				return fmt.Errorf("copying merge file %q: copy was not durable", rel)
			}
			return nil
		default:
			return &download.ArchiveRejectedError{Reason: download.ArchiveRejectedUnsafeEntry, Detail: rel}
		}
	}); err != nil {
		_ = os.RemoveAll(stageDir)
		return fmt.Errorf("preparing merge stage: %w", err)
	}
	return nil
}

// publishPreparedMerge preserves current metadata fields and swaps a completed merge into place under the game's profile lock.
func (is *InstallService) publishPreparedMerge(gameID, modName, token string, snapshot *download.ModMetadata) error {
	defer is.s.lockProfiles(gameID)()
	modsDir := config.ModsDir(gameID)
	modDir := filepath.Join(modsDir, modName)
	stageDir := filepath.Join(modsDir, reinstallStagePrefix+token)
	current, err := download.LoadModMetadata(modDir)
	if err != nil {
		_ = os.RemoveAll(stageDir)
		return fmt.Errorf("reading mod metadata: %w", err)
	}
	staged, err := download.LoadModMetadata(stageDir)
	if err != nil {
		_ = os.RemoveAll(stageDir)
		return fmt.Errorf("reading merged metadata: %w", err)
	}
	final := mergeStageMetadata(snapshot, current, staged)
	final.Folder = modName
	if err := download.SaveModMetadata(stageDir, final); err != nil {
		_ = os.RemoveAll(stageDir)
		return fmt.Errorf("writing merged metadata: %w", err)
	}
	return is.s.svc.mods.publishReinstallStage(gameID, modName, modsDir, token, dto.GameRunningOperationMerge, true)
}

// mergeStageMetadata retains metadata edits made to the original while the merged files were prepared.
func mergeStageMetadata(snapshot, current, staged *download.ModMetadata) *download.ModMetadata {
	final := *staged
	if current.Name != snapshot.Name {
		final.Name = current.Name
	}
	if current.Installed != snapshot.Installed {
		final.Installed = current.Installed
	}
	if current.Category != snapshot.Category {
		final.Category = current.Category
	}
	if current.Version != snapshot.Version {
		final.Version = current.Version
	}
	if current.Enabled != snapshot.Enabled {
		final.Enabled = current.Enabled
	}
	if current.ModPage != snapshot.ModPage {
		final.ModPage = current.ModPage
	}
	if current.TrueIndex != snapshot.TrueIndex {
		final.TrueIndex = current.TrueIndex
	}
	if current.VisualIndex != snapshot.VisualIndex {
		final.VisualIndex = current.VisualIndex
	}
	if current.Separator != snapshot.Separator {
		final.Separator = current.Separator
	}
	return &final
}

package daemon

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func copyUpSelectedInput(dataRoot, outputRoot, selected string) error {
	clean := filepath.Clean(selected)
	if clean == "." || clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return errors.New("selected input must be a relative Data path")
	}
	source := filepath.Join(dataRoot, clean)
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.New("selected input must be a file")
	}
	destination := filepath.Join(outputRoot, clean)
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return err
	}
	if err := replaceWithPrivateCopy(source, destination, info.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Remove(source); err != nil {
		return err
	}
	if err := os.Link(destination, source); err == nil {
		return nil
	}
	abs, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	return os.Symlink(abs, source)
}

func prepareWritableOutputLayer(dataRoot, outputRoot string) error {
	return filepath.Walk(outputRoot, func(source string, sourceInfo os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if sourceInfo.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(outputRoot, source)
		if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) {
			return err
		}
		farmPath := filepath.Join(dataRoot, rel)
		farmInfo, err := os.Stat(farmPath)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		resolvedSource := source
		if sourceInfo.Mode()&os.ModeSymlink != 0 {
			resolvedSource, err = filepath.EvalSymlinks(source)
			if err != nil {
				return err
			}
			sourceInfo, err = os.Stat(resolvedSource)
			if err != nil {
				return err
			}
		}
		if !os.SameFile(sourceInfo, farmInfo) {
			if resolvedFarm, resolveErr := filepath.EvalSymlinks(farmPath); resolveErr != nil || resolvedFarm != resolvedSource {
				return nil
			}
		}
		return replaceWithPrivateCopy(resolvedSource, farmPath, sourceInfo.Mode().Perm())
	})
}

func replaceWithPrivateCopy(source, destination string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	temp, err := os.CreateTemp(filepath.Dir(destination), ".gorganizer-copyup-")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	cleanup := true
	defer func() {
		_ = temp.Close()
		if cleanup {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := io.Copy(temp, in); err != nil {
		return err
	}
	if err := temp.Chmod(mode | 0200); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, destination); err != nil {
		return fmt.Errorf("replacing %s with private copy: %w", destination, err)
	}
	cleanup = false
	return nil
}

// importScratchOutput atomically commits private scratch output into a capture mod.
func importScratchOutput(scratchRoot, captureRoot string, preserveCapture bool) (int, error) {
	if scratchRoot == "" || captureRoot == "" {
		return 0, errors.New("scratch and capture roots are required")
	}
	scratchInfo, err := os.Stat(scratchRoot)
	if err != nil {
		return 0, fmt.Errorf("reading tool scratch output: %w", err)
	}
	if !scratchInfo.IsDir() {
		return 0, errors.New("tool scratch output is not a directory")
	}

	parent := filepath.Dir(captureRoot)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return 0, fmt.Errorf("creating capture parent: %w", err)
	}
	stage, err := os.MkdirTemp(parent, ".gorganizer-tool-import-")
	if err != nil {
		return 0, fmt.Errorf("creating tool import stage: %w", err)
	}
	stagePresent := true
	defer func() {
		if stagePresent {
			_ = os.RemoveAll(stage)
		}
	}()

	if preserveCapture {
		if info, statErr := os.Stat(captureRoot); statErr == nil {
			if !info.IsDir() {
				return 0, errors.New("capture root is not a directory")
			}
			if _, err := copyToolOutputTree(captureRoot, stage); err != nil {
				return 0, fmt.Errorf("staging existing capture contents: %w", err)
			}
		} else if !os.IsNotExist(statErr) {
			return 0, fmt.Errorf("reading capture root: %w", statErr)
		}
	}

	count, err := copyToolOutputTree(scratchRoot, stage)
	if err != nil {
		return 0, fmt.Errorf("staging scratch output: %w", err)
	}
	if count == 0 {
		return 0, nil
	}
	if err := os.Chmod(stage, 0755); err != nil {
		return 0, err
	}

	backup := stage + ".previous"
	hadCapture := false
	if _, err := os.Stat(captureRoot); err == nil {
		if err := os.Rename(captureRoot, backup); err != nil {
			return 0, fmt.Errorf("backing up capture mod before import: %w", err)
		}
		hadCapture = true
	} else if !os.IsNotExist(err) {
		return 0, fmt.Errorf("checking capture mod before import: %w", err)
	}
	if err := os.Rename(stage, captureRoot); err != nil {
		if hadCapture {
			_ = os.Rename(backup, captureRoot)
		}
		return 0, fmt.Errorf("activating imported tool output: %w", err)
	}
	stagePresent = false
	if hadCapture {
		if err := os.RemoveAll(backup); err != nil {
			return count, fmt.Errorf("removing previous capture mod after import: %w", err)
		}
	}
	if dir, err := os.Open(parent); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return count, nil
}

func copyToolOutputTree(sourceRoot, destinationRoot string) (int, error) {
	count := 0
	err := filepath.Walk(sourceRoot, func(source string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourceRoot, source)
		if err != nil || rel == "." {
			return err
		}
		if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe tool output path %q", rel)
		}
		destination := filepath.Join(destinationRoot, rel)
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("tool output contains unsupported symlink %q", rel)
		}
		if info.IsDir() {
			return os.MkdirAll(destination, info.Mode().Perm()|0700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("tool output contains unsupported special file %q", rel)
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return err
		}
		in, err := os.Open(source)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm()|0200)
		if err != nil {
			_ = in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		syncErr := out.Sync()
		closeOutErr := out.Close()
		closeInErr := in.Close()
		if copyErr != nil {
			return copyErr
		}
		if syncErr != nil {
			return syncErr
		}
		if closeOutErr != nil {
			return closeOutErr
		}
		if closeInErr != nil {
			return closeInErr
		}
		if err := os.Chtimes(destination, time.Now(), info.ModTime()); err != nil {
			return err
		}
		count++
		return nil
	})
	return count, err
}

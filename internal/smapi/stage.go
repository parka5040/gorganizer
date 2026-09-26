package smapi

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const maxLauncherBytes int64 = 4 << 20

type StageInput struct {
	VanillaLauncher   PayloadFile
	VanillaSource     string
	DepsSHA           string
	CarriedUserConfig bool
	UserConfig        PayloadFile
}

// BuildStage creates the private game projection the upstream installer runs against.
func BuildStage(gameDir, stageGame string, spec LoaderSpec) (StageInput, error) {
	if err := spec.Validate(); err != nil {
		return StageInput{}, err
	}
	if err := os.MkdirAll(stageGame, 0755); err != nil {
		return StageInput{}, fmt.Errorf("creating stage: %w", err)
	}
	var in StageInput
	if _, err := copyRegular(filepath.Join(gameDir, spec.GameAssembly), filepath.Join(stageGame, spec.GameAssembly), 0644); err != nil {
		return StageInput{}, fmt.Errorf("staging %s: %w", spec.GameAssembly, err)
	}
	deps, err := copyRegular(filepath.Join(gameDir, spec.GameVersionFile), filepath.Join(stageGame, spec.GameVersionFile), 0644)
	if err != nil {
		return StageInput{}, fmt.Errorf("staging %s: %w", spec.GameVersionFile, err)
	}
	in.DepsSHA = deps.SHA256
	source, data, err := findVanillaLauncher(gameDir, spec)
	if err != nil {
		return StageInput{}, err
	}
	in.VanillaSource = source
	launcher, err := writeNewFile(filepath.Join(stageGame, spec.LauncherName), data, 0755)
	if err != nil {
		return StageInput{}, fmt.Errorf("staging vanilla launcher: %w", err)
	}
	in.VanillaLauncher = launcher
	userConfig := filepath.Join(gameDir, filepath.FromSlash(userConfigRel))
	if isPlainDir(filepath.Join(gameDir, loaderInternalDir)) && isRegularFile(userConfig) {
		stageConfig := filepath.Join(stageGame, filepath.FromSlash(userConfigRel))
		if err := os.MkdirAll(filepath.Dir(stageConfig), 0755); err != nil {
			return StageInput{}, err
		}
		carried, err := copyRegular(userConfig, stageConfig, 0644)
		if err != nil {
			return StageInput{}, fmt.Errorf("staging SMAPI user config: %w", err)
		}
		in.CarriedUserConfig = true
		in.UserConfig = carried
	}
	if err := os.Mkdir(filepath.Join(stageGame, modsDirName), 0755); err != nil {
		return StageInput{}, fmt.Errorf("creating staged Mods folder: %w", err)
	}
	return in, nil
}

// findVanillaLauncher returns the game-relative name and bytes of the freshest launcher that does not start the loader.
func findVanillaLauncher(gameDir string, spec LoaderSpec) (string, []byte, error) {
	for _, name := range []string{spec.LauncherName, spec.LauncherBackupName} {
		data, ok, err := readVanillaLauncher(filepath.Join(gameDir, name), spec)
		if err != nil {
			return "", nil, err
		}
		if ok {
			return name, data, nil
		}
	}
	return "", nil, fmt.Errorf("neither %s nor %s is a vanilla launcher: %w", spec.LauncherName, spec.LauncherBackupName, ErrNoVanillaLauncher)
}

// readVanillaLauncher reads path when it is a regular file that does not mention the loader executable.
func readVanillaLauncher(path string, spec LoaderSpec) ([]byte, bool, error) {
	info, err := lstatOptional(path)
	if err != nil {
		return nil, false, err
	}
	if info == nil || !info.Mode().IsRegular() {
		return nil, false, nil
	}
	data, err := readSmallRegular(path, maxLauncherBytes)
	if err != nil {
		return nil, false, fmt.Errorf("reading launcher: %w", err)
	}
	if mentionsLoader(data, spec) {
		return nil, false, nil
	}
	return data, true, nil
}

// mentionsLoader reports whether launcher content starts the loader executable.
func mentionsLoader(data []byte, spec LoaderSpec) bool {
	return bytes.Contains(data, []byte(spec.LoaderExecutable))
}

// writeNewFile creates path exclusively with data and perm and returns its digest.
func writeNewFile(path string, data []byte, perm os.FileMode) (PayloadFile, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return PayloadFile{}, err
	}
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Chmod(perm)
	}
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return PayloadFile{}, err
	}
	return digestBytes(data), nil
}

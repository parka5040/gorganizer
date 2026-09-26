package smapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	defaultInstallerTimeout = 5 * time.Minute
	maxErrorOutput          = 2 << 10
	installerPath           = "/usr/bin:/bin"
	warningPhase            = "warning"
	appliedPhase            = "applied"
	relocateDirName         = "relocate"
)

type LoaderState uint8

const (
	StateUnknown LoaderState = iota
	StateNotInstalled
	StateOK
	StateLauncherReverted
	StateIncomplete
	StateUnsupportedBuild
	StateInterrupted
)

type Status struct {
	State           LoaderState
	Managed         bool
	Version         string
	Recorded        bool
	RecordedVersion string
	Detail          string
}

type Installer struct {
	Spec     LoaderSpec
	Runner   Runner
	Now      func() time.Time
	Progress func(phase, detail string)
	Timeout  time.Duration
	fault    func(string) error
}

type operation struct {
	gameDir string
	id      string
	work    string
	fault   func(string) error
	crashed bool
}

type bundledTargets struct {
	targets []string
	found   map[string]bool
}

// String returns the lower-case wire name of the loader state.
func (s LoaderState) String() string {
	switch s {
	case StateNotInstalled:
		return "not_installed"
	case StateOK:
		return "ok"
	case StateLauncherReverted:
		return "launcher_reverted"
	case StateIncomplete:
		return "incomplete"
	case StateUnsupportedBuild:
		return "unsupported_build"
	case StateInterrupted:
		return "interrupted"
	default:
		return "unknown"
	}
}

// Install runs the verified upstream installer against a private stage, proves the result, and applies it to gameDir transactionally.
func (in *Installer) Install(ctx context.Context, gameDir string, art Artifact) (*Record, error) {
	unlock, err := in.begin(ctx, gameDir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	op, err := in.newOperation(gameDir)
	if err != nil {
		return nil, err
	}
	defer op.finish()
	if err := writeOwner(op.work, ownerInfo{}); err != nil {
		return nil, fmt.Errorf("recording installer owner: %w", err)
	}
	if err := op.step("work"); err != nil {
		return nil, err
	}
	in.progress("verify", art.Version)
	bundle, err := OpenBundle(art, filepath.Join(op.work, "bundle"), in.Spec)
	if err != nil {
		return nil, err
	}
	in.progress("extract", art.Version)
	payload, err := bundle.Payload(in.Spec)
	if err != nil {
		return nil, err
	}
	if err := op.step("bundle"); err != nil {
		return nil, err
	}
	in.progress("stage", "")
	stageGame := filepath.Join(op.work, "stage", "game")
	stageIn, err := BuildStage(gameDir, stageGame, in.Spec)
	if err != nil {
		return nil, err
	}
	env, err := installerEnv(op.work)
	if err != nil {
		return nil, err
	}
	if err := op.step("stage"); err != nil {
		return nil, err
	}
	in.progress("run", art.Version)
	var ownerMu sync.Mutex
	var owner ownerInfo
	var ownerErr error
	result, runErr := in.runner().Run(ctx, Command{
		Path:    bundle.InstallerPath,
		Args:    []string{"--install", "--no-prompt", "--game-path", stageGame},
		Dir:     filepath.Dir(bundle.InstallerPath),
		Env:     env,
		Timeout: in.timeout(),
	}, func(pid int) {
		ownerMu.Lock()
		defer ownerMu.Unlock()
		start, err := ProcessStartTime(pid)
		if err == nil {
			owner = ownerInfo{PID: pid, Start: start}
			err = writeOwner(op.work, owner)
		}
		ownerErr = err
	})
	if runErr != nil {
		return nil, fmt.Errorf("running SMAPI installer: %w%s", runErr, outputTail(result.Output))
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("SMAPI installer exited with code %d%s", result.ExitCode, outputTail(result.Output))
	}
	ownerMu.Lock()
	recordedOwner, recordErr := owner, ownerErr
	ownerMu.Unlock()
	if recordErr != nil {
		return nil, fmt.Errorf("recording installer process: %w", recordErr)
	}
	if err := op.step("run"); err != nil {
		return nil, err
	}
	in.progress("check", "")
	if err := VerifyStage(stageGame, stageIn, payload, in.Spec); err != nil {
		return nil, fmt.Errorf("SMAPI installer result rejected: %w%s", err, outputTail(result.Output))
	}
	if err := op.step("check"); err != nil {
		return nil, err
	}
	bundled, err := in.relocateBundledMods(gameDir, stageGame, op.work, payload)
	if err != nil {
		return nil, err
	}
	plan, err := in.installPlan(gameDir, payload, stageIn, bundled)
	if err != nil {
		return nil, err
	}
	plan.OpID = op.id
	plan.Version = art.Version
	plan.ArtifactSHA256 = art.SHA256
	plan.WorkDir = op.work
	plan.StageGame = stageGame
	plan.InstallerPID = recordedOwner.PID
	plan.InstallerStart = recordedOwner.Start
	plan.InstalledAt = in.now()
	plan.fault = op.step
	plan.warn = in.warn
	in.progress("apply", "")
	record, err := apply(gameDir, plan)
	if err != nil {
		return record, err
	}
	in.progress(appliedPhase, art.Version)
	return record, nil
}

// installPlan derives the placed, removed, restored, and preserved game-root paths of an install from the verified payload and stage.
func (in *Installer) installPlan(gameDir string, p Payload, stageIn StageInput, bundled bundledTargets) (TxnPlan, error) {
	spec := in.Spec
	previous, err := ReadRecord(gameDir)
	if err != nil {
		return TxnPlan{}, err
	}
	previousTargets := map[string]bool{}
	if previous != nil {
		if err := previous.checkOwnership(spec); err != nil {
			return TxnPlan{}, err
		}
		for _, target := range previous.Targets {
			previousTargets[target] = true
		}
	}
	expect, err := in.expectedInputs(gameDir, stageIn)
	if err != nil {
		return TxnPlan{}, err
	}
	place := append(payloadTopLevel(p), spec.LauncherName, spec.LauncherBackupName, spec.LoaderDepsFile)
	place = sortedUnique(append(place, bundled.targets...))
	uninstall := map[string]bool{}
	for _, rel := range spec.UninstallPaths {
		uninstall[rel] = true
	}
	var preserve []string
	for _, rel := range place {
		full, err := safeGamePath(gameDir, rel)
		if err != nil {
			return TxnPlan{}, err
		}
		info, err := lstatOptional(full)
		if err != nil {
			return TxnPlan{}, err
		}
		switch {
		case info == nil:
		case info.IsDir():
			if !previousTargets[rel] && !uninstall[rel] && !bundled.found[rel] {
				return TxnPlan{}, &UnsafeTargetError{Path: rel, Reason: "an existing directory the mod loader does not own would be replaced"}
			}
		case info.Mode().IsRegular():
			if rel != spec.LauncherName && rel != spec.LauncherBackupName && !previousTargets[rel] && !uninstall[rel] {
				preserve = append(preserve, rel)
			}
		}
	}
	restore := map[string]RestoreFile{}
	var carried, drop []OriginalFile
	if previous != nil {
		for _, original := range previous.Originals {
			data, loadErr := loadOriginal(gameDir, original)
			if contains(place, original.Path) {
				if loadErr != nil {
					in.warn(fmt.Sprintf("saved original %s cannot be verified: %v", original.Path, loadErr))
				}
				carried = append(carried, original)
				continue
			}
			if loadErr != nil {
				in.warn(fmt.Sprintf("dropping saved original %s: %v; the loader's copy is removed instead", original.Path, loadErr))
				continue
			}
			restore[original.Path] = RestoreFile{Data: data, Mode: os.FileMode(original.Mode)}
			drop = append(drop, original)
		}
	}
	candidates := append([]string{}, spec.UninstallPaths...)
	if previous != nil {
		candidates = append(candidates, previous.Targets...)
	}
	var remove []string
	for _, rel := range sortedUnique(candidates) {
		if overlapsAny(rel, place) || overlapsAny(rel, restoreKeys(restore)) {
			continue
		}
		exists, err := checkUnder(gameDir, rel)
		if err != nil {
			return TxnPlan{}, err
		}
		if exists {
			remove = append(remove, rel)
		}
	}
	return TxnPlan{
		Op:                OpInstall,
		Place:             place,
		Remove:            remove,
		Restore:           restore,
		PreserveOriginals: preserve,
		Originals:         carried,
		DropOriginals:     drop,
		Expect:            expect,
		Protected:         spec.protectedNames(),
		Farm:              spec.Farm,
	}, nil
}

// expectedInputs re-reads the game inputs the stage was built from, failing with ErrGameChanged when they moved on, and returns the digests Apply must still find.
func (in *Installer) expectedInputs(gameDir string, stageIn StageInput) (map[string]string, error) {
	spec := in.Spec
	source, data, err := findVanillaLauncher(gameDir, spec)
	if errors.Is(err, ErrNoVanillaLauncher) {
		return nil, fmt.Errorf("%v: %w", err, ErrGameChanged)
	}
	if err != nil {
		return nil, err
	}
	if source != stageIn.VanillaSource || digestBytes(data).SHA256 != stageIn.VanillaLauncher.SHA256 {
		return nil, fmt.Errorf("the vanilla launcher changed (now %s): %w", source, ErrGameChanged)
	}
	expect := map[string]string{spec.GameVersionFile: stageIn.DepsSHA}
	for _, name := range []string{spec.LauncherName, spec.LauncherBackupName} {
		full := filepath.Join(gameDir, name)
		info, err := lstatOptional(full)
		if err != nil {
			return nil, err
		}
		switch {
		case info == nil:
			expect[name] = ""
		case info.Mode().IsRegular():
			sum, _, err := hashRegular(full, false)
			if err != nil {
				return nil, err
			}
			expect[name] = sum.SHA256
		}
	}
	if stageIn.CarriedUserConfig {
		expect[userConfigRel] = stageIn.UserConfig.SHA256
	}
	return expect, nil
}

// relocateBundledMods moves each staged bundled mod onto the Mods folder that already holds a mod with the same UniqueID and returns the bundled targets.
func (in *Installer) relocateBundledMods(gameDir, stageGame, work string, p Payload) (bundledTargets, error) {
	folders := payloadModFolders(p)
	result := bundledTargets{found: map[string]bool{}}
	ids := map[string]string{}
	for _, folder := range folders {
		id, err := stagedModID(stageGame, folder, p)
		if err != nil {
			return bundledTargets{}, err
		}
		ids[folder] = id
	}
	destinations := map[string]string{}
	mods := filepath.Join(gameDir, modsDirName)
	if isPlainDir(mods) {
		scanned, err := Scan(mods)
		if err != nil {
			return bundledTargets{}, fmt.Errorf("scanning %s for bundled mods: %w", modsDirName, err)
		}
		for _, found := range scanned {
			if found.Kind != FolderMod || found.Manifest == nil {
				continue
			}
			for _, folder := range folders {
				if _, done := destinations[folder]; !done && SameID(found.Manifest.UniqueID, ids[folder]) {
					destinations[folder] = modsDirName + "/" + found.RelPath
				}
			}
		}
	}
	var moves []string
	for _, folder := range folders {
		target := folder
		if destination, ok := destinations[folder]; ok {
			if err := validateRelPath(destination); err != nil {
				return bundledTargets{}, &UnsafeTargetError{Path: destination, Reason: err.Error()}
			}
			target = destination
			result.found[target] = true
		}
		for _, other := range result.targets {
			if overlaps(target, other) {
				return bundledTargets{}, &UnsafeTargetError{Path: target, Reason: "two bundled mods would share the folder " + other}
			}
		}
		result.targets = append(result.targets, target)
		if target != folder {
			moves = append(moves, folder, target)
		}
	}
	holding := filepath.Join(work, relocateDirName)
	for i := 0; i < len(moves); i += 2 {
		if err := os.MkdirAll(holding, 0700); err != nil {
			return bundledTargets{}, err
		}
		if err := os.Rename(filepath.Join(stageGame, filepath.FromSlash(moves[i])), filepath.Join(holding, strconv.Itoa(i))); err != nil {
			return bundledTargets{}, fmt.Errorf("relocating bundled mod %s: %w", moves[i], err)
		}
	}
	for i := 0; i < len(moves); i += 2 {
		destination := filepath.Join(stageGame, filepath.FromSlash(moves[i+1]))
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return bundledTargets{}, err
		}
		if err := renameNoReplace(filepath.Join(holding, strconv.Itoa(i)), destination); err != nil {
			return bundledTargets{}, fmt.Errorf("relocating bundled mod %s: %w", moves[i], err)
		}
		in.progress("stage", "updating "+moves[i+1]+" in place")
	}
	return result, nil
}

// stagedModID reads the UniqueID of a staged bundled mod folder from its manifest.
func stagedModID(stageGame, folder string, p Payload) (string, error) {
	for rel := range p.Mods {
		segments := strings.Split(rel, "/")
		if len(segments) == 3 && segments[0]+"/"+segments[1] == folder && strings.EqualFold(segments[2], manifestFileName) {
			data, err := readSmallRegular(filepath.Join(stageGame, filepath.FromSlash(rel)), maxManifestBytes)
			if err != nil {
				return "", err
			}
			manifest, err := ParseManifest(data)
			if err != nil || manifest == nil {
				return "", fmt.Errorf("bundled mod %s has no readable manifest", folder)
			}
			return manifest.UniqueID, nil
		}
	}
	return "", fmt.Errorf("bundled mod %s has no manifest", folder)
}

// loadOriginal reads a saved original and verifies it against its recorded digest and size.
func loadOriginal(gameDir string, original OriginalFile) ([]byte, error) {
	full, err := safeGamePath(gameDir, OriginalsDir+"/"+original.Path)
	if err != nil {
		return nil, err
	}
	data, err := readSmallRegular(full, maxRestoreBytes)
	if err != nil {
		return nil, err
	}
	if got := digestBytes(data); got.SHA256 != original.SHA256 || got.Size != original.Size {
		return nil, errors.New("its content does not match the install record")
	}
	return data, nil
}

// Uninstall restores the vanilla launcher and verified originals and removes the loader files from gameDir transactionally.
func (in *Installer) Uninstall(ctx context.Context, gameDir string) error {
	unlock, err := in.begin(ctx, gameDir)
	if err != nil {
		return err
	}
	defer unlock()
	spec := in.Spec
	record, err := ReadRecord(gameDir)
	if err != nil {
		return err
	}
	if record != nil {
		if err := record.checkOwnership(spec); err != nil {
			return err
		}
	}
	restore := map[string]RestoreFile{}
	_, current, err := readVanillaLauncher(filepath.Join(gameDir, spec.LauncherName), spec)
	if err != nil {
		return err
	}
	if !current {
		data, backupVanilla, err := readVanillaLauncher(filepath.Join(gameDir, spec.LauncherBackupName), spec)
		if err != nil {
			return err
		}
		if !backupVanilla {
			return fmt.Errorf("cannot restore %s: %w", spec.LauncherName, ErrNoVanillaLauncher)
		}
		restore[spec.LauncherName] = RestoreFile{Data: data, Mode: 0755}
	}
	candidates := append(append([]string{}, spec.UninstallPaths...), spec.LauncherBackupName)
	var drop []OriginalFile
	if record != nil {
		for _, original := range record.Originals {
			data, err := loadOriginal(gameDir, original)
			if err != nil {
				in.warn(fmt.Sprintf("cannot restore saved original %s: %v; removing the loader's copy instead", original.Path, err))
				continue
			}
			restore[original.Path] = RestoreFile{Data: data, Mode: os.FileMode(original.Mode)}
			drop = append(drop, original)
		}
		for _, target := range record.Targets {
			if !isWithin(target, modsDirName) {
				candidates = append(candidates, target)
			}
		}
	}
	var remove []string
	for _, rel := range sortedUnique(candidates) {
		if rel == spec.LauncherName || overlapsAny(rel, restoreKeys(restore)) {
			continue
		}
		exists, err := checkUnder(gameDir, rel)
		if err != nil {
			return err
		}
		if exists {
			remove = append(remove, rel)
		}
	}
	op, err := in.newOperation(gameDir)
	if err != nil {
		return err
	}
	defer op.finish()
	if err := op.step("work"); err != nil {
		return err
	}
	in.progress("apply", "")
	_, err = apply(gameDir, TxnPlan{
		OpID:          op.id,
		Op:            OpUninstall,
		WorkDir:       op.work,
		Remove:        remove,
		Restore:       restore,
		DropOriginals: drop,
		Protected:     spec.protectedNames(),
		Farm:          spec.Farm,
		fault:         op.step,
		warn:          in.warn,
	})
	if err != nil {
		return err
	}
	in.progress(appliedPhase, "")
	return nil
}

// begin validates the request, takes the game lock, and refuses while recovery is pending, the build is unsupported, or a farm is active.
func (in *Installer) begin(ctx context.Context, gameDir string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := in.Spec.Validate(); err != nil {
		return nil, err
	}
	if err := checkGameDir(gameDir); err != nil {
		return nil, err
	}
	unlock, err := lockGame(gameDir)
	if err != nil {
		return nil, err
	}
	if err := in.admit(gameDir); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}

// newOperation creates the private work directory of a fresh operation.
func (in *Installer) newOperation(gameDir string) (*operation, error) {
	op := &operation{gameDir: gameDir, id: uuid.NewString(), fault: in.fault}
	op.work = filepath.Join(gameDir, WorkDir, op.id)
	if err := os.MkdirAll(filepath.Dir(op.work), 0755); err != nil {
		return nil, fmt.Errorf("creating mod-loader work directory: %w", err)
	}
	if err := os.Mkdir(op.work, 0700); err != nil {
		return nil, fmt.Errorf("creating mod-loader work directory: %w", err)
	}
	return op, nil
}

// step invokes the test fault hook and remembers a simulated crash so the work directory stays as a crash would leave it.
func (op *operation) step(name string) error {
	if op.fault == nil {
		return nil
	}
	err := op.fault(name)
	if isFault(err) {
		op.crashed = true
	}
	return err
}

// finish removes the work directory unless a crash was simulated or an intent still references it.
func (op *operation) finish() {
	if op.crashed || isRegularFile(filepath.Join(op.gameDir, IntentFile)) {
		return
	}
	_ = removeOwnedTree(op.work)
	removeIfEmpty(filepath.Dir(op.work))
}

// admit refuses an operation while a transaction is pending, the game build is unsupported, or a farm is active.
func (in *Installer) admit(gameDir string) error {
	status, err := Inspect(gameDir, in.Spec)
	if err != nil {
		return err
	}
	switch status.State {
	case StateInterrupted:
		return fmt.Errorf("%s: %w", status.Detail, ErrInterrupted)
	case StateUnsupportedBuild:
		return fmt.Errorf("%s: %w", status.Detail, ErrUnsupportedBuild)
	}
	return checkFarm(gameDir, in.Spec.Farm)
}

// warn reports a non-fatal problem through the progress callback.
func (in *Installer) warn(message string) {
	in.progress(warningPhase, message)
}

// progress reports a phase to the optional progress callback.
func (in *Installer) progress(phase, detail string) {
	if in.Progress != nil {
		in.Progress(phase, detail)
	}
}

// runner returns the configured runner or the real process runner.
func (in *Installer) runner() Runner {
	if in.Runner == nil {
		return ExecRunner{}
	}
	return in.Runner
}

// timeout returns the configured installer timeout or the default.
func (in *Installer) timeout() time.Duration {
	if in.Timeout <= 0 {
		return defaultInstallerTimeout
	}
	return in.Timeout
}

// now returns the configured clock time or the wall clock.
func (in *Installer) now() time.Time {
	if in.Now == nil {
		return time.Now()
	}
	return in.Now()
}

// installerEnv creates the redirected home and temp directories and returns the complete installer environment.
func installerEnv(work string) ([]string, error) {
	home := filepath.Join(work, "home")
	config := filepath.Join(home, ".config")
	data := filepath.Join(home, ".local", "share")
	cache := filepath.Join(home, ".cache")
	tmp := filepath.Join(work, "tmp")
	for _, dir := range []string{config, data, cache, tmp} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, fmt.Errorf("creating installer environment: %w", err)
		}
	}
	return []string{
		"PATH=" + installerPath,
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + config,
		"XDG_DATA_HOME=" + data,
		"XDG_CACHE_HOME=" + cache,
		"TMPDIR=" + tmp,
		"TERM=dumb",
		"LANG=C.UTF-8",
		"DOTNET_SYSTEM_GLOBALIZATION_INVARIANT=1",
		"DOTNET_CLI_TELEMETRY_OPTOUT=1",
	}, nil
}

// outputTail formats the last bytes of installer output for an error message.
func outputTail(output []byte) string {
	if len(output) == 0 {
		return ""
	}
	if len(output) > maxErrorOutput {
		output = output[len(output)-maxErrorOutput:]
	}
	return "\ninstaller output (tail):\n" + strings.TrimSpace(string(output))
}

// restoreKeys returns the sorted target paths of a restore map.
func restoreKeys(restore map[string]RestoreFile) []string {
	keys := make([]string, 0, len(restore))
	for rel := range restore {
		keys = append(keys, rel)
	}
	sort.Strings(keys)
	return keys
}

// overlapsAny reports whether rel overlaps any of targets.
func overlapsAny(rel string, targets []string) bool {
	for _, target := range targets {
		if overlaps(rel, target) {
			return true
		}
	}
	return false
}

// Inspect classifies the mod-loader state of gameDir and whether it matches a committed record.
func Inspect(gameDir string, spec LoaderSpec) (Status, error) {
	if err := spec.Validate(); err != nil {
		return Status{}, err
	}
	if info, err := lstatOptional(filepath.Join(gameDir, IntentFile)); err != nil || info != nil {
		if err != nil {
			return Status{}, err
		}
		return Status{State: StateInterrupted, Detail: "a mod-loader transaction was interrupted"}, nil
	}
	for _, marker := range spec.ForeignMarkers {
		if info, err := lstatOptional(filepath.Join(gameDir, filepath.FromSlash(marker))); err != nil || info != nil {
			if err != nil {
				return Status{}, err
			}
			return Status{State: StateUnsupportedBuild, Detail: "found " + marker}, nil
		}
	}
	native := false
	for _, marker := range spec.NativeMarkers {
		info, err := lstatOptional(filepath.Join(gameDir, filepath.FromSlash(marker)))
		if err != nil {
			return Status{}, err
		}
		native = native || info != nil
	}
	if !native {
		return Status{State: StateUnsupportedBuild, Detail: "no native game executable"}, nil
	}
	status := Status{}
	executable, err := lstatOptional(filepath.Join(gameDir, spec.LoaderExecutable))
	if err != nil {
		return Status{}, err
	}
	assemblyPresent := isRegularFile(filepath.Join(gameDir, loaderAssembly))
	internalInfo, err := lstatOptional(filepath.Join(gameDir, loaderInternalDir))
	if err != nil {
		return Status{}, err
	}
	launcherPresent := isRegularFile(filepath.Join(gameDir, spec.LauncherName))
	launcher, launcherErr := readSmallRegular(filepath.Join(gameDir, spec.LauncherName), maxLauncherBytes)
	mentions := launcherErr == nil && mentionsLoader(launcher, spec)
	complete := executable != nil && executable.Mode().IsRegular() && executable.Mode().Perm()&0111 != 0 &&
		assemblyPresent && internalInfo != nil && internalInfo.IsDir() &&
		isRegularFile(filepath.Join(gameDir, spec.LoaderDepsFile))
	switch {
	case executable == nil && !assemblyPresent && internalInfo == nil && !mentions:
		status.State = StateNotInstalled
		status.Detail = "SMAPI is not installed"
	case complete && !launcherPresent:
		status.State = StateIncomplete
		status.Detail = spec.LauncherName + " is missing; repair"
	case complete && mentions && depsStale(gameDir, spec):
		status.State = StateIncomplete
		status.Detail = "loader deps out of date; repair"
	case complete && mentions:
		status.State = StateOK
		status.Detail = "SMAPI is installed"
	case complete:
		status.State = StateLauncherReverted
		status.Detail = spec.LauncherName + " no longer starts " + spec.LoaderExecutable
	default:
		status.State = StateIncomplete
		status.Detail = "SMAPI files are incomplete"
	}
	record, err := ReadRecord(gameDir)
	if err != nil {
		return Status{}, err
	}
	if record != nil {
		status.Recorded = true
		status.RecordedVersion = record.Version
		matches, err := recordMatches(gameDir, record, spec)
		if err != nil {
			return Status{}, err
		}
		if matches {
			status.Managed = true
			status.Version = record.Version
		}
	}
	return status, nil
}

// depsStale reports whether the loader's copy of the game dependency file no longer matches the game's.
func depsStale(gameDir string, spec LoaderSpec) bool {
	game, err := readSmallRegular(filepath.Join(gameDir, spec.GameVersionFile), maxStateBytes)
	if err != nil {
		return false
	}
	loader, err := readSmallRegular(filepath.Join(gameDir, spec.LoaderDepsFile), maxStateBytes)
	return err != nil || !bytes.Equal(game, loader)
}

// recordMatches reports whether every recorded loader file still has its recorded content, trusting unchanged size, mode, and mtime before hashing.
func recordMatches(gameDir string, record *Record, spec LoaderSpec) (bool, error) {
	for _, file := range record.Files {
		if file.Path == userConfigRel || isWithin(file.Path, userLogsRel) || file.Path == spec.LauncherName || file.Path == spec.LauncherBackupName {
			continue
		}
		full, err := safeGamePath(gameDir, file.Path)
		if err != nil {
			var unsafe *UnsafeTargetError
			if errors.As(err, &unsafe) {
				return false, nil
			}
			return false, err
		}
		info, err := lstatOptional(full)
		if err != nil {
			return false, err
		}
		if info == nil || !info.Mode().IsRegular() || info.Size() != file.Size {
			return false, nil
		}
		if file.MTime != 0 && info.ModTime().UnixNano() == file.MTime && uint32(info.Mode().Perm()) == file.Mode {
			continue
		}
		got, _, err := hashRegular(full, false)
		if err != nil {
			return false, err
		}
		if got.SHA256 != file.SHA256 {
			return false, nil
		}
	}
	return true, nil
}

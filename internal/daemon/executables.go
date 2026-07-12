package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/tools"
	"github.com/parka/gorganizer/internal/vfs"
)

type execRun struct {
	runID  string
	gameID string
	handle *tools.ExternalLaunchHandle
}

func execToSpec(e config.Executable) dto.ExecutableSpec {
	return dto.ExecutableSpec{
		ID:                 e.ID,
		Title:              e.Title,
		ExePath:            e.ExePath,
		ToolID:             e.ToolID,
		Runner:             e.Runner,
		Args:               append([]string(nil), e.Args...),
		Environment:        cloneStringMap(e.Environment),
		WorkingDir:         e.WorkingDir,
		PrefixAppID:        e.PrefixAppID,
		OutputPolicy:       e.OutputPolicy,
		SelectedInput:      e.SelectedInput,
		NeedsVFSMounted:    e.NeedsVFSMounted,
		CaptureOutputToMod: e.CaptureOutputToMod,
		SanitizeEnv:        e.SanitizeEnv,
		ExtraRWPaths:       append([]string(nil), e.ExtraRWPaths...),
		AutoDetected:       e.AutoDetected,
	}
}

func specToExec(s dto.ExecutableSpec) config.Executable {
	return config.Executable{
		ID:                 s.ID,
		Title:              s.Title,
		ExePath:            s.ExePath,
		ToolID:             s.ToolID,
		Runner:             s.Runner,
		Args:               append([]string(nil), s.Args...),
		Environment:        cloneStringMap(s.Environment),
		WorkingDir:         s.WorkingDir,
		PrefixAppID:        s.PrefixAppID,
		OutputPolicy:       s.OutputPolicy,
		SelectedInput:      s.SelectedInput,
		NeedsVFSMounted:    s.NeedsVFSMounted,
		CaptureOutputToMod: s.CaptureOutputToMod,
		SanitizeEnv:        s.SanitizeEnv,
		ExtraRWPaths:       append([]string(nil), s.ExtraRWPaths...),
		AutoDetected:       s.AutoDetected,
	}
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// ListExecutables returns the registered executables for a game.
func (es *ExecutableService) ListExecutables(gameID string) ([]dto.ExecutableSpec, error) {
	es.s.mu.RLock()
	gc, ok := es.s.config.Games[gameID]
	es.s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	out := make([]dto.ExecutableSpec, 0, len(gc.Executables))
	for _, e := range gc.Executables {
		out = append(out, execToSpec(e))
	}
	return out, nil
}

// UpsertExecutable adds or updates an executable (assigning an ID when empty)
func (es *ExecutableService) UpsertExecutable(gameID string, spec dto.ExecutableSpec) (dto.ExecutableSpec, error) {
	if strings.TrimSpace(spec.Title) == "" || strings.TrimSpace(spec.ExePath) == "" {
		return dto.ExecutableSpec{}, fmt.Errorf("executable requires a title and exe path")
	}
	if spec.Runner != "" && spec.Runner != string(tools.RunnerProton) && spec.Runner != string(tools.RunnerNative) && spec.Runner != string(tools.RunnerJava) {
		return dto.ExecutableSpec{}, fmt.Errorf("unsupported executable runner %q", spec.Runner)
	}
	if spec.PrefixAppID < 0 {
		return dto.ExecutableSpec{}, errors.New("prefix app ID cannot be negative")
	}
	if spec.OutputPolicy != "" {
		switch tools.OutputPolicy(spec.OutputPolicy) {
		case tools.OutputNone, tools.OutputReadOnly, tools.OutputProfileSync, tools.OutputScratchImport,
			tools.OutputSelectedCopyUp, tools.OutputNamedMod, tools.OutputExclusiveSourceEdit:
		default:
			return dto.ExecutableSpec{}, fmt.Errorf("unsupported output policy %q", spec.OutputPolicy)
		}
	}
	for key, value := range spec.Environment {
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "=\x00\r\n") || strings.ContainsRune(value, '\x00') {
			return dto.ExecutableSpec{}, fmt.Errorf("invalid environment entry %q", key)
		}
	}
	es.s.mu.Lock()
	defer es.s.mu.Unlock()
	gc, ok := es.s.config.Games[gameID]
	if !ok {
		return dto.ExecutableSpec{}, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	e := specToExec(spec)
	if e.ToolID != "" {
		entry, trusted := tools.ValidateCatalogMatch(e.ToolID, gameID, e.ExePath)
		if !trusted {
			e.ToolID = ""
			e.AutoDetected = false
		} else {
			e.Runner = string(entry.Runner)
			e.PrefixAppID = entry.PrefixAppID
			e.OutputPolicy = string(entry.OutputPolicy)
			e.NeedsVFSMounted = entry.NeedsVFSMounted
			if entry.WorkingDirGameRoot {
				e.WorkingDir = gc.InstallPath
				if gc.LinkedFromGameID != "" {
					if parent, exists := es.s.config.Games[gc.LinkedFromGameID]; exists {
						e.WorkingDir = parent.InstallPath
					}
				}
			}
		}
	}
	if e.ID == "" {
		e.ID = "exe-" + uuid.NewString()
	}
	replaced := false
	for i := range gc.Executables {
		if gc.Executables[i].ID == e.ID {
			gc.Executables[i] = e
			replaced = true
			break
		}
	}
	if !replaced {
		gc.Executables = append(gc.Executables, e)
	}
	es.s.config.Games[gameID] = gc
	if err := es.s.config.Save(); err != nil {
		return dto.ExecutableSpec{}, fmt.Errorf("saving config: %w", err)
	}
	return execToSpec(e), nil
}

// RemoveExecutable deletes an executable by id.
func (es *ExecutableService) RemoveExecutable(gameID, id string) error {
	es.s.mu.Lock()
	defer es.s.mu.Unlock()
	gc, ok := es.s.config.Games[gameID]
	if !ok {
		return fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	kept := gc.Executables[:0]
	found := false
	for _, e := range gc.Executables {
		if e.ID == id {
			found = true
			continue
		}
		kept = append(kept, e)
	}
	if !found {
		return fmt.Errorf("executable %q not found for %s", id, gameID)
	}
	gc.Executables = kept
	es.s.config.Games[gameID] = gc
	return es.s.config.Save()
}

// DetectExecutables scans the game's base (Data.orig when mounted, else Data)
func (es *ExecutableService) DetectExecutables(gameID string) ([]dto.DetectedExecutable, error) {
	es.s.mu.RLock()
	gc, ok := es.s.config.Games[gameID]
	mm := es.s.mountMgrs[gameID]
	ms := es.s.mountStates[gameID]
	es.s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}

	roots := es.executableScanRoots(gameID, gc, mm, ms.profileName)
	found := tools.DetectExecutablesForGame(gameID, roots)
	out := make([]dto.DetectedExecutable, 0, len(found))
	for _, f := range found {
		exePath := es.resolveDetectedExecutablePath(gameID, gc, mm, f.ExePath)
		out = append(out, dto.DetectedExecutable{
			ToolID:             f.CatalogID,
			Title:              f.Title,
			ExePath:            exePath,
			Runner:             string(f.Runner),
			PrefixAppID:        f.PrefixAppID,
			OutputPolicy:       string(f.OutputPolicy),
			NeedsVFSMounted:    f.NeedsVFSMounted,
			CaptureOutputToMod: f.CaptureOutputToMod,
			ExtraRWScratch:     f.ExtraRWScratch,
			DefaultArgs:        append([]string(nil), f.DefaultArgs...),
		})
	}
	return out, nil
}

func (es *ExecutableService) resolveDetectedExecutablePath(gameID string, gc config.GameConfig, mm *vfs.MountManager, detected string) string {
	dataPath := filepath.Join(gc.InstallPath, gc.DataSubpath)
	if gc.DataSubpath == "" {
		dataPath = filepath.Join(gc.InstallPath, "Data")
	}
	if mm != nil && mm.IsMounted() {
		if rel, err := filepath.Rel(mm.BackupPath(), detected); err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			candidate := filepath.Join(dataPath, rel)
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}
	rel, err := filepath.Rel(config.ModsDir(gameID), detected)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return detected
	}
	parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
	if len(parts) < 2 {
		return detected
	}
	modRelative := filepath.Join(parts[1:]...)
	if len(parts) >= 3 && strings.EqualFold(parts[1], vfs.RootContentDirName) {
		candidate := filepath.Join(gc.InstallPath, filepath.Join(parts[2:]...))
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		return detected
	}
	candidate := filepath.Join(dataPath, modRelative)
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return detected
}

// executableScanRoots returns the directories to scan for tool executables: the
func (es *ExecutableService) executableScanRoots(gameID string, gc config.GameConfig, mm *vfs.MountManager, profileName string) []string {
	subpath := gc.DataSubpath
	if subpath == "" {
		subpath = "Data"
	}
	base := filepath.Join(gc.InstallPath, subpath)
	if mm != nil && mm.IsMounted() {
		base = mm.BackupPath()
	}
	roots := []string{gc.InstallPath, base, config.ToolsDir()}
	modsDir := config.ModsDir(gameID)
	if _, entries, err := es.s.profileMgr.Load(gameID, profileName); err == nil {
		for _, e := range entries {
			if e.Enabled {
				roots = append(roots, filepath.Join(modsDir, e.Name))
			}
		}
	}
	return roots
}

// LaunchExecutable runs a registered tool through the game's Proton prefix
func (es *ExecutableService) LaunchExecutable(gameID, execID, profileName string, requestedAutoSort ...bool) (int, string, error) {
	es.s.execLaunchMu.Lock()
	defer es.s.execLaunchMu.Unlock()
	if err := es.s.awaitRecovery(); err != nil {
		return 0, "", err
	}
	if pending := es.s.recoveryPendingFor(gameID); pending != nil {
		return 0, "", fmt.Errorf("recovery pending for %s: %s", gameID, pending.Reason)
	}
	if conflict := es.s.findMutexConflict(gameID); conflict != "" {
		return 0, "", &VFSMutexError{GameID: gameID, Conflicting: conflict, Group: mutexGroupOf(gameID)}
	}
	if es.s.mountBusy(gameID) {
		return 0, "", fmt.Errorf("cannot launch a tool while %s or another managed tool is running", gameID)
	}

	es.s.mu.RLock()
	gc, ok := es.s.config.Games[gameID]
	es.s.mu.RUnlock()
	if !ok {
		return 0, "", fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	var exe *config.Executable
	for i := range gc.Executables {
		if gc.Executables[i].ID == execID {
			e := gc.Executables[i]
			exe = &e
			break
		}
	}
	if exe == nil {
		return 0, "", fmt.Errorf("executable %q not found for %s", execID, gameID)
	}
	catalogEntry, trustedCatalogEntry := tools.ValidateCatalogMatch(exe.ToolID, gameID, exe.ExePath)
	trustedToolID := ""
	if trustedCatalogEntry {
		trustedToolID = exe.ToolID
	}
	outputPolicy := tools.OutputPolicy(exe.OutputPolicy)
	if outputPolicy == "" && exe.CaptureOutputToMod != "" {
		outputPolicy = tools.OutputNamedMod
	}

	eff := gc
	if gc.LinkedFromGameID != "" {
		e2, err := es.s.config.EffectiveGameConfig(gameID)
		if err != nil {
			return 0, "", err
		}
		eff = e2
	}
	prefixReserved := false
	prefixHandedOff := false
	releasePrefix := func() {}
	if runner := tools.RunnerKind(exe.Runner); runner != tools.RunnerNative && runner != tools.RunnerJava {
		var reserveErr error
		releasePrefix, reserveErr = es.s.toolMgr.ReservePrefix(&eff, exe.PrefixAppID)
		if reserveErr != nil {
			return 0, "", reserveErr
		}
		prefixReserved = true
	}
	defer func() {
		if prefixReserved && !prefixHandedOff {
			releasePrefix()
		}
	}()
	if err := es.ensureExecutableRuntime(gameID, eff, *exe); err != nil {
		return 0, "", err
	}
	profileSyncCompatData := ""
	if outputPolicy == tools.OutputProfileSync && trustedToolID != "loot" && profileName != "" {
		profileSyncCompatData, _ = tools.ResolveCompatDataPath(&eff, exe.PrefixAppID)
		if _, err := es.s.iniMgr.PushToDocumentsAt(gameID, profileName, eff.SteamAppID, profileSyncCompatData); err != nil {
			return 0, "", fmt.Errorf("preparing profile INIs for tool: %w", err)
		}
	}

	mm := es.s.ensureMountManager(gameID, eff)
	if outputPolicy == tools.OutputExclusiveSourceEdit && mm.IsMounted() {
		return 0, "", errors.New("exclusive source-edit tools require the game VFS to be unmounted")
	}
	if exe.NeedsVFSMounted {
		if !mm.IsMounted() && profileName != "" {
			if _, err := es.s.svc.vfs.MountVFS(gameID, profileName); err != nil {
				return 0, "", fmt.Errorf("mounting VFS for tool: %w", err)
			}
		}
		if mm.IsMounted() && mm.IsDirty() && !es.s.mountBusy(gameID) {
			if err := es.s.svc.vfs.RebuildVFS(gameID); err != nil {
				return 0, "", fmt.Errorf("applying pending mod changes before tool launch: %w", err)
			}
		}
		if mm.IsMounted() {
			if err := es.s.applyRootDeployment(gameID, eff, profileName); err != nil {
				return 0, "", fmt.Errorf("applying game-root deployment before tool launch: %w", err)
			}
		}
	}

	dataPath := mm.DataPath()
	resolvedExePath := es.resolveDetectedExecutablePath(gameID, eff, mm, exe.ExePath)
	modsDir := config.ModsDir(gameID)
	overwriteRoot := filepath.Join(modsDir, "Overwrite")

	captureRoot := overwriteRoot
	captureIsOverwrite := true
	if exe.CaptureOutputToMod != "" && !strings.EqualFold(exe.CaptureOutputToMod, "Overwrite") {
		captureRoot = filepath.Join(modsDir, exe.CaptureOutputToMod)
		captureIsOverwrite = false
		if err := os.MkdirAll(captureRoot, 0755); err != nil {
			return 0, "", fmt.Errorf("creating capture mod %q: %w", exe.CaptureOutputToMod, err)
		}
		es.s.svc.mods.ensureInModList(gameID, exe.CaptureOutputToMod)
	}

	if outputPolicy == tools.OutputNamedMod && mm.IsMounted() && !captureIsOverwrite {
		if err := prepareWritableOutputLayer(dataPath, captureRoot); err != nil {
			return 0, "", fmt.Errorf("preparing writable output mod: %w", err)
		}
	}
	if outputPolicy == tools.OutputSelectedCopyUp && mm.IsMounted() && exe.SelectedInput != "" {
		if err := copyUpSelectedInput(dataPath, captureRoot, exe.SelectedInput); err != nil {
			return 0, "", fmt.Errorf("copying selected tool input into output mod: %w", err)
		}
	}
	runID := "run-" + uuid.NewString()
	var lootWorkspace *tools.LOOTWorkspace
	if trustedToolID == "loot" {
		if profileName == "" {
			return 0, "", fmt.Errorf("LOOT requires an active profile")
		}
		if gameID != "morrowind" {
			if err := es.s.svc.launch.writePluginsTxt(gameID, eff, profileName); err != nil {
				return 0, "", fmt.Errorf("preparing plugin state for LOOT: %w", err)
			}
		}
		library, err := tools.ResolveSteamLibrary(&eff)
		if err != nil {
			return 0, "", fmt.Errorf("resolving Steam library for LOOT: %w", err)
		}
		lootWorkspace, err = tools.BuildLOOTWorkspace(tools.LOOTWorkspaceOptions{
			SteamLibrary: library, AppID: eff.SteamAppID, RunID: runID, GameID: gameID,
			InstallPath: eff.InstallPath, DataPath: dataPath, DataSubpath: eff.DataSubpath,
		})
		if err != nil {
			return 0, "", err
		}
		if gameID == "morrowind" {
			if err := es.s.svc.launch.writePluginsTxt(gameID, eff, profileName, lootWorkspace.GameRoot); err != nil {
				_ = lootWorkspace.Remove()
				return 0, "", fmt.Errorf("preparing Morrowind plugin state for LOOT: %w", err)
			}
		}
	}

	profileDir := es.s.profileMgr.ProfileDir(gameID, profileName)
	scratchRoot := filepath.Join(config.CacheDir(), "tool-scratch", gameID, runID)
	scratchOutputRoot := filepath.Join(scratchRoot, "output")
	scratchTempRoot := filepath.Join(scratchRoot, "temp")
	usesScratch := outputPolicy == tools.OutputScratchImport || (trustedCatalogEntry && catalogEntry.ExtraRWScratch)
	if usesScratch {
		if err := os.MkdirAll(scratchTempRoot, 0700); err != nil {
			if lootWorkspace != nil {
				_ = lootWorkspace.Remove()
			}
			return 0, "", fmt.Errorf("creating tool scratch directory: %w", err)
		}
	}
	toolOutputRoot := captureRoot
	if outputPolicy == tools.OutputScratchImport {
		if err := os.MkdirAll(scratchOutputRoot, 0700); err != nil {
			_ = os.RemoveAll(scratchRoot)
			return 0, "", fmt.Errorf("creating private tool output directory: %w", err)
		}
		toolOutputRoot = scratchOutputRoot
	}
	winePath := func(path string) string {
		translated, err := es.s.toolMgr.WineTranslatePath(gameID, &eff, path)
		if err != nil {
			return path
		}
		return translated
	}
	repl := strings.NewReplacer(
		"%GAME_DIR%", eff.InstallPath,
		"%DATA_DIR%", dataPath,
		"%MODS_DIR%", modsDir,
		"%OVERWRITE%", overwriteRoot,
		"%PROFILE_DIR%", profileDir,
		"%OUTPUT_DIR%", toolOutputRoot,
		"%SCRATCH_DIR%", scratchTempRoot,
		"%WIN_GAME_DIR%", winePath(eff.InstallPath),
		"%WIN_DATA_DIR%", winePath(dataPath),
		"%WIN_MODS_DIR%", winePath(modsDir),
		"%WIN_PROFILE_DIR%", winePath(profileDir),
		"%WIN_OUTPUT_DIR%", winePath(toolOutputRoot),
		"%WIN_SCRATCH_DIR%", winePath(scratchTempRoot),
		"%WIN_TOOL_DIR%", winePath(filepath.Dir(resolvedExePath)),
	)
	expand := func(s string) string {
		s = repl.Replace(s)
		for {
			i := strings.Index(s, "%WIN:")
			if i < 0 {
				break
			}
			rest := s[i+len("%WIN:"):]
			end := strings.Index(rest, "%")
			if end < 0 {
				break
			}
			raw := rest[:end]
			win, err := es.s.toolMgr.WineTranslatePath(gameID, &eff, raw)
			if err != nil {
				win = raw
			}
			s = s[:i] + win + rest[end+1:]
		}
		return s
	}

	args := make([]string, 0, len(exe.Args))
	for _, a := range exe.Args {
		args = append(args, expand(a))
	}
	if lootWorkspace != nil {
		lootGameID, _ := tools.LOOTGameID(gameID)
		gamePath, err := es.s.toolMgr.WineTranslatePath(gameID, &eff, lootWorkspace.GameRoot)
		if err != nil {
			_ = lootWorkspace.Remove()
			return 0, "", fmt.Errorf("translating LOOT game path: %w", err)
		}
		lootData := config.ToolDataDir("loot", gameID, profileName)
		if err := os.MkdirAll(lootData, 0755); err != nil {
			_ = lootWorkspace.Remove()
			return 0, "", fmt.Errorf("creating LOOT data directory: %w", err)
		}
		lootDataPath, err := es.s.toolMgr.WineTranslatePath(gameID, &eff, lootData)
		if err != nil {
			_ = lootWorkspace.Remove()
			return 0, "", fmt.Errorf("translating LOOT data path: %w", err)
		}
		autoSort := len(requestedAutoSort) > 0 && requestedAutoSort[0]
		for _, arg := range args {
			if arg == "--auto-sort" {
				autoSort = true
			}
		}
		if autoSort && !tools.LOOTAutoSortSupported(gameID) {
			_ = lootWorkspace.Remove()
			return 0, "", errors.New("automatic LOOT sorting is disabled for Tale of Two Wastelands")
		}
		args = []string{"--game=" + lootGameID, "--game-path=" + gamePath, "--loot-data-path=" + lootDataPath}
		if autoSort {
			args = append(args, "--auto-sort")
		}
		dataPath = lootWorkspace.DataPath
	}
	workDir := ""
	if exe.WorkingDir != "" {
		workDir = expand(exe.WorkingDir)
	}

	rwPaths := make([]string, 0)
	if outputPolicy == "" || outputPolicy == tools.OutputNamedMod || outputPolicy == tools.OutputSelectedCopyUp {
		rwPaths = append(rwPaths, captureRoot)
	}
	roPaths := []string{filepath.Dir(resolvedExePath), modsDir}
	dataWritable := outputPolicy == "" || outputPolicy == tools.OutputProfileSync ||
		outputPolicy == tools.OutputNamedMod || outputPolicy == tools.OutputSelectedCopyUp
	if dataWritable {
		rwPaths = append(rwPaths, dataPath)
	} else {
		roPaths = append(roPaths, dataPath)
	}
	if outputPolicy == "" && lootWorkspace == nil {
		rwPaths = append(rwPaths, eff.InstallPath)
	} else {
		roPaths = append(roPaths, eff.InstallPath)
	}
	for _, path := range exe.ExtraRWPaths {
		rwPaths = append(rwPaths, expand(path))
	}
	if usesScratch {
		rwPaths = append(rwPaths, scratchRoot)
	}
	extraEnv := make([]string, 0, len(exe.Environment))
	envKeys := make([]string, 0, len(exe.Environment))
	for key := range exe.Environment {
		envKeys = append(envKeys, key)
	}
	sort.Strings(envKeys)
	for _, key := range envKeys {
		extraEnv = append(extraEnv, key+"="+expand(exe.Environment[key]))
	}
	if usesScratch {
		winScratch := winePath(scratchTempRoot)
		extraEnv = append(extraEnv, "TMP="+winScratch, "TEMP="+winScratch)
	}

	var handle *tools.ExternalLaunchHandle
	var err error
	switch tools.RunnerKind(exe.Runner) {
	case tools.RunnerNative, tools.RunnerJava:
		handle, err = es.s.toolMgr.LaunchNative(tools.NativeLaunchOpts{
			ToolID: trustedToolID, ExePath: expand(resolvedExePath), Args: args,
			ExtraEnv: extraEnv, WorkingDir: workDir, JavaArchive: tools.RunnerKind(exe.Runner) == tools.RunnerJava,
			SanitizeEnv: exe.SanitizeEnv,
		})
	default:
		handle, err = es.s.toolMgr.LaunchExternalWithOptions(tools.ExternalLaunchOpts{
			PrefixGameID: gameID, GameCfg: &eff, ExePath: expand(resolvedExePath), Args: args,
			ExtraEnv: extraEnv, PreferredProton: es.s.config.PreferredProton,
			SanitizeEnv: exe.SanitizeEnv, RWPaths: rwPaths, WorkingDir: workDir,
			PrefixAppID: exe.PrefixAppID, ROPaths: roPaths, PrefixReserved: prefixReserved,
		})
	}
	if err != nil {
		if lootWorkspace != nil {
			_ = lootWorkspace.Remove()
		}
		if usesScratch {
			if cleanupErr := os.RemoveAll(scratchRoot); cleanupErr != nil {
				slog.Warn("cleaning tool scratch directory failed", "run", runID, "err", cleanupErr)
			}
		}
		return 0, "", err
	}

	run := &execRun{runID: runID, gameID: gameID, handle: handle}
	es.s.execRunsMu.Lock()
	es.s.execRuns[runID] = run
	es.s.execRunsMu.Unlock()

	es.s.emitInfo(fmt.Sprintf("[%s:start] %s", runID, exe.Title))
	slog.Info("external tool launched", "game", gameID, "tool", exe.Title, "run", runID, "pid", handle.PID)

	needsCapture := exe.NeedsVFSMounted && (outputPolicy == "" || outputPolicy == tools.OutputNamedMod || outputPolicy == tools.OutputSelectedCopyUp)
	prefixHandedOff = prefixReserved
	go func() {
		if prefixReserved {
			defer releasePrefix()
		}
		<-handle.Done
		code := <-handle.ExitCode
		scratchImportFailed := false
		if code == 0 && lootWorkspace != nil {
			if importErr := es.importLOOTLoadout(gameID, profileName, eff, lootWorkspace); importErr != nil {
				slog.Warn("importing LOOT result failed", "run", runID, "err", importErr)
				es.s.emitInfo(fmt.Sprintf("[%s:loot-import] failed: %v", runID, importErr))
				code = -1
			} else {
				es.s.emitInfo(fmt.Sprintf("[%s:loot-import] profile loadout updated", runID))
			}
		}
		if code == 0 && outputPolicy == tools.OutputProfileSync && trustedToolID != "loot" && profileName != "" {
			if importErr := es.s.iniMgr.PullFromDocumentsAt(gameID, profileName, eff.SteamAppID, profileSyncCompatData); importErr != nil {
				slog.Warn("importing tool-edited profile INIs failed", "run", runID, "err", importErr)
				es.s.emitInfo(fmt.Sprintf("[%s:ini-import] failed: %v", runID, importErr))
				code = -1
			}
		}
		if code == 0 && outputPolicy == tools.OutputScratchImport {
			imported, importErr := importScratchOutput(scratchOutputRoot, captureRoot, captureIsOverwrite)
			if importErr != nil {
				scratchImportFailed = true
				code = -1
				slog.Warn("importing tool scratch output failed", "run", runID, "scratch", scratchRoot, "err", importErr)
				es.s.emitInfo(fmt.Sprintf("[%s:scratch-import] failed; output preserved at %s: %v", runID, scratchRoot, importErr))
			} else {
				if imported > 0 {
					es.s.emitInfo(fmt.Sprintf("[%s:scratch-import] %d files → %s", runID, imported, filepath.Base(captureRoot)))
				}
				if mm.IsMounted() && profileName != "" {
					_, entries, loadErr := es.s.profileMgr.Load(gameID, profileName)
					if loadErr == nil {
						loadErr = mm.MarkDirty(es.s.svc.vfs.buildLayers(gameID, eff, entries))
					}
					if loadErr != nil {
						code = -1
						slog.Warn("marking VFS dirty after scratch import failed", "run", runID, "err", loadErr)
						es.s.emitInfo(fmt.Sprintf("[%s:scratch-import] imported, but VFS refresh failed: %v", runID, loadErr))
					}
				}
			}
		}
		if lootWorkspace != nil {
			if cleanupErr := lootWorkspace.Remove(); cleanupErr != nil {
				slog.Warn("cleaning LOOT workspace failed", "run", runID, "err", cleanupErr)
			}
		}
		if usesScratch && !scratchImportFailed {
			if cleanupErr := os.RemoveAll(scratchRoot); cleanupErr != nil {
				slog.Warn("cleaning tool scratch directory failed", "run", runID, "err", cleanupErr)
			}
		}
		if code == 0 && needsCapture && mm.IsMounted() {
			if moved, capErr := vfs.CaptureNewFilesInto(dataPath, captureRoot, true, captureIsOverwrite); capErr != nil {
				code = -1
				slog.Warn("capturing tool output failed", "run", runID, "err", capErr)
				es.s.emitInfo(fmt.Sprintf("[%s:capture] failed: %v", runID, capErr))
			} else if moved > 0 {
				es.s.emitInfo(fmt.Sprintf("[%s:capture] %d files → %s", runID, moved, filepath.Base(captureRoot)))
			}
		}
		es.s.execRunsMu.Lock()
		delete(es.s.execRuns, runID)
		es.s.execRunsMu.Unlock()
		es.s.emitInfo(fmt.Sprintf("[%s:exit] code=%d", runID, code))
		slog.Info("external tool exited", "run", runID, "code", code)
	}()

	return handle.PID, runID, nil
}

// CancelExecutable requests a Wine-aware shutdown of a running tool.
func (es *ExecutableService) CancelExecutable(runID string) error {
	es.s.execRunsMu.Lock()
	run, ok := es.s.execRuns[runID]
	es.s.execRunsMu.Unlock()
	if !ok {
		return fmt.Errorf("no running tool with id %q", runID)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run.handle.Cancel(ctx)
	return nil
}

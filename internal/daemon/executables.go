package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/ipc"
	"github.com/parka/gorganizer/internal/tools"
	"github.com/parka/gorganizer/internal/vfs"
)

// execRun tracks a single in-flight external-tool launch.
type execRun struct {
	runID  string
	gameID string
	handle *tools.ExternalLaunchHandle
}

func execToSpec(e config.Executable) ipc.ExecutableSpec {
	return ipc.ExecutableSpec{
		ID:                 e.ID,
		Title:              e.Title,
		ExePath:            e.ExePath,
		Args:               append([]string(nil), e.Args...),
		WorkingDir:         e.WorkingDir,
		NeedsVFSMounted:    e.NeedsVFSMounted,
		CaptureOutputToMod: e.CaptureOutputToMod,
		SanitizeEnv:        e.SanitizeEnv,
		ExtraRWPaths:       append([]string(nil), e.ExtraRWPaths...),
		AutoDetected:       e.AutoDetected,
	}
}

func specToExec(s ipc.ExecutableSpec) config.Executable {
	return config.Executable{
		ID:                 s.ID,
		Title:              s.Title,
		ExePath:            s.ExePath,
		Args:               append([]string(nil), s.Args...),
		WorkingDir:         s.WorkingDir,
		NeedsVFSMounted:    s.NeedsVFSMounted,
		CaptureOutputToMod: s.CaptureOutputToMod,
		SanitizeEnv:        s.SanitizeEnv,
		ExtraRWPaths:       append([]string(nil), s.ExtraRWPaths...),
		AutoDetected:       s.AutoDetected,
	}
}

// ListExecutables returns the registered executables for a game.
func (d *Daemon) ListExecutables(gameID string) ([]ipc.ExecutableSpec, error) {
	d.mu.RLock()
	gc, ok := d.config.Games[gameID]
	d.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	out := make([]ipc.ExecutableSpec, 0, len(gc.Executables))
	for _, e := range gc.Executables {
		out = append(out, execToSpec(e))
	}
	return out, nil
}

// UpsertExecutable adds or updates an executable (assigning an ID when empty)
// and persists the config atomically.
func (d *Daemon) UpsertExecutable(gameID string, spec ipc.ExecutableSpec) (ipc.ExecutableSpec, error) {
	if strings.TrimSpace(spec.Title) == "" || strings.TrimSpace(spec.ExePath) == "" {
		return ipc.ExecutableSpec{}, fmt.Errorf("executable requires a title and exe path")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	gc, ok := d.config.Games[gameID]
	if !ok {
		return ipc.ExecutableSpec{}, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	e := specToExec(spec)
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
	d.config.Games[gameID] = gc
	if err := d.config.Save(); err != nil {
		return ipc.ExecutableSpec{}, fmt.Errorf("saving config: %w", err)
	}
	return execToSpec(e), nil
}

// RemoveExecutable deletes an executable by id.
func (d *Daemon) RemoveExecutable(gameID, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	gc, ok := d.config.Games[gameID]
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
	d.config.Games[gameID] = gc
	return d.config.Save()
}

// DetectExecutables scans the game's base (Data.orig when mounted, else Data)
// plus enabled mod folders for known modding tools.
func (d *Daemon) DetectExecutables(gameID string) ([]ipc.DetectedExecutable, error) {
	d.mu.RLock()
	gc, ok := d.config.Games[gameID]
	mm := d.mountMgrs[gameID]
	ms := d.mountStates[gameID]
	d.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}

	roots := d.executableScanRoots(gameID, gc, mm, ms.profileName)
	found := tools.DetectExecutables(roots)
	out := make([]ipc.DetectedExecutable, 0, len(found))
	for _, f := range found {
		out = append(out, ipc.DetectedExecutable{
			Title:              f.Title,
			ExePath:            f.ExePath,
			NeedsVFSMounted:    f.NeedsVFSMounted,
			CaptureOutputToMod: f.CaptureOutputToMod,
		})
	}
	return out, nil
}

// executableScanRoots returns the directories to scan for tool executables: the
// vanilla base (backup while mounted) and each enabled mod folder.
func (d *Daemon) executableScanRoots(gameID string, gc config.GameConfig, mm *vfs.MountManager, profileName string) []string {
	subpath := gc.DataSubpath
	if subpath == "" {
		subpath = "Data"
	}
	base := filepath.Join(gc.InstallPath, subpath)
	if mm != nil && mm.IsMounted() {
		base = mm.BackupPath()
	}
	roots := []string{base}
	modsDir := config.ModsDir(gameID)
	if _, entries, err := d.profileMgr.Load(gameID, profileName); err == nil {
		for _, e := range entries {
			if e.Enabled {
				roots = append(roots, filepath.Join(modsDir, e.Name))
			}
		}
	}
	return roots
}

// LaunchExecutable runs a registered tool through the game's Proton prefix
// against the mounted farm, and captures its new output into a mod on exit.
// Returns the tool PID and a run id (for cancel / progress).
func (d *Daemon) LaunchExecutable(gameID, execID, profileName string) (int, string, error) {
	if err := d.awaitRecovery(); err != nil {
		return 0, "", err
	}
	if pending := d.recoveryPendingFor(gameID); pending != nil {
		return 0, "", fmt.Errorf("recovery pending for %s: %s", gameID, pending.Reason)
	}
	// A tool holds the farm like a game launch — honor the FNV/TTW mutex group.
	if conflict := d.findMutexConflict(gameID); conflict != "" {
		return 0, "", &ipc.VFSMutexError{GameID: gameID, Conflicting: conflict, Group: mutexGroupOf(gameID)}
	}

	d.mu.RLock()
	gc, ok := d.config.Games[gameID]
	d.mu.RUnlock()
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

	eff := gc
	if gc.LinkedFromGameID != "" {
		e2, err := d.config.EffectiveGameConfig(gameID)
		if err != nil {
			return 0, "", err
		}
		eff = e2
	}

	mm := d.ensureMountManager(gameID, eff)
	if exe.NeedsVFSMounted {
		if !mm.IsMounted() && profileName != "" {
			if _, err := d.MountVFS(gameID, profileName); err != nil {
				return 0, "", fmt.Errorf("mounting VFS for tool: %w", err)
			}
		}
		// Apply pending mod changes so the tool sees the current layout — but
		// never swap the farm out from under an already-running reader (R5-tool).
		if mm.IsMounted() && mm.IsDirty() && !d.mountBusy(gameID) {
			if err := mm.ReMaterialize(); err != nil {
				return 0, "", fmt.Errorf("applying pending mod changes before tool launch: %w", err)
			}
		}
	}

	dataPath := mm.DataPath()
	modsDir := config.ModsDir(gameID)
	overwriteRoot := filepath.Join(modsDir, "Overwrite")

	// Resolve the capture target.
	captureRoot := overwriteRoot
	captureIsOverwrite := true
	if exe.CaptureOutputToMod != "" && !strings.EqualFold(exe.CaptureOutputToMod, "Overwrite") {
		captureRoot = filepath.Join(modsDir, exe.CaptureOutputToMod)
		captureIsOverwrite = false
		if err := os.MkdirAll(captureRoot, 0755); err != nil {
			return 0, "", fmt.Errorf("creating capture mod %q: %w", exe.CaptureOutputToMod, err)
		}
		// Surface the output mod in the list so the user can enable/order it.
		d.ensureInModList(gameID, exe.CaptureOutputToMod)
	}

	repl := strings.NewReplacer(
		"%GAME_DIR%", eff.InstallPath,
		"%DATA_DIR%", dataPath,
		"%MODS_DIR%", modsDir,
		"%OVERWRITE%", overwriteRoot,
	)
	expand := func(s string) string {
		s = repl.Replace(s)
		// %WIN:<path>% => Windows path inside the prefix.
		for {
			i := strings.Index(s, "%WIN:")
			if i < 0 {
				break
			}
			j := strings.Index(s[i:], "%")
			end := strings.Index(s[i+1:], "%")
			if end < 0 {
				break
			}
			_ = j
			raw := s[i+len("%WIN:") : i+1+end]
			win, err := d.toolMgr.WineTranslatePath(gameID, &eff, raw)
			if err != nil {
				win = raw
			}
			s = s[:i] + win + s[i+1+end+1:]
		}
		return s
	}

	args := make([]string, 0, len(exe.Args))
	for _, a := range exe.Args {
		args = append(args, expand(a))
	}
	workDir := ""
	if exe.WorkingDir != "" {
		workDir = expand(exe.WorkingDir)
	}

	rwPaths := append([]string{dataPath, eff.InstallPath, captureRoot}, exe.ExtraRWPaths...)

	handle, err := d.toolMgr.LaunchExternalWithOptions(tools.ExternalLaunchOpts{
		PrefixGameID:    gameID,
		GameCfg:         &eff,
		ExePath:         exe.ExePath,
		Args:            args,
		PreferredProton: d.config.PreferredProton,
		SanitizeEnv:     exe.SanitizeEnv,
		RWPaths:         rwPaths,
		WorkingDir:      workDir,
	})
	if err != nil {
		return 0, "", err
	}

	runID := "run-" + uuid.NewString()
	run := &execRun{runID: runID, gameID: gameID, handle: handle}
	d.execRunsMu.Lock()
	d.execRuns[runID] = run
	d.execRunsMu.Unlock()

	d.emitInfo(fmt.Sprintf("[%s:start] %s", runID, exe.Title))
	slog.Info("external tool launched", "game", gameID, "tool", exe.Title, "run", runID, "pid", handle.PID)

	needsCapture := exe.NeedsVFSMounted
	go func() {
		<-handle.Done
		code := <-handle.ExitCode
		if code == 0 && needsCapture && mm.IsMounted() {
			// The tool's fresh output is nlink==1; move it into the capture mod
			// and re-link it into the live farm so it stays visible (§5.4).
			if moved, capErr := vfs.CaptureNewFilesInto(dataPath, captureRoot, true, captureIsOverwrite); capErr != nil {
				slog.Warn("capturing tool output failed", "run", runID, "err", capErr)
				d.emitInfo(fmt.Sprintf("[%s:capture] failed: %v", runID, capErr))
			} else if moved > 0 {
				d.emitInfo(fmt.Sprintf("[%s:capture] %d files → %s", runID, moved, filepath.Base(captureRoot)))
			}
		}
		d.execRunsMu.Lock()
		delete(d.execRuns, runID)
		d.execRunsMu.Unlock()
		d.emitInfo(fmt.Sprintf("[%s:exit] code=%d", runID, code))
		slog.Info("external tool exited", "run", runID, "code", code)
	}()

	return handle.PID, runID, nil
}

// CancelExecutable requests a Wine-aware shutdown of a running tool.
func (d *Daemon) CancelExecutable(runID string) error {
	d.execRunsMu.Lock()
	run, ok := d.execRuns[runID]
	d.execRunsMu.Unlock()
	if !ok {
		return fmt.Errorf("no running tool with id %q", runID)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	run.handle.Cancel(ctx)
	return nil
}

// emitInfo publishes a status Info line (best-effort, non-blocking).
func (d *Daemon) emitInfo(msg string) {
	select {
	case d.statusCh <- ipc.StatusEventResult{Info: msg}:
	default:
	}
}

package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/gamedef"
	"github.com/parka/gorganizer/internal/ghrelease"
	"github.com/parka/gorganizer/internal/smapi"
	"github.com/parka/gorganizer/internal/vfs"
)

const (
	loaderLatestTTL        = time.Hour
	loaderShutdownWait     = 10 * time.Second
	loaderRecoveryPrefix   = "mod loader transaction: "
	loaderReleaseAPIPrefix = "https://api.github.com/repos/"
	loaderReleaseAPISuffix = "/releases/latest"
	loaderUserAgentPrefix  = "gorganizer-managed-"
)

var loaderToolNames = map[gamedef.ModLoaderKind]string{
	gamedef.ModLoaderSMAPI: "smapi",
}

type loaderEngine interface {
	Inspect(gameDir string) (smapi.Status, error)
	Install(ctx context.Context, gameDir string, art smapi.Artifact, progress func(phase, detail string)) (*smapi.Record, error)
	Uninstall(ctx context.Context, gameDir string, progress func(phase, detail string)) error
	Recover(gameDir string) (smapi.RecoverResult, error)
}

type loaderArtifacts interface {
	Latest(ctx context.Context) (ghrelease.Release, error)
	Fetch(ctx context.Context, rel ghrelease.Release) (smapi.Artifact, error)
	Artifact(version string) (smapi.Artifact, error)
	ActiveVersion() (string, error)
	PreviousVersion() (string, error)
	Activate(version string) (ghrelease.Current, error)
	Rollback() (ghrelease.Current, error)
}

type smapiEngine struct {
	spec smapi.LoaderSpec
}

type latestRelease struct {
	version   string
	fetchedAt time.Time
}

type loaderOp struct {
	ctx          context.Context
	gameID       string
	gameDir      string
	pendingGames []string
	def          *gamedef.ModLoaderSpec
	engine       loaderEngine
	release      func()
	done         func()
}

type loaderOutcome struct {
	result dto.ModLoaderStatusResult
	err    error
}

type ModLoaderService struct {
	s *session

	engineFor    func(spec smapi.LoaderSpec) loaderEngine
	newArtifacts func(def *gamedef.ModLoaderSpec) (loaderArtifacts, error)

	artifactsMu sync.Mutex
	artifacts   map[gamedef.ModLoaderKind]loaderArtifacts

	latestMu  sync.Mutex
	latest    map[gamedef.ModLoaderKind]latestRelease
	latestGen uint64

	opMu          sync.Mutex
	opWG          sync.WaitGroup
	opCtx         context.Context
	opCancel      context.CancelFunc
	opClosed      bool
	opAbandoned   chan struct{}
	abandonOnce   sync.Once
	opWaitTimeout time.Duration
}

// Inspect classifies the SMAPI install in gameDir.
func (e smapiEngine) Inspect(gameDir string) (smapi.Status, error) {
	return smapi.Inspect(gameDir, e.spec)
}

// Install runs a staged, journaled SMAPI install of art into gameDir.
func (e smapiEngine) Install(ctx context.Context, gameDir string, art smapi.Artifact, progress func(phase, detail string)) (*smapi.Record, error) {
	installer := &smapi.Installer{Spec: e.spec, Progress: progress}
	return installer.Install(ctx, gameDir, art)
}

// Uninstall runs a journaled SMAPI uninstall in gameDir.
func (e smapiEngine) Uninstall(ctx context.Context, gameDir string, progress func(phase, detail string)) error {
	installer := &smapi.Installer{Spec: e.spec, Progress: progress}
	return installer.Uninstall(ctx, gameDir)
}

// Recover finishes or rolls back an interrupted SMAPI transaction in gameDir, killing a still-live recorded installer group.
func (e smapiEngine) Recover(gameDir string) (smapi.RecoverResult, error) {
	return smapi.Recover(gameDir, killLoaderProcessGroup)
}

// killLoaderProcessGroup kills a recorded installer process group when the process is still the recorded one.
func killLoaderProcessGroup(pid int, start uint64) error {
	_, err := smapi.KillProcessGroupIfSame(pid, start)
	return err
}

// newModLoaderService builds the mod-loader service whose operations are cancelled when the session shuts down.
func newModLoaderService(s *session) *ModLoaderService {
	ctx, cancel := context.WithCancel(context.Background())
	ml := &ModLoaderService{
		s:             s,
		engineFor:     func(spec smapi.LoaderSpec) loaderEngine { return smapiEngine{spec: spec} },
		newArtifacts:  newLoaderArtifacts,
		artifacts:     make(map[gamedef.ModLoaderKind]loaderArtifacts),
		latest:        make(map[gamedef.ModLoaderKind]latestRelease),
		opCtx:         ctx,
		opCancel:      cancel,
		opAbandoned:   make(chan struct{}),
		opWaitTimeout: loaderShutdownWait,
	}
	if s.shutdownCh != nil {
		go func() {
			select {
			case <-s.shutdownCh:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	return ml
}

// newLoaderArtifacts builds the verified GitHub-release artifact store of a registry mod loader under the managed tools directory.
func newLoaderArtifacts(def *gamedef.ModLoaderSpec) (loaderArtifacts, error) {
	name, ok := loaderToolNames[def.Kind]
	if !ok {
		return nil, fmt.Errorf("mod loader kind %d has no artifact store", def.Kind)
	}
	pattern, err := regexp.Compile(def.AssetPattern)
	if err != nil {
		return nil, fmt.Errorf("%s asset pattern: %w", def.DisplayName, err)
	}
	return &smapi.ArtifactStore{
		Store:   &ghrelease.Store{Root: filepath.Join(config.ToolsDir(), name), Label: def.DisplayName},
		Fetcher: ghrelease.NewFetcher(nil),
		Source: ghrelease.Source{
			APIURL:       loaderReleaseAPIPrefix + def.GitHubRepo + loaderReleaseAPISuffix,
			AssetPattern: pattern,
			UserAgent:    loaderUserAgentPrefix + name,
			Label:        def.DisplayName,
		},
	}, nil
}

// GetModLoaderStatus reports the game's loader install, retained artifacts, and optionally the latest upstream release.
func (ml *ModLoaderService) GetModLoaderStatus(ctx context.Context, gameID string, checkLatest bool) (dto.ModLoaderStatusResult, error) {
	spec, def, ok := loaderSpecFor(gameID)
	if !ok {
		return dto.ModLoaderStatusResult{}, &dto.ModLoaderUnsupportedError{GameID: gameID}
	}
	ml.s.mu.RLock()
	gameDir, err := ml.s.loaderGameDirLocked(gameID)
	key := ml.s.fenceKeyLocked(gameID)
	ml.s.mu.RUnlock()
	if err != nil {
		return dto.ModLoaderStatusResult{}, err
	}
	result, err := ml.loaderStatus(gameID, def, ml.engineFor(spec), gameDir)
	if err != nil {
		return dto.ModLoaderStatusResult{}, err
	}
	result.Busy = ml.s.exclusiveHeld(key)
	if !checkLatest {
		return result, nil
	}
	latest, err := ml.latestVersion(ctx, def)
	if err != nil {
		result.Detail = joinDetail(result.Detail, "checking the latest "+def.DisplayName+" release failed: "+err.Error())
		return result, nil
	}
	result.LatestVersion = latest
	result.UpdateAvailable = result.Managed && versionNewer(latest, result.InstalledVersion)
	return result, nil
}

// InstallModLoader installs or updates the game's loader from the latest verified release, or repairs it from the active artifact.
func (ml *ModLoaderService) InstallModLoader(ctx context.Context, gameID string, repairOnly bool) (dto.ModLoaderStatusResult, error) {
	return ml.runLoaderRPC(func() (dto.ModLoaderStatusResult, error) { return ml.installModLoader(ctx, gameID, repairOnly) })
}

// installModLoader is the body of InstallModLoader.
func (ml *ModLoaderService) installModLoader(ctx context.Context, gameID string, repairOnly bool) (dto.ModLoaderStatusResult, error) {
	op, err := ml.admitLoaderOp(ctx, gameID)
	if err != nil {
		return dto.ModLoaderStatusResult{}, err
	}
	defer op.finish()
	if err := ml.prepareLoaderOp(op); err != nil {
		return dto.ModLoaderStatusResult{}, ml.failLoaderOp(op, err)
	}
	arts, err := ml.artifactStore(op.def)
	if err != nil {
		return dto.ModLoaderStatusResult{}, ml.failLoaderOp(op, err)
	}
	downloadCtx, stop := mergeCancel(ctx, op.ctx)
	art, err := ml.resolveArtifact(downloadCtx, op, arts, repairOnly)
	stop()
	if err != nil {
		return dto.ModLoaderStatusResult{}, ml.failLoaderOp(op, err)
	}
	if err := ml.runLoaderInstall(op, art); err != nil {
		return dto.ModLoaderStatusResult{}, ml.failLoaderOp(op, err)
	}
	if _, err := arts.Activate(art.Version); err != nil {
		return dto.ModLoaderStatusResult{}, ml.failLoaderOp(op, fmt.Errorf("%s %s is installed, but recording it as the active version failed: %w", op.def.DisplayName, art.Version, err))
	}
	ml.loaderEmit(op, "done", fmt.Sprintf("%s %s installed", op.def.DisplayName, art.Version))
	return ml.loaderStatus(gameID, op.def, op.engine, op.gameDir)
}

// UninstallModLoader restores the vanilla game from the loader's install record without touching the retained artifacts.
func (ml *ModLoaderService) UninstallModLoader(ctx context.Context, gameID string) (dto.ModLoaderStatusResult, error) {
	return ml.runLoaderRPC(func() (dto.ModLoaderStatusResult, error) { return ml.uninstallModLoader(ctx, gameID) })
}

// uninstallModLoader is the body of UninstallModLoader.
func (ml *ModLoaderService) uninstallModLoader(ctx context.Context, gameID string) (dto.ModLoaderStatusResult, error) {
	op, err := ml.admitLoaderOp(ctx, gameID)
	if err != nil {
		return dto.ModLoaderStatusResult{}, err
	}
	defer op.finish()
	if err := ml.prepareLoaderOp(op); err != nil {
		return dto.ModLoaderStatusResult{}, ml.failLoaderOp(op, err)
	}
	if err := ctx.Err(); err != nil {
		return dto.ModLoaderStatusResult{}, ml.failLoaderOp(op, err)
	}
	err = op.engine.Uninstall(op.ctx, op.gameDir, ml.loaderProgress(op))
	if err != nil && errors.Is(err, smapi.ErrInterrupted) {
		result, recoverErr := ml.recoverLoaderInProcess(op)
		switch {
		case recoverErr != nil:
			err = fmt.Errorf("%w; recovery also failed: %v", err, recoverErr)
		case result.Committed:
			ml.loaderEmit(op, "warning", "the uninstall committed but its cleanup was interrupted; recovery finished it")
			err = nil
		default:
			err = recoveredLoaderCause(err)
		}
	}
	if err != nil {
		return dto.ModLoaderStatusResult{}, ml.failLoaderOp(op, err)
	}
	ml.loaderEmit(op, "done", op.def.DisplayName+" uninstalled")
	return ml.loaderStatus(gameID, op.def, op.engine, op.gameDir)
}

// RollbackModLoader reinstalls the retained previous loader release and swaps the retained versions only after it succeeds.
func (ml *ModLoaderService) RollbackModLoader(ctx context.Context, gameID string) (dto.ModLoaderStatusResult, error) {
	return ml.runLoaderRPC(func() (dto.ModLoaderStatusResult, error) { return ml.rollbackModLoader(ctx, gameID) })
}

// rollbackModLoader is the body of RollbackModLoader.
func (ml *ModLoaderService) rollbackModLoader(ctx context.Context, gameID string) (dto.ModLoaderStatusResult, error) {
	op, err := ml.admitLoaderOp(ctx, gameID)
	if err != nil {
		return dto.ModLoaderStatusResult{}, err
	}
	defer op.finish()
	if err := ml.prepareLoaderOp(op); err != nil {
		return dto.ModLoaderStatusResult{}, ml.failLoaderOp(op, err)
	}
	if err := ctx.Err(); err != nil {
		return dto.ModLoaderStatusResult{}, ml.failLoaderOp(op, err)
	}
	arts, err := ml.artifactStore(op.def)
	if err != nil {
		return dto.ModLoaderStatusResult{}, ml.failLoaderOp(op, err)
	}
	previous, err := arts.PreviousVersion()
	if err != nil {
		return dto.ModLoaderStatusResult{}, ml.failLoaderOp(op, err)
	}
	if previous == "" {
		return dto.ModLoaderStatusResult{}, ml.failLoaderOp(op, fmt.Errorf("no previous %s version is retained: %w", op.def.DisplayName, ghrelease.ErrNoPrevious))
	}
	art, err := arts.Artifact(previous)
	if err != nil {
		return dto.ModLoaderStatusResult{}, ml.failLoaderOp(op, fmt.Errorf("previous %s %s cannot be used: %v: %w", op.def.DisplayName, previous, err, ghrelease.ErrPreviousUnavailable))
	}
	ml.loaderEmit(op, "verify", fmt.Sprintf("rolling back to retained %s %s", op.def.DisplayName, previous))
	if err := ml.runLoaderInstall(op, art); err != nil {
		return dto.ModLoaderStatusResult{}, ml.failLoaderOp(op, err)
	}
	if _, err := arts.Rollback(); err != nil {
		return dto.ModLoaderStatusResult{}, ml.failLoaderOp(op, fmt.Errorf("%s %s is installed, but swapping the retained versions failed: %w", op.def.DisplayName, previous, err))
	}
	ml.loaderEmit(op, "done", fmt.Sprintf("rolled back to %s %s", op.def.DisplayName, previous))
	return ml.loaderStatus(gameID, op.def, op.engine, op.gameDir)
}

// runLoaderRPC runs a mutating loader operation and returns a shutdown error instead of waiting once shutdown stopped waiting for it.
func (ml *ModLoaderService) runLoaderRPC(body func() (dto.ModLoaderStatusResult, error)) (dto.ModLoaderStatusResult, error) {
	done := make(chan loaderOutcome, 1)
	go func() {
		result, err := body()
		done <- loaderOutcome{result: result, err: err}
	}()
	select {
	case out := <-done:
		return out.result, out.err
	case <-ml.opAbandoned:
		select {
		case out := <-done:
			return out.result, out.err
		default:
		}
		return dto.ModLoaderStatusResult{}, errors.New("daemon is shutting down; the unfinished mod-loader operation is recovered at the next start")
	}
}

// admitLoaderOp waits for startup recovery within ctx, registers a daemon-lifetime operation, takes the game's exclusive fence, and resolves everything the operation and its failure path need, the farm guard included, so it never takes s.mu afterwards.
func (ml *ModLoaderService) admitLoaderOp(ctx context.Context, gameID string) (*loaderOp, error) {
	spec, def, ok := loaderSpecFor(gameID)
	if !ok {
		return nil, &dto.ModLoaderUnsupportedError{GameID: gameID}
	}
	if err := ml.s.awaitRecoveryCtx(ctx); err != nil {
		return nil, err
	}
	ctx, done, err := ml.beginLoaderOp()
	if err != nil {
		return nil, err
	}
	ml.s.mu.Lock()
	gameDir, err := ml.s.loaderGameDirLocked(gameID)
	var release func()
	var pendingGames []string
	if err == nil {
		spec.Farm = ml.s.loaderFarmGuardLocked(gameID, gameDir)
		if err = ml.s.steamAdmissionLocked(gameID, filepath.Join(gameDir, filepath.FromSlash(spec.Farm.DeployDir))); err == nil {
			release, err = ml.s.reserveExclusiveLocked(gameID, dto.BusyOperationModLoader)
			pendingGames = ml.s.loaderGamesAtLocked(gameDir)
		}
	}
	ml.s.mu.Unlock()
	if err != nil {
		done()
		return nil, err
	}
	return &loaderOp{
		ctx: ctx, gameID: gameID, gameDir: gameDir, pendingGames: pendingGames, def: def,
		engine: ml.engineFor(spec), release: release, done: done,
	}, nil
}

// finish releases the operation's fence and marks it complete for shutdown.
func (op *loaderOp) finish() {
	op.release()
	op.done()
}

// beginLoaderOp registers a running operation with the shutdown wait group, refusing once shutdown began.
func (ml *ModLoaderService) beginLoaderOp() (context.Context, func(), error) {
	ml.opMu.Lock()
	defer ml.opMu.Unlock()
	if ml.opClosed || ml.opCtx.Err() != nil {
		return nil, nil, &dto.ShuttingDownError{Operation: dto.BusyOperationModLoader}
	}
	ml.opWG.Add(1)
	var once sync.Once
	return ml.opCtx, func() { once.Do(ml.opWG.Done) }, nil
}

// stopLoaderOps refuses new operations, cancels running ones, and waits up to timeout for them to finish, abandoning the RPCs of any that do not.
func (ml *ModLoaderService) stopLoaderOps(timeout time.Duration) {
	ml.opMu.Lock()
	ml.opClosed = true
	ml.opMu.Unlock()
	ml.opCancel()
	finished := make(chan struct{})
	go func() {
		ml.opWG.Wait()
		close(finished)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-finished:
	case <-timer.C:
		slog.Warn("mod-loader operation still running at shutdown; startup recovery rolls it back", "waited", timeout)
		ml.abandonOnce.Do(func() { close(ml.opAbandoned) })
	}
}

// prepareLoaderOp refuses unsupported builds and recovers an interrupted transaction in-process before a mutating operation.
func (ml *ModLoaderService) prepareLoaderOp(op *loaderOp) error {
	status, err := op.engine.Inspect(op.gameDir)
	if err != nil {
		return err
	}
	switch status.State {
	case smapi.StateUnsupportedBuild:
		return fmt.Errorf("%w: %s", &smapi.UnavailableError{GameID: op.gameID, State: status.State, Context: smapi.UnavailableForLoaderChange}, status.Detail)
	case smapi.StateInterrupted:
		ml.loaderEmit(op, "warning", "recovering an interrupted "+op.def.DisplayName+" transaction first")
		if _, err := ml.recoverLoaderInProcess(op); err != nil {
			return err
		}
	}
	return nil
}

// resolveArtifact returns the retained active artifact for a repair, never downloading one, or downloads and verifies the latest release.
func (ml *ModLoaderService) resolveArtifact(ctx context.Context, op *loaderOp, arts loaderArtifacts, repairOnly bool) (smapi.Artifact, error) {
	label := op.def.DisplayName
	if repairOnly {
		active, err := arts.ActiveVersion()
		if err != nil {
			return smapi.Artifact{}, noLoaderArtifact(op.gameID, fmt.Errorf("reading the retained %s versions failed (%v); install %s instead of repairing: %w", label, err, label, smapi.ErrNoArtifact))
		}
		if active == "" {
			return smapi.Artifact{}, noLoaderArtifact(op.gameID, fmt.Errorf("no retained %s version to repair from; install %s instead of repairing: %w", label, label, smapi.ErrNoArtifact))
		}
		art, err := arts.Artifact(active)
		if err != nil {
			return smapi.Artifact{}, noLoaderArtifact(op.gameID, fmt.Errorf("retained %s %s is unusable (%v); install %s instead of repairing: %w", label, active, err, label, smapi.ErrNoArtifact))
		}
		ml.loaderEmit(op, "verify", fmt.Sprintf("repairing with retained %s %s", label, active))
		return art, nil
	}
	ml.loaderEmit(op, "download", "checking the latest "+label+" release")
	rel, err := arts.Latest(ctx)
	if err != nil {
		return smapi.Artifact{}, err
	}
	ml.rememberLatest(op.def.Kind, rel.Version)
	ml.loaderEmit(op, "download", fmt.Sprintf("fetching %s %s", label, rel.Version))
	art, err := arts.Fetch(ctx, rel)
	if err != nil {
		return smapi.Artifact{}, err
	}
	ml.loaderEmit(op, "verify", fmt.Sprintf("%s %s matches its published SHA-256", label, art.Version))
	return art, nil
}

// noLoaderArtifact wraps err as the no_artifact failure of gameID.
func noLoaderArtifact(gameID string, err error) error {
	return &smapi.FailedError{GameID: gameID, Reason: smapi.FailureNoArtifact, Err: err}
}

// recoveredLoaderCause returns the failure an operation reports once in-process recovery resolved the interruption err describes, so a retryable cause is not reported as interrupted.
func recoveredLoaderCause(err error) error {
	var rollback *smapi.RollbackFailedError
	if errors.As(err, &rollback) && rollback.Cause != nil {
		return rollback.Cause
	}
	return errors.New("an interrupted mod-loader transaction was recovered before the operation could finish; retry it")
}

// runLoaderInstall runs the staged install under the daemon-lifetime context, treating a committed install whose cleanup was interrupted as success.
func (ml *ModLoaderService) runLoaderInstall(op *loaderOp, art smapi.Artifact) error {
	record, err := op.engine.Install(op.ctx, op.gameDir, art, ml.loaderProgress(op))
	if err == nil {
		return nil
	}
	if !errors.Is(err, smapi.ErrInterrupted) {
		return err
	}
	if record != nil {
		ml.loaderEmit(op, "warning", fmt.Sprintf("%s %s is installed but its cleanup was interrupted; recovering", op.def.DisplayName, art.Version))
		if _, recoverErr := ml.recoverLoaderInProcess(op); recoverErr != nil {
			slog.Warn("recovering a committed mod-loader install failed", "game", op.gameID, "err", recoverErr)
		}
		return nil
	}
	if _, recoverErr := ml.recoverLoaderInProcess(op); recoverErr != nil {
		return fmt.Errorf("%w; recovery also failed: %v", err, recoverErr)
	}
	return recoveredLoaderCause(err)
}

// recoverLoaderInProcess runs loader recovery without daemon locks and registers a pending recovery for the games resolved at admission when it fails for any reason but a live transaction.
func (ml *ModLoaderService) recoverLoaderInProcess(op *loaderOp) (smapi.RecoverResult, error) {
	result, err := op.engine.Recover(op.gameDir)
	if err != nil {
		slog.Error("in-process mod-loader recovery failed", "game", op.gameID, "path", op.gameDir, "err", err)
		if !errors.Is(err, smapi.ErrTransactionActive) {
			ml.s.registerLoaderPending(op.pendingGames, op.gameDir, err, ml.loaderPublish)
		}
		return result, err
	}
	logLoaderRecovery(op.gameID, op.gameDir, result)
	for _, warning := range result.Warnings {
		ml.loaderEmit(op, "warning", warning)
	}
	return result, nil
}

// failLoaderOp translates an operation error into its typed form, reports it on the status stream, and returns it.
func (ml *ModLoaderService) failLoaderOp(op *loaderOp, err error) error {
	err = translateLoaderError(op.gameID, err)
	slog.Error("mod-loader operation failed", "game", op.gameID, "err", err)
	ml.loaderEmit(op, "failed", err.Error())
	return err
}

// translateLoaderError maps loader sentinels onto the typed busy, unavailable, and failure errors the transport encodes.
func translateLoaderError(gameID string, err error) error {
	var busy *dto.OperationBusyError
	var unavailable *smapi.UnavailableError
	switch {
	case err == nil, errors.As(err, &busy), errors.As(err, &unavailable):
		return err
	case errors.Is(err, smapi.ErrTransactionActive):
		return fmt.Errorf("%w: %v", &dto.OperationBusyError{GameID: gameID, Operation: dto.BusyOperationTransaction}, err)
	case errors.Is(err, smapi.ErrUnsupportedBuild):
		return fmt.Errorf("%w: %v", &smapi.UnavailableError{GameID: gameID, State: smapi.StateUnsupportedBuild, Context: smapi.UnavailableForLoaderChange}, err)
	}
	return smapi.ClassifyFailure(gameID, err)
}

// loaderStatus inspects gameDir and reads the retained artifact versions without network access.
func (ml *ModLoaderService) loaderStatus(gameID string, def *gamedef.ModLoaderSpec, engine loaderEngine, gameDir string) (dto.ModLoaderStatusResult, error) {
	status, err := engine.Inspect(gameDir)
	if err != nil {
		return dto.ModLoaderStatusResult{}, err
	}
	result := dto.ModLoaderStatusResult{
		GameID:  gameID,
		Kind:    modLoaderKindResult(def.Kind),
		State:   loaderStateResult(status.State),
		Managed: status.Managed,
		Detail:  status.Detail,
	}
	if status.Recorded {
		result.InstalledVersion = status.RecordedVersion
		if !status.Managed {
			result.Detail = joinDetail(result.Detail, "files changed since gorganizer installed "+def.DisplayName+" "+status.RecordedVersion)
		}
	}
	arts, err := ml.artifactStore(def)
	if err != nil {
		result.Detail = joinDetail(result.Detail, err.Error())
		return result, nil
	}
	if result.ActiveVersion, err = arts.ActiveVersion(); err != nil {
		result.Detail = joinDetail(result.Detail, "reading retained versions failed: "+err.Error())
		return result, nil
	}
	if result.PreviousVersion, err = arts.PreviousVersion(); err != nil {
		result.Detail = joinDetail(result.Detail, "reading retained versions failed: "+err.Error())
	}
	return result, nil
}

// artifactStore returns the cached artifact store of a loader kind, building it on first use.
func (ml *ModLoaderService) artifactStore(def *gamedef.ModLoaderSpec) (loaderArtifacts, error) {
	ml.artifactsMu.Lock()
	defer ml.artifactsMu.Unlock()
	if arts, ok := ml.artifacts[def.Kind]; ok {
		return arts, nil
	}
	arts, err := ml.newArtifacts(def)
	if err != nil {
		return nil, err
	}
	ml.artifacts[def.Kind] = arts
	return arts, nil
}

// latestVersion returns the newest upstream release version, reusing an answer younger than loaderLatestTTL and holding no lock across the network call.
func (ml *ModLoaderService) latestVersion(ctx context.Context, def *gamedef.ModLoaderSpec) (string, error) {
	now := ml.s.clock()
	ml.latestMu.Lock()
	cached, gen := ml.latest[def.Kind], ml.latestGen
	ml.latestMu.Unlock()
	if age := now.Sub(cached.fetchedAt); cached.version != "" && age >= 0 && age < loaderLatestTTL {
		return cached.version, nil
	}
	arts, err := ml.artifactStore(def)
	if err != nil {
		return "", err
	}
	rel, err := arts.Latest(ctx)
	if err != nil {
		return "", err
	}
	ml.latestMu.Lock()
	if ml.latestGen == gen {
		ml.latest[def.Kind] = latestRelease{version: rel.Version, fetchedAt: now}
	}
	ml.latestMu.Unlock()
	return rel.Version, nil
}

// rememberLatest records a freshly resolved release and advances the cache generation so older in-flight lookups cannot overwrite it.
func (ml *ModLoaderService) rememberLatest(kind gamedef.ModLoaderKind, version string) {
	ml.latestMu.Lock()
	ml.latestGen++
	ml.latest[kind] = latestRelease{version: version, fetchedAt: ml.s.clock()}
	ml.latestMu.Unlock()
}

// loaderProgress returns the installer progress callback that forwards every phase to the status stream.
func (ml *ModLoaderService) loaderProgress(op *loaderOp) func(phase, detail string) {
	return func(phase, detail string) {
		ml.loaderEmit(op, phase, detail)
	}
}

// loaderEmit publishes "[<loader>:<phase>] <detail>" on the status stream unless shutdown closed it.
func (ml *ModLoaderService) loaderEmit(op *loaderOp, phase, detail string) {
	ml.loaderPublish(dto.StatusEventResult{Info: fmt.Sprintf("[%s:%s] %s", loaderTag(op.def), phase, detail)})
}

// loaderPublish sends a status event without blocking, dropping it once shutdown closed the stream.
func (ml *ModLoaderService) loaderPublish(evt dto.StatusEventResult) {
	ml.s.publishGuarded(evt)
}

// loaderTag returns the status-line tag of a loader kind.
func loaderTag(def *gamedef.ModLoaderSpec) string {
	if name, ok := loaderToolNames[def.Kind]; ok {
		return name
	}
	return "modloader"
}

// loaderStateResult maps a loader state to its wire value.
func loaderStateResult(state smapi.LoaderState) dto.ModLoaderStateResult {
	switch state {
	case smapi.StateNotInstalled:
		return dto.ModLoaderStateNotInstalled
	case smapi.StateOK:
		return dto.ModLoaderStateOK
	case smapi.StateLauncherReverted:
		return dto.ModLoaderStateLauncherReverted
	case smapi.StateIncomplete:
		return dto.ModLoaderStateIncomplete
	case smapi.StateUnsupportedBuild:
		return dto.ModLoaderStateUnsupportedBuild
	case smapi.StateInterrupted:
		return dto.ModLoaderStateInterrupted
	default:
		return dto.ModLoaderStateUnspecified
	}
}

// versionNewer reports whether candidate parses as a newer loader version than installed.
func versionNewer(candidate, installed string) bool {
	next, err := smapi.ParseVersion(candidate, false)
	if err != nil {
		return false
	}
	current, err := smapi.ParseVersion(installed, true)
	if err != nil {
		return false
	}
	return next.IsNewerThan(current)
}

// joinDetail appends extra to a status detail line.
func joinDetail(detail, extra string) string {
	if detail == "" {
		return extra
	}
	return detail + "; " + extra
}

// mergeCancel returns a context derived from parent that is also cancelled once other is done.
func mergeCancel(parent, other context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(other, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

// loaderGameDirLocked returns the absolute, cleaned install directory a loader operation acts on; the caller holds s.mu.
func (s *session) loaderGameDirLocked(gameID string) (string, error) {
	gc, err := s.config.EffectiveGameConfig(gameID)
	if err != nil {
		return "", err
	}
	if gc.InstallPath == "" {
		return "", fmt.Errorf("game %s has no install path", gameID)
	}
	if !filepath.IsAbs(gc.InstallPath) {
		return "", fmt.Errorf("install path %q of %s is not absolute", gc.InstallPath, gameID)
	}
	return filepath.Clean(gc.InstallPath), nil
}

// loaderFarmGuardLocked returns the farm guard of gameID's loader transactions: the deploy folder its mount manager farms, relative to gameDir, with the vfs sentinel and sibling names; the caller holds s.mu.
func (s *session) loaderFarmGuardLocked(gameID, gameDir string) smapi.FarmGuard {
	deploy := ""
	if mm, ok := s.mountMgrs[gameID]; ok {
		if rel, err := filepath.Rel(gameDir, mm.DataPath()); err == nil {
			deploy = filepath.ToSlash(rel)
		}
	}
	if deploy == "" {
		if gc, err := s.config.EffectiveGameConfig(gameID); err == nil {
			deploy = deploySubpath(gameID, gc)
		}
	}
	return smapi.FarmGuard{DeployDir: deploy, Sentinel: vfs.SentinelFilename, SiblingSuffixes: vfs.FarmSiblingSuffixes()}
}

// loaderGamesAtLocked returns the configured loader games installed at gameDir; the caller holds s.mu.
func (s *session) loaderGamesAtLocked(gameDir string) []string {
	var games []string
	for gameID := range s.config.Games {
		if _, _, ok := loaderSpecFor(gameID); !ok {
			continue
		}
		if dir, err := s.loaderGameDirLocked(gameID); err == nil && dir == gameDir {
			games = append(games, gameID)
		}
	}
	sort.Strings(games)
	return games
}

// registerLoaderPending records a pending mod-loader recovery for every game at gameDir and publishes it.
func (s *session) registerLoaderPending(gameIDs []string, gameDir string, cause error, publish func(dto.StatusEventResult)) {
	for _, gameID := range gameIDs {
		pending := &dto.RecoveryPendingResult{
			GameID:     gameID,
			DataPath:   gameDir,
			BackupPath: filepath.Join(gameDir, smapi.BackupDir),
			Reason:     loaderRecoveryPrefix + cause.Error(),
			Kind:       dto.RecoveryKindModLoader,
		}
		s.pendingRecoveriesMu.Lock()
		if s.loaderPendingRecoveries == nil {
			s.loaderPendingRecoveries = make(map[string]*dto.RecoveryPendingResult)
		}
		pending = identifiedRecovery(s.loaderPendingRecoveries[gameID], pending)
		s.loaderPendingRecoveries[gameID] = pending
		s.pendingRecoveriesMu.Unlock()
		publish(dto.StatusEventResult{RecoveryPending: pending})
		s.publishRecoveryStatuses(gameID)
	}
}

type loaderRecoveryTarget struct {
	gameDir string
	gameIDs []string
	spec    smapi.LoaderSpec
}

// recoverAddedLoaderGames runs loader recovery for games configured or detected after startup, under each install's exclusive fence.
func (s *session) recoverAddedLoaderGames(gameIDs []string) {
	var ready []string
	for _, gameID := range gameIDs {
		if s.deferredFor(gameID, "modloader") == nil {
			ready = append(ready, gameID)
		}
	}
	s.recoverLoaderGames(ready, true)
}

// recoverLoaderGames recovers each install directory of the loader games among gameIDs once, outside s.mu, taking its exclusive fence when fenced, registers a pending recovery for every loader game at a directory whose recovery fails, and returns the directories deferred because another transaction held their lock.
func (s *session) recoverLoaderGames(gameIDs []string, fenced bool) []loaderRecoveryTarget {
	ml := s.svc.modLoader
	if ml == nil {
		return nil
	}
	var deferred []loaderRecoveryTarget
	for _, target := range s.loaderRecoveryTargets(gameIDs) {
		if !loaderStatePresent(target.gameDir) {
			continue
		}
		release := func() {}
		if fenced {
			s.mu.Lock()
			reserved, err := s.reserveExclusiveLocked(target.gameIDs[0], dto.BusyOperationModLoader)
			s.mu.Unlock()
			if err != nil {
				slog.Warn("mod-loader recovery deferred; mounting and launching stay refused while its intent exists",
					"games", target.gameIDs, "path", target.gameDir, "err", err)
				continue
			}
			release = reserved
		}
		result, err := ml.engineFor(target.spec).Recover(target.gameDir)
		release()
		if err != nil {
			if errors.Is(err, smapi.ErrTransactionActive) {
				slog.Warn("mod-loader recovery deferred; another transaction holds the game directory",
					"games", target.gameIDs, "path", target.gameDir, "err", err)
				deferred = append(deferred, target)
				continue
			}
			slog.Error("mod-loader crash recovery failed; refusing mount, launch, and loader changes until confirmed",
				"games", target.gameIDs, "path", target.gameDir, "err", err)
			s.registerLoaderPending(target.gameIDs, target.gameDir, err, s.publishRecoveryEvent)
			continue
		}
		logLoaderRecovery(strings.Join(target.gameIDs, ","), target.gameDir, result)
	}
	return deferred
}

// retryDeferredLoaderRecovery re-runs startup loader recovery once for install directories whose transaction lock was held, registering a pending recovery only for a failure other than a still-held lock, which keeps mounting and launching refused while its intent exists, and returns the directories whose lock is still held.
func (s *session) retryDeferredLoaderRecovery(targets []loaderRecoveryTarget) map[string]bool {
	held := map[string]bool{}
	ml := s.svc.modLoader
	if ml == nil {
		return held
	}
	for _, target := range targets {
		result, err := ml.engineFor(target.spec).Recover(target.gameDir)
		switch {
		case errors.Is(err, smapi.ErrTransactionActive):
			held[target.gameDir] = true
			slog.Warn("mod-loader recovery still deferred; mounting and launching stay refused while its intent exists",
				"games", target.gameIDs, "path", target.gameDir, "err", err)
		case err != nil:
			slog.Error("mod-loader crash recovery failed; refusing mount, launch, and loader changes until confirmed",
				"games", target.gameIDs, "path", target.gameDir, "err", err)
			s.registerLoaderPending(target.gameIDs, target.gameDir, err, s.publishRecoveryEvent)
		default:
			logLoaderRecovery(strings.Join(target.gameIDs, ","), target.gameDir, result)
		}
	}
	return held
}

// loaderRecoveryTargets resolves the install directories of the loader games among gameIDs with every configured loader game installed there, logging games whose directory cannot be resolved.
func (s *session) loaderRecoveryTargets(gameIDs []string) []loaderRecoveryTarget {
	sorted := append([]string(nil), gameIDs...)
	sort.Strings(sorted)
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]bool{}
	var targets []loaderRecoveryTarget
	for _, gameID := range sorted {
		spec, _, ok := loaderSpecFor(gameID)
		if !ok {
			continue
		}
		gameDir, err := s.loaderGameDirLocked(gameID)
		if err != nil {
			slog.Warn("mod-loader recovery skipped", "game", gameID, "err", err)
			continue
		}
		if seen[gameDir] {
			continue
		}
		seen[gameDir] = true
		targets = append(targets, loaderRecoveryTarget{gameDir: gameDir, gameIDs: s.loaderGamesAtLocked(gameDir), spec: spec})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].gameDir < targets[j].gameDir })
	return targets
}

// retryLoaderRecovery re-runs loader recovery for a game with a pending loader entry and clears every entry at that directory on success.
func (s *session) retryLoaderRecovery(gameID string) (bool, error) {
	s.pendingRecoveriesMu.Lock()
	pending := s.loaderPendingRecoveries[gameID]
	s.pendingRecoveriesMu.Unlock()
	if pending == nil {
		return false, nil
	}
	spec, _, ok := loaderSpecFor(gameID)
	if !ok || s.svc.modLoader == nil {
		return true, fmt.Errorf("no mod loader is registered for %s", gameID)
	}
	result, err := s.svc.modLoader.engineFor(spec).Recover(pending.DataPath)
	if err != nil {
		return true, fmt.Errorf("recovering the mod-loader transaction at %s: %w", pending.DataPath, err)
	}
	logLoaderRecovery(gameID, pending.DataPath, result)
	var resolved []string
	s.pendingRecoveriesMu.Lock()
	for id, entry := range s.loaderPendingRecoveries {
		if entry.DataPath == pending.DataPath {
			delete(s.loaderPendingRecoveries, id)
			resolved = append(resolved, id)
		}
	}
	s.pendingRecoveriesMu.Unlock()
	sort.Strings(resolved)
	for _, id := range resolved {
		s.publishGuarded(dto.StatusEventResult{Info: fmt.Sprintf("recovery resolved for %s", id)})
	}
	return true, nil
}

// publishRecoveryEvent sends a recovery event without blocking, dropping it once shutdown closed the status stream.
func (s *session) publishRecoveryEvent(evt dto.StatusEventResult) {
	s.publishGuarded(evt)
}

// loaderStatePresent reports whether gameDir holds a loader intent, work, or backup directory that recovery must resolve, logging an install directory it cannot inspect.
func loaderStatePresent(gameDir string) bool {
	info, err := os.Lstat(gameDir)
	switch {
	case err != nil:
		slog.Warn("mod-loader recovery skipped: the install directory is unavailable", "path", gameDir, "err", err)
		return false
	case info.Mode()&os.ModeSymlink != 0:
		slog.Warn("mod-loader recovery skipped: the install directory is a symlink, which the mod loader never manages", "path", gameDir)
		return false
	case !info.IsDir():
		slog.Warn("mod-loader recovery skipped: the install path is not a directory", "path", gameDir)
		return false
	}
	for _, name := range []string{smapi.IntentFile, smapi.WorkDir, smapi.BackupDir} {
		if _, err := os.Lstat(filepath.Join(gameDir, name)); err == nil || !errors.Is(err, os.ErrNotExist) {
			return true
		}
	}
	return false
}

// refuseLoaderIntentLocked refuses mounting or launching a loader game while its install holds a mod-loader intent that no recovery resolved; the caller holds s.mu.
func (s *session) refuseLoaderIntentLocked(gameID string) error {
	if _, _, ok := loaderSpecFor(gameID); !ok {
		return nil
	}
	gameDir, err := s.loaderGameDirLocked(gameID)
	if err != nil {
		return nil
	}
	return loaderIntentRefusal(gameID, gameDir)
}

// loaderIntentRefusal returns an interrupted UnavailableError while gameDir holds a mod-loader intent.
func loaderIntentRefusal(gameID, gameDir string) error {
	_, err := os.Lstat(filepath.Join(gameDir, smapi.IntentFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking %s for an interrupted mod-loader transaction: %w", gameDir, err)
	}
	return &smapi.UnavailableError{GameID: gameID, State: smapi.StateInterrupted, Context: smapi.UnavailableForMount}
}

// logLoaderRecovery logs the outcome of a successful loader recovery.
func logLoaderRecovery(games, gameDir string, result smapi.RecoverResult) {
	if !result.RolledBack && !result.Committed && result.SweptWorkDirs == 0 && result.SweptBackups == 0 && len(result.Warnings) == 0 {
		return
	}
	slog.Info("mod-loader recovery finished", "games", games, "path", gameDir,
		"rolled_back", result.RolledBack, "committed", result.Committed,
		"swept_work", result.SweptWorkDirs, "swept_backups", result.SweptBackups, "warnings", result.Warnings)
}

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/fsutil"
	"github.com/parka/gorganizer/internal/transfer"
	"golang.org/x/sys/unix"
)

const (
	reinstallStagePrefix   = ".reinstall-"
	reinstallOldPrefix     = ".reinstall-old-"
	reinstallIntentPrefix  = ".gorganizer-reinstall-intent-"
	reinstallIntentSuffix  = ".json"
	reinstallIntentVersion = 2
)

type reinstallIntent struct {
	SchemaVersion int                           `json:"schema_version"`
	Mod           string                        `json:"mod"`
	Stage         string                        `json:"stage"`
	Old           string                        `json:"old"`
	StageIdentity *atomicfile.DirectoryIdentity `json:"stage_identity,omitempty"`
}

// checkModReplacement refuses names reserved by an unfinished reinstall or transfer journal.
func checkModReplacement(modsDir, name string) error {
	entries, err := os.ReadDir(modsDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking reinstall journals: %w", err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), reinstallIntentPrefix) || !strings.HasSuffix(entry.Name(), reinstallIntentSuffix) {
			continue
		}
		intent, err := readReinstallIntent(filepath.Join(modsDir, entry.Name()))
		if err != nil {
			return fmt.Errorf("checking reinstall journal %q: %w", entry.Name(), err)
		}
		if intent.Mod == name {
			return &download.ReplacementPendingError{Name: name}
		}
	}
	return transfer.CheckPendingReplacement(modsDir, name)
}

// ReinstallMod waits for startup recovery, then rebuilds a mod from its recorded source archives in hidden staging and swaps it in only after every replay succeeds.
func (md *ModService) ReinstallMod(ctx context.Context, gameID, modName, clientRequestID string) (replayed, skipped, fileCount int, err error) {
	if err = md.s.installOutcomes.register(gameID, clientRequestID); err != nil {
		return 0, 0, 0, err
	}
	published := false
	defer func() {
		md.s.installOutcomes.finish(clientRequestID, ctx, published, dto.InstallOutcome{
			ModFolder: modName, FileCount: fileCount, ArchivesReplayed: replayed, ArchivesSkipped: skipped,
		}, err)
	}()
	if err := md.s.awaitRecoveryCtx(ctx); err != nil {
		return 0, 0, 0, err
	}
	if err := md.s.refuseWhenShuttingDown("reinstall"); err != nil {
		return 0, 0, 0, err
	}
	if err := md.s.deferredFor(gameID, dto.BusyOperationReinstall); err != nil {
		return 0, 0, 0, err
	}
	if !md.s.gameConfigured(gameID) {
		return 0, 0, 0, fmt.Errorf("%w: %s", config.ErrInvalidGameID, gameID)
	}
	release, err := md.s.acquireShared(gameID, dto.BusyOperationReinstall)
	if err != nil {
		return 0, 0, 0, err
	}
	defer release()
	if err := installCtxErr(ctx); err != nil {
		return 0, 0, 0, err
	}
	if err := checkInstallLayout(gameID); err != nil {
		return 0, 0, 0, err
	}
	defer md.s.lockMods(gameID, modName)()
	if err := installCtxErr(ctx); err != nil {
		return 0, 0, 0, err
	}
	if err := checkModReplacement(config.ModsDir(gameID), modName); err != nil {
		return 0, 0, 0, err
	}
	modDir, err := resolveExistingModDir(gameID, modName)
	if err != nil {
		return 0, 0, 0, err
	}
	modsDir := config.ModsDir(gameID)
	meta, err := download.LoadModMetadata(modDir)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("loading mod metadata: %w", err)
	}
	if meta == nil || len(meta.SourceArchives) == 0 {
		return 0, 0, 0, fmt.Errorf("mod %q has no source_archives to replay", modName)
	}
	sources, err := resolveReinstallSources(modsDir, modName, meta.SourceArchives)
	if err != nil {
		return 0, 0, 0, err
	}

	token := uuid.NewString()
	stageDir := filepath.Join(modsDir, reinstallStagePrefix+token)
	if err := md.replaySources(ctx, gameID, modName, reinstallStagePrefix+token, meta.SourceArchives, sources); err != nil {
		_ = os.RemoveAll(stageDir)
		return 0, 0, 0, err
	}
	if err := md.s.reinstallStep("replayed"); err != nil {
		return 0, 0, 0, err
	}
	fileCount, err = md.commitReinstall(ctx, gameID, modName, modsDir, token, meta, &published)
	if err != nil {
		return 0, 0, 0, err
	}
	md.s.invalidateInstalledArchiveCache(gameID)
	slog.Info("mod reinstalled", "game", gameID, "mod", modName, "archives", len(sources), "files", fileCount)
	return len(sources), 0, fileCount, nil
}

// mountedModUsedLocked reports whether the mounted farm or root deployment uses a mod; the caller holds s.mu and the profile lock.
func (md *ModService) mountedModUsedLocked(gameID, modName string) (bool, error) {
	mm := md.s.mountMgrs[gameID]
	if mm != nil && mm.IsMounted() {
		for _, layer := range mm.AppliedLayers() {
			if layer.Name == modName {
				return true, nil
			}
		}
		if ms, ok := md.s.mountStates[gameID]; ok {
			_, entries, err := md.s.profileMgr.Load(gameID, ms.profileName)
			if err != nil {
				return false, fmt.Errorf("loading mounted profile %q: %w", ms.profileName, err)
			}
			for _, e := range entries {
				if e.Name == modName && e.Enabled {
					return true, nil
				}
			}
		}
	}
	if manager := md.s.rootDeployMgrs[gameID]; manager != nil {
		manifest, err := manager.ActiveManifest()
		if err != nil {
			return false, fmt.Errorf("reading active game-root deployment: %w", err)
		}
		if manifest != nil {
			modDir := filepath.Join(config.ModsDir(gameID), modName)
			for _, entry := range manifest.Entries {
				if entry.LayerName == modName || filepath.Clean(entry.SourceRoot) == modDir {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// resolveReinstallSources resolves every recorded source archive to a readable file, failing on the first missing one.
func resolveReinstallSources(modsDir, modName string, refs []download.SourceArchiveRef) ([]string, error) {
	paths := make([]string, 0, len(refs))
	for _, sa := range refs {
		abs := sa.Path
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(modsDir, sa.Path)
			if !fsutil.ContainedBy(modsDir, abs) {
				return nil, &UnsafePathError{Field: "source_archives.path"}
			}
		}
		if err := checkReadableFile(abs); err != nil {
			return nil, &download.ReinstallSourceMissingError{Mod: modName, Path: sa.Path}
		}
		paths = append(paths, abs)
	}
	return paths, nil
}

// checkReadableFile returns an error unless path is a regular file that opens for reading without blocking.
func checkReadableFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	if !opened.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	return nil
}

// replaySources installs every source archive in order into the hidden staging mod folder.
func (md *ModService) replaySources(ctx context.Context, gameID, modName, stageName string, refs []download.SourceArchiveRef, sources []string) error {
	planner := layoutPlannerFor(gameID)
	sink := func(p download.InstallProgress) {
		if md.s.installCopyProgress != nil && p.Step == download.StageCopying {
			md.s.installCopyProgress(p)
		}
		md.s.installBus.Publish(gameID, dto.InstallEventResult{
			GameID: gameID,
			Progress: &dto.InstallProgressResult{
				InstallID: p.InstallID, ModName: modName,
				Step: dto.InstallStep(p.Step), Pct: p.Pct,
				CurrentFile: p.CurrentFile, FilesDone: p.FilesDone,
				FilesTotal: p.FilesTotal, Error: p.Error, GameID: gameID,
			},
		})
	}
	for i, sa := range refs {
		if err := installCtxErr(ctx); err != nil {
			return err
		}
		req := download.InstallRequest{
			Context: ctx,
			GameID:  gameID, ArchivePath: sources[i],
			Mode: download.ModeMergeIntoMod, TargetMod: stageName,
			SourceArchiveRef: download.SourceArchiveRef{
				Path: sa.Path, ModID: sa.ModID, FileID: sa.FileID,
				InstalledAt: sa.InstalledAt,
			},
			ProgressSink: sink,
			Layout:       planner,
		}
		if _, err := download.Install(req); err != nil {
			if _, isFomod := download.IsFomodMarker(err); isFomod {
				return &download.FomodReinstallUnsupportedError{Mod: modName}
			}
			slog.Error("reinstall step failed", "archive", sa.Path, "err", err)
			return fmt.Errorf("replaying source archive %q: %w", sa.Path, err)
		}
	}
	return nil
}

// commitReinstall writes the merged metadata into staging and swaps the staged mod in, all under the game's profile lock.
func (md *ModService) commitReinstall(ctx context.Context, gameID, modName, modsDir, token string, snapshot *download.ModMetadata, published *bool) (int, error) {
	defer md.s.lockProfiles(gameID)()
	modDir := filepath.Join(modsDir, modName)
	stageDir := filepath.Join(modsDir, reinstallStagePrefix+token)
	final, err := mergedReinstallMetadata(modDir, stageDir, modName, snapshot)
	if err != nil {
		_ = os.RemoveAll(stageDir)
		return 0, err
	}
	if err := download.SaveModMetadata(stageDir, final); err != nil {
		_ = os.RemoveAll(stageDir)
		return 0, fmt.Errorf("writing reinstalled metadata: %w", err)
	}
	if err := installCtxErr(ctx); err != nil {
		_ = os.RemoveAll(stageDir)
		return 0, err
	}
	*published = true
	if err := md.publishReinstallStage(gameID, modName, modsDir, token, dto.GameRunningOperationReinstall, false); err != nil {
		return 0, err
	}
	if md.s.installAfterPublish != nil {
		md.s.installAfterPublish()
	}
	return final.FileCount, nil
}

// publishReinstallStage records and swaps an already completed stage; the caller holds the game's profile lock.
func (md *ModService) publishReinstallStage(gameID, modName, modsDir, token, operation string, discardOnIntentFailure bool) error {
	stageDir := filepath.Join(modsDir, reinstallStagePrefix+token)
	modDir := filepath.Join(modsDir, modName)
	intent := reinstallIntent{
		SchemaVersion: reinstallIntentVersion,
		Mod:           modName,
		Stage:         reinstallStagePrefix + token,
		Old:           reinstallOldPrefix + token,
	}
	identity, err := atomicfile.Identity(stageDir)
	if err != nil {
		return fmt.Errorf("identifying reinstall stage: %w", err)
	}
	intent.StageIdentity = &identity
	intentPath := filepath.Join(modsDir, reinstallIntentPrefix+token+reinstallIntentSuffix)
	if err := writeReinstallIntent(intentPath, intent); err != nil {
		_ = os.RemoveAll(stageDir)
		if discardOnIntentFailure {
			removeReinstallIntent(intentPath)
		}
		return fmt.Errorf("recording reinstall intent: %w", err)
	}
	if err := md.s.reinstallStep("intent-written"); err != nil {
		if discardOnIntentFailure {
			_ = os.RemoveAll(stageDir)
			removeReinstallIntent(intentPath)
		}
		return err
	}
	if err := md.s.reinstallStep("sync-filesystem"); err != nil {
		return err
	}
	if err := atomicfile.SyncFilesystem(stageDir); err != nil {
		return fmt.Errorf("syncing staged mod files: %w", err)
	}
	if err := md.s.reinstallStep("sync-stage-dir"); err != nil {
		return err
	}
	if err := atomicfile.SyncDir(modsDir); err != nil {
		return fmt.Errorf("syncing mods directory before swap: %w", err)
	}
	return md.swapReinstalledMod(gameID, modName, modDir, stageDir, filepath.Join(modsDir, intent.Old), intentPath, operation)
}

// mergedReinstallMetadata combines the current non-file keys of the original metadata with the replayed source and file lists.
func mergedReinstallMetadata(modDir, stageDir, modName string, snapshot *download.ModMetadata) (*download.ModMetadata, error) {
	base := snapshot
	if _, err := os.Lstat(filepath.Join(modDir, "metadata.yaml")); err == nil {
		fresh, err := download.LoadModMetadata(modDir)
		if err != nil {
			return nil, fmt.Errorf("re-reading mod metadata: %w", err)
		}
		base = fresh
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("re-reading mod metadata: %w", err)
	}
	replay, err := download.LoadModMetadata(stageDir)
	if err != nil {
		return nil, fmt.Errorf("reading replayed metadata: %w", err)
	}
	final := *base
	final.Folder = modName
	final.SourceArchives = replay.SourceArchives
	final.Files = replay.Files
	final.FileCount = replay.FileCount
	return &final, nil
}

// swapReinstalledMod publishes a staged mod and removes the previous folder after rebuilding any deployed farm.
func (md *ModService) swapReinstalledMod(gameID, modName, modDir, stageDir, oldDir, intentPath, operation string) error {
	if err := md.swapInReinstalledMod(gameID, modName, modDir, stageDir, oldDir, intentPath, operation); err != nil {
		return err
	}
	if err := md.s.reinstallStep("sync-swap-dir"); err != nil {
		return err
	}
	if err := atomicfile.SyncDir(filepath.Dir(modDir)); err != nil {
		return fmt.Errorf("syncing mods directory after swap: %w", err)
	}
	if err := md.s.reinstallStep("installed"); err != nil {
		return err
	}
	if err := md.s.reinstallStep("remove-old"); err != nil {
		return err
	}
	if err := os.RemoveAll(oldDir); err != nil {
		slog.Warn("reinstall: removing previous mod folder failed; startup recovery retries", "path", oldDir, "err", err)
		return nil
	}
	if err := md.s.reinstallStep("old-removed"); err != nil {
		return err
	}
	removeReinstallIntent(intentPath)
	return nil
}

// swapInReinstalledMod swaps a completed stage and discards its intent after a refusal or successful rollback.
func (md *ModService) swapInReinstalledMod(gameID, modName, modDir, stageDir, oldDir, intentPath, operation string) error {
	discard, err := md.swapInReinstalledModLocked(gameID, modName, modDir, stageDir, oldDir, operation)
	if discard {
		_ = os.RemoveAll(stageDir)
		removeReinstallIntent(intentPath)
	}
	return err
}

// swapInReinstalledModLocked swaps a mod and rebuilds its mounted farm under s.mu, reporting whether the stage and intent can be discarded.
func (md *ModService) swapInReinstalledModLocked(gameID, modName, modDir, stageDir, oldDir, operation string) (bool, error) {
	md.s.mu.Lock()
	defer md.s.mu.Unlock()
	used, err := md.mountedModUsedLocked(gameID, modName)
	if err != nil {
		return true, err
	}
	var rebuild func() (bool, error)
	if used {
		mm := md.s.mountMgrs[gameID]
		if mm == nil || !mm.IsMounted() {
			return true, &dto.GameRunningError{GameID: gameID, Operation: operation}
		}
		if err := md.checkMountedModChangeLocked(gameID, operation); err != nil {
			return true, err
		}
		release, err := md.s.reserveShared(gameID, dto.BusyOperationApply)
		if err != nil {
			return true, err
		}
		defer release()
		gc, err := md.s.config.EffectiveGameConfig(gameID)
		if err != nil {
			return true, err
		}
		ms := md.s.mountStates[gameID]
		_, entries, err := md.s.profileMgr.Load(gameID, ms.profileName)
		if err != nil {
			return true, fmt.Errorf("loading mounted profile %q: %w", ms.profileName, err)
		}
		previousDesired := md.s.svc.vfs.buildLayers(gameID, gc, entries)
		previousApplied := mm.AppliedLayers()
		wasDirty := mm.IsDirty()
		root, err := md.s.ensureRootDeploymentManager(gameID, gc)
		if err != nil {
			return true, fmt.Errorf("initializing game-root deployment: %w", err)
		}
		rebuild = func() (bool, error) {
			layers := md.s.svc.vfs.buildLayers(gameID, gc, entries)
			err := mm.MarkDirty(layers)
			if err == nil {
				err = md.rematerializeModChangeLocked(mm)
			}
			if err == nil {
				_, err = root.Apply(layers, ms.profileName)
			}
			if err != nil {
				if rollbackErr := md.reinstallRename("rollback-stage", modDir, stageDir); rollbackErr != nil {
					return false, errors.Join(fmt.Errorf("rebuilding deployed mod: %w", err), fmt.Errorf("moving replacement aside for recovery: %w", rollbackErr))
				}
				if rollbackErr := md.reinstallRename("rollback-original", oldDir, modDir); rollbackErr != nil {
					return false, errors.Join(fmt.Errorf("rebuilding deployed mod: %w", err), fmt.Errorf("restoring original mod: %w", rollbackErr))
				}
				restoreErr := md.restoreModFarmLocked(mm, root, ms.profileName, previousApplied, previousDesired, wasDirty)
				return restoreErr == nil, errors.Join(fmt.Errorf("rebuilding deployed mod: %w", err), restoreErr)
			}
			md.s.publishGuarded(dto.StatusEventResult{VFSStatus: md.s.svc.vfs.vfsStatus(gameID, gc, ms.profileName, mm, entries)})
			return false, nil
		}
	}
	if err := md.reinstallRename("move-aside", modDir, oldDir); err != nil {
		return true, fmt.Errorf("moving original mod aside: %w", err)
	}
	if err := md.s.reinstallStep("moved-aside"); err != nil {
		return false, err
	}
	if err := md.reinstallRename("install", stageDir, modDir); err != nil {
		if rollbackErr := md.reinstallRename("restore", oldDir, modDir); rollbackErr != nil {
			return false, errors.Join(fmt.Errorf("installing reinstalled mod: %w", err), fmt.Errorf("restoring original mod from %s: %w", oldDir, rollbackErr))
		}
		return true, fmt.Errorf("installing reinstalled mod: %w", err)
	}
	if err := md.s.reinstallStep("swapped"); err != nil {
		return false, err
	}
	if rebuild != nil {
		if discard, err := rebuild(); err != nil {
			return discard, err
		}
	}
	return false, nil
}

// reinstallRename renames from to to unless the test fault hook fails the named step.
func (md *ModService) reinstallRename(step, from, to string) error {
	if err := md.s.reinstallStep(step); err != nil {
		return err
	}
	return os.Rename(from, to)
}

// reinstallStep runs the test fault hook for a named reinstall step.
func (s *session) reinstallStep(step string) error {
	if s.reinstallFault == nil {
		return nil
	}
	return s.reinstallFault(step)
}

// writeReinstallIntent durably records an in-flight reinstall swap.
func writeReinstallIntent(path string, intent reinstallIntent) error {
	data, err := json.MarshalIndent(intent, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, append(data, '\n'), 0644)
}

// readReinstallIntent loads and validates a reinstall intent record.
func readReinstallIntent(path string) (reinstallIntent, error) {
	var intent reinstallIntent
	data, err := os.ReadFile(path)
	if err != nil {
		return intent, err
	}
	if err := json.Unmarshal(data, &intent); err != nil {
		return intent, fmt.Errorf("parsing reinstall intent: %w", err)
	}
	if intent.SchemaVersion != 1 && intent.SchemaVersion != reinstallIntentVersion {
		return intent, fmt.Errorf("unsupported reinstall intent schema %d", intent.SchemaVersion)
	}
	if intent.SchemaVersion == reinstallIntentVersion && intent.StageIdentity == nil ||
		intent.StageIdentity != nil && intent.StageIdentity.Ino == 0 {
		return intent, fmt.Errorf("reinstall intent has an invalid staging identity")
	}
	if err := download.ValidateTargetModName(intent.Mod); err != nil {
		return intent, err
	}
	token := strings.TrimPrefix(intent.Stage, reinstallStagePrefix)
	if token == intent.Stage || token == "" || strings.HasPrefix(intent.Stage, reinstallOldPrefix) ||
		intent.Old != reinstallOldPrefix+token || fsutil.ValidateName(intent.Stage) != nil || fsutil.ValidateName(intent.Old) != nil {
		return intent, fmt.Errorf("reinstall intent names unsafe folders %q and %q", intent.Stage, intent.Old)
	}
	return intent, nil
}

// removeReinstallIntent deletes a reinstall intent record, logging a failure for startup recovery to clean up.
func removeReinstallIntent(path string) {
	if err := atomicfile.RemoveDurable(path); err != nil {
		slog.Warn("reinstall: removing intent failed; startup recovery cleans it up", "path", path, "err", err)
	}
}

// recoverInterruptedReinstalls resolves reinstall intents and reaps reinstall staging folders in every configured mods dir.
func (s *session) recoverInterruptedReinstalls() {
	for _, gameID := range s.recoverableGameIDs() {
		recoverReinstalls(config.ModsDir(gameID))
	}
}

// recoverReinstalls restores or completes every interrupted reinstall in modsDir, then reaps staging folders no intent claims.
func recoverReinstalls(modsDir string) {
	entries, err := os.ReadDir(modsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("reinstall recovery: reading mods dir failed", "path", modsDir, "err", err)
		}
		return
	}
	claimed := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, reinstallIntentPrefix) || !strings.HasSuffix(name, reinstallIntentSuffix) {
			continue
		}
		intentPath := filepath.Join(modsDir, name)
		token := strings.TrimSuffix(strings.TrimPrefix(name, reinstallIntentPrefix), reinstallIntentSuffix)
		intent, err := readReinstallIntent(intentPath)
		if err == nil {
			err = resolveReinstallIntent(modsDir, intent)
		}
		if err != nil {
			slog.Error("reinstall recovery: leaving interrupted reinstall for manual repair", "intent", intentPath, "err", err)
			claimed[reinstallStagePrefix+token] = true
			claimed[reinstallOldPrefix+token] = true
			continue
		}
		if err := atomicfile.RemoveDurable(intentPath); err != nil {
			slog.Warn("reinstall recovery: removing intent failed", "intent", intentPath, "err", err)
		}
	}
	reapOrphanReinstallDirs(modsDir, claimed)
}

// resolveReinstallIntent restores a missing original mod from its moved-aside copy and removes the intent's staging folders.
func resolveReinstallIntent(modsDir string, intent reinstallIntent) error {
	modDir := filepath.Join(modsDir, intent.Mod)
	stageDir := filepath.Join(modsDir, intent.Stage)
	oldDir := filepath.Join(modsDir, intent.Old)
	present, err := pathPresent(modDir)
	if err != nil {
		return err
	}
	oldPresent, err := pathPresent(oldDir)
	if err != nil {
		return err
	}
	if present && oldPresent {
		identity, err := atomicfile.Identity(modDir)
		if err != nil {
			return fmt.Errorf("identifying reinstall target: %w", err)
		}
		if intent.StageIdentity == nil || identity != *intent.StageIdentity {
			if err := preserveReinstallOld(modsDir, oldDir, intent.Mod, strings.TrimPrefix(intent.Old, reinstallOldPrefix)); err != nil {
				return err
			}
		} else {
			if err := atomicfile.SyncFilesystem(modDir); err != nil {
				return err
			}
			if err := atomicfile.SyncDir(modsDir); err != nil {
				return err
			}
		}
	}
	if !present {
		oldPresent, err := pathPresent(oldDir)
		if err != nil {
			return err
		}
		stagePresent, err := pathPresent(stageDir)
		if err != nil {
			return err
		}
		switch {
		case oldPresent:
			if err := os.Rename(oldDir, modDir); err != nil {
				return fmt.Errorf("restoring mod %q: %w", intent.Mod, err)
			}
			slog.Warn("reinstall recovery: restored the original mod folder", "mod", intent.Mod)
		case stagePresent:
			if err := os.Rename(stageDir, modDir); err != nil {
				return fmt.Errorf("installing staged mod %q: %w", intent.Mod, err)
			}
			slog.Warn("reinstall recovery: installed the completed staging folder", "mod", intent.Mod)
		default:
			return fmt.Errorf("mod %q is missing and neither %s nor %s exists", intent.Mod, intent.Old, intent.Stage)
		}
		if err := atomicfile.SyncDir(modsDir); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(stageDir); err != nil {
		return fmt.Errorf("removing reinstall staging %s: %w", intent.Stage, err)
	}
	if err := os.RemoveAll(oldDir); err != nil {
		return fmt.Errorf("removing previous mod folder %s: %w", intent.Old, err)
	}
	return nil
}

// preserveReinstallOld moves an ambiguous original into a hidden inspectable sibling.
func preserveReinstallOld(modsDir, oldDir, name, token string) error {
	recovered := filepath.Join(modsDir, ".gorganizer-recovered-"+token)
	if exists, err := pathPresent(recovered); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("recovered mod folder %q already exists", recovered)
	}
	if err := unix.Renameat2(unix.AT_FDCWD, oldDir, unix.AT_FDCWD, recovered, unix.RENAME_NOREPLACE); err != nil {
		return fmt.Errorf("preserving previous mod folder: %w", err)
	}
	if err := atomicfile.SyncDir(modsDir); err != nil {
		return err
	}
	slog.Warn("reinstall recovery: preserved previous mod folder", "mod", name, "path", recovered)
	return nil
}

// reapOrphanReinstallDirs removes reinstall staging folders without an intent and restores or removes moved-aside originals by their recorded folder name.
func reapOrphanReinstallDirs(modsDir string, claimed map[string]bool) {
	entries, err := os.ReadDir(modsDir)
	if err != nil {
		slog.Warn("reinstall recovery: reading mods dir failed", "path", modsDir, "err", err)
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || claimed[name] {
			continue
		}
		path := filepath.Join(modsDir, name)
		switch {
		case strings.HasPrefix(name, reinstallOldPrefix):
			recoverOrphanOldDir(modsDir, path)
		case strings.HasPrefix(name, reinstallStagePrefix):
			if err := os.RemoveAll(path); err != nil {
				slog.Warn("reinstall recovery: removing orphan staging failed", "path", path, "err", err)
				continue
			}
			slog.Info("reinstall recovery: removed orphan staging folder", "path", path)
		}
	}
}

// recoverOrphanOldDir restores a moved-aside original whose mod folder is missing and removes it when the mod folder exists.
func recoverOrphanOldDir(modsDir, oldDir string) {
	meta, err := download.LoadModMetadata(oldDir)
	if err != nil || meta == nil || download.ValidateTargetModName(meta.Folder) != nil {
		slog.Error("reinstall recovery: leaving a previous mod folder with no usable recorded name", "path", oldDir, "err", err)
		return
	}
	modDir := filepath.Join(modsDir, meta.Folder)
	present, err := pathPresent(modDir)
	if err != nil {
		slog.Error("reinstall recovery: leaving a previous mod folder", "path", oldDir, "err", err)
		return
	}
	if present {
		if err := preserveReinstallOld(modsDir, oldDir, meta.Folder, strings.TrimPrefix(filepath.Base(oldDir), reinstallOldPrefix)); err != nil {
			slog.Error("reinstall recovery: leaving ambiguous previous mod folder", "path", oldDir, "err", err)
		}
		return
	}
	if err := os.Rename(oldDir, modDir); err != nil {
		slog.Error("reinstall recovery: restoring previous mod folder failed", "path", oldDir, "mod", meta.Folder, "err", err)
		return
	}
	slog.Warn("reinstall recovery: restored a previous mod folder by its recorded name", "mod", meta.Folder)
}

// pathPresent reports whether path exists without following a final symlink.
func pathPresent(path string) (bool, error) {
	if _, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

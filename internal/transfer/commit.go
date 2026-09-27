package transfer

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/fsutil"
)

const (
	transferIntentPrefix = ".gorganizer-transfer-intent-"
	transferIntentSuffix = ".json"
	transferStagePrefix  = ".transfer-stage-"
	transferOldPrefix    = ".transfer-old-"
)

type transferIntent struct {
	SchemaVersion int    `json:"schema_version"`
	OpID          string `json:"op_id"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Staged        string `json:"staged"`
	Old           string `json:"old"`
}

type transferCommitOps struct {
	rename       func(step, from, to string) error
	remove       func(step, path string) error
	removeIntent func(string) error
	sync         func(string) error
}

// replaceDir replaces a mod or profile directory with a journaled same-root swap.
func replaceDir(root, name, staged string) error {
	kind := "mod"
	if filepath.Base(root) == "profiles" {
		kind = "profile"
	}
	return replaceDirWithOps(root, name, staged, kind, defaultTransferCommitOps())
}

// defaultTransferCommitOps supplies the filesystem operations used by replacement.
func defaultTransferCommitOps() transferCommitOps {
	return transferCommitOps{
		rename:       func(_ string, from, to string) error { return os.Rename(from, to) },
		remove:       func(_ string, path string) error { return os.RemoveAll(path) },
		removeIntent: atomicfile.RemoveDurable,
		sync:         atomicfile.SyncDir,
	}
}

// transferPathPresent checks a path without following its final component.
func transferPathPresent(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// transferRealDir requires an existing path to be a real directory.
func transferRealDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a real directory", path)
	}
	return nil
}

// replaceDirWithOps publishes a staged directory and preserves its original until the new target is synced.
func replaceDirWithOps(root, name, staged, kind string, ops transferCommitOps) error {
	if kind != "mod" && kind != "profile" || fsutil.ValidateName(name) != nil || strings.HasPrefix(name, ".") ||
		kind == "mod" && download.ValidateTargetModName(name) != nil ||
		filepath.Dir(staged) == root && filepath.Base(staged) == name {
		return fmt.Errorf("unsafe transfer replacement name %q", name)
	}
	if err := transferRealDir(staged); err != nil {
		return fmt.Errorf("checking transfer stage: %w", err)
	}
	if err := transferRealDir(filepath.Join(root, name)); err != nil {
		return fmt.Errorf("checking transfer target: %w", err)
	}
	pending, err := pendingTransferFor(root, name)
	if err != nil {
		return &transferCommitError{pending: true, err: fmt.Errorf("checking previous transfer: %w", err)}
	}
	if pending {
		return &transferCommitError{pending: true, err: fmt.Errorf("a previous replacement of %q still needs recovery", name)}
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Errorf("creating transfer identity: %w", err)
	}
	id := hex.EncodeToString(random[:])
	intent := transferIntent{SchemaVersion: 1, OpID: id, Kind: kind, Name: name,
		Staged: transferStagePrefix + id, Old: transferOldPrefix + id}
	stagePath := filepath.Join(root, intent.Staged)
	target := filepath.Join(root, name)
	oldPath := filepath.Join(root, intent.Old)
	intentPath := filepath.Join(root, transferIntentPrefix+id+transferIntentSuffix)
	for _, path := range []string{stagePath, oldPath, intentPath} {
		present, err := transferPathPresent(path)
		if err != nil {
			return fmt.Errorf("checking transfer journal destination: %w", err)
		}
		if present {
			return fmt.Errorf("transfer journal destination %q already exists", path)
		}
	}
	if err := ops.rename("stage", staged, stagePath); err != nil {
		return fmt.Errorf("moving transfer stage into destination: %w", err)
	}
	if err := ops.sync(root); err != nil {
		_ = ops.rename("unstage", stagePath, staged)
		return fmt.Errorf("syncing transfer stage: %w", err)
	}
	data, err := json.Marshal(intent)
	if err != nil {
		return fmt.Errorf("encoding transfer intent: %w", err)
	}
	outcome, err := atomicfile.WriteFileDurable(intentPath, data, 0644)
	if err != nil {
		if outcome == atomicfile.NotPublished {
			_ = ops.rename("unstage", stagePath, staged)
		}
		return &transferCommitError{pending: outcome != atomicfile.NotPublished, err: fmt.Errorf("recording transfer intent: %w", err)}
	}
	failBeforeSwap := func(cause error, movedAside bool) error {
		if movedAside {
			if err := ops.rename("restore", oldPath, target); err != nil {
				return &transferCommitError{pending: true, err: errors.Join(cause, fmt.Errorf("restoring original: %w", err))}
			}
		}
		if err := ops.sync(root); err != nil {
			return &transferCommitError{pending: true, err: errors.Join(cause, err)}
		}
		if err := ops.remove("discard-stage", stagePath); err != nil {
			return &transferCommitError{pending: true, err: errors.Join(cause, err)}
		}
		if err := ops.removeIntent(intentPath); err != nil {
			return &transferCommitError{pending: true, err: errors.Join(cause, err)}
		}
		return cause
	}
	if err := ops.rename("move-aside", target, oldPath); err != nil {
		return failBeforeSwap(fmt.Errorf("moving original aside: %w", err), false)
	}
	if err := ops.rename("install", stagePath, target); err != nil {
		return failBeforeSwap(fmt.Errorf("installing transfer replacement: %w", err), true)
	}
	if err := ops.sync(root); err != nil {
		return &transferCommitError{committed: true, pending: true, err: fmt.Errorf("syncing transfer replacement: %w", err)}
	}
	if err := ops.remove("remove-old", oldPath); err != nil {
		return &transferCommitError{committed: true, pending: true, err: fmt.Errorf("removing previous folder: %w", err)}
	}
	if err := ops.removeIntent(intentPath); err != nil {
		return &transferCommitError{committed: true, pending: true, err: fmt.Errorf("removing transfer intent: %w", err)}
	}
	return nil
}

// pendingTransferFor reports whether a prior journal still claims this target or an unreadable journal blocks safe replacement.
func pendingTransferFor(root, name string) (bool, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		fileName := entry.Name()
		if !strings.HasPrefix(fileName, transferIntentPrefix) || !strings.HasSuffix(fileName, transferIntentSuffix) {
			continue
		}
		intent, err := loadTransferIntent(filepath.Join(root, fileName), entry)
		if err != nil {
			return false, err
		}
		if intent.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// loadTransferIntent reads a bounded regular journal and validates every recorded path component.
func loadTransferIntent(path string, entry os.DirEntry) (transferIntent, error) {
	var intent transferIntent
	info, err := entry.Info()
	if err != nil {
		return intent, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return intent, fmt.Errorf("unsafe transfer intent %q: not a small regular file", entry.Name())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return intent, err
	}
	if len(data) > 4096 {
		return intent, fmt.Errorf("unsafe transfer intent %q: oversized journal", entry.Name())
	}
	if err := json.Unmarshal(data, &intent); err != nil {
		return intent, err
	}
	return intent, validateTransferIntent(intent, entry.Name())
}

// validateTransferIntent requires a single safe segment for every recorded name and binds the record to its filename.
func validateTransferIntent(intent transferIntent, fileName string) error {
	id := strings.TrimSuffix(strings.TrimPrefix(fileName, transferIntentPrefix), transferIntentSuffix)
	if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" || intent.OpID != id || intent.SchemaVersion != 1 ||
		(intent.Kind != "mod" && intent.Kind != "profile") || fsutil.ValidateName(intent.Name) != nil || strings.HasPrefix(intent.Name, ".") ||
		intent.Kind == "mod" && download.ValidateTargetModName(intent.Name) != nil ||
		fsutil.ValidateName(intent.Staged) != nil || fsutil.ValidateName(intent.Old) != nil ||
		intent.Staged != transferStagePrefix+id || intent.Old != transferOldPrefix+id {
		return fmt.Errorf("unsafe transfer intent %q", fileName)
	}
	return nil
}

// RecoverTransfers resolves every journaled replacement in root without acting on invalid intents.
func RecoverTransfers(root string) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading transfer directory: %w", err)
	}
	var failures []error
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, transferIntentPrefix) || !strings.HasSuffix(name, transferIntentSuffix) {
			continue
		}
		path := filepath.Join(root, name)
		intent, err := loadTransferIntent(path, entry)
		if err == nil {
			err = resolveTransferIntent(root, intent)
		}
		if err == nil {
			err = atomicfile.RemoveDurable(path)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("recovering transfer intent %q: %w", name, err))
		}
	}
	return errors.Join(failures...)
}

// resolveTransferIntent restores an absent target from its original or staged folder before removing leftovers.
func resolveTransferIntent(root string, intent transferIntent) error {
	target := filepath.Join(root, intent.Name)
	old := filepath.Join(root, intent.Old)
	stage := filepath.Join(root, intent.Staged)
	for _, path := range []string{target, old, stage} {
		present, err := transferPathPresent(path)
		if err != nil {
			return err
		}
		if present {
			if err := transferRealDir(path); err != nil {
				return err
			}
		}
	}
	present, _ := transferPathPresent(target)
	if !present {
		oldPresent, _ := transferPathPresent(old)
		stagePresent, _ := transferPathPresent(stage)
		switch {
		case oldPresent:
			if err := os.Rename(old, target); err != nil {
				return fmt.Errorf("restoring original %q: %w", intent.Name, err)
			}
		case stagePresent:
			if err := os.Rename(stage, target); err != nil {
				return fmt.Errorf("installing staged %q: %w", intent.Name, err)
			}
		default:
			return fmt.Errorf("transfer target %q and both recovery folders are missing", intent.Name)
		}
		if err := atomicfile.SyncDir(root); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(stage); err != nil {
		return fmt.Errorf("removing transfer stage: %w", err)
	}
	if err := os.RemoveAll(old); err != nil {
		return fmt.Errorf("removing previous transfer folder: %w", err)
	}
	return atomicfile.SyncDir(root)
}

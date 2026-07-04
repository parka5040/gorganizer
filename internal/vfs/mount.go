package vfs

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// MountManager activates a per-game overlay as a hardlink farm at gameDataPath,
// preserving the original Data/ at gameDataPath + ".orig".
type MountManager struct {
	gameDataPath  string
	backupSuffix  string
	overwriteRoot string
	gameID        string

	tree        *MergedTree
	layers      []Layer // current desired layout (base already pointed at backup)
	profileName string
	mounted     bool

	// desiredGen advances on every MarkDirty; appliedGen catches up when the
	// on-disk farm is (re)materialized. IsDirty ⇔ desiredGen != appliedGen.
	desiredGen uint64
	appliedGen uint64

	mu sync.Mutex
}

// NewMountManager creates a MountManager; overwriteRoot may be "" to disable
// write capture. gameID is recorded in the sentinel for identity/forensics.
func NewMountManager(gameDataPath string, overwriteRoot string, gameID string) *MountManager {
	return &MountManager{
		gameDataPath:  gameDataPath,
		backupSuffix:  ".orig",
		overwriteRoot: overwriteRoot,
		gameID:        gameID,
	}
}

// SetOverwriteRoot updates the write-capture target; safe only when not mounted.
func (m *MountManager) SetOverwriteRoot(root string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.overwriteRoot = root
}

// SetMountedForTesting flips the mounted flag without filesystem work; test-only.
func (m *MountManager) SetMountedForTesting(mounted bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mounted = mounted
}

// Activate replaces Data/ with a materialized hardlink farm; layer 0 must be
// "__base__". It is intent-first: an activation marker is written before the
// destructive rename so a crash anywhere below self-heals via CleanupStale
// rather than degrading to a manual recovery prompt (H-3).
func (m *MountManager) Activate(layers []Layer, profileName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mounted {
		return ErrAlreadyMounted
	}

	dataPath := m.gameDataPath
	backupPath := dataPath + m.backupSuffix

	if mount, err := DetectFuseMount(dataPath); err == nil && mount != nil {
		return fmt.Errorf("vfs: refusing to activate over a live FUSE mount at %s (run `gorganizerctl recover --game ...`)", dataPath)
	}
	if _, err := os.Stat(dataPath); os.IsNotExist(err) {
		return fmt.Errorf("%w: %s", ErrDataDirMissing, dataPath)
	}
	if _, err := os.Stat(backupPath); err == nil {
		return fmt.Errorf("%w: %s", ErrBackupExists, backupPath)
	}

	// Reap stale transient siblings from a prior interrupted run before dropping
	// our own intent marker (defensive; recovery normally clears these first).
	_ = os.RemoveAll(stagingDirPath(dataPath))
	_ = os.RemoveAll(oldFarmPath(dataPath))
	_ = RemoveIntent(applyingIntentPath(dataPath))

	// Point the base layer at the (soon-to-be) backup before building, so the
	// farm links base files from Data.orig rather than the path we rename away.
	if len(layers) > 0 && layers[0].Name == "__base__" {
		layers[0].RootPath = backupPath
	}
	overwriteName := m.deriveOverwriteName(layers)
	sentLayers := layersForSentinel(layers)

	intentPath := activatingIntentPath(dataPath)
	if err := WriteIntent(intentPath, &ActivationIntent{
		SchemaVersion: CurrentIntentSchema,
		Magic:         IntentMagic,
		Kind:          IntentActivating,
		GameID:        m.gameID,
		DataPath:      dataPath,
		BackupPath:    backupPath,
		OverwriteRoot: m.overwriteRoot,
		PID:           os.Getpid(),
	}); err != nil {
		return fmt.Errorf("writing activation intent: %w", err)
	}

	slog.Info("renaming data directory", "from", dataPath, "to", backupPath)
	if err := os.Rename(dataPath, backupPath); err != nil {
		_ = RemoveIntent(intentPath)
		return fmt.Errorf("renaming %s to %s: %w", dataPath, backupPath, err)
	}

	tree := NewMergedTree()
	if err := tree.Build(layers); err != nil {
		_ = os.Rename(backupPath, dataPath)
		_ = RemoveIntent(intentPath)
		return fmt.Errorf("building merged tree: %w", err)
	}

	stats, err := BuildInto(dataPath, tree, layers, overwriteName)
	if err != nil {
		_ = os.RemoveAll(dataPath)
		_ = os.Rename(backupPath, dataPath)
		_ = RemoveIntent(intentPath)
		return fmt.Errorf("materializing overlay: %w", err)
	}

	sentinel := &Sentinel{
		SchemaVersion:       CurrentSentinelSchema,
		Magic:               SentinelMagic,
		GameID:              m.gameID,
		ProfileName:         profileName,
		ActivationPID:       os.Getpid(),
		ActivationStartedAt: time.Now().UTC(),
		Hash:                ComputeLayerHash(sentLayers),
		BackupPath:          backupPath,
		OverwriteMod:        overwriteName,
		OverwriteRoot:       m.overwriteRoot,
		Layers:              sentLayers,
		MaterializerVersion: CurrentMaterializerVersion,
	}
	if err := WriteSentinel(dataPath, sentinel); err != nil {
		_ = os.RemoveAll(dataPath)
		_ = os.Rename(backupPath, dataPath)
		_ = RemoveIntent(intentPath)
		return fmt.Errorf("writing sentinel: %w", err)
	}

	// Commit: the sentinel is durable, so the intent marker is no longer needed.
	_ = RemoveIntent(intentPath)

	m.tree = tree
	m.layers = layers
	m.profileName = profileName
	m.mounted = true
	m.desiredGen = 1
	m.appliedGen = 1

	slog.Info("VFS materialized",
		"path", dataPath,
		"layers", len(layers),
		"files_hardlinked", stats.FilesHardlinked,
		"files_symlinked", stats.FilesSymlinked,
		"dirs_created", stats.DirsCreated,
		"overwrite_mod", overwriteName)

	return nil
}

// Deactivate validates the sentinel, captures new writes, then restores
// Data.orig. On a capture failure it aborts and leaves the farm intact (H-1).
func (m *MountManager) Deactivate() error { return m.deactivate(false) }

// ForceDeactivate tears down even if capturing new writes fails, discarding the
// uncaptured files. Only for explicit, user-consented teardown.
func (m *MountManager) ForceDeactivate() error { return m.deactivate(true) }

func (m *MountManager) deactivate(force bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.mounted {
		return ErrNotMounted
	}

	dataPath := m.gameDataPath
	backupPath := dataPath + m.backupSuffix

	s, err := ReadSentinel(dataPath)
	if err != nil {
		return fmt.Errorf("validating overlay before tear-down: %w", err)
	}
	if vErr := ValidateSentinel(s); vErr != nil {
		return fmt.Errorf("sentinel rejected: %w", vErr)
	}

	if m.overwriteRoot != "" {
		moved, capErr := CaptureNewFiles(dataPath, m.overwriteRoot)
		if capErr != nil {
			if !force {
				// Do NOT proceed to RemoveAll — that would destroy the uncaptured
				// writes we just failed to save (saves, tool output). Leave the farm
				// mounted and intact; the user can retry, fix the cause, or force (H-1).
				return fmt.Errorf("%w: %v", ErrCaptureFailed, capErr)
			}
			slog.Warn("force deactivate: capture failed, proceeding and discarding uncaptured writes",
				"path", dataPath, "err", capErr)
		} else if moved > 0 {
			slog.Info("captured tool/game writes into overwrite mod",
				"count", moved, "overwrite_root", m.overwriteRoot)
		}
	}

	slog.Info("tearing down materialized overlay", "path", dataPath)
	if err := os.RemoveAll(dataPath); err != nil {
		return fmt.Errorf("removing materialized %s: %w", dataPath, err)
	}

	if err := os.Rename(backupPath, dataPath); err != nil {
		return fmt.Errorf("restoring %s from %s: %w", dataPath, backupPath, err)
	}

	m.tree = nil
	m.layers = nil
	m.mounted = false
	m.desiredGen = 0
	m.appliedGen = 0

	slog.Info("VFS deactivated and data directory restored", "path", dataPath)
	return nil
}

// MarkDirty updates the in-memory desired layout without touching the on-disk
// farm and advances desiredGen, coalescing rapid edits. The farm is brought in
// sync later by ReMaterialize (before launch, or on explicit Apply). The base
// layer is re-pointed at the backup since, while mounted, the real base files
// live in Data.orig.
func (m *MountManager) MarkDirty(layers []Layer) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.mounted {
		return ErrNotMounted
	}
	if len(layers) > 0 && layers[0].Name == "__base__" {
		layers[0].RootPath = m.gameDataPath + m.backupSuffix
	}
	tree := NewMergedTree()
	if err := tree.Build(layers); err != nil {
		return fmt.Errorf("rebuilding merged tree: %w", err)
	}
	m.tree = tree
	m.layers = layers
	m.desiredGen++
	return nil
}

// ReMaterialize rebuilds the on-disk farm to match the current in-memory tree,
// making pending MarkDirty edits visible to the game/tools. It captures new
// writes first (H-1), builds a fresh farm in a staging sibling, then atomically
// swaps it into place — Data is never observed partial, and open FDs survive.
// It takes no layers argument: it materializes exactly the current tree and
// advances appliedGen to the desiredGen captured under this lock (Guard R1), so
// a concurrent MarkDirty can never be mis-reported as clean.
func (m *MountManager) ReMaterialize() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.mounted {
		return ErrNotMounted
	}

	dataPath := m.gameDataPath
	targetGen := m.desiredGen
	if m.appliedGen == targetGen {
		return nil // already in sync
	}

	s, err := ReadSentinel(dataPath)
	if err != nil {
		return fmt.Errorf("validating overlay before apply: %w", err)
	}
	if vErr := ValidateSentinel(s); vErr != nil {
		return fmt.Errorf("sentinel rejected: %w", vErr)
	}

	// Capture new writes (saves, tool output) into Overwrite BEFORE we rebuild,
	// so they are durable and reappear in the rebuilt farm. On failure, abort
	// without swapping (H-1).
	if m.overwriteRoot != "" {
		if _, capErr := CaptureNewFiles(dataPath, m.overwriteRoot); capErr != nil {
			return fmt.Errorf("%w: %v", ErrCaptureFailed, capErr)
		}
	}

	// Rebuild the tree from the current layers AFTER capture, so freshly-captured
	// files (now in the Overwrite layer) are included in the materialized farm.
	tree := NewMergedTree()
	if err := tree.Build(m.layers); err != nil {
		return fmt.Errorf("rebuilding merged tree for apply: %w", err)
	}
	m.tree = tree

	staging := stagingDirPath(dataPath)
	_ = os.RemoveAll(staging)
	overwriteName := m.deriveOverwriteName(m.layers)
	if _, err := BuildInto(staging, tree, m.layers, overwriteName); err != nil {
		_ = os.RemoveAll(staging)
		return fmt.Errorf("materializing staging overlay: %w", err)
	}

	sentLayers := layersForSentinel(m.layers)
	if err := WriteSentinel(staging, &Sentinel{
		SchemaVersion:       CurrentSentinelSchema,
		Magic:               SentinelMagic,
		GameID:              m.gameID,
		ProfileName:         m.profileName,
		ActivationPID:       os.Getpid(),
		ActivationStartedAt: time.Now().UTC(),
		Hash:                ComputeLayerHash(sentLayers),
		BackupPath:          dataPath + m.backupSuffix,
		OverwriteMod:        overwriteName,
		OverwriteRoot:       m.overwriteRoot,
		Layers:              sentLayers,
		MaterializerVersion: CurrentMaterializerVersion,
	}); err != nil {
		_ = os.RemoveAll(staging)
		return fmt.Errorf("writing staging sentinel: %w", err)
	}

	applyPath := applyingIntentPath(dataPath)
	if err := WriteIntent(applyPath, &ActivationIntent{
		SchemaVersion: CurrentIntentSchema,
		Magic:         IntentMagic,
		Kind:          IntentApplying,
		GameID:        m.gameID,
		DataPath:      dataPath,
		BackupPath:    dataPath + m.backupSuffix,
		OverwriteRoot: m.overwriteRoot,
		StagingPath:   staging,
		PID:           os.Getpid(),
	}); err != nil {
		_ = os.RemoveAll(staging)
		return fmt.Errorf("writing apply intent: %w", err)
	}

	// Atomic commit: swap the fresh staging farm with the live Data farm. After
	// this, dataPath is the new farm and staging is the old one.
	if err := renameExchange(dataPath, staging); err != nil {
		// Fallback for filesystems without RENAME_EXCHANGE: two renames with a
		// brief window where Data is absent. Recovery reaps oldfarm/staging and
		// restores from backup if interrupted.
		oldFarm := oldFarmPath(dataPath)
		_ = os.RemoveAll(oldFarm)
		if rerr := os.Rename(dataPath, oldFarm); rerr != nil {
			_ = os.RemoveAll(staging)
			_ = RemoveIntent(applyPath)
			return fmt.Errorf("apply swap (fallback, aside): %w", rerr)
		}
		if rerr := os.Rename(staging, dataPath); rerr != nil {
			// Best-effort roll back to the old farm.
			_ = os.Rename(oldFarm, dataPath)
			_ = RemoveIntent(applyPath)
			return fmt.Errorf("apply swap (fallback, in): %w", rerr)
		}
		_ = os.RemoveAll(oldFarm)
	}

	_ = os.RemoveAll(staging)
	_ = RemoveIntent(applyPath)
	m.appliedGen = targetGen

	slog.Info("VFS re-materialized to apply pending changes",
		"path", dataPath, "applied_gen", m.appliedGen, "desired_gen", m.desiredGen)
	return nil
}

// IsDirty reports whether pending edits are not yet applied to the on-disk farm.
func (m *MountManager) IsDirty() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mounted && m.desiredGen != m.appliedGen
}

// Generations returns (applied, desired) for status reporting.
func (m *MountManager) Generations() (applied, desired uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.appliedGen, m.desiredGen
}

// RecoverIfNeeded handles startup recovery via CleanupStale.
func (m *MountManager) RecoverIfNeeded() (RecoveryOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	outcome, err := CleanupStale(m.gameDataPath)
	if err != nil {
		return outcome, fmt.Errorf("recovering %s: %w", m.gameDataPath, err)
	}
	return outcome, nil
}

func (m *MountManager) DataPath() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.gameDataPath
}

// BackupPath returns the location of the original Data/ while mounted (Data.orig).
func (m *MountManager) BackupPath() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.gameDataPath + m.backupSuffix
}

func (m *MountManager) IsMounted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mounted
}

func (m *MountManager) Tree() *MergedTree {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tree
}

// WaitMount is a no-op preserved for API compatibility with the FUSE backend.
func (m *MountManager) WaitMount() {}

// deriveOverwriteName returns the layer Name whose RootPath matches overwriteRoot.
func (m *MountManager) deriveOverwriteName(layers []Layer) string {
	if m.overwriteRoot == "" {
		return ""
	}
	want, err := filepath.Abs(m.overwriteRoot)
	if err != nil {
		want = m.overwriteRoot
	}
	for _, l := range layers {
		got, err := filepath.Abs(l.RootPath)
		if err != nil {
			got = l.RootPath
		}
		if got == want {
			return l.Name
		}
	}
	return ""
}

func layersForSentinel(layers []Layer) []SentinelLayer {
	out := make([]SentinelLayer, 0, len(layers))
	for _, l := range layers {
		out = append(out, SentinelLayer{
			Name:    l.Name,
			Root:    l.RootPath,
			Enabled: l.Enabled,
		})
	}
	return out
}

var _ = errors.Is

package vfs

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type MountManager struct {
	gameDataPath  string
	backupSuffix  string
	overwriteRoot string
	gameID        string

	tree          *MergedTree
	layers        []Layer
	appliedLayers []Layer
	profileName   string
	mounted       bool

	desiredGen uint64
	appliedGen uint64

	mu sync.Mutex
}

// NewMountManager creates a MountManager; an empty overwriteRoot disables write capture.
func NewMountManager(gameDataPath string, overwriteRoot string, gameID string) *MountManager {
	return &MountManager{
		gameDataPath:  gameDataPath,
		backupSuffix:  farmBackupSuffix,
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

// Activate replaces Data/ with a hardlink farm of the given layers; layer 0 must be "__base__".
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

	_ = os.RemoveAll(stagingDirPath(dataPath))
	_ = os.RemoveAll(oldFarmPath(dataPath))
	_ = RemoveIntent(applyingIntentPath(dataPath))

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
		FarmID:              stats.FarmID,
		Manifest:            stats.Manifest,
		ManifestSHA256:      stats.ManifestSHA256,
		ManifestEntries:     stats.ManifestEntries,
	}
	if err := WriteSentinel(dataPath, sentinel); err != nil {
		_ = os.RemoveAll(dataPath)
		_ = os.Rename(backupPath, dataPath)
		_ = RemoveIntent(intentPath)
		return fmt.Errorf("writing sentinel: %w", err)
	}

	if err := RemoveIntent(intentPath); err != nil {
		slog.Warn("activation committed but could not remove intent", "path", intentPath, "err", err)
	}

	m.tree = tree
	m.layers = layers
	m.appliedLayers = append([]Layer(nil), layers...)
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

// Deactivate captures new writes into Overwrite and restores Data.orig, leaving the farm intact if capture fails.
func (m *MountManager) Deactivate() error { return m.deactivate(false) }

// ForceDeactivate tears down the farm even if capture fails, discarding uncaptured files.
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
	m.appliedLayers = nil
	m.mounted = false
	m.desiredGen = 0
	m.appliedGen = 0

	slog.Info("VFS deactivated and data directory restored", "path", dataPath)
	return nil
}

// MarkDirty updates the in-memory desired layout and advances desiredGen without touching the on-disk farm.
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

// ReMaterialize captures new writes and atomically swaps in a farm rebuilt from the current in-memory tree.
func (m *MountManager) ReMaterialize() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.mounted {
		return ErrNotMounted
	}

	dataPath := m.gameDataPath
	targetGen := m.desiredGen
	if m.appliedGen == targetGen {
		return nil
	}

	s, err := ReadSentinel(dataPath)
	if err != nil {
		return fmt.Errorf("validating overlay before apply: %w", err)
	}
	if vErr := ValidateSentinel(s); vErr != nil {
		return fmt.Errorf("sentinel rejected: %w", vErr)
	}

	if m.overwriteRoot != "" {
		if _, capErr := CaptureNewFiles(dataPath, m.overwriteRoot); capErr != nil {
			return fmt.Errorf("%w: %v", ErrCaptureFailed, capErr)
		}
	}

	tree := NewMergedTree()
	if err := tree.Build(m.layers); err != nil {
		return fmt.Errorf("rebuilding merged tree for apply: %w", err)
	}
	m.tree = tree

	staging := stagingDirPath(dataPath)
	_ = os.RemoveAll(staging)
	overwriteName := m.deriveOverwriteName(m.layers)
	stats, err := BuildInto(staging, tree, m.layers, overwriteName)
	if err != nil {
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
		FarmID:              stats.FarmID,
		Manifest:            stats.Manifest,
		ManifestSHA256:      stats.ManifestSHA256,
		ManifestEntries:     stats.ManifestEntries,
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

	if err := renameExchange(dataPath, staging); err != nil {
		if rmErr := os.RemoveAll(staging); rmErr != nil {
			err = errors.Join(err, fmt.Errorf("removing staging overlay: %w", rmErr))
		}
		if rmErr := RemoveIntent(applyPath); rmErr != nil {
			err = errors.Join(err, rmErr)
		}
		if errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EINVAL) {
			return fmt.Errorf("this game's drive does not support the atomic folder swap Gorganizer needs to apply changes while mods are active; deactivate mods, then apply: %w", err)
		}
		return fmt.Errorf("apply swap: %w", err)
	}

	_ = os.RemoveAll(staging)
	if err := RemoveIntent(applyPath); err != nil {
		slog.Warn("apply committed but could not remove intent", "path", applyPath, "err", err)
	}
	m.appliedGen = targetGen
	m.appliedLayers = append([]Layer(nil), m.layers...)

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

func (m *MountManager) AppliedLayers() []Layer {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Layer(nil), m.appliedLayers...)
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

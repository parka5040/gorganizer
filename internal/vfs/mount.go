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

	tree    *MergedTree
	mounted bool
	mu      sync.Mutex
}

// NewMountManager creates a MountManager; overwriteRoot may be "" to disable write capture.
func NewMountManager(gameDataPath string, overwriteRoot string) *MountManager {
	return &MountManager{
		gameDataPath:  gameDataPath,
		backupSuffix:  ".orig",
		overwriteRoot: overwriteRoot,
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

// Activate replaces Data/ with a materialized hardlink farm; layer 0 must be "__base__".
func (m *MountManager) Activate(layers []Layer) error {
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

	slog.Info("renaming data directory", "from", dataPath, "to", backupPath)
	if err := os.Rename(dataPath, backupPath); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", dataPath, backupPath, err)
	}

	if len(layers) > 0 && layers[0].Name == "__base__" {
		layers[0].RootPath = backupPath
	}

	tree := NewMergedTree()
	if err := tree.Build(layers); err != nil {
		_ = os.Rename(backupPath, dataPath)
		return fmt.Errorf("building merged tree: %w", err)
	}

	overwriteName := m.deriveOverwriteName(layers)

	stats, err := BuildInto(dataPath, tree, layers, overwriteName)
	if err != nil {
		_ = os.RemoveAll(dataPath)
		_ = os.Rename(backupPath, dataPath)
		return fmt.Errorf("materializing overlay: %w", err)
	}

	sentinel := &Sentinel{
		SchemaVersion:       CurrentSentinelSchema,
		Magic:               SentinelMagic,
		ProfileName:         "",
		ActivationPID:       os.Getpid(),
		ActivationStartedAt: time.Now().UTC(),
		BackupPath:          backupPath,
		OverwriteMod:        overwriteName,
		Layers:              layersForSentinel(layers),
		MaterializerVersion: CurrentMaterializerVersion,
	}
	if err := WriteSentinel(dataPath, sentinel); err != nil {
		_ = os.RemoveAll(dataPath)
		_ = os.Rename(backupPath, dataPath)
		return fmt.Errorf("writing sentinel: %w", err)
	}

	m.tree = tree
	m.mounted = true

	slog.Info("VFS materialized",
		"path", dataPath,
		"layers", len(layers),
		"files_hardlinked", stats.FilesHardlinked,
		"files_symlinked", stats.FilesSymlinked,
		"dirs_created", stats.DirsCreated,
		"overwrite_mod", overwriteName)

	return nil
}

// Deactivate validates the sentinel, captures new writes, then restores Data.orig.
func (m *MountManager) Deactivate() error {
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
			// Do NOT proceed to RemoveAll — that would destroy the uncaptured
			// writes we just failed to save (saves, tool output). Leave the farm
			// mounted and intact; the user can retry, fix the cause, or force (H-1).
			return fmt.Errorf("%w: %v", ErrCaptureFailed, capErr)
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
	m.mounted = false

	slog.Info("VFS deactivated and data directory restored", "path", dataPath)
	return nil
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

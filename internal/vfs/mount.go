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

// MountManager activates a per-game overlay by materializing the merged
// layer view as a hardlink farm at gameDataPath, with the original Data/
// preserved at gameDataPath + ".orig". No FUSE, no userspace daemon in the
// kernel I/O path — once Activate returns, daemon death cannot make the
// game's Data dir unreadable, which was the failure mode that motivated
// this rewrite.
//
// The public API is preserved verbatim from the previous FUSE-backed
// implementation so daemon.go callers compile unchanged. The on-disk
// shape, however, is completely different:
//
//   - Activate: rename Data → Data.orig, materialize hardlink farm at
//     Data, write sentinel at Data/.gorganizer-overlay.json.
//   - Deactivate: validate sentinel, capture any new files (writes that
//     went via atomic-save and broke the hardlink) into the configured
//     overwrite mod, rm -rf Data, mv Data.orig → Data.
//   - RecoverIfNeeded: delegates to CleanupStale, which knows how to
//     reap a leftover FUSE mount from the prior implementation AND how
//     to restore Data.orig when the daemon died mid-session.
type MountManager struct {
	gameDataPath  string // .../Skyrim Special Edition/Data
	backupSuffix  string // ".orig"
	overwriteRoot string // optional: configured Overwrite mod's directory; "" = no escape hatch

	tree    *MergedTree
	mounted bool
	mu      sync.Mutex
}

// NewMountManager creates a MountManager for the given game Data path.
// overwriteRoot is the absolute path to the configured Overwrite mod's
// folder; pass "" to disable the write-capture escape hatch (writes to
// non-overwrite materialized files will still fail with EACCES due to
// the read-only mode policy).
func NewMountManager(gameDataPath string, overwriteRoot string) *MountManager {
	return &MountManager{
		gameDataPath:  gameDataPath,
		backupSuffix:  ".orig",
		overwriteRoot: overwriteRoot,
	}
}

// SetOverwriteRoot updates the path the manager uses for write capture on
// Deactivate. Provided as a setter so the daemon can swap profiles' overwrite
// mods without recreating the manager. Safe to call only when not mounted —
// changing the overwrite root mid-session would mean Deactivate's capture
// step reads a different folder than Activate intended.
func (m *MountManager) SetOverwriteRoot(root string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.overwriteRoot = root
}

// Activate replaces the game's Data/ with a materialized hardlink farm of
// the merged layer view. Layer 0 must be "__base__"; its RootPath is rewritten
// to the backup directory after the rename so the materializer reads from
// the (renamed) original Data tree.
func (m *MountManager) Activate(layers []Layer) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.mounted {
		return ErrAlreadyMounted
	}

	dataPath := m.gameDataPath
	backupPath := dataPath + m.backupSuffix

	// Pre-flight: refuse if a prior session left a stale FUSE mount or
	// half-renamed state. Surfaces the user toward `gorganizerctl recover`
	// rather than producing a confusing rename error.
	if mount, err := DetectFuseMount(dataPath); err == nil && mount != nil {
		return fmt.Errorf("vfs: refusing to activate over a live FUSE mount at %s (run `gorganizerctl recover --game ...`)", dataPath)
	}
	if _, err := os.Stat(dataPath); os.IsNotExist(err) {
		return fmt.Errorf("%w: %s", ErrDataDirMissing, dataPath)
	}
	if _, err := os.Stat(backupPath); err == nil {
		return fmt.Errorf("%w: %s", ErrBackupExists, backupPath)
	}

	// 1. Rename Data → Data.orig (atomic on same filesystem).
	slog.Info("renaming data directory", "from", dataPath, "to", backupPath)
	if err := os.Rename(dataPath, backupPath); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", dataPath, backupPath, err)
	}

	// 2. Rewrite layer 0's RootPath to point at the backup we just created.
	if len(layers) > 0 && layers[0].Name == "__base__" {
		layers[0].RootPath = backupPath
	}

	// 3. Build the merged tree from all layers.
	tree := NewMergedTree()
	if err := tree.Build(layers); err != nil {
		// Best-effort rollback so the user isn't stuck without a Data dir.
		_ = os.Rename(backupPath, dataPath)
		return fmt.Errorf("building merged tree: %w", err)
	}

	// 4. Discover the overwrite mod's name (case-insensitive match by path).
	overwriteName := m.deriveOverwriteName(layers)

	// 5. Materialize directly into Data/ (cross-fs not possible here:
	//    we just renamed Data → Data.orig on the same filesystem). Phase
	//    4 will add the precompute-cache + cross-fs staging path; for
	//    now the simple "build it in place" path keeps the rewrite small.
	stats, err := BuildInto(dataPath, tree, layers, overwriteName)
	if err != nil {
		// Materialization failed mid-build: tear down the partial Data/
		// and restore the backup so the user isn't stuck.
		_ = os.RemoveAll(dataPath)
		_ = os.Rename(backupPath, dataPath)
		return fmt.Errorf("materializing overlay: %w", err)
	}

	// 6. Write the sentinel — this is the marker recovery uses to prove
	//    "this Data/ is gorganizer-owned".
	sentinel := &Sentinel{
		SchemaVersion:       CurrentSentinelSchema,
		Magic:               SentinelMagic,
		ProfileName:         "", // daemon may set this via a future setter
		ActivationPID:       os.Getpid(),
		ActivationStartedAt: time.Now().UTC(),
		BackupPath:          backupPath,
		OverwriteMod:        overwriteName,
		Layers:              layersForSentinel(layers),
		MaterializerVersion: CurrentMaterializerVersion,
	}
	// GameID is set by the caller via the in-memory state map; we pass
	// through the layer-derived name only for forensic logging. Daemon
	// writes a richer sentinel via SetSentinelGameID-style calls if added.
	if err := WriteSentinel(dataPath, sentinel); err != nil {
		// The materialized tree is fine; only the metadata write failed.
		// Roll back fully so recovery doesn't see a Data without a
		// sentinel later (which would surface as recovery-pending).
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

// Deactivate validates the sentinel, captures any new files (writes that
// went through atomic-save and broke the materialized hardlink) into the
// overwrite mod, then tears the Data/ tree down and restores Data.orig.
func (m *MountManager) Deactivate() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.mounted {
		return ErrNotMounted
	}

	dataPath := m.gameDataPath
	backupPath := dataPath + m.backupSuffix

	// 1. Validate sentinel before destroying anything. A missing or bad
	//    sentinel means something other than us owns Data/ now — refuse
	//    rather than rm -rf the user's data.
	s, err := ReadSentinel(dataPath)
	if err != nil {
		return fmt.Errorf("validating overlay before tear-down: %w", err)
	}
	if vErr := ValidateSentinel(s); vErr != nil {
		return fmt.Errorf("sentinel rejected: %w", vErr)
	}

	// 2. Capture any new-file writes into the overwrite mod (no-op when
	//    overwriteRoot is empty).
	if m.overwriteRoot != "" {
		moved, capErr := CaptureNewFiles(dataPath, m.overwriteRoot)
		if capErr != nil {
			slog.Warn("write capture failed — proceeding with teardown",
				"path", dataPath, "err", capErr)
		} else if moved > 0 {
			slog.Info("captured tool/game writes into overwrite mod",
				"count", moved, "overwrite_root", m.overwriteRoot)
		}
	}

	// 3. rm -rf the materialized tree. RemoveAll uses lstat+unlink and
	//    never follows symlinks for deletion, so cross-fs symlinks point at
	//    real mods and don't get cascaded into.
	slog.Info("tearing down materialized overlay", "path", dataPath)
	if err := os.RemoveAll(dataPath); err != nil {
		return fmt.Errorf("removing materialized %s: %w", dataPath, err)
	}

	// 4. Restore the original Data/.
	if err := os.Rename(backupPath, dataPath); err != nil {
		return fmt.Errorf("restoring %s from %s: %w", dataPath, backupPath, err)
	}

	m.tree = nil
	m.mounted = false

	slog.Info("VFS deactivated and data directory restored", "path", dataPath)
	return nil
}

// RecoverIfNeeded handles the daemon-startup recovery path. Delegates to
// CleanupStale, which knows about both the legacy stale-FUSE-mount case
// (kept compatible with the old in-process FUSE backend) and the new
// sentinel-based crash recovery.
//
// Returns the RecoveryOutcome so the caller (daemon) can surface a
// recovery-pending event to the GUI when the on-disk state is ambiguous
// and CleanupStale refused to act.
func (m *MountManager) RecoverIfNeeded() (RecoveryOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	outcome, err := CleanupStale(m.gameDataPath)
	if err != nil {
		return outcome, fmt.Errorf("recovering %s: %w", m.gameDataPath, err)
	}
	return outcome, nil
}

// DataPath returns the configured Data directory path. Callers that need
// to operate on the path directly (e.g. RestoreFromBackup invoked via an
// IPC handler outside the manager) use this rather than reaching into the
// struct.
func (m *MountManager) DataPath() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.gameDataPath
}

// IsMounted returns the current mount state.
func (m *MountManager) IsMounted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mounted
}

// Tree returns the current MergedTree (nil if not mounted).
func (m *MountManager) Tree() *MergedTree {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tree
}

// WaitMount is preserved for API compatibility with the previous FUSE-
// backed implementation. It used to block on the FUSE server's exit; the
// new materialized overlay has no server to wait on, so this is a no-op.
// Tests that called it as a synchronization point should use IsMounted +
// channels instead going forward.
func (m *MountManager) WaitMount() {}

// deriveOverwriteName looks up the configured overwriteRoot in the layer
// list and returns the matching layer's Name (case-sensitive). When the
// overwriteRoot isn't a layer (legitimate when no escape hatch is
// configured), returns "" and the materializer treats every file as
// read-only.
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

// layersForSentinel projects []Layer down to the minimum-needed
// SentinelLayer slice for forensic recording.
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

// Compile-time guard so we notice if errors.Is shape changes.
var _ = errors.Is

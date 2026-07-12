package vfs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/parka/gorganizer/internal/atomicfile"
)

const RootContentDirName = ".gorganizer-root"

const (
	RootManifestFilename = ".gorganizer-root-manifest.json"
	RootIntentFilename   = ".gorganizer-root-intent.json"
	RootBackupDirName    = ".gorganizer-root-backups"

	rootManifestMagic  = "gorganizer-root-deployment"
	rootIntentMagic    = "gorganizer-root-intent"
	rootCurrentSchema  = 1
	rootLinkTempPrefix = ".gorganizer-root-link-"
)

var (
	ErrRootRecoveryPending   = errors.New("vfs: game-root deployment recovery pending")
	ErrRootCasefoldCollision = errors.New("vfs: game-root case-fold collision")
	ErrRootPathConflict      = errors.New("vfs: game-root path conflict")
)

type RootDeploymentConfig struct {
	GameRoot       string
	GameID         string
	ProtectedPaths []string
}

type RootDeploymentManager struct {
	gameRoot       string
	gameID         string
	protectedPaths []string
	mu             sync.Mutex
}

type RootPlanEntry struct {
	RelativePath string
	SourcePath   string
	SourceRoot   string
	LayerName    string
}

type RootDeploymentPlan struct {
	Entries []RootPlanEntry
}

type RootBackup struct {
	Kind       string `json:"kind"`
	SHA256     string `json:"sha256,omitempty"`
	Size       int64  `json:"size,omitempty"`
	Mode       uint32 `json:"mode,omitempty"`
	LinkTarget string `json:"link_target,omitempty"`
}

type RootManifestEntry struct {
	RelativePath string      `json:"relative_path"`
	SourcePath   string      `json:"source_path"`
	SourceRoot   string      `json:"source_root"`
	LayerName    string      `json:"layer_name"`
	Backup       *RootBackup `json:"backup,omitempty"`
}

type RootManifest struct {
	SchemaVersion int                 `json:"schema_version"`
	Magic         string              `json:"magic"`
	GameID        string              `json:"game_id"`
	GameRoot      string              `json:"game_root"`
	ProfileName   string              `json:"profile_name"`
	AppliedAt     time.Time           `json:"applied_at"`
	Entries       []RootManifestEntry `json:"entries"`
	CreatedDirs   []string            `json:"created_dirs,omitempty"`
}

type rootIntent struct {
	SchemaVersion int           `json:"schema_version"`
	Magic         string        `json:"magic"`
	GameID        string        `json:"game_id"`
	GameRoot      string        `json:"game_root"`
	PID           int           `json:"pid"`
	Previous      *RootManifest `json:"previous,omitempty"`
	Desired       *RootManifest `json:"desired,omitempty"`
}

type RootDeployStats struct {
	LinksCreated    int
	LinksRemoved    int
	BackupsCreated  int
	BackupsRestored int
	DirsCreated     int
	DirsRemoved     int
}

type RootRecoveryPending struct {
	Path   string
	Reason string
}

type RootRecoveryOutcome struct {
	Recovered bool
	Pending   *RootRecoveryPending
}

type RootDriftError struct {
	Path   string
	Reason string
}

func (e *RootDriftError) Error() string {
	return fmt.Sprintf("%v at %q: %s", ErrRootRecoveryPending, e.Path, e.Reason)
}

func (e *RootDriftError) Is(target error) bool { return target == ErrRootRecoveryPending }

func NewRootDeploymentManager(cfg RootDeploymentConfig) (*RootDeploymentManager, error) {
	if cfg.GameRoot == "" {
		return nil, errors.New("vfs: root deployment: empty game root")
	}
	if cfg.GameID == "" {
		return nil, errors.New("vfs: root deployment: empty game ID")
	}
	abs, err := filepath.Abs(cfg.GameRoot)
	if err != nil {
		return nil, fmt.Errorf("vfs: root deployment: resolving game root: %w", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("vfs: root deployment: resolving game root symlinks: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return nil, fmt.Errorf("vfs: root deployment: stat game root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("vfs: root deployment: game root %q is not a directory", real)
	}

	protected := make([]string, 0, len(cfg.ProtectedPaths)+3)
	for _, p := range cfg.ProtectedPaths {
		norm, err := validRootRelativePath(p)
		if err != nil {
			return nil, fmt.Errorf("vfs: root deployment: protected path %q: %w", p, err)
		}
		protected = append(protected, norm)
	}
	protected = append(protected,
		NormalizePath(RootManifestFilename),
		NormalizePath(RootIntentFilename),
		NormalizePath(RootBackupDirName),
	)
	return &RootDeploymentManager{gameRoot: real, gameID: cfg.GameID, protectedPaths: protected}, nil
}

func (m *RootDeploymentManager) GameRoot() string { return m.gameRoot }

// BuildRootDeploymentPlan applies layer priority to reserved root content.
func BuildRootDeploymentPlan(layers []Layer) (*RootDeploymentPlan, error) {
	winners := make(map[string]RootPlanEntry)
	for _, layer := range layers {
		if !layer.Enabled || layer.Name == "__base__" {
			continue
		}
		layerAbs, err := filepath.Abs(layer.RootPath)
		if err != nil {
			return nil, fmt.Errorf("root layer %q: resolving root: %w", layer.Name, err)
		}
		layerReal, err := filepath.EvalSymlinks(layerAbs)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("root layer %q: resolving root symlinks: %w", layer.Name, err)
		}
		rootEntries, err := os.ReadDir(layerAbs)
		if err != nil {
			return nil, fmt.Errorf("root layer %q: reading root: %w", layer.Name, err)
		}
		var contentNames []string
		for _, entry := range rootEntries {
			if NormalizeName(entry.Name()) == RootContentDirName {
				contentNames = append(contentNames, entry.Name())
			}
		}
		if len(contentNames) == 0 {
			continue
		}
		if len(contentNames) > 1 {
			return nil, fmt.Errorf("%w in layer %q: multiple %s directories", ErrRootCasefoldCollision, layer.Name, RootContentDirName)
		}
		contentRoot := filepath.Join(layerAbs, contentNames[0])
		contentInfo, err := os.Lstat(contentRoot)
		if err != nil {
			return nil, fmt.Errorf("root layer %q: stat %s: %w", layer.Name, RootContentDirName, err)
		}
		if contentInfo.Mode()&os.ModeSymlink != 0 || !contentInfo.IsDir() {
			return nil, fmt.Errorf("root layer %q: %s must be a physical directory", layer.Name, RootContentDirName)
		}

		seen := make(map[string]string)
		err = filepath.WalkDir(contentRoot, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path == contentRoot || d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(contentRoot, path)
			if err != nil {
				return err
			}
			norm, err := validRootRelativePath(filepath.ToSlash(rel))
			if err != nil {
				return err
			}
			if prior, ok := seen[norm]; ok {
				return fmt.Errorf("%w in layer %q: %q and %q", ErrRootCasefoldCollision, layer.Name, prior, filepath.ToSlash(rel))
			}
			seen[norm] = filepath.ToSlash(rel)

			sourceAbs, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			resolved, err := filepath.EvalSymlinks(sourceAbs)
			if err != nil {
				return fmt.Errorf("root source %q is dangling or inaccessible: %w", sourceAbs, err)
			}
			if !pathWithin(layerReal, resolved) {
				return fmt.Errorf("root source %q resolves outside layer %q", sourceAbs, layer.RootPath)
			}
			info, err := os.Stat(resolved)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("root source %q is not a regular file", sourceAbs)
			}

			winners[norm] = RootPlanEntry{
				RelativePath: filepath.ToSlash(rel),
				SourcePath:   resolved,
				SourceRoot:   layerReal,
				LayerName:    layer.Name,
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walking root layer %q: %w", layer.Name, err)
		}
	}

	norms := make([]string, 0, len(winners))
	for norm := range winners {
		norms = append(norms, norm)
	}
	sort.Strings(norms)
	for i, norm := range norms {
		for j := i + 1; j < len(norms) && strings.HasPrefix(norms[j], norm+"/"); j++ {
			return nil, fmt.Errorf("%w: %q is both a file and an ancestor of %q", ErrRootPathConflict, winners[norm].RelativePath, winners[norms[j]].RelativePath)
		}
	}
	plan := &RootDeploymentPlan{Entries: make([]RootPlanEntry, 0, len(norms))}
	for _, norm := range norms {
		plan.Entries = append(plan.Entries, winners[norm])
	}
	return plan, nil
}

// Apply deploys winning root files while preserving earlier backups.
func (m *RootDeploymentManager) Apply(layers []Layer, profileName string) (RootDeployStats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var stats RootDeployStats
	if outcome, err := m.recoverLocked(&stats); err != nil {
		return stats, err
	} else if outcome.Pending != nil {
		return stats, &RootDriftError{Path: outcome.Pending.Path, Reason: outcome.Pending.Reason}
	}

	plan, err := BuildRootDeploymentPlan(layers)
	if err != nil {
		return stats, err
	}
	previous, err := m.readManifest()
	if err != nil {
		return stats, err
	}
	if err := m.validateActive(previous); err != nil {
		return stats, err
	}
	desired, err := m.prepareManifest(plan, previous, profileName)
	if err != nil {
		return stats, err
	}
	if err := m.commit(previous, desired, &stats); err != nil {
		return stats, err
	}
	return stats, nil
}

// Deactivate removes managed root links and restores verified backups.
func (m *RootDeploymentManager) Deactivate() (RootDeployStats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var stats RootDeployStats
	if outcome, err := m.recoverLocked(&stats); err != nil {
		return stats, err
	} else if outcome.Pending != nil {
		return stats, &RootDriftError{Path: outcome.Pending.Path, Reason: outcome.Pending.Reason}
	}
	previous, err := m.readManifest()
	if err != nil {
		return stats, err
	}
	if previous == nil {
		return stats, nil
	}
	if err := m.validateActive(previous); err != nil {
		return stats, err
	}
	if err := m.commit(previous, nil, &stats); err != nil {
		return stats, err
	}
	return stats, nil
}

// Recover finishes an interrupted transaction or reports ambiguous drift.
func (m *RootDeploymentManager) Recover() (RootRecoveryOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recoverLocked(&RootDeployStats{})
}

func (m *RootDeploymentManager) ActiveManifest() (*RootManifest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.readManifest()
}

func (m *RootDeploymentManager) recoverLocked(stats *RootDeployStats) (RootRecoveryOutcome, error) {
	intent, err := m.readIntent()
	if err != nil {
		var drift *RootDriftError
		if errors.As(err, &drift) {
			return RootRecoveryOutcome{Pending: &RootRecoveryPending{Path: drift.Path, Reason: drift.Reason}}, nil
		}
		return RootRecoveryOutcome{}, err
	}
	if intent != nil {
		if err := m.reconcile(intent.Previous, intent.Desired, stats); err != nil {
			var drift *RootDriftError
			if errors.As(err, &drift) {
				return RootRecoveryOutcome{Pending: &RootRecoveryPending{Path: drift.Path, Reason: drift.Reason}}, nil
			}
			return RootRecoveryOutcome{}, err
		}
		if err := m.writeCommittedManifest(intent.Desired); err != nil {
			return RootRecoveryOutcome{}, err
		}
		if err := m.removeIntent(); err != nil {
			return RootRecoveryOutcome{}, err
		}
		return RootRecoveryOutcome{Recovered: true}, nil
	}

	manifest, err := m.readManifest()
	if err != nil {
		var drift *RootDriftError
		if errors.As(err, &drift) {
			return RootRecoveryOutcome{Pending: &RootRecoveryPending{Path: drift.Path, Reason: drift.Reason}}, nil
		}
		return RootRecoveryOutcome{}, err
	}
	if manifest != nil {
		if err := m.validateActive(manifest); err != nil {
			var drift *RootDriftError
			if errors.As(err, &drift) {
				return RootRecoveryOutcome{Pending: &RootRecoveryPending{Path: drift.Path, Reason: drift.Reason}}, nil
			}
			return RootRecoveryOutcome{}, err
		}
		return RootRecoveryOutcome{}, nil
	}
	if nonempty, err := dirNonempty(m.backupRoot()); err != nil {
		return RootRecoveryOutcome{}, err
	} else if nonempty {
		return RootRecoveryOutcome{Pending: &RootRecoveryPending{
			Path: m.backupRoot(), Reason: "backup tree exists without a deployment manifest or transaction intent",
		}}, nil
	}
	return RootRecoveryOutcome{}, nil
}

func (m *RootDeploymentManager) commit(previous, desired *RootManifest, stats *RootDeployStats) error {
	intent := &rootIntent{
		SchemaVersion: rootCurrentSchema, Magic: rootIntentMagic,
		GameID: m.gameID, GameRoot: m.gameRoot, PID: os.Getpid(),
		Previous: previous, Desired: desired,
	}
	if err := m.writeIntent(intent); err != nil {
		return err
	}
	if err := m.reconcile(previous, desired, stats); err != nil {
		return err
	}
	if err := m.writeCommittedManifest(desired); err != nil {
		return err
	}
	return m.removeIntent()
}

func (m *RootDeploymentManager) prepareManifest(plan *RootDeploymentPlan, previous *RootManifest, profileName string) (*RootManifest, error) {
	if plan == nil || len(plan.Entries) == 0 {
		return nil, nil
	}
	prevByNorm := manifestEntries(previous)
	for oldNorm, old := range prevByNorm {
		for _, p := range plan.Entries {
			newNorm := NormalizePath(p.RelativePath)
			if oldNorm != newNorm && (strings.HasPrefix(oldNorm, newNorm+"/") || strings.HasPrefix(newNorm, oldNorm+"/")) {
				return nil, fmt.Errorf("%w: cannot transition between file paths %q and %q in one apply", ErrRootPathConflict, old.RelativePath, p.RelativePath)
			}
		}
	}

	desired := &RootManifest{
		SchemaVersion: rootCurrentSchema, Magic: rootManifestMagic,
		GameID: m.gameID, GameRoot: m.gameRoot, ProfileName: profileName,
		AppliedAt: time.Now().UTC(),
	}
	plannedDirs := make(map[string]string)
	created := make(map[string]string)
	if previous != nil {
		for _, d := range previous.CreatedDirs {
			plannedDirs[NormalizePath(d)] = d
		}
	}
	for _, p := range plan.Entries {
		norm := NormalizePath(p.RelativePath)
		if m.isProtected(norm) {
			return nil, fmt.Errorf("%w: root content targets protected path %q", ErrRootPathConflict, p.RelativePath)
		}
		if err := validateRootSource(p.SourcePath, p.SourceRoot); err != nil {
			return nil, err
		}
		if old, ok := prevByNorm[norm]; ok {
			desired.Entries = append(desired.Entries, RootManifestEntry{
				RelativePath: old.RelativePath, SourcePath: p.SourcePath,
				SourceRoot: p.SourceRoot, LayerName: p.LayerName, Backup: old.Backup,
			})
			continue
		}
		actualRel, missing, err := m.resolveDestination(p.RelativePath, plannedDirs)
		if err != nil {
			return nil, err
		}
		for _, d := range missing {
			created[NormalizePath(d)] = d
			plannedDirs[NormalizePath(d)] = d
		}
		dest := filepath.Join(m.gameRoot, filepath.FromSlash(actualRel))
		var backup *RootBackup
		if _, err := os.Lstat(dest); err == nil {
			backup, err = describeBackup(dest)
			if err != nil {
				return nil, fmt.Errorf("describing collision at %q: %w", dest, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("stat destination %q: %w", dest, err)
		}
		desired.Entries = append(desired.Entries, RootManifestEntry{
			RelativePath: actualRel, SourcePath: p.SourcePath,
			SourceRoot: p.SourceRoot, LayerName: p.LayerName, Backup: backup,
		})
	}
	if previous != nil {
		for _, d := range previous.CreatedDirs {
			if directoryNeeded(d, desired.Entries) {
				created[NormalizePath(d)] = d
			}
		}
	}
	for _, d := range created {
		desired.CreatedDirs = append(desired.CreatedDirs, d)
	}
	sort.Slice(desired.CreatedDirs, func(i, j int) bool {
		di, dj := pathDepth(desired.CreatedDirs[i]), pathDepth(desired.CreatedDirs[j])
		if di != dj {
			return di < dj
		}
		return NormalizePath(desired.CreatedDirs[i]) < NormalizePath(desired.CreatedDirs[j])
	})
	sort.Slice(desired.Entries, func(i, j int) bool {
		return NormalizePath(desired.Entries[i].RelativePath) < NormalizePath(desired.Entries[j].RelativePath)
	})
	return desired, nil
}

func (m *RootDeploymentManager) reconcile(previous, desired *RootManifest, stats *RootDeployStats) error {
	prev := manifestEntries(previous)
	want := manifestEntries(desired)

	for norm, old := range prev {
		if _, keep := want[norm]; keep {
			continue
		}
		if err := m.removeAndRestore(old, stats); err != nil {
			return err
		}
	}

	if desired != nil {
		for _, d := range desired.CreatedDirs {
			created, err := ensurePhysicalDirectoryPath(m.gameRoot, d, true)
			if err != nil {
				return err
			}
			stats.DirsCreated += created
		}

		for norm, entry := range want {
			old, existed := prev[norm]
			if err := m.installEntry(entry, old, existed, stats); err != nil {
				return err
			}
		}
	}

	wantedDirs := manifestDirs(desired)
	if previous != nil {
		dirs := append([]string(nil), previous.CreatedDirs...)
		sort.Slice(dirs, func(i, j int) bool { return pathDepth(dirs[i]) > pathDepth(dirs[j]) })
		for _, d := range dirs {
			if _, keep := wantedDirs[NormalizePath(d)]; keep {
				continue
			}
			if err := os.Remove(filepath.Join(m.gameRoot, filepath.FromSlash(d))); err == nil {
				stats.DirsRemoved++
			} else if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, fs.ErrInvalid) {
				if !isDirectoryNotEmpty(err) {
					return fmt.Errorf("removing root deployment directory %q: %w", d, err)
				}
			}
		}
	}
	if desired != nil {
		if err := m.validateBackupTree(desired); err != nil {
			return err
		}
	} else {
		if nonempty, err := dirNonempty(m.backupRoot()); err != nil {
			return err
		} else if nonempty {
			return drift(m.backupRoot(), "backups remain after all managed paths were restored")
		}
		_ = os.RemoveAll(m.backupRoot())
	}
	return nil
}

func (m *RootDeploymentManager) installEntry(entry, old RootManifestEntry, existed bool, stats *RootDeployStats) error {
	if err := validateRootSource(entry.SourcePath, entry.SourceRoot); err != nil {
		return err
	}
	if err := m.ensureDestinationParent(entry.RelativePath); err != nil {
		return err
	}
	dest := filepath.Join(m.gameRoot, filepath.FromSlash(entry.RelativePath))
	backupPath := m.backupPath(entry.RelativePath)

	if existed {
		matchesNew, err := symlinkMatches(dest, entry.SourcePath)
		if err != nil {
			return err
		}
		if matchesNew {
			return m.verifyExpectedBackup(entry, backupPath)
		}
		matchesOld, err := symlinkMatches(dest, old.SourcePath)
		if err != nil {
			return err
		}
		if !matchesOld {
			return drift(dest, "managed symlink was changed or replaced")
		}
		if err := m.verifyExpectedBackup(entry, backupPath); err != nil {
			return err
		}
		if err := atomicSymlink(entry.SourcePath, dest); err != nil {
			return fmt.Errorf("replacing root link %q: %w", dest, err)
		}
		stats.LinksCreated++
		return nil
	}

	backupExists := false
	if _, err := os.Lstat(backupPath); err == nil {
		backupExists = true
		if entry.Backup == nil {
			return drift(backupPath, "unexpected backup exists for a path that had no original file")
		}
		if err := verifyBackup(backupPath, entry.Backup); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	matchesNew, err := symlinkMatches(dest, entry.SourcePath)
	if err != nil {
		return err
	}
	if matchesNew {
		if entry.Backup != nil && !backupExists {
			return drift(backupPath, "original file backup is missing")
		}
		return nil
	}

	if entry.Backup != nil {
		if backupExists {
			if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
				if err != nil {
					return err
				}
				return drift(dest, "unexpected file appeared after the original was backed up")
			}
		} else {
			if _, err := os.Lstat(dest); errors.Is(err, os.ErrNotExist) {
				return drift(dest, "original file and its planned backup are both missing")
			} else if err != nil {
				return err
			}
			if err := verifyBackup(dest, entry.Backup); err != nil {
				return err
			}
			if err := m.ensureBackupParent(entry.RelativePath); err != nil {
				return err
			}
			if err := os.Rename(dest, backupPath); err != nil {
				return fmt.Errorf("backing up root collision %q: %w", dest, err)
			}
			stats.BackupsCreated++
		}
	} else if _, err := os.Lstat(dest); err == nil {
		return drift(dest, "unexpected file occupies a path planned as initially empty")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := atomicSymlink(entry.SourcePath, dest); err != nil {
		return fmt.Errorf("creating root link %q: %w", dest, err)
	}
	stats.LinksCreated++
	return nil
}

func (m *RootDeploymentManager) removeAndRestore(entry RootManifestEntry, stats *RootDeployStats) error {
	if err := m.ensureDestinationParent(entry.RelativePath); err != nil {
		return err
	}
	dest := filepath.Join(m.gameRoot, filepath.FromSlash(entry.RelativePath))
	backupPath := m.backupPath(entry.RelativePath)
	matches, err := symlinkMatches(dest, entry.SourcePath)
	if err != nil {
		return err
	}
	if matches {
		if err := os.Remove(dest); err != nil {
			return err
		}
		stats.LinksRemoved++
	} else if _, err := os.Lstat(dest); err == nil {
		if entry.Backup != nil {
			if verifyBackup(dest, entry.Backup) == nil {
				if _, bErr := os.Lstat(backupPath); errors.Is(bErr, os.ErrNotExist) {
					return nil
				}
			}
		}
		return drift(dest, "managed symlink was changed or replaced; refusing to remove it")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if entry.Backup == nil {
		return nil
	}
	if err := m.ensureBackupParentExisting(entry.RelativePath); err != nil {
		return err
	}
	if _, err := os.Lstat(backupPath); errors.Is(err, os.ErrNotExist) {
		if _, destErr := os.Lstat(dest); destErr == nil && verifyBackup(dest, entry.Backup) == nil {
			return nil
		}
		return drift(backupPath, "original file backup is missing")
	} else if err != nil {
		return err
	}
	if err := verifyBackup(backupPath, entry.Backup); err != nil {
		return err
	}
	if _, err := os.Lstat(dest); err == nil {
		return drift(dest, "cannot restore backup over an unexpected file")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(backupPath, dest); err != nil {
		return fmt.Errorf("restoring root backup %q: %w", dest, err)
	}
	stats.BackupsRestored++
	removeEmptyParents(filepath.Dir(backupPath), m.backupRoot())
	return nil
}

func (m *RootDeploymentManager) validateActive(manifest *RootManifest) error {
	if manifest == nil {
		return nil
	}
	if err := m.validateManifest(manifest); err != nil {
		return err
	}
	for _, entry := range manifest.Entries {
		if err := m.ensureDestinationParent(entry.RelativePath); err != nil {
			return err
		}
		dest := filepath.Join(m.gameRoot, filepath.FromSlash(entry.RelativePath))
		matches, err := symlinkMatches(dest, entry.SourcePath)
		if err != nil {
			return err
		}
		if !matches {
			return drift(dest, "active manifest does not match the deployed symlink")
		}
		if err := m.verifyExpectedBackup(entry, m.backupPath(entry.RelativePath)); err != nil {
			return err
		}
	}
	return m.validateBackupTree(manifest)
}

func (m *RootDeploymentManager) verifyExpectedBackup(entry RootManifestEntry, backupPath string) error {
	if entry.Backup == nil {
		if _, err := os.Lstat(backupPath); err == nil {
			return drift(backupPath, "unexpected backup for an originally empty path")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := m.ensureBackupParentExisting(entry.RelativePath); err != nil {
		return err
	}
	if _, err := os.Lstat(backupPath); errors.Is(err, os.ErrNotExist) {
		return drift(backupPath, "original file backup is missing")
	} else if err != nil {
		return err
	}
	return verifyBackup(backupPath, entry.Backup)
}

func (m *RootDeploymentManager) resolveDestination(requested string, plannedDirs map[string]string) (string, []string, error) {
	components := strings.Split(filepath.ToSlash(requested), "/")
	current := m.gameRoot
	actual := make([]string, 0, len(components))
	missing := make([]string, 0)
	for i, wanted := range components {
		isLeaf := i == len(components)-1
		parentNorm := NormalizePath(strings.Join(append(actual, wanted), "/"))
		if !isLeaf {
			if planned, ok := plannedDirs[parentNorm]; ok {
				actual = strings.Split(filepath.ToSlash(planned), "/")
				current = filepath.Join(m.gameRoot, filepath.FromSlash(planned))
				continue
			}
		}
		entries, err := os.ReadDir(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				actual = append(actual, wanted)
				if !isLeaf {
					rel := strings.Join(actual, "/")
					missing = append(missing, rel)
					plannedDirs[NormalizePath(rel)] = rel
				}
				current = filepath.Join(current, wanted)
				continue
			}
			return "", nil, err
		}
		var matches []fs.DirEntry
		for _, candidate := range entries {
			if NormalizeName(candidate.Name()) == NormalizeName(wanted) {
				matches = append(matches, candidate)
			}
		}
		if len(matches) > 1 {
			return "", nil, fmt.Errorf("%w under %q for component %q", ErrRootCasefoldCollision, current, wanted)
		}
		if len(matches) == 0 {
			actual = append(actual, wanted)
			if !isLeaf {
				rel := strings.Join(actual, "/")
				missing = append(missing, rel)
				plannedDirs[NormalizePath(rel)] = rel
			}
			current = filepath.Join(current, wanted)
			continue
		}
		match := matches[0]
		actual = append(actual, match.Name())
		current = filepath.Join(current, match.Name())
		if !isLeaf {
			info, err := match.Info()
			if err != nil {
				return "", nil, err
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return "", nil, fmt.Errorf("%w: parent %q is not a physical directory", ErrRootPathConflict, current)
			}
			plannedDirs[NormalizePath(strings.Join(actual, "/"))] = strings.Join(actual, "/")
		}
	}
	return strings.Join(actual, "/"), missing, nil
}

func (m *RootDeploymentManager) isProtected(norm string) bool {
	for _, p := range m.protectedPaths {
		if norm == p || strings.HasPrefix(norm, p+"/") || strings.HasPrefix(p, norm+"/") {
			return true
		}
	}
	return false
}

func (m *RootDeploymentManager) isInsideProtected(norm string) bool {
	for _, p := range m.protectedPaths {
		if norm == p || strings.HasPrefix(norm, p+"/") {
			return true
		}
	}
	return false
}

func (m *RootDeploymentManager) manifestPath() string {
	return filepath.Join(m.gameRoot, RootManifestFilename)
}
func (m *RootDeploymentManager) intentPath() string {
	return filepath.Join(m.gameRoot, RootIntentFilename)
}
func (m *RootDeploymentManager) backupRoot() string {
	return filepath.Join(m.gameRoot, RootBackupDirName)
}
func (m *RootDeploymentManager) backupPath(rel string) string {
	return filepath.Join(m.backupRoot(), filepath.FromSlash(rel))
}

func (m *RootDeploymentManager) ensureDestinationParent(rel string) error {
	parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(rel)))
	if parent == "." {
		return nil
	}
	_, err := ensurePhysicalDirectoryPath(m.gameRoot, parent, false)
	return err
}

func (m *RootDeploymentManager) ensureBackupParent(rel string) error {
	parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(rel)))
	backupParent := RootBackupDirName
	if parent != "." {
		backupParent += "/" + parent
	}
	_, err := ensurePhysicalDirectoryPath(m.gameRoot, backupParent, true)
	if err == nil {
		err = os.Chmod(m.backupRoot(), 0700)
	}
	return err
}

func (m *RootDeploymentManager) ensureBackupParentExisting(rel string) error {
	parent := filepath.ToSlash(filepath.Dir(filepath.FromSlash(rel)))
	backupParent := RootBackupDirName
	if parent != "." {
		backupParent += "/" + parent
	}
	_, err := ensurePhysicalDirectoryPath(m.gameRoot, backupParent, false)
	return err
}

func (m *RootDeploymentManager) validateBackupTree(manifest *RootManifest) error {
	expected := make(map[string]struct{})
	if manifest != nil {
		for _, entry := range manifest.Entries {
			if entry.Backup != nil {
				expected[NormalizePath(entry.RelativePath)] = struct{}{}
			}
		}
	}
	if _, err := os.Lstat(m.backupRoot()); errors.Is(err, os.ErrNotExist) {
		if len(expected) == 0 {
			return nil
		}
		return drift(m.backupRoot(), "backup tree is missing")
	} else if err != nil {
		return err
	}
	if _, err := ensurePhysicalDirectoryPath(m.gameRoot, RootBackupDirName, false); err != nil {
		return err
	}
	found := make(map[string]struct{})
	err := filepath.WalkDir(m.backupRoot(), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == m.backupRoot() || entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(m.backupRoot(), path)
		if err != nil {
			return err
		}
		norm, err := validRootRelativePath(filepath.ToSlash(rel))
		if err != nil {
			return drift(path, "backup tree contains an unsafe path")
		}
		if _, ok := expected[norm]; !ok {
			return drift(path, "backup tree contains an untracked entry")
		}
		found[norm] = struct{}{}
		return nil
	})
	if err != nil {
		return err
	}
	for norm := range expected {
		if _, ok := found[norm]; !ok {
			return drift(m.backupRoot(), fmt.Sprintf("tracked backup %q is missing", norm))
		}
	}
	return nil
}

func (m *RootDeploymentManager) readManifest() (*RootManifest, error) {
	body, err := os.ReadFile(m.manifestPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var manifest RootManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, drift(m.manifestPath(), fmt.Sprintf("manifest is malformed: %v", err))
	}
	if err := m.validateManifest(&manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func (m *RootDeploymentManager) validateManifest(manifest *RootManifest) error {
	if manifest == nil {
		return nil
	}
	if manifest.Magic != rootManifestMagic || manifest.SchemaVersion != rootCurrentSchema || manifest.GameID != m.gameID || filepath.Clean(manifest.GameRoot) != m.gameRoot {
		return drift(m.manifestPath(), "manifest identity, magic, or schema does not match this installation")
	}
	seen := make(map[string]struct{})
	paths := make([]string, 0, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		norm, err := validRootRelativePath(entry.RelativePath)
		if err != nil {
			return drift(m.manifestPath(), "manifest contains an unsafe destination path")
		}
		if m.isProtected(norm) {
			return drift(m.manifestPath(), "manifest targets a protected manager or Data path")
		}
		if _, ok := seen[norm]; ok {
			return drift(m.manifestPath(), "manifest contains duplicate case-folded destinations")
		}
		seen[norm] = struct{}{}
		paths = append(paths, norm)
		if !filepath.IsAbs(entry.SourcePath) || !filepath.IsAbs(entry.SourceRoot) {
			return drift(m.manifestPath(), "manifest contains a non-absolute source path")
		}
		if !pathWithin(filepath.Clean(entry.SourceRoot), filepath.Clean(entry.SourcePath)) {
			return drift(m.manifestPath(), "manifest source path escapes its source root")
		}
		if entry.Backup != nil && entry.Backup.Kind != "file" && entry.Backup.Kind != "symlink" {
			return drift(m.manifestPath(), "manifest contains an unknown backup kind")
		}
	}
	sort.Strings(paths)
	for i, path := range paths {
		if i+1 < len(paths) && strings.HasPrefix(paths[i+1], path+"/") {
			return drift(m.manifestPath(), "manifest contains a file/ancestor destination conflict")
		}
	}
	dirSeen := make(map[string]struct{})
	for _, d := range manifest.CreatedDirs {
		norm, err := validRootRelativePath(d)
		if err != nil {
			return drift(m.manifestPath(), "manifest contains an unsafe created directory")
		}
		if m.isInsideProtected(norm) {
			return drift(m.manifestPath(), "manifest contains a protected created directory")
		}
		if _, exists := dirSeen[norm]; exists {
			return drift(m.manifestPath(), "manifest contains a duplicate created directory")
		}
		dirSeen[norm] = struct{}{}
		if !directoryNeeded(d, manifest.Entries) {
			return drift(m.manifestPath(), "manifest contains an unneeded created directory")
		}
	}
	return nil
}

func (m *RootDeploymentManager) writeCommittedManifest(manifest *RootManifest) error {
	if manifest == nil || len(manifest.Entries) == 0 {
		if err := os.Remove(m.manifestPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return syncDir(m.gameRoot)
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(m.manifestPath(), body, 0600)
}

func (m *RootDeploymentManager) readIntent() (*rootIntent, error) {
	body, err := os.ReadFile(m.intentPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var in rootIntent
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, drift(m.intentPath(), fmt.Sprintf("intent is malformed: %v", err))
	}
	if in.Magic != rootIntentMagic || in.SchemaVersion != rootCurrentSchema || in.GameID != m.gameID || filepath.Clean(in.GameRoot) != m.gameRoot {
		return nil, drift(m.intentPath(), "intent identity, magic, or schema does not match this installation")
	}
	if err := m.validateManifest(in.Previous); err != nil {
		return nil, err
	}
	if err := m.validateManifest(in.Desired); err != nil {
		return nil, err
	}
	return &in, nil
}

func (m *RootDeploymentManager) writeIntent(in *rootIntent) error {
	body, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(m.intentPath(), body, 0600)
}

func (m *RootDeploymentManager) removeIntent() error {
	if err := os.Remove(m.intentPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDir(m.gameRoot)
}

func manifestEntries(manifest *RootManifest) map[string]RootManifestEntry {
	out := make(map[string]RootManifestEntry)
	if manifest != nil {
		for _, e := range manifest.Entries {
			out[NormalizePath(e.RelativePath)] = e
		}
	}
	return out
}

func manifestDirs(manifest *RootManifest) map[string]struct{} {
	out := make(map[string]struct{})
	if manifest != nil {
		for _, d := range manifest.CreatedDirs {
			out[NormalizePath(d)] = struct{}{}
		}
	}
	return out
}

func validRootRelativePath(path string) (string, error) {
	path = strings.ReplaceAll(path, "\\", "/")
	if path == "" || strings.HasPrefix(path, "/") {
		return "", errors.New("path must be non-empty and relative")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != path {
		return "", errors.New("path is not clean or escapes the game root")
	}
	for _, part := range strings.Split(clean, "/") {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("path contains an unsafe component")
		}
	}
	return NormalizePath(clean), nil
}

func validateRootSource(source, sourceRoot string) error {
	if !filepath.IsAbs(source) || !filepath.IsAbs(sourceRoot) {
		return errors.New("vfs: root source paths must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return fmt.Errorf("vfs: root source %q is dangling or inaccessible: %w", source, err)
	}
	rootResolved, err := filepath.EvalSymlinks(sourceRoot)
	if err != nil {
		return fmt.Errorf("vfs: root source root %q is inaccessible: %w", sourceRoot, err)
	}
	if !pathWithin(rootResolved, resolved) {
		return fmt.Errorf("vfs: root source %q escapes source root %q", source, sourceRoot)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("vfs: root source %q is not a regular file", source)
	}
	return nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}

func describeBackup(path string) (*RootBackup, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return nil, err
		}
		return &RootBackup{Kind: "symlink", LinkTarget: target, Mode: uint32(info.Mode())}, nil
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("existing destination is neither a regular file nor a symlink")
	}
	digest, err := hashFile(path)
	if err != nil {
		return nil, err
	}
	return &RootBackup{Kind: "file", SHA256: digest, Size: info.Size(), Mode: uint32(info.Mode())}, nil
}

func verifyBackup(path string, want *RootBackup) error {
	if want == nil {
		return errors.New("vfs: nil backup description")
	}
	got, err := describeBackup(path)
	if err != nil {
		return drift(path, fmt.Sprintf("cannot verify original backup: %v", err))
	}
	if got.Kind != want.Kind || got.SHA256 != want.SHA256 || got.Size != want.Size || got.LinkTarget != want.LinkTarget || got.Mode != want.Mode {
		return drift(path, "original backup no longer matches its recorded fingerprint")
	}
	return nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func symlinkMatches(path, source string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return false, nil
	}
	target, err := os.Readlink(path)
	if err != nil {
		return false, err
	}
	return filepath.IsAbs(target) && filepath.Clean(target) == filepath.Clean(source), nil
}

func atomicSymlink(source, destination string) error {
	if !filepath.IsAbs(source) {
		return errors.New("root deployment symlink source must be absolute")
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), rootLinkTempPrefix)
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Remove(tmpPath); err != nil {
		return err
	}
	defer os.Remove(tmpPath)
	if err := os.Symlink(source, tmpPath); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, destination); err != nil {
		return err
	}
	return syncDir(filepath.Dir(destination))
}

func directoryNeeded(dir string, entries []RootManifestEntry) bool {
	norm := NormalizePath(dir)
	for _, e := range entries {
		if strings.HasPrefix(NormalizePath(e.RelativePath), norm+"/") {
			return true
		}
	}
	return false
}

// ensurePhysicalDirectoryPath walks directory components while rejecting symlinks.
func ensurePhysicalDirectoryPath(gameRoot, rel string, create bool) (int, error) {
	if _, err := validRootRelativePath(rel); err != nil {
		return 0, drift(rel, "unsafe directory path")
	}
	current := gameRoot
	created := 0
	for _, component := range strings.Split(filepath.ToSlash(rel), "/") {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if !create {
				return created, drift(current, "expected physical parent directory is missing")
			}
			if err := os.Mkdir(current, 0755); err != nil {
				return created, fmt.Errorf("creating physical directory %q: %w", current, err)
			}
			created++
		case err != nil:
			return created, err
		case !info.IsDir() || info.Mode()&os.ModeSymlink != 0:
			return created, drift(current, "expected a physical directory; symlinks and files are unsafe here")
		}
	}
	return created, nil
}

func pathDepth(path string) int { return strings.Count(filepath.ToSlash(path), "/") + 1 }

func removeEmptyParents(start, stop string) {
	start, stop = filepath.Clean(start), filepath.Clean(stop)
	for pathWithin(stop, start) && start != stop {
		if err := os.Remove(start); err != nil {
			return
		}
		start = filepath.Dir(start)
	}
}

func dirNonempty(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return len(entries) != 0, nil
}

func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func drift(path, reason string) *RootDriftError { return &RootDriftError{Path: path, Reason: reason} }

func isDirectoryNotEmpty(err error) bool {
	return errors.Is(err, fs.ErrExist) || strings.Contains(strings.ToLower(err.Error()), "directory not empty")
}

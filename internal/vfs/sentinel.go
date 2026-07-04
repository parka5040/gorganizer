package vfs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/parka/gorganizer/internal/atomicfile"
)

// SentinelFilename is the marker file written inside an active overlay.
const SentinelFilename = ".gorganizer-overlay.json"

// SentinelMagic identifies a gorganizer-owned overlay; rejected on mismatch.
const SentinelMagic = "gorganizer-overlay"

// CurrentSentinelSchema is the schema version this build reads and writes.
// v1: original (GameID/Hash declared but never populated).
// v2: GameID/ProfileName/OverwriteRoot populated; Hash is a layer-identity
//     fingerprint that ValidateSentinel recomputes and matches.
const CurrentSentinelSchema = 2

// CurrentMaterializerVersion bumps when the on-disk layout changes incompatibly.
const CurrentMaterializerVersion = 1

// Sibling markers (siblings of dataPath, so they survive the Data↔Data.orig
// renames): the activation/apply intent files and the transient build dirs a
// crash may leave behind. CleanupStale reaps these unconditionally.
const (
	activatingSuffix = ".gorganizer-activating"
	applyingSuffix   = ".gorganizer-applying"
	stagingSuffix    = ".gorganizer-staging"
	oldFarmSuffix    = ".gorganizer-oldfarm"
)

// IntentMagic identifies a gorganizer activation/apply intent marker.
const IntentMagic = "gorganizer-intent"

// CurrentIntentSchema is the intent-marker schema version.
const CurrentIntentSchema = 1

// IntentKind distinguishes an interrupted Activate from an interrupted Apply.
type IntentKind string

const (
	IntentActivating IntentKind = "activating"
	IntentApplying   IntentKind = "applying"
)

// ActivationIntent is a sibling marker written *before* a destructive rename so
// that a crash mid-operation is self-healing: CleanupStale finds it and rolls
// the operation forward or back automatically instead of prompting the user.
type ActivationIntent struct {
	SchemaVersion int        `json:"schema_version"`
	Magic         string     `json:"magic"`
	Kind          IntentKind `json:"kind"`
	GameID        string     `json:"game_id"`
	DataPath      string     `json:"data_path"`
	BackupPath    string     `json:"backup_path"`
	OverwriteRoot string     `json:"overwrite_root"`
	StagingPath   string     `json:"staging_path,omitempty"`
	PID           int        `json:"pid"`
}

func activatingIntentPath(dataPath string) string { return dataPath + activatingSuffix }
func applyingIntentPath(dataPath string) string   { return dataPath + applyingSuffix }
func stagingDirPath(dataPath string) string       { return dataPath + stagingSuffix }
func oldFarmPath(dataPath string) string          { return dataPath + oldFarmSuffix }

var ErrIntentMissing = errors.New("vfs: activation intent missing")

// WriteIntent atomically writes an intent marker.
func WriteIntent(markerPath string, in *ActivationIntent) error {
	if in == nil {
		return errors.New("vfs: WriteIntent: nil intent")
	}
	body, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling intent: %w", err)
	}
	return atomicfile.WriteFile(markerPath, body, 0644)
}

// ReadIntent loads an intent marker; returns ErrIntentMissing when absent.
func ReadIntent(markerPath string) (*ActivationIntent, error) {
	body, err := os.ReadFile(markerPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrIntentMissing
		}
		return nil, fmt.Errorf("reading intent %s: %w", markerPath, err)
	}
	var in ActivationIntent
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, fmt.Errorf("%w: intent parse: %v", ErrSentinelInvalid, err)
	}
	if in.Magic != IntentMagic {
		return nil, fmt.Errorf("%w: bad intent magic %q", ErrSentinelInvalid, in.Magic)
	}
	return &in, nil
}

// RemoveIntent deletes an intent marker; idempotent.
func RemoveIntent(markerPath string) error {
	if err := os.Remove(markerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing intent %s: %w", markerPath, err)
	}
	return nil
}

// ComputeLayerHash fingerprints the layer identity set (name, root, enabled) in
// order. It deliberately excludes MaterializerVersion so a materializer bump
// never strands a still-recoverable mounted farm (Guard R5/R6).
func ComputeLayerHash(layers []SentinelLayer) string {
	h := sha256.New()
	for _, l := range layers {
		fmt.Fprintf(h, "%s\x00%s\x00%t\n", l.Name, l.Root, l.Enabled)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SentinelLayer is a per-layer forensic record persisted in the sentinel.
type SentinelLayer struct {
	Name    string `json:"name"`
	Root    string `json:"root"`
	Enabled bool   `json:"enabled"`
}

// Sentinel is the on-disk record of an active overlay; load-bearing fields must stay stable across schema versions.
type Sentinel struct {
	SchemaVersion       int             `json:"schema_version"`
	Magic               string          `json:"magic"`
	GameID              string          `json:"game_id"`
	ProfileName         string          `json:"profile_name"`
	ActivationPID       int             `json:"activation_pid"`
	ActivationStartedAt time.Time       `json:"activation_started_at"`
	Hash                string          `json:"hash"`
	BackupPath          string          `json:"backup_path"`
	OverwriteMod        string          `json:"overwrite_mod"`
	OverwriteRoot       string          `json:"overwrite_root"`
	Layers              []SentinelLayer `json:"layers"`
	MaterializerVersion int             `json:"materializer_version"`
}

var (
	ErrSentinelMissing = errors.New("vfs: overlay sentinel missing")
	ErrSentinelInvalid = errors.New("vfs: overlay sentinel invalid")
)

// WriteSentinel serializes s to <dataPath>/SentinelFilename with 0644.
func WriteSentinel(dataPath string, s *Sentinel) error {
	if s == nil {
		return errors.New("vfs: WriteSentinel: nil sentinel")
	}
	body, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling sentinel: %w", err)
	}
	target := filepath.Join(dataPath, SentinelFilename)
	if err := os.WriteFile(target, body, 0644); err != nil {
		return fmt.Errorf("writing %s: %w", target, err)
	}
	return nil
}

// ReadSentinel loads the sentinel; returns ErrSentinelMissing when absent.
func ReadSentinel(dataPath string) (*Sentinel, error) {
	target := filepath.Join(dataPath, SentinelFilename)
	body, err := os.ReadFile(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrSentinelMissing
		}
		return nil, fmt.Errorf("reading %s: %w", target, err)
	}
	var s Sentinel
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("%w: parse: %v", ErrSentinelInvalid, err)
	}
	return &s, nil
}

// ValidateSentinel checks magic, schema version, backup_path existence, and —
// for schema v2+ — the populated identity fields and a recomputed layer hash.
// It is version-aware so that v1 sentinels (written by older builds) still
// validate and recover exactly as before; a version newer than this build
// understands is rejected rather than mishandled.
func ValidateSentinel(s *Sentinel) error {
	if s == nil {
		return fmt.Errorf("%w: nil", ErrSentinelInvalid)
	}
	if s.Magic != SentinelMagic {
		return fmt.Errorf("%w: bad magic %q (want %q)", ErrSentinelInvalid, s.Magic, SentinelMagic)
	}
	if s.SchemaVersion < 1 || s.SchemaVersion > CurrentSentinelSchema {
		return fmt.Errorf("%w: schema_version %d (this build understands 1..%d)",
			ErrSentinelInvalid, s.SchemaVersion, CurrentSentinelSchema)
	}
	if s.BackupPath == "" {
		return fmt.Errorf("%w: empty backup_path", ErrSentinelInvalid)
	}
	if _, err := os.Stat(s.BackupPath); err != nil {
		return fmt.Errorf("%w: backup_path %q: %v", ErrSentinelInvalid, s.BackupPath, err)
	}
	if s.SchemaVersion >= 2 {
		if s.GameID == "" {
			return fmt.Errorf("%w: v2 sentinel missing game_id", ErrSentinelInvalid)
		}
		if s.Hash == "" {
			return fmt.Errorf("%w: v2 sentinel missing hash", ErrSentinelInvalid)
		}
		if got := ComputeLayerHash(s.Layers); got != s.Hash {
			return fmt.Errorf("%w: layer hash mismatch (recorded %s, computed %s)",
				ErrSentinelInvalid, s.Hash, got)
		}
	}
	return nil
}

// RemoveSentinel deletes the sentinel file; idempotent.
func RemoveSentinel(dataPath string) error {
	target := filepath.Join(dataPath, SentinelFilename)
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", target, err)
	}
	return nil
}

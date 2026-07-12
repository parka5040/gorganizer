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

const SentinelFilename = ".gorganizer-overlay.json"

const SentinelMagic = "gorganizer-overlay"

const CurrentSentinelSchema = 2

const CurrentMaterializerVersion = 1

const (
	activatingSuffix = ".gorganizer-activating"
	applyingSuffix   = ".gorganizer-applying"
	stagingSuffix    = ".gorganizer-staging"
	oldFarmSuffix    = ".gorganizer-oldfarm"
)

const IntentMagic = "gorganizer-intent"

const CurrentIntentSchema = 1

type IntentKind string

const (
	IntentActivating IntentKind = "activating"
	IntentApplying   IntentKind = "applying"
)

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
func ComputeLayerHash(layers []SentinelLayer) string {
	h := sha256.New()
	for _, l := range layers {
		fmt.Fprintf(h, "%s\x00%s\x00%t\n", l.Name, l.Root, l.Enabled)
	}
	return hex.EncodeToString(h.Sum(nil))
}

type SentinelLayer struct {
	Name    string `json:"name"`
	Root    string `json:"root"`
	Enabled bool   `json:"enabled"`
}

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

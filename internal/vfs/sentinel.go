package vfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SentinelFilename is the marker file written inside an active overlay.
const SentinelFilename = ".gorganizer-overlay.json"

// SentinelMagic identifies a gorganizer-owned overlay; rejected on mismatch.
const SentinelMagic = "gorganizer-overlay"

// CurrentSentinelSchema is the schema version this build reads and writes.
const CurrentSentinelSchema = 1

// CurrentMaterializerVersion bumps when the on-disk layout changes incompatibly.
const CurrentMaterializerVersion = 1

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

// ValidateSentinel checks magic, schema version, and backup_path existence.
func ValidateSentinel(s *Sentinel) error {
	if s == nil {
		return fmt.Errorf("%w: nil", ErrSentinelInvalid)
	}
	if s.Magic != SentinelMagic {
		return fmt.Errorf("%w: bad magic %q (want %q)", ErrSentinelInvalid, s.Magic, SentinelMagic)
	}
	if s.SchemaVersion != CurrentSentinelSchema {
		return fmt.Errorf("%w: schema_version %d (this build understands %d)",
			ErrSentinelInvalid, s.SchemaVersion, CurrentSentinelSchema)
	}
	if s.BackupPath == "" {
		return fmt.Errorf("%w: empty backup_path", ErrSentinelInvalid)
	}
	if _, err := os.Stat(s.BackupPath); err != nil {
		return fmt.Errorf("%w: backup_path %q: %v", ErrSentinelInvalid, s.BackupPath, err)
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

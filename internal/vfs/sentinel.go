package vfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SentinelFilename is the marker file the materializer writes inside an
// active overlay. Its presence tells the recovery path "this Data/ is
// gorganizer-owned, here is the backup we can safely restore". Choose a
// name that's clearly ours (so a hand-edited Data/ from a non-gorganizer
// install doesn't accidentally validate) and that starts with a dot so
// Windows tools running under Wine treat it as hidden.
const SentinelFilename = ".gorganizer-overlay.json"

// SentinelMagic is the schema-version-independent identifier the sentinel
// uses to claim ownership. The materializer rejects any sentinel with the
// wrong magic — that's how we distinguish "valid sentinel from a previous
// gorganizer run" from "JSON file that happens to share our filename".
const SentinelMagic = "gorganizer-overlay"

// CurrentSentinelSchema is the schema version the current code reads and
// writes. A sentinel with a different version is treated as unrecognized
// (recovery-pending) rather than silently coerced — different schemas
// imply different on-disk invariants and we'd rather refuse than guess.
const CurrentSentinelSchema = 1

// CurrentMaterializerVersion is bumped whenever the materializer's on-disk
// layout changes in a way that breaks the cache (e.g. switch from per-file
// hardlinks to whole-tree CoW). The cache validator rejects stale entries.
const CurrentMaterializerVersion = 1

// SentinelLayer is the per-layer record persisted in the sentinel — kept
// minimal because the cache hash already pins the actual layer content;
// these fields are forensic ("which mods were active when this overlay
// was built").
type SentinelLayer struct {
	Name    string `json:"name"`
	Root    string `json:"root"`
	Enabled bool   `json:"enabled"`
}

// Sentinel is the on-disk record of an active overlay. Anything load-bearing
// for recovery (Magic, BackupPath, GameID, OverwriteMod) MUST stay
// stable across schema versions — recovery has to be able to read old
// sentinels from a daemon that was killed mid-upgrade.
type Sentinel struct {
	SchemaVersion        int             `json:"schema_version"`
	Magic                string          `json:"magic"`
	GameID               string          `json:"game_id"`
	ProfileName          string          `json:"profile_name"`
	ActivationPID        int             `json:"activation_pid"`
	ActivationStartedAt  time.Time       `json:"activation_started_at"`
	Hash                 string          `json:"hash"`
	BackupPath           string          `json:"backup_path"`
	OverwriteMod         string          `json:"overwrite_mod"`
	Layers               []SentinelLayer `json:"layers"`
	MaterializerVersion  int             `json:"materializer_version"`
}

var (
	// ErrSentinelMissing means there's no sentinel at the expected path.
	// Recovery treats this as "Data/ wasn't activated by us" — combined with
	// presence of Data.orig/, surfaces as recovery-pending.
	ErrSentinelMissing = errors.New("vfs: overlay sentinel missing")

	// ErrSentinelInvalid means the sentinel exists but doesn't pass the
	// magic + schema + backup-existence checks. Same recovery behavior as
	// missing: refuse auto-action, surface to user.
	ErrSentinelInvalid = errors.New("vfs: overlay sentinel invalid")
)

// WriteSentinel serializes s to <dataPath>/SentinelFilename with 0644.
// Caller is responsible for filling Magic + SchemaVersion +
// MaterializerVersion (we don't auto-fill so a typo in the constants
// surfaces in tests rather than baking incorrect values into prod).
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

// ReadSentinel loads the sentinel at <dataPath>/SentinelFilename. Returns
// ErrSentinelMissing when the file isn't there (the legitimate
// "no-overlay" state) and ErrSentinelInvalid wrapping the parse error
// when it's there but malformed.
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

// ValidateSentinel checks the load-bearing fields a recovery path must
// trust before it acts: magic + schema-version match + backup_path
// existing on disk. GameID is intentionally not required here — it's
// forensic-only at the sentinel layer; the daemon's recovery path
// cross-checks it against the expected value when it has one. This
// keeps MountManager (which doesn't know its own gameID) able to write
// and validate sentinels without an extra plumbing path.
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

// RemoveSentinel deletes the sentinel file from dataPath. Idempotent —
// missing file is not an error, since Deactivate may run after a partial
// activate that never wrote one.
func RemoveSentinel(dataPath string) error {
	target := filepath.Join(dataPath, SentinelFilename)
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", target, err)
	}
	return nil
}

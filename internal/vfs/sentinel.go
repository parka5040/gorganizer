package vfs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/parka/gorganizer/internal/atomicfile"
)

const SentinelFilename = ".gorganizer-overlay.json"

const RetainedSessionSiblingSuffix = ".gorganizer-session"

const SentinelMagic = "gorganizer-overlay"

const CurrentSentinelSchema = 3

const CurrentMaterializerVersion = 1

const (
	activatingSuffix   = ".gorganizer-activating"
	applyingSuffix     = ".gorganizer-applying"
	stagingSuffix      = ".gorganizer-staging"
	oldFarmSuffix      = ".gorganizer-oldfarm"
	deactivatingSuffix = ".gorganizer-deactivating"
	retiredSuffix      = ".gorganizer-retired"
	farmBackupSuffix   = ".orig"
	sentinelTempName   = ".tmp-" + SentinelFilename + "-"
)

// FarmSiblingSuffixes returns the pending transition siblings and parked original next to a farm's deploy folder.
func FarmSiblingSuffixes() []string {
	return []string{activatingSuffix, applyingSuffix, stagingSuffix, oldFarmSuffix, deactivatingSuffix, retiredSuffix, restoringSuffix, farmBackupSuffix}
}

// RetainedFarmSiblingSuffixes returns farm siblings that persist while a launched game may still use its deploy folder.
func RetainedFarmSiblingSuffixes() []string {
	return []string{RetainedSessionSiblingSuffix, preservedSuffix, maintenanceSuffix}
}

var farmManifestName = regexp.MustCompile(`^\.gorganizer-farm-[0-9a-f-]{36}\.jsonl$`)

// IsFarmMetadataFile reports whether name belongs to the farm's sentinel, manifest, or an interrupted temporary write.
func IsFarmMetadataFile(name string) bool {
	return name == SentinelFilename || strings.HasPrefix(name, sentinelTempName) ||
		strings.HasPrefix(name, farmManifestPrefix) || strings.HasPrefix(name, ".tmp-"+farmManifestPrefix)
}

const IntentMagic = "gorganizer-intent"

const CurrentIntentSchema = 2

type IntentKind string

const (
	IntentActivating IntentKind = "activating"
	IntentApplying   IntentKind = "applying"
)

type ActivationIntent struct {
	SchemaVersion int                `json:"schema_version"`
	Magic         string             `json:"magic"`
	Kind          IntentKind         `json:"kind"`
	GameID        string             `json:"game_id"`
	DataPath      string             `json:"data_path"`
	BackupPath    string             `json:"backup_path"`
	OverwriteRoot string             `json:"overwrite_root"`
	StagingPath   string             `json:"staging_path,omitempty"`
	OperationID   string             `json:"operation_id,omitempty"`
	Original      directoryIdentity  `json:"original,omitempty"`
	Farm          *directoryIdentity `json:"farm,omitempty"`
	LiveFarmID    string             `json:"live_farm_id,omitempty"`
	StagingFarmID string             `json:"staging_farm_id,omitempty"`
	PID           int                `json:"pid"`
}

func activatingIntentPath(dataPath string) string { return dataPath + activatingSuffix }

// ActivationIntentPath returns the activation intent path for dataPath.
func ActivationIntentPath(dataPath string) string { return activatingIntentPath(dataPath) }

func applyingIntentPath(dataPath string) string { return dataPath + applyingSuffix }
func stagingDirPath(dataPath string) string     { return dataPath + stagingSuffix }
func oldFarmPath(dataPath string) string        { return dataPath + oldFarmSuffix }

var ErrIntentMissing = errors.New("vfs: activation intent missing")

var writeIntentDurable = atomicfile.WriteFileDurable

// WriteIntent durably writes an intent marker.
func WriteIntent(markerPath string, in *ActivationIntent) error {
	if in == nil {
		return errors.New("vfs: WriteIntent: nil intent")
	}
	body, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling intent: %w", err)
	}
	_, err = writeIntentDurable(markerPath, body, 0644)
	return err
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
	if in.Magic != IntentMagic || in.SchemaVersion < 1 || in.SchemaVersion > CurrentIntentSchema {
		return nil, fmt.Errorf("%w: invalid or unsupported intent", ErrSentinelInvalid)
	}
	if in.SchemaVersion == 2 {
		if _, err := uuid.Parse(in.OperationID); err != nil {
			return nil, fmt.Errorf("%w: invalid intent operation ID: %v", ErrSentinelInvalid, err)
		}
		switch in.Kind {
		case IntentActivating:
			if in.Original.Dev == 0 || in.Original.Ino == 0 {
				return nil, fmt.Errorf("%w: missing original directory identity", ErrSentinelInvalid)
			}
			if in.Farm != nil && (in.Farm.Dev == 0 || in.Farm.Ino == 0 || *in.Farm == in.Original) {
				return nil, fmt.Errorf("%w: invalid activation farm identity", ErrSentinelInvalid)
			}
		case IntentApplying:
			if in.LiveFarmID == "" || in.StagingFarmID == "" || in.LiveFarmID == in.StagingFarmID {
				return nil, fmt.Errorf("%w: missing or duplicate apply farm identities", ErrSentinelInvalid)
			}
		default:
			return nil, fmt.Errorf("%w: invalid intent kind %q", ErrSentinelInvalid, in.Kind)
		}
	}
	return &in, nil
}

// RemoveIntent durably deletes an intent marker if it exists.
func RemoveIntent(markerPath string) error {
	if err := atomicfile.RemoveDurable(markerPath); err != nil {
		return fmt.Errorf("removing intent %s: %w", markerPath, err)
	}
	return nil
}

// ComputeLayerHash fingerprints the ordered layer identity set (name, root, enabled).
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

type StorefrontSnapshot struct {
	Store            string    `json:"store"`
	AppID            int       `json:"app_id"`
	BuildID          string    `json:"build_id"`
	StateFlags       uint64    `json:"state_flags"`
	UpdateResult     string    `json:"update_result"`
	LastUpdated      int64     `json:"last_updated"`
	DepotFingerprint string    `json:"depot_fingerprint"`
	CapturedAt       time.Time `json:"captured_at"`
}

type Sentinel struct {
	SchemaVersion       int                 `json:"schema_version"`
	Magic               string              `json:"magic"`
	GameID              string              `json:"game_id"`
	ProfileName         string              `json:"profile_name"`
	ActivationPID       int                 `json:"activation_pid"`
	ActivationStartedAt time.Time           `json:"activation_started_at"`
	Hash                string              `json:"hash"`
	BackupPath          string              `json:"backup_path"`
	OverwriteMod        string              `json:"overwrite_mod"`
	OverwriteRoot       string              `json:"overwrite_root"`
	Layers              []SentinelLayer     `json:"layers"`
	MaterializerVersion int                 `json:"materializer_version"`
	FarmID              string              `json:"farm_id,omitempty"`
	Manifest            string              `json:"manifest,omitempty"`
	ManifestSHA256      string              `json:"manifest_sha256,omitempty"`
	ManifestEntries     int                 `json:"manifest_entries,omitempty"`
	Storefront          *StorefrontSnapshot `json:"storefront,omitempty"`
}

var (
	ErrSentinelMissing = errors.New("vfs: overlay sentinel missing")
	ErrSentinelInvalid = errors.New("vfs: overlay sentinel invalid")
)

// WriteSentinel atomically serializes s to <dataPath>/SentinelFilename with 0644.
func WriteSentinel(dataPath string, s *Sentinel) error {
	if s == nil {
		return errors.New("vfs: WriteSentinel: nil sentinel")
	}
	body, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling sentinel: %w", err)
	}
	target := filepath.Join(dataPath, SentinelFilename)
	if err := atomicfile.WriteFile(target, body, 0644); err != nil {
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

// ValidateSentinel checks a sentinel's version, backup, identity, and manifest reference.
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
	if s.SchemaVersion >= 3 {
		if s.FarmID == "" || !farmManifestName.MatchString(s.Manifest) || s.Manifest != farmManifestPrefix+s.FarmID+".jsonl" {
			return fmt.Errorf("%w: v3 sentinel has an invalid farm manifest reference", ErrSentinelInvalid)
		}
		if s.ManifestEntries < 0 {
			return fmt.Errorf("%w: negative manifest_entries", ErrSentinelInvalid)
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

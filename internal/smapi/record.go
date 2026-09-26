package smapi

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/parka/gorganizer/internal/atomicfile"
)

const (
	recordSchema   = 1
	recordLoader   = "smapi"
	maxStateBytes  = 16 << 20
	recordFileMode = 0644
)

type Record struct {
	SchemaVersion  int            `json:"schema_version"`
	Loader         string         `json:"loader"`
	OpID           string         `json:"op_id"`
	Version        string         `json:"version"`
	ArtifactSHA256 string         `json:"artifact_sha256"`
	InstalledAt    time.Time      `json:"installed_at"`
	Targets        []string       `json:"targets"`
	Files          []RecordEntry  `json:"files"`
	Originals      []OriginalFile `json:"originals,omitempty"`
}

type RecordEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
	MTime  int64  `json:"mtime_ns,omitempty"`
}

type OriginalFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
}

// ReadRecord loads the committed install record of gameDir, returning nil when there is none.
func ReadRecord(gameDir string) (*Record, error) {
	full := filepath.Join(gameDir, RecordFile)
	info, err := lstatOptional(full)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, nil
	}
	data, err := readSmallRegular(full, maxStateBytes)
	if err != nil {
		return nil, fmt.Errorf("reading mod-loader record: %w", err)
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("decoding mod-loader record: %w", err)
	}
	if err := record.validate(); err != nil {
		return nil, err
	}
	return &record, nil
}

// validate checks the record schema, that every target is a safe distinct path covering recorded files, and that originals are well formed.
func (r *Record) validate() error {
	if r.SchemaVersion != recordSchema {
		return fmt.Errorf("mod-loader record has unsupported schema %d", r.SchemaVersion)
	}
	if r.Loader != recordLoader {
		return fmt.Errorf("mod-loader record names loader %q", r.Loader)
	}
	if err := validateOpID(r.OpID); err != nil {
		return fmt.Errorf("mod-loader record: %w", err)
	}
	covered := map[string]bool{}
	for i, target := range r.Targets {
		if err := validateRelPath(target); err != nil {
			return fmt.Errorf("mod-loader record target: %w", err)
		}
		if target == modsDirName {
			return fmt.Errorf("mod-loader record claims the whole %s folder", modsDirName)
		}
		for _, other := range r.Targets[:i] {
			if overlaps(target, other) {
				return fmt.Errorf("mod-loader record targets %q and %q overlap", other, target)
			}
		}
	}
	for _, file := range r.Files {
		if err := validateRelPath(file.Path); err != nil {
			return fmt.Errorf("mod-loader record file: %w", err)
		}
		if err := validateDigest(file.SHA256, file.Size, file.Mode); err != nil {
			return fmt.Errorf("mod-loader record file %s: %w", file.Path, err)
		}
		owner := ""
		for _, target := range r.Targets {
			if isWithin(file.Path, target) {
				owner = target
			}
		}
		if owner == "" {
			return fmt.Errorf("mod-loader record file %s lies outside every recorded target", file.Path)
		}
		covered[owner] = true
	}
	for _, target := range r.Targets {
		if !covered[target] {
			return fmt.Errorf("mod-loader record target %s has no recorded files", target)
		}
	}
	seen := map[string]bool{}
	for _, original := range r.Originals {
		if err := validateRelPath(original.Path); err != nil {
			return fmt.Errorf("mod-loader record original: %w", err)
		}
		if err := validateDigest(original.SHA256, original.Size, original.Mode); err != nil {
			return fmt.Errorf("mod-loader record original %s: %w", original.Path, err)
		}
		if seen[original.Path] || !contains(r.Targets, original.Path) {
			return fmt.Errorf("mod-loader record original %s is duplicated or not a recorded target", original.Path)
		}
		seen[original.Path] = true
	}
	return nil
}

// checkOwnership refuses a record that claims a game-owned path of spec.
func (r *Record) checkOwnership(spec LoaderSpec) error {
	protected := spec.protectedNames()
	for _, target := range r.Targets {
		if protectedTarget(target, protected) {
			return &UnsafeTargetError{Path: target, Reason: "the install record claims a game-owned path"}
		}
	}
	return nil
}

// original returns the recorded original for rel, if any.
func (r *Record) original(rel string) (OriginalFile, bool) {
	if r == nil {
		return OriginalFile{}, false
	}
	for _, original := range r.Originals {
		if original.Path == rel {
			return original, true
		}
	}
	return OriginalFile{}, false
}

// validateDigest requires a 64-character hex digest, a non-negative size, and permission-only mode bits.
func validateDigest(sum string, size int64, mode uint32) error {
	if len(sum) != 64 {
		return errors.New("invalid SHA-256 digest")
	}
	if _, err := hex.DecodeString(sum); err != nil {
		return fmt.Errorf("invalid SHA-256 digest: %w", err)
	}
	if size < 0 || mode&^0777 != 0 {
		return errors.New("invalid size or mode")
	}
	return nil
}

// writeRecord atomically commits record to gameDir.
func writeRecord(gameDir string, record *Record) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(filepath.Join(gameDir, RecordFile), data, recordFileMode); err != nil {
		return fmt.Errorf("writing mod-loader record: %w", err)
	}
	return nil
}

// recordEntries hashes and flushes every regular file under each rel path of root and returns them sorted.
func recordEntries(root string, rels []string) ([]RecordEntry, error) {
	var entries []RecordEntry
	for _, rel := range rels {
		start := filepath.Join(root, filepath.FromSlash(rel))
		err := filepath.WalkDir(start, func(full string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return syncDir(full)
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("%s is not a regular file", full)
			}
			file, info, err := hashRegular(full, true)
			if err != nil {
				return err
			}
			slash, err := relToSlash(root, full)
			if err != nil {
				return err
			}
			entries = append(entries, RecordEntry{Path: slash, SHA256: file.SHA256, Size: file.Size, Mode: uint32(info.Mode().Perm()), MTime: info.ModTime().UnixNano()})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("recording %s: %w", rel, err)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

// removeRecord deletes the committed record, treating a missing record as removed.
func removeRecord(gameDir string) error {
	err := os.Remove(filepath.Join(gameDir, RecordFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing mod-loader record: %w", err)
	}
	return nil
}

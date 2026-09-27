package vfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/parka/gorganizer/internal/atomicfile"
)

const preservedSuffix = ".gorganizer-preserved"
const maintenanceSuffix = ".gorganizer-maintenance"

type CaptureOptions struct {
	PreserveInto string              `json:"preserve_into,omitempty"`
	BatchID      string              `json:"batch_id,omitempty"`
	Baseline     *StorefrontSnapshot `json:"baseline,omitempty"`
	Current      *StorefrontSnapshot `json:"current,omitempty"`
}

type PreservedBatch struct {
	SchemaVersion int                 `json:"schema_version"`
	BatchID       string              `json:"batch_id"`
	GameID        string              `json:"game_id"`
	CreatedAt     time.Time           `json:"created_at"`
	Reason        string              `json:"reason"`
	Baseline      *StorefrontSnapshot `json:"baseline"`
	Current       *StorefrontSnapshot `json:"current"`
	Files         []string            `json:"files"`
	Path          string              `json:"-"`
}

type MaintenanceMarker struct {
	SchemaVersion int                 `json:"schema_version"`
	Reason        string              `json:"reason"`
	GameID        string              `json:"game_id"`
	Baseline      *StorefrontSnapshot `json:"baseline"`
	Current       *StorefrontSnapshot `json:"current"`
	BatchIDs      []string            `json:"batch_ids"`
	CreatedAt     time.Time           `json:"created_at"`
}

// PreservedDir returns the retained files directory beside a game's deploy folder.
func PreservedDir(dataPath string) string { return dataPath + preservedSuffix }

// MaintenancePath returns the durable maintenance marker beside a game's deploy folder.
func MaintenancePath(dataPath string) string { return dataPath + maintenanceSuffix }

// ReadMaintenance reads and validates the maintenance marker without following a final symlink.
func ReadMaintenance(dataPath string) (*MaintenanceMarker, error) {
	path := MaintenancePath(dataPath)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("checking maintenance marker: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("maintenance marker is not a regular file")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading maintenance marker: %w", err)
	}
	var marker MaintenanceMarker
	if err := json.Unmarshal(body, &marker); err != nil {
		return nil, fmt.Errorf("parsing maintenance marker: %w", err)
	}
	if marker.SchemaVersion != 1 || marker.GameID == "" || marker.CreatedAt.IsZero() ||
		(marker.Reason != "verify" && marker.Reason != "user") {
		return nil, fmt.Errorf("invalid or unsupported maintenance marker")
	}
	return &marker, nil
}

// ListPreservedBatches returns valid batch records in creation order, ignoring malformed entries.
func ListPreservedBatches(dataPath string) ([]PreservedBatch, error) {
	root := PreservedDir(dataPath)
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("checking preserved batches: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("preserved batches location is not a directory")
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading preserved batches: %w", err)
	}
	var batches []PreservedBatch
	for _, entry := range entries {
		if !entry.IsDir() || uuid.Validate(entry.Name()) != nil {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, err := os.Lstat(filepath.Join(path, "batch.json"))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(path, "batch.json"))
		if err != nil {
			continue
		}
		var batch PreservedBatch
		if json.Unmarshal(body, &batch) != nil || batch.SchemaVersion != 1 || batch.BatchID != entry.Name() ||
			batch.GameID == "" || batch.Reason != "steam_changed" || batch.CreatedAt.IsZero() {
			continue
		}
		batch.Path = path
		batches = append(batches, batch)
	}
	sort.Slice(batches, func(i, j int) bool {
		if batches[i].CreatedAt.Equal(batches[j].CreatedAt) {
			return batches[i].BatchID < batches[j].BatchID
		}
		return batches[i].CreatedAt.Before(batches[j].CreatedAt)
	})
	return batches, nil
}

// preserveFarmOutput moves unowned regular farm files to a durable batch without touching Overwrite.
func preserveFarmOutput(farmDir, dataPath string, s *Sentinel, opts CaptureOptions) error {
	if opts.PreserveInto != PreservedDir(dataPath) || uuid.Validate(opts.BatchID) != nil || opts.Baseline == nil || opts.Current == nil {
		return fmt.Errorf("%w: invalid preservation decision", ErrCaptureFailed)
	}
	manifest, err := ReadFarmManifest(farmDir, s)
	if err != nil {
		return fmt.Errorf("%w: reading farm manifest: %w", ErrCaptureFailed, err)
	}
	classification, err := ClassifyFarm(farmDir, manifest)
	if err != nil {
		return fmt.Errorf("%w: classifying farm: %w", ErrCaptureFailed, err)
	}
	root := opts.PreserveInto
	if info, err := os.Lstat(root); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%w: preserved files location is not a directory", ErrCaptureFailed)
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(root, 0755); err != nil {
			return fmt.Errorf("%w: creating preserved files folder: %w", ErrCaptureFailed, err)
		}
		if err := atomicfile.SyncDir(filepath.Dir(root)); err != nil {
			return fmt.Errorf("%w: syncing preserved files folder: %w", ErrCaptureFailed, err)
		}
	} else {
		return fmt.Errorf("%w: checking preserved files folder: %w", ErrCaptureFailed, err)
	}
	batchPath := filepath.Join(root, opts.BatchID)
	info, statErr := os.Lstat(batchPath)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("%w: checking preserved batch: %w", ErrCaptureFailed, statErr)
	}
	if statErr == nil && !info.IsDir() {
		return fmt.Errorf("%w: preserved batch is not a directory", ErrCaptureFailed)
	}
	if errors.Is(statErr, os.ErrNotExist) {
		if len(classification.Output) == 0 {
			return nil
		}
		if err := os.Mkdir(batchPath, 0755); err != nil {
			return fmt.Errorf("%w: creating preserved batch: %w", ErrCaptureFailed, err)
		}
		if err := atomicfile.SyncDir(root); err != nil {
			return fmt.Errorf("%w: syncing preserved batch: %w", ErrCaptureFailed, err)
		}
	}
	batch := PreservedBatch{SchemaVersion: 1, BatchID: opts.BatchID, GameID: s.GameID, CreatedAt: time.Now().UTC(),
		Reason: "steam_changed", Baseline: opts.Baseline, Current: opts.Current}
	batchRecord := filepath.Join(batchPath, "batch.json")
	if statErr == nil {
		recordInfo, err := os.Lstat(batchRecord)
		if err == nil && recordInfo.Mode().IsRegular() {
			body, err := os.ReadFile(batchRecord)
			if err != nil || json.Unmarshal(body, &batch) != nil || batch.SchemaVersion != 1 || batch.BatchID != opts.BatchID ||
				batch.GameID != s.GameID || batch.Reason != "steam_changed" || batch.CreatedAt.IsZero() ||
				!reflect.DeepEqual(batch.Baseline, opts.Baseline) || !reflect.DeepEqual(batch.Current, opts.Current) {
				return fmt.Errorf("%w: invalid preserved batch record", ErrCaptureFailed)
			}
		} else if err != nil && !errors.Is(err, os.ErrNotExist) || err == nil {
			return fmt.Errorf("%w: checking preserved batch record: %w", ErrCaptureFailed, err)
		}
	}
	known := make(map[string]bool, len(batch.Files))
	for _, rel := range batch.Files {
		if !filepath.IsLocal(filepath.FromSlash(rel)) || known[rel] {
			return fmt.Errorf("%w: invalid preserved batch file %q", ErrCaptureFailed, rel)
		}
		known[rel] = true
	}
	for _, rel := range classification.Output {
		if !known[rel] {
			batch.Files = append(batch.Files, rel)
		}
	}
	sort.Strings(batch.Files)
	body, err := json.Marshal(batch)
	if err != nil {
		return fmt.Errorf("%w: encoding preserved batch: %w", ErrCaptureFailed, err)
	}
	if _, err := atomicfile.WriteFileDurable(batchRecord, body, 0644); err != nil {
		return fmt.Errorf("%w: recording preserved batch: %w", ErrCaptureFailed, err)
	}
	dirs := make(map[string]struct{})
	for _, rel := range batch.Files {
		path := filepath.Join(farmDir, filepath.FromSlash(rel))
		dst := filepath.Join(batchPath, "files", filepath.FromSlash(rel))
		created, err := prepareCaptureDestination(filepath.Join(batchPath, "files"), filepath.FromSlash(rel), dirs)
		if err != nil {
			return errors.Join(err, syncCaptureDirs(dirs))
		}
		dstInfo, dstErr := os.Lstat(dst)
		if dstErr != nil && !errors.Is(dstErr, os.ErrNotExist) {
			return fmt.Errorf("%w: checking preserved file: %w", ErrCaptureFailed, dstErr)
		}
		srcInfo, srcErr := os.Lstat(path)
		if srcErr != nil && !errors.Is(srcErr, os.ErrNotExist) {
			return fmt.Errorf("%w: checking farm file: %w", ErrCaptureFailed, srcErr)
		}
		if dstErr == nil {
			if !dstInfo.Mode().IsRegular() || srcErr == nil {
				return fmt.Errorf("%w: preserved file %q conflicts with farm output", ErrCaptureFailed, rel)
			}
			continue
		}
		if srcErr != nil || !srcInfo.Mode().IsRegular() {
			return fmt.Errorf("%w: preserved file %q is missing", ErrCaptureFailed, rel)
		}
		if err := moveFile(path, dst, created); err != nil {
			return errors.Join(fmt.Errorf("%w: preserving %q: %w", ErrCaptureFailed, rel, err), syncCaptureDirs(dirs))
		}
	}
	return syncCaptureDirs(dirs)
}

// writeMaintenance records that the original files require verification before mods may resume.
func writeMaintenance(dataPath string, s *Sentinel, opts CaptureOptions) error {
	batches, err := ListPreservedBatches(dataPath)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(batches))
	for _, batch := range batches {
		if batch.GameID == s.GameID {
			ids = append(ids, batch.BatchID)
		}
	}
	marker, err := ReadMaintenance(dataPath)
	if err != nil {
		return err
	}
	if marker == nil {
		marker = &MaintenanceMarker{SchemaVersion: 1, GameID: s.GameID,
			Baseline: opts.Baseline, CreatedAt: time.Now().UTC()}
	}
	if marker.GameID != s.GameID {
		return fmt.Errorf("maintenance marker belongs to %s, not %s", marker.GameID, s.GameID)
	}
	marker.Reason = "verify"
	marker.Current = opts.Current
	marker.BatchIDs = ids
	body, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	if _, err := atomicfile.WriteFileDurable(MaintenancePath(dataPath), body, 0644); err != nil {
		return fmt.Errorf("writing maintenance marker: %w", err)
	}
	return nil
}

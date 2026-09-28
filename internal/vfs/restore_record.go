package vfs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/parka/gorganizer/internal/atomicfile"
)

const restoringSuffix = ".gorganizer-restoring"

type restoreRecord struct {
	SchemaVersion int               `json:"schema_version"`
	DataPath      string            `json:"data_path"`
	Backup        directoryIdentity `json:"backup"`
	Farm          directoryIdentity `json:"farm,omitempty"`
}

var restoreStep = func(int) error { return nil }

// readRestoreRecord reads a confirmed restore's durable backup identity without following a record link.
func readRestoreRecord(dataPath string) (*restoreRecord, error) {
	path := dataPath + restoringSuffix
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("checking restore record: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return nil, fmt.Errorf("invalid restore record")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading restore record: %w", err)
	}
	var record restoreRecord
	if err := json.Unmarshal(body, &record); err != nil || record.SchemaVersion != 1 || record.DataPath != dataPath || record.Backup.Dev == 0 || record.Backup.Ino == 0 {
		return nil, fmt.Errorf("invalid restore record: %v", err)
	}
	return &record, nil
}

// writeRestoreRecord durably records the backup before a confirmed restore consumes it.
func writeRestoreRecord(dataPath string, record *restoreRecord) error {
	body, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encoding restore record: %w", err)
	}
	if _, err := atomicfile.WriteFileDurable(dataPath+restoringSuffix, body, 0600); err != nil {
		return fmt.Errorf("writing restore record: %w", err)
	}
	return nil
}

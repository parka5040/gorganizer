package download

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/kvfile"
)

const ledgerFilename = "inflight.yaml"

type LedgerStatus string

const (
	LedgerQueued      LedgerStatus = "queued"
	LedgerDownloading LedgerStatus = "downloading"
	LedgerDownloaded  LedgerStatus = "downloaded"
	LedgerCancelled   LedgerStatus = "cancelled"
	LedgerFailed      LedgerStatus = "failed"
)

type LedgerEntry struct {
	ID             string
	NXMURI         string
	GameSlug       string
	GameID         string
	ModID          int
	FileID         int
	ArchiveRelPath string
	BytesDone      int64
	BytesTotal     int64
	StartedAt      time.Time
	UpdatedAt      time.Time
	Status         LedgerStatus
	Error          string
}

// Terminal returns true for ledger statuses that no longer require work.
func (e LedgerEntry) Terminal() bool {
	return e.Status == LedgerDownloaded ||
		e.Status == LedgerCancelled ||
		e.Status == LedgerFailed
}

var (
	ledgerMuOnce sync.Once
	ledgerMu     map[string]*sync.Mutex
	ledgerMuMap  sync.Mutex
)

func ledgerLock(gameID string) *sync.Mutex {
	ledgerMuOnce.Do(func() { ledgerMu = make(map[string]*sync.Mutex) })
	ledgerMuMap.Lock()
	defer ledgerMuMap.Unlock()
	m, ok := ledgerMu[gameID]
	if !ok {
		m = &sync.Mutex{}
		ledgerMu[gameID] = m
	}
	return m
}

// LoadLedger returns every entry in the game's ledger; missing file = empty list.
func LoadLedger(gameID string) ([]LedgerEntry, error) {
	path := filepath.Join(config.DownloadsDir(gameID), ledgerFilename)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	var out []LedgerEntry
	var cur *LedgerEntry
	sc := kvfile.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		l := sc.Line()
		if l.Text == "inflight:" {
			continue
		}
		if l.IsListItem {
			if cur != nil {
				out = append(out, *cur)
			}
			cur = &LedgerEntry{GameID: gameID}
		}
		if cur == nil {
			continue
		}
		k, v, ok := kvfile.CutKV(l.Item)
		if !ok {
			continue
		}
		v = kvfile.UnquoteValue(v)
		switch k {
		case "id":
			cur.ID = v
		case "nxm_uri":
			cur.NXMURI = v
		case "game_slug":
			cur.GameSlug = v
		case "mod_id":
			cur.ModID, _ = strconv.Atoi(v)
		case "file_id":
			cur.FileID, _ = strconv.Atoi(v)
		case "archive_rel":
			cur.ArchiveRelPath = v
		case "bytes_done":
			cur.BytesDone, _ = strconv.ParseInt(v, 10, 64)
		case "bytes_total":
			cur.BytesTotal, _ = strconv.ParseInt(v, 10, 64)
		case "started_at":
			if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
				cur.StartedAt = t
			}
		case "updated_at":
			if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
				cur.UpdatedAt = t
			}
		case "status":
			cur.Status = LedgerStatus(v)
		case "error":
			cur.Error = v
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out, sc.Err()
}

func SaveLedger(gameID string, entries []LedgerEntry) error {
	dir := config.DownloadsDir(gameID)
	if _, err := config.EnsureDir(dir); err != nil {
		return err
	}
	path := filepath.Join(dir, ledgerFilename)

	var w kvfile.Writer
	w.Comment("Gorganizer in-flight downloads ledger — auto-generated")
	w.ListHeader("inflight")
	for _, e := range entries {
		w.ItemQuoted("id", e.ID)
		w.ContQuoted("nxm_uri", e.NXMURI)
		w.ContQuoted("game_slug", e.GameSlug)
		w.ContInt("mod_id", e.ModID)
		w.ContInt("file_id", e.FileID)
		w.ContQuoted("archive_rel", e.ArchiveRelPath)
		w.ContInt64("bytes_done", e.BytesDone)
		w.ContInt64("bytes_total", e.BytesTotal)
		if !e.StartedAt.IsZero() {
			w.ContQuoted("started_at", e.StartedAt.UTC().Format(time.RFC3339Nano))
		}
		if !e.UpdatedAt.IsZero() {
			w.ContQuoted("updated_at", e.UpdatedAt.UTC().Format(time.RFC3339Nano))
		}
		w.ContQuoted("status", string(e.Status))
		if e.Error != "" {
			w.ContQuoted("error", e.Error)
		}
	}

	return w.WriteAtomic(path, 0644)
}

// UpsertLedgerEntry inserts or overwrites a single entry, keyed by ID.
func UpsertLedgerEntry(e LedgerEntry) error {
	if e.ID == "" {
		return fmt.Errorf("ledger entry requires ID")
	}
	mu := ledgerLock(e.GameID)
	mu.Lock()
	defer mu.Unlock()
	entries, err := LoadLedger(e.GameID)
	if err != nil {
		return err
	}
	e.UpdatedAt = time.Now()
	replaced := false
	for i := range entries {
		if entries[i].ID == e.ID {
			entries[i] = e
			replaced = true
			break
		}
	}
	if !replaced {
		if e.StartedAt.IsZero() {
			e.StartedAt = time.Now()
		}
		entries = append(entries, e)
	}
	return SaveLedger(e.GameID, entries)
}

// RemoveLedgerEntry drops an entry by ID; no-op if missing.
func RemoveLedgerEntry(gameID, id string) error {
	mu := ledgerLock(gameID)
	mu.Lock()
	defer mu.Unlock()
	entries, err := LoadLedger(gameID)
	if err != nil {
		return err
	}
	kept := entries[:0]
	for _, e := range entries {
		if e.ID != id {
			kept = append(kept, e)
		}
	}
	return SaveLedger(gameID, kept)
}

const PartSuffix = ".part"

// PartPath returns the .part sibling of an archive path.
func PartPath(archivePath string) string { return archivePath + PartSuffix }

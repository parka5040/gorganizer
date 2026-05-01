package download

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parka/gorganizer/internal/config"
)

const ledgerFilename = "inflight.yaml"

// LedgerStatus is the on-disk status string for a download.
type LedgerStatus string

const (
	LedgerQueued      LedgerStatus = "queued"
	LedgerDownloading LedgerStatus = "downloading"
	LedgerDownloaded  LedgerStatus = "downloaded"
	LedgerCancelled   LedgerStatus = "cancelled"
	LedgerFailed      LedgerStatus = "failed"
)

// LedgerEntry is one durable row in inflight.yaml.
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
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		raw := scanner.Text()
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || line == "inflight:" {
			continue
		}
		if strings.HasPrefix(line, "- ") {
			if cur != nil {
				out = append(out, *cur)
			}
			cur = &LedgerEntry{GameID: gameID}
			line = strings.TrimPrefix(line, "- ")
		}
		if cur == nil {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(strings.Trim(strings.TrimSpace(v), `"`))
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
	return out, scanner.Err()
}

func SaveLedger(gameID string, entries []LedgerEntry) error {
	dir := config.DownloadsDir(gameID)
	if _, err := config.EnsureDir(dir); err != nil {
		return err
	}
	path := filepath.Join(dir, ledgerFilename)

	var b strings.Builder
	b.WriteString("# Gorganizer in-flight downloads ledger — auto-generated\n")
	b.WriteString("inflight:\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "  - id: %q\n", e.ID)
		fmt.Fprintf(&b, "    nxm_uri: %q\n", e.NXMURI)
		fmt.Fprintf(&b, "    game_slug: %q\n", e.GameSlug)
		fmt.Fprintf(&b, "    mod_id: %d\n", e.ModID)
		fmt.Fprintf(&b, "    file_id: %d\n", e.FileID)
		fmt.Fprintf(&b, "    archive_rel: %q\n", e.ArchiveRelPath)
		fmt.Fprintf(&b, "    bytes_done: %d\n", e.BytesDone)
		fmt.Fprintf(&b, "    bytes_total: %d\n", e.BytesTotal)
		if !e.StartedAt.IsZero() {
			fmt.Fprintf(&b, "    started_at: %q\n", e.StartedAt.UTC().Format(time.RFC3339Nano))
		}
		if !e.UpdatedAt.IsZero() {
			fmt.Fprintf(&b, "    updated_at: %q\n", e.UpdatedAt.UTC().Format(time.RFC3339Nano))
		}
		fmt.Fprintf(&b, "    status: %q\n", string(e.Status))
		if e.Error != "" {
			fmt.Fprintf(&b, "    error: %q\n", e.Error)
		}
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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

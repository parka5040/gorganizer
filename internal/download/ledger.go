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

// ledgerFilename is the per-game durable ledger of in-flight / terminal
// downloads. Lives next to {DownloadsDir}/metadata.yaml so an `rm -rf
// Downloads/` wipes both halves of the state.
const ledgerFilename = "inflight.yaml"

// LedgerStatus is the durable status for a download as persisted to disk.
// Deliberately a small, stable string set — the ipc.DownloadStatus numeric
// enum is what flows on the wire, but we store strings here so a human
// editing inflight.yaml during debugging gets readable text.
type LedgerStatus string

const (
	LedgerQueued      LedgerStatus = "queued"
	LedgerDownloading LedgerStatus = "downloading"
	LedgerDownloaded  LedgerStatus = "downloaded" // file complete; ledger will be evicted by UpsertEntry
	LedgerCancelled   LedgerStatus = "cancelled"
	LedgerFailed      LedgerStatus = "failed"
)

// LedgerEntry is one durable row in inflight.yaml. Everything needed to
// resume the download after a daemon restart lives here.
type LedgerEntry struct {
	ID             string       // UUIDv7 — persistent across restarts
	NXMURI         string       // raw nxm:// URI so we can re-resolve if the CDN URL expires
	GameSlug       string
	GameID         string
	ModID          int
	FileID         int
	ArchiveRelPath string // relative to DownloadsDir(gameID)
	BytesDone      int64
	BytesTotal     int64
	StartedAt      time.Time
	UpdatedAt      time.Time
	Status         LedgerStatus
	Error          string
}

// Terminal returns true for ledger statuses that no longer require work —
// the caller can skip them on rehydrate or drop them from the list at
// will.
func (e LedgerEntry) Terminal() bool {
	return e.Status == LedgerDownloaded ||
		e.Status == LedgerCancelled ||
		e.Status == LedgerFailed
}

// ledgerMutexes per-game serializes ledger I/O. The download pipeline hits
// this on every progress update, so a write-through mutex with an atomic
// tmp+rename is enough; no pg-scale pressure.
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

// LoadLedger returns every entry in the game's ledger. Missing file is not
// an error — fresh install → empty list.
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

// SaveLedger rewrites the ledger atomically.
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
// Serializes writes per-gameID — safe to call from the download goroutine
// on every progress tick (provided the caller throttles).
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

// RemoveLedgerEntry drops an entry by ID. No-op if missing.
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

// PartSuffix is the extension used for in-progress archive writes. The
// downloader streams into <archive>.part; on success the file is atomically
// renamed to <archive>. Any `.part` file on disk at startup is evidence of
// an interrupted download and pairs with a ledger entry.
const PartSuffix = ".part"

// PartPath returns the .part sibling of an archive path.
func PartPath(archivePath string) string { return archivePath + PartSuffix }

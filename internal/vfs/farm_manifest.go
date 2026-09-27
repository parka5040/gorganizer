package vfs

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/parka/gorganizer/internal/atomicfile"
)

const farmManifestPrefix = ".gorganizer-farm-"

type FarmManifestEntry struct {
	Path       string `json:"p"`
	Dev        uint64 `json:"d"`
	Ino        uint64 `json:"i"`
	Type       string `json:"t"`
	LinkTarget string `json:"s,omitempty"`
}

type farmManifestHeader struct {
	SchemaVersion int       `json:"schema_version"`
	FarmID        string    `json:"farm_id"`
	CreatedAt     time.Time `json:"created_at"`
	Entries       int       `json:"entries"`
}

type FarmManifest struct {
	FarmID    string
	CreatedAt time.Time
	Entries   map[string]FarmManifestEntry
	Casefold  map[string]string
}

type farmManifestWriter struct {
	farmID    string
	createdAt time.Time
	entries   *os.File
	buffer    *bufio.Writer
	encoder   *json.Encoder
	count     int
}

func newFarmManifestWriter(outDir string) (*farmManifestWriter, error) {
	entries, err := os.CreateTemp(outDir, ".tmp-"+farmManifestPrefix+"entries-*")
	if err != nil {
		return nil, fmt.Errorf("creating farm manifest entries: %w", err)
	}
	buffer := bufio.NewWriter(entries)
	return &farmManifestWriter{
		farmID: uuid.NewString(), createdAt: time.Now().UTC(), entries: entries,
		buffer: buffer, encoder: json.NewEncoder(buffer),
	}, nil
}

func (w *farmManifestWriter) close() {
	_ = w.entries.Close()
	_ = os.Remove(w.entries.Name())
}

func (w *farmManifestWriter) record(farmPath, rel string) error {
	info, err := os.Lstat(farmPath)
	if err != nil {
		return fmt.Errorf("lstat placed entry %q: %w", farmPath, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%w: no file identity for %q", ErrManifestInvalid, farmPath)
	}
	entry := FarmManifestEntry{Path: rel, Dev: uint64(stat.Dev), Ino: stat.Ino}
	switch {
	case info.Mode().IsRegular():
		entry.Type = "f"
	case info.Mode()&os.ModeSymlink != 0:
		entry.Type = "l"
		entry.LinkTarget, err = os.Readlink(farmPath)
		if err != nil {
			return fmt.Errorf("reading placed symlink %q: %w", farmPath, err)
		}
	default:
		return fmt.Errorf("%w: unsupported placed entry %q", ErrManifestInvalid, farmPath)
	}
	if err := w.encoder.Encode(entry); err != nil {
		return fmt.Errorf("writing farm manifest entry %q: %w", rel, err)
	}
	w.count++
	return nil
}

func (w *farmManifestWriter) finish(outDir string) (string, string, int, error) {
	if err := w.buffer.Flush(); err != nil {
		return "", "", 0, fmt.Errorf("flushing farm manifest entries: %w", err)
	}
	if _, err := w.entries.Seek(0, io.SeekStart); err != nil {
		return "", "", 0, fmt.Errorf("rewinding farm manifest entries: %w", err)
	}
	name := farmManifestPrefix + w.farmID + ".jsonl"
	tmp, err := os.CreateTemp(outDir, ".tmp-"+name+"-*")
	if err != nil {
		return "", "", 0, fmt.Errorf("creating farm manifest: %w", err)
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	h := sha256.New()
	body := bufio.NewWriter(io.MultiWriter(tmp, h))
	if err := json.NewEncoder(body).Encode(farmManifestHeader{
		SchemaVersion: 1, FarmID: w.farmID, CreatedAt: w.createdAt, Entries: w.count,
	}); err != nil {
		return "", "", 0, fmt.Errorf("writing farm manifest header: %w", err)
	}
	if _, err := io.Copy(body, w.entries); err != nil {
		return "", "", 0, fmt.Errorf("writing farm manifest entries: %w", err)
	}
	if err := body.Flush(); err != nil {
		return "", "", 0, fmt.Errorf("flushing farm manifest: %w", err)
	}
	if err := tmp.Chmod(0644); err != nil {
		return "", "", 0, fmt.Errorf("setting farm manifest mode: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return "", "", 0, fmt.Errorf("syncing farm manifest: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", "", 0, fmt.Errorf("closing farm manifest: %w", err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(outDir, name)); err != nil {
		return "", "", 0, fmt.Errorf("publishing farm manifest: %w", err)
	}
	if err := atomicfile.SyncDir(outDir); err != nil {
		return "", "", 0, fmt.Errorf("syncing farm manifest directory: %w", err)
	}
	return name, hex.EncodeToString(h.Sum(nil)), w.count, nil
}

// ReadFarmManifest verifies and loads the manifest referenced by a v3 sentinel.
func ReadFarmManifest(dataPath string, s *Sentinel) (*FarmManifest, error) {
	if s == nil || s.SchemaVersion != 3 || s.FarmID == "" ||
		!farmManifestName.MatchString(s.Manifest) || s.Manifest != farmManifestPrefix+s.FarmID+".jsonl" ||
		s.ManifestEntries < 0 {
		return nil, fmt.Errorf("%w: invalid sentinel manifest reference", ErrManifestInvalid)
	}
	file, err := os.OpenFile(filepath.Join(dataPath, s.Manifest), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: opening manifest: %w", ErrManifestInvalid, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("%w: stat manifest: %w", ErrManifestInvalid, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: manifest is not a regular file", ErrManifestInvalid)
	}
	h := sha256.New()
	scanner := bufio.NewScanner(io.TeeReader(file, h))
	scanner.Buffer(make([]byte, 64*1024), 64*1024+2)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("%w: reading header: %w", ErrManifestInvalid, err)
		}
		return nil, fmt.Errorf("%w: missing header", ErrManifestInvalid)
	}
	if len(scanner.Bytes()) > 64*1024 {
		return nil, fmt.Errorf("%w: header too long", ErrManifestInvalid)
	}
	var header farmManifestHeader
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
		return nil, fmt.Errorf("%w: parsing header: %w", ErrManifestInvalid, err)
	}
	if header.SchemaVersion != 1 || header.FarmID != s.FarmID ||
		header.CreatedAt.IsZero() || header.Entries != s.ManifestEntries {
		return nil, fmt.Errorf("%w: header does not match sentinel", ErrManifestInvalid)
	}
	manifest := &FarmManifest{
		FarmID: header.FarmID, CreatedAt: header.CreatedAt,
		Entries:  make(map[string]FarmManifestEntry),
		Casefold: make(map[string]string),
	}
	for scanner.Scan() {
		if len(scanner.Bytes()) > 64*1024 {
			return nil, fmt.Errorf("%w: entry too long", ErrManifestInvalid)
		}
		var entry FarmManifestEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return nil, fmt.Errorf("%w: entry parse: %w", ErrManifestInvalid, err)
		}
		if !validFarmManifestPath(entry.Path) || (entry.Type != "f" && entry.Type != "l") ||
			(entry.Type == "l" && entry.LinkTarget == "") || (entry.Type == "f" && entry.LinkTarget != "") {
			return nil, fmt.Errorf("%w: invalid entry %q", ErrManifestInvalid, entry.Path)
		}
		fold := NormalizePath(entry.Path)
		if _, exists := manifest.Casefold[fold]; exists {
			return nil, fmt.Errorf("%w: duplicate path %q", ErrManifestInvalid, entry.Path)
		}
		manifest.Entries[entry.Path] = entry
		manifest.Casefold[fold] = entry.Path
		if len(manifest.Entries) > s.ManifestEntries {
			return nil, fmt.Errorf("%w: too many entries", ErrManifestInvalid)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%w: reading entries: %w", ErrManifestInvalid, err)
	}
	if len(manifest.Entries) != s.ManifestEntries {
		return nil, fmt.Errorf("%w: entry count mismatch", ErrManifestInvalid)
	}
	if hex.EncodeToString(h.Sum(nil)) != s.ManifestSHA256 {
		return nil, fmt.Errorf("%w: sha256 mismatch", ErrManifestInvalid)
	}
	return manifest, nil
}

func validFarmManifestPath(p string) bool {
	if p == "" || path.IsAbs(p) || path.Clean(p) != p || strings.ContainsAny(p, "\\\x00") ||
		strings.HasPrefix(p, ".gorganizer-") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == "." || part == "" {
			return false
		}
	}
	return true
}

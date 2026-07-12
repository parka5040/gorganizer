package download

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/kvfile"
)

type IndexEntry struct {
	Path        string
	ModID       int
	FileID      int
	Hidden      bool
	Uninstalled bool
}

type DownloadsIndex struct {
	Archives []IndexEntry
}

type ArchiveSidecar struct {
	ModID           int
	ModName         string
	GameDomain      string
	ThumbnailURL    string
	AdultContent    bool
	FileID          int
	FileName        string
	FileArchiveName string
	Version         string
	Category        string
	UploadedAt      string
	DownloadedAt    string
	SizeBytes       int64
}

var (
	indexMuOnce sync.Once
	indexMu     map[string]*sync.Mutex
	indexMuMap  sync.Mutex
)

func indexLock(gameID string) *sync.Mutex {
	indexMuOnce.Do(func() { indexMu = make(map[string]*sync.Mutex) })
	indexMuMap.Lock()
	defer indexMuMap.Unlock()
	m, ok := indexMu[gameID]
	if !ok {
		m = &sync.Mutex{}
		indexMu[gameID] = m
	}
	return m
}

// LoadIndex reads the per-game downloads index, returning empty on missing file.
func LoadIndex(gameID string) (*DownloadsIndex, error) {
	path := filepath.Join(config.DownloadsDir(gameID), "metadata.yaml")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &DownloadsIndex{}, nil
		}
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	idx := &DownloadsIndex{}
	var cur *IndexEntry
	sc := kvfile.NewScanner(f)
	for sc.Scan() {
		l := sc.Line()
		if l.Text == "archives:" {
			continue
		}
		if l.IsListItem {
			if cur != nil {
				idx.Archives = append(idx.Archives, *cur)
			}
			cur = &IndexEntry{}
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
		case "path":
			cur.Path = v
		case "mod_id":
			cur.ModID, _ = strconv.Atoi(v)
		case "file_id":
			cur.FileID, _ = strconv.Atoi(v)
		case "hidden":
			cur.Hidden = (v == "true")
		case "uninstalled":
			cur.Uninstalled = (v == "true")
		}
	}
	if cur != nil {
		idx.Archives = append(idx.Archives, *cur)
	}
	return idx, sc.Err()
}

// SaveIndex writes the index atomically.
func SaveIndex(gameID string, idx *DownloadsIndex) error {
	dir := config.DownloadsDir(gameID)
	if _, err := config.EnsureDir(dir); err != nil {
		return err
	}
	path := filepath.Join(dir, "metadata.yaml")

	var w kvfile.Writer
	w.Comment("Gorganizer downloads index — auto-generated")
	w.ListHeader("archives")
	for _, e := range idx.Archives {
		w.ItemQuoted("path", e.Path)
		w.ContInt("mod_id", e.ModID)
		w.ContInt("file_id", e.FileID)
		w.ContBool("hidden", e.Hidden)
		w.ContBool("uninstalled", e.Uninstalled)
	}

	return w.WriteAtomic(path, 0644)
}

// UpsertEntry adds or replaces an entry by path.
func UpsertEntry(gameID string, entry IndexEntry) error {
	mu := indexLock(gameID)
	mu.Lock()
	defer mu.Unlock()

	idx, err := LoadIndex(gameID)
	if err != nil {
		return err
	}
	replaced := false
	for i, e := range idx.Archives {
		if e.Path == entry.Path {
			idx.Archives[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		idx.Archives = append(idx.Archives, entry)
	}
	return SaveIndex(gameID, idx)
}

// RemoveEntry drops an entry by path; no-op if missing.
func RemoveEntry(gameID, relPath string) error {
	mu := indexLock(gameID)
	mu.Lock()
	defer mu.Unlock()

	idx, err := LoadIndex(gameID)
	if err != nil {
		return err
	}
	kept := idx.Archives[:0]
	for _, e := range idx.Archives {
		if e.Path != relPath {
			kept = append(kept, e)
		}
	}
	idx.Archives = kept
	return SaveIndex(gameID, idx)
}

// SetUninstalled toggles the sticky Uninstalled flag on a single index entry.
func SetUninstalled(gameID, relPath string, uninstalled bool) error {
	mu := indexLock(gameID)
	mu.Lock()
	defer mu.Unlock()

	idx, err := LoadIndex(gameID)
	if err != nil {
		return err
	}
	for i, e := range idx.Archives {
		if e.Path == relPath {
			idx.Archives[i].Uninstalled = uninstalled
			return SaveIndex(gameID, idx)
		}
	}
	return nil
}

// SetHidden toggles the hidden flag on a single entry.
func SetHidden(gameID, relPath string, hidden bool) error {
	mu := indexLock(gameID)
	mu.Lock()
	defer mu.Unlock()

	idx, err := LoadIndex(gameID)
	if err != nil {
		return err
	}
	for i, e := range idx.Archives {
		if e.Path == relPath {
			idx.Archives[i].Hidden = hidden
			return SaveIndex(gameID, idx)
		}
	}
	return fmt.Errorf("archive %q not in index", relPath)
}

// SetHiddenBulk sets the hidden flag for every entry matching pred.
func SetHiddenBulk(gameID string, hidden bool, pred func(IndexEntry) bool) error {
	mu := indexLock(gameID)
	mu.Lock()
	defer mu.Unlock()

	idx, err := LoadIndex(gameID)
	if err != nil {
		return err
	}
	for i := range idx.Archives {
		if pred(idx.Archives[i]) {
			idx.Archives[i].Hidden = hidden
		}
	}
	return SaveIndex(gameID, idx)
}

func SidecarPath(archivePath string) string {
	return archivePath + ".meta.yaml"
}

// SaveSidecar writes the per-archive metadata alongside the archive.
func SaveSidecar(archivePath string, s ArchiveSidecar, at time.Time) error {
	if s.DownloadedAt == "" {
		s.DownloadedAt = at.UTC().Format(time.RFC3339)
	}
	var w kvfile.Writer
	w.Comment("Gorganizer archive metadata — auto-generated")
	w.KVInt("mod_id", s.ModID)
	w.KVQuoted("mod_name", s.ModName)
	w.KVQuoted("game_domain", s.GameDomain)
	w.KVQuoted("thumbnail_url", s.ThumbnailURL)
	w.KVBool("adult_content", s.AdultContent)
	w.KVInt("file_id", s.FileID)
	w.KVQuoted("file_name", s.FileName)
	w.KVQuoted("file_archive_name", s.FileArchiveName)
	w.KVQuoted("version", s.Version)
	w.KVQuoted("category", s.Category)
	w.KVQuoted("uploaded_at", s.UploadedAt)
	w.KVQuoted("downloaded_at", s.DownloadedAt)
	w.KVInt64("size_bytes", s.SizeBytes)

	return w.WriteAtomic(SidecarPath(archivePath), 0644)
}

// LoadSidecar reads the per-archive .meta.yaml.
func LoadSidecar(archivePath string) (*ArchiveSidecar, error) {
	path := SidecarPath(archivePath)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	s := &ArchiveSidecar{}
	sc := kvfile.NewScanner(f)
	for sc.Scan() {
		k, v, ok := kvfile.CutKV(sc.Line().Text)
		if !ok {
			continue
		}
		v = kvfile.UnquoteValue(v)
		switch k {
		case "mod_id":
			s.ModID, _ = strconv.Atoi(v)
		case "mod_name":
			s.ModName = v
		case "game_domain":
			s.GameDomain = v
		case "thumbnail_url":
			s.ThumbnailURL = v
		case "adult_content":
			s.AdultContent = (v == "true")
		case "file_id":
			s.FileID, _ = strconv.Atoi(v)
		case "file_name":
			s.FileName = v
		case "file_archive_name":
			s.FileArchiveName = v
		case "version":
			s.Version = v
		case "category":
			s.Category = v
		case "uploaded_at":
			s.UploadedAt = v
		case "downloaded_at":
			s.DownloadedAt = v
		case "size_bytes":
			s.SizeBytes, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	return s, sc.Err()
}

// SanitizeForFolder makes a Nexus mod name safe to use as a directory name.
func SanitizeForFolder(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == ' ', r == '-', r == '_', r == '.',
			r == '(', r == ')', r == '\'':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return strings.TrimSpace(b.String())
}

// NormalizeCategory maps v1 Nexus CATEGORY_NAME to the v3 enum names.
func NormalizeCategory(v1Name string) string {
	switch strings.ToUpper(strings.TrimSpace(v1Name)) {
	case "MAIN":
		return "main"
	case "UPDATE":
		return "update"
	case "OPTIONAL":
		return "optional"
	case "OLD_VERSION", "OLD":
		return "old_version"
	case "MISCELLANEOUS", "MISC":
		return "miscellaneous"
	case "":
		return ""
	default:
		return strings.ToLower(v1Name)
	}
}

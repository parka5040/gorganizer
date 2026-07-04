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

	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
)

// IndexEntry is a single archive row in the per-game Downloads index.
type IndexEntry struct {
	Path        string
	ModID       int
	FileID      int
	Hidden      bool
	Uninstalled bool
}

// DownloadsIndex is the in-memory form of {DownloadsDir}/metadata.yaml.
type DownloadsIndex struct {
	Archives []IndexEntry
}

// ArchiveSidecar is the per-archive Nexus metadata written as <archive>.meta.yaml.
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
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		raw := scanner.Text()
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || line == "archives:" {
			continue
		}
		if strings.HasPrefix(line, "- ") {
			if cur != nil {
				idx.Archives = append(idx.Archives, *cur)
			}
			cur = &IndexEntry{}
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
	return idx, scanner.Err()
}

// SaveIndex writes the index atomically.
func SaveIndex(gameID string, idx *DownloadsIndex) error {
	dir := config.DownloadsDir(gameID)
	if _, err := config.EnsureDir(dir); err != nil {
		return err
	}
	path := filepath.Join(dir, "metadata.yaml")

	var b strings.Builder
	b.WriteString("# Gorganizer downloads index — auto-generated\n")
	b.WriteString("archives:\n")
	for _, e := range idx.Archives {
		fmt.Fprintf(&b, "  - path: %q\n", e.Path)
		fmt.Fprintf(&b, "    mod_id: %d\n", e.ModID)
		fmt.Fprintf(&b, "    file_id: %d\n", e.FileID)
		fmt.Fprintf(&b, "    hidden: %t\n", e.Hidden)
		fmt.Fprintf(&b, "    uninstalled: %t\n", e.Uninstalled)
	}

	return atomicfile.WriteFile(path, []byte(b.String()), 0644)
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
	var b strings.Builder
	b.WriteString("# Gorganizer archive metadata — auto-generated\n")
	fmt.Fprintf(&b, "mod_id: %d\n", s.ModID)
	fmt.Fprintf(&b, "mod_name: %q\n", s.ModName)
	fmt.Fprintf(&b, "game_domain: %q\n", s.GameDomain)
	fmt.Fprintf(&b, "thumbnail_url: %q\n", s.ThumbnailURL)
	fmt.Fprintf(&b, "adult_content: %t\n", s.AdultContent)
	fmt.Fprintf(&b, "file_id: %d\n", s.FileID)
	fmt.Fprintf(&b, "file_name: %q\n", s.FileName)
	fmt.Fprintf(&b, "file_archive_name: %q\n", s.FileArchiveName)
	fmt.Fprintf(&b, "version: %q\n", s.Version)
	fmt.Fprintf(&b, "category: %q\n", s.Category)
	fmt.Fprintf(&b, "uploaded_at: %q\n", s.UploadedAt)
	fmt.Fprintf(&b, "downloaded_at: %q\n", s.DownloadedAt)
	fmt.Fprintf(&b, "size_bytes: %d\n", s.SizeBytes)

	return atomicfile.WriteFile(SidecarPath(archivePath), []byte(b.String()), 0644)
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
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(strings.Trim(strings.TrimSpace(v), `"`))
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
	return s, scanner.Err()
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

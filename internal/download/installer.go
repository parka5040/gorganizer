package download

import (
	"archive/zip"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bodgit/sevenzip"

	"github.com/parka/gorganizer/internal/fsutil"
	"github.com/parka/gorganizer/internal/kvfile"
)

type Extractor interface {
	Extract(archivePath, destDir string) error
	CanHandle(archivePath string) bool
}

type ModStructure int

const (
	StructureFlat ModStructure = iota
	StructureBAIN
	StructureFOMOD
)

// DetectExtractor picks the right extractor by reading magic bytes.
func DetectExtractor(archivePath string) (Extractor, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	magic := make([]byte, 8)
	n, _ := f.Read(magic)
	if n < 2 {
		return nil, fmt.Errorf("%w: file too small", ErrUnsupportedArchive)
	}

	if magic[0] == 0x50 && magic[1] == 0x4B {
		return &ZipExtractor{}, nil
	}
	if n >= 6 && magic[0] == 0x37 && magic[1] == 0x7A && magic[2] == 0xBC &&
		magic[3] == 0xAF && magic[4] == 0x27 && magic[5] == 0x1C {
		return &SevenZipExtractor{}, nil
	}
	if n >= 4 && magic[0] == 0x52 && magic[1] == 0x61 && magic[2] == 0x72 && magic[3] == 0x21 {
		return &RarExtractor{}, nil
	}

	return nil, fmt.Errorf("%w: unrecognized magic bytes", ErrUnsupportedArchive)
}

func DetectStructure(extractDir string) ModStructure {
	if fsutil.DirExists(filepath.Join(extractDir, "fomod")) {
		return StructureFOMOD
	}
	entries, err := os.ReadDir(extractDir)
	if err != nil {
		return StructureFlat
	}
	bainCount := 0
	for _, e := range entries {
		if e.IsDir() && len(e.Name()) >= 2 && e.Name()[0] >= '0' && e.Name()[0] <= '9' {
			bainCount++
		}
	}
	if bainCount >= 2 {
		return StructureBAIN
	}
	return StructureFlat
}

var bethesdaDataSubdirs = map[string]struct{}{
	"meshes":       {},
	"textures":     {},
	"sounds":       {},
	"music":        {},
	"video":        {},
	"scripts":      {},
	"interface":    {},
	"menus":        {},
	"shaders":      {},
	"shadersfx":    {},
	"strings":      {},
	"lodsettings":  {},
	"materials":    {},
	"seq":          {},
	"grass":        {},
	"voices":       {},
	"trees":        {},
	"fonts":        {},
	"docs":         {},
	"edit scripts": {},
	"nvse":         {},
	"obse":         {},
	"skse":         {},
	"fose":         {},
	"f4se":         {},
	"sfse":         {},
}

func isBethesdaDataSubdir(name string) bool {
	_, ok := bethesdaDataSubdirs[strings.ToLower(name)]
	return ok
}

// findContentRoot returns the directory holding the mod content that overlays Data/.
func findContentRoot(extractDir string) string {
	for _, rel := range []string{
		filepath.Join("ObvData", "Data"),
		filepath.Join("Content", "Dev", "ObvData", "Data"),
		filepath.Join("OblivionRemastered", "Content", "Dev", "ObvData", "Data"),
	} {
		if nested := exclusiveNestedContentRoot(extractDir, rel); nested != "" {
			return nested
		}
	}

	entries, err := os.ReadDir(extractDir)
	if err != nil {
		return extractDir
	}

	if len(entries) == 1 && entries[0].IsDir() {
		wrapperName := entries[0].Name()
		wrapperPath := filepath.Join(extractDir, wrapperName)
		if strings.EqualFold(wrapperName, "Engine") || strings.EqualFold(wrapperName, "OblivionRemastered") {
			return extractDir
		}
		for _, rel := range []string{
			filepath.Join("ObvData", "Data"),
			filepath.Join("Content", "Dev", "ObvData", "Data"),
			filepath.Join("OblivionRemastered", "Content", "Dev", "ObvData", "Data"),
		} {
			if nested := exclusiveNestedContentRoot(wrapperPath, rel); nested != "" {
				return nested
			}
		}
		if strings.EqualFold(wrapperName, "OblivionRemastered") &&
			fsutil.DirExists(filepath.Join(wrapperPath, "Content", "Dev", "ObvData", "Data")) {
			return extractDir
		}
		if fsutil.DirExists(filepath.Join(wrapperPath, "Data")) {
			return filepath.Join(wrapperPath, "Data")
		}
		if isBethesdaDataSubdir(wrapperName) {
			return extractDir
		}
		return wrapperPath
	}
	if fsutil.DirExists(filepath.Join(extractDir, "Data")) {
		return filepath.Join(extractDir, "Data")
	}
	return extractDir
}

// exclusiveNestedContentRoot returns a populated nested root when no payload exists outside it.
func exclusiveNestedContentRoot(base, rel string) string {
	nested := resolveNestedDirectoryFold(base, rel)
	if nested == "" {
		return ""
	}
	found := false
	outside := false
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		within, relErr := filepath.Rel(nested, path)
		if relErr != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
			outside = true
			return filepath.SkipAll
		}
		found = true
		return nil
	})
	if err != nil || outside || !found {
		return ""
	}
	return nested
}

func resolveNestedDirectoryFold(base, relative string) string {
	current := base
	for _, component := range strings.Split(filepath.ToSlash(relative), "/") {
		entries, err := os.ReadDir(current)
		if err != nil {
			return ""
		}
		match := ""
		for _, entry := range entries {
			if !entry.IsDir() || !strings.EqualFold(entry.Name(), component) {
				continue
			}
			if match != "" {
				return ""
			}
			match = entry.Name()
		}
		if match == "" {
			return ""
		}
		current = filepath.Join(current, match)
	}
	return current
}

// FindContentRoot is the exported shim over findContentRoot.
func FindContentRoot(extractDir string) string { return findContentRoot(extractDir) }

func hasOblivionRemasteredRootMarkers(root string) bool {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		for _, marker := range []string{"Engine", "OblivionRemastered", "OblivionRemastered.exe"} {
			if strings.EqualFold(entry.Name(), marker) {
				return true
			}
		}
	}
	return false
}

func routeOblivionRemasteredPath(rel string, rooted bool) string {
	clean := filepath.Clean(rel)
	for _, dataPrefix := range []string{
		filepath.Join("OblivionRemastered", "Content", "Dev", "ObvData", "Data"),
		filepath.Join("Content", "Dev", "ObvData", "Data"),
		filepath.Join("ObvData", "Data"),
	} {
		if strings.EqualFold(clean, dataPrefix) {
			return "."
		}
		if suffix, ok := trimPathPrefixFold(clean, dataPrefix); ok {
			return suffix
		}
	}
	first := clean
	if separator := strings.IndexRune(clean, filepath.Separator); separator >= 0 {
		first = clean[:separator]
	}
	if strings.EqualFold(first, "Engine") || strings.EqualFold(first, "OblivionRemastered") ||
		strings.EqualFold(clean, "OblivionRemastered.exe") {
		return filepath.Join(".gorganizer-root", clean)
	}
	_ = rooted
	return clean
}

func trimPathPrefixFold(path, prefix string) (string, bool) {
	pathParts := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
	prefixParts := strings.Split(filepath.ToSlash(filepath.Clean(prefix)), "/")
	if len(pathParts) <= len(prefixParts) {
		return "", false
	}
	for index := range prefixParts {
		if !strings.EqualFold(pathParts[index], prefixParts[index]) {
			return "", false
		}
	}
	return filepath.FromSlash(strings.Join(pathParts[len(prefixParts):], "/")), true
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

type FomodKind int

const (
	FomodKindNone FomodKind = iota
	FomodKindModuleConfig
	FomodKindLegacyInfoOnly
)

// HasFomodInstaller returns true when the extracted archive contains a FOMOD.
func HasFomodInstaller(extractDir string) bool {
	root, _ := FindFomodRootKind(extractDir)
	return root != ""
}

// FindFomodRoot returns the absolute path to the FOMOD project root, or "".
func FindFomodRoot(extractDir string) string {
	root, _ := FindFomodRootKind(extractDir)
	return root
}

// FindFomodRootKind walks up to 3 levels deep looking for a fomod/ subdir.
func FindFomodRootKind(extractDir string) (string, FomodKind) {
	if root, kind := checkFomodAt(extractDir); kind != FomodKindNone {
		return root, kind
	}
	level1, _ := os.ReadDir(extractDir)
	for _, d1 := range level1 {
		if !d1.IsDir() {
			continue
		}
		l1Path := filepath.Join(extractDir, d1.Name())
		if root, kind := checkFomodAt(l1Path); kind != FomodKindNone {
			return root, kind
		}
		level2, _ := os.ReadDir(l1Path)
		for _, d2 := range level2 {
			if !d2.IsDir() {
				continue
			}
			l2Path := filepath.Join(l1Path, d2.Name())
			if root, kind := checkFomodAt(l2Path); kind != FomodKindNone {
				return root, kind
			}
		}
	}
	return "", FomodKindNone
}

func checkFomodAt(dir string) (string, FomodKind) {
	fomodDir, err := findCaseInsensitiveChild(dir, "fomod")
	if err != nil || fomodDir == "" {
		return "", FomodKindNone
	}
	if cfg, err := findCaseInsensitiveChild(fomodDir, "moduleconfig.xml"); err == nil && cfg != "" {
		return dir, FomodKindModuleConfig
	}
	if info, err := findCaseInsensitiveChild(fomodDir, "info.xml"); err == nil && info != "" {
		return dir, FomodKindLegacyInfoOnly
	}
	return "", FomodKindNone
}

// ExpandNestedFomods extracts any *.fomod archives found up to two levels deep.
func ExpandNestedFomods(extractDir string) {
	visit := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(strings.ToLower(name), ".fomod") {
				continue
			}
			srcPath := filepath.Join(dir, name)
			outDir := filepath.Join(dir, strings.TrimSuffix(name, filepath.Ext(name)))
			if err := os.MkdirAll(outDir, 0755); err != nil {
				slog.Warn("ExpandNestedFomods: mkdir failed", "path", outDir, "err", err)
				continue
			}
			ex := []Extractor{&SevenZipExtractor{}, &ZipExtractor{}}
			extracted := false
			for _, x := range ex {
				if err := x.Extract(srcPath, outDir); err == nil {
					extracted = true
					break
				}
			}
			if !extracted {
				slog.Warn("ExpandNestedFomods: could not extract nested .fomod", "path", srcPath)
				_ = os.RemoveAll(outDir)
				continue
			}
			_ = os.Remove(srcPath)
		}
	}
	visit(extractDir)
	level1, _ := os.ReadDir(extractDir)
	for _, d1 := range level1 {
		if d1.IsDir() {
			visit(filepath.Join(extractDir, d1.Name()))
		}
	}
}

func findCaseInsensitiveChild(parent, target string) (string, error) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return "", err
	}
	target = strings.ToLower(target)
	for _, e := range entries {
		if strings.ToLower(e.Name()) == target {
			return filepath.Join(parent, e.Name()), nil
		}
	}
	return "", nil
}

// ClearModFiles removes everything under modDir except metadata.yaml.
func ClearModFiles(modDir string) error {
	entries, err := os.ReadDir(modDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == "metadata.yaml" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(modDir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

type SourceArchiveRef struct {
	Path        string
	ModID       int
	FileID      int
	InstalledAt string
	Merged      bool
}

type ModMetadata struct {
	Name           string
	Folder         string
	Installed      string
	Category       string
	Version        string
	Enabled        bool
	FileCount      int
	ModPage        string
	TrueIndex      string
	VisualIndex    string
	Separator      string
	SourceArchives []SourceArchiveRef
	Files          []string
}

// LoadModMetadata reads {modDir}/metadata.yaml; missing file returns zero value.
func LoadModMetadata(modDir string) (*ModMetadata, error) {
	path := filepath.Join(modDir, "metadata.yaml")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ModMetadata{}, nil
		}
		return nil, err
	}
	defer f.Close()

	m := &ModMetadata{}
	var (
		inSourceList bool
		inFilesList  bool
		curArchive   *SourceArchiveRef
	)

	sc := kvfile.NewScanner(f)
	for sc.Scan() {
		l := sc.Line()
		if strings.HasSuffix(l.Text, ":") && !strings.HasPrefix(l.Raw, " ") {
			if curArchive != nil {
				m.SourceArchives = append(m.SourceArchives, *curArchive)
				curArchive = nil
			}
			switch strings.TrimSuffix(l.Text, ":") {
			case "source_archives":
				inSourceList, inFilesList = true, false
			case "files":
				inSourceList, inFilesList = false, true
			default:
				inSourceList, inFilesList = false, false
			}
			continue
		}
		if inSourceList && l.IsListItem {
			if curArchive != nil {
				m.SourceArchives = append(m.SourceArchives, *curArchive)
			}
			curArchive = &SourceArchiveRef{}
		}
		if inSourceList && curArchive != nil {
			k, v, ok := kvfile.CutKV(l.Item)
			if ok {
				v = kvfile.UnquoteValue(v)
				switch k {
				case "path":
					curArchive.Path = v
				case "mod_id":
					fmt.Sscanf(v, "%d", &curArchive.ModID)
				case "file_id":
					fmt.Sscanf(v, "%d", &curArchive.FileID)
				case "installed_at":
					curArchive.InstalledAt = v
				case "merged":
					curArchive.Merged = (v == "true")
				}
			}
			continue
		}
		if inFilesList && l.IsListItem {
			m.Files = append(m.Files, kvfile.UnquoteItem(l.Item))
			continue
		}

		k, v, ok := kvfile.CutKV(l.Text)
		if !ok {
			continue
		}
		v = kvfile.UnquoteValue(v)
		switch k {
		case "name":
			m.Name = v
		case "folder":
			m.Folder = v
		case "installed":
			m.Installed = v
		case "category":
			m.Category = v
		case "version":
			m.Version = v
		case "enabled":
			m.Enabled = (v == "true")
		case "file_count":
			fmt.Sscanf(v, "%d", &m.FileCount)
		case "mod_page":
			m.ModPage = v
		case "true_index":
			m.TrueIndex = v
		case "visual_index":
			m.VisualIndex = v
		case "separator":
			m.Separator = v
		case "source_archive":
			if v != "" {
				m.SourceArchives = append(m.SourceArchives, SourceArchiveRef{Path: v})
			}
		}
	}
	if curArchive != nil {
		m.SourceArchives = append(m.SourceArchives, *curArchive)
	}
	return m, sc.Err()
}

func SaveModMetadata(modDir string, m *ModMetadata) error {
	var w kvfile.Writer
	w.Comment("Gorganizer mod metadata — auto-generated")
	w.KVQuoted("name", m.Name)
	w.KVQuoted("folder", m.Folder)
	w.KVQuoted("installed", m.Installed)
	w.KVQuoted("category", m.Category)
	w.KVQuoted("version", m.Version)
	w.KVBool("enabled", m.Enabled)
	w.KVInt("file_count", m.FileCount)
	if m.ModPage != "" {
		w.KVQuoted("mod_page", m.ModPage)
	}
	if m.TrueIndex != "" {
		w.KVQuoted("true_index", m.TrueIndex)
	}
	if m.VisualIndex != "" {
		w.KVQuoted("visual_index", m.VisualIndex)
	}
	if m.Separator != "" {
		w.KVQuoted("separator", m.Separator)
	}
	w.ListHeader("source_archives")
	for _, s := range m.SourceArchives {
		w.ItemQuoted("path", s.Path)
		w.ContInt("mod_id", s.ModID)
		w.ContInt("file_id", s.FileID)
		w.ContQuoted("installed_at", s.InstalledAt)
		if s.Merged {
			w.ContBool("merged", true)
		}
	}
	w.ListHeader("files")
	for _, f := range m.Files {
		w.ItemString(f)
	}
	return w.WriteAtomic(filepath.Join(modDir, "metadata.yaml"), 0644)
}

// AppendSourceArchive adds an archive ref and merges newFiles into the files list.
func AppendSourceArchive(modDir, modName string, ref SourceArchiveRef, displayName, category, version, modPage string, newFiles []string) error {
	m, err := LoadModMetadata(modDir)
	if err != nil {
		return err
	}
	if m.Folder == "" {
		m.Folder = modName
	}
	if m.Name == "" {
		if displayName != "" {
			m.Name = displayName
		} else {
			m.Name = modName
		}
	}
	if m.Installed == "" {
		m.Installed = time.Now().UTC().Format(time.RFC3339)
	}
	if m.Category == "" {
		m.Category = category
	}
	if m.Version == "" {
		m.Version = version
	}
	if m.ModPage == "" && modPage != "" {
		m.ModPage = modPage
	}
	if ref.InstalledAt == "" {
		ref.InstalledAt = time.Now().UTC().Format(time.RFC3339)
	}
	m.SourceArchives = append(m.SourceArchives, ref)

	seen := make(map[string]struct{}, len(m.Files)+len(newFiles))
	merged := make([]string, 0, len(m.Files)+len(newFiles))
	for _, f := range m.Files {
		if _, dup := seen[f]; dup {
			continue
		}
		seen[f] = struct{}{}
		merged = append(merged, f)
	}
	for _, f := range newFiles {
		if f == "" || f == "metadata.yaml" {
			continue
		}
		if _, dup := seen[f]; dup {
			continue
		}
		seen[f] = struct{}{}
		merged = append(merged, f)
	}
	m.Files = merged
	m.FileCount = len(m.Files)

	return SaveModMetadata(modDir, m)
}

type ZipExtractor struct{}

func (e *ZipExtractor) CanHandle(archivePath string) bool {
	return strings.HasSuffix(strings.ToLower(archivePath), ".zip")
}

func (e *ZipExtractor) Extract(archivePath, destDir string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("opening zip: %w", err)
	}
	defer r.Close()

	for _, f := range r.File {
		destPath := filepath.Join(destDir, f.Name)
		if !strings.HasPrefix(filepath.Clean(destPath), filepath.Clean(destDir)+string(os.PathSeparator)) {
			continue
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(destPath, 0755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(destPath)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

type SevenZipExtractor struct{}

func (e *SevenZipExtractor) CanHandle(archivePath string) bool {
	return strings.HasSuffix(strings.ToLower(archivePath), ".7z")
}

func (e *SevenZipExtractor) Extract(archivePath, destDir string) error {
	r, err := sevenzip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("opening 7z: %w", err)
	}
	defer r.Close()

	for _, f := range r.File {
		destPath := filepath.Join(destDir, f.Name)
		if !strings.HasPrefix(filepath.Clean(destPath), filepath.Clean(destDir)+string(os.PathSeparator)) {
			continue
		}
		if f.FileInfo().IsDir() {
			os.MkdirAll(destPath, 0755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(destPath)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

type RarExtractor struct{}

func (e *RarExtractor) CanHandle(archivePath string) bool {
	return strings.HasSuffix(strings.ToLower(archivePath), ".rar")
}

func (e *RarExtractor) Extract(archivePath, destDir string) error {
	cmd := exec.Command("unrar", "x", "-o+", archivePath, destDir+"/")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		slog.Warn("unrar failed, trying 7z fallback", "err", err)
		cmd = exec.Command("7z", "x", "-o"+destDir, "-y", archivePath)
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		return cmd.Run()
	}
	return nil
}

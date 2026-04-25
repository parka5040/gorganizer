package download

import (
	"archive/zip"
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bodgit/sevenzip"
)

// Extractor abstracts archive extraction. Interface Segregation: each format
// has its own implementation. Open/Closed: new formats can be added without
// modifying existing code.
type Extractor interface {
	Extract(archivePath, destDir string) error
	CanHandle(archivePath string) bool
}

// ModStructure represents the detected layout of an extracted archive.
type ModStructure int

const (
	StructureFlat  ModStructure = iota // files at root or in a single top-level dir
	StructureBAIN                      // numbered directories (00 Core, 01 Optional)
	StructureFOMOD                     // fomod/ directory present
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

	// Zip: PK (0x50 0x4B)
	if magic[0] == 0x50 && magic[1] == 0x4B {
		return &ZipExtractor{}, nil
	}
	// 7z: 0x37 0x7A 0xBC 0xAF 0x27 0x1C
	if n >= 6 && magic[0] == 0x37 && magic[1] == 0x7A && magic[2] == 0xBC &&
		magic[3] == 0xAF && magic[4] == 0x27 && magic[5] == 0x1C {
		return &SevenZipExtractor{}, nil
	}
	// RAR: Rar! (0x52 0x61 0x72 0x21)
	if n >= 4 && magic[0] == 0x52 && magic[1] == 0x61 && magic[2] == 0x72 && magic[3] == 0x21 {
		return &RarExtractor{}, nil
	}

	return nil, fmt.Errorf("%w: unrecognized magic bytes", ErrUnsupportedArchive)
}

// DetectStructure examines an extracted directory to determine its layout.
func DetectStructure(extractDir string) ModStructure {
	if dirExists(filepath.Join(extractDir, "fomod")) {
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

// bethesdaDataSubdirs is the set of directory names that, when seen as the
// SOLE top-level entry in an extracted archive, must NOT be stripped as a
// "ModName/" wrapper — they're game-canonical Data/ subdirectories whose
// presence is meaningful (e.g. xNVSE plugins live in Data/NVSE/Plugins/, so
// stripping `nvse/` gives us `plugins/foo.dll` at the mod root and the
// engine never finds it).
//
// Match is case-insensitive to handle both `NVSE/` and `nvse/`. This list
// covers Bethesda titles from Morrowind through Starfield + their
// script-extender + xEdit conventions.
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
	"edit scripts": {}, // xEdit
	"nvse":         {}, // Fallout NV
	"obse":         {}, // Oblivion
	"skse":         {}, // Skyrim LE/SSE
	"fose":         {}, // Fallout 3
	"f4se":         {}, // Fallout 4
	"sfse":         {}, // Starfield
}

func isBethesdaDataSubdir(name string) bool {
	_, ok := bethesdaDataSubdirs[strings.ToLower(name)]
	return ok
}

// findContentRoot determines which directory inside the extracted archive
// contains the actual mod content that should go into the game's Data/ folder.
//
// Handles common mod archive layouts:
//  1. Archive contains Data/ folder directly → use contents of Data/
//  2. Archive has single wrapper dir containing Data/ → use contents of that Data/
//  3. Archive has single wrapper dir whose name is a known Data/ subdir
//     (e.g. NVSE/, meshes/) → archive root IS the data; do NOT strip
//  4. Archive has single wrapper dir (other name, no Data/) → use contents of wrapper
//  5. Archive root has game files directly → use archive root
func findContentRoot(extractDir string) string {
	entries, err := os.ReadDir(extractDir)
	if err != nil {
		return extractDir
	}

	if len(entries) == 1 && entries[0].IsDir() {
		wrapperName := entries[0].Name()
		wrapperPath := filepath.Join(extractDir, wrapperName)
		if dirExists(filepath.Join(wrapperPath, "Data")) {
			return filepath.Join(wrapperPath, "Data")
		}
		// Don't strip a known Data/ subdir — it's content, not a wrapper.
		if isBethesdaDataSubdir(wrapperName) {
			return extractDir
		}
		return wrapperPath
	}
	if dirExists(filepath.Join(extractDir, "Data")) {
		return filepath.Join(extractDir, "Data")
	}
	return extractDir
}

// FindContentRoot is the exported shim over findContentRoot for callers
// outside the download package (e.g. daemon.PreviewInstall, which walks
// the preview tree to produce a flat file list).
func FindContentRoot(extractDir string) string { return findContentRoot(extractDir) }

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// copyFile copies src → dst (creating the parent dir as needed). Used
// internally by the install path.
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

// FomodKind distinguishes a modern XML-driven FOMOD (ModuleConfig.xml, full
// wizard) from a legacy NMM-style installer (fomod/info.xml only, often paired
// with a C# script we deliberately do NOT execute).
type FomodKind int

const (
	FomodKindNone FomodKind = iota
	FomodKindModuleConfig
	FomodKindLegacyInfoOnly
)

// HasFomodInstaller returns true when the extracted archive root contains a
// fomod/ directory with either ModuleConfig.xml (modern wizard) or info.xml
// (legacy NMM-style — surfaced as an info-only popup).
func HasFomodInstaller(extractDir string) bool {
	root, _ := FindFomodRootKind(extractDir)
	return root != ""
}

// FindFomodRoot returns the absolute path to the FOMOD project root (the
// directory that contains the fomod/ subdir), or "" if none is found. Kept
// for callers that don't care about the kind.
func FindFomodRoot(extractDir string) string {
	root, _ := FindFomodRootKind(extractDir)
	return root
}

// FindFomodRootKind walks up to 3 levels deep looking for a fomod/ subdir
// containing ModuleConfig.xml (preferred) or info.xml (legacy fallback).
// Returns ("", FomodKindNone) when nothing matches.
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

// ExpandNestedFomods finds *.fomod files (an NMM convention: a 7z- or
// zip-encoded archive nested inside the outer download) anywhere within
// extractDir up to two levels deep, extracts each in place into a sibling
// directory of the same base name, and removes the original .fomod file.
// This is what unblocks legacy mods like Mod Configuration Menu, whose
// outer 7z carries a single MyMod.fomod containing the actual fomod/ tree.
//
// Errors are logged and skipped per-file: a corrupted nested archive
// shouldn't fail the whole install.
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
			// Try 7z first (most common), then zip. We don't trust the
			// extension alone — .fomod is just a convention.
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

// ClearModFiles removes every file and directory under modDir except the
// metadata.yaml at the root. Used by ReinstallMod before replaying archives.
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

// --- Mod metadata.yaml reader/writer ---

// SourceArchiveRef is one entry in the mod's source_archives list.
type SourceArchiveRef struct {
	Path        string
	ModID       int
	FileID      int
	InstalledAt string // ISO 8601
	// Merged is true when this archive was installed via "Merge Into Existing
	// Mod..." into a target that already had at least one prior archive.
	// Distinguishes downstream UI ("Merged" status + "Show Containing Mod")
	// from a fresh install. Serialized only when true to keep older
	// metadata.yaml files round-trippable.
	Merged bool
}

// ModMetadata is the flat form of a mod's metadata.yaml.
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

// LoadModMetadata reads {modDir}/metadata.yaml. Missing file → zero-value
// metadata + nil error.
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

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		raw := scanner.Text()
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasSuffix(line, ":") && !strings.HasPrefix(raw, " ") {
			if curArchive != nil {
				m.SourceArchives = append(m.SourceArchives, *curArchive)
				curArchive = nil
			}
			switch strings.TrimSuffix(line, ":") {
			case "source_archives":
				inSourceList, inFilesList = true, false
			case "files":
				inSourceList, inFilesList = false, true
			default:
				inSourceList, inFilesList = false, false
			}
			continue
		}
		if inSourceList && strings.HasPrefix(line, "- ") {
			if curArchive != nil {
				m.SourceArchives = append(m.SourceArchives, *curArchive)
			}
			curArchive = &SourceArchiveRef{}
			line = strings.TrimPrefix(line, "- ")
		}
		if inSourceList && curArchive != nil {
			k, v, ok := strings.Cut(line, ":")
			if ok {
				v = strings.TrimSpace(strings.Trim(strings.TrimSpace(v), `"`))
				switch strings.TrimSpace(k) {
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
		if inFilesList && strings.HasPrefix(line, "- ") {
			m.Files = append(m.Files, strings.Trim(strings.TrimPrefix(line, "- "), `"`))
			continue
		}

		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(strings.Trim(strings.TrimSpace(v), `"`))
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
	return m, scanner.Err()
}

// SaveModMetadata writes {modDir}/metadata.yaml.
func SaveModMetadata(modDir string, m *ModMetadata) error {
	var b strings.Builder
	b.WriteString("# Gorganizer mod metadata — auto-generated\n")
	fmt.Fprintf(&b, "name: %q\n", m.Name)
	fmt.Fprintf(&b, "folder: %q\n", m.Folder)
	fmt.Fprintf(&b, "installed: %q\n", m.Installed)
	fmt.Fprintf(&b, "category: %q\n", m.Category)
	fmt.Fprintf(&b, "version: %q\n", m.Version)
	fmt.Fprintf(&b, "enabled: %t\n", m.Enabled)
	fmt.Fprintf(&b, "file_count: %d\n", m.FileCount)
	if m.ModPage != "" {
		fmt.Fprintf(&b, "mod_page: %q\n", m.ModPage)
	}
	if m.TrueIndex != "" {
		fmt.Fprintf(&b, "true_index: %q\n", m.TrueIndex)
	}
	if m.VisualIndex != "" {
		fmt.Fprintf(&b, "visual_index: %q\n", m.VisualIndex)
	}
	if m.Separator != "" {
		fmt.Fprintf(&b, "separator: %q\n", m.Separator)
	}
	b.WriteString("source_archives:\n")
	for _, s := range m.SourceArchives {
		fmt.Fprintf(&b, "  - path: %q\n", s.Path)
		fmt.Fprintf(&b, "    mod_id: %d\n", s.ModID)
		fmt.Fprintf(&b, "    file_id: %d\n", s.FileID)
		fmt.Fprintf(&b, "    installed_at: %q\n", s.InstalledAt)
		if s.Merged {
			b.WriteString("    merged: true\n")
		}
	}
	b.WriteString("files:\n")
	for _, f := range m.Files {
		fmt.Fprintf(&b, "  - %q\n", f)
	}
	return os.WriteFile(filepath.Join(modDir, "metadata.yaml"), []byte(b.String()), 0644)
}

// AppendSourceArchive adds one archive reference to a mod's source_archives
// list and merges `newFiles` into the files list (deduplicated). Creates
// the metadata.yaml if absent.
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

// --- Extractor implementations ---

// ZipExtractor uses the Go stdlib archive/zip.
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

// SevenZipExtractor uses github.com/bodgit/sevenzip.
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

// RarExtractor shells out to the unrar CLI.
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

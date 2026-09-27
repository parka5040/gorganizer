package daemon

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/fsutil"
)

type archiveIdentity struct {
	dev   uint64
	ino   uint64
	size  int64
	mtime int64
}

// resolveExternalArchive verifies an absolute regular archive and records its resolved filesystem identity.
func resolveExternalArchive(path string) (string, archiveIdentity, error) {
	if !filepath.IsAbs(path) {
		return "", archiveIdentity{}, &UnsafePathError{Field: "external_archive_path"}
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", archiveIdentity{}, fmt.Errorf("resolving external archive: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", archiveIdentity{}, fmt.Errorf("checking external archive: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", archiveIdentity{}, &UnsafePathError{Field: "external_archive_path"}
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", archiveIdentity{}, fmt.Errorf("checking external archive identity: unsupported filesystem")
	}
	return resolved, archiveIdentity{dev: uint64(stat.Dev), ino: stat.Ino, size: info.Size(), mtime: info.ModTime().UnixNano()}, nil
}

// selectableContentRoots lists visible directories through two levels and includes the extraction root.
func selectableContentRoots(root string) ([]string, error) {
	roots := []string{""}
	var visit func(string, int) error
	visit = func(rel string, depth int) error {
		parent := filepath.Join(root, rel)
		entries, err := os.ReadDir(parent)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			child := filepath.ToSlash(filepath.Join(rel, entry.Name()))
			roots = append(roots, child)
			if depth < 2 {
				if err := visit(child, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit("", 1); err != nil {
		return nil, fmt.Errorf("listing archive directories: %w", err)
	}
	sort.Strings(roots)
	if len(roots) > 500 {
		roots = roots[:500]
	}
	return roots, nil
}

// caseInsensitivePreviewChild locates a direct extracted child without following directory symlinks.
func caseInsensitivePreviewChild(parent, name string) (string, error) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), name) {
			return filepath.Join(parent, entry.Name()), nil
		}
	}
	return "", os.ErrNotExist
}

// moduleConfigBytes reads the bounded regular ModuleConfig file of a discovered FOMOD.
func moduleConfigBytes(moduleRoot string) ([]byte, error) {
	reject := &download.ArchiveRejectedError{Reason: download.ArchiveRejectedLimit, Detail: "ModuleConfig.xml"}
	fomod, err := caseInsensitivePreviewChild(moduleRoot, "fomod")
	if err != nil {
		return nil, reject
	}
	path, err := caseInsensitivePreviewChild(fomod, "ModuleConfig.xml")
	if err != nil {
		return nil, reject
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, reject
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !fsutil.ContainedBy(moduleRoot, resolved) {
		return nil, reject
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, fmt.Errorf("reading ModuleConfig.xml: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return nil, fmt.Errorf("reading ModuleConfig.xml: %w", err)
	}
	if len(data) > 1<<20 {
		return nil, reject
	}
	return data, nil
}

// previewScreenshotBytes reads an image only when its resolved regular file is within the private extraction.
func previewScreenshotBytes(extractRoot, moduleRoot, path string) []byte {
	if path == "" {
		return nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(moduleRoot, filepath.FromSlash(strings.ReplaceAll(path, `\`, `/`)))
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".bmp", ".webp":
	default:
		return nil
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !fsutil.ContainedBy(extractRoot, resolved) {
		return nil
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 2<<20 {
		return nil
	}
	data, err := os.ReadFile(resolved)
	if err != nil || len(data) > 2<<20 {
		return nil
	}
	return data
}

// requiredFomodFiles extracts the mandatory file and folder operations from the ModuleConfig payload.
func requiredFomodFiles(data []byte) []dto.FomodFileResult {
	var doc struct {
		Required struct {
			Files []struct {
				Source      string `xml:"source,attr"`
				Destination string `xml:"destination,attr"`
				Priority    int32  `xml:"priority,attr"`
			} `xml:"file"`
			Folders []struct {
				Source      string `xml:"source,attr"`
				Destination string `xml:"destination,attr"`
				Priority    int32  `xml:"priority,attr"`
			} `xml:"folder"`
		} `xml:"requiredInstallFiles"`
	}
	if xml.Unmarshal(data, &doc) != nil {
		return nil
	}
	var files []dto.FomodFileResult
	for _, file := range doc.Required.Files {
		if file.Source != "" {
			files = append(files, dto.FomodFileResult{Source: file.Source, Destination: file.Destination, Priority: file.Priority})
		}
	}
	for _, folder := range doc.Required.Folders {
		if folder.Source != "" {
			files = append(files, dto.FomodFileResult{Source: folder.Source, Destination: folder.Destination, IsFolder: true, Priority: folder.Priority})
		}
	}
	return files
}

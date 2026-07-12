package download

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

type InstallMode int

const (
	ModeNewMod       InstallMode = 0
	ModeMergeIntoMod InstallMode = 1
)

type InstallProgress struct {
	InstallID   string
	Step        InstallStage
	Pct         int32
	CurrentFile string
	FilesDone   int64
	FilesTotal  int64
	Error       string
}

type ProgressSink func(InstallProgress)

type InstallRequest struct {
	GameID             string
	ArchivePath        string
	ExtractedRoot      string
	Mode               InstallMode
	TargetMod          string
	SourceArchiveRef   SourceArchiveRef
	DisplayName        string
	Category           string
	Version            string
	ModPage            string
	FomodSelectedFiles []FomodFile
	ProgressSink       ProgressSink
	InstallID          string
}

type InstallResult struct {
	ModFolder string
	FileCount int
	Files     []string
	InstallID string
}

type FomodFile struct {
	Source      string
	Destination string
	IsFolder    bool
	Priority    int32
}

type InstallStage int

const (
	StageExtracting InstallStage = 1
	StageCopying    InstallStage = 2
	StageFinalizing InstallStage = 3
	StageComplete   InstallStage = 4
	StageFailed     InstallStage = 5
)

// Install is the canonical install path: extract, optionally apply FOMOD selection, stage, rename, write metadata.
func Install(req InstallRequest) (*InstallResult, error) {
	if req.InstallID == "" {
		req.InstallID = "inst-" + uuid.NewString()
	}
	emit := func(p InstallProgress) {
		p.InstallID = req.InstallID
		if req.ProgressSink != nil {
			req.ProgressSink(p)
		}
	}

	modsDir := modsDirFor(req.GameID)
	if modsDir == "" {
		return nil, fmt.Errorf("no mods dir for game %q", req.GameID)
	}
	if req.TargetMod == "" {
		return nil, fmt.Errorf("target mod name required")
	}
	finalDir := filepath.Join(modsDir, req.TargetMod)

	extractRoot := req.ExtractedRoot
	var extractTmp string
	if extractRoot == "" {
		if req.ArchivePath == "" {
			return nil, fmt.Errorf("neither ArchivePath nor ExtractedRoot provided")
		}
		extractor, err := DetectExtractor(req.ArchivePath)
		if err != nil {
			return nil, fmt.Errorf("detecting archive type: %w", err)
		}
		tmp, err := os.MkdirTemp("", "gorganizer-install-*")
		if err != nil {
			return nil, fmt.Errorf("creating temp dir: %w", err)
		}
		extractTmp = tmp
		extractRoot = tmp
		emit(InstallProgress{Step: StageExtracting, Pct: -1})
		if err := extractor.Extract(req.ArchivePath, tmp); err != nil {
			os.RemoveAll(tmp)
			return nil, fmt.Errorf("extracting: %w", err)
		}
		ExpandNestedFomods(tmp)
	}
	if extractTmp != "" {
		defer os.RemoveAll(extractTmp)
	}

	if len(req.FomodSelectedFiles) == 0 && HasFomodInstaller(extractRoot) {
		return nil, &installFomodMarker{Path: req.ArchivePath}
	}

	stageDir, err := os.MkdirTemp(modsDir, ".stage-")
	if err != nil {
		return nil, fmt.Errorf("creating stage dir: %w", err)
	}
	stageCleanup := true
	defer func() {
		if stageCleanup {
			os.RemoveAll(stageDir)
		}
	}()

	emit(InstallProgress{Step: StageCopying, Pct: 0})

	var written []string
	if len(req.FomodSelectedFiles) > 0 {
		written, err = copyFomodSelection(req.GameID, extractRoot, stageDir, req.FomodSelectedFiles, req.InstallID, req.ProgressSink)
	} else {
		written, err = copyFlatten(req.GameID, extractRoot, stageDir, req.InstallID, req.ProgressSink)
	}
	if err != nil {
		emit(InstallProgress{Step: StageFailed, Error: err.Error()})
		return nil, err
	}

	emit(InstallProgress{Step: StageFinalizing, Pct: 100})

	switch req.Mode {
	case ModeNewMod:
		if _, statErr := os.Stat(finalDir); statErr == nil {
			return nil, &installCollisionMarker{Name: req.TargetMod}
		}
		if err := os.Rename(stageDir, finalDir); err != nil {
			return nil, fmt.Errorf("moving stage → %s: %w", finalDir, err)
		}
		stageCleanup = false
	case ModeMergeIntoMod:
		if err := os.MkdirAll(finalDir, 0755); err != nil {
			return nil, fmt.Errorf("ensuring merge target: %w", err)
		}
		if err := mergeTree(stageDir, finalDir); err != nil {
			return nil, fmt.Errorf("merging into %s: %w", finalDir, err)
		}
	default:
		return nil, fmt.Errorf("unknown install mode %d", req.Mode)
	}

	ref := req.SourceArchiveRef
	if ref.InstalledAt == "" {
		ref.InstalledAt = time.Now().UTC().Format(time.RFC3339)
	}
	if req.Mode == ModeMergeIntoMod {
		if existing, lerr := LoadModMetadata(finalDir); lerr == nil && existing != nil && len(existing.SourceArchives) > 0 {
			ref.Merged = true
		}
	}
	if err := AppendSourceArchive(
		finalDir, req.TargetMod, ref,
		req.DisplayName, req.Category, req.Version, req.ModPage, written,
	); err != nil {
		slog.Warn("updating mod metadata failed", "err", err)
	}

	relFromDownloads := strings.TrimPrefix(ref.Path, "Downloads/")
	if relFromDownloads != ref.Path {
		_ = SetUninstalled(req.GameID, relFromDownloads, false)
	}

	final, _ := LoadModMetadata(finalDir)
	fileCount := 0
	if final != nil {
		fileCount = final.FileCount
	}
	emit(InstallProgress{Step: StageComplete, Pct: 100, FilesDone: int64(fileCount), FilesTotal: int64(fileCount)})

	return &InstallResult{
		ModFolder: req.TargetMod,
		FileCount: fileCount,
		Files:     written,
		InstallID: req.InstallID,
	}, nil
}

type installFomodMarker struct{ Path string }

func (e *installFomodMarker) Error() string {
	return fmt.Sprintf("FOMOD required: %s", e.Path)
}

// IsFomodMarker extracts the archive path from a fomod-required marker.
func IsFomodMarker(err error) (string, bool) {
	if e, ok := err.(*installFomodMarker); ok {
		return e.Path, true
	}
	return "", false
}

type installCollisionMarker struct{ Name string }

func (e *installCollisionMarker) Error() string {
	return fmt.Sprintf("mod %q already exists", e.Name)
}

// IsCollisionMarker extracts the colliding mod name from a collision marker.
func IsCollisionMarker(err error) (string, bool) {
	if e, ok := err.(*installCollisionMarker); ok {
		return e.Name, true
	}
	return "", false
}

// copyFlatten replays the archive's content root into stage.
func copyFlatten(gameID, extractRoot, stageDir, installID string, sink ProgressSink) ([]string, error) {
	contentRoot := findContentRoot(extractRoot)
	rootedOblivionRemastered := gameID == "oblivionremastered" && hasOblivionRemasteredRootMarkers(contentRoot)
	resolvedContentRoot, err := filepath.EvalSymlinks(contentRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving archive content root: %w", err)
	}

	var written []string
	err = filepath.WalkDir(contentRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(contentRoot, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		installRel := routeOblivionRemasteredPath(rel, rootedOblivionRemastered)
		dst := filepath.Join(stageDir, installRel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0755)
		}
		copySource := path
		if d.Type()&os.ModeSymlink != 0 {
			copySource, err = filepath.EvalSymlinks(path)
			if err != nil || !pathContainedBy(resolvedContentRoot, copySource) {
				return fmt.Errorf("archive symlink %q resolves outside its content root", rel)
			}
		}
		info, err := os.Stat(copySource)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("archive contains unsupported special file %q", rel)
		}
		if err := copyFile(copySource, dst); err != nil {
			return err
		}
		written = append(written, installRel)
		if sink != nil && len(written)%32 == 0 {
			sink(InstallProgress{
				InstallID:   installID,
				Step:        StageCopying,
				Pct:         -1,
				CurrentFile: rel,
				FilesDone:   int64(len(written)),
			})
		}
		return nil
	})
	return written, err
}

// copyFomodSelection applies a FOMOD plugin's file/folder rules.
func copyFomodSelection(gameID, extractRoot, stageDir string, files []FomodFile, installID string, sink ProgressSink) ([]string, error) {
	var written []string
	for _, f := range files {
		src, err := containedInstallPath(extractRoot, f.Source, false)
		if err != nil {
			return written, fmt.Errorf("unsafe FOMOD source %q: %w", f.Source, err)
		}
		destRel := f.Destination
		if destRel == "" {
			destRel = f.Source
		}
		if gameID == "oblivionremastered" {
			destRel = routeOblivionRemasteredPath(filepath.FromSlash(strings.ReplaceAll(destRel, `\`, `/`)), hasOblivionRemasteredRootMarkers(extractRoot))
		}
		destRoot, err := containedInstallPath(stageDir, destRel, true)
		if err != nil {
			return written, fmt.Errorf("unsafe FOMOD destination %q: %w", f.Destination, err)
		}
		info, err := os.Stat(src)
		if err != nil {
			slog.Warn("fomod file missing, skipping", "path", f.Source)
			continue
		}
		resolvedSource, err := filepath.EvalSymlinks(src)
		if err != nil {
			return written, fmt.Errorf("resolving FOMOD source %q: %w", f.Source, err)
		}
		resolvedRoot, err := filepath.EvalSymlinks(extractRoot)
		if err != nil || !pathContainedBy(resolvedRoot, resolvedSource) {
			return written, fmt.Errorf("unsafe FOMOD source %q: resolves outside extraction root", f.Source)
		}
		src = resolvedSource
		if f.IsFolder || info.IsDir() {
			err := filepath.WalkDir(src, func(path string, d os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				rel, err := filepath.Rel(src, path)
				if err != nil {
					return err
				}
				dst := filepath.Join(destRoot, rel)
				if d.IsDir() {
					return os.MkdirAll(dst, 0755)
				}
				copySource := path
				if d.Type()&os.ModeSymlink != 0 {
					copySource, err = filepath.EvalSymlinks(path)
					if err != nil || !pathContainedBy(resolvedRoot, copySource) {
						return fmt.Errorf("FOMOD source symlink %q resolves outside extraction root", path)
					}
				}
				if err := copyFile(copySource, dst); err != nil {
					return err
				}
				rec := filepath.ToSlash(filepath.Join(destRel, rel))
				written = append(written, rec)
				return nil
			})
			if err != nil {
				return written, err
			}
		} else {
			if err := copyFile(src, destRoot); err != nil {
				return written, err
			}
			written = append(written, destRel)
		}
		if sink != nil {
			sink(InstallProgress{
				InstallID: installID, Step: StageCopying,
				Pct: -1, CurrentFile: f.Source,
				FilesDone: int64(len(written)),
			})
		}
	}
	return written, nil
}

func containedInstallPath(root, relative string, allowDot bool) (string, error) {
	relative = filepath.FromSlash(strings.ReplaceAll(strings.TrimSpace(relative), `\`, `/`))
	if relative == "" || filepath.IsAbs(relative) {
		return "", errors.New("path must be non-empty and relative")
	}
	clean := filepath.Clean(relative)
	if clean == "." && !allowDot {
		return "", errors.New("path must name an extracted entry")
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes its root")
	}
	joined := filepath.Join(root, clean)
	if !pathContainedBy(root, joined) {
		return "", errors.New("path escapes its root")
	}
	return joined, nil
}

func pathContainedBy(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// mergeTree copies every file from src into dst, overwriting on collision.
func mergeTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

var modsDirResolver func(gameID string) string

// SetModsDirResolver registers the gameID → mods-dir lookup.
func SetModsDirResolver(f func(string) string) {
	modsDirResolver = f
}

func modsDirFor(gameID string) string {
	if modsDirResolver == nil {
		return ""
	}
	return modsDirResolver(gameID)
}

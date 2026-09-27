package download

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/fsutil"
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
	GameID              string
	ArchivePath         string
	ExtractedRoot       string
	ContentRoot         string
	LegacyFomodFlatCopy bool
	Mode                InstallMode
	TargetMod           string
	RecordModName       string
	SourceArchiveRef    SourceArchiveRef
	DeferIndexUpdate    bool
	DisplayName         string
	Category            string
	Version             string
	ModPage             string
	FomodSelectedFiles  []FomodFile
	ProgressSink        ProgressSink
	InstallID           string
	Layout              LayoutPlanner
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

var renameInstallStageFn = os.Rename

// Install extracts and stages an archive, records its files, and publishes the mod.
func Install(req InstallRequest) (*InstallResult, error) {
	if req.Layout != nil && len(req.FomodSelectedFiles) > 0 {
		return nil, ErrFomodNotSupportedForLayout
	}
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
		defer os.RemoveAll(extractTmp)
		budget := NewExtractBudget()
		emit(InstallProgress{Step: StageExtracting, Pct: -1})
		if err := extractor.ExtractWithBudget(req.ArchivePath, tmp, budget); err != nil {
			return nil, fmt.Errorf("extracting: %w", err)
		}
		if req.Layout == nil {
			if err := ExpandNestedFomods(tmp, budget); err != nil {
				return nil, fmt.Errorf("expanding nested installers: %w", err)
			}
		}
	}

	if req.Layout == nil && len(req.FomodSelectedFiles) == 0 && !req.LegacyFomodFlatCopy && HasFomodInstaller(extractRoot) {
		return nil, &installFomodMarker{Path: req.ArchivePath}
	}
	if req.Layout == nil && len(req.FomodSelectedFiles) > 0 {
		moduleRoot, kind := FindFomodRootKind(extractRoot)
		if kind == FomodKindNone {
			return nil, fmt.Errorf("FOMOD selection requires an installer")
		}
		extractRoot = moduleRoot
	}

	var planned []PlannedCopy
	if req.Layout != nil {
		copies, err := req.Layout.Plan(extractRoot)
		if err != nil {
			emit(InstallProgress{Step: StageFailed, Error: err.Error()})
			return nil, fmt.Errorf("planning archive layout: %w", err)
		}
		if req.Mode == ModeMergeIntoMod {
			copies, err = alignPlannedWithExisting(finalDir, copies)
			if err != nil {
				emit(InstallProgress{Step: StageFailed, Error: err.Error()})
				return nil, err
			}
		}
		planned = copies
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
	switch {
	case len(req.FomodSelectedFiles) > 0:
		written, err = copyFomodSelection(req.GameID, extractRoot, stageDir, req.FomodSelectedFiles, req.InstallID, req.ProgressSink)
	case req.Layout != nil:
		written, err = copyPlanned(extractRoot, stageDir, planned, req.InstallID, req.ProgressSink)
	default:
		contentRoot := req.ContentRoot
		if contentRoot == "" {
			rel, _ := DetectContentRoot(extractRoot, req.GameID)
			contentRoot = filepath.Join(extractRoot, filepath.FromSlash(rel))
		}
		written, err = copyFlatten(req.GameID, extractRoot, contentRoot, stageDir, req.InstallID, req.ProgressSink, req.LegacyFomodFlatCopy)
	}
	if err != nil {
		emit(InstallProgress{Step: StageFailed, Error: err.Error()})
		return nil, err
	}

	emit(InstallProgress{Step: StageFinalizing, Pct: 100})

	ref := req.SourceArchiveRef
	if ref.InstalledAt == "" {
		ref.InstalledAt = time.Now().UTC().Format(time.RFC3339)
	}
	recordModName := req.TargetMod
	if req.RecordModName != "" {
		recordModName = req.RecordModName
	}
	record := func(dir string) (int, error) {
		count, err := appendSourceArchive(
			dir, recordModName, ref,
			req.DisplayName, req.Category, req.Version, req.ModPage, written,
		)
		if err != nil {
			recordErr := &InstallRecordError{Mod: recordModName, Err: err}
			emit(InstallProgress{Step: StageFailed, Error: recordErr.Error()})
			return 0, recordErr
		}
		return count, nil
	}

	var fileCount int
	switch req.Mode {
	case ModeNewMod:
		ref.Merged = false
		fileCount, err = record(stageDir)
		if err != nil {
			return nil, err
		}
		if _, statErr := os.Stat(finalDir); statErr == nil {
			return nil, &installCollisionMarker{Name: req.TargetMod}
		}
		if err := renameInstallStageFn(stageDir, finalDir); err != nil {
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
		if existing, lerr := LoadModMetadata(finalDir); lerr == nil && existing != nil && len(existing.SourceArchives) > 0 {
			ref.Merged = true
		}
		fileCount, err = record(finalDir)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown install mode %d", req.Mode)
	}

	relFromDownloads := strings.TrimPrefix(ref.Path, "Downloads/")
	if relFromDownloads != ref.Path && !req.DeferIndexUpdate {
		if err := SetUninstalled(req.GameID, relFromDownloads, false); err != nil {
			slog.Warn("updating download index failed", "err", err)
		}
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

// skipArchiveMetadata logs and skips a content-root installation record once per copy.
func skipArchiveMetadata(rel string, logged *bool) bool {
	if !strings.EqualFold(rel, "metadata.yaml") {
		return false
	}
	if !*logged {
		slog.Info("ignoring archive metadata.yaml")
		*logged = true
	}
	return true
}

// copyFlatten replays the archive's content root into stage.
func copyFlatten(gameID, extractRoot, contentRoot, stageDir, installID string, sink ProgressSink, excludeFomod bool) ([]string, error) {
	resolvedExtractRoot, err := filepath.EvalSymlinks(extractRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving archive extraction root: %w", err)
	}
	resolvedContentRoot, err := filepath.EvalSymlinks(contentRoot)
	if err != nil || !fsutil.ContainedBy(resolvedExtractRoot, resolvedContentRoot) {
		return nil, fmt.Errorf("archive content root resolves outside extraction root")
	}
	info, err := os.Stat(contentRoot)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("archive content root is not a directory")
	}
	rootedOblivionRemastered := gameID == "oblivionremastered" && hasOblivionRemasteredRootMarkers(contentRoot)

	var written []string
	var loggedMetadata bool
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
		if !d.IsDir() && skipArchiveMetadata(rel, &loggedMetadata) {
			return nil
		}
		if excludeFomod {
			if strings.EqualFold(rel, "fomod") && d.IsDir() {
				return filepath.SkipDir
			}
			if !d.IsDir() && strings.EqualFold(filepath.Ext(rel), ".cs") {
				return nil
			}
		}
		installRel := routeOblivionRemasteredPath(rel, rootedOblivionRemastered)
		dst := filepath.Join(stageDir, installRel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0755)
		}
		copySource := path
		if d.Type()&os.ModeSymlink != 0 {
			copySource, err = filepath.EvalSymlinks(path)
			if err != nil || !fsutil.ContainedBy(resolvedContentRoot, copySource) {
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

// copyFomodSelection applies a FOMOD plugin's file/folder rules in priority order.
func copyFomodSelection(gameID, extractRoot, stageDir string, files []FomodFile, installID string, sink ProgressSink) ([]string, error) {
	resolvedRoot, err := filepath.EvalSymlinks(extractRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving FOMOD extraction root: %w", err)
	}
	ordered := append([]FomodFile(nil), files...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Priority < ordered[j].Priority })
	var written []string
	var loggedMetadata bool
	for _, f := range ordered {
		src, err := resolveFomodSource(extractRoot, resolvedRoot, f.Source)
		if errors.Is(err, os.ErrNotExist) {
			slog.Warn("fomod file missing, skipping", "path", f.Source)
			continue
		}
		if err != nil {
			return written, err
		}
		info, err := os.Stat(src)
		if err != nil {
			return written, fmt.Errorf("reading FOMOD source %q: %w", f.Source, err)
		}
		destRel := f.Destination
		if destRel == "" {
			if f.IsFolder || info.IsDir() {
				destRel = "."
			} else {
				destRel = filepath.Base(src)
			}
		}
		if gameID == "oblivionremastered" {
			destRel = routeOblivionRemasteredPath(filepath.FromSlash(strings.ReplaceAll(destRel, `\`, `/`)), hasOblivionRemasteredRootMarkers(extractRoot))
		}
		destRoot, err := fsutil.SafeJoin(stageDir, destRel, true)
		if err != nil {
			return written, fmt.Errorf("unsafe FOMOD destination %q: %w", f.Destination, err)
		}
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
				toRoot, destErr := filepath.Rel(stageDir, dst)
				if destErr != nil {
					return destErr
				}
				if skipArchiveMetadata(toRoot, &loggedMetadata) {
					return nil
				}
				copySource := path
				if d.Type()&os.ModeSymlink != 0 {
					copySource, err = filepath.EvalSymlinks(path)
					if err != nil || !fsutil.ContainedBy(resolvedRoot, copySource) {
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
			toRoot, err := filepath.Rel(stageDir, destRoot)
			if err != nil {
				return written, err
			}
			if skipArchiveMetadata(toRoot, &loggedMetadata) {
				continue
			}
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
	if len(written) == 0 {
		return nil, ErrEmptyInstallSelection
	}
	return written, nil
}

// resolveFomodSource finds each source path component case-insensitively within the extraction root.
func resolveFomodSource(extractRoot, resolvedRoot, source string) (string, error) {
	joined, err := fsutil.SafeJoin(extractRoot, source, false)
	if err != nil {
		return "", fmt.Errorf("unsafe FOMOD source %q: %w", source, err)
	}
	rel, err := filepath.Rel(extractRoot, joined)
	if err != nil {
		return "", fmt.Errorf("unsafe FOMOD source %q: %w", source, err)
	}
	current := resolvedRoot
	for _, component := range strings.Split(rel, string(filepath.Separator)) {
		entries, err := os.ReadDir(current)
		if err != nil {
			return "", fmt.Errorf("reading FOMOD source %q: %w", source, err)
		}
		match := ""
		ambiguous := false
		for _, entry := range entries {
			if entry.Name() == component {
				match = component
				ambiguous = false
				break
			}
			if strings.EqualFold(entry.Name(), component) {
				if match != "" {
					ambiguous = true
				}
				match = entry.Name()
			}
		}
		if ambiguous {
			return "", fmt.Errorf("ambiguous FOMOD source %q: multiple entries match %q", source, component)
		}
		if match == "" {
			return "", fmt.Errorf("FOMOD source %q: %w", source, os.ErrNotExist)
		}
		resolved, err := filepath.EvalSymlinks(filepath.Join(current, match))
		if err != nil {
			return "", fmt.Errorf("resolving FOMOD source %q: %w", source, err)
		}
		if !fsutil.ContainedBy(resolvedRoot, resolved) {
			return "", fmt.Errorf("unsafe FOMOD source %q: resolves outside extraction root", source)
		}
		current = resolved
	}
	return current, nil
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
		if err := fsutil.CheckExistingPath(dst, rel); err != nil {
			if errors.Is(err, fsutil.ErrExistingLink) || errors.Is(err, fsutil.ErrExistingNonDirectory) {
				return &ArchiveRejectedError{Reason: ArchiveRejectedUnsafeEntry, Detail: rel}
			}
			return err
		}
		target := filepath.Join(dst, rel)
		info, statErr := os.Lstat(target)
		if statErr != nil && !os.IsNotExist(statErr) {
			return statErr
		}
		if statErr == nil && (d.IsDir() != info.IsDir() || !d.IsDir() && !info.Mode().IsRegular()) {
			return &ArchiveRejectedError{Reason: ArchiveRejectedUnsafeEntry, Detail: rel}
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		if !d.Type().IsRegular() {
			return &ArchiveRejectedError{Reason: ArchiveRejectedUnsafeEntry, Detail: rel}
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if statErr == nil {
			_, err := atomicfile.CopyFileDurable(path, target, 0644, true)
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0644)
		if err != nil {
			closeErr := in.Close()
			if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EISDIR) {
				return errors.Join(&ArchiveRejectedError{Reason: ArchiveRejectedUnsafeEntry, Detail: rel}, closeErr)
			}
			return errors.Join(err, closeErr)
		}
		_, err = io.Copy(out, in)
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
		if closeErr := in.Close(); err == nil {
			err = closeErr
		}
		return err
	})
}

var reservedModFolderNames = []string{"Overwrite", "Downloads"}

// ValidateTargetModName rejects a mod folder name that is unsafe, hidden, or reserved.
func ValidateTargetModName(name string) error {
	if err := fsutil.ValidateName(name); err != nil {
		return &InvalidTargetModError{Name: name, Reason: err.Error()}
	}
	if strings.HasPrefix(name, ".") {
		return &InvalidTargetModError{Name: name, Reason: "name must not start with a dot"}
	}
	for _, reserved := range reservedModFolderNames {
		if strings.EqualFold(name, reserved) {
			return &InvalidTargetModError{Name: name, Reason: "name is reserved"}
		}
	}
	return nil
}

// NormalizeDerivedModName turns a folder name derived from archive metadata into a valid, non-reserved mod folder name.
func NormalizeDerivedModName(name string) string {
	name = strings.TrimSpace(strings.TrimLeftFunc(name, func(r rune) bool { return r == '.' || unicode.IsSpace(r) }))
	for _, reserved := range reservedModFolderNames {
		if strings.EqualFold(name, reserved) {
			name += "_"
		}
	}
	if ValidateTargetModName(name) != nil {
		return "Mod"
	}
	return name
}

// ValidateMergeTarget requires a valid mod folder name that names an existing, non-symlinked directory under modsDir.
func ValidateMergeTarget(modsDir, name string) error {
	if err := ValidateTargetModName(name); err != nil {
		return err
	}
	info, err := os.Lstat(filepath.Join(modsDir, name))
	switch {
	case err != nil:
		return &InvalidTargetModError{Name: name, Reason: "merge target is not an existing mod folder"}
	case info.Mode()&os.ModeSymlink != 0:
		return &InvalidTargetModError{Name: name, Reason: "merge target is a symlink"}
	case !info.IsDir():
		return &InvalidTargetModError{Name: name, Reason: "merge target is not a directory"}
	}
	return nil
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

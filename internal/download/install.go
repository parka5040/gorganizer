package download

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// InstallMode controls the disposition of an install: NEW_MOD creates a
// fresh mod folder; MERGE_INTO overlays the archive onto an existing mod
// folder and appends to its source_archives list.
type InstallMode int

const (
	ModeNewMod     InstallMode = 0
	ModeMergeIntoMod InstallMode = 1
)

// InstallProgress reports one step in the install lifecycle. ProgressSink
// implementations must be non-blocking; they're called on the extraction /
// copy hot loop.
type InstallProgress struct {
	InstallID   string
	Step        InstallStage
	Pct         int32
	CurrentFile string
	FilesDone   int64
	FilesTotal  int64
	Error       string
}

// ProgressSink is the progress callback signature shared by every install
// entry point. Pass nil to opt out.
type ProgressSink func(InstallProgress)

// InstallRequest is the canonical input to every install. Invariants:
//   - Exactly one of ArchivePath / ExtractedRoot must be set. When
//     ExtractedRoot is set, the archive has already been extracted
//     (typically by PreviewInstall) and the canonical Install skips
//     re-extraction.
//   - Mode + TargetMod determine the destination folder.
//   - SourceArchiveRef is the entry that gets appended to the mod's
//     source_archives list. A non-nil ref is *mandatory* — every install
//     writes source_archives so ReinstallMod always works. Callers outside
//     the download pipeline (drag-drop) supply a ref whose Path points at
//     the original archive location or an imported copy.
type InstallRequest struct {
	GameID           string
	ArchivePath      string // absolute path on disk; required unless ExtractedRoot is set
	ExtractedRoot    string // pre-extracted directory (from PreviewInstall)
	Mode             InstallMode
	TargetMod        string // folder name under ModsDir
	SourceArchiveRef SourceArchiveRef
	DisplayName      string // optional; falls back to TargetMod
	Category         string // optional Nexus category
	Version          string // optional Nexus version
	ModPage          string // optional Nexus page URL
	// FomodSelectedFiles: when non-empty, skip auto-flatten and copy
	// exactly this set of files (relative to ExtractedRoot).
	FomodSelectedFiles []FomodFile
	// ProgressSink is the optional callback. Events include an InstallID
	// which the daemon uses to route stream events to the right subscriber.
	ProgressSink ProgressSink
	// InstallID is the correlation key for streamed progress. When empty,
	// one is generated here.
	InstallID string
}

// InstallResult summarizes a successful install.
type InstallResult struct {
	ModFolder string
	FileCount int
	Files     []string
	InstallID string
}

// FomodFile is the set-lookup form of a FOMOD user selection.
type FomodFile struct {
	Source      string // relative to extract root
	Destination string // relative to mod folder root (empty = same as Source)
	IsFolder    bool
	Priority    int32
}

// InstallStage is the coarse phase reported via ProgressSink. The proto
// enum is the authoritative wire form — these values align with it.
type InstallStage int

const (
	StageExtracting InstallStage = 1
	StageCopying    InstallStage = 2
	StageFinalizing InstallStage = 3
	StageComplete   InstallStage = 4
	StageFailed     InstallStage = 5
)

// Install is the single canonical path every install flows through:
// drag-drop, post-download auto-install, user-initiated InstallDownload,
// ReinstallMod. One function, one set of invariants (atomic staging, always
// writes source_archives, always emits progress).
//
// Flow:
//
//  1. Extract archive → staging dir, unless ExtractedRoot is already set.
//  2. Bail with *FomodRequiredError if the archive is a FOMOD package and
//     the caller did NOT pass FomodSelectedFiles. (We surface a structured
//     error rather than a sentinel so the daemon can map it to gRPC.)
//  3. Copy (or apply selected files) into {ModsDir}/.stage-<uuid>/ — this
//     keeps the real mod folder untouched until the operation succeeds.
//  4. For NEW_MOD: fail if {ModsDir}/{target}/ already exists; otherwise
//     rename staging → target. For MERGE_INTO: move files from staging into
//     the existing target, overwriting.
//  5. Write/merge metadata.yaml with the new source_archives entry.
//  6. Emit StageComplete progress.
//
// The caller is responsible for cleaning up a preview dir (ExtractedRoot)
// on completion; Install doesn't remove it (it may be reused by other
// callers).
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

	// Extract if the caller didn't hand us a pre-extracted tree.
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
		// Legacy NMM-style installers nest a *.fomod (itself an archive)
		// inside the outer download. Expand before FOMOD detection so the
		// inner fomod/ tree is visible.
		ExpandNestedFomods(tmp)
	}
	if extractTmp != "" {
		defer os.RemoveAll(extractTmp)
	}

	// FOMOD detection. When the caller didn't pre-select, bail with a
	// structured error so the caller can route to PreviewInstall and show
	// the wizard. We deliberately do NOT emit a StageFailed event here —
	// fomod_required is a routing signal, not a failure. The frontend
	// catches the error, opens the wizard, and the user sees the popup
	// instead of a noisy "Install failed: fomod_required" line in the
	// activity log followed by a successful install.
	if len(req.FomodSelectedFiles) == 0 && HasFomodInstaller(extractRoot) {
		return nil, &installFomodMarker{Path: req.ArchivePath}
	}

	// Stage files into a fresh dir alongside the real mod folder, then
	// rename into place for NEW_MOD or merge for MERGE_INTO. The atomic
	// rename means a failure partway through never leaves a half-populated
	// mod folder.
	stageDir, err := os.MkdirTemp(modsDir, ".stage-")
	if err != nil {
		return nil, fmt.Errorf("creating stage dir: %w", err)
	}
	// On any failure after this point we clean up the stage dir; on
	// success it gets renamed/consumed.
	stageCleanup := true
	defer func() {
		if stageCleanup {
			os.RemoveAll(stageDir)
		}
	}()

	emit(InstallProgress{Step: StageCopying, Pct: 0})

	var written []string
	if len(req.FomodSelectedFiles) > 0 {
		written, err = copyFomodSelection(extractRoot, stageDir, req.FomodSelectedFiles, req.InstallID, req.ProgressSink)
	} else {
		written, err = copyFlatten(extractRoot, stageDir, req.InstallID, req.ProgressSink)
	}
	if err != nil {
		emit(InstallProgress{Step: StageFailed, Error: err.Error()})
		return nil, err
	}

	emit(InstallProgress{Step: StageFinalizing, Pct: 100})

	// Move staging → final.
	switch req.Mode {
	case ModeNewMod:
		if _, statErr := os.Stat(finalDir); statErr == nil {
			return nil, &installCollisionMarker{Name: req.TargetMod}
		}
		if err := os.Rename(stageDir, finalDir); err != nil {
			return nil, fmt.Errorf("moving stage → %s: %w", finalDir, err)
		}
		// Rename consumed the directory — flip the cleanup guard so the
		// defer doesn't try to remove an already-gone path.
		stageCleanup = false
	case ModeMergeIntoMod:
		if err := os.MkdirAll(finalDir, 0755); err != nil {
			return nil, fmt.Errorf("ensuring merge target: %w", err)
		}
		if err := mergeTree(stageDir, finalDir); err != nil {
			return nil, fmt.Errorf("merging into %s: %w", finalDir, err)
		}
		// mergeTree COPIES from stage to final — the source remains. Leave
		// stageCleanup=true so the deferred RemoveAll above wipes it. Until
		// 2026-04, this branch was incorrectly setting stageCleanup=false
		// after successful merges, which caused every merge-mode install
		// (including ReinstallMod's per-archive replay loop) to leak a
		// .stage-<rand>/ tree of fully-extracted files inside ModsDir.
	default:
		return nil, fmt.Errorf("unknown install mode %d", req.Mode)
	}

	// Write / append metadata.yaml. This is the INVARIANT — every install
	// writes source_archives so ReinstallMod and UninstallMod always work.
	ref := req.SourceArchiveRef
	if ref.InstalledAt == "" {
		ref.InstalledAt = time.Now().UTC().Format(time.RFC3339)
	}
	// Mark this archive as "merged into a pre-existing mod" only when the
	// merge target already had at least one prior source_archive. A fresh
	// new-mod install via merge mode (rare but possible) doesn't qualify —
	// the user expects the FIRST archive into a mod folder to read as a
	// regular install.
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

	// Clear the archive's sticky Uninstalled flag if the archive ref was
	// supplied as "Downloads/..." (i.e. came from the downloads index).
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

// installFomodMarker / installCollisionMarker are thin sentinels returned
// from Install; the daemon layer wraps them into IPC-level error types
// (ipc.FomodRequiredError, ipc.ModCollisionError) so the structured-error
// mapping stays in one place. Kept package-local so the download package
// doesn't import ipc.
type installFomodMarker struct{ Path string }

func (e *installFomodMarker) Error() string {
	return fmt.Sprintf("FOMOD required: %s", e.Path)
}

// IsFomodMarker extracts the archive path from a fomod-required marker
// returned by Install. The daemon calls this to decide whether to map the
// error to FomodRequiredError.
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

// IsCollisionMarker extracts the colliding mod name from a collision marker
// returned by Install.
func IsCollisionMarker(err error) (string, bool) {
	if e, ok := err.(*installCollisionMarker); ok {
		return e.Name, true
	}
	return "", false
}

// copyFlatten replays the archive's content root into stage, mirroring
// findContentRoot's logic for Bethesda-style layouts.
func copyFlatten(extractRoot, stageDir, installID string, sink ProgressSink) ([]string, error) {
	contentRoot := findContentRoot(extractRoot)

	var written []string
	err := filepath.WalkDir(contentRoot, func(path string, d os.DirEntry, err error) error {
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
		dst := filepath.Join(stageDir, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0755)
		}
		if err := copyFile(path, dst); err != nil {
			return err
		}
		written = append(written, rel)
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

// copyFomodSelection applies a FOMOD plugin's file/folder rules: each
// FomodFile may name a source file or folder (relative to the extract
// root) and an optional destination path inside the mod folder.
func copyFomodSelection(extractRoot, stageDir string, files []FomodFile, installID string, sink ProgressSink) ([]string, error) {
	var written []string
	for _, f := range files {
		src := filepath.Join(extractRoot, filepath.FromSlash(f.Source))
		destRel := f.Destination
		if destRel == "" {
			destRel = f.Source
		}
		info, err := os.Stat(src)
		if err != nil {
			// Missing entries in a FOMOD plan are usually optional — skip.
			slog.Warn("fomod file missing, skipping", "path", f.Source)
			continue
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
				dst := filepath.Join(stageDir, filepath.FromSlash(destRel), rel)
				if d.IsDir() {
					return os.MkdirAll(dst, 0755)
				}
				if err := copyFile(path, dst); err != nil {
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
			dst := filepath.Join(stageDir, filepath.FromSlash(destRel))
			if err := copyFile(src, dst); err != nil {
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

// mergeTree walks `src` and copies every file into `dst`, overwriting on
// collision. Used by MERGE_INTO installs after staging.
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

// modsDirFor is a decoupled shim so `download` doesn't import config. The
// daemon wires this up via SetModsDirResolver on init. Staying out of
// config also means the download package has no game-list dependency —
// just a string → string lookup.
var modsDirResolver func(gameID string) string

// SetModsDirResolver lets the daemon plug in a mods-dir lookup. Exposed so
// tests can also stub it. Called once at startup.
func SetModsDirResolver(f func(string) string) {
	modsDirResolver = f
}

func modsDirFor(gameID string) string {
	if modsDirResolver == nil {
		return ""
	}
	return modsDirResolver(gameID)
}

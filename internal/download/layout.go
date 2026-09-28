package download

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/parka/gorganizer/internal/fsutil"
)

type PlannedCopy struct {
	SourceRel string
	DestName  string
}

type LayoutPlanner interface {
	Plan(extractRoot string) ([]PlannedCopy, error)
}

// ValidatePlannedCopies rejects unsafe, hidden, reserved, or case-insensitively duplicated planned destination names.
func ValidatePlannedCopies(copies []PlannedCopy) error {
	seen := make(map[string]string, len(copies))
	for _, c := range copies {
		if err := fsutil.ValidateName(c.DestName); err != nil {
			return fmt.Errorf("%w: planned destination %q: %v", ErrUnsafeArchive, c.DestName, err)
		}
		if strings.HasPrefix(c.DestName, ".") {
			return fmt.Errorf("%w: planned destination %q is hidden", ErrUnsafeArchive, c.DestName)
		}
		if strings.TrimSpace(c.DestName) != c.DestName {
			return fmt.Errorf("%w: planned destination %q has surrounding whitespace", ErrUnsafeArchive, c.DestName)
		}
		if strings.EqualFold(c.DestName, "metadata.yaml") {
			return fmt.Errorf("%w: planned destination %q is reserved", ErrUnsafeArchive, c.DestName)
		}
		key := strings.ToLower(c.DestName)
		if previous, dup := seen[key]; dup {
			return fmt.Errorf("%w: planned destinations %q and %q collide", ErrUnsafeArchive, previous, c.DestName)
		}
		seen[key] = c.DestName
	}
	return nil
}

// alignPlannedWithExisting renames each planned destination to the spelling of an existing top-level folder of targetDir that differs only by letter case.
func alignPlannedWithExisting(targetDir string, copies []PlannedCopy) ([]PlannedCopy, error) {
	if err := ValidatePlannedCopies(copies); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		if os.IsNotExist(err) {
			return copies, nil
		}
		return nil, fmt.Errorf("reading merge target %s: %w", targetDir, err)
	}
	existing := make(map[string][]os.DirEntry, len(entries))
	for _, entry := range entries {
		key := strings.ToLower(entry.Name())
		existing[key] = append(existing[key], entry)
	}
	aligned := make([]PlannedCopy, len(copies))
	for i, planned := range copies {
		aligned[i] = planned
		matches := existing[strings.ToLower(planned.DestName)]
		if len(matches) == 0 {
			continue
		}
		match, err := pickExistingFolder(planned.DestName, matches)
		if err != nil {
			return nil, err
		}
		aligned[i].DestName = match
	}
	return aligned, nil
}

// pickExistingFolder chooses the existing real directory a planned destination merges into, refusing files, symlinks and ambiguous spellings.
func pickExistingFolder(destName string, matches []os.DirEntry) (string, error) {
	var chosen os.DirEntry
	if len(matches) == 1 {
		chosen = matches[0]
	} else {
		for _, match := range matches {
			if match.Name() == destName {
				chosen = match
			}
		}
	}
	if chosen == nil {
		return "", fmt.Errorf("%w: planned folder %q matches several existing folders that differ only by letter case", ErrUnsafeArchive, destName)
	}
	if chosen.Type()&os.ModeSymlink != 0 || !chosen.IsDir() {
		return "", fmt.Errorf("%w: planned folder %q collides with existing non-folder entry %q", ErrUnsafeArchive, destName, chosen.Name())
	}
	return chosen.Name(), nil
}

// resolvePlannedSource resolves a planned source folder inside the extract root, rejecting escapes and non-directories.
func resolvePlannedSource(extractRoot, resolvedRoot, sourceRel string) (string, error) {
	source := extractRoot
	if sourceRel != "" {
		joined, err := fsutil.SafeJoin(extractRoot, sourceRel, true)
		if err != nil {
			return "", fmt.Errorf("%w: planned source %q: %v", ErrUnsafeArchive, sourceRel, err)
		}
		source = joined
	}
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil || !fsutil.ContainedBy(resolvedRoot, resolved) {
		return "", fmt.Errorf("%w: planned source %q resolves outside the extract root", ErrUnsafeArchive, sourceRel)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("planned source %q: %w", sourceRel, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: planned source %q is not a directory", ErrUnsafeArchive, sourceRel)
	}
	return resolved, nil
}

// copyPlanned copies each planned source folder into its own top-level folder of the stage.
func copyPlanned(extractRoot, stageDir string, copies []PlannedCopy, installID string, sink ProgressSink, contexts ...context.Context) ([]string, error) {
	var ctx context.Context
	if len(contexts) > 0 {
		ctx = contexts[0]
	}
	if err := ValidatePlannedCopies(copies); err != nil {
		return nil, err
	}
	resolvedRoot, err := filepath.EvalSymlinks(extractRoot)
	if err != nil {
		return nil, fmt.Errorf("resolving archive extract root: %w", err)
	}

	var written []string
	for _, planned := range copies {
		if err := installContextErr(ctx); err != nil {
			return written, err
		}
		source, err := resolvePlannedSource(extractRoot, resolvedRoot, planned.SourceRel)
		if err != nil {
			return written, err
		}
		destRoot := filepath.Join(stageDir, planned.DestName)
		if err := os.MkdirAll(destRoot, 0755); err != nil {
			return written, err
		}
		err = filepath.WalkDir(source, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err := installContextErr(ctx); err != nil {
				return err
			}
			rel, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			if rel == "." {
				return nil
			}
			record := planned.DestName + "/" + filepath.ToSlash(rel)
			dst, err := fsutil.SafeJoin(stageDir, record, false)
			if err != nil || dst != filepath.Join(destRoot, rel) {
				return fmt.Errorf("%w: archive entry %q cannot be staged under %q", ErrUnsafeArchive, rel, planned.DestName)
			}
			if d.IsDir() {
				return os.MkdirAll(dst, 0755)
			}
			copySource := path
			if d.Type()&os.ModeSymlink != 0 {
				copySource, err = filepath.EvalSymlinks(path)
				if err != nil || !fsutil.ContainedBy(resolvedRoot, copySource) {
					return fmt.Errorf("archive symlink %q resolves outside its extract root", record)
				}
			}
			info, err := os.Stat(copySource)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("archive contains unsupported special file %q", record)
			}
			if err := copyFile(copySource, dst); err != nil {
				return err
			}
			written = append(written, record)
			if sink != nil && len(written)%32 == 0 {
				sink(InstallProgress{
					InstallID:   installID,
					Step:        StageCopying,
					Pct:         -1,
					CurrentFile: record,
					FilesDone:   int64(len(written)),
				})
			}
			return nil
		})
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

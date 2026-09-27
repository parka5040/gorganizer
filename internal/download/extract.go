package download

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/parka/gorganizer/internal/fsutil"
)

type extractLimits struct {
	MaxEntries    int
	MaxEntryBytes int64
	MaxTotalBytes int64
}

type ExtractBudget struct {
	remainingEntries    int
	remainingTotalBytes int64
	maxEntryBytes       int64
}

type extractionLimitReader struct {
	reader    io.Reader
	remaining int64
	exceeded  bool
}

// defaultExtractLimits returns the archive extraction safety limits.
func defaultExtractLimits() extractLimits {
	return extractLimits{
		MaxEntries:    500000,
		MaxEntryBytes: 8 << 30,
		MaxTotalBytes: 32 << 30,
	}
}

// NewExtractBudget creates a budget for all archives extracted during one operation.
func NewExtractBudget() *ExtractBudget {
	return newExtractBudget(defaultExtractLimits())
}

// newExtractBudget creates an extraction budget with the given limits.
func newExtractBudget(limits extractLimits) *ExtractBudget {
	return &ExtractBudget{
		remainingEntries:    limits.MaxEntries,
		remainingTotalBytes: limits.MaxTotalBytes,
		maxEntryBytes:       limits.MaxEntryBytes,
	}
}

// Read copies no more than the configured number of bytes from the reader.
func (r *extractionLimitReader) Read(p []byte) (int, error) {
	if r.remaining > 0 {
		if int64(len(p)) > r.remaining {
			p = p[:r.remaining]
		}
		n, err := r.reader.Read(p)
		r.remaining -= int64(n)
		return n, err
	}

	var probe [1]byte
	n, err := r.reader.Read(probe[:])
	if n > 0 {
		r.exceeded = true
		return 0, io.EOF
	}
	return n, err
}

// extractEntries writes archive entries into destDir within the shared budget.
func extractEntries[T any](files []T, destDir string, budget *ExtractBudget, name func(T) string, isDir func(T) bool, mode func(T) os.FileMode, open func(T) (io.ReadCloser, error)) error {
	for _, file := range files {
		entryName := name(file)
		if budget.remainingEntries <= 0 {
			return &ArchiveRejectedError{Reason: ArchiveRejectedLimit, Detail: fmt.Sprintf("entry %q exceeds the maximum entry count", entryName)}
		}
		budget.remainingEntries--

		destPath, safeJoinErr := fsutil.SafeJoin(destDir, entryName, false)
		if safeJoinErr != nil {
			return &ArchiveRejectedError{Reason: ArchiveRejectedUnsafeEntry, Detail: fmt.Sprintf("entry %q escapes the destination directory: %v", entryName, safeJoinErr)}
		}

		entryMode := mode(file)
		if entryMode&os.ModeSymlink != 0 || (!isDir(file) && entryMode&os.ModeType != 0) {
			return &ArchiveRejectedError{Reason: ArchiveRejectedUnsafeEntry, Detail: fmt.Sprintf("entry %q is not a regular file or directory", entryName)}
		}
		if isDir(file) {
			if mkdirErr := os.MkdirAll(destPath, 0755); mkdirErr != nil {
				return mkdirErr
			}
			continue
		}

		if mkdirErr := os.MkdirAll(filepath.Dir(destPath), 0755); mkdirErr != nil {
			return mkdirErr
		}
		if err := copyExtractedFile(file, destPath, entryName, budget, open); err != nil {
			return err
		}
	}
	return nil
}

// copyExtractedFile writes one archive file while enforcing the shared byte limits.
func copyExtractedFile[T any](file T, destPath, entryName string, budget *ExtractBudget, open func(T) (io.ReadCloser, error)) error {
	input, err := open(file)
	if err != nil {
		return err
	}

	output, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		closeErr := input.Close()
		if os.IsExist(err) {
			return &ArchiveRejectedError{Reason: ArchiveRejectedUnsafeEntry, Detail: fmt.Sprintf("duplicate entry %q", entryName)}
		}
		if closeErr != nil {
			return closeErr
		}
		return err
	}

	limit := budget.maxEntryBytes
	entryLimitReached := limit <= budget.remainingTotalBytes
	if budget.remainingTotalBytes < limit {
		limit = budget.remainingTotalBytes
	}
	if limit < 0 {
		limit = 0
	}
	limitedInput := &extractionLimitReader{reader: input, remaining: limit}
	written, copyErr := io.Copy(output, limitedInput)
	budget.remainingTotalBytes -= written
	outputCloseErr := output.Close()
	inputCloseErr := input.Close()
	if limitedInput.exceeded {
		if entryLimitReached {
			return &ArchiveRejectedError{Reason: ArchiveRejectedLimit, Detail: fmt.Sprintf("entry %q exceeds the maximum entry size", entryName)}
		}
		return &ArchiveRejectedError{Reason: ArchiveRejectedLimit, Detail: fmt.Sprintf("entry %q exceeds the maximum total size", entryName)}
	}
	if copyErr != nil {
		return copyErr
	}
	if outputCloseErr != nil {
		return outputCloseErr
	}
	return inputCloseErr
}

// validateRarExtraction checks files produced by an external archive extractor against the shared budget.
func validateRarExtraction(destDir string, budget *ExtractBudget) error {
	root, err := filepath.Abs(destDir)
	if err != nil {
		return &ArchiveRejectedError{Reason: ArchiveRejectedUnsafeEntry, Detail: fmt.Sprintf("cannot resolve destination directory: %v", err)}
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return &ArchiveRejectedError{Reason: ArchiveRejectedUnsafeEntry, Detail: fmt.Sprintf("cannot resolve destination directory: %v", err)}
	}

	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return &ArchiveRejectedError{Reason: ArchiveRejectedUnsafeEntry, Detail: fmt.Sprintf("entry %q is a symlink", path)}
		}
		resolvedPath, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr != nil {
			return &ArchiveRejectedError{Reason: ArchiveRejectedUnsafeEntry, Detail: fmt.Sprintf("entry %q cannot be resolved: %v", path, resolveErr)}
		}
		if !fsutil.ContainedBy(resolvedRoot, resolvedPath) {
			return &ArchiveRejectedError{Reason: ArchiveRejectedUnsafeEntry, Detail: fmt.Sprintf("entry %q resolves outside the destination directory", path)}
		}
		if path == root {
			return nil
		}
		if budget.remainingEntries <= 0 {
			return &ArchiveRejectedError{Reason: ArchiveRejectedLimit, Detail: fmt.Sprintf("entry %q exceeds the maximum entry count", path)}
		}
		budget.remainingEntries--
		if entry.IsDir() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if !info.Mode().IsRegular() {
			return &ArchiveRejectedError{Reason: ArchiveRejectedUnsafeEntry, Detail: fmt.Sprintf("entry %q is not a regular file", path)}
		}
		if info.Size() > budget.maxEntryBytes {
			return &ArchiveRejectedError{Reason: ArchiveRejectedLimit, Detail: fmt.Sprintf("entry %q exceeds the maximum entry size", path)}
		}
		if info.Size() > budget.remainingTotalBytes {
			return &ArchiveRejectedError{Reason: ArchiveRejectedLimit, Detail: fmt.Sprintf("entry %q exceeds the maximum total size", path)}
		}
		budget.remainingTotalBytes -= info.Size()
		return nil
	})
}

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

// extractEntries writes archive entries into destDir under the given limits.
func extractEntries[T any](files []T, destDir string, limits extractLimits, name func(T) string, isDir func(T) bool, mode func(T) os.FileMode, open func(T) (io.ReadCloser, error)) (err error) {
	defer func() {
		if err != nil {
			_ = os.RemoveAll(destDir)
		}
	}()

	var totalBytes int64
	for index, file := range files {
		entryName := name(file)
		if index >= limits.MaxEntries {
			return fmt.Errorf("%w: entry %q exceeds the maximum entry count", ErrUnsafeArchive, entryName)
		}

		destPath, safeJoinErr := fsutil.SafeJoin(destDir, entryName, false)
		if safeJoinErr != nil {
			return fmt.Errorf("%w: entry %q escapes the destination directory: %v", ErrUnsafeArchive, entryName, safeJoinErr)
		}

		entryMode := mode(file)
		if entryMode&os.ModeSymlink != 0 || (!isDir(file) && entryMode&os.ModeType != 0) {
			return fmt.Errorf("%w: entry %q is not a regular file or directory", ErrUnsafeArchive, entryName)
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
		written, copyErr := copyExtractedFile(file, destPath, entryName, limits, totalBytes, open)
		if copyErr != nil {
			return copyErr
		}
		totalBytes += written
	}
	return nil
}

// copyExtractedFile writes one archive file while enforcing byte limits.
func copyExtractedFile[T any](file T, destPath, entryName string, limits extractLimits, totalBytes int64, open func(T) (io.ReadCloser, error)) (int64, error) {
	input, err := open(file)
	if err != nil {
		return 0, err
	}

	output, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		closeErr := input.Close()
		if os.IsExist(err) {
			return 0, fmt.Errorf("%w: duplicate entry %q", ErrUnsafeArchive, entryName)
		}
		if closeErr != nil {
			return 0, closeErr
		}
		return 0, err
	}

	remainingTotal := limits.MaxTotalBytes - totalBytes
	limit := limits.MaxEntryBytes
	entryLimitReached := limit <= remainingTotal
	if remainingTotal < limit {
		limit = remainingTotal
	}
	if limit < 0 {
		limit = 0
	}
	limitedInput := &extractionLimitReader{reader: input, remaining: limit}
	written, copyErr := io.Copy(output, limitedInput)
	outputCloseErr := output.Close()
	inputCloseErr := input.Close()
	if limitedInput.exceeded {
		if entryLimitReached {
			return 0, fmt.Errorf("%w: entry %q exceeds the maximum entry size", ErrUnsafeArchive, entryName)
		}
		return 0, fmt.Errorf("%w: entry %q exceeds the maximum total size", ErrUnsafeArchive, entryName)
	}
	if copyErr != nil {
		return 0, copyErr
	}
	if outputCloseErr != nil {
		return 0, outputCloseErr
	}
	if inputCloseErr != nil {
		return 0, inputCloseErr
	}
	return written, nil
}

// validateRarExtraction checks files produced by an external archive extractor.
func validateRarExtraction(destDir string, limits extractLimits) (err error) {
	defer func() {
		if err != nil {
			_ = os.RemoveAll(destDir)
		}
	}()

	root, err := filepath.Abs(destDir)
	if err != nil {
		return fmt.Errorf("%w: cannot resolve destination directory: %v", ErrUnsafeArchive, err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("%w: cannot resolve destination directory: %v", ErrUnsafeArchive, err)
	}

	var fileCount int
	var totalBytes int64
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: entry %q is a symlink", ErrUnsafeArchive, path)
		}
		resolvedPath, resolveErr := filepath.EvalSymlinks(path)
		if resolveErr != nil {
			return fmt.Errorf("%w: entry %q cannot be resolved: %v", ErrUnsafeArchive, path, resolveErr)
		}
		if !fsutil.ContainedBy(resolvedRoot, resolvedPath) {
			return fmt.Errorf("%w: entry %q resolves outside the destination directory", ErrUnsafeArchive, path)
		}
		if entry.IsDir() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return infoErr
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		fileCount++
		if fileCount > limits.MaxEntries {
			return fmt.Errorf("%w: entry %q exceeds the maximum entry count", ErrUnsafeArchive, path)
		}
		totalBytes += info.Size()
		if totalBytes > limits.MaxTotalBytes {
			return fmt.Errorf("%w: entry %q exceeds the maximum total size", ErrUnsafeArchive, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

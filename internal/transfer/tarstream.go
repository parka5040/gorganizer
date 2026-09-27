package transfer

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/klauspost/compress/zstd"
)

var zstdMagic = []byte{0x28, 0xB5, 0x2F, 0xFD}

var gzipMagic = []byte{0x1F, 0x8B}

// writeTarBytes adds one regular-file entry with the given content.
func writeTarBytes(tw *tar.Writer, name string, data []byte, modTime time.Time) error {
	hdr := &tar.Header{
		Name:     name,
		Typeflag: tar.TypeReg,
		Mode:     0644,
		Size:     int64(len(data)),
		ModTime:  modTime,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("writing tar header %s: %w", name, err)
	}
	if _, err := tw.Write(data); err != nil {
		return fmt.Errorf("writing tar entry %s: %w", name, err)
	}
	return nil
}

// writeTarTree streams every directory and regular file under root as prefix/<rel> entries.
func writeTarTree(tw *tar.Writer, root, prefix string, onFile func(rel string, size int64)) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		name := prefix + "/" + filepath.ToSlash(rel)
		if rel == "." {
			name = prefix
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			hdr := &tar.Header{
				Name:     name + "/",
				Typeflag: tar.TypeDir,
				Mode:     0755,
				ModTime:  info.ModTime(),
			}
			return tw.WriteHeader(hdr)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		hdr := &tar.Header{
			Name:     name,
			Typeflag: tar.TypeReg,
			Mode:     int64(info.Mode().Perm()),
			Size:     info.Size(),
			ModTime:  info.ModTime(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("writing tar header %s: %w", name, err)
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		n, err := io.Copy(tw, f)
		f.Close()
		if err != nil {
			return fmt.Errorf("writing tar entry %s: %w", name, err)
		}
		if onFile != nil {
			onFile(name, n)
		}
		return nil
	})
}

// openArchiveReader opens archivePath and returns a tar.Reader after zstd/gzip/plain magic-byte detection.
func openArchiveReader(archivePath string) (*tar.Reader, func() error, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, nil, err
	}
	br := bufio.NewReaderSize(f, 1<<16)
	magic, err := br.Peek(4)
	if err != nil && len(magic) < 2 {
		f.Close()
		return nil, nil, fmt.Errorf("reading archive header %s: %w", archivePath, err)
	}
	switch {
	case len(magic) >= 4 && bytes.Equal(magic[:4], zstdMagic):
		zr, err := zstd.NewReader(br)
		if err != nil {
			f.Close()
			return nil, nil, fmt.Errorf("opening zstd stream: %w", err)
		}
		closer := func() error {
			zr.Close()
			return f.Close()
		}
		return tar.NewReader(zr), closer, nil
	case len(magic) >= 2 && bytes.Equal(magic[:2], gzipMagic):
		gr, err := gzip.NewReader(br)
		if err != nil {
			f.Close()
			return nil, nil, fmt.Errorf("opening gzip stream: %w", err)
		}
		closer := func() error {
			gr.Close()
			return f.Close()
		}
		return tar.NewReader(gr), closer, nil
	default:
		return tar.NewReader(br), f.Close, nil
	}
}

// splitEntryName validates a tar entry name and returns its top-level prefix plus remainder.
func splitEntryName(name string) (prefix, rest string, err error) {
	if name == "" || strings.HasPrefix(name, "/") {
		return "", "", &TransferPathError{Entry: name}
	}
	clean := path.Clean(name)
	if clean != strings.TrimSuffix(name, "/") {
		return "", "", &TransferPathError{Entry: name}
	}
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") || strings.HasSuffix(clean, "/..") {
		return "", "", &TransferPathError{Entry: name}
	}
	prefix, rest, _ = strings.Cut(clean, "/")
	switch prefix {
	case "mods", "overwrite", "profiles", "gamesettings":
		return prefix, rest, nil
	}
	if clean == manifestEntryName {
		return manifestEntryName, "", nil
	}
	return "", "", &TransferPathError{Entry: name}
}

// validateEntryType rejects links and special tar entries.
func validateEntryType(hdr *tar.Header) error {
	switch hdr.Typeflag {
	case tar.TypeDir, tar.TypeReg, tar.TypeRegA:
		return nil
	case tar.TypeSymlink, tar.TypeLink:
		return &BundleRejectedError{Reason: BundleRejectedLink, Item: hdr.Name}
	default:
		return &BundleRejectedError{Reason: BundleRejectedSpecial, Item: hdr.Name}
	}
}

// recordEntry rejects duplicate file paths and conflicting directory entries across the archive.
func recordEntry(seen map[string]byte, hdr *tar.Header) error {
	name := strings.TrimSuffix(hdr.Name, "/")
	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		if seen[parent] == 'f' {
			return &BundleRejectedError{Reason: BundleRejectedDuplicate, Item: hdr.Name}
		}
		if seen[parent] == 0 {
			seen[parent] = 'i'
		}
	}
	if seen[name] == 'f' || seen[name] == 'd' || (seen[name] == 'i' && hdr.Typeflag != tar.TypeDir) {
		return &BundleRejectedError{Reason: BundleRejectedDuplicate, Item: hdr.Name}
	}
	if hdr.Typeflag == tar.TypeDir {
		seen[name] = 'd'
	} else {
		seen[name] = 'f'
	}
	return nil
}

// duplicatePathError reports whether a file or directory already occupies a staging path.
func duplicatePathError(err error) bool {
	return os.IsExist(err) || errors.Is(err, syscall.ENOTDIR)
}

// extractEntry writes one validated tar entry beneath destRoot, preserving the entry's relative path.
func extractEntry(tr *tar.Reader, hdr *tar.Header, destRoot, rel string) (int64, error) {
	dest := filepath.Join(destRoot, filepath.FromSlash(rel))
	switch hdr.Typeflag {
	case tar.TypeDir:
		if info, err := os.Lstat(dest); err == nil {
			if !info.IsDir() {
				return 0, &BundleRejectedError{Reason: BundleRejectedDuplicate, Item: hdr.Name}
			}
			return 0, nil
		} else if duplicatePathError(err) {
			return 0, &BundleRejectedError{Reason: BundleRejectedDuplicate, Item: hdr.Name}
		} else if !os.IsNotExist(err) {
			return 0, err
		}
		if err := os.MkdirAll(dest, 0755); err != nil {
			if duplicatePathError(err) {
				return 0, &BundleRejectedError{Reason: BundleRejectedDuplicate, Item: hdr.Name}
			}
			return 0, err
		}
		return 0, nil
	case tar.TypeReg, tar.TypeRegA:
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			if duplicatePathError(err) {
				return 0, &BundleRejectedError{Reason: BundleRejectedDuplicate, Item: hdr.Name}
			}
			return 0, err
		}
		mode := fs.FileMode(hdr.Mode).Perm()
		if mode == 0 {
			mode = 0644
		}
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			if duplicatePathError(err) {
				return 0, &BundleRejectedError{Reason: BundleRejectedDuplicate, Item: hdr.Name}
			}
			return 0, err
		}
		n, err := io.Copy(f, tr)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return n, fmt.Errorf("extracting %s: %w", hdr.Name, err)
		}
		return n, nil
	default:
		return 0, validateEntryType(hdr)
	}
}

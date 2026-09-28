package release

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/parka/gorganizer/internal/fsutil"
	"golang.org/x/sys/unix"
)

const maxEntries = 20000
const maxUnpacked = int64(2 << 30)
const maxArchive = int64(1 << 30)

var digestPattern = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

// expectedDigest finds the sole checksum for the requested archive.
func expectedDigest(data []byte, archive string) (string, error) {
	if len(data) > 64<<10 {
		return "", fmt.Errorf("release checksums are too large")
	}
	var matching []string
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		fields := strings.SplitN(line, "  ", 2)
		if len(fields) != 2 || !digestPattern.MatchString(fields[0]) || strings.TrimPrefix(fields[1], "*") == "" || strings.ContainsAny(fields[1], "\r\n") {
			return "", fmt.Errorf("invalid release checksums")
		}
		if fields[1] == archive || fields[1] == "*"+archive {
			matching = append(matching, strings.ToLower(fields[0]))
		}
	}
	if len(matching) != 1 {
		return "", fmt.Errorf("release checksums must contain exactly one entry for %s", archive)
	}
	return matching[0], nil
}

// safeName validates an archive or manifest path without normalizing unsafe components.
func safeName(name string) error {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || strings.ContainsRune(name, 0) {
		return fmt.Errorf("unsafe release path %q", name)
	}
	for _, segment := range strings.Split(strings.TrimSuffix(name, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("unsafe release path %q", name)
		}
	}
	for _, c := range name {
		if c < 0x20 || c == 0x7f {
			return fmt.Errorf("unsafe release path %q", name)
		}
	}
	return nil
}

// safeLink checks that a relative symlink remains inside the release tree.
func safeLink(name, target string) error {
	if target == "" || strings.HasPrefix(target, "/") || strings.Contains(target, "\\") || strings.ContainsRune(target, 0) {
		return fmt.Errorf("unsafe release link %q", name)
	}
	for _, c := range target {
		if c < 0x20 || c == 0x7f {
			return fmt.Errorf("unsafe release link %q", name)
		}
	}
	joined := path.Clean(path.Join(path.Dir(name), target))
	if joined == ".." || strings.HasPrefix(joined, "../") || joined == "." {
		return fmt.Errorf("release link leaves its folder: %s", name)
	}
	return nil
}

// plainParents ensures every existing ancestor of an entry is a real directory.
func plainParents(root, relative string) error {
	parent := path.Dir(relative)
	if parent == "." {
		return nil
	}
	current := root
	for _, component := range strings.Split(parent, "/") {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("checking release folder: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("release folder is not a real directory: %s", relative)
		}
	}
	return nil
}

// unpack extracts a verified archive while rejecting unsafe entries and excessive contents.
func unpack(archive, stage, version string, wrap func(io.Writer) io.Writer) error {
	file, err := os.Open(archive)
	if err != nil {
		return fmt.Errorf("opening release archive: %w", err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("opening compressed release: %w", err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	prefix := "gorganizer-" + version
	seen := make(map[string]bool)
	var size int64
	for entries := 0; ; entries++ {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("reading release archive: %w", err)
		}
		if entries >= maxEntries {
			return fmt.Errorf("release has too many entries")
		}
		name := strings.TrimSuffix(header.Name, "/")
		if err := safeName(name); err != nil {
			return err
		}
		if name != prefix && !strings.HasPrefix(name, prefix+"/") {
			return fmt.Errorf("file outside the release: %s", name)
		}
		if seen[name] {
			return fmt.Errorf("duplicate release entry: %s", name)
		}
		seen[name] = true
		if header.Mode&0o7000 != 0 {
			return fmt.Errorf("unsafe permissions in release: %s", name)
		}
		if header.Size < 0 || header.Size > maxUnpacked-size {
			return fmt.Errorf("release is too large")
		}
		size += header.Size
		if name == prefix {
			if header.Typeflag != tar.TypeDir {
				return fmt.Errorf("invalid release root")
			}
			continue
		}
		rel := strings.TrimPrefix(name, prefix+"/")
		if err := plainParents(stage, rel); err != nil {
			return err
		}
		dest := filepath.Join(stage, filepath.FromSlash(rel))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.Mkdir(dest, 0o755); err != nil {
				return fmt.Errorf("creating release folder: %w", err)
			}
		case tar.TypeReg, tar.TypeRegA:
			fd, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY|unix.O_NOFOLLOW, fs.FileMode(header.Mode)&0o755)
			if err != nil {
				return fmt.Errorf("creating release file: %w", err)
			}
			var writer io.Writer = fd
			if wrap != nil {
				writer = wrap(fd)
			}
			_, copyErr := io.CopyN(writer, reader, header.Size)
			if copyErr == nil {
				copyErr = fd.Sync()
			}
			closeErr := fd.Close()
			if copyErr != nil {
				return fmt.Errorf("writing release file: %w", copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("closing release file: %w", closeErr)
			}
		case tar.TypeSymlink:
			if err := safeLink(rel, header.Linkname); err != nil {
				return err
			}
			if err := os.Symlink(header.Linkname, dest); err != nil {
				return fmt.Errorf("creating release link: %w", err)
			}
		default:
			return fmt.Errorf("unsupported release entry: %s", name)
		}
	}
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return fmt.Errorf("incomplete release archive: %w", err)
	}
	return nil
}

// copyTree adopts an extracted release through exclusive writes into a private stage.
func copyTree(source, stage string, wrap func(io.Writer) io.Writer) error {
	info, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("opening extracted release: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("extracted release must be a real folder")
	}
	count := 0
	var size int64
	return filepath.WalkDir(source, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == source {
			return nil
		}
		count++
		if count > maxEntries {
			return fmt.Errorf("release has too many entries")
		}
		rel, err := filepath.Rel(source, name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if err := safeName(rel); err != nil {
			return err
		}
		if err := plainParents(stage, rel); err != nil {
			return err
		}
		info, err := os.Lstat(name)
		if err != nil {
			return err
		}
		if info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return fmt.Errorf("unsafe permissions in release: %s", rel)
		}
		dest := filepath.Join(stage, rel)
		switch {
		case info.IsDir():
			return os.Mkdir(dest, 0o755)
		case info.Mode().IsRegular():
			if info.Size() > maxUnpacked-size {
				return fmt.Errorf("release is too large")
			}
			size += info.Size()
			from, err := os.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW, 0)
			if err != nil {
				return err
			}
			defer from.Close()
			to, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY|unix.O_NOFOLLOW, info.Mode().Perm()&0o755)
			if err != nil {
				return err
			}
			var writer io.Writer = to
			if wrap != nil {
				writer = wrap(to)
			}
			_, err = io.CopyN(writer, from, info.Size())
			if err == nil {
				err = to.Sync()
			}
			closeErr := to.Close()
			if err != nil {
				return err
			}
			return closeErr
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(name)
			if err != nil {
				return err
			}
			if err := safeLink(rel, target); err != nil {
				return err
			}
			return os.Symlink(target, dest)
		default:
			return fmt.Errorf("unsupported release entry: %s", rel)
		}
	})
}

// manifestLines parses the bundle manifest as unique SHA-256 checksums of safe paths.
func manifestLines(data []byte) (map[string]string, error) {
	if len(data) > 4<<20 {
		return nil, fmt.Errorf("release manifest is too large")
	}
	result := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) != 2 || !digestPattern.MatchString(parts[0]) || safeName(parts[1]) != nil || parts[1] == "MANIFEST.sha256" || strings.ContainsAny(parts[1], "\r\n") {
			return nil, fmt.Errorf("invalid release manifest")
		}
		if _, exists := result[parts[1]]; exists {
			return nil, fmt.Errorf("duplicate release manifest entry")
		}
		result[parts[1]] = strings.ToLower(parts[0])
	}
	return result, nil
}

// checkBundle verifies files, links, metadata and the packaged executables.
func checkBundle(ctx context.Context, stage, version string) error {
	root, err := os.Lstat(stage)
	if err != nil {
		return fmt.Errorf("opening release folder: %w", err)
	}
	if !root.IsDir() {
		return fmt.Errorf("release is not a real folder")
	}
	manifestPath := filepath.Join(stage, "MANIFEST.sha256")
	info, err := os.Lstat(manifestPath)
	if err != nil {
		return fmt.Errorf("missing release manifest: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return fmt.Errorf("invalid release manifest")
	}
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("reading release manifest: %w", err)
	}
	entries, err := manifestLines(manifest)
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	err = filepath.WalkDir(stage, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == stage {
			return nil
		}
		rel, err := filepath.Rel(stage, name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if err := safeName(rel); err != nil {
			return err
		}
		info, err := os.Lstat(name)
		if err != nil {
			return err
		}
		if info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return fmt.Errorf("unsafe permissions: %s", rel)
		}
		switch {
		case info.IsDir():
			return nil
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(name)
			if err != nil {
				return err
			}
			if err := safeLink(rel, target); err != nil {
				return err
			}
			resolved, err := filepath.EvalSymlinks(name)
			if err != nil {
				return fmt.Errorf("invalid release link %s: %w", rel, err)
			}
			if !fsutil.ContainedBy(stage, resolved) {
				return fmt.Errorf("release link leaves its folder: %s", rel)
			}
			return nil
		case info.Mode().IsRegular():
			if rel == "MANIFEST.sha256" {
				return nil
			}
			expected, ok := entries[rel]
			if !ok {
				return fmt.Errorf("unlisted release file: %s", rel)
			}
			seen[rel] = true
			fd, err := os.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW, 0)
			if err != nil {
				return err
			}
			var digest hash.Hash = sha256.New()
			_, err = io.Copy(digest, fd)
			closeErr := fd.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			if hex.EncodeToString(digest.Sum(nil)) != expected {
				return fmt.Errorf("release file does not match manifest: %s", rel)
			}
			return nil
		default:
			return fmt.Errorf("unsupported release entry: %s", rel)
		}
	})
	if err != nil {
		return err
	}
	for name := range entries {
		if !seen[name] {
			return fmt.Errorf("missing release file: %s", name)
		}
	}
	metadataPath := filepath.Join(stage, "release.json")
	metadataInfo, err := os.Lstat(metadataPath)
	if err != nil {
		return fmt.Errorf("missing release information: %w", err)
	}
	if !metadataInfo.Mode().IsRegular() || metadataInfo.Size() > 1<<20 {
		return fmt.Errorf("invalid release information")
	}
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return fmt.Errorf("reading release information: %w", err)
	}
	var metadata struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &metadata) != nil || metadata.Version != version {
		return fmt.Errorf("release version does not match %s", version)
	}
	for _, required := range []struct {
		name       string
		executable bool
	}{
		{"gorganizer.sh", true}, {"bin/gorganizer-gui", true}, {"resources/icons/tmp_logo.png", false},
	} {
		info, err := os.Lstat(filepath.Join(stage, required.name))
		if err != nil || !info.Mode().IsRegular() || required.executable && info.Mode().Perm()&0o111 == 0 {
			return fmt.Errorf("missing release file: %s", required.name)
		}
	}
	for _, name := range []string{"gorganizerctl", "gorganizerd"} {
		binary := filepath.Join(stage, "bin", name)
		info, err := os.Lstat(binary)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			return fmt.Errorf("missing executable: %s", name)
		}
		checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		output, err := exec.CommandContext(checkCtx, binary, "--version").Output()
		cancel()
		if err != nil || !strings.HasPrefix(strings.TrimSpace(string(output)), name+" "+version) {
			return fmt.Errorf("release executable failed its version check: %s", name)
		}
	}
	return nil
}

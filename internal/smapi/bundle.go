package smapi

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"

	"golang.org/x/sys/unix"

	"github.com/parka/gorganizer/internal/fsutil"
	"github.com/parka/gorganizer/internal/ghrelease"
)

const (
	maxArchiveBytes   int64 = 512 << 20
	maxArchiveEntries       = 20000
	maxManifestBytes  int64 = 1 << 20
	payloadIgnoredDir       = "mcs"
)

type Bundle struct {
	Root          string
	InstallerPath string
	PayloadPath   string
	payload       []byte
}

type Payload struct {
	Root     map[string]PayloadFile
	RootDirs []string
	Mods     map[string]PayloadFile
	Launcher PayloadFile
}

// OpenBundle reads the artifact archive into memory through its verified descriptor, re-hashes those exact bytes, and extracts them freshly into dest.
func OpenBundle(art Artifact, dest string, spec LoaderSpec) (Bundle, error) {
	if err := spec.Validate(); err != nil {
		return Bundle{}, err
	}
	data, err := readVerifiedArchive(art)
	if err != nil {
		return Bundle{}, err
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Bundle{}, fmt.Errorf("reading SMAPI %s archive: %w", art.Version, err)
	}
	if err := os.MkdirAll(dest, 0755); err != nil {
		return Bundle{}, fmt.Errorf("creating bundle directory: %w", err)
	}
	top, err := extractArchive(reader, dest)
	if err != nil {
		return Bundle{}, fmt.Errorf("extracting SMAPI %s archive: %w", art.Version, err)
	}
	root := filepath.Join(dest, top)
	bundle := Bundle{Root: root}
	for _, target := range []struct {
		rel string
		out *string
	}{
		{spec.InstallerRelPath, &bundle.InstallerPath},
		{spec.PayloadRelPath, &bundle.PayloadPath},
	} {
		full, err := fsutil.SafeJoin(root, target.rel, false)
		if err != nil {
			return Bundle{}, fmt.Errorf("SMAPI bundle path %q: %w", target.rel, err)
		}
		if !isRegularFile(full) {
			return Bundle{}, fmt.Errorf("SMAPI bundle is missing regular file %q", target.rel)
		}
		*target.out = full
	}
	if bundle.payload, err = archiveEntryBytes(reader, top+"/"+spec.PayloadRelPath); err != nil {
		return Bundle{}, err
	}
	if err := os.Chmod(bundle.InstallerPath, 0755); err != nil {
		return Bundle{}, fmt.Errorf("marking SMAPI installer executable: %w", err)
	}
	return bundle, nil
}

// Payload indexes the verified in-memory loader payload of the bundle.
func (b Bundle) Payload(spec LoaderSpec) (Payload, error) {
	if b.payload == nil {
		return Payload{}, errors.New("bundle carries no verified loader payload")
	}
	return indexPayload(b.payload, spec)
}

// readVerifiedArchive reads the artifact through the descriptor ghrelease verified and re-hashes exactly the bytes it returns.
func readVerifiedArchive(art Artifact) ([]byte, error) {
	file, size, err := ghrelease.OpenVerified(art.ZipPath, art.SHA256)
	if err != nil {
		return nil, fmt.Errorf("verifying SMAPI %s archive: %w", art.Version, err)
	}
	defer file.Close()
	if size > maxArchiveBytes {
		return nil, fmt.Errorf("SMAPI %s archive exceeds %d bytes: %w", art.Version, maxArchiveBytes, ghrelease.ErrTooLarge)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxArchiveBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading SMAPI %s archive: %w", art.Version, err)
	}
	if int64(len(data)) > maxArchiveBytes {
		return nil, fmt.Errorf("SMAPI %s archive exceeds %d bytes: %w", art.Version, maxArchiveBytes, ghrelease.ErrTooLarge)
	}
	sum := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), art.SHA256) {
		return nil, fmt.Errorf("SMAPI %s archive changed after verification: %w", art.Version, ghrelease.ErrDigestMismatch)
	}
	return data, nil
}

// archiveEntryBytes returns the content of the single regular entry called name, bounded by the archive budget.
func archiveEntryBytes(reader *zip.Reader, name string) ([]byte, error) {
	for _, entry := range reader.File {
		if entry.Name != name {
			continue
		}
		src, err := entry.Open()
		if err != nil {
			return nil, fmt.Errorf("opening archive entry %q: %w", name, err)
		}
		defer src.Close()
		data, err := io.ReadAll(io.LimitReader(src, maxArchiveBytes+1))
		if err != nil {
			return nil, fmt.Errorf("reading archive entry %q: %w", name, err)
		}
		if int64(len(data)) > maxArchiveBytes {
			return nil, fmt.Errorf("archive entry %q exceeds %d bytes: %w", name, maxArchiveBytes, ghrelease.ErrTooLarge)
		}
		return data, nil
	}
	return nil, fmt.Errorf("SMAPI bundle has no %q entry", name)
}

// extractArchive safely extracts every entry of reader into dest and returns the single top-level directory name.
func extractArchive(reader *zip.Reader, dest string) (string, error) {
	if len(reader.File) > maxArchiveEntries {
		return "", fmt.Errorf("archive has more than %d entries", maxArchiveEntries)
	}
	top := ""
	var total int64
	for _, entry := range reader.File {
		rel, isDir, err := archiveEntryPath(entry)
		if err != nil {
			return "", err
		}
		segments := strings.Split(rel, "/")
		if len(segments) == 1 && !isDir {
			return "", fmt.Errorf("archive has top-level file %q", rel)
		}
		if top == "" {
			top = segments[0]
		} else if segments[0] != top {
			return "", fmt.Errorf("archive has more than one top-level directory (%q, %q)", top, segments[0])
		}
		target, err := fsutil.SafeJoin(dest, rel, false)
		if err != nil {
			return "", fmt.Errorf("archive entry %q: %w", rel, err)
		}
		if isDir {
			if err := os.MkdirAll(target, 0755); err != nil {
				return "", err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return "", err
		}
		written, err := extractFile(entry, target, maxArchiveBytes-total)
		total += written
		if err != nil {
			return "", err
		}
	}
	if top == "" {
		return "", errors.New("archive is empty")
	}
	return top, nil
}

// extractFile writes one archive entry to the new file target, failing once more than budget bytes are produced.
func extractFile(entry *zip.File, target string, budget int64) (int64, error) {
	src, err := entry.Open()
	if err != nil {
		return 0, fmt.Errorf("opening archive entry %q: %w", entry.Name, err)
	}
	defer src.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0644)
	if err != nil {
		return 0, err
	}
	written, copyErr := io.Copy(out, io.LimitReader(src, budget+1))
	closeErr := out.Close()
	if copyErr != nil {
		return written, fmt.Errorf("extracting archive entry %q: %w", entry.Name, copyErr)
	}
	if written > budget {
		return written, fmt.Errorf("archive exceeds %d extracted bytes: %w", maxArchiveBytes, ghrelease.ErrTooLarge)
	}
	if closeErr != nil {
		return written, fmt.Errorf("closing extracted entry %q: %w", entry.Name, closeErr)
	}
	return written, nil
}

// archiveEntryPath validates an archive entry name and type, returning its clean slash path and whether it is a directory.
func archiveEntryPath(entry *zip.File) (string, bool, error) {
	name := entry.Name
	mode := entry.Mode()
	isDir := strings.HasSuffix(name, "/") || mode.IsDir()
	if !isDir && !mode.IsRegular() {
		return "", false, fmt.Errorf("archive entry %q is not a regular file or directory", name)
	}
	if strings.Contains(name, `\`) || strings.HasPrefix(name, "/") || strings.ContainsFunc(name, unicode.IsControl) {
		return "", false, fmt.Errorf("archive entry %q has an unsafe name", name)
	}
	rel := strings.TrimSuffix(name, "/")
	if rel == "" || path.Clean(rel) != rel {
		return "", false, fmt.Errorf("archive entry %q has an unsafe name", name)
	}
	for _, segment := range strings.Split(rel, "/") {
		if segment == "." || segment == ".." || segment == "" {
			return "", false, fmt.Errorf("archive entry %q has an unsafe name", name)
		}
	}
	return rel, isDir, nil
}

// ReadPayload indexes the loader payload archive at payloadPath in memory without extracting it.
func ReadPayload(payloadPath string, spec LoaderSpec) (Payload, error) {
	data, err := readSmallRegular(payloadPath, maxArchiveBytes)
	if err != nil {
		return Payload{}, fmt.Errorf("opening loader payload: %w", err)
	}
	return indexPayload(data, spec)
}

// indexPayload indexes in-memory loader payload bytes into root files, bundled mods, and the launcher.
func indexPayload(data []byte, spec LoaderSpec) (Payload, error) {
	if err := spec.Validate(); err != nil {
		return Payload{}, err
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Payload{}, fmt.Errorf("reading loader payload: %w", err)
	}
	if len(reader.File) > maxArchiveEntries {
		return Payload{}, fmt.Errorf("loader payload has more than %d entries", maxArchiveEntries)
	}
	payload := Payload{Root: map[string]PayloadFile{}, Mods: map[string]PayloadFile{}}
	dirs := map[string]bool{}
	modFiles := map[string]map[string]PayloadFile{}
	manifests := map[string][]byte{}
	launcherSeen := false
	var total int64
	for _, entry := range reader.File {
		rel, isDir, err := archiveEntryPath(entry)
		if err != nil {
			return Payload{}, fmt.Errorf("loader payload: %w", err)
		}
		if err := validateRelPath(rel); err != nil {
			return Payload{}, fmt.Errorf("loader payload entry: %w", err)
		}
		if isDir {
			continue
		}
		segments := strings.Split(rel, "/")
		isManifest := len(segments) == 3 && segments[0] == modsDirName && strings.EqualFold(segments[2], manifestFileName)
		file, content, err := hashEntry(entry, maxArchiveBytes-total, isManifest)
		total += file.Size
		if err != nil {
			return Payload{}, err
		}
		switch {
		case rel == spec.LauncherPayloadName:
			payload.Launcher = file
			launcherSeen = true
		case segments[0] == modsDirName:
			if len(segments) < 3 {
				continue
			}
			if modFiles[segments[1]] == nil {
				modFiles[segments[1]] = map[string]PayloadFile{}
			}
			modFiles[segments[1]][rel] = file
			if isManifest {
				manifests[segments[1]] = content
			}
		case segments[0] == payloadIgnoredDir:
			continue
		default:
			payload.Root[rel] = file
			for dir := path.Dir(rel); dir != "."; dir = path.Dir(dir) {
				dirs[dir] = true
			}
		}
	}
	if !launcherSeen {
		return Payload{}, fmt.Errorf("loader payload has no %s", spec.LauncherPayloadName)
	}
	if _, ok := payload.Root[spec.LoaderExecutable]; !ok {
		return Payload{}, fmt.Errorf("loader payload has no %s", spec.LoaderExecutable)
	}
	reserved := spec.protectedNames()
	for _, name := range []string{spec.LauncherName, spec.LauncherBackupName, spec.LoaderDepsFile} {
		reserved[name] = true
	}
	for rel := range payload.Root {
		if reserved[strings.SplitN(rel, "/", 2)[0]] || rel == userConfigRel {
			return Payload{}, fmt.Errorf("loader payload unexpectedly ships %s", rel)
		}
	}
	for folder, files := range modFiles {
		if !isBundledManifest(manifests[folder], spec.BundledModIDs) {
			continue
		}
		for rel, file := range files {
			payload.Mods[rel] = file
		}
	}
	payload.RootDirs = sortedSet(dirs)
	return payload, nil
}

// hashEntry hashes one payload entry within budget and optionally returns its content.
func hashEntry(entry *zip.File, budget int64, keep bool) (PayloadFile, []byte, error) {
	src, err := entry.Open()
	if err != nil {
		return PayloadFile{}, nil, fmt.Errorf("opening payload entry %q: %w", entry.Name, err)
	}
	defer src.Close()
	hash := sha256.New()
	var kept strings.Builder
	var sink io.Writer = hash
	if keep {
		sink = io.MultiWriter(hash, &limitedBuilder{b: &kept, limit: maxManifestBytes})
	}
	size, err := io.Copy(sink, io.LimitReader(src, budget+1))
	file := PayloadFile{SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size}
	if err != nil {
		return file, nil, fmt.Errorf("reading payload entry %q: %w", entry.Name, err)
	}
	if size > budget {
		return file, nil, fmt.Errorf("loader payload exceeds %d bytes: %w", maxArchiveBytes, ghrelease.ErrTooLarge)
	}
	if !keep {
		return file, nil, nil
	}
	return file, []byte(kept.String()), nil
}

type limitedBuilder struct {
	b     *strings.Builder
	limit int64
}

// Write keeps bytes up to the limit and silently discards the rest.
func (l *limitedBuilder) Write(p []byte) (int, error) {
	room := l.limit - int64(l.b.Len())
	if room > 0 {
		if int64(len(p)) > room {
			l.b.Write(p[:room])
		} else {
			l.b.Write(p)
		}
	}
	return len(p), nil
}

// isBundledManifest reports whether manifest parses and names one of the allowed bundled mod IDs, compared case-sensitively like the upstream installer.
func isBundledManifest(manifest []byte, allowed []string) bool {
	if manifest == nil {
		return false
	}
	parsed, err := ParseManifest(manifest)
	if err != nil || parsed == nil {
		return false
	}
	return contains(allowed, parsed.UniqueID)
}

// payloadTopLevel returns the sorted distinct top-level names of the payload root files.
func payloadTopLevel(p Payload) []string {
	set := map[string]bool{}
	for rel := range p.Root {
		set[strings.SplitN(rel, "/", 2)[0]] = true
	}
	return sortedSet(set)
}

// payloadModFolders returns the sorted distinct Mods/<folder> paths of the bundled mods.
func payloadModFolders(p Payload) []string {
	set := map[string]bool{}
	for rel := range p.Mods {
		segments := strings.SplitN(rel, "/", 3)
		set[segments[0]+"/"+segments[1]] = true
	}
	return sortedSet(set)
}

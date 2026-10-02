package release

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/fsutil"
	"golang.org/x/sys/unix"
)

type Manager struct {
	Root         string
	Trust        *TrustSet
	RuntimeDir   string
	Source       Source
	WrapWriter   func(io.Writer) io.Writer
	AfterPublish func() error
}

type Status struct {
	Current   string
	Previous  string
	Available []string
	InUse     string
}

// DataRoot returns the user's release generations folder.
func DataRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("finding your home folder: %w", err)
	}
	data := os.Getenv("XDG_DATA_HOME")
	if !filepath.IsAbs(data) {
		data = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(data, "gorganizer", "releases"), nil
}

// ValidateTag checks the version syntax of a published release tag.
func ValidateTag(tag string) error {
	if !tagPattern.MatchString(tag) {
		return fmt.Errorf("invalid release tag %q", tag)
	}
	return nil
}

// Compare compares two nonnegative dotted version numbers without integer overflow.
func Compare(first, second string) (int, error) {
	if !tagPattern.MatchString("v"+first) || !tagPattern.MatchString("v"+second) {
		return 0, fmt.Errorf("invalid release version")
	}
	a, b := strings.Split(first, "."), strings.Split(second, ".")
	for i := range a {
		x, y := strings.TrimLeft(a[i], "0"), strings.TrimLeft(b[i], "0")
		if len(x) != len(y) {
			if len(x) > len(y) {
				return 1, nil
			}
			return -1, nil
		}
		if x != y {
			if x > y {
				return 1, nil
			}
			return -1, nil
		}
	}
	return 0, nil
}

// BaseVersion extracts a bounded release version from build metadata.
func BaseVersion(v string) (string, bool) {
	base, _, _ := strings.Cut(v, "+")
	if ValidateTag("v"+base) != nil {
		return "", false
	}
	return base, true
}

// NotesURL returns the release notes page for a validated tag.
func NotesURL(tag string) (string, error) {
	if err := configError(); err != nil {
		return "", err
	}
	if err := ValidateTag(tag); err != nil {
		return "", err
	}
	return notesURLBase + tag, nil
}

// rootPath resolves a configured or default releases folder.
func (m *Manager) rootPath() (string, error) {
	if m.Root != "" {
		return filepath.Abs(m.Root)
	}
	return DataRoot()
}

// prepareRoot creates and checks the owned real releases directory.
func (m *Manager) prepareRoot() (string, error) {
	root, err := m.rootPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		return "", fmt.Errorf("creating application data folder: %w", err)
	}
	if err := os.Mkdir(root, 0o755); err == nil {
		if err := os.Chmod(root, 0o755); err != nil {
			return "", fmt.Errorf("setting releases folder permissions: %w", err)
		}
	} else if !errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("creating releases folder: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || int(stat.Uid) != os.Getuid() || info.Mode().Perm() != 0o755 {
		return "", fmt.Errorf("the releases folder must be a normal folder owned by you with standard permissions")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(root))
	if err != nil {
		return "", fmt.Errorf("resolving application data folder: %w", err)
	}
	physicalRoot := filepath.Join(parent, filepath.Base(root))
	path := string(os.PathSeparator)
	for _, component := range strings.Split(strings.TrimPrefix(physicalRoot, path), path) {
		if component != "" {
			path = filepath.Join(path, component)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return "", fmt.Errorf("checking releases folder ancestry %s: %w", path, err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !info.IsDir() || !ok || (stat.Uid != 0 && int(stat.Uid) != os.Getuid()) ||
			(info.Mode().Perm()&0o022 != 0 && !(stat.Uid == 0 && info.Mode()&os.ModeSticky != 0)) ||
			((path == parent || path == physicalRoot) && int(stat.Uid) != os.Getuid()) {
			return "", fmt.Errorf("the releases folder is inside a folder other users can change: %s", path)
		}
	}
	return physicalRoot, nil
}

type heldLock struct{ root string }

var installDeadline = 30 * time.Minute
var renameLink = os.Rename
var syncReleaseDir = atomicfile.SyncDir

// withLock serializes changes to the release generations and clears abandoned stages.
func (m *Manager) withLock(ctx context.Context, action func(*heldLock) error) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("waiting for release lock: %w", err)
	}
	root, err := m.prepareRoot()
	if err != nil {
		return err
	}
	fd, err := unix.Open(filepath.Join(root, ".release.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("opening release lock: %w", err)
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 || stat.Mode&0o077 != 0 {
		return fmt.Errorf("the releases folder contains an unsafe lock file")
	}
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("waiting for release lock: %w", err)
		}
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			return fmt.Errorf("locking releases: %w", err)
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("waiting for release lock: %w", ctx.Err())
		case <-timer.C:
		}
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	if err := sweep(root); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return action(&heldLock{root})
}

// sweep removes abandoned stages and temporary links under the exclusive lock.
func sweep(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".stage-") || strings.HasPrefix(name, ".link-") {
			if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
				return fmt.Errorf("removing abandoned release stage: %w", err)
			}
		}
	}
	return syncReleaseDir(root)
}

// newStage creates an unpredictable staging folder while holding the release lock.
func newStage(lock *heldLock) (string, error) {
	root := lock.root
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	name := filepath.Join(root, fmt.Sprintf(".stage-%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]))
	if err := os.Mkdir(name, 0o700); err != nil {
		return "", fmt.Errorf("creating release stage: %w", err)
	}
	if err := fsutil.EnsurePrivateDir(name); err != nil {
		return "", err
	}
	return name, nil
}

// Install downloads and publishes one fully verified release.
func (m *Manager) Install(ctx context.Context, tag string) (string, error) {
	version, _, err := m.install(ctx, tag, false)
	return version, err
}

// Update installs a strictly newer release without racing other release switches.
func (m *Manager) Update(ctx context.Context, tag string) (string, bool, error) {
	return m.install(ctx, tag, true)
}

// install resolves a tag and optionally enforces a newer version under the release lock.
func (m *Manager) install(ctx context.Context, tag string, newerOnly bool) (string, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, installDeadline)
	defer cancel()
	resolved, err := m.Source.ResolveTag(ctx, tag)
	if err != nil {
		return "", false, err
	}
	version := strings.TrimPrefix(resolved, "v")
	changed := false
	err = m.withLock(ctx, func(lock *heldLock) error {
		root := lock.root
		if newerOnly {
			current, err := linkTarget(root, "current")
			if err != nil {
				return err
			}
			if current != "" {
				order, err := Compare(version, current)
				if err != nil {
					return err
				}
				if order < 0 {
					return fmt.Errorf("an older release cannot replace your current version")
				}
				if order == 0 {
					return nil
				}
			}
		}
		trust := m.Trust
		if trust == nil {
			keys, err := ProductionTrust()
			if err != nil {
				return err
			}
			trust = &keys
		}
		archive := "gorganizer-" + version + "-linux-x86_64.tar.gz"
		checksURL, err := m.Source.AssetURL(resolved, "SHA256SUMS")
		if err != nil {
			return err
		}
		checks, err := m.Source.fetch(ctx, checksURL, 64<<10)
		if err != nil {
			return fmt.Errorf("getting release checksums: %w", err)
		}
		sigURL, err := m.Source.AssetURL(resolved, "SHA256SUMS.sig")
		if err != nil {
			return err
		}
		sig, err := m.Source.fetch(ctx, sigURL, 2<<10)
		if err != nil {
			var status *StatusError
			if errors.As(err, &status) && status.Code == 404 {
				return fmt.Errorf("this release is not signed; nothing was installed")
			}
			return fmt.Errorf("getting release signature: %w", err)
		}
		if err := VerifySums(*trust, resolved, checks, sig); err != nil {
			return err
		}
		expected, err := expectedDigest(checks, archive)
		if err != nil {
			return err
		}
		stage, err := newStage(lock)
		if err != nil {
			return err
		}
		defer os.RemoveAll(stage)
		archiveURL, err := m.Source.AssetURL(resolved, archive)
		if err != nil {
			return err
		}
		response, err := m.Source.open(ctx, archiveURL)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.ContentLength > maxArchive {
			return fmt.Errorf("release download is too large")
		}
		target := filepath.Join(stage, "archive.tmp")
		fd, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0o600)
		if err != nil {
			return fmt.Errorf("creating release download: %w", err)
		}
		writer := io.Writer(fd)
		if m.WrapWriter != nil {
			writer = m.WrapWriter(fd)
		}
		hasher := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(writer, hasher), io.LimitReader(contextReader{ctx, archiveBodyReader{response.Request.Context(), response.Body}}, maxArchive+1))
		if copyErr == nil {
			copyErr = fd.Sync()
		}
		closeErr := fd.Close()
		if copyErr != nil {
			return fmt.Errorf("downloading release archive: %w", copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("closing release download: %w", closeErr)
		}
		if n > maxArchive {
			return fmt.Errorf("release download is too large")
		}
		if response.ContentLength >= 0 && n != response.ContentLength {
			return fmt.Errorf("release download is incomplete")
		}
		if hex.EncodeToString(hasher.Sum(nil)) != expected {
			return fmt.Errorf("release download failed its checksum check")
		}
		if err := unpack(ctx, target, stage, version, m.WrapWriter); err != nil {
			return err
		}
		if err := os.Remove(target); err != nil {
			return err
		}
		if err := m.publish(ctx, root, stage, version); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return version, changed, err
}

// Adopt copies and verifies an already extracted bundle before publication.
func (m *Manager) Adopt(ctx context.Context, source string) (string, error) {
	info, err := os.Lstat(source)
	if err != nil {
		return "", fmt.Errorf("opening extracted release: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("extracted release must be a real folder")
	}
	metadataFile := filepath.Join(source, "release.json")
	info, err = os.Lstat(metadataFile)
	if err != nil {
		return "", fmt.Errorf("reading release information: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return "", fmt.Errorf("invalid release information")
	}
	data, err := os.ReadFile(metadataFile)
	if err != nil {
		return "", fmt.Errorf("reading release information: %w", err)
	}
	var metadata struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return "", err
	}
	version := metadata.Version
	if err := ValidateTag("v" + version); err != nil {
		return "", err
	}
	err = m.withLock(ctx, func(lock *heldLock) error {
		root := lock.root
		stage, err := newStage(lock)
		if err != nil {
			return err
		}
		defer os.RemoveAll(stage)
		if err := copyTree(ctx, source, stage, m.WrapWriter); err != nil {
			return fmt.Errorf("copying release: %w", err)
		}
		return m.publish(ctx, root, stage, version)
	})
	return version, err
}

// publish verifies a staged generation and switches the active release.
func (m *Manager) publish(ctx context.Context, root, stage, version string) error {
	if err := checkBundle(ctx, stage, version); err != nil {
		return err
	}
	if err := atomicfile.SyncFilesystem(stage); err != nil {
		return fmt.Errorf("saving release files: %w", err)
	}
	destination := filepath.Join(root, version)
	commitCtx := context.WithoutCancel(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("publishing release: %w", err)
		}
		info, statErr := os.Lstat(destination)
		if statErr != nil {
			return fmt.Errorf("checking existing release: %w", statErr)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !info.IsDir() || !ok || int(stat.Uid) != os.Getuid() {
			return fmt.Errorf("an existing release is not an owned real folder")
		}
		if err := existingMatches(ctx, stage, destination); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	} else {
		if err := syncReleaseDir(root); err != nil {
			return err
		}
	}
	if m.AfterPublish != nil {
		if err := m.AfterPublish(); err != nil {
			return err
		}
	}
	if err := switchTo(root, version); err != nil {
		return err
	}
	return m.collect(commitCtx, root)
}

// linkTarget returns a version symlink target without following arbitrary links.
func linkTarget(root, name string) (string, error) {
	target, err := os.Readlink(filepath.Join(root, name))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("checking release %s: %w", name, err)
	}
	if ValidateTag("v"+target) != nil {
		return "", nil
	}
	info, err := os.Lstat(filepath.Join(root, target))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("checking release %s target: %w", name, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || int(stat.Uid) != os.Getuid() {
		return "", nil
	}
	return target, nil
}

// replaceLink atomically publishes a relative version symlink.
func replaceLink(root, name, target string) error {
	if err := ValidateTag("v" + target); err != nil {
		return err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	tmp := filepath.Join(root, ".link-"+hex.EncodeToString(id[:]))
	if err := os.Symlink(target, tmp); err != nil {
		return fmt.Errorf("creating release link: %w", err)
	}
	defer os.Remove(tmp)
	if err := renameLink(tmp, filepath.Join(root, name)); err != nil {
		return fmt.Errorf("switching release link: %w", err)
	}
	return syncReleaseDir(root)
}

// switchTo keeps the old current release as previous before switching current.
func switchTo(root, version string) error {
	current, err := linkTarget(root, "current")
	if err != nil {
		return err
	}
	if current == version {
		return nil
	}
	if current != "" {
		if err := replaceLink(root, "previous", current); err != nil {
			return err
		}
	}
	return replaceLink(root, "current", version)
}

// Rollback switches to the previous verified release.
func (m *Manager) Rollback(ctx context.Context) (string, error) {
	var restored string
	err := m.withLock(ctx, func(lock *heldLock) error {
		root := lock.root
		previous, err := linkTarget(root, "previous")
		if err != nil {
			return err
		}
		if previous == "" {
			return fmt.Errorf("there is no previous release to restore")
		}
		current, err := linkTarget(root, "current")
		if err != nil {
			return err
		}
		if current == previous {
			return fmt.Errorf("there is no previous release to restore")
		}
		if err := checkBundle(ctx, filepath.Join(root, previous), previous); err != nil {
			return fmt.Errorf("previous release is not valid: %w", err)
		}
		commitCtx := context.WithoutCancel(ctx)
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := replaceLink(root, "current", previous); err != nil {
			return err
		}
		if current != "" {
			if err := replaceLink(root, "previous", current); err != nil {
				return err
			}
		}
		restored = previous
		return m.collect(commitCtx, root)
	})
	return restored, err
}

// SessionMarker returns the runtime path containing the running session's release directory.
func (m *Manager) SessionMarker() string {
	if m.RuntimeDir != "" {
		return filepath.Join(m.RuntimeDir, "session-release")
	}
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		return ""
	}
	return filepath.Join(base, "gorganizer", "session-release")
}

// Status reads the active, previous, installed and running releases.
func (m *Manager) Status() (Status, error) {
	return m.StatusContext(context.Background())
}

// StatusContext reads release status while observing cancellation during the lock wait.
func (m *Manager) StatusContext(ctx context.Context) (Status, error) {
	var result Status
	err := m.withLock(ctx, func(lock *heldLock) error {
		root := lock.root
		var err error
		result.Current, err = linkTarget(root, "current")
		if err != nil {
			return err
		}
		result.Previous, err = linkTarget(root, "previous")
		if err != nil {
			return err
		}
		result.InUse = m.inUse(root)
		entries, err := os.ReadDir(root)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !entry.IsDir() || ValidateTag("v"+entry.Name()) != nil {
				continue
			}
			result.Available = append(result.Available, entry.Name())
		}
		sort.Slice(result.Available, func(i, j int) bool { order, _ := Compare(result.Available[i], result.Available[j]); return order > 0 })
		return nil
	})
	return result, err
}

// inUse reports a marked version only while its session lock is held.
func (m *Manager) inUse(root string) string {
	marker := m.SessionMarker()
	if marker == "" {
		return ""
	}
	fd, err := unix.Open(filepath.Join(filepath.Dir(marker), "session.lock"), unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return ""
	}
	defer unix.Close(fd)
	if unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB) == nil {
		unix.Flock(fd, unix.LOCK_UN)
		return ""
	}
	content, err := os.ReadFile(marker)
	if err != nil {
		return ""
	}
	version := filepath.Base(strings.TrimSpace(string(content)))
	if ValidateTag("v"+version) != nil || strings.TrimSpace(string(content)) != filepath.Join(root, version) {
		return ""
	}
	return version
}

// collect removes obsolete verified generations and old staging directories.
func (m *Manager) collect(ctx context.Context, root string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := linkTarget(root, "current")
	if err != nil {
		return err
	}
	previous, err := linkTarget(root, "previous")
	if err != nil {
		return err
	}
	used := m.inUse(root)
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() {
			continue
		}
		if name == current || name == previous || name == used {
			continue
		}
		if strings.HasPrefix(name, ".stage-") {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if time.Since(info.ModTime()) < time.Hour {
				continue
			}
		} else if ValidateTag("v"+name) != nil {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			return fmt.Errorf("removing old release: %w", err)
		}
	}
	return syncReleaseDir(root)
}

// Collect removes unused release generations and abandoned stages.
func (m *Manager) Collect() error {
	return m.withLock(context.Background(), func(lock *heldLock) error {
		return m.collect(context.Background(), lock.root)
	})
}

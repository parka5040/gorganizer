package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/atomicfile"
	"golang.org/x/sys/unix"
)

// TestUnpackBoundsTrailingGzipData checks that the post-tar gzip drain has a hard limit.
func TestUnpackBoundsTrailingGzipData(t *testing.T) {
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	tarWriter := tar.NewWriter(gz)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "gorganizer-1.2.3", Mode: 0o755, Typeflag: tar.TypeDir}); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(gz, bytes.NewReader(make([]byte, 65<<20)), 65<<20); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "release.tar.gz")
	if err := os.WriteFile(archive, compressed.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unpack(context.Background(), archive, t.TempDir(), "1.2.3", nil); err == nil || !strings.Contains(err.Error(), "unexpected trailing data") {
		t.Fatalf("trailing gzip: %v", err)
	}
}

// TestInstallRequiresValidSignatureBeforeArchive checks unsigned and altered releases never download or execute an archive.
func TestInstallRequiresValidSignatureBeforeArchive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct {
		name, signature string
		want            string
	}{{"missing", "", "not signed"}, {"malformed", "bad\n", "signature"}, {"wrong tag", "wrong", "signature"}} {
		t.Run(tc.name, func(t *testing.T) {
			version := "1.2.3"
			checks := checksumLine(version, []byte("archive"))
			var archives int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch filepath.Base(r.URL.Path) {
				case "SHA256SUMS":
					io.WriteString(w, checks)
				case "SHA256SUMS.sig":
					if tc.signature == "" {
						http.NotFound(w, r)
					} else if tc.signature == "wrong" {
						w.Write(testSignature(1, "v1.2.4", []byte(checks)))
					} else {
						io.WriteString(w, tc.signature)
					}
				default:
					archives++
					w.Write([]byte("archive"))
				}
			}))
			defer server.Close()
			m := &Manager{Root: filepath.Join(t.TempDir(), "releases"), Trust: testTrust(t), Source: Source{BaseURL: server.URL, Client: server.Client()}}
			_, err := m.Install(context.Background(), "v"+version)
			if err == nil || !strings.Contains(err.Error(), tc.want) || archives != 0 {
				t.Fatalf("install: %v; archives=%d", err, archives)
			}
			checkNoStage(t, m.Root)
		})
	}
}

// TestProductionTrustFailsClosedWithoutRunningArchive checks an empty embedded trust prevents downloads.
func TestProductionTrustFailsClosedWithoutRunningArchive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	saved := productionKeys
	productionKeys = nil
	t.Cleanup(func() { productionKeys = saved })
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer server.Close()
	m := &Manager{Root: filepath.Join(t.TempDir(), "releases"), Source: Source{Client: server.Client(), BaseURL: server.URL}}
	if _, err := m.Install(context.Background(), "v1.2.3"); err == nil || !strings.Contains(err.Error(), "no release signing keys") || requests != 0 {
		t.Fatalf("production trust: %v; requests=%d", err, requests)
	}
}

// TestExistingVersionNeverExecutesInstalledBinary checks re-use hashes an installed tree without launching it.
func TestExistingVersionNeverExecutesInstalledBinary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := &Manager{Root: filepath.Join(t.TempDir(), "releases")}
	bundle := writeFixture(t, "1.2.3")
	if _, err := m.Adopt(context.Background(), bundle); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "executed")
	binary := filepath.Join(m.Root, "1.2.3", "bin", "gorganizerctl")
	original, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf ran > '"+marker+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = m.Adopt(context.Background(), bundle)
	if err == nil || !strings.Contains(err.Error(), "the installed release does not match this download") {
		t.Fatalf("modified installed version accepted: %v", err)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("existing binary was executed: %v", err)
	}
	if err := os.WriteFile(binary, original, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Adopt(context.Background(), bundle); err != nil {
		t.Fatalf("unchanged installed version was not reusable: %v", err)
	}
}

// TestDanglingCurrentIsRepaired checks a broken current link is treated as no installed version.
func TestDanglingCurrentIsRepaired(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := &Manager{Root: filepath.Join(t.TempDir(), "releases")}
	if err := os.MkdirAll(m.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("9.9.9", filepath.Join(m.Root, "current")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Adopt(context.Background(), writeFixture(t, "1.2.3")); err != nil {
		t.Fatal(err)
	}
	state, err := m.Status()
	if err != nil || state.Current != "1.2.3" || state.Previous != "" {
		t.Fatalf("link not repaired: %+v: %v", state, err)
	}
}

// TestGenerationLinksRejectSymlinkTargets checks that a target generation is an owned real directory.
func TestGenerationLinksRejectSymlinkTargets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := &Manager{Root: filepath.Join(t.TempDir(), "releases")}
	if err := os.MkdirAll(m.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, target string
	}{
		{"symlinked directory", t.TempDir()},
		{"regular file", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			version := filepath.Join(m.Root, "9.9.9")
			if tc.target == "" {
				if err := os.WriteFile(version, []byte("not a directory"), 0o644); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink(tc.target, version); err != nil {
				t.Fatal(err)
			}
			defer os.Remove(version)
			if err := os.Symlink("9.9.9", filepath.Join(m.Root, "previous")); err != nil {
				t.Fatal(err)
			}
			defer os.Remove(filepath.Join(m.Root, "previous"))
			if _, err := m.Rollback(context.Background()); err == nil || err.Error() != "there is no previous release to restore" {
				t.Fatalf("invalid previous accepted: %v", err)
			}
		})
	}
}

// TestVersionHelpersAndXDGRoot checks bounded tags, build metadata and relative XDG fallback.
func TestVersionHelpersAndXDGRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "relative/data")
	root, err := DataRoot()
	if err != nil || root != filepath.Join(home, ".local/share/gorganizer/releases") {
		t.Fatalf("XDG fallback: %s: %v", root, err)
	}
	for _, tc := range []struct {
		v, base string
		valid   bool
	}{{"0.1.0+abc", "0.1.0", true}, {"0.1.0+v0.1.0-3-gabc-dirty", "0.1.0", true}, {"dev", "", false}, {"", "", false}, {"1234567890.1.2", "", false}} {
		got, ok := BaseVersion(tc.v)
		if got != tc.base || ok != tc.valid {
			t.Fatalf("base version %q: %q %v", tc.v, got, ok)
		}
	}
	for _, tag := range []string{"v1234567890.1.2", "v1.1234567890.2", "v1.2.1234567890"} {
		_, compareErr := Compare(strings.TrimPrefix(tag, "v"), "1.2.3")
		if ValidateTag(tag) == nil || compareErr == nil {
			t.Fatalf("accepted long tag %s", tag)
		}
		if _, err := NotesURL(tag); err == nil {
			t.Fatalf("accepted notes tag %s", tag)
		}
	}
	if url, err := NotesURL("v1.2.3"); err != nil || url != "https://github.com/parka5040/gorganizer/releases/tag/v1.2.3" {
		t.Fatalf("notes URL: %s: %v", url, err)
	}
}

// checkNoStage reports any stage or link left in the release store.
func checkNoStage(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".stage-") || strings.HasPrefix(entry.Name(), ".link-") {
			t.Fatalf("abandoned temporary entry: %s", entry.Name())
		}
	}
}

// TestSweepAllLockedOperations checks that the next operation reaps abandoned stages and links.
func TestSweepAllLockedOperations(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := &Manager{Root: filepath.Join(t.TempDir(), "releases")}
	if _, err := m.Adopt(context.Background(), writeFixture(t, "1.2.3")); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []struct {
		name string
		run  func() error
	}{{"adopt", func() error { _, err := m.Adopt(context.Background(), writeFixture(t, "1.2.3")); return err }}, {"rollback", func() error {
		_, err := m.Rollback(context.Background())
		if err != nil && strings.Contains(err.Error(), "no previous") {
			return nil
		}
		return err
	}}, {"collect", m.Collect}, {"no-change update", func() error { _, _, err := m.Update(context.Background(), "v1.2.3"); return err }}} {
		t.Run(operation.name, func(t *testing.T) {
			if err := os.Mkdir(filepath.Join(m.Root, ".stage-killed"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("1.2.3", filepath.Join(m.Root, ".link-killed")); err != nil {
				t.Fatal(err)
			}
			if err := operation.run(); err != nil {
				t.Fatal(err)
			}
			checkNoStage(t, m.Root)
		})
	}
}

// TestCancelledLockWaitLeavesCurrentUntouched checks that a long flock wait obeys its context.
func TestCancelledLockWaitLeavesCurrentUntouched(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := &Manager{Root: filepath.Join(t.TempDir(), "releases")}
	if _, err := m.Adopt(context.Background(), writeFixture(t, "1.2.3")); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Open(filepath.Join(m.Root, ".release.lock"), unix.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = m.Adopt(ctx, writeFixture(t, "1.2.4"))
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("lock wait: %v; elapsed %v", err, time.Since(start))
	}
	current, err := os.Readlink(filepath.Join(m.Root, "current"))
	if err != nil || current != "1.2.3" {
		t.Fatalf("current changed: %s: %v", current, err)
	}
	checkNoStage(t, m.Root)
}

// TestSIGTERMDownloadLeavesLinksUntouched checks SIGTERM during an archive download cleans the stage.
func TestSIGTERMDownloadLeavesLinksUntouched(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	archive := packFixture(t, "1.2.4", fixture("1.2.4"))
	checks := checksumLine("1.2.4", archive)
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case "SHA256SUMS":
			io.WriteString(w, checks)
		case "SHA256SUMS.sig":
			w.Write(testSignature(1, "v1.2.4", []byte(checks)))
		default:
			w.(http.Flusher).Flush()
			close(started)
			<-r.Context().Done()
		}
	}))
	defer server.Close()
	m := &Manager{Root: filepath.Join(t.TempDir(), "releases"), Trust: testTrust(t), Source: Source{BaseURL: server.URL, Client: server.Client()}}
	if _, err := m.Adopt(context.Background(), writeFixture(t, "1.2.3")); err != nil {
		t.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	go func() { <-started; _ = syscall.Kill(os.Getpid(), syscall.SIGTERM) }()
	start := time.Now()
	_, _, err := m.Update(ctx, "v1.2.4")
	if !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatalf("download SIGTERM: %v; elapsed %v", err, time.Since(start))
	}
	assertLinks(t, m.Root, "1.2.3", "")
	checkNoStage(t, m.Root)
}

type cancelWriter struct {
	io.Writer
	cancel context.CancelFunc
}

// Write cancels a staged extraction after writing its first bytes.
func (w cancelWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.cancel()
	return n, err
}

// TestCancelledExtractionLeavesLinksUntouched checks an interrupted extraction never publishes.
func TestCancelledExtractionLeavesLinksUntouched(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	archive := packFixture(t, "1.2.4", fixture("1.2.4"))
	server, m := serveFixture(t, "1.2.4", archive, checksumLine("1.2.4", archive))
	defer server.Close()
	if _, err := m.Adopt(context.Background(), writeFixture(t, "1.2.3")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var writes int
	m.WrapWriter = func(w io.Writer) io.Writer {
		writes++
		if writes > 1 {
			return cancelWriter{w, cancel}
		}
		return w
	}
	_, _, err := m.Update(ctx, "v1.2.4")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("extraction cancel: %v", err)
	}
	assertLinks(t, m.Root, "1.2.3", "")
	checkNoStage(t, m.Root)
}

// TestInstallDeadlineBoundsTagResolution checks the overall deadline includes latest-tag resolution.
func TestInstallDeadlineBoundsTagResolution(t *testing.T) {
	original := installDeadline
	installDeadline = 25 * time.Millisecond
	defer func() { installDeadline = original }()
	m := &Manager{Root: filepath.Join(t.TempDir(), "releases")}
	m.Source.Latest = func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
	start := time.Now()
	_, err := m.Install(context.Background(), "")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("install deadline: %v; elapsed %v", err, time.Since(start))
	}
}

// TestIdleWatchdogCancelsStalledRequest checks the network idle timer stops a body read.
func TestIdleWatchdogCancelsStalledRequest(t *testing.T) {
	original := idleTimeout
	idleTimeout = 25 * time.Millisecond
	defer func() { idleTimeout = original }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"asset", func() error {
			_, err := (Source{Client: server.Client()}).fetch(context.Background(), server.URL, 1024)
			return err
		}},
		{"latest", func() error {
			_, err := (Source{Client: server.Client(), LatestURL: server.URL}).ResolveTag(context.Background(), "")
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			err := tc.call()
			if !errors.Is(err, ErrUnreachable) || time.Since(start) > time.Second {
				t.Fatalf("idle watchdog: %v; elapsed %v", err, time.Since(start))
			}
		})
	}
}

// assertLinks checks the visible generation links without creating an operation stage.
func assertLinks(t *testing.T, root, current, previous string) {
	t.Helper()
	for _, tc := range []struct{ name, want string }{{"current", current}, {"previous", previous}} {
		got, err := os.Readlink(filepath.Join(root, tc.name))
		if tc.want == "" && os.IsNotExist(err) {
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("%s: %q: %v; want %q", tc.name, got, err, tc.want)
		}
		if tc.want != "" {
			info, err := os.Lstat(filepath.Join(root, got))
			if err != nil || !info.IsDir() {
				t.Fatalf("incomplete current generation: %v", err)
			}
		}
	}
}

// TestCancelAfterPublicationCompletesSwitch checks cancellation after the stage rename cannot split the link transaction.
func TestCancelAfterPublicationCompletesSwitch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := &Manager{Root: filepath.Join(t.TempDir(), "releases")}
	if _, err := m.Adopt(context.Background(), writeFixture(t, "1.2.3")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := syncReleaseDir
	defer func() { syncReleaseDir = original }()
	var calls int
	syncReleaseDir = func(root string) error {
		calls++
		if calls == 2 {
			cancel()
		}
		return original(root)
	}
	version, err := m.Adopt(ctx, writeFixture(t, "1.2.4"))
	syncReleaseDir = original
	if err != nil || version != "1.2.4" || ctx.Err() == nil {
		t.Fatalf("post-commit cancellation: %s: %v; ctx=%v", version, err, ctx.Err())
	}
	assertLinks(t, m.Root, "1.2.4", "1.2.3")
}

// TestRollbackCancelAfterSwitchCompletes checks rollback keeps switching after its commit boundary.
func TestRollbackCancelAfterSwitchCompletes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := &Manager{Root: filepath.Join(t.TempDir(), "releases")}
	for _, version := range []string{"1.2.3", "1.2.4"} {
		if _, err := m.Adopt(context.Background(), writeFixture(t, version)); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := renameLink
	defer func() { renameLink = original }()
	renameLink = func(old, new string) error {
		err := original(old, new)
		if err == nil && filepath.Base(new) == "current" {
			cancel()
		}
		return err
	}
	version, err := m.Rollback(ctx)
	renameLink = original
	if err != nil || version != "1.2.3" || ctx.Err() == nil {
		t.Fatalf("rollback after cancel: %s: %v; ctx=%v", version, err, ctx.Err())
	}
	assertLinks(t, m.Root, "1.2.3", "1.2.4")
}

// TestLinkFaultsKeepCompleteCurrent checks link rename and sync failures around both switches.
func TestLinkFaultsKeepCompleteCurrent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct {
		name string
		link string
		sync int
	}{
		{"previous rename", "previous", 0},
		{"current rename", "current", 0},
		{"sweep sync", "", 1},
		{"stage sync", "", 2},
		{"previous sync", "", 3},
		{"current sync", "", 4},
		{"collect sync", "", 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &Manager{Root: filepath.Join(t.TempDir(), "releases")}
			if _, err := m.Adopt(context.Background(), writeFixture(t, "1.2.3")); err != nil {
				t.Fatal(err)
			}
			originalRename, originalSync := renameLink, syncReleaseDir
			defer func() { renameLink, syncReleaseDir = originalRename, originalSync }()
			var syncs int
			renameLink = func(old, new string) error {
				if filepath.Base(new) == tc.link {
					return syscall.EIO
				}
				return os.Rename(old, new)
			}
			syncReleaseDir = func(dir string) error {
				syncs++
				if syncs == tc.sync {
					return syscall.EIO
				}
				return atomicfile.SyncDir(dir)
			}
			_, err := m.Adopt(context.Background(), writeFixture(t, "1.2.4"))
			if err == nil || !errors.Is(err, syscall.EIO) {
				t.Fatalf("fault not observed: %v (sync calls %d)", err, syncs)
			}
			renameLink, syncReleaseDir = originalRename, originalSync
			state, err := m.Status()
			if err != nil {
				t.Fatal(err)
			}
			if state.Current != "1.2.3" && state.Current != "1.2.4" {
				t.Fatalf("lost current generation: %+v", state)
			}
			assertLinks(t, m.Root, state.Current, state.Previous)
			if state.Previous == state.Current {
				if _, err := m.Rollback(context.Background()); err == nil || err.Error() != "there is no previous release to restore" {
					t.Fatalf("same-link rollback: %v", err)
				}
			}
		})
	}
}

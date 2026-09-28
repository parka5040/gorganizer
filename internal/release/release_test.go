package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type fixtureEntry struct {
	name string
	kind byte
	body string
	link string
	mode int64
	size int64
}

// fixture creates a small release tree and manifest for local archive tests.
func fixture(version string) []fixtureEntry {
	files := []fixtureEntry{
		{name: "release.json", body: fmt.Sprintf(`{"version":%q}`, version), mode: 0o644},
		{name: "gorganizer.sh", body: "#!/bin/sh\n", mode: 0o755},
		{name: "resources/icons/tmp_logo.png", body: "icon", mode: 0o644},
		{name: "bin/gorganizerctl", body: "#!/bin/sh\nprintf 'gorganizerctl %s\\n' '" + version + "'\n", mode: 0o755},
		{name: "bin/gorganizerd", body: "#!/bin/sh\nprintf 'gorganizerd %s\\n' '" + version + "'\n", mode: 0o755},
		{name: "bin/gorganizer-gui", body: "#!/bin/sh\nexit 0\n", mode: 0o755},
		{name: "lib/libsample.so.1.2", body: "library", mode: 0o644},
	}
	var manifest strings.Builder
	for _, f := range files {
		h := sha256.Sum256([]byte(f.body))
		fmt.Fprintf(&manifest, "%x  %s\n", h, f.name)
	}
	result := []fixtureEntry{{name: ".", kind: tar.TypeDir, mode: 0o755}, {name: "bin", kind: tar.TypeDir, mode: 0o755}, {name: "lib", kind: tar.TypeDir, mode: 0o755}, {name: "resources", kind: tar.TypeDir, mode: 0o755}, {name: "resources/icons", kind: tar.TypeDir, mode: 0o755}}
	result = append(result, files...)
	result = append(result, fixtureEntry{name: "lib/libsample.so.1", kind: tar.TypeSymlink, link: "libsample.so.1.2", mode: 0o777}, fixtureEntry{name: "MANIFEST.sha256", body: manifest.String(), mode: 0o644})
	return result
}

// packFixture archives test entries under the expected top directory.
func packFixture(t *testing.T, version string, entries []fixtureEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(gz)
	for _, f := range entries {
		name := "gorganizer-" + version + "/" + f.name
		if f.name == "." {
			name = "gorganizer-" + version
		}
		if strings.HasPrefix(f.name, "/") {
			name = f.name
		}
		kind := f.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		size := int64(len(f.body))
		if f.size > 0 {
			size = f.size
		}
		header := &tar.Header{Name: name, Typeflag: kind, Mode: f.mode, Linkname: f.link, Size: size}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if f.body != "" && kind == tar.TypeReg {
			if _, err := io.WriteString(writer, f.body); err != nil {
				t.Fatal(err)
			}
		}
		if f.size > int64(len(f.body)) {
			break
		}
	}
	_ = writer.Close()
	_ = gz.Close()
	return buffer.Bytes()
}

// serveFixture serves a locally built release and customizable checksum lines.
func serveFixture(t *testing.T, version string, archive []byte, checks string) (*httptest.Server, *Manager) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case "latest":
			fmt.Fprintf(w, `{"tag_name":"v%s"}`, version)
		case "SHA256SUMS":
			io.WriteString(w, checks)
		case "gorganizer-" + version + "-linux-x86_64.tar.gz":
			w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	m := &Manager{Root: filepath.Join(t.TempDir(), "releases"), Source: Source{BaseURL: server.URL, LatestURL: server.URL + "/latest", Client: server.Client()}}
	return server, m
}

// checksumLine returns the digest line for a release archive.
func checksumLine(version string, data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x  gorganizer-%s-linux-x86_64.tar.gz\n", h, version)
}

// TestInstallFixture verifies the downloaded bundle before switching the active generation.
func TestInstallFixture(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	version := "1.2.3"
	archive := packFixture(t, version, fixture(version))
	server, manager := serveFixture(t, version, archive, checksumLine(version, archive))
	defer server.Close()
	installed, err := manager.Install(context.Background(), "")
	if err != nil || installed != version {
		t.Fatalf("install: %s: %v", installed, err)
	}
	state, err := manager.Status()
	if err != nil || state.Current != version || len(state.Available) != 1 {
		t.Fatalf("status: %+v: %v", state, err)
	}
	target, err := os.Readlink(filepath.Join(manager.Root, "lib", "not-here"))
	if err == nil {
		t.Fatalf("unexpected release link: %s", target)
	}
	link, err := os.Readlink(filepath.Join(manager.Root, "current", "lib", "libsample.so.1"))
	if err != nil || link != "libsample.so.1.2" {
		t.Fatalf("library link: %q: %v", link, err)
	}
}

// TestInstallRefusesBadDownloads checks hashes, checksum counts and incomplete responses.
func TestInstallRefusesBadDownloads(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	version := "1.2.3"
	archive := packFixture(t, version, fixture(version))
	valid := checksumLine(version, archive)
	cases := []struct {
		name, checks string
		truncate     bool
	}{
		{name: "checksum mismatch", checks: strings.Repeat("0", 64) + valid[64:]},
		{name: "missing checksum", checks: strings.Repeat("0", 64) + "  unrelated.tar.gz\n"},
		{name: "duplicate checksum", checks: valid + valid},
		{name: "malformed checksums", checks: "not a checksum"},
		{name: "truncated download", checks: valid, truncate: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, manager := serveFixture(t, version, archive, tc.checks)
			if tc.truncate {
				server.Close()
				server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, ".tar.gz") {
						w.Header().Set("Content-Length", fmt.Sprint(len(archive)))
						w.Write(archive[:len(archive)/2])
						return
					}
					io.WriteString(w, valid)
				}))
				manager.Source.BaseURL = server.URL
			}
			defer server.Close()
			if _, err := manager.Install(context.Background(), "v"+version); err == nil {
				t.Fatal("unsafe archive installed")
			}
			if _, err := os.Lstat(filepath.Join(manager.Root, "current")); !os.IsNotExist(err) {
				t.Fatalf("current changed: %v", err)
			}
		})
	}
}

// TestInstallRefusesArchiveEntries checks traversal, links, special files and mode and size limits.
func TestInstallRefusesArchiveEntries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	version := "1.2.3"
	cases := []struct {
		name   string
		entry  fixtureEntry
		repeat int
	}{
		{name: "parent traversal", entry: fixtureEntry{name: "../outside", body: "bad", mode: 0o644}},
		{name: "absolute", entry: fixtureEntry{name: "/outside", body: "bad", mode: 0o644}},
		{name: "escaping link", entry: fixtureEntry{name: "lib/bad", kind: tar.TypeSymlink, link: "../../outside", mode: 0o777}},
		{name: "hardlink", entry: fixtureEntry{name: "lib/bad", kind: tar.TypeLink, link: "gorganizer-1.2.3/lib/libsample.so.1.2"}},
		{name: "device", entry: fixtureEntry{name: "lib/bad", kind: tar.TypeChar}},
		{name: "setuid", entry: fixtureEntry{name: "lib/bad", body: "bad", mode: 0o4755}},
		{name: "size budget", entry: fixtureEntry{name: "lib/bad", size: maxUnpacked + 1}},
		{name: "entry budget", repeat: maxEntries + 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries := fixture(version)
			if tc.repeat > 0 {
				entries = []fixtureEntry{{name: ".", kind: tar.TypeDir, mode: 0o755}, {name: "many", kind: tar.TypeDir, mode: 0o755}}
				for i := 0; i < tc.repeat; i++ {
					entries = append(entries, fixtureEntry{name: fmt.Sprintf("many/%d", i), kind: tar.TypeDir, mode: 0o755})
				}
			} else {
				entries = append(entries, tc.entry)
			}
			archive := packFixture(t, version, entries)
			server, manager := serveFixture(t, version, archive, checksumLine(version, archive))
			defer server.Close()
			if _, err := manager.Install(context.Background(), "v"+version); err == nil {
				t.Fatal("unsafe archive installed")
			}
		})
	}
}

// updateFixtureManifest recalculates checksums after changing one fixture file.
func updateFixtureManifest(entries []fixtureEntry) []fixtureEntry {
	var manifest strings.Builder
	for _, entry := range entries {
		if entry.name == "MANIFEST.sha256" || entry.kind != 0 {
			continue
		}
		hash := sha256.Sum256([]byte(entry.body))
		fmt.Fprintf(&manifest, "%x  %s\n", hash, entry.name)
	}
	for i := range entries {
		if entries[i].name == "MANIFEST.sha256" {
			entries[i].body = manifest.String()
		}
	}
	return entries
}

// TestInstallRefusesBundleChanges checks manifest and version checks before publication.
func TestInstallRefusesBundleChanges(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	version := "1.2.3"
	cases := []struct {
		name, want string
		edit       func([]fixtureEntry) []fixtureEntry
	}{
		{name: "extra file", want: "unlisted release file", edit: func(e []fixtureEntry) []fixtureEntry {
			return append(e, fixtureEntry{name: "bin/extra", body: "bad", mode: 0o644})
		}},
		{name: "missing file", want: "missing release file", edit: func(e []fixtureEntry) []fixtureEntry { return append(e[:5], e[6:]...) }},
		{name: "metadata mismatch", want: "release version does not match", edit: func(e []fixtureEntry) []fixtureEntry {
			for i := range e {
				if e[i].name == "release.json" {
					e[i].body = `{"version":"2.0.0"}`
				}
			}
			return updateFixtureManifest(e)
		}},
		{name: "failing self-check", want: "failed its version check", edit: func(e []fixtureEntry) []fixtureEntry {
			for i := range e {
				if e[i].name == "bin/gorganizerd" {
					e[i].body = "#!/bin/sh\nexit 1\n"
				}
			}
			return updateFixtureManifest(e)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archive := packFixture(t, version, tc.edit(fixture(version)))
			server, manager := serveFixture(t, version, archive, checksumLine(version, archive))
			defer server.Close()
			if _, err := manager.Install(context.Background(), "v"+version); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %s, got %v", tc.want, err)
			}
		})
	}
}

type brokenWriter struct{ io.Writer }

// Write simulates a full disk before any bytes reach the destination.
func (brokenWriter) Write([]byte) (int, error) { return 0, unix.ENOSPC }

// TestInstallDiskFullLeavesCurrent verifies a failed write never switches the active release.
func TestInstallDiskFullLeavesCurrent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	archive := packFixture(t, "1.2.3", fixture("1.2.3"))
	server, manager := serveFixture(t, "1.2.3", archive, checksumLine("1.2.3", archive))
	defer server.Close()
	manager.WrapWriter = func(w io.Writer) io.Writer { return brokenWriter{w} }
	if _, err := manager.Install(context.Background(), "v1.2.3"); err == nil {
		t.Fatal("disk-full download installed")
	}
	if _, err := os.Lstat(filepath.Join(manager.Root, "current")); !os.IsNotExist(err) {
		t.Fatalf("current changed: %v", err)
	}
}

// writeFixture creates an extracted bundle for adoption and rollback tests.
func writeFixture(t *testing.T, version string) string {
	t.Helper()
	root := t.TempDir()
	for _, entry := range fixture(version) {
		if entry.name == "." {
			continue
		}
		path := filepath.Join(root, entry.name)
		if entry.kind == tar.TypeDir {
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if entry.kind == tar.TypeSymlink {
			if err := os.Symlink(entry.link, path); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(path, []byte(entry.body), os.FileMode(entry.mode)); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestInterruptedPublicationAndRollback verifies reuse after rename and an atomic rollback.
func TestInterruptedPublicationAndRollback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := &Manager{Root: filepath.Join(t.TempDir(), "releases")}
	ctx := context.Background()
	first := writeFixture(t, "1.2.3")
	if _, err := m.Adopt(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := writeFixture(t, "1.2.4")
	m.AfterPublish = func() error { return fmt.Errorf("simulated interruption") }
	if _, err := m.Adopt(ctx, second); err == nil {
		t.Fatal("expected interruption")
	}
	state, err := m.Status()
	if err != nil || state.Current != "1.2.3" {
		t.Fatalf("active release changed: %+v: %v", state, err)
	}
	m.AfterPublish = nil
	if _, err := m.Adopt(ctx, second); err != nil {
		t.Fatal(err)
	}
	state, _ = m.Status()
	if state.Current != "1.2.4" || state.Previous != "1.2.3" {
		t.Fatalf("switch: %+v", state)
	}
	version, err := m.Rollback(ctx)
	if err != nil || version != "1.2.3" {
		t.Fatalf("rollback: %q: %v", version, err)
	}
	state, _ = m.Status()
	if state.Current != "1.2.3" || state.Previous != "1.2.4" {
		t.Fatalf("rollback status: %+v", state)
	}
}

// TestExistingVersionRequiresSameManifest refuses republished contents under an installed version.
func TestExistingVersionRequiresSameManifest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := &Manager{Root: filepath.Join(t.TempDir(), "releases")}
	ctx := context.Background()
	if _, err := m.Adopt(ctx, writeFixture(t, "1.2.3")); err != nil {
		t.Fatal(err)
	}
	changed := writeFixture(t, "1.2.3")
	file := filepath.Join(changed, "gorganizer.sh")
	if err := os.WriteFile(file, []byte("#!/bin/sh\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	entries := fixture("1.2.3")
	for i := range entries {
		if entries[i].name == "gorganizer.sh" {
			entries[i].body = "#!/bin/sh\nexit 2\n"
		}
	}
	entries = updateFixtureManifest(entries)
	for _, entry := range entries {
		if entry.name == "MANIFEST.sha256" {
			if err := os.WriteFile(filepath.Join(changed, entry.name), []byte(entry.body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := m.Adopt(ctx, changed); err == nil || !strings.Contains(err.Error(), "does not match this download") {
		t.Fatalf("different manifest: %v", err)
	}
	state, err := m.Status()
	if err != nil || state.Current != "1.2.3" {
		t.Fatalf("release changed: %+v: %v", state, err)
	}
}

// TestCollectionKeepsActivePreviousAndRunning verifies generation and stage retention.
func TestCollectionKeepsActivePreviousAndRunning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	runtime := t.TempDir()
	m := &Manager{Root: filepath.Join(t.TempDir(), "releases"), RuntimeDir: runtime}
	for _, version := range []string{"1.2.1", "1.2.2"} {
		if _, err := m.Adopt(context.Background(), writeFixture(t, version)); err != nil {
			t.Fatal(err)
		}
	}
	lock, err := os.OpenFile(filepath.Join(runtime, "session.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.SessionMarker(), []byte(filepath.Join(m.Root, "1.2.1")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Adopt(context.Background(), writeFixture(t, "1.2.3")); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(m.Root, ".stage-old")
	if err := os.Mkdir(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	before := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, before, before); err != nil {
		t.Fatal(err)
	}
	if err := m.Collect(); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"1.2.1", "1.2.2", "1.2.3"} {
		if _, err := os.Stat(filepath.Join(m.Root, version)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale stage remains: %v", err)
	}
	unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	if err := m.Collect(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.Root, "1.2.1")); !os.IsNotExist(err) {
		t.Fatalf("unused version remains: %v", err)
	}
}

// TestReleaseStoreRefusesLinks rejects symlinked storage and version directories.
func TestReleaseStoreRefusesLinks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	bundle := writeFixture(t, "1.2.3")
	for _, tc := range []struct {
		name     string
		linkRoot bool
	}{{"release root", true}, {"existing version", false}} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "releases")
			if tc.linkRoot {
				if err := os.Symlink(t.TempDir(), root); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(root, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(bundle, filepath.Join(root, "1.2.3")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := (&Manager{Root: root}).Adopt(ctx, bundle); err == nil {
				t.Fatal("symlinked release store was accepted")
			}
		})
	}
}

// TestTagAndVersionComparison rejects malformed tags and compares numeric components.
func TestTagAndVersionComparison(t *testing.T) {
	for _, pair := range []struct {
		a, b string
		want int
	}{{"1.2.10", "1.2.9", 1}, {"1.2.3", "1.2.3", 0}, {"0.9.9", "1.0.0", -1}} {
		got, err := Compare(pair.a, pair.b)
		if err != nil || got != pair.want {
			t.Fatalf("compare %s %s: %d: %v", pair.a, pair.b, got, err)
		}
	}
	for _, tag := range []string{"1.2.3", "v1.2.3/../../x", "v1.2", "v1.2.3-alpha"} {
		if ValidateTag(tag) == nil {
			t.Fatalf("accepted tag %q", tag)
		}
	}
}

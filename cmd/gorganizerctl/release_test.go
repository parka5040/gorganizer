package main

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

	"github.com/parka/gorganizer/internal/desktop"
	"github.com/parka/gorganizer/internal/release"
)

// commandFixture writes a small verified release tree inside a test directory.
func commandFixture(t *testing.T, version string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"release.json":                 fmt.Sprintf(`{"version":%q}`, version),
		"gorganizer.sh":                "#!/bin/sh\n",
		"resources/icons/tmp_logo.png": "icon",
		"bin/gorganizerctl":            "#!/bin/sh\nprintf 'gorganizerctl %s\\n' '" + version + "'\n",
		"bin/gorganizerd":              "#!/bin/sh\nprintf 'gorganizerd %s\\n' '" + version + "'\n",
		"bin/gorganizer-gui":           "#!/bin/sh\nexit 0\n",
	}
	var manifest strings.Builder
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.HasPrefix(path, "bin/") || path == "gorganizer.sh" {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256([]byte(content))
		fmt.Fprintf(&manifest, "%x  %s\n", h, path)
	}
	if err := os.WriteFile(filepath.Join(root, "MANIFEST.sha256"), []byte(manifest.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// commandDeps isolates release storage, desktop files and session state.
func commandDeps(t *testing.T) (releaseDeps, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	out, errOut := new(bytes.Buffer), new(bytes.Buffer)
	return releaseDeps{manager: &release.Manager{Root: filepath.Join(home, "data", "gorganizer", "releases")}, out: out, errOut: errOut, running: func() bool { return false }}, out, errOut
}

// TestReleaseStatusGolden checks the status report's stable plain-language format.
func TestReleaseStatusGolden(t *testing.T) {
	deps, out, errOut := commandDeps(t)
	for _, version := range []string{"1.2.3", "1.2.4"} {
		if _, err := deps.manager.Adopt(context.Background(), commandFixture(t, version)); err != nil {
			t.Fatal(err)
		}
	}
	if code := runReleaseWith([]string{"status"}, deps); code != 0 {
		t.Fatalf("status: %d: %s", code, errOut)
	}
	want := "Current: 1.2.4\nPrevious: 1.2.3\nAvailable: 1.2.4, 1.2.3\nIn use: none\n"
	if out.String() != want {
		t.Fatalf("status:\n%s\nwant:\n%s", out, want)
	}
}

// TestReleaseUpdateRefusesDowngrade ensures no older release asset is fetched or installed.
func TestReleaseUpdateRefusesDowngrade(t *testing.T) {
	deps, out, errOut := commandDeps(t)
	if _, err := deps.manager.Adopt(context.Background(), commandFixture(t, "2.0.0")); err != nil {
		t.Fatal(err)
	}
	deps.manager.Source.Latest = func(context.Context) (string, error) { return "v1.9.9", nil }
	if code := runReleaseWith([]string{"update"}, deps); code != 1 || !strings.Contains(errOut.String(), "older release") {
		t.Fatalf("downgrade: %d, %q, %q", code, out, errOut)
	}
	state, err := deps.manager.Status()
	if err != nil || state.Current != "2.0.0" {
		t.Fatalf("release changed: %+v: %v", state, err)
	}
}

// commandArchive packages an extracted fixture for a local release server.
func commandArchive(t *testing.T, root, version string) []byte {
	t.Helper()
	var archive bytes.Buffer
	compressed := gzip.NewWriter(&archive)
	writer := tar.NewWriter(compressed)
	if err := writer.WriteHeader(&tar.Header{Name: "gorganizer-" + version, Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	err := filepath.WalkDir(root, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == root {
			return nil
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		kind := byte(tar.TypeReg)
		if entry.IsDir() {
			kind = tar.TypeDir
		}
		header := &tar.Header{Name: "gorganizer-" + version + "/" + filepath.ToSlash(rel), Typeflag: kind, Mode: int64(info.Mode().Perm()), Size: info.Size()}
		if entry.IsDir() {
			header.Size = 0
		}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.Open(name)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(writer, data)
		closeErr := data.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

// TestReleaseExplicitOlderInstallWarns checks that a requested older tag warns before switching.
func TestReleaseExplicitOlderInstallWarns(t *testing.T) {
	deps, out, errOut := commandDeps(t)
	if _, err := deps.manager.Adopt(context.Background(), commandFixture(t, "2.0.0")); err != nil {
		t.Fatal(err)
	}
	version := "1.9.9"
	archive := commandArchive(t, commandFixture(t, version), version)
	checksum := sha256.Sum256(archive)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if filepath.Base(r.URL.Path) == "SHA256SUMS" {
			fmt.Fprintf(w, "%x  gorganizer-%s-linux-x86_64.tar.gz\n", checksum, version)
		} else {
			w.Write(archive)
		}
	}))
	defer server.Close()
	deps.manager.Source = release.Source{BaseURL: server.URL, Client: server.Client()}
	if code := runReleaseWith([]string{"install", "--tag", "v" + version}, deps); code != 0 {
		t.Fatalf("explicit older install: %d: %s", code, errOut)
	}
	if !strings.Contains(errOut.String(), "Warning: this will install an older version") || !strings.Contains(out.String(), version) {
		t.Fatalf("warning: %q, output: %q", errOut, out)
	}
}

// TestReleasePurgePreservesRunningGeneration checks the offline purge leaves releases for the launcher to remove.
func TestReleasePurgePreservesRunningGeneration(t *testing.T) {
	deps, _, _ := commandDeps(t)
	root := filepath.Dir(deps.manager.Root)
	if err := os.MkdirAll(filepath.Join(root, "releases", "1.2.3"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "fallout", "mods"), 0o755); err != nil {
		t.Fatal(err)
	}
	paths, err := uninstallPaths(true, true, "")
	if err != nil {
		t.Fatal(err)
	}
	foundMods := false
	for _, candidate := range paths {
		if candidate.path == filepath.Join(root, "releases") || candidate.path == root {
			t.Fatalf("purge would remove running release: %s", candidate.path)
		}
		if candidate.path == filepath.Join(root, "fallout") {
			foundMods = true
		}
	}
	if !foundMods {
		t.Fatal("purge did not include game data")
	}
}

// TestReleaseAdoptRegistersCurrent verifies the menu entry follows future switches through current.
func TestReleaseAdoptRegistersCurrent(t *testing.T) {
	deps, out, errOut := commandDeps(t)
	from := commandFixture(t, "1.2.3")
	if code := runReleaseWith([]string{"install", "--from", from}, deps); code != 0 {
		t.Fatalf("install: %d: %s", code, errOut)
	}
	if out.String() != "Gorganizer 1.2.3 is installed.\n" {
		t.Fatalf("install text: %q", out)
	}
	paths, err := desktop.EnvironmentPaths()
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := os.ReadFile(paths.Launcher)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(launcher), "/releases/current/gorganizer.sh") {
		t.Fatalf("launcher does not follow current: %s", launcher)
	}
	out.Reset()
	deps.running = func() bool { return true }
	if code := runReleaseWith([]string{"install", "--from", commandFixture(t, "1.2.4")}, deps); code != 0 {
		t.Fatalf("update: %d: %s", code, errOut)
	}
	if out.String() != "Update installed. It will be used next time you open Gorganizer.\n" {
		t.Fatalf("session text: %q", out)
	}
}

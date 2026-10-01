package launchertest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestBootstrapInstallsLocalFixture verifies the bootstrap checksum, extraction and handoff without network access.
func TestBootstrapInstallsLocalFixture(t *testing.T) {
	bootstrap := bootstrapPath(t)
	assets := t.TempDir()
	version := "1.2.3"
	dir := filepath.Join(assets, "v"+version)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	archive := "gorganizer-" + version + "-linux-x86_64.tar.gz"
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	entries := []struct {
		name, content string
		kind          byte
		mode          int64
	}{
		{name: "gorganizer-" + version, kind: tar.TypeDir, mode: 0o755},
		{name: "gorganizer-" + version + "/bin", kind: tar.TypeDir, mode: 0o755},
		{name: "gorganizer-" + version + "/bin/gorganizerctl", content: "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$SHIM_LOG\"\n", mode: 0o755},
	}
	for _, entry := range entries {
		kind := entry.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		if err := tarWriter.WriteHeader(&tar.Header{Name: entry.name, Mode: entry.mode, Typeflag: kind, Size: int64(len(entry.content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write([]byte(entry.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, archive), buffer.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(buffer.Bytes())
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(fmt.Sprintf("%x  %s\n", digest, archive)), 0o644); err != nil {
		t.Fatal(err)
	}
	shimDir := t.TempDir()
	writeFixtureFile(t, filepath.Join(shimDir, "curl"), []byte("#!/bin/sh\n[ \"$1\" = -fL ] || exit 1\n[ \"$2\" = --output ] || exit 1\ncase \"$4\" in file://*) cp \"${4#file://}\" \"$3\" ;; *) exit 1 ;; esac\n"), 0o755)
	writeNormalPlatformShims(t, shimDir)
	log := filepath.Join(t.TempDir(), "installed")
	cmd := exec.Command("sh", bootstrap, "v"+version)
	cmd.Env = bootstrapEnv(t, shimDir,
		"GORGANIZER_RELEASE_BASE_URL=file://"+assets,
		"SHIM_LOG="+log,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bootstrap: %v: %s", err, out)
	}
	if got := string(readFixtureFile(t, log)); !strings.HasPrefix(got, "release install --from ") || !strings.Contains(got, "gorganizer-"+version) {
		t.Fatalf("handoff: %q", got)
	}
}

// TestBootstrapTruncatedDownloadRunsNothing verifies incomplete bootstrap downloads cannot run fetchers.
func TestBootstrapTruncatedDownloadRunsNothing(t *testing.T) {
	contents, err := os.ReadFile(bootstrapPath(t))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(contents), "\n"), "\n")
	if lines[len(lines)-1] != "main \"$@\"" {
		t.Fatalf("final line: %q", lines[len(lines)-1])
	}
	shimDir := t.TempDir()
	log := filepath.Join(t.TempDir(), "downloads")
	fetchShim := []byte("#!/bin/sh\nprintf '%s\\n' \"$0\" >> \"$SHIM_LOG\"\nexit 1\n")
	writeFixtureFile(t, filepath.Join(shimDir, "curl"), fetchShim, 0o755)
	writeFixtureFile(t, filepath.Join(shimDir, "wget"), fetchShim, 0o755)
	writeNormalPlatformShims(t, shimDir)
	env := bootstrapEnv(t, shimDir, "SHIM_LOG="+log)
	prefixPath := filepath.Join(t.TempDir(), "prefix.sh")
	for n := 1; n < len(lines); n++ {
		prefix := strings.Join(lines[:n], "\n") + "\n"
		if err := os.WriteFile(prefixPath, []byte(prefix), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sh", prefixPath)
		cmd.Env = env
		_ = cmd.Run()
		assertNoBootstrapDownload(t, log)
	}
	cmd := exec.Command("sh", "-s")
	cmd.Env = env
	cmd.Stdin = strings.NewReader(strings.Join(lines[:len(lines)-1], "\n") + "\n")
	_ = cmd.Run()
	assertNoBootstrapDownload(t, log)
}

// TestBootstrapRefusesRoot verifies the bootstrap exits before downloading as root.
func TestBootstrapRefusesRoot(t *testing.T) {
	shimDir := t.TempDir()
	log := filepath.Join(t.TempDir(), "downloads")
	writeFixtureFile(t, filepath.Join(shimDir, "id"), []byte("#!/bin/sh\nprintf '%s\\n' 0\n"), 0o755)
	writeFixtureFile(t, filepath.Join(shimDir, "curl"), []byte("#!/bin/sh\nprintf '%s\\n' curl >> \"$SHIM_LOG\"\nexit 1\n"), 0o755)
	cmd := exec.Command("sh", bootstrapPath(t))
	cmd.Env = bootstrapEnv(t, shimDir, "SHIM_LOG="+log)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("bootstrap succeeded as root")
	}
	if !strings.Contains(string(out), "without sudo") {
		t.Fatalf("stderr: %q", out)
	}
	assertNoBootstrapDownload(t, log)
}

// TestBootstrapRefusesOtherArchitectures verifies the bootstrap exits before downloading on unsupported platforms.
func TestBootstrapRefusesOtherArchitectures(t *testing.T) {
	shimDir := t.TempDir()
	log := filepath.Join(t.TempDir(), "downloads")
	writeFixtureFile(t, filepath.Join(shimDir, "id"), []byte("#!/bin/sh\nprintf '%s\\n' 1000\n"), 0o755)
	writeFixtureFile(t, filepath.Join(shimDir, "uname"), []byte("#!/bin/sh\ncase \"$1\" in -s) printf '%s\\n' Linux ;; -m) printf '%s\\n' aarch64 ;; *) exit 1 ;; esac\n"), 0o755)
	writeFixtureFile(t, filepath.Join(shimDir, "curl"), []byte("#!/bin/sh\nprintf '%s\\n' curl >> \"$SHIM_LOG\"\nexit 1\n"), 0o755)
	cmd := exec.Command("sh", bootstrapPath(t))
	cmd.Env = bootstrapEnv(t, shimDir, "SHIM_LOG="+log)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("bootstrap succeeded on aarch64")
	}
	if !strings.Contains(string(out), "x86_64") {
		t.Fatalf("stderr: %q", out)
	}
	assertNoBootstrapDownload(t, log)
}

// bootstrapPath returns the installation bootstrap path.
func bootstrapPath(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(cwd, "..", "..", "packaging", "install.sh")
}

// bootstrapEnv returns an isolated environment for bootstrap subprocesses.
func bootstrapEnv(t *testing.T, shimDir string, extra ...string) []string {
	t.Helper()
	blocked := map[string]bool{
		"HOME":                          true,
		"PATH":                          true,
		"TMPDIR":                        true,
		"XDG_CACHE_HOME":                true,
		"XDG_CONFIG_HOME":               true,
		"XDG_DATA_HOME":                 true,
		"XDG_RUNTIME_DIR":               true,
		"GORGANIZER_RELEASE_BASE_URL":   true,
		"GORGANIZER_RELEASE_LATEST_URL": true,
		"SHIM_LOG":                      true,
	}
	env := make([]string, 0, len(os.Environ())+8+len(extra))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !blocked[name] {
			env = append(env, entry)
		}
	}
	env = append(env,
		"PATH="+shimDir+":"+os.Getenv("PATH"),
		"HOME="+t.TempDir(),
		"TMPDIR="+t.TempDir(),
		"XDG_CACHE_HOME="+t.TempDir(),
		"XDG_CONFIG_HOME="+t.TempDir(),
		"XDG_DATA_HOME="+t.TempDir(),
		"XDG_RUNTIME_DIR="+t.TempDir(),
	)
	return append(env, extra...)
}

// writeNormalPlatformShims writes the supported platform command shims.
func writeNormalPlatformShims(t *testing.T, dir string) {
	t.Helper()
	writeFixtureFile(t, filepath.Join(dir, "id"), []byte("#!/bin/sh\n[ \"$1\" = -u ] || exit 1\nprintf '%s\\n' 1000\n"), 0o755)
	writeFixtureFile(t, filepath.Join(dir, "uname"), []byte("#!/bin/sh\ncase \"$1\" in -s) printf '%s\\n' Linux ;; -m) printf '%s\\n' x86_64 ;; *) exit 1 ;; esac\n"), 0o755)
}

// assertNoBootstrapDownload verifies the fetcher shims did not run.
func assertNoBootstrapDownload(t *testing.T, log string) {
	t.Helper()
	contents, err := os.ReadFile(log)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("download attempted: %q", contents)
}

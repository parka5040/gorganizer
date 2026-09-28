package launchertest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestBootstrapInstallsLocalFixture verifies the bootstrap checksum, extraction and handoff without network access.
func TestBootstrapInstallsLocalFixture(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := filepath.Join(cwd, "..", "..", "packaging", "install.sh")
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
	log := filepath.Join(t.TempDir(), "installed")
	cmd := exec.Command("sh", bootstrap, "v"+version)
	cmd.Env = append(os.Environ(), "PATH="+shimDir+":"+os.Getenv("PATH"), "GORGANIZER_RELEASE_BASE_URL=file://"+assets, "TMPDIR="+t.TempDir(), "SHIM_LOG="+log, "HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bootstrap: %v: %s", err, out)
	}
	if got := string(readFixtureFile(t, log)); !strings.HasPrefix(got, "release install --from ") || !strings.Contains(got, "gorganizer-"+version) {
		t.Fatalf("handoff: %q", got)
	}
}

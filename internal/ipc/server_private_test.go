package ipc

import (
	"os"
	"path/filepath"
	"testing"
)

// privateSocketTestPath creates a short isolated directory for a Unix socket.
func privateSocketTestPath(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir, err := os.MkdirTemp("", "gzr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "gorganizer.sock")
}

// TestServerSocketIsPrivate checks that Start sets restrictive socket permissions.
func TestServerSocketIsPrivate(t *testing.T) {
	path := privateSocketTestPath(t)
	srv := NewServer(path, nil)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
		t.Errorf("socket mode = %v, want socket with mode 0600", info.Mode())
	}
}

// TestServerRefusesNonSocketAtPath checks that Start leaves a regular file at the socket path untouched.
func TestServerRefusesNonSocketAtPath(t *testing.T) {
	path := privateSocketTestPath(t)
	if err := os.WriteFile(path, []byte("keep this"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := NewServer(path, nil).Start(); err == nil {
		t.Fatal("Start accepted a regular file as its socket")
	}
	after, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("file removed: %v", err)
	}
	if !os.SameFile(before, after) || before.Mode() != after.Mode() {
		t.Errorf("file changed: before %v, after %v", before, after)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "keep this" {
		t.Errorf("file contents changed: data %q, error %v", data, err)
	}
}

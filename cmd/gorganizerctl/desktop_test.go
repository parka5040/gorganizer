package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/desktop"
)

// TestDesktopLifecycle checks registration, stale checkout detection and selective removal.
func TestDesktopLifecycle(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	checkout := filepath.Join(root, "My 'Mods' $files")
	if err := os.MkdirAll(checkout, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "gorganizer.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	paths, err := desktop.EnvironmentPaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.Mimeapps), 0o700); err != nil {
		t.Fatal(err)
	}
	original := "# a note\n[Default Applications]\nx-scheme-handler/nxm=other.desktop;\n[Added Associations]\nx-scheme-handler/nxm=other.desktop;\n[Other]\nsetting=yes\n"
	if err := os.WriteFile(paths.Mimeapps, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	run := func(action, dir string) int {
		out.Reset()
		errOut.Reset()
		return runDesktopWith([]string{action, "--checkout", dir, "--icon", paths.Icon}, &out, &errOut)
	}
	if code := run("register", checkout); code != 0 {
		t.Fatalf("register = %d: %s", code, errOut.String())
	}
	for _, entry := range []struct{ path, want string }{
		{paths.Application, desktop.RenderApplication(paths.Launcher, paths.Icon)},
		{paths.NXM, desktop.RenderNXM(paths.Launcher, paths.Icon)},
	} {
		body, err := os.ReadFile(entry.path)
		if err != nil || string(body) != entry.want {
			t.Fatalf("entry %s = %q, %v", entry.path, body, err)
		}
	}
	if code := run("status", checkout); code != 0 || !strings.Contains(out.String(), "are ready") {
		t.Fatalf("status = %d, %s, %s", code, out.String(), errOut.String())
	}
	before, err := os.ReadFile(paths.Mimeapps)
	if err != nil {
		t.Fatal(err)
	}
	if code := run("register", checkout); code != 0 {
		t.Fatalf("repeat register = %d: %s", code, errOut.String())
	}
	after, err := os.ReadFile(paths.Mimeapps)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("repeat registration changed settings: %q, %v", after, err)
	}
	moved := filepath.Join(root, "moved checkout")
	if err := os.Rename(checkout, moved); err != nil {
		t.Fatal(err)
	}
	if code := run("status", checkout); code != 1 || !strings.Contains(out.String(), "needs updating") {
		t.Fatalf("stale status = %d, %s, %s", code, out.String(), errOut.String())
	}
	if code := run("register", moved); code != 0 {
		t.Fatalf("register moved = %d: %s", code, errOut.String())
	}
	if code := run("status", moved); code != 0 {
		t.Fatalf("moved status = %d: %s", code, errOut.String())
	}
	if code := run("unregister", moved); code != 0 {
		t.Fatalf("unregister = %d: %s", code, errOut.String())
	}
	if code := run("unregister", moved); code != 0 {
		t.Fatalf("repeat unregister = %d: %s", code, errOut.String())
	}
	for _, path := range []string{paths.Application, paths.NXM, paths.Launcher} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("registration remained at %s: %v", path, err)
		}
	}
	settings, err := os.ReadFile(paths.Mimeapps)
	if err != nil || !strings.Contains(string(settings), "# a note\n") || !strings.Contains(string(settings), "nxm=other.desktop;") || !strings.Contains(string(settings), "setting=yes\n") || strings.Contains(string(settings), desktop.Handler) {
		t.Errorf("other settings = %q, %v", settings, err)
	}
}

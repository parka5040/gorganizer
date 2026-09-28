package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/desktop"
)

// TestUninstallRemovesStableRegistration checks new entries and the launcher without disturbing other handlers.
func TestUninstallRemovesStableRegistration(t *testing.T) {
	f := newUninstallFixture(t)
	if err := os.WriteFile(f.deps.launcher, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	paths, err := desktop.EnvironmentPaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := desktop.Register(paths, filepath.Dir(f.deps.launcher), paths.Icon); err != nil {
		t.Fatal(err)
	}
	settings := "# leave this\n[Default Applications]\nx-scheme-handler/nxm=gorganizer-nxm.desktop;\n[Added Associations]\nx-scheme-handler/nxm=gorganizer-nxm.desktop;other.desktop;\n"
	if err := os.WriteFile(paths.Mimeapps, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runUninstallWith([]string{"--yes"}, f.deps); code != 0 {
		t.Fatalf("uninstall = %d, %s", code, f.errOut.String())
	}
	for _, path := range []string{paths.Application, paths.NXM, paths.Launcher} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("installed file remained at %s: %v", path, err)
		}
	}
	body, err := os.ReadFile(paths.Mimeapps)
	if err != nil || !strings.Contains(string(body), "# leave this\n") || !strings.Contains(string(body), "nxm=other.desktop;\n") || strings.Contains(string(body), desktop.Handler) {
		t.Errorf("settings = %q, %v", body, err)
	}
}

// TestUninstallRemovesInterimRegistration recognizes entries with escaped Exec separators.
func TestUninstallRemovesInterimRegistration(t *testing.T) {
	f := newUninstallFixture(t)
	t.Setenv("XDG_DATA_HOME", filepath.Join(f.root, "My Data"))
	if err := os.WriteFile(f.deps.launcher, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	paths, err := desktop.EnvironmentPaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := desktop.Register(paths, filepath.Dir(f.deps.launcher), paths.Icon); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		path, action string
		uri          bool
	}{
		{paths.Application, "launch", false},
		{paths.NXM, "nxm", true},
	} {
		body, err := os.ReadFile(entry.path)
		if err != nil {
			t.Fatal(err)
		}
		newExec := desktop.ExecValue([]string{paths.Launcher, entry.action}, entry.uri)
		interimExec := strings.ReplaceAll(newExec, " ", `\s`)
		body = []byte(strings.Replace(string(body), "Exec="+newExec+"\n", "Exec="+interimExec+"\n", 1))
		if err := os.WriteFile(entry.path, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if code := runUninstallWith([]string{"--yes"}, f.deps); code != 0 {
		t.Fatalf("uninstall = %d, %s", code, f.errOut.String())
	}
	for _, path := range []string{paths.Application, paths.NXM, paths.Launcher} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("interim registration remained at %s: %v", path, err)
		}
	}
	settings, err := os.ReadFile(paths.Mimeapps)
	if err != nil || strings.Contains(string(settings), desktop.Handler) {
		t.Errorf("NXM association remained: %q, %v", settings, err)
	}
}

// TestUninstallKeepsUnidentifiedStableEntry checks a foreign launcher cannot authorize deletion of a desktop entry.
func TestUninstallKeepsUnidentifiedStableEntry(t *testing.T) {
	f := newUninstallFixture(t)
	paths, err := desktop.EnvironmentPaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.Launcher), 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := desktop.RenderLauncher(filepath.Join(f.root, "other-copy"))
	if err := os.WriteFile(paths.Launcher, []byte(foreign), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.NXM), 0o700); err != nil {
		t.Fatal(err)
	}
	entry := desktop.RenderNXM(paths.Launcher, paths.Icon)
	if err := os.WriteFile(paths.NXM, []byte(entry), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.Mimeapps), 0o700); err != nil {
		t.Fatal(err)
	}
	settings := "[Default Applications]\nx-scheme-handler/nxm=gorganizer-nxm.desktop;\n"
	if err := os.WriteFile(paths.Mimeapps, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runUninstallWith([]string{"--yes"}, f.deps); code != 0 {
		t.Fatalf("uninstall = %d, %s", code, f.errOut.String())
	}
	for path, want := range map[string]string{paths.NXM: entry, paths.Launcher: foreign, paths.Mimeapps: settings} {
		body, err := os.ReadFile(path)
		if err != nil || string(body) != want {
			t.Errorf("foreign file %s = %q, %v", path, body, err)
		}
	}
	if !strings.Contains(f.out.String(), "Kept "+paths.NXM+" because it belongs to another copy of Gorganizer.") {
		t.Errorf("missing foreign copy note: %q", f.out.String())
	}
}

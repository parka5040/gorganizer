package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
)

func TestDestructiveModOperationsRejectReservedNames(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newProfileDaemon(t, "Default")
	modsDir := config.ModsDir("skyrimse")
	makeModFolders(t, "Mod")
	markers := []string{
		filepath.Join(modsDir, "Downloads", "archive.zip"),
		filepath.Join(modsDir, "Overwrite", "captured.ini"),
	}
	for _, marker := range markers {
		writeFixture(t, marker)
	}
	operations := []struct {
		name string
		run  func(string) error
	}{
		{name: "Uninstall", run: func(name string) error {
			_, err := d.UninstallMod("skyrimse", name, true)
			return err
		}},
		{name: "Rename old", run: func(name string) error { return d.RenameMod("skyrimse", name, "Renamed") }},
		{name: "Rename new", run: func(name string) error { return d.RenameMod("skyrimse", "Mod", name) }},
		{name: "Reinstall", run: func(name string) error {
			_, _, _, err := d.ReinstallMod("skyrimse", name)
			return err
		}},
	}
	for _, operation := range operations {
		for _, name := range []string{"Downloads", "downloads", "Overwrite", ".hidden", "..", "a/b"} {
			t.Run(operation.name+"/"+name, func(t *testing.T) {
				var invalid *download.InvalidTargetModError
				if err := operation.run(name); !errors.As(err, &invalid) {
					t.Fatalf("%s(%q) error = %v, want InvalidTargetModError", operation.name, name, err)
				}
				for _, marker := range markers {
					if _, err := os.Stat(marker); err != nil {
						t.Errorf("reserved folder marker %q changed: %v", marker, err)
					}
				}
				if _, err := os.Stat(filepath.Join(modsDir, "Mod")); err != nil {
					t.Errorf("valid mod changed: %v", err)
				}
			})
		}
	}
	for _, name := range []string{"Downloads", ".hidden"} {
		t.Run("Rename identical/"+name, func(t *testing.T) {
			var invalid *download.InvalidTargetModError
			if err := d.RenameMod("skyrimse", name, name); !errors.As(err, &invalid) {
				t.Fatalf("RenameMod(%q, %q) = %v, want InvalidTargetModError", name, name, err)
			}
		})
	}
}

func TestUninstallRejectsSymlinkedModFolder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newProfileDaemon(t, "Default")
	outside := t.TempDir()
	marker := filepath.Join(outside, "marker.txt")
	writeFixture(t, marker)
	modsDir := config.ModsDir("skyrimse")
	if err := os.MkdirAll(modsDir, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(modsDir, "Evil")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	var invalid *download.InvalidTargetModError
	if _, err := d.UninstallMod("skyrimse", "Evil", true); !errors.As(err, &invalid) {
		t.Fatalf("UninstallMod(Evil) error = %v, want InvalidTargetModError", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("symlink changed: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("outside marker changed: %v", err)
	}
}

func TestRenameModRefusesDanglingSymlinkCollision(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newProfileDaemon(t, "Default")
	makeModFolders(t, "Mod")
	modsDir := config.ModsDir("skyrimse")
	link := filepath.Join(modsDir, "Taken")
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), link); err != nil {
		t.Fatal(err)
	}
	var collision *ModCollisionError
	if err := d.RenameMod("skyrimse", "Mod", "Taken"); !errors.As(err, &collision) {
		t.Fatalf("RenameMod collision = %v, want ModCollisionError", err)
	}
	if _, err := os.Stat(filepath.Join(modsDir, "Mod")); err != nil {
		t.Errorf("original mod changed: %v", err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("collision symlink changed: %v, %v", info, err)
	}
}

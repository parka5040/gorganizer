package migrate

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/vfs"
)

// fixture creates isolated personal storage, a source checkout and a configured game.
func fixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "runtime"))
	t.Setenv("GORGANIZER_ROOT", filepath.Join(root, "checkout"))
	from := filepath.Join(root, "checkout")
	mods := filepath.Join(from, "SkyrimSE_Mods")
	install := filepath.Join(root, "game")
	for _, path := range []string{mods, filepath.Join(install, "Data")} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.DefaultConfig()
	cfg.Games["skyrimse"] = config.GameConfig{Name: "Skyrim SE", InstallPath: install, DataSubpath: "Data"}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	return from, mods, install
}

// put creates a fixture file inside an isolated test directory.
func put(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
}

// planned returns the single-game migration plan for the fixture.
func planned(t *testing.T, from string) *MigrationPlan {
	t.Helper()
	p, err := Plan(from)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) != 1 {
		t.Fatalf("planned items = %d, want 1", len(p.Items))
	}
	return p
}

// TestPlanFindsLegacyFoldersAndBlockers checks deployed farms, collisions, links and unfinished work.
func TestPlanFindsLegacyFoldersAndBlockers(t *testing.T) {
	for _, tc := range []struct {
		name     string
		arrange  func(*testing.T, string, string)
		blocker  string
		outLinks int
	}{
		{"deployed", func(t *testing.T, mods, install string) {
			put(t, filepath.Join(install, "Data", vfs.SentinelFilename), "{}")
		}, "Unmount the mods", 0},
		{"backup", func(t *testing.T, mods, install string) { _ = os.Mkdir(install+"/Data.orig", 0o755) }, "Unmount the mods", 0},
		{"game root", func(t *testing.T, mods, install string) {
			put(t, filepath.Join(install, vfs.RootManifestFilename), "{}")
		}, "Unmount the mods", 0},
		{"collision", func(t *testing.T, mods, install string) {
			put(t, filepath.Join(config.XDGModsDir("skyrimse"), "already"), "keep")
		}, "already contains files", 0},
		{"inside link", func(t *testing.T, mods, install string) { _ = os.Symlink("Overwrite", filepath.Join(mods, "alias")) }, "points inside the old folder", 0},
		{"inside link through alias", func(t *testing.T, mods, install string) {
			alias := filepath.Join(filepath.Dir(filepath.Dir(mods)), "old-mods-alias")
			if err := os.Symlink(mods, alias); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(alias, "Overwrite"), filepath.Join(mods, "alias")); err != nil {
				t.Fatal(err)
			}
		}, "points inside the old folder", 0},
		{"outside link", func(t *testing.T, mods, install string) {
			_ = os.Symlink(filepath.Join(install, "elsewhere"), filepath.Join(mods, "alias"))
		}, "", 1},
		{"special file", func(t *testing.T, mods, install string) {
			if err := os.MkdirAll(mods, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := syscallMkfifo(filepath.Join(mods, "pipe")); err != nil {
				t.Fatal(err)
			}
		}, "needs to be moved manually", 0},
		{"unfinished reinstall", func(t *testing.T, mods, install string) {
			put(t, filepath.Join(mods, ".gorganizer-reinstall-intent-op.json"), "{}")
		}, "Unmount the mods", 0},
		{"unfinished landing", func(t *testing.T, mods, install string) {
			put(t, filepath.Join(mods, "Downloads", ".gorganizer-landing", "op.json"), "{}")
		}, "Unmount the mods", 0},
		{"preserved batches", func(t *testing.T, mods, install string) {
			put(t, filepath.Join(mods, ".gorganizer-dependency-requests.json"), `{"schema_version":1,"batches":[{}]}`)
		}, "Unmount the mods", 0},
		{"other Data sibling", func(t *testing.T, mods, install string) {
			put(t, filepath.Join(install, "Data.gorganizer-unknown"), "keep")
		}, "Unmount the mods", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			from, mods, install := fixture(t)
			put(t, filepath.Join(mods, "Overwrite", "keep"), "data")
			tc.arrange(t, mods, install)
			p := planned(t, from)
			if tc.blocker != "" && !strings.Contains(strings.Join(p.Items[0].Blockers, "|"), tc.blocker) {
				t.Fatalf("blockers = %v", p.Items[0].Blockers)
			}
			if tc.blocker == "" && p.HasBlockers() {
				t.Fatalf("unexpected blockers: %v", p.Items[0].Blockers)
			}
			if len(p.Items[0].OutsideLinks) != tc.outLinks {
				t.Fatalf("outside links = %v", p.Items[0].OutsideLinks)
			}
		})
	}
}

// syscallMkfifo creates a local named pipe to check the special-file blocker.
func syscallMkfifo(path string) error { return syscall.Mkfifo(path, 0o600) }

// TestPlanUsesRegisteredDataFolder checks configured games with an empty subpath still block on their real Data folder.
func TestPlanUsesRegisteredDataFolder(t *testing.T) {
	from, _, install := fixture(t)
	mods := filepath.Join(from, "Morrowind_Mods")
	put(t, filepath.Join(mods, "Overwrite", "save"), "save")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Games["morrowind"] = config.GameConfig{Name: "Morrowind", InstallPath: install}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(install, "Data Files", vfs.SentinelFilename), "{}")
	plan, err := Plan(from)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range plan.Items {
		if item.GameID == "morrowind" {
			if !strings.Contains(strings.Join(item.Blockers, "|"), "Unmount the mods") {
				t.Fatalf("Morrowind blockers = %v", item.Blockers)
			}
			return
		}
	}
	t.Fatal("Morrowind folder was not planned")
}

// TestPlanDoesNotReadFileContents checks an unreadable mod file can be planned without hashing.
func TestPlanDoesNotReadFileContents(t *testing.T) {
	from, mods, _ := fixture(t)
	file := filepath.Join(mods, "Overwrite", "unreadable")
	put(t, file, "save")
	if err := os.Chmod(file, 0); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("file contents read during planning")
	original := hashFile
	hashFile = func(string, entry) (string, error) { return "", failure }
	t.Cleanup(func() { hashFile = original })
	plan, err := Plan(from)
	if err != nil {
		t.Fatalf("planning unreadable file: %v", err)
	}
	if plan.HasBlockers() || plan.Items[0].Files != 1 || plan.Items[0].Bytes != 4 {
		t.Fatalf("planned unreadable file = %+v", plan.Items[0])
	}
	for _, e := range plan.Items[0].Entries {
		if e.SHA256 != "" {
			t.Fatalf("planning hashed %s", e.Path)
		}
	}
}

// TestSameFilesystemMigrationMovesEverything checks a same-drive rename preserves hidden state and removes the source.
func TestSameFilesystemMigrationMovesEverything(t *testing.T) {
	from, mods, _ := fixture(t)
	files := map[string]string{
		"Downloads/metadata.yaml": "index", "Downloads/archive.zip": "download", "Overwrite/new.txt": "save", ".gorganizer-game.yaml": "settings", "A Mod/metadata.yaml": "metadata", "A Mod/data/file": "assets",
	}
	for path, content := range files {
		put(t, filepath.Join(mods, path), content)
	}
	p := planned(t, from)
	if p.Items[0].Mode != "rename" || p.HasBlockers() {
		t.Fatalf("unexpected plan: %+v", p.Items[0])
	}
	if err := Execute(p, ExecuteOptions{}); err != nil {
		t.Fatal(err)
	}
	for path, content := range files {
		body, err := os.ReadFile(filepath.Join(config.XDGModsDir("skyrimse"), path))
		if err != nil || string(body) != content {
			t.Fatalf("moved %s = %q, %v", path, body, err)
		}
	}
	if pathExists(mods) || pathExists(journalPath()) {
		t.Fatal("source or journal remains after move")
	}
}

// TestRenameDoesNotHashFiles checks a same-drive move needs no file reads.
func TestRenameDoesNotHashFiles(t *testing.T) {
	from, mods, _ := fixture(t)
	put(t, filepath.Join(mods, "Overwrite", "save"), "before")
	p := planned(t, from)
	failure := errors.New("rename hashed a file")
	original := hashFile
	hashFile = func(string, entry) (string, error) { return "", failure }
	t.Cleanup(func() { hashFile = original })
	if err := Execute(p, ExecuteOptions{}); err != nil {
		t.Fatal(err)
	}
}

// TestCrossFilesystemMigrationCopiesVerifiesAndRemoves checks the forced copy path and preserved outside link text.
func TestCrossFilesystemMigrationCopiesVerifiesAndRemoves(t *testing.T) {
	from, mods, install := fixture(t)
	put(t, filepath.Join(mods, "Downloads", "archive.zip"), "download")
	put(t, filepath.Join(mods, "Overwrite", "save.txt"), "save")
	if err := os.Chmod(filepath.Join(mods, "Overwrite"), os.ModeSticky|0o750); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(mods, "external")
	if err := os.Symlink(filepath.Join(install, "other"), link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(config.XDGModsDir("skyrimse"), "Overwrite"), filepath.Join(mods, "new-location")); err != nil {
		t.Fatal(err)
	}
	if err := Execute(planned(t, from), ExecuteOptions{ForceCopy: true}); err != nil {
		t.Fatal(err)
	}
	if pathExists(mods) || pathExists(journalPath()) {
		t.Fatal("source or journal remains after copy")
	}
	got, err := os.Readlink(filepath.Join(config.XDGModsDir("skyrimse"), "external"))
	if err != nil || got != filepath.Join(install, "other") {
		t.Fatalf("link target = %q, %v", got, err)
	}
	for _, path := range []string{"Downloads/archive.zip", "Overwrite/save.txt"} {
		if _, err := os.Stat(filepath.Join(config.XDGModsDir("skyrimse"), path)); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(filepath.Join(config.XDGModsDir("skyrimse"), "Overwrite"))
	if err != nil || info.Mode()&os.ModeSticky == 0 || info.Mode().Perm() != 0o750 {
		t.Fatalf("copied folder mode = %v, %v", info, err)
	}
}

// TestCopyModeVerifiesHashes checks copied and source bytes against hashes recorded before copying.
func TestCopyModeVerifiesHashes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		boundary string
		stage    bool
	}{
		{"copied file", "file-copied", true},
		{"source before removal", "published", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			from, mods, _ := fixture(t)
			source := filepath.Join(mods, "Overwrite", "save")
			put(t, source, "before")
			p := planned(t, from)
			for _, e := range p.Items[0].Entries {
				if e.SHA256 != "" {
					t.Fatal("planning hashed source files")
				}
			}
			info, err := os.Stat(source)
			if err != nil {
				t.Fatal(err)
			}
			err = Execute(p, ExecuteOptions{ForceCopy: true, AfterBoundary: func(boundary string) error {
				if boundary != tc.boundary {
					return nil
				}
				j, err := readJournal()
				if err != nil {
					return err
				}
				var digest string
				for _, e := range j.Items[0].Entries {
					if e.Path == filepath.Join("Overwrite", "save") {
						digest = e.SHA256
					}
				}
				if len(digest) != 64 {
					return errors.New("copy hash missing from journal")
				}
				if tc.stage {
					return os.WriteFile(filepath.Join(p.Items[0].Destination+".gorganizer-migrating", "Overwrite", "save"), []byte("AFTER!"), 0o640)
				}
				if err := os.WriteFile(source, []byte("AFTER!"), 0o640); err != nil {
					return err
				}
				return os.Chtimes(source, time.Now(), info.ModTime())
			}})
			if err == nil || !strings.Contains(err.Error(), "changed") {
				t.Fatalf("hash mismatch accepted: %v", err)
			}
			if !pathExists(source) || !pathExists(journalPath()) {
				t.Fatal("source or recovery journal removed after failed hash check")
			}
		})
	}
}

// TestMigrationFaultMatrix checks that every injected durable-boundary failure can resume without duplicates.
func TestMigrationFaultMatrix(t *testing.T) {
	for _, mode := range []string{"rename", "copy"} {
		boundaries := []string{"planned", "verified", "published", "source-removed", "config-saved", "config-patched", "committed"}
		if mode == "rename" {
			boundaries = append(boundaries, "moved-tree", "moved")
		} else {
			boundaries = append(boundaries, "stage-created", "directory-created", "file-copied", "directory-synced", "copied-tree", "copied", "published-tree", "source-parked", "source-deleted")
		}
		for _, boundary := range boundaries {
			t.Run(mode+"/"+boundary, func(t *testing.T) {
				from, mods, _ := fixture(t)
				put(t, filepath.Join(mods, "Downloads", "archive.zip"), "download")
				put(t, filepath.Join(mods, "Overwrite", "save.txt"), "save")
				cfg, err := config.Load()
				if err != nil {
					t.Fatal(err)
				}
				gc := cfg.Games["skyrimse"]
				gc.Executables = []config.Executable{{ID: "edit", ExePath: filepath.Join(mods, "tool")}}
				cfg.Games["skyrimse"] = gc
				if err := cfg.Save(); err != nil {
					t.Fatal(err)
				}
				p := planned(t, from)
				tripped := false
				failure := errors.New("injected crash")
				err = Execute(p, ExecuteOptions{ForceCopy: mode == "copy", AfterBoundary: func(name string) error {
					if name == boundary && !tripped {
						tripped = true
						return failure
					}
					return nil
				}})
				if !tripped || !errors.Is(err, failure) {
					t.Fatalf("boundary %s was not interrupted: %v", boundary, err)
				}
				if err := Resume(); err != nil {
					t.Fatalf("resume after %s: %v", boundary, err)
				}
				if pathExists(mods) || pathExists(journalPath()) {
					t.Fatal("source or journal remains")
				}
				if body, err := os.ReadFile(filepath.Join(config.XDGModsDir("skyrimse"), "Overwrite", "save.txt")); err != nil || string(body) != "save" {
					t.Fatalf("saved file = %q, %v", body, err)
				}
				cfg, err = config.Load()
				if err != nil || cfg.Games["skyrimse"].Executables[0].ExePath != filepath.Join(config.XDGModsDir("skyrimse"), "tool") {
					t.Fatalf("settings not patched: %v, %v", cfg, err)
				}
			})
		}
	}
}

// TestCopyProgressIncludesPartialFiles checks copy progress starts before a large file finishes.
func TestCopyProgressIncludesPartialFiles(t *testing.T) {
	for _, tc := range []struct {
		name      string
		forceCopy bool
		want      bool
	}{
		{"copy", true, true},
		{"rename", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			from, mods, _ := fixture(t)
			file := filepath.Join(mods, "Overwrite", "archive")
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, make([]byte, 1<<20), 0o600); err != nil {
				t.Fatal(err)
			}
			var partial, final bool
			p := planned(t, from)
			err := Execute(p, ExecuteOptions{ForceCopy: tc.forceCopy, CopyProgress: func(copied, total int64) {
				if copied < total {
					partial = true
				}
				if copied == total {
					final = true
				}
			}})
			if err != nil || partial != tc.want || final != tc.want {
				t.Fatalf("copy progress = (%t, %t), error %v", partial, final, err)
			}
		})
	}
}

// TestChangedSourceIsNeverDeleted checks that edits after planning and after publishing leave both copies intact.
func TestChangedSourceIsNeverDeleted(t *testing.T) {
	for _, boundary := range []string{"before start", "published"} {
		t.Run(boundary, func(t *testing.T) {
			from, mods, _ := fixture(t)
			file := filepath.Join(mods, "Overwrite", "save")
			put(t, file, "before")
			p := planned(t, from)
			if boundary == "before start" {
				put(t, file, "edited")
				if err := Execute(p, ExecuteOptions{ForceCopy: true}); err == nil || pathExists(journalPath()) {
					t.Fatalf("changed plan accepted: %v", err)
				}
			} else {
				err := Execute(p, ExecuteOptions{ForceCopy: true, AfterBoundary: func(phase string) error {
					if phase == "published" {
						put(t, file, "edited")
					}
					return nil
				}})
				if err == nil || !strings.Contains(err.Error(), "changed") {
					t.Fatalf("changed source accepted: %v", err)
				}
				if err := Resume(); err == nil {
					t.Fatal("resume removed a modified source")
				}
				if !pathExists(filepath.Join(config.XDGModsDir("skyrimse"), "Overwrite", "save")) {
					t.Fatal("published copy missing")
				}
			}
			if body, err := os.ReadFile(file); err != nil || string(body) != "edited" {
				t.Fatalf("edited source = %q, %v", body, err)
			}
		})
	}
}

// TestChangedParkedSourceIsNeverDeleted checks cleanup refuses a newly added file in the old parked folder.
func TestChangedParkedSourceIsNeverDeleted(t *testing.T) {
	from, mods, _ := fixture(t)
	put(t, filepath.Join(mods, "Overwrite", "save"), "save")
	stop := errors.New("parked")
	err := Execute(planned(t, from), ExecuteOptions{ForceCopy: true, AfterBoundary: func(phase string) error {
		if phase == "source-parked" {
			return stop
		}
		return nil
	}})
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
	j, err := readJournal()
	if err != nil {
		t.Fatal(err)
	}
	parked := mods + ".gorganizer-migrating-source-" + j.OperationID
	newFile := filepath.Join(parked, "added")
	put(t, newFile, "do not delete")
	if err := Resume(); err == nil {
		t.Fatal("resumed cleanup deleted a changed source")
	}
	if body, err := os.ReadFile(newFile); err != nil || string(body) != "do not delete" {
		t.Fatalf("parked file = %q, %v", body, err)
	}
}

// TestResumeAfterPartialSourceRemoval checks an interrupted cleanup only removes surviving planned files.
func TestResumeAfterPartialSourceRemoval(t *testing.T) {
	from, mods, _ := fixture(t)
	put(t, filepath.Join(mods, "Downloads", "archive"), "download")
	put(t, filepath.Join(mods, "Overwrite", "save"), "save")
	errStop := errors.New("stop after parking")
	err := Execute(planned(t, from), ExecuteOptions{ForceCopy: true, AfterBoundary: func(phase string) error {
		if phase == "source-parked" {
			return errStop
		}
		return nil
	}})
	if !errors.Is(err, errStop) {
		t.Fatal(err)
	}
	j, err := readJournal()
	if err != nil {
		t.Fatal(err)
	}
	parked := mods + ".gorganizer-migrating-source-" + j.OperationID
	if err := os.Remove(filepath.Join(parked, "Downloads", "archive")); err != nil {
		t.Fatal(err)
	}
	if err := Resume(); err != nil {
		t.Fatalf("resuming partial cleanup: %v", err)
	}
	if pathExists(parked) || pathExists(mods) || pathExists(journalPath()) {
		t.Fatal("cleanup left a parked folder, source or journal")
	}
	if body, err := os.ReadFile(filepath.Join(config.XDGModsDir("skyrimse"), "Downloads", "archive")); err != nil || string(body) != "download" {
		t.Fatalf("published copy = %q, %v", body, err)
	}
}

// TestConfigReferencesRewrittenWithPreimage checks settings are only patched after publishing and the original is journaled.
func TestConfigReferencesRewrittenWithPreimage(t *testing.T) {
	from, mods, _ := fixture(t)
	put(t, filepath.Join(mods, "Overwrite", "save"), "save")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	gc := cfg.Games["skyrimse"]
	gc.ToolExe = filepath.Join(mods, "tool")
	gc.Executables = []config.Executable{{ID: "tool", ExePath: filepath.Join(mods, "tool"), WorkingDir: mods, ExtraRWPaths: []string{mods, filepath.Join(t.TempDir(), "outside")}}}
	cfg.Games["skyrimse"] = gc
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(configFilePath())
	if err != nil {
		t.Fatal(err)
	}
	p := planned(t, from)
	if len(p.Items[0].References) != 4 {
		t.Fatalf("references = %v", p.Items[0].References)
	}
	failure := errors.New("stop after publish")
	err = Execute(p, ExecuteOptions{AfterBoundary: func(phase string) error {
		if phase == "published" {
			return failure
		}
		return nil
	}})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	j, err := readJournal()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(j.ConfigBefore, before) || !j.ConfigExists {
		t.Fatal("journal lacks the exact settings preimage")
	}
	if err := Resume(); err != nil {
		t.Fatal(err)
	}
	updated, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	want := config.XDGModsDir("skyrimse")
	got := updated.Games["skyrimse"]
	if got.ToolExe != filepath.Join(want, "tool") || got.Executables[0].WorkingDir != want || got.Executables[0].ExePath != filepath.Join(want, "tool") || got.Executables[0].ExtraRWPaths[0] != want {
		t.Fatalf("unpatched settings = %+v", got)
	}
	if !reflect.DeepEqual(got.Executables[0].ExtraRWPaths[1], gc.Executables[0].ExtraRWPaths[1]) {
		t.Fatal("outside path changed")
	}
}

// TestChangedConfigIsNeverOverwritten checks a setting edited mid-move is preserved until a person resolves the conflict.
func TestChangedConfigIsNeverOverwritten(t *testing.T) {
	from, mods, _ := fixture(t)
	put(t, filepath.Join(mods, "Overwrite", "save"), "save")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	gc := cfg.Games["skyrimse"]
	gc.Executables = []config.Executable{{ExePath: filepath.Join(mods, "tool")}}
	cfg.Games["skyrimse"] = gc
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	stop := errors.New("settings changed")
	err = Execute(planned(t, from), ExecuteOptions{AfterBoundary: func(phase string) error {
		if phase == "published" {
			changed, err := config.Load()
			if err != nil {
				return err
			}
			changed.LogLevel = "debug"
			if err := changed.Save(); err != nil {
				return err
			}
			return stop
		}
		return nil
	}})
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
	if err := Resume(); err == nil || !strings.Contains(err.Error(), "settings changed") {
		t.Fatalf("overwrote edited settings: %v", err)
	}
	cfg, err = config.Load()
	if err != nil || cfg.LogLevel != "debug" || cfg.Games["skyrimse"].Executables[0].ExePath != filepath.Join(mods, "tool") {
		t.Fatalf("settings changed by resume: %v, %v", cfg, err)
	}
	if !pathExists(journalPath()) || !pathExists(filepath.Join(config.XDGModsDir("skyrimse"), "Overwrite", "save")) {
		t.Fatal("journal or moved files missing after settings conflict")
	}
}

// TestSymlinkedJournalFailsClosed checks resume never follows a link to another file.
func TestSymlinkedJournalFailsClosed(t *testing.T) {
	_, mods, _ := fixture(t)
	put(t, filepath.Join(mods, "keep"), "keep")
	if err := os.MkdirAll(config.DataDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other.json")
	put(t, other, `{"schema_version":1}`)
	if err := os.Symlink(other, journalPath()); err != nil {
		t.Fatal(err)
	}
	if err := Resume(); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("followed journal symlink: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(mods, "keep")); err != nil || string(body) != "keep" {
		t.Fatalf("source changed: %q, %v", body, err)
	}
}

// TestUnknownJournalSchemaFailsClosed checks a future format does not change any folder.
func TestUnknownJournalSchemaFailsClosed(t *testing.T) {
	_, mods, _ := fixture(t)
	put(t, filepath.Join(mods, "keep"), "keep")
	if err := os.MkdirAll(config.DataDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	put(t, journalPath(), `{"schema_version":999}`)
	if err := Resume(); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("resume = %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(mods, "keep")); err != nil || string(body) != "keep" {
		t.Fatalf("old file changed: %q, %v", body, err)
	}
	if _, err := os.Stat(journalPath()); err != nil {
		t.Fatal(err)
	}
}

package daemon

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
)

// snapshotTree returns every entry under dir mapped to its file content, with directories and symlinks mapped to markers.
func snapshotTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			out[filepath.ToSlash(rel)+"/"] = "dir"
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			out[filepath.ToSlash(rel)] = "symlink:" + target
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// assertNoReinstallLeftovers fails when hidden reinstall or install staging folders remain in modsDir.
func assertNoReinstallLeftovers(t *testing.T, modsDir string) {
	t.Helper()
	for _, name := range installEntries(t, modsDir) {
		if strings.HasPrefix(name, reinstallStagePrefix) || strings.HasPrefix(name, ".stage-") {
			t.Errorf("staging leftover %q in mods dir", name)
		}
	}
}

// installForReinstall installs archive files for gameID either from Downloads or from an external absolute path.
func installForReinstall(t *testing.T, d *Daemon, gameID, name string, files map[string]string, external bool) (string, string) {
	t.Helper()
	if err := os.MkdirAll(config.ModsDir(gameID), 0755); err != nil {
		t.Fatal(err)
	}
	req := dto.StartInstallRequest{GameID: gameID, Mode: dto.InstallAsNewMod}
	var archive string
	if external {
		archive = filepath.Join(t.TempDir(), "external", name+".zip")
		req.ExternalArchivePath = archive
	} else {
		archive = filepath.Join(config.DownloadsDir(gameID), name+".zip")
		req.ArchiveRelPath = name + ".zip"
	}
	writeZipFiles(t, archive, files)
	folder, _, err := d.StartInstall(req)
	if err != nil {
		t.Fatalf("StartInstall(%s): %v", name, err)
	}
	return folder, archive
}

func TestReinstallModRestoresFilesFromEverySource(t *testing.T) {
	cases := []struct {
		name     string
		external bool
		wantPath func(archive string) string
	}{
		{name: "external absolute archive", external: true, wantPath: func(archive string) string { return archive }},
		{name: "downloads relative archive", wantPath: func(string) string { return "Downloads/Base.zip" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newStardewDaemon(t)
			modsDir := config.ModsDir("skyrimse")
			folder, archive := installForReinstall(t, d, "skyrimse", "Base", map[string]string{
				"plugin.esp": "plugin", "textures/a.dds": "texture",
			}, tc.external)
			patch := filepath.Join(t.TempDir(), "Patch.zip")
			writeZipFiles(t, patch, map[string]string{"textures/b.dds": "patch texture", "plugin.esp": "patched plugin"})
			if _, _, err := d.StartInstall(dto.StartInstallRequest{
				GameID: "skyrimse", ExternalArchivePath: patch, Mode: dto.InstallMergeIntoMod, TargetMod: folder,
			}); err != nil {
				t.Fatalf("merge install: %v", err)
			}
			modDir := filepath.Join(modsDir, folder)
			want := snapshotTree(t, modDir)
			wantMeta, err := download.LoadModMetadata(modDir)
			if err != nil {
				t.Fatal(err)
			}

			if err := os.Remove(filepath.Join(modDir, "textures", "a.dds")); err != nil {
				t.Fatal(err)
			}
			writeFixture(t, filepath.Join(modDir, "junk.txt"))
			if err := os.WriteFile(filepath.Join(modDir, "plugin.esp"), []byte("user edit"), 0644); err != nil {
				t.Fatal(err)
			}

			replayed, skipped, fileCount, err := d.ReinstallMod("skyrimse", folder)
			if err != nil {
				t.Fatalf("ReinstallMod: %v", err)
			}
			if replayed != 2 || skipped != 0 || fileCount != 3 {
				t.Errorf("ReinstallMod = %d, %d, %d, want 2, 0, 3", replayed, skipped, fileCount)
			}
			got := snapshotTree(t, modDir)
			delete(got, "metadata.yaml")
			delete(want, "metadata.yaml")
			if !reflect.DeepEqual(got, want) {
				t.Errorf("reinstalled tree = %v, want %v", got, want)
			}
			meta, err := download.LoadModMetadata(modDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(meta.SourceArchives) != 2 || meta.SourceArchives[0].Path != tc.wantPath(archive) || meta.SourceArchives[1].Path != patch {
				t.Errorf("source archives = %+v", meta.SourceArchives)
			}
			if meta.SourceArchives[0].Merged || !meta.SourceArchives[1].Merged {
				t.Errorf("merged flags = %v, %v, want false, true", meta.SourceArchives[0].Merged, meta.SourceArchives[1].Merged)
			}
			if !reflect.DeepEqual(meta.Files, wantMeta.Files) || meta.FileCount != 3 {
				t.Errorf("metadata files = %v (%d), want %v", meta.Files, meta.FileCount, wantMeta.Files)
			}
			assertNoReinstallLeftovers(t, modsDir)
			if _, err := d.GetMod("skyrimse", folder); err != nil {
				t.Errorf("GetMod after reinstall: %v", err)
			}
		})
	}
}

func TestReinstallModRefusesBeforeTouchingTheMod(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(t *testing.T, modDir, archive string)
		check    func(t *testing.T, err error)
		external bool
	}{
		{
			name: "missing downloads archive",
			mutate: func(t *testing.T, _, archive string) {
				if err := os.Remove(archive); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, err error) {
				var missing *download.ReinstallSourceMissingError
				if !errors.As(err, &missing) || missing.Mod != "Base" || missing.Path != "Downloads/Base.zip" {
					t.Fatalf("error = %v, want ReinstallSourceMissingError for Downloads/Base.zip", err)
				}
			},
		},
		{
			name:     "missing external archive",
			external: true,
			mutate: func(t *testing.T, _, archive string) {
				if err := os.Remove(archive); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, err error) {
				var missing *download.ReinstallSourceMissingError
				if !errors.As(err, &missing) || !filepath.IsAbs(missing.Path) {
					t.Fatalf("error = %v, want ReinstallSourceMissingError with the absolute path", err)
				}
			},
		},
		{
			name: "archive replaced by a directory",
			mutate: func(t *testing.T, _, archive string) {
				if err := os.Remove(archive); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(archive, 0755); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, err error) {
				var missing *download.ReinstallSourceMissingError
				if !errors.As(err, &missing) {
					t.Fatalf("error = %v, want ReinstallSourceMissingError", err)
				}
			},
		},
		{
			name: "empty recorded source path",
			mutate: func(t *testing.T, modDir, _ string) {
				appendRecordedSource(t, modDir, "")
			},
			check: func(t *testing.T, err error) {
				var missing *download.ReinstallSourceMissingError
				if !errors.As(err, &missing) || missing.Path != "" {
					t.Fatalf("error = %v, want ReinstallSourceMissingError for the empty path", err)
				}
			},
		},
		{
			name: "escaping recorded source path",
			mutate: func(t *testing.T, modDir, _ string) {
				appendRecordedSource(t, modDir, "../outside.zip")
			},
			check: func(t *testing.T, err error) {
				var unsafe *UnsafePathError
				if !errors.As(err, &unsafe) {
					t.Fatalf("error = %v, want UnsafePathError", err)
				}
			},
		},
		{
			name: "corrupt archive fails the replay",
			mutate: func(t *testing.T, _, archive string) {
				if err := os.WriteFile(archive, []byte("PK\x03\x04 not really a zip"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, err error) {
				if err == nil || !strings.Contains(err.Error(), "replaying source archive") {
					t.Fatalf("error = %v, want a replay failure", err)
				}
			},
		},
		{
			name: "corrupt second archive fails after staging started",
			mutate: func(t *testing.T, modDir, _ string) {
				corrupt := filepath.Join(t.TempDir(), "Corrupt.zip")
				if err := os.WriteFile(corrupt, []byte("PK\x03\x04 not really a zip"), 0644); err != nil {
					t.Fatal(err)
				}
				appendRecordedSource(t, modDir, corrupt)
			},
			check: func(t *testing.T, err error) {
				if err == nil || !strings.Contains(err.Error(), "Corrupt.zip") {
					t.Fatalf("error = %v, want a replay failure naming Corrupt.zip", err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newStardewDaemon(t)
			modsDir := config.ModsDir("skyrimse")
			folder, archive := installForReinstall(t, d, "skyrimse", "Base", map[string]string{
				"plugin.esp": "plugin", "textures/a.dds": "texture",
			}, tc.external)
			modDir := filepath.Join(modsDir, folder)
			tc.mutate(t, modDir, archive)
			want := snapshotTree(t, modDir)

			_, _, _, err := d.ReinstallMod("skyrimse", folder)
			tc.check(t, err)
			if got := snapshotTree(t, modDir); !reflect.DeepEqual(got, want) {
				t.Errorf("mod changed by refused reinstall:\n got %v\nwant %v", got, want)
			}
			assertNoReinstallLeftovers(t, modsDir)
		})
	}
}

// appendRecordedSource adds a source_archives entry with the given path to a mod's metadata.
func appendRecordedSource(t *testing.T, modDir, path string) {
	t.Helper()
	meta, err := download.LoadModMetadata(modDir)
	if err != nil {
		t.Fatal(err)
	}
	meta.SourceArchives = append(meta.SourceArchives, download.SourceArchiveRef{Path: path})
	if err := download.SaveModMetadata(modDir, meta); err != nil {
		t.Fatal(err)
	}
}

func TestReinstallModPreservesMetadataKeys(t *testing.T) {
	d := newStardewDaemon(t)
	modsDir := config.ModsDir("skyrimse")
	folder, _ := installForReinstall(t, d, "skyrimse", "Base", map[string]string{"plugin.esp": "plugin"}, false)
	modDir := filepath.Join(modsDir, folder)
	meta, err := download.LoadModMetadata(modDir)
	if err != nil {
		t.Fatal(err)
	}
	meta.Name = "Pretty Name"
	meta.Installed = "2020-01-02T03:04:05Z"
	meta.Category = "User Interface"
	meta.Version = "1.2.3"
	meta.Enabled = true
	meta.ModPage = "https://www.nexusmods.com/skyrimspecialedition/mods/1"
	meta.TrueIndex = "0x20"
	meta.VisualIndex = "0x30"
	meta.Separator = "Interface"
	if err := download.SaveModMetadata(modDir, meta); err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := d.ReinstallMod("skyrimse", folder); err != nil {
		t.Fatalf("ReinstallMod: %v", err)
	}
	got, err := download.LoadModMetadata(modDir)
	if err != nil {
		t.Fatal(err)
	}
	want := *meta
	want.Folder = folder
	want.SourceArchives = got.SourceArchives
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("metadata after reinstall = %+v, want %+v", *got, want)
	}
	if len(got.SourceArchives) != 1 || got.SourceArchives[0].Path != "Downloads/Base.zip" ||
		got.SourceArchives[0].InstalledAt != meta.SourceArchives[0].InstalledAt {
		t.Errorf("source archives = %+v, want the recorded Downloads/Base.zip entry", got.SourceArchives)
	}
}

func TestReinstallModKeepsSMAPIManifestFolders(t *testing.T) {
	d := newStardewDaemon(t)
	modsDir := config.ModsDir("stardewvalley")
	folder, _ := installForReinstall(t, d, "stardewvalley", "SampleMod", map[string]string{
		"SampleMod/manifest.json": sampleManifest, "SampleMod/Sample.dll": "dll",
	}, false)
	modDir := filepath.Join(modsDir, folder)
	if err := os.Remove(filepath.Join(modDir, "SampleMod", "Sample.dll")); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(modDir, "SampleMod", "junk.txt"))

	if _, _, fileCount, err := d.ReinstallMod("stardewvalley", folder); err != nil || fileCount != 2 {
		t.Fatalf("ReinstallMod = %d, %v", fileCount, err)
	}
	want := []string{"SampleMod/Sample.dll", "SampleMod/manifest.json", "metadata.yaml"}
	if got := modFiles(t, modDir); !reflect.DeepEqual(got, want) {
		t.Errorf("reinstalled files = %v, want %v", got, want)
	}
	assertNoReinstallLeftovers(t, modsDir)
}

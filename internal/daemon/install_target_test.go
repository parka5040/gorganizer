package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
)

func TestStartInstallValidatesTargetMod(t *testing.T) {
	cases := []struct {
		name       string
		mode       dto.InstallMode
		target     string
		wantFolder string
	}{
		{name: "new parent", mode: dto.InstallAsNewMod, target: ".."},
		{name: "new dot", mode: dto.InstallAsNewMod, target: "."},
		{name: "new hidden", mode: dto.InstallAsNewMod, target: ".hidden"},
		{name: "new reinstall staging prefix", mode: dto.InstallAsNewMod, target: ".reinstall-x"},
		{name: "new nested is sanitized", mode: dto.InstallAsNewMod, target: "a/b", wantFolder: "a_b"},
		{name: "new overwrite", mode: dto.InstallAsNewMod, target: "Overwrite"},
		{name: "new downloads folded", mode: dto.InstallAsNewMod, target: "downloads"},
		{name: "new valid", mode: dto.InstallAsNewMod, target: "Fresh Mod", wantFolder: "Fresh Mod"},
		{name: "merge parent", mode: dto.InstallMergeIntoMod, target: ".."},
		{name: "merge dot", mode: dto.InstallMergeIntoMod, target: "."},
		{name: "merge hidden", mode: dto.InstallMergeIntoMod, target: ".hidden"},
		{name: "merge nested", mode: dto.InstallMergeIntoMod, target: "a/b"},
		{name: "merge overwrite", mode: dto.InstallMergeIntoMod, target: "Overwrite"},
		{name: "merge downloads folded", mode: dto.InstallMergeIntoMod, target: "DOWNLOADS"},
		{name: "merge missing mod", mode: dto.InstallMergeIntoMod, target: "Missing"},
		{name: "merge symlinked mod", mode: dto.InstallMergeIntoMod, target: "Linked"},
		{name: "merge file", mode: dto.InstallMergeIntoMod, target: "NotADir"},
		{name: "merge valid", mode: dto.InstallMergeIntoMod, target: "Existing", wantFolder: "Existing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newStardewDaemon(t)
			modsDir := config.ModsDir("skyrimse")
			writeZipFiles(t, filepath.Join(config.DownloadsDir("skyrimse"), "Patch.zip"), map[string]string{"plugin.esp": "plugin"})
			writeFixture(t, filepath.Join(modsDir, "Existing", "existing.esp"))
			writeFixture(t, filepath.Join(modsDir, "Overwrite", "captured.ini"))
			writeFixture(t, filepath.Join(modsDir, ".hidden", "keep.txt"))
			writeFixture(t, filepath.Join(modsDir, "NotADir"))
			linkTarget := t.TempDir()
			if err := os.Symlink(linkTarget, filepath.Join(modsDir, "Linked")); err != nil {
				t.Fatal(err)
			}
			root := filepath.Dir(modsDir)
			before := snapshotTree(t, root)

			folder, _, err := d.StartInstall(dto.StartInstallRequest{
				GameID: "skyrimse", ArchiveRelPath: "Patch.zip", Mode: tc.mode, TargetMod: tc.target,
			})
			if tc.wantFolder != "" {
				if err != nil || folder != tc.wantFolder {
					t.Fatalf("StartInstall = %q, %v, want %q", folder, err, tc.wantFolder)
				}
				if _, err := os.Stat(filepath.Join(modsDir, tc.wantFolder, "plugin.esp")); err != nil {
					t.Errorf("installed file missing: %v", err)
				}
				return
			}
			var invalid *download.InvalidTargetModError
			if !errors.As(err, &invalid) {
				t.Fatalf("StartInstall error = %v, want InvalidTargetModError", err)
			}
			if after := snapshotTree(t, root); !reflect.DeepEqual(after, before) {
				t.Errorf("refused install changed the mods root:\n got %v\nwant %v", after, before)
			}
			if entries, err := os.ReadDir(linkTarget); err != nil || len(entries) != 0 {
				t.Errorf("symlink target contents = %v, %v, want empty", entries, err)
			}
		})
	}
}

func TestModNameEntryPointsRejectInvalidNames(t *testing.T) {
	for _, name := range []string{"..", ".", ".hidden", "Overwrite", "Downloads", "downloads"} {
		t.Run(name, func(t *testing.T) {
			d := newStardewDaemon(t)
			modsDir := config.ModsDir("skyrimse")
			writeFixture(t, filepath.Join(modsDir, "Downloads", "archive.zip"))
			writeFixture(t, filepath.Join(modsDir, "Overwrite", "captured.ini"))
			writeFixture(t, filepath.Join(modsDir, ".hidden", "metadata.yaml"))

			var invalid *download.InvalidTargetModError
			if _, _, _, err := d.ReinstallMod("skyrimse", name); !errors.As(err, &invalid) {
				t.Errorf("ReinstallMod(%q) error = %v, want InvalidTargetModError", name, err)
			}
			if _, err := d.RegisterManualInstall("skyrimse", name, ""); !errors.As(err, &invalid) {
				t.Errorf("RegisterManualInstall(%q) error = %v, want InvalidTargetModError", name, err)
			}
			if entries, err := d.GetModList("skyrimse", "Default"); err != nil || len(entries) != 0 {
				t.Errorf("modlist after refused registration = %+v, %v", entries, err)
			}
		})
	}
}

func TestStartInstallNormalizesDerivedModNames(t *testing.T) {
	cases := []struct {
		name       string
		archive    string
		sidecar    string
		target     string
		wantFolder string
		wantRefuse bool
	}{
		{name: "leading dot archive name", archive: ".NET Script Framework.zip", wantFolder: "NET Script Framework"},
		{name: "reserved archive name", archive: "Downloads.zip", wantFolder: "Downloads_"},
		{name: "dots only archive name", archive: "....zip", wantFolder: "Mod"},
		{name: "reserved sidecar name", archive: "Anything.zip", sidecar: "Overwrite", wantFolder: "Overwrite_"},
		{name: "hidden sidecar name", archive: "Anything.zip", sidecar: " .Hidden Mod", wantFolder: "Hidden Mod"},
		{name: "explicit hidden target refused", archive: "Anything.zip", target: ".NET Script Framework", wantRefuse: true},
		{name: "explicit reserved target refused", archive: "Anything.zip", target: "overwrite", wantRefuse: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newStardewDaemon(t)
			archive := filepath.Join(config.DownloadsDir("skyrimse"), tc.archive)
			writeZipFiles(t, archive, map[string]string{"plugin.esp": "plugin"})
			if tc.sidecar != "" {
				if err := download.SaveSidecar(archive, download.ArchiveSidecar{ModName: tc.sidecar}, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			folder, _, err := d.StartInstall(dto.StartInstallRequest{
				GameID: "skyrimse", ArchiveRelPath: tc.archive, Mode: dto.InstallAsNewMod, TargetMod: tc.target,
			})
			if tc.wantRefuse {
				var invalid *download.InvalidTargetModError
				if !errors.As(err, &invalid) {
					t.Fatalf("StartInstall error = %v, want InvalidTargetModError", err)
				}
				return
			}
			if err != nil || folder != tc.wantFolder {
				t.Fatalf("StartInstall = %q, %v, want %q", folder, err, tc.wantFolder)
			}
			if _, err := os.Stat(filepath.Join(config.ModsDir("skyrimse"), tc.wantFolder, "plugin.esp")); err != nil {
				t.Errorf("installed file missing: %v", err)
			}
			if names, _ := d.GetModList("skyrimse", "Default"); len(names) != 1 || names[0].ModName != tc.wantFolder {
				t.Errorf("modlist = %+v, want %s", names, tc.wantFolder)
			}
		})
	}
}

func TestStartInstallRefusesAPreviewOfAnotherGameOrArchive(t *testing.T) {
	d := newStardewDaemon(t)
	writeZipFiles(t, filepath.Join(config.DownloadsDir("skyrimse"), "Skyrim.zip"), map[string]string{"plugin.esp": "plugin"})
	writeZipFiles(t, filepath.Join(config.DownloadsDir("skyrimse"), "Other.zip"), map[string]string{"other.esp": "other"})
	writeManifestArchive(t, filepath.Join(config.DownloadsDir("stardewvalley"), "Skyrim.zip"))
	preview, err := d.PreviewInstall(dto.PreviewInstallRequest{GameID: "skyrimse", ArchiveRelPath: "Skyrim.zip"})
	if err != nil {
		t.Fatalf("PreviewInstall: %v", err)
	}
	cases := []struct {
		name string
		req  dto.StartInstallRequest
	}{
		{name: "other game", req: dto.StartInstallRequest{GameID: "stardewvalley", ArchiveRelPath: "Skyrim.zip", Mode: dto.InstallAsNewMod}},
		{name: "other archive", req: dto.StartInstallRequest{GameID: "skyrimse", ArchiveRelPath: "Other.zip", Mode: dto.InstallAsNewMod}},
		{name: "external archive", req: dto.StartInstallRequest{GameID: "skyrimse", ExternalArchivePath: filepath.Join(config.DownloadsDir("skyrimse"), "Skyrim.zip"), Mode: dto.InstallAsNewMod}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.req.PreviewID = preview.PreviewID
			var notFound *PreviewNotFoundError
			if _, _, err := d.StartInstall(tc.req); !errors.As(err, &notFound) || notFound.PreviewID != preview.PreviewID {
				t.Fatalf("StartInstall error = %v, want PreviewNotFoundError", err)
			}
			for _, gameID := range []string{"skyrimse", "stardewvalley"} {
				for _, name := range []string{"Skyrim", "Other"} {
					if _, err := os.Stat(filepath.Join(config.ModsDir(gameID), name)); !errors.Is(err, os.ErrNotExist) {
						t.Errorf("%s/%s installed from a mismatched preview: %v", gameID, name, err)
					}
				}
			}
		})
	}
	folder, _, err := d.StartInstall(dto.StartInstallRequest{
		GameID: "skyrimse", ArchiveRelPath: "Skyrim.zip", Mode: dto.InstallAsNewMod, PreviewID: preview.PreviewID,
	})
	if err != nil || folder != "Skyrim" {
		t.Fatalf("StartInstall with the matching preview = %q, %v", folder, err)
	}
}

func TestStartInstallCreatesAMissingModsDir(t *testing.T) {
	d, _, _ := newDepsDaemon(t)
	modsDir := config.ModsDir(depsGame)
	if err := os.RemoveAll(modsDir); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "sample.zip")
	writeManifestArchive(t, archive)
	folder, _, err := d.StartInstall(dto.StartInstallRequest{GameID: depsGame, ExternalArchivePath: archive, Mode: dto.InstallAsNewMod, TargetMod: "Sample"})
	if err != nil {
		t.Fatalf("StartInstall without a mods directory: %v", err)
	}
	if info, err := os.Stat(filepath.Join(modsDir, folder)); err != nil || !info.IsDir() {
		t.Fatalf("installed mod %q missing: %v", folder, err)
	}

	if err := os.RemoveAll(modsDir); err != nil {
		t.Fatal(err)
	}
	writeFileContent(t, modsDir, "not a directory")
	if _, _, err := d.StartInstall(dto.StartInstallRequest{GameID: depsGame, ExternalArchivePath: archive, Mode: dto.InstallAsNewMod, TargetMod: "Again"}); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("StartInstall over a file mods path = %v, want a refusal", err)
	}
	if data, err := os.ReadFile(modsDir); err != nil || string(data) != "not a directory" {
		t.Fatalf("the mods path file was replaced: %q, %v", data, err)
	}
}

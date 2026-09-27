package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
)

// previewTestDaemon creates a recovered Skyrim daemon with private home and temporary directories.
func previewTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	return newIsolatedDaemon(t, map[string]config.GameConfig{
		"skyrimse": {InstallPath: t.TempDir(), DataSubpath: "Data"},
	})
}

// TestPreviewExternalArchive verifies external source validation and a successful private preview.
func TestPreviewExternalArchive(t *testing.T) {
	d := previewTestDaemon(t)
	archive := filepath.Join(t.TempDir(), "External.zip")
	writeZipFiles(t, archive, map[string]string{"Data/one.esp": "one"})
	link := filepath.Join(t.TempDir(), "alias.zip")
	if err := os.Symlink(archive, link); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		path string
		ok   bool
	}{
		{name: "absolute", path: archive, ok: true},
		{name: "symlink", path: link, ok: true},
		{name: "relative", path: "External.zip"},
		{name: "directory", path: t.TempDir()},
		{name: "missing", path: filepath.Join(t.TempDir(), "missing.zip")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := d.PreviewInstall(dto.PreviewInstallRequest{GameID: "skyrimse", ExternalArchivePath: tc.path})
			if tc.ok {
				if err != nil || result == nil || result.PreviewID == "" || result.DetectedRoot != "Data" {
					t.Fatalf("PreviewInstall = %+v, %v", result, err)
				}
				folder, _, err := d.StartInstall(dto.StartInstallRequest{
					GameID: "skyrimse", ExternalArchivePath: tc.path, PreviewID: result.PreviewID,
					Mode: dto.InstallAsNewMod, TargetMod: "Install-" + tc.name,
				})
				if err != nil {
					t.Fatalf("StartInstall: %v", err)
				}
				if files := modFiles(t, filepath.Join(config.ModsDir("skyrimse"), folder)); !reflect.DeepEqual(files, []string{"metadata.yaml", "one.esp"}) {
					t.Fatalf("installed files = %v", files)
				}
			} else if err == nil {
				t.Fatal("PreviewInstall accepted invalid external archive")
			}
		})
	}
	for _, req := range []dto.PreviewInstallRequest{
		{GameID: "skyrimse"},
		{GameID: "skyrimse", ArchiveRelPath: "Library.zip", ExternalArchivePath: archive},
	} {
		if _, err := d.PreviewInstall(req); err == nil {
			t.Fatalf("PreviewInstall accepted conflicting sources: %+v", req)
		}
	}
}

// TestPreviewBindsSourceIdentity refuses modified, replaced, and source-kind-swapped preview installs.
func TestPreviewBindsSourceIdentity(t *testing.T) {
	for _, mutation := range []struct {
		name string
		edit func(*testing.T, string)
	}{
		{name: "modify", edit: func(t *testing.T, path string) {
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write([]byte("changed")); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "replace", edit: func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			writeZipFiles(t, path, map[string]string{"other.esp": "other"})
		}},
		{name: "remove", edit: func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			d := previewTestDaemon(t)
			archive := filepath.Join(t.TempDir(), "External.zip")
			writeZipFiles(t, archive, map[string]string{"plugin.esp": "data"})
			preview, err := d.PreviewInstall(dto.PreviewInstallRequest{GameID: "skyrimse", ExternalArchivePath: archive})
			if err != nil {
				t.Fatal(err)
			}
			mutation.edit(t, archive)
			_, _, err = d.StartInstall(dto.StartInstallRequest{
				GameID: "skyrimse", ExternalArchivePath: archive, PreviewID: preview.PreviewID,
				Mode: dto.InstallAsNewMod, TargetMod: "Target",
			})
			var missing *PreviewNotFoundError
			if !errors.As(err, &missing) {
				t.Fatalf("StartInstall error = %v, want PreviewNotFoundError", err)
			}
		})
	}
	d := previewTestDaemon(t)
	writeZipFiles(t, filepath.Join(config.DownloadsDir("skyrimse"), "Library.zip"), map[string]string{"plugin.esp": "data"})
	external := filepath.Join(t.TempDir(), "External.zip")
	writeZipFiles(t, external, map[string]string{"plugin.esp": "data"})
	preview, err := d.PreviewInstall(dto.PreviewInstallRequest{GameID: "skyrimse", ArchiveRelPath: "Library.zip"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = d.StartInstall(dto.StartInstallRequest{
		GameID: "skyrimse", ExternalArchivePath: external, PreviewID: preview.PreviewID,
		Mode: dto.InstallAsNewMod, TargetMod: "Target",
	})
	var missing *PreviewNotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("StartInstall with external source = %v, want PreviewNotFoundError", err)
	}
	externalPreview, err := d.PreviewInstall(dto.PreviewInstallRequest{GameID: "skyrimse", ExternalArchivePath: external})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = d.StartInstall(dto.StartInstallRequest{
		GameID: "skyrimse", ArchiveRelPath: "Absent.zip", PreviewID: externalPreview.PreviewID,
		Mode: dto.InstallAsNewMod, TargetMod: "Target",
	})
	if !errors.As(err, &missing) {
		t.Fatalf("StartInstall with library source = %v, want PreviewNotFoundError", err)
	}
}

// TestPreviewReturnsModuleConfigBytes returns the original case-insensitive XML payload and refuses oversized files.
func TestPreviewReturnsModuleConfigBytes(t *testing.T) {
	d := previewTestDaemon(t)
	for _, tc := range []struct {
		name string
		xml  string
		fail bool
	}{
		{name: "bounded", xml: `<config><moduleName>Example</moduleName><requiredInstallFiles><file source="plugin.esp" destination="plugin.esp"/></requiredInstallFiles></config>`},
		{name: "oversized", xml: strings.Repeat("x", (1<<20)+1), fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "Installer.zip")
			writeZipFiles(t, path, map[string]string{"Wrapper/FoMoD/MoDuLeCoNfIg.XmL": tc.xml, "Wrapper/plugin.esp": "plugin"})
			result, err := d.PreviewInstall(dto.PreviewInstallRequest{GameID: "skyrimse", ExternalArchivePath: path})
			if tc.fail {
				var rejected *download.ArchiveRejectedError
				if !errors.As(err, &rejected) || rejected.Reason != download.ArchiveRejectedLimit || rejected.Detail != "ModuleConfig.xml" {
					t.Fatalf("PreviewInstall error = %v, want ModuleConfig.xml limit", err)
				}
				return
			}
			if err != nil || result.Plan == nil || string(result.Plan.ModuleConfigXML) != tc.xml {
				t.Fatalf("PreviewInstall = %+v, %v, want XML bytes", result, err)
			}
			if len(result.Plan.RequiredFiles) != 1 || result.Plan.RequiredFiles[0].Source != "plugin.esp" {
				t.Fatalf("required files = %+v", result.Plan.RequiredFiles)
			}
		})
	}
}

// TestPreviewScreenshotIsContainedAndBounded includes safe images and omits unsafe or oversized screenshots.
func TestPreviewScreenshotIsContainedAndBounded(t *testing.T) {
	for _, tc := range []struct {
		name  string
		image string
		files map[string]string
		want  string
	}{
		{name: "legacy image", files: map[string]string{"fomod/info.xml": "<fomod><Name>Legacy</Name></fomod>", "fomod/Screenshot.PNG": "image"}, want: "image"},
		{name: "module image", image: "fomod/Preview.WeBp", files: map[string]string{"fomod/Preview.WeBp": "webp"}, want: "webp"},
		{name: "escape", image: "../outside.png", files: map[string]string{"fomod/Preview.png": "image"}},
		{name: "too large", image: "fomod/huge.png", files: map[string]string{"fomod/huge.png": strings.Repeat("x", (2<<20)+1)}},
		{name: "wrong extension", image: "fomod/Preview.txt", files: map[string]string{"fomod/Preview.txt": "text"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := previewTestDaemon(t)
			files := make(map[string]string)
			for k, v := range tc.files {
				files[k] = v
			}
			if tc.image != "" {
				files["fomod/ModuleConfig.xml"] = `<config><moduleImage path="` + tc.image + `"/></config>`
			}
			archive := filepath.Join(t.TempDir(), "Image.zip")
			writeZipFiles(t, archive, files)
			result, err := d.PreviewInstall(dto.PreviewInstallRequest{GameID: "skyrimse", ExternalArchivePath: archive})
			if err != nil || result.Plan == nil || string(result.Plan.ScreenshotData) != tc.want {
				t.Fatalf("PreviewInstall = %+v, %v, want screenshot %q", result, err, tc.want)
			}
		})
	}
}

// TestPreviewContentRootDetection checks game-root signals, wrapper flattening, and ambiguous layouts.
func TestPreviewContentRootDetection(t *testing.T) {
	for _, tc := range []struct {
		name      string
		files     map[string]string
		root      string
		ambiguous bool
	}{
		{name: "Data", files: map[string]string{"dAtA/plugin.esp": "plugin"}, root: "dAtA"},
		{name: "wrapper Data", files: map[string]string{"Wrapper/daTa/plugin.esp": "plugin"}, root: "Wrapper/daTa"},
		{name: "plugin", files: map[string]string{"plugin.esp": "plugin", "assets/other.txt": "other"}},
		{name: "single wrapper", files: map[string]string{"Wrapper/readme.txt": "readme"}, root: "Wrapper"},
		{name: "ambiguous", files: map[string]string{"A/readme.txt": "a", "B/readme.txt": "b"}, ambiguous: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := previewTestDaemon(t)
			archive := filepath.Join(t.TempDir(), "Content.zip")
			writeZipFiles(t, archive, tc.files)
			preview, err := d.PreviewInstall(dto.PreviewInstallRequest{GameID: "skyrimse", ExternalArchivePath: archive})
			if err != nil || preview.DetectedRoot != tc.root || preview.RootAmbiguous != tc.ambiguous {
				t.Fatalf("PreviewInstall = %+v, %v, want root %q ambiguous %t", preview, err, tc.root, tc.ambiguous)
			}
			if len(preview.SelectableRoots) == 0 || preview.SelectableRoots[0] != "" {
				t.Fatalf("selectable roots = %v", preview.SelectableRoots)
			}
		})
	}
}

// TestPreviewOblivionGameRootMarkers keeps Oblivion Remastered root files together for deployment routing.
func TestPreviewOblivionGameRootMarkers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		"oblivionremastered": {InstallPath: t.TempDir(), DataSubpath: "OblivionRemastered/Content/Dev/ObvData/Data"},
	})
	archive := filepath.Join(t.TempDir(), "GameRoot.zip")
	writeZipFiles(t, archive, map[string]string{
		"Engine/root.txt": "engine", "OblivionRemastered/Content/Dev/ObvData/Data/mesh.txt": "data",
	})
	preview, err := d.PreviewInstall(dto.PreviewInstallRequest{GameID: "oblivionremastered", ExternalArchivePath: archive})
	if err != nil || preview.DetectedRoot != "" || preview.RootAmbiguous {
		t.Fatalf("PreviewInstall = %+v, %v", preview, err)
	}
	folder, _, err := d.StartInstall(dto.StartInstallRequest{
		GameID: "oblivionremastered", ExternalArchivePath: archive, PreviewID: preview.PreviewID,
		TargetMod: "GameRoot", Mode: dto.InstallAsNewMod,
	})
	if err != nil {
		t.Fatal(err)
	}
	if files := modFiles(t, filepath.Join(config.ModsDir("oblivionremastered"), folder)); !reflect.DeepEqual(files, []string{
		".gorganizer-root/Engine/root.txt", "mesh.txt", "metadata.yaml",
	}) {
		t.Fatalf("installed files = %v", files)
	}
}

// TestPreviewSelectableRootsCapsAndHides lists at most 500 sorted visible roots through depth two.
func TestPreviewSelectableRootsCapsAndHides(t *testing.T) {
	d := previewTestDaemon(t)
	files := map[string]string{".hidden/sub/file.txt": "hidden", "Wrapper/Inner/Deep/file.txt": "deep"}
	for i := range 510 {
		files["Dir"+stringNumber(i)+"/file.txt"] = "content"
	}
	archive := filepath.Join(t.TempDir(), "Many.zip")
	writeZipFiles(t, archive, files)
	preview, err := d.PreviewInstall(dto.PreviewInstallRequest{GameID: "skyrimse", ExternalArchivePath: archive})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.SelectableRoots) != 500 || !slices.IsSorted(preview.SelectableRoots) || preview.SelectableRoots[0] != "" {
		t.Fatalf("selectable roots length/order = %d/%v", len(preview.SelectableRoots), preview.SelectableRoots[:min(len(preview.SelectableRoots), 5)])
	}
	if slices.Contains(preview.SelectableRoots, ".hidden") || slices.Contains(preview.SelectableRoots, "Wrapper/Inner/Deep") {
		t.Fatalf("selectable roots include hidden or deep paths: %v", preview.SelectableRoots)
	}
}

// stringNumber formats a directory fixture index consistently.
func stringNumber(n int) string {
	return fmt.Sprintf("%03d", n)
}

// TestStartInstallSelectedRoot installs exactly a chosen subtree and rejects invalid or FOMOD choices.
func TestStartInstallSelectedRoot(t *testing.T) {
	d := previewTestDaemon(t)
	archive := filepath.Join(t.TempDir(), "Choose.zip")
	writeZipFiles(t, archive, map[string]string{"First/plugin.esp": "first", "Second/plugin.esp": "second", "Second/Sub/other.txt": "other"})
	preview, err := d.PreviewInstall(dto.PreviewInstallRequest{GameID: "skyrimse", ExternalArchivePath: archive})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(preview.SelectableRoots, []string{"", "First", "Second", "Second/Sub"}) {
		t.Fatalf("selectable roots = %v", preview.SelectableRoots)
	}
	req := dto.StartInstallRequest{
		GameID: "skyrimse", ExternalArchivePath: archive, PreviewID: preview.PreviewID,
		TargetMod: "Chosen", Mode: dto.InstallAsNewMod, SelectedRoot: "Second",
	}
	for _, root := range []string{"../Second", "Second/../First", "Missing", "second"} {
		req.SelectedRoot = root
		_, _, err := d.StartInstall(req)
		var unsafe *UnsafePathError
		if !errors.As(err, &unsafe) || unsafe.Field != "selected_root" {
			t.Fatalf("StartInstall(%q) error = %v, want selected_root", root, err)
		}
	}
	req.SelectedRoot = "Second"
	folder, _, err := d.StartInstall(req)
	if err != nil || folder != "Chosen" {
		t.Fatalf("StartInstall = %q, %v", folder, err)
	}
	if files := modFiles(t, filepath.Join(config.ModsDir("skyrimse"), folder)); !reflect.DeepEqual(files, []string{"Sub/other.txt", "metadata.yaml", "plugin.esp"}) {
		t.Fatalf("installed files = %v", files)
	}
	fomodArchive := filepath.Join(t.TempDir(), "Fomod.zip")
	writeZipFiles(t, fomodArchive, map[string]string{"fomod/ModuleConfig.xml": "<config/>", "plugin.esp": "plugin"})
	fomod, err := d.PreviewInstall(dto.PreviewInstallRequest{GameID: "skyrimse", ExternalArchivePath: fomodArchive})
	if err != nil {
		t.Fatal(err)
	}
	req.ExternalArchivePath, req.PreviewID, req.SelectedRoot, req.TargetMod = fomodArchive, fomod.PreviewID, "fomod", "Refused"
	_, _, err = d.StartInstall(req)
	var unsafe *UnsafePathError
	if !errors.As(err, &unsafe) || unsafe.Field != "selected_root" {
		t.Fatalf("FOMOD selected root error = %v", err)
	}
}

// TestStartInstallConfirmedEmptyFomodSelection refuses a confirmed installer with no selected or required files.
func TestStartInstallConfirmedEmptyFomodSelection(t *testing.T) {
	d := previewTestDaemon(t)
	archive := filepath.Join(t.TempDir(), "Empty.zip")
	writeZipFiles(t, archive, map[string]string{"fomod/ModuleConfig.xml": "<config/>", "plugin.esp": "plugin"})
	preview, err := d.PreviewInstall(dto.PreviewInstallRequest{GameID: "skyrimse", ExternalArchivePath: archive})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = d.StartInstall(dto.StartInstallRequest{
		GameID: "skyrimse", ExternalArchivePath: archive, PreviewID: preview.PreviewID,
		TargetMod: "Empty", Mode: dto.InstallAsNewMod, FomodConfirmed: true,
	})
	if !errors.Is(err, download.ErrEmptyInstallSelection) {
		t.Fatalf("StartInstall error = %v, want ErrEmptyInstallSelection", err)
	}
}

// TestStartInstallConfirmedRequiredFomodFiles installs mandatory files when no optional files were selected.
func TestStartInstallConfirmedRequiredFomodFiles(t *testing.T) {
	d := previewTestDaemon(t)
	archive := filepath.Join(t.TempDir(), "Required.zip")
	writeZipFiles(t, archive, map[string]string{
		"Wrapper/fomod/ModuleConfig.xml": `<config><requiredInstallFiles><file source="mandatory.esp" destination="mandatory.esp"/></requiredInstallFiles></config>`,
		"Wrapper/mandatory.esp":          "required", "outside.esp": "outside",
	})
	preview, err := d.PreviewInstall(dto.PreviewInstallRequest{GameID: "skyrimse", ExternalArchivePath: archive})
	if err != nil {
		t.Fatal(err)
	}
	folder, _, err := d.StartInstall(dto.StartInstallRequest{
		GameID: "skyrimse", ExternalArchivePath: archive, PreviewID: preview.PreviewID,
		TargetMod: "Required", Mode: dto.InstallAsNewMod, FomodConfirmed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if files := modFiles(t, filepath.Join(config.ModsDir("skyrimse"), folder)); !reflect.DeepEqual(files, []string{"mandatory.esp", "metadata.yaml"}) {
		t.Fatalf("installed files = %v", files)
	}
}

// TestStartInstallUnconfirmedFomodStillRequiresWizard keeps the wizard-required response for an unconfirmed installer.
func TestStartInstallUnconfirmedFomodStillRequiresWizard(t *testing.T) {
	d := previewTestDaemon(t)
	archive := filepath.Join(t.TempDir(), "Wizard.zip")
	writeZipFiles(t, archive, map[string]string{"fomod/ModuleConfig.xml": "<config/>", "plugin.esp": "plugin"})
	preview, err := d.PreviewInstall(dto.PreviewInstallRequest{GameID: "skyrimse", ExternalArchivePath: archive})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = d.StartInstall(dto.StartInstallRequest{
		GameID: "skyrimse", ExternalArchivePath: archive, PreviewID: preview.PreviewID,
		TargetMod: "Wizard", Mode: dto.InstallAsNewMod,
	})
	var required *FomodRequiredError
	if !errors.As(err, &required) || required.PreviewID != preview.PreviewID {
		t.Fatalf("StartInstall error = %v, want FomodRequiredError", err)
	}
}

// TestLegacyFomodConfirmedFlatCopyExcludesFomodDir copies legacy payload without installer metadata or scripts.
func TestLegacyFomodConfirmedFlatCopyExcludesFomodDir(t *testing.T) {
	d := previewTestDaemon(t)
	archive := filepath.Join(t.TempDir(), "Legacy.zip")
	writeZipFiles(t, archive, map[string]string{
		"Wrapper/fomod/info.xml":  "<fomod><Name>Legacy</Name></fomod>",
		"Wrapper/fomod/script.cs": "never run", "Wrapper/plugin.esp": "plugin", "Wrapper/Data/extra.esp": "extra",
	})
	preview, err := d.PreviewInstall(dto.PreviewInstallRequest{GameID: "skyrimse", ExternalArchivePath: archive})
	if err != nil || preview.Plan == nil || !preview.Plan.LegacyInfoOnly {
		t.Fatalf("PreviewInstall = %+v, %v", preview, err)
	}
	folder, _, err := d.StartInstall(dto.StartInstallRequest{
		GameID: "skyrimse", ExternalArchivePath: archive, PreviewID: preview.PreviewID,
		TargetMod: "Legacy", Mode: dto.InstallAsNewMod, FomodConfirmed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if files := modFiles(t, filepath.Join(config.ModsDir("skyrimse"), folder)); !reflect.DeepEqual(files, []string{"Data/extra.esp", "metadata.yaml", "plugin.esp"}) {
		t.Fatalf("installed files = %v, want payload and metadata", files)
	}
}

// TestStartInstallFomodSelectionStaysInsideModuleRoot prevents a selection from reading a sibling of the module root.
func TestStartInstallFomodSelectionStaysInsideModuleRoot(t *testing.T) {
	d := previewTestDaemon(t)
	archive := filepath.Join(t.TempDir(), "Nested.zip")
	writeZipFiles(t, archive, map[string]string{"Wrapper/fomod/ModuleConfig.xml": "<config/>", "Wrapper/inside.esp": "inside", "outside.esp": "outside"})
	preview, err := d.PreviewInstall(dto.PreviewInstallRequest{GameID: "skyrimse", ExternalArchivePath: archive})
	if err != nil {
		t.Fatal(err)
	}
	for _, previewID := range []string{preview.PreviewID, ""} {
		_, _, err = d.StartInstall(dto.StartInstallRequest{
			GameID: "skyrimse", ExternalArchivePath: archive, PreviewID: previewID,
			TargetMod: "Unsafe", Mode: dto.InstallAsNewMod, FomodConfirmed: true,
			FomodSelectedFiles: []dto.FomodFileResult{{Source: "../outside.esp", Destination: "outside.esp"}},
		})
		if err == nil {
			t.Fatalf("FOMOD selection escaped module root with preview %q", previewID)
		}
	}
}

// TestStartInstallManifestLayoutRejectsPreviewOverrides preserves manifest-layout precedence over FOMOD and roots.
func TestStartInstallManifestLayoutRejectsPreviewOverrides(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newStardewDaemon(t)
	archive := filepath.Join(t.TempDir(), "Manifest.zip")
	writeManifestArchive(t, archive)
	preview, err := d.PreviewInstall(dto.PreviewInstallRequest{GameID: "stardewvalley", ExternalArchivePath: archive})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		confirm bool
		root    string
	}{
		{name: "confirmed", confirm: true},
		{name: "chosen root", root: "SampleMod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := d.StartInstall(dto.StartInstallRequest{
				GameID: "stardewvalley", ExternalArchivePath: archive, PreviewID: preview.PreviewID,
				TargetMod: "Refused", Mode: dto.InstallAsNewMod, FomodConfirmed: tc.confirm, SelectedRoot: tc.root,
			})
			if tc.confirm && !errors.Is(err, download.ErrFomodNotSupportedForLayout) {
				t.Fatalf("confirmed error = %v", err)
			}
			var unsafe *UnsafePathError
			if tc.root != "" && (!errors.As(err, &unsafe) || unsafe.Field != "selected_root") {
				t.Fatalf("selected root error = %v", err)
			}
		})
	}
}

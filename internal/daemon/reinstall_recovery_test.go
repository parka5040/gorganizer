package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/separators"
)

var errSimulatedCrash = errors.New("simulated crash")

// damageMod removes one installed file and adds a stray one so a reinstall result differs from the damaged tree.
func damageMod(t *testing.T, modDir string) {
	t.Helper()
	if err := os.Remove(filepath.Join(modDir, "textures", "a.dds")); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(modDir, "junk.txt"))
}

// restartDaemon builds a second daemon over the same state directories, running its startup recovery.
func restartDaemon(t *testing.T, d *Daemon) *Daemon {
	t.Helper()
	restarted, err := New(d.config)
	if err != nil {
		t.Fatalf("restarting daemon: %v", err)
	}
	t.Cleanup(restarted.Shutdown)
	return restarted
}

// assertNoReinstallState fails when reinstall staging folders or intent records remain in modsDir.
func assertNoReinstallState(t *testing.T, modsDir string) {
	t.Helper()
	for _, name := range installEntries(t, modsDir) {
		if strings.HasPrefix(name, reinstallStagePrefix) || strings.HasPrefix(name, reinstallIntentPrefix) || strings.HasPrefix(name, ".stage-") {
			t.Errorf("reinstall leftover %q in mods dir", name)
		}
	}
}

func TestReinstallCrashAtEveryStepIsRecoverable(t *testing.T) {
	cases := []struct {
		step            string
		wantReinstalled bool
	}{
		{step: "replayed"},
		{step: "intent-written"},
		{step: "moved-aside"},
		{step: "installed", wantReinstalled: true},
		{step: "old-removed", wantReinstalled: true},
	}
	for _, tc := range cases {
		t.Run(tc.step, func(t *testing.T) {
			d := newStardewDaemon(t)
			modsDir := config.ModsDir("skyrimse")
			folder, _ := installForReinstall(t, d, "skyrimse", "Base", map[string]string{
				"plugin.esp": "plugin", "textures/a.dds": "texture",
			}, false)
			modDir := filepath.Join(modsDir, folder)
			reinstalled := snapshotTree(t, modDir)
			damageMod(t, modDir)
			damaged := snapshotTree(t, modDir)

			d.reinstallFault = func(step string) error {
				if step == tc.step {
					return errSimulatedCrash
				}
				return nil
			}
			if _, _, _, err := d.ReinstallMod(context.Background(), "skyrimse", folder, ""); !errors.Is(err, errSimulatedCrash) {
				t.Fatalf("ReinstallMod error = %v, want the simulated crash", err)
			}
			d.reinstallFault = nil

			restartDaemon(t, d)
			want := damaged
			if tc.wantReinstalled {
				want = reinstalled
			}
			if got := snapshotTree(t, modDir); !reflect.DeepEqual(got, want) {
				t.Errorf("mod after recovery:\n got %v\nwant %v", got, want)
			}
			assertNoReinstallState(t, modsDir)
		})
	}
}

func TestReinstallDoubleRenameFailureIsRecoveredAtStartup(t *testing.T) {
	d := newStardewDaemon(t)
	modsDir := config.ModsDir("skyrimse")
	folder, _ := installForReinstall(t, d, "skyrimse", "Base", map[string]string{
		"plugin.esp": "plugin", "textures/a.dds": "texture",
	}, false)
	modDir := filepath.Join(modsDir, folder)
	damageMod(t, modDir)
	damaged := snapshotTree(t, modDir)
	renameFailure := errors.New("rename refused")
	d.reinstallFault = func(step string) error {
		if step == "install" || step == "restore" {
			return renameFailure
		}
		return nil
	}

	if _, _, _, err := d.ReinstallMod(context.Background(), "skyrimse", folder, ""); !errors.Is(err, renameFailure) {
		t.Fatalf("ReinstallMod error = %v, want the rename failure", err)
	}
	if _, err := os.Lstat(modDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mod folder after double rename failure: %v, want moved aside", err)
	}
	d.reinstallFault = nil

	restartDaemon(t, d)
	if got := snapshotTree(t, modDir); !reflect.DeepEqual(got, damaged) {
		t.Errorf("mod after recovery:\n got %v\nwant %v", got, damaged)
	}
	assertNoReinstallState(t, modsDir)
}

func TestReinstallRecoveryReapsOrphans(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newStardewDaemon(t)
	modsDir := config.ModsDir("skyrimse")
	writeTestModMetadata := func(dir, folder string) {
		t.Helper()
		writeFixture(t, filepath.Join(dir, "plugin.esp"))
		if err := download.SaveModMetadata(dir, &download.ModMetadata{Name: folder, Folder: folder}); err != nil {
			t.Fatal(err)
		}
	}
	orphanStage := filepath.Join(modsDir, reinstallStagePrefix+"stage-token")
	writeFixture(t, filepath.Join(orphanStage, "partial.esp"))
	lostOld := filepath.Join(modsDir, reinstallOldPrefix+"lost-token")
	writeTestModMetadata(lostOld, "Lost")
	lostSnapshot := snapshotTree(t, lostOld)
	presentOld := filepath.Join(modsDir, reinstallOldPrefix+"present-token")
	writeTestModMetadata(presentOld, "Present")
	writeTestModMetadata(filepath.Join(modsDir, "Present"), "Present")
	presentSnapshot := snapshotTree(t, filepath.Join(modsDir, "Present"))
	anonymousOld := filepath.Join(modsDir, reinstallOldPrefix+"anonymous-token")
	writeFixture(t, filepath.Join(anonymousOld, "plugin.esp"))
	corruptIntent := filepath.Join(modsDir, reinstallIntentPrefix+"corrupt-token"+reinstallIntentSuffix)
	writeFixture(t, corruptIntent)
	claimedStage := filepath.Join(modsDir, reinstallStagePrefix+"corrupt-token")
	writeFixture(t, filepath.Join(claimedStage, "kept.esp"))

	restartDaemon(t, d)

	if _, err := os.Lstat(orphanStage); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("orphan staging folder: %v, want removed", err)
	}
	if got := snapshotTree(t, filepath.Join(modsDir, "Lost")); !reflect.DeepEqual(got, lostSnapshot) {
		t.Errorf("restored Lost = %v, want %v", got, lostSnapshot)
	}
	if _, err := os.Lstat(presentOld); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("previous copy of a present mod: %v, want moved to recovered folder", err)
	}
	if got := snapshotTree(t, filepath.Join(modsDir, ".gorganizer-recovered-present-token")); !reflect.DeepEqual(got, presentSnapshot) {
		t.Errorf("preserved previous copy = %v, want %v", got, presentSnapshot)
	}
	if got := snapshotTree(t, filepath.Join(modsDir, "Present")); !reflect.DeepEqual(got, presentSnapshot) {
		t.Errorf("present mod changed: %v, want %v", got, presentSnapshot)
	}
	for _, kept := range []string{anonymousOld, corruptIntent, claimedStage} {
		if _, err := os.Lstat(kept); err != nil {
			t.Errorf("%s was removed although its owner is unknown: %v", filepath.Base(kept), err)
		}
	}
}

// TestReinstallModTracksMountedProfileChangesDuringReplay checks that a mod enabled while sources replay is deployed at the swap.
func TestReinstallModTracksMountedProfileChangesDuringReplay(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	install := filepath.Join(t.TempDir(), "Stardew Valley")
	for _, marker := range []string{"Stardew Valley", "StardewValley"} {
		writeFixture(t, filepath.Join(install, marker))
	}
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		"stardewvalley": {Name: "Stardew Valley", InstallPath: install, DataSubpath: "Mods", SteamAppID: 413150},
	})
	modsDir := config.ModsDir("stardewvalley")
	enabled, _ := installForReinstall(t, d, "stardewvalley", "Enabled", map[string]string{
		"Enabled/manifest.json": `{"Name":"Enabled","UniqueID":"a.enabled","EntryDll":"E.dll"}`,
	}, false)
	disabled, _ := installForReinstall(t, d, "stardewvalley", "Disabled", map[string]string{
		"Disabled/manifest.json": `{"Name":"Disabled","UniqueID":"a.disabled","EntryDll":"D.dll"}`,
	}, false)
	if err := d.SetModList("stardewvalley", "Default", []dto.ModListEntryResult{
		{ModName: enabled, Enabled: true}, {ModName: disabled},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.MountVFS("stardewvalley", "Default"); err != nil {
		t.Fatalf("MountVFS: %v", err)
	}
	mounted := true
	t.Cleanup(func() {
		if mounted {
			_ = d.UnmountVFS("stardewvalley")
		}
	})
	farmFile := filepath.Join(install, "Mods", "Enabled", "manifest.json")
	if _, _, _, err := d.ReinstallMod(context.Background(), "stardewvalley", enabled, ""); err != nil {
		t.Fatalf("ReinstallMod(enabled): %v", err)
	}
	var st syscall.Stat_t
	if err := syscall.Stat(farmFile, &st); err != nil || st.Nlink != 2 {
		t.Errorf("farm file link count = %d (%v), want 2", st.Nlink, err)
	}

	d.reinstallFault = func(step string) error {
		if step == "replayed" {
			return d.SetModList("stardewvalley", "Default", []dto.ModListEntryResult{
				{ModName: enabled, Enabled: true}, {ModName: disabled, Enabled: true},
			})
		}
		return nil
	}
	if _, _, _, err := d.ReinstallMod(context.Background(), "stardewvalley", disabled, ""); err != nil {
		t.Fatalf("ReinstallMod enabled during replay: %v", err)
	}
	if _, err := os.Stat(filepath.Join(install, "Mods", "Disabled", "manifest.json")); err != nil {
		t.Errorf("mod enabled during replay was not deployed: %v", err)
	}
	assertNoReinstallState(t, modsDir)
	d.reinstallFault = nil
	if err := d.SetModList("stardewvalley", "Default", []dto.ModListEntryResult{
		{ModName: enabled, Enabled: true}, {ModName: disabled},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := d.ReinstallMod(context.Background(), "stardewvalley", disabled, ""); err != nil {
		t.Fatalf("ReinstallMod(disabled) while mounted: %v", err)
	}

	if err := d.UnmountVFS("stardewvalley"); err != nil {
		t.Fatalf("UnmountVFS: %v", err)
	}
	mounted = false
	if entries, err := os.ReadDir(filepath.Join(modsDir, "Overwrite")); err == nil && len(entries) != 0 {
		t.Errorf("unmount captured files into Overwrite: %v", entries)
	}
	if _, _, _, err := d.ReinstallMod(context.Background(), "stardewvalley", enabled, ""); err != nil {
		t.Fatalf("ReinstallMod(enabled) after unmount: %v", err)
	}
}

func TestReinstallModKeepsMetadataEditedDuringReplay(t *testing.T) {
	d := newStardewDaemon(t)
	modsDir := config.ModsDir("skyrimse")
	folder, _ := installForReinstall(t, d, "skyrimse", "Base", map[string]string{"plugin.esp": "plugin"}, false)
	modDir := filepath.Join(modsDir, folder)
	d.reinstallFault = func(step string) error {
		if step != "replayed" {
			return nil
		}
		for key, value := range map[string]string{"category": "Edited", "separator": "New Group", "true_index": "0x99", "visual_index": "0x77"} {
			if _, err := download.PatchModMetadataField(modDir, key, value); err != nil {
				return err
			}
		}
		return nil
	}
	if _, _, _, err := d.ReinstallMod(context.Background(), "skyrimse", folder, ""); err != nil {
		t.Fatalf("ReinstallMod: %v", err)
	}
	meta, err := download.LoadModMetadata(modDir)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Category != "Edited" || meta.Separator != "New Group" || meta.TrueIndex != "0x99" || meta.VisualIndex != "0x77" {
		t.Errorf("metadata edits made during the replay were lost: %+v", meta)
	}
	if len(meta.SourceArchives) != 1 || !reflect.DeepEqual(meta.Files, []string{"plugin.esp"}) {
		t.Errorf("replayed file keys = %+v", meta)
	}
}

func TestReinstallModConcurrentWithSetModListKeepsMetadataConsistent(t *testing.T) {
	d := newStardewDaemon(t)
	modsDir := config.ModsDir("skyrimse")
	folder, _ := installForReinstall(t, d, "skyrimse", "Base", map[string]string{"plugin.esp": "plugin"}, false)
	other, _ := installForReinstall(t, d, "skyrimse", "Other", map[string]string{"other.esp": "other"}, false)
	modDir := filepath.Join(modsDir, folder)
	for i := 0; i < 40; i++ {
		order := []dto.ModListEntryResult{{ModName: folder}, {ModName: other}}
		wantIndex := separators.FormatIndex(1 * trueIndexStep)
		if i%2 == 1 {
			order = []dto.ModListEntryResult{{ModName: other}, {ModName: folder}}
			wantIndex = separators.FormatIndex(2 * trueIndexStep)
		}
		var reinstallErr, setErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _, _, reinstallErr = d.ReinstallMod(context.Background(), "skyrimse", folder, "")
		}()
		go func() {
			defer wg.Done()
			setErr = d.SetModList("skyrimse", "Default", order)
		}()
		wg.Wait()
		if reinstallErr != nil || setErr != nil {
			t.Fatalf("iteration %d: ReinstallMod = %v, SetModList = %v", i, reinstallErr, setErr)
		}
		meta, err := download.LoadModMetadata(modDir)
		if err != nil {
			t.Fatal(err)
		}
		if meta.TrueIndex != wantIndex || len(meta.SourceArchives) != 1 || !reflect.DeepEqual(meta.Files, []string{"plugin.esp"}) || meta.Folder != folder {
			t.Fatalf("iteration %d metadata = %+v, want true_index %s with the replayed files", i, meta, wantIndex)
		}
	}
	assertNoReinstallState(t, modsDir)
}

func TestReinstallModRefusesFomodInstalledMods(t *testing.T) {
	d := newStardewDaemon(t)
	modsDir := config.ModsDir("skyrimse")
	folder, archive := installForReinstall(t, d, "skyrimse", "Chooser", map[string]string{"plugin.esp": "plugin"}, false)
	writeZipFiles(t, archive, map[string]string{"fomod/ModuleConfig.xml": "<config/>", "Option A/plugin.esp": "a"})
	modDir := filepath.Join(modsDir, folder)
	before := snapshotTree(t, modDir)

	var fomodErr *download.FomodReinstallUnsupportedError
	if _, _, _, err := d.ReinstallMod(context.Background(), "skyrimse", folder, ""); !errors.As(err, &fomodErr) || fomodErr.Mod != folder {
		t.Fatalf("ReinstallMod error = %v, want FomodReinstallUnsupportedError", err)
	}
	if got := snapshotTree(t, modDir); !reflect.DeepEqual(got, before) {
		t.Errorf("refused reinstall changed the mod:\n got %v\nwant %v", got, before)
	}
	assertNoReinstallState(t, modsDir)
}

func TestReinstallModRefusesAFifoSourceWithoutBlocking(t *testing.T) {
	d := newStardewDaemon(t)
	folder, archive := installForReinstall(t, d, "skyrimse", "Base", map[string]string{"plugin.esp": "plugin"}, false)
	if err := os.Remove(archive); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(archive, 0644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, _, err := d.ReinstallMod(context.Background(), "skyrimse", folder, "")
		done <- err
	}()
	select {
	case err := <-done:
		var missing *download.ReinstallSourceMissingError
		if !errors.As(err, &missing) {
			t.Fatalf("ReinstallMod error = %v, want ReinstallSourceMissingError", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ReinstallMod blocked on a FIFO source")
	}
}

func TestReinstallModRefusesASymlinkedModFolder(t *testing.T) {
	d := newStardewDaemon(t)
	modsDir := config.ModsDir("skyrimse")
	folder, _ := installForReinstall(t, d, "skyrimse", "Base", map[string]string{"plugin.esp": "plugin"}, false)
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Rename(filepath.Join(modsDir, folder), target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(modsDir, folder)); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, target)

	var invalid *download.InvalidTargetModError
	if _, _, _, err := d.ReinstallMod(context.Background(), "skyrimse", folder, ""); !errors.As(err, &invalid) {
		t.Fatalf("ReinstallMod error = %v, want InvalidTargetModError", err)
	}
	if info, err := os.Lstat(filepath.Join(modsDir, folder)); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("symlinked mod folder replaced: %v", err)
	}
	if got := snapshotTree(t, target); !reflect.DeepEqual(got, before) {
		t.Errorf("symlink target changed:\n got %v\nwant %v", got, before)
	}
	var notFound *ModNotFoundError
	if _, _, _, err := d.ReinstallMod(context.Background(), "skyrimse", "Missing", ""); !errors.As(err, &notFound) {
		t.Errorf("ReinstallMod(missing) error = %v, want ModNotFoundError", err)
	}
}

func TestReinstallModMergesCaseVariantManifestFolders(t *testing.T) {
	d := newStardewDaemon(t)
	modsDir := config.ModsDir("stardewvalley")
	folder, _ := installForReinstall(t, d, "stardewvalley", "SampleMod", map[string]string{
		"SampleMod/manifest.json": sampleManifest, "SampleMod/Sample.dll": "dll",
	}, false)
	update := filepath.Join(t.TempDir(), "Update.zip")
	writeZipFiles(t, update, map[string]string{
		"samplemod/manifest.json":    sampleManifest,
		"samplemod/assets/extra.png": "png",
	})
	if _, _, err := d.StartInstall(context.Background(), dto.StartInstallRequest{
		GameID: "stardewvalley", ExternalArchivePath: update, Mode: dto.InstallMergeIntoMod, TargetMod: folder,
	}); err != nil {
		t.Fatalf("merge install: %v", err)
	}
	modDir := filepath.Join(modsDir, folder)
	want := []string{"SampleMod/Sample.dll", "SampleMod/assets/extra.png", "SampleMod/manifest.json", "metadata.yaml"}
	if got := modFiles(t, modDir); !reflect.DeepEqual(got, want) {
		t.Fatalf("merged files = %v, want %v", got, want)
	}
	if _, _, _, err := d.ReinstallMod(context.Background(), "stardewvalley", folder, ""); err != nil {
		t.Fatalf("ReinstallMod: %v", err)
	}
	if got := modFiles(t, modDir); !reflect.DeepEqual(got, want) {
		t.Errorf("reinstalled files = %v, want %v", got, want)
	}
}

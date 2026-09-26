package smapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// collectWarnings returns an installer whose warnings are appended to the returned slice pointer.
func collectWarnings(runner Runner) (*Installer, *[]string) {
	var warnings []string
	in := newTestInstaller(runner)
	in.Progress = func(phase, detail string) {
		if phase == warningPhase {
			warnings = append(warnings, detail)
		}
	}
	return in, &warnings
}

// TestPayloadIdenticalGameFileIsRestored verifies a game file equal to the payload is still preserved and restored by uninstall.
func TestPayloadIdenticalGameFileIsRestored(t *testing.T) {
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	game := newTestGame(t)
	writeTestFile(t, game, "steam_appid.txt", "413150", 0640)
	pristine := snapshotGame(t, game)
	record, err := newTestInstaller(&fakeRunner{spec: testSpec()}).Install(context.Background(), game, art)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	want := digestBytes([]byte("413150"))
	if len(record.Originals) != 1 || record.Originals[0] != (OriginalFile{Path: "steam_appid.txt", SHA256: want.SHA256, Size: want.Size, Mode: 0640}) {
		t.Fatalf("record originals = %+v", record.Originals)
	}
	if err := newTestInstaller(&fakeRunner{spec: testSpec()}).Uninstall(context.Background(), game); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	after := snapshotGame(t, game)
	for rel := range after {
		if isWithin(rel, "Mods") {
			delete(after, rel)
		}
	}
	assertSnapshot(t, "after uninstall", pristine, after)
}

// TestUninstallWithMissingSavedOriginal verifies a vanished saved original is dropped with a warning and the loader copy is removed.
func TestUninstallWithMissingSavedOriginal(t *testing.T) {
	game := installed(t, writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2")))
	if err := os.Remove(filepath.Join(game, OriginalsDir, "steam_appid.txt")); err != nil {
		t.Fatal(err)
	}
	in, warnings := collectWarnings(&fakeRunner{spec: testSpec()})
	if err := in.Uninstall(context.Background(), game); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if len(*warnings) != 1 || !strings.Contains((*warnings)[0], "steam_appid.txt") {
		t.Fatalf("warnings = %v", *warnings)
	}
	for _, rel := range []string{"steam_appid.txt", RecordFile, OriginalsDir, "smapi-internal"} {
		assertMissing(t, game, rel)
	}
	if got := readGameFile(t, game, "StardewValley"); got != testVanillaLauncher {
		t.Fatalf("launcher = %q", got)
	}
	assertNoScratch(t, game)
}

// TestUninstallWithCorruptSavedOriginal verifies a saved original that fails verification is neither restored nor deleted.
func TestUninstallWithCorruptSavedOriginal(t *testing.T) {
	game := installed(t, writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2")))
	writeTestFile(t, game, OriginalsDir+"/steam_appid.txt", "999", 0644)
	in, warnings := collectWarnings(&fakeRunner{spec: testSpec()})
	if err := in.Uninstall(context.Background(), game); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if len(*warnings) != 1 {
		t.Fatalf("warnings = %v", *warnings)
	}
	assertMissing(t, game, "steam_appid.txt")
	if got := readGameFile(t, game, OriginalsDir+"/steam_appid.txt"); got != "999" {
		t.Fatalf("corrupt saved original was changed to %q", got)
	}
}

// TestUninstallWithoutRecordKeepsOriginals verifies an unrecorded uninstall leaves the saved originals it cannot verify.
func TestUninstallWithoutRecordKeepsOriginals(t *testing.T) {
	game := installed(t, writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2")))
	if err := os.Remove(filepath.Join(game, RecordFile)); err != nil {
		t.Fatal(err)
	}
	if err := newTestInstaller(&fakeRunner{spec: testSpec()}).Uninstall(context.Background(), game); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if got := readGameFile(t, game, OriginalsDir+"/steam_appid.txt"); got != "480" {
		t.Fatalf("saved original = %q", got)
	}
	assertMissing(t, game, "smapi-internal")
	assertNoScratch(t, game)
}

// TestInstallRefusesUnvouchedSavedOriginal verifies a leftover saved original is adopted only when it equals the game file.
func TestInstallRefusesUnvouchedSavedOriginal(t *testing.T) {
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	game := newTestGame(t)
	writeTestFile(t, game, OriginalsDir+"/steam_appid.txt", "999", 0644)
	before := snapshotGame(t, game)
	_, err := newTestInstaller(&fakeRunner{spec: testSpec()}).Install(context.Background(), game, art)
	var unsafe *UnsafeTargetError
	if !errors.As(err, &unsafe) || unsafe.Path != OriginalsDir+"/steam_appid.txt" {
		t.Fatalf("Install error = %v", err)
	}
	assertSnapshot(t, "after refusal", before, snapshotGame(t, game))
	assertNoScratch(t, game)
	writeTestFile(t, game, OriginalsDir+"/steam_appid.txt", "480", 0644)
	record, err := newTestInstaller(&fakeRunner{spec: testSpec()}).Install(context.Background(), game, art)
	if err != nil || len(record.Originals) != 1 {
		t.Fatalf("Install with a matching leftover = %+v, %v", record, err)
	}
}

// TestUpdateRestoresDroppedOriginal verifies an update whose payload stops shipping a preserved path restores the original in the same transaction.
func TestUpdateRestoresDroppedOriginal(t *testing.T) {
	dir := t.TempDir()
	game := installed(t, writeArtifact(t, dir, "4.5.2", testPayloadFiles("4.5.2")))
	files := testPayloadFiles("4.5.3")
	delete(files, "steam_appid.txt")
	record, err := newTestInstaller(&fakeRunner{spec: testSpec()}).Install(context.Background(), game, writeArtifact(t, dir, "4.5.3", files))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := readGameFile(t, game, "steam_appid.txt"); got != "480" {
		t.Fatalf("steam_appid.txt = %q", got)
	}
	if len(record.Originals) != 0 || contains(record.Targets, "steam_appid.txt") {
		t.Fatalf("record still tracks steam_appid.txt: %+v", record)
	}
	assertMissing(t, game, OriginalsDir)
	assertNoScratch(t, game)
	if err := newTestInstaller(&fakeRunner{spec: testSpec()}).Uninstall(context.Background(), game); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if got := readGameFile(t, game, "steam_appid.txt"); got != "480" {
		t.Fatalf("steam_appid.txt after uninstall = %q", got)
	}
}

// TestInstallRefusesUnownedDirectories verifies directories the loader does not own are never replaced.
func TestInstallRefusesUnownedDirectories(t *testing.T) {
	cases := []struct {
		name    string
		payload func(files map[string]string)
		setup   func(t *testing.T, game string)
		path    string
	}{
		{
			name:    "user directory shadowing a payload folder",
			payload: func(files map[string]string) { files["extras/tool.dll"] = "tool" },
			setup:   func(t *testing.T, game string) { writeTestFile(t, game, "extras/notes.txt", "mine", 0644) },
			path:    "extras",
		},
		{
			name: "user mod in a bundled mod folder",
			setup: func(t *testing.T, game string) {
				writeTestFile(t, game, "Mods/ConsoleCommands/manifest.json", bundledManifest("User.Mod", "1.0.0"), 0644)
			},
			path: "Mods/ConsoleCommands",
		},
		{
			name: "directory at a payload file path",
			setup: func(t *testing.T, game string) {
				if err := os.Remove(filepath.Join(game, "steam_appid.txt")); err != nil {
					t.Fatal(err)
				}
				writeTestFile(t, game, "steam_appid.txt/keep", "mine", 0644)
			},
			path: "steam_appid.txt",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := testPayloadFiles("4.5.2")
			if tc.payload != nil {
				tc.payload(files)
			}
			art := writeArtifact(t, t.TempDir(), "4.5.2", files)
			game := newTestGame(t)
			tc.setup(t, game)
			before := snapshotGame(t, game)
			_, err := newTestInstaller(&fakeRunner{spec: testSpec()}).Install(context.Background(), game, art)
			var unsafe *UnsafeTargetError
			if !errors.As(err, &unsafe) || unsafe.Path != tc.path {
				t.Fatalf("Install error = %v", err)
			}
			assertSnapshot(t, tc.name, before, snapshotGame(t, game))
			assertNoScratch(t, game)
		})
	}
}

// TestRecordClaimingGameContentIsRefused verifies install and uninstall refuse a record that claims a game-owned folder.
func TestRecordClaimingGameContentIsRefused(t *testing.T) {
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	game := installed(t, art)
	record, err := ReadRecord(game)
	if err != nil {
		t.Fatal(err)
	}
	record.Targets = append(record.Targets, "Content")
	record.Files = append(record.Files, RecordEntry{Path: "Content/Data/Fish.xnb", SHA256: digestBytes([]byte("fish")).SHA256, Size: 4, Mode: 0644})
	if err := writeRecord(game, record); err != nil {
		t.Fatal(err)
	}
	before := snapshotGame(t, game)
	if err := newTestInstaller(&fakeRunner{spec: testSpec()}).Uninstall(context.Background(), game); !errors.Is(err, ErrUnsafeTarget) {
		t.Fatalf("Uninstall error = %v", err)
	}
	if _, err := newTestInstaller(&fakeRunner{spec: testSpec()}).Install(context.Background(), game, art); !errors.Is(err, ErrUnsafeTarget) {
		t.Fatalf("Install error = %v", err)
	}
	assertSnapshot(t, "after refusals", before, snapshotGame(t, game))
	readGameFile(t, game, "Content/Data/Fish.xnb")
}

// TestInstallUpdatesRelocatedBundledMods verifies a bundled mod kept elsewhere under Mods is updated in place instead of duplicated.
func TestInstallUpdatesRelocatedBundledMods(t *testing.T) {
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	game := newTestGame(t)
	writeTestFile(t, game, "Mods/SMAPI/ConsoleCommands/manifest.json", bundledManifest("smapi.consolecommands", "4.0.0"), 0644)
	writeTestFile(t, game, "Mods/SMAPI/ConsoleCommands/ConsoleCommands.dll", "console 4.0.0", 0644)
	writeTestFile(t, game, "Mods/UserMod/manifest.json", bundledManifest("User.Mod", "1.0.0"), 0644)
	record, err := newTestInstaller(&fakeRunner{spec: testSpec()}).Install(context.Background(), game, art)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	assertMissing(t, game, "Mods/ConsoleCommands")
	if got := readGameFile(t, game, "Mods/SMAPI/ConsoleCommands/ConsoleCommands.dll"); got != "console 4.5.2" {
		t.Fatalf("relocated bundled mod = %q", got)
	}
	readGameFile(t, game, "Mods/SaveBackup/SaveBackup.dll")
	readGameFile(t, game, "Mods/UserMod/manifest.json")
	if !contains(record.Targets, "Mods/SMAPI/ConsoleCommands") || contains(record.Targets, "Mods/ConsoleCommands") {
		t.Fatalf("record targets = %v", record.Targets)
	}
	if _, err := newTestInstaller(&fakeRunner{spec: testSpec()}).Install(context.Background(), game, art); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	assertMissing(t, game, "Mods/ConsoleCommands")
	status, err := Inspect(game, testSpec())
	if err != nil || status.State != StateOK || !status.Managed {
		t.Fatalf("Inspect = %+v, %v", status, err)
	}
}

// TestInstallHandlesSwappedBundledFolders verifies bundled mods living in each other's folder names are each updated where they are.
func TestInstallHandlesSwappedBundledFolders(t *testing.T) {
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	game := newTestGame(t)
	writeTestFile(t, game, "Mods/SaveBackup/manifest.json", bundledManifest("SMAPI.ConsoleCommands", "4.0.0"), 0644)
	writeTestFile(t, game, "Mods/ConsoleCommands/manifest.json", bundledManifest("SMAPI.SaveBackup", "4.0.0"), 0644)
	if _, err := newTestInstaller(&fakeRunner{spec: testSpec()}).Install(context.Background(), game, art); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got := readGameFile(t, game, "Mods/SaveBackup/ConsoleCommands.dll"); got != "console 4.5.2" {
		t.Fatalf("console commands = %q", got)
	}
	if got := readGameFile(t, game, "Mods/ConsoleCommands/SaveBackup.dll"); got != "backup 4.5.2" {
		t.Fatalf("save backup = %q", got)
	}
	assertNoScratch(t, game)
}

// TestInstallRefusesBundledFolderClash verifies two bundled mods that would share one folder are refused before any change.
func TestInstallRefusesBundledFolderClash(t *testing.T) {
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	game := newTestGame(t)
	writeTestFile(t, game, "Mods/SaveBackup/manifest.json", bundledManifest("SMAPI.ConsoleCommands", "4.0.0"), 0644)
	before := snapshotGame(t, game)
	if _, err := newTestInstaller(&fakeRunner{spec: testSpec()}).Install(context.Background(), game, art); !errors.Is(err, ErrUnsafeTarget) {
		t.Fatalf("Install error = %v", err)
	}
	assertSnapshot(t, "after refusal", before, snapshotGame(t, game))
	assertNoScratch(t, game)
}

// TestInstallDetectsGameChangesDuringRun verifies inputs changed while the installer ran fail with ErrGameChanged and leave the game untouched.
func TestInstallDetectsGameChangesDuringRun(t *testing.T) {
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	updatedLauncher := testVanillaLauncher + "# updated by Steam\n"
	cases := []struct {
		name   string
		setup  func(t *testing.T) string
		change func(game string) error
	}{
		{"deps updated", newTestGame, func(game string) error {
			return os.WriteFile(filepath.Join(game, "Stardew Valley.deps.json"), []byte(`{"targets":{}}`), 0644)
		}},
		{"launcher restored during repair", func(t *testing.T) string { return installed(t, art) }, func(game string) error {
			return os.WriteFile(filepath.Join(game, "StardewValley"), []byte(updatedLauncher), 0755)
		}},
		{"vanilla launcher replaced", newTestGame, func(game string) error {
			return os.WriteFile(filepath.Join(game, "StardewValley"), []byte(updatedLauncher), 0755)
		}},
		{"user config edited", func(t *testing.T) string {
			game := installed(t, art)
			writeTestFile(t, game, "smapi-internal/config.user.json", "{}", 0644)
			return game
		}, func(game string) error {
			return os.WriteFile(filepath.Join(game, "smapi-internal", "config.user.json"), []byte(`{"edited": true}`), 0644)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			game := tc.setup(t)
			changed := ""
			runner := &fakeRunner{spec: testSpec(), during: func(string) error {
				if err := tc.change(game); err != nil {
					return err
				}
				changed = game
				return nil
			}}
			before := func() map[string]string {
				probe := cloneGame(t, game)
				if err := tc.change(probe); err != nil {
					t.Fatal(err)
				}
				return snapshotGame(t, probe)
			}()
			_, err := newTestInstaller(runner).Install(context.Background(), game, art)
			if !errors.Is(err, ErrGameChanged) || changed == "" {
				t.Fatalf("Install error = %v", err)
			}
			assertSnapshot(t, tc.name, before, snapshotGame(t, game))
			assertNoScratch(t, game)
		})
	}
}

// TestLauncherChangedBeforeBackupRollsBack verifies a launcher rewritten between preflight and its backup is detected in the backup and rolled back.
func TestLauncherChangedBeforeBackupRollsBack(t *testing.T) {
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	game := newTestGame(t)
	steam := testVanillaLauncher + "# steam update\n"
	in := newTestInstaller(&fakeRunner{spec: testSpec()})
	in.fault = func(step string) error {
		if step == "backup:StardewValley" {
			return os.WriteFile(filepath.Join(game, "StardewValley"), []byte(steam), 0755)
		}
		return nil
	}
	_, err := in.Install(context.Background(), game, art)
	if !errors.Is(err, ErrGameChanged) || errors.Is(err, ErrInterrupted) {
		t.Fatalf("Install error = %v", err)
	}
	if got := readGameFile(t, game, "StardewValley"); got != steam {
		t.Fatalf("launcher = %q", got)
	}
	for _, rel := range []string{"StardewModdingAPI", "smapi-internal", OriginalsDir, RecordFile} {
		assertMissing(t, game, rel)
	}
	if got := readGameFile(t, game, "steam_appid.txt"); got != "480" {
		t.Fatalf("steam_appid.txt = %q", got)
	}
	assertNoScratch(t, game)
}

// TestInstallRefusesFarmTransitions verifies loader operations refuse while a farm transition sibling or parked Mods backup exists.
func TestInstallRefusesFarmTransitions(t *testing.T) {
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	for _, sibling := range []string{"Mods.gorganizer-activating", "Mods.gorganizer-applying", "Mods.gorganizer-staging", "Mods.gorganizer-oldfarm", "Mods.orig"} {
		t.Run(sibling, func(t *testing.T) {
			game := installed(t, art)
			if err := os.Mkdir(filepath.Join(game, sibling), 0755); err != nil {
				t.Fatal(err)
			}
			before := snapshotGame(t, game)
			if _, err := newTestInstaller(&fakeRunner{spec: testSpec()}).Install(context.Background(), game, art); !errors.Is(err, ErrFarmMounted) {
				t.Fatalf("Install error = %v", err)
			}
			if err := newTestInstaller(&fakeRunner{spec: testSpec()}).Uninstall(context.Background(), game); !errors.Is(err, ErrFarmMounted) {
				t.Fatalf("Uninstall error = %v", err)
			}
			if _, err := Apply(game, TxnPlan{OpID: uuid.NewString(), Op: OpUninstall, Farm: testSpec().Farm}); !errors.Is(err, ErrFarmMounted) {
				t.Fatalf("Apply error = %v", err)
			}
			assertSnapshot(t, sibling, before, snapshotGame(t, game))
			assertNoScratch(t, game)
		})
	}
}

// TestVerifyStageUserConfig verifies a carried user config must survive byte-identical and an uncarried one must not appear.
func TestVerifyStageUserConfig(t *testing.T) {
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	cases := []struct {
		name  string
		setup func(t *testing.T) string
		stage func(stage string) error
		want  error
	}{
		{"carried config altered", func(t *testing.T) string {
			game := installed(t, art)
			writeTestFile(t, game, "smapi-internal/config.user.json", `{"DeveloperMode": true}`, 0644)
			return game
		}, func(stage string) error {
			return os.WriteFile(filepath.Join(stage, "smapi-internal", "config.user.json"), []byte(`{"ConsoleColorScheme": "DarkBackground"}`), 0644)
		}, ErrStageIncomplete},
		{"config created", newTestGame, func(stage string) error {
			return os.WriteFile(filepath.Join(stage, "smapi-internal", "config.user.json"), []byte(`{}`), 0644)
		}, ErrStageUnexpected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			game := tc.setup(t)
			before := snapshotGame(t, game)
			_, err := newTestInstaller(&fakeRunner{spec: testSpec(), during: tc.stage}).Install(context.Background(), game, art)
			if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), "config.user.json") {
				t.Fatalf("Install error = %v", err)
			}
			assertSnapshot(t, tc.name, before, snapshotGame(t, game))
			assertNoScratch(t, game)
		})
	}
}

// TestInspectReportsRecordAndDetails verifies Recorded, deps staleness, a missing launcher, and the metadata fast path.
func TestInspectReportsRecordAndDetails(t *testing.T) {
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	game := installed(t, art)
	writeTestFile(t, game, "StardewModdingAPI.dll", "assembly 9.9.9", 0644)
	status, err := Inspect(game, testSpec())
	if err != nil || status.Managed || !status.Recorded || status.RecordedVersion != "4.5.2" || status.Version != "" {
		t.Fatalf("Inspect of a modified install = %+v, %v", status, err)
	}
	game = installed(t, art)
	writeTestFile(t, game, "Stardew Valley.deps.json", `{"targets":{}}`, 0644)
	status, err = Inspect(game, testSpec())
	if err != nil || status.State != StateIncomplete || status.Detail != "loader deps out of date; repair" {
		t.Fatalf("Inspect with stale deps = %+v, %v", status, err)
	}
	if _, err := newTestInstaller(&fakeRunner{spec: testSpec()}).Install(context.Background(), game, art); err != nil {
		t.Fatalf("repair: %v", err)
	}
	if status, err = Inspect(game, testSpec()); err != nil || status.State != StateOK {
		t.Fatalf("Inspect after repair = %+v, %v", status, err)
	}
	if err := os.Remove(filepath.Join(game, "StardewValley")); err != nil {
		t.Fatal(err)
	}
	status, err = Inspect(game, testSpec())
	if err != nil || status.State != StateIncomplete || !strings.Contains(status.Detail, "StardewValley is missing") {
		t.Fatalf("Inspect without launcher = %+v, %v", status, err)
	}
	game = installed(t, art)
	full := filepath.Join(game, "StardewModdingAPI.dll")
	info, err := os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("assembly 4.5.9"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(full, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if status, err = Inspect(game, testSpec()); err != nil || !status.Managed {
		t.Fatalf("Inspect with unchanged metadata = %+v, %v; the fast path should trust size, mode, and mtime", status, err)
	}
	if err := os.Chtimes(full, info.ModTime().Add(1), info.ModTime().Add(1)); err != nil {
		t.Fatal(err)
	}
	if status, err = Inspect(game, testSpec()); err != nil || status.Managed {
		t.Fatalf("Inspect after an mtime change = %+v, %v; hashing should catch the edit", status, err)
	}
}

// TestEnsureDirMarksNewDirectoriesDirty verifies every created directory and its parent are queued for fsync.
func TestEnsureDirMarksNewDirectoriesDirty(t *testing.T) {
	game := newTestGame(t)
	mount, err := mountOf(game)
	if err != nil {
		t.Fatal(err)
	}
	tx := &txn{gameDir: game, mount: mount, dirty: map[string]bool{}}
	op := uuid.NewString()
	leaf := filepath.Join(game, BackupDir, op, "Mods")
	if err := tx.ensureDir(leaf); err != nil {
		t.Fatalf("ensureDir: %v", err)
	}
	for _, dir := range []string{game, filepath.Join(game, BackupDir), filepath.Join(game, BackupDir, op), leaf} {
		if !tx.dirty[dir] {
			t.Fatalf("%s is not marked dirty: %v", dir, tx.dirty)
		}
	}
	if err := tx.flush(); err != nil || len(tx.dirty) != 0 {
		t.Fatalf("flush = %v, dirty = %v", err, tx.dirty)
	}
}

// TestFarmGuardFollowsTheConfiguredDeployFolder locks that the farm check looks at the deploy folder and names the daemon passes in, and refuses without them.
func TestFarmGuardFollowsTheConfiguredDeployFolder(t *testing.T) {
	game := newTestGame(t)
	guard := FarmGuard{DeployDir: "Deploy/Mods", Sentinel: "farm.marker", SiblingSuffixes: []string{".parked"}}
	if err := checkFarm(game, guard); err != nil {
		t.Fatalf("checkFarm on a clean deploy folder: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(game, "Mods"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(game, "Mods", ".gorganizer-overlay.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := checkFarm(game, guard); err != nil {
		t.Fatalf("checkFarm looked outside the configured deploy folder: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(game, "Deploy", "Mods"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(game, "Deploy", "Mods", "farm.marker"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := checkFarm(game, guard); !errors.Is(err, ErrFarmMounted) {
		t.Fatalf("checkFarm with the configured sentinel = %v, want ErrFarmMounted", err)
	}
	if err := os.Remove(filepath.Join(game, "Deploy", "Mods", "farm.marker")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(game, "Deploy", "Mods.parked"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := checkFarm(game, guard); !errors.Is(err, ErrFarmMounted) {
		t.Fatalf("checkFarm with the configured sibling = %v, want ErrFarmMounted", err)
	}
	for _, incomplete := range []FarmGuard{{}, {DeployDir: "Mods"}, {DeployDir: "Mods", Sentinel: "x"}, {DeployDir: "../Mods", Sentinel: "x", SiblingSuffixes: []string{".orig"}}} {
		if err := checkFarm(game, incomplete); err == nil || errors.Is(err, ErrFarmMounted) {
			t.Errorf("checkFarm(%+v) = %v, want a refusal of the incomplete guard", incomplete, err)
		}
	}
}

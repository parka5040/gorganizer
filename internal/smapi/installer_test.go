package smapi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestInstallSuccess verifies a staged install places the payload, preserves the game's steam_appid.txt, and commits a record.
func TestInstallSuccess(t *testing.T) {
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	game := newTestGame(t)
	runner := &fakeRunner{spec: testSpec()}
	in := newTestInstaller(runner)
	var phases []string
	in.Progress = func(phase, detail string) { phases = append(phases, phase) }
	record, err := in.Install(context.Background(), game, art)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if record == nil || record.Version != "4.5.2" || record.ArtifactSHA256 != art.SHA256 || record.Loader != "smapi" {
		t.Fatalf("record = %+v", record)
	}
	appID := digestBytes([]byte("480"))
	if !reflect.DeepEqual(record.Originals, []OriginalFile{{Path: "steam_appid.txt", SHA256: appID.SHA256, Size: appID.Size, Mode: 0644}}) {
		t.Fatalf("record originals = %v", record.Originals)
	}
	wantTargets := []string{
		"Mods/ConsoleCommands", "Mods/SaveBackup", "StardewModdingAPI", "StardewModdingAPI.deps.json",
		"StardewModdingAPI.dll", "StardewModdingAPI.runtimeconfig.json", "StardewModdingAPI.xml",
		"StardewValley", "StardewValley-original", "smapi-internal", "steam_appid.txt",
	}
	if !reflect.DeepEqual(record.Targets, wantTargets) {
		t.Fatalf("record targets = %v", record.Targets)
	}
	if got := readGameFile(t, game, "StardewValley"); got != testSMAPILauncher {
		t.Fatalf("launcher = %q", got)
	}
	if got := readGameFile(t, game, "StardewValley-original"); got != testVanillaLauncher {
		t.Fatalf("launcher backup = %q", got)
	}
	if got := readGameFile(t, game, "steam_appid.txt"); got != "413150" {
		t.Fatalf("steam_appid.txt = %q", got)
	}
	if got := readGameFile(t, game, OriginalsDir+"/steam_appid.txt"); got != "480" {
		t.Fatalf("preserved steam_appid.txt = %q", got)
	}
	if got := readGameFile(t, game, "StardewModdingAPI.deps.json"); got != testDepsJSON {
		t.Fatalf("loader deps = %q", got)
	}
	for rel, want := range map[string]os.FileMode{
		"StardewValley":                    0755,
		"StardewValley-original":           0755,
		"StardewModdingAPI":                0755,
		"StardewModdingAPI.dll":            0644,
		"smapi-internal/SMAPI.Toolkit.dll": 0644,
		"smapi-internal":                   0755,
	} {
		if got := fileMode(t, game, rel); got != want {
			t.Fatalf("mode of %s = %o, want %o", rel, got, want)
		}
	}
	readGameFile(t, game, "Mods/ConsoleCommands/ConsoleCommands.dll")
	readGameFile(t, game, "Mods/SaveBackup/SaveBackup.dll")
	assertMissing(t, game, "Mods/Stray")
	assertMissing(t, game, "unix-launcher.sh")
	assertNoScratch(t, game)
	paths := map[string]bool{}
	for _, file := range record.Files {
		paths[file.Path] = true
	}
	for _, rel := range []string{"StardewValley", "StardewValley-original", "StardewModdingAPI", "steam_appid.txt", "StardewModdingAPI.deps.json", "smapi-internal/i18n/default.json", "Mods/SaveBackup/manifest.json"} {
		if !paths[rel] {
			t.Fatalf("record is missing %s: %v", rel, record.Files)
		}
	}
	for _, rel := range []string{"Stardew Valley.dll", "Stardew Valley.deps.json", "Content/Data/Fish.xnb"} {
		if paths[rel] {
			t.Fatalf("record lists game file %s", rel)
		}
	}
	onDisk, err := ReadRecord(game)
	if err != nil || onDisk == nil || onDisk.OpID != record.OpID {
		t.Fatalf("ReadRecord = %+v, %v", onDisk, err)
	}
	status, err := Inspect(game, testSpec())
	if err != nil || status.State != StateOK || !status.Managed || status.Version != "4.5.2" {
		t.Fatalf("Inspect = %+v, %v", status, err)
	}
	want := []string{"verify", "extract", "stage", "run", "check", "apply", "applied"}
	if !reflect.DeepEqual(phases, want) {
		t.Fatalf("phases = %v, want %v", phases, want)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %d", len(runner.calls))
	}
	cmd := runner.calls[0]
	stage := argAfter(cmd.Args, "--game-path")
	if !reflect.DeepEqual(cmd.Args, []string{"--install", "--no-prompt", "--game-path", stage}) {
		t.Fatalf("args = %v", cmd.Args)
	}
	workRoot := filepath.Join(game, WorkDir) + string(filepath.Separator)
	if !strings.HasPrefix(stage, workRoot) || stage == game {
		t.Fatalf("installer game path %s is not a private stage", stage)
	}
	if cmd.Dir != filepath.Dir(cmd.Path) || filepath.Base(cmd.Path) != "SMAPI.Installer" || cmd.Timeout != defaultInstallerTimeout {
		t.Fatalf("command = %+v", cmd)
	}
	work := strings.TrimSuffix(stage, filepath.Join("stage", "game"))
	env := map[string]string{}
	for _, kv := range cmd.Env {
		key, value, _ := strings.Cut(kv, "=")
		env[key] = value
	}
	wantEnv := map[string]string{
		"PATH":                                  "/usr/bin:/bin",
		"HOME":                                  filepath.Join(work, "home"),
		"XDG_CONFIG_HOME":                       filepath.Join(work, "home", ".config"),
		"XDG_DATA_HOME":                         filepath.Join(work, "home", ".local", "share"),
		"XDG_CACHE_HOME":                        filepath.Join(work, "home", ".cache"),
		"TMPDIR":                                filepath.Join(work, "tmp"),
		"TERM":                                  "dumb",
		"LANG":                                  "C.UTF-8",
		"DOTNET_SYSTEM_GLOBALIZATION_INVARIANT": "1",
		"DOTNET_CLI_TELEMETRY_OPTOUT":           "1",
	}
	if !reflect.DeepEqual(env, wantEnv) {
		t.Fatalf("env = %v", cmd.Env)
	}
}

// TestInstallRejectsExitZeroNoop verifies an installer that exits 0 without doing anything is rejected and the game is untouched.
func TestInstallRejectsExitZeroNoop(t *testing.T) {
	assertInstallRejected(t, &fakeRunner{mode: "noop", spec: testSpec()}, 0, func(err error) bool {
		return errors.Is(err, ErrStageIncomplete) && strings.Contains(err.Error(), "SMAPI is installed!")
	})
}

// TestInstallRejectsUnexpectedFiles verifies an extra stage file is refused.
func TestInstallRejectsUnexpectedFiles(t *testing.T) {
	assertInstallRejected(t, &fakeRunner{mode: "garbage", spec: testSpec()}, 0, func(err error) bool {
		return errors.Is(err, ErrStageUnexpected) && strings.Contains(err.Error(), "garbage.txt")
	})
}

// TestInstallRejectsHardLinkedStageFiles verifies a stage file hard-linked outside the stage is refused.
func TestInstallRejectsHardLinkedStageFiles(t *testing.T) {
	assertInstallRejected(t, &fakeRunner{mode: "hardlink", spec: testSpec()}, 0, func(err error) bool {
		return errors.Is(err, ErrStageUnexpected) && strings.Contains(err.Error(), "smapi-internal/config.json (hard link)")
	})
}

// TestInstallTimeout verifies a hung installer is abandoned after the timeout without touching the game.
func TestInstallTimeout(t *testing.T) {
	assertInstallRejected(t, &fakeRunner{mode: "hang", spec: testSpec()}, 50*time.Millisecond, func(err error) bool {
		return errors.Is(err, context.DeadlineExceeded)
	})
}

// TestInstallNonZeroExit verifies a crashing installer is reported with its output.
func TestInstallNonZeroExit(t *testing.T) {
	assertInstallRejected(t, &fakeRunner{mode: "exit", spec: testSpec()}, 0, func(err error) bool {
		return strings.Contains(err.Error(), "code 3") && strings.Contains(err.Error(), "boom")
	})
}

// assertInstallRejected runs a failing install and checks the error and that the game is unchanged and clean.
func assertInstallRejected(t *testing.T, runner Runner, timeout time.Duration, match func(error) bool) {
	t.Helper()
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	game := newTestGame(t)
	before := snapshotGame(t, game)
	in := newTestInstaller(runner)
	in.Timeout = timeout
	_, err := in.Install(context.Background(), game, art)
	if err == nil || !match(err) {
		t.Fatalf("Install error = %v", err)
	}
	assertSnapshot(t, "after rejected install", before, snapshotGame(t, game))
	assertNoScratch(t, game)
}

// TestInstallRefusesUnsafeTargets verifies symlinked, hard-linked, and farm-mounted targets are refused before any change.
func TestInstallRefusesUnsafeTargets(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, game string)
		check func(err error) bool
	}{
		{
			name: "symlinked smapi-internal",
			setup: func(t *testing.T, game string) {
				elsewhere := t.TempDir()
				if err := os.Symlink(elsewhere, filepath.Join(game, "smapi-internal")); err != nil {
					t.Fatal(err)
				}
			},
			check: func(err error) bool {
				var unsafe *UnsafeTargetError
				return errors.As(err, &unsafe) && unsafe.Path == "smapi-internal" && errors.Is(err, ErrUnsafeTarget)
			},
		},
		{
			name: "hard-linked steam_appid.txt",
			setup: func(t *testing.T, game string) {
				if err := os.Link(filepath.Join(game, "steam_appid.txt"), filepath.Join(t.TempDir(), "appid-link")); err != nil {
					t.Skipf("cannot hard link across temp dirs: %v", err)
				}
			},
			check: func(err error) bool {
				var unsafe *UnsafeTargetError
				return errors.As(err, &unsafe) && unsafe.Path == "steam_appid.txt" && strings.Contains(unsafe.Reason, "hard links")
			},
		},
		{
			name: "mounted farm",
			setup: func(t *testing.T, game string) {
				if err := os.MkdirAll(filepath.Join(game, "Mods"), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(game, "Mods", testSpec().Farm.Sentinel), []byte("{}"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			check: func(err error) bool { return errors.Is(err, ErrFarmMounted) },
		},
		{
			name: "symlinked Mods",
			setup: func(t *testing.T, game string) {
				if err := os.Symlink(t.TempDir(), filepath.Join(game, "Mods")); err != nil {
					t.Fatal(err)
				}
			},
			check: func(err error) bool { return errors.Is(err, ErrUnsafeTarget) },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
			game := newTestGame(t)
			tc.setup(t, game)
			before := snapshotGame(t, game)
			_, err := newTestInstaller(&fakeRunner{spec: testSpec()}).Install(context.Background(), game, art)
			if err == nil || !tc.check(err) {
				t.Fatalf("Install error = %v", err)
			}
			assertSnapshot(t, tc.name, before, snapshotGame(t, game))
			assertNoScratch(t, game)
		})
	}
}

// TestReinstallOverManagedInstall verifies a repair keeps the preserved original, carries the user config, and stays managed.
func TestReinstallOverManagedInstall(t *testing.T) {
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	game := installed(t, art)
	userConfig := `{"DeveloperMode": true}`
	if err := os.WriteFile(filepath.Join(game, "smapi-internal", "config.user.json"), []byte(userConfig), 0644); err != nil {
		t.Fatal(err)
	}
	in := newTestInstaller(&fakeRunner{spec: testSpec()})
	plan := planFor(t, in, game, art)
	if len(plan.PreserveOriginals) != 0 {
		t.Fatalf("originals re-captured: %v", plan.PreserveOriginals)
	}
	if len(plan.Originals) != 1 || plan.Originals[0].Path != "steam_appid.txt" || len(plan.Restore) != 0 || len(plan.DropOriginals) != 0 {
		t.Fatalf("carried originals = %v, restore = %v, drop = %v", plan.Originals, plan.Restore, plan.DropOriginals)
	}
	if len(plan.Remove) != 0 {
		t.Fatalf("remove = %v, want nothing beyond the placed entries", plan.Remove)
	}
	wantPlace := []string{
		"Mods/ConsoleCommands", "Mods/SaveBackup", "StardewModdingAPI", "StardewModdingAPI.deps.json",
		"StardewModdingAPI.dll", "StardewModdingAPI.runtimeconfig.json", "StardewModdingAPI.xml",
		"StardewValley", "StardewValley-original", "smapi-internal", "steam_appid.txt",
	}
	if !reflect.DeepEqual(plan.Place, wantPlace) {
		t.Fatalf("place = %v", plan.Place)
	}
	record, err := in.Install(context.Background(), game, art)
	if err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if len(record.Originals) != 1 || record.Originals[0].Path != "steam_appid.txt" {
		t.Fatalf("record originals = %v", record.Originals)
	}
	if got := readGameFile(t, game, OriginalsDir+"/steam_appid.txt"); got != "480" {
		t.Fatalf("preserved original = %q", got)
	}
	if got := readGameFile(t, game, "smapi-internal/config.user.json"); got != userConfig {
		t.Fatalf("user config = %q", got)
	}
	status, err := Inspect(game, testSpec())
	if err != nil || status.State != StateOK || !status.Managed {
		t.Fatalf("Inspect = %+v, %v", status, err)
	}
	assertNoScratch(t, game)
}

// planFor stages art against game the way Install does, without running the transaction, and returns the resulting plan.
func planFor(t *testing.T, in *Installer, game string, art Artifact) TxnPlan {
	t.Helper()
	work := t.TempDir()
	bundle, err := OpenBundle(art, filepath.Join(work, "bundle"), in.Spec)
	if err != nil {
		t.Fatalf("OpenBundle: %v", err)
	}
	payload, err := bundle.Payload(in.Spec)
	if err != nil {
		t.Fatalf("Payload: %v", err)
	}
	stage := filepath.Join(work, "stage", "game")
	stageIn, err := BuildStage(game, stage, in.Spec)
	if err != nil {
		t.Fatalf("BuildStage: %v", err)
	}
	if err := simulateUpstream(stage, bundle.PayloadPath, in.Spec); err != nil {
		t.Fatalf("simulateUpstream: %v", err)
	}
	bundled, err := in.relocateBundledMods(game, stage, work, payload)
	if err != nil {
		t.Fatalf("relocateBundledMods: %v", err)
	}
	plan, err := in.installPlan(game, payload, stageIn, bundled)
	if err != nil {
		t.Fatalf("installPlan: %v", err)
	}
	return plan
}

// readTestPayload extracts and indexes the payload of art in a temporary directory.
func readTestPayload(t *testing.T, art Artifact) Payload {
	t.Helper()
	bundle, err := OpenBundle(art, t.TempDir(), testSpec())
	if err != nil {
		t.Fatalf("OpenBundle: %v", err)
	}
	payload, err := ReadPayload(bundle.PayloadPath, testSpec())
	if err != nil {
		t.Fatalf("ReadPayload: %v", err)
	}
	return payload
}

// TestUpdateRemovesDroppedFiles verifies files recorded by the previous version but absent from the new payload are removed.
func TestUpdateRemovesDroppedFiles(t *testing.T) {
	dir := t.TempDir()
	oldFiles := testPayloadFiles("4.5.1")
	oldFiles["StardewModdingAPI.Legacy.dll"] = "legacy"
	oldFiles["smapi-internal/Old.dll"] = "old"
	game := installed(t, writeArtifact(t, dir, "4.5.1", oldFiles))
	readGameFile(t, game, "StardewModdingAPI.Legacy.dll")
	record, err := newTestInstaller(&fakeRunner{spec: testSpec()}).Install(context.Background(), game, writeArtifact(t, dir, "4.5.2", testPayloadFiles("4.5.2")))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	assertMissing(t, game, "StardewModdingAPI.Legacy.dll")
	assertMissing(t, game, "smapi-internal/Old.dll")
	if record.Version != "4.5.2" || readGameFile(t, game, "StardewModdingAPI.dll") != "assembly 4.5.2" {
		t.Fatalf("update did not place the new payload")
	}
	if got := readGameFile(t, game, OriginalsDir+"/steam_appid.txt"); got != "480" {
		t.Fatalf("preserved original = %q", got)
	}
	status, err := Inspect(game, testSpec())
	if err != nil || status.State != StateOK || status.Version != "4.5.2" {
		t.Fatalf("Inspect = %+v, %v", status, err)
	}
}

// TestRepairAfterLauncherRevert verifies a Steam-restored launcher is detected and repaired using the freshest vanilla launcher.
func TestRepairAfterLauncherRevert(t *testing.T) {
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	game := installed(t, art)
	updated := testVanillaLauncher + "# updated by Steam\n"
	if err := os.WriteFile(filepath.Join(game, "StardewValley"), []byte(updated), 0755); err != nil {
		t.Fatal(err)
	}
	status, err := Inspect(game, testSpec())
	if err != nil || status.State != StateLauncherReverted || !status.Managed || !status.Recorded {
		t.Fatalf("Inspect after revert = %+v, %v", status, err)
	}
	if _, err := newTestInstaller(&fakeRunner{spec: testSpec()}).Install(context.Background(), game, art); err != nil {
		t.Fatalf("repair: %v", err)
	}
	if got := readGameFile(t, game, "StardewValley-original"); got != updated {
		t.Fatalf("launcher backup = %q, want the Steam-restored launcher", got)
	}
	if got := readGameFile(t, game, "StardewValley"); got != testSMAPILauncher {
		t.Fatalf("launcher = %q", got)
	}
	status, err = Inspect(game, testSpec())
	if err != nil || status.State != StateOK || !status.Managed {
		t.Fatalf("Inspect after repair = %+v, %v", status, err)
	}
}

// TestUninstallRestoresVanilla verifies uninstall restores the launcher and originals, removes SMAPI, and keeps Mods content.
func TestUninstallRestoresVanilla(t *testing.T) {
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	pristine := snapshotGame(t, newTestGame(t))
	game := installed(t, art)
	userMod := filepath.Join(game, "Mods", "UserMod")
	if err := os.MkdirAll(userMod, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userMod, "manifest.json"), []byte(bundledManifest("User.Mod", "1.0.0")), 0644); err != nil {
		t.Fatal(err)
	}
	if err := newTestInstaller(&fakeRunner{spec: testSpec()}).Uninstall(context.Background(), game); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if got := readGameFile(t, game, "StardewValley"); got != testVanillaLauncher {
		t.Fatalf("launcher = %q", got)
	}
	if got := fileMode(t, game, "StardewValley"); got != 0755 {
		t.Fatalf("launcher mode = %o", got)
	}
	if got := readGameFile(t, game, "steam_appid.txt"); got != "480" {
		t.Fatalf("steam_appid.txt = %q", got)
	}
	for _, rel := range []string{"StardewValley-original", "StardewModdingAPI", "StardewModdingAPI.dll", "StardewModdingAPI.deps.json", "StardewModdingAPI.xml", "StardewModdingAPI.runtimeconfig.json", "smapi-internal", RecordFile, OriginalsDir} {
		assertMissing(t, game, rel)
	}
	readGameFile(t, game, "Mods/ConsoleCommands/ConsoleCommands.dll")
	readGameFile(t, game, "Mods/UserMod/manifest.json")
	assertNoScratch(t, game)
	after := snapshotGame(t, game)
	for rel := range after {
		if isWithin(rel, "Mods") {
			delete(after, rel)
		}
	}
	assertSnapshot(t, "after uninstall", pristine, after)
	status, err := Inspect(game, testSpec())
	if err != nil || status.State != StateNotInstalled || status.Managed {
		t.Fatalf("Inspect = %+v, %v", status, err)
	}
}

// TestUninstallKeepsSteamRestoredLauncher verifies uninstall keeps a current vanilla launcher instead of restoring an older backup.
func TestUninstallKeepsSteamRestoredLauncher(t *testing.T) {
	game := installed(t, writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2")))
	updated := testVanillaLauncher + "# updated by Steam\n"
	if err := os.WriteFile(filepath.Join(game, "StardewValley"), []byte(updated), 0755); err != nil {
		t.Fatal(err)
	}
	if err := newTestInstaller(&fakeRunner{spec: testSpec()}).Uninstall(context.Background(), game); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if got := readGameFile(t, game, "StardewValley"); got != updated {
		t.Fatalf("launcher = %q", got)
	}
	assertMissing(t, game, "StardewValley-original")
	assertMissing(t, game, "smapi-internal")
}

// TestUninstallWithoutVanillaLauncher verifies uninstall refuses when no vanilla launcher can be found.
func TestUninstallWithoutVanillaLauncher(t *testing.T) {
	game := installed(t, writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2")))
	if err := os.WriteFile(filepath.Join(game, "StardewValley-original"), []byte(testSMAPILauncher), 0755); err != nil {
		t.Fatal(err)
	}
	before := snapshotGame(t, game)
	err := newTestInstaller(&fakeRunner{spec: testSpec()}).Uninstall(context.Background(), game)
	if !errors.Is(err, ErrNoVanillaLauncher) {
		t.Fatalf("Uninstall error = %v", err)
	}
	assertSnapshot(t, "after refused uninstall", before, snapshotGame(t, game))
	assertNoScratch(t, game)
}

// TestInstallRefusesInterruptedAndUnsupported verifies installs are refused while recovery is pending or the build is unsupported.
func TestInstallRefusesInterruptedAndUnsupported(t *testing.T) {
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	game := newTestGame(t)
	if err := os.WriteFile(filepath.Join(game, IntentFile), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := newTestInstaller(&fakeRunner{spec: testSpec()}).Install(context.Background(), game, art); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("Install with intent = %v", err)
	}
	game = newTestGame(t)
	if err := os.WriteFile(filepath.Join(game, "Stardew Valley.exe"), []byte("MZ"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := newTestInstaller(&fakeRunner{spec: testSpec()}).Install(context.Background(), game, art); !errors.Is(err, ErrUnsupportedBuild) {
		t.Fatalf("Install on Windows build = %v", err)
	}
	if err := newTestInstaller(&fakeRunner{spec: testSpec()}).Uninstall(context.Background(), game); !errors.Is(err, ErrUnsupportedBuild) {
		t.Fatalf("Uninstall on Windows build = %v", err)
	}
}

// TestInspectStates verifies every loader state classification.
func TestInspectStates(t *testing.T) {
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	cases := []struct {
		name    string
		setup   func(t *testing.T) string
		state   LoaderState
		managed bool
	}{
		{"not installed", newTestGame, StateNotInstalled, false},
		{"ok", func(t *testing.T) string { return installed(t, art) }, StateOK, true},
		{"launcher reverted", func(t *testing.T) string {
			game := installed(t, art)
			copyTestFile(t, filepath.Join(game, "StardewValley-original"), filepath.Join(game, "StardewValley"))
			return game
		}, StateLauncherReverted, true},
		{"incomplete", func(t *testing.T) string {
			game := installed(t, art)
			if err := os.Remove(filepath.Join(game, "StardewModdingAPI.dll")); err != nil {
				t.Fatal(err)
			}
			return game
		}, StateIncomplete, false},
		{"loader not executable", func(t *testing.T) string {
			game := installed(t, art)
			if err := os.Chmod(filepath.Join(game, "StardewModdingAPI"), 0644); err != nil {
				t.Fatal(err)
			}
			return game
		}, StateIncomplete, true},
		{"unmanaged manual install", func(t *testing.T) string {
			game := installed(t, art)
			if err := os.Remove(filepath.Join(game, RecordFile)); err != nil {
				t.Fatal(err)
			}
			return game
		}, StateOK, false},
		{"user config edits stay managed", func(t *testing.T) string {
			game := installed(t, art)
			if err := os.WriteFile(filepath.Join(game, "smapi-internal", "config.user.json"), []byte("{}"), 0644); err != nil {
				t.Fatal(err)
			}
			return game
		}, StateOK, true},
		{"windows build", func(t *testing.T) string {
			game := newTestGame(t)
			if err := os.WriteFile(filepath.Join(game, "Stardew Valley.exe"), []byte("MZ"), 0644); err != nil {
				t.Fatal(err)
			}
			return game
		}, StateUnsupportedBuild, false},
		{"no native executable", func(t *testing.T) string {
			game := newTestGame(t)
			if err := os.Remove(filepath.Join(game, "Stardew Valley")); err != nil {
				t.Fatal(err)
			}
			return game
		}, StateUnsupportedBuild, false},
		{"launcher missing", func(t *testing.T) string {
			game := installed(t, art)
			if err := os.Remove(filepath.Join(game, "StardewValley")); err != nil {
				t.Fatal(err)
			}
			return game
		}, StateIncomplete, true},
		{"loader deps stale", func(t *testing.T) string {
			game := installed(t, art)
			writeTestFile(t, game, "Stardew Valley.deps.json", `{"targets":{".NETCoreApp,Version=v6.0":{"Stardew Valley/1.6.16.1":{}}}}`, 0644)
			return game
		}, StateIncomplete, true},
		{"modified loader file", func(t *testing.T) string {
			game := installed(t, art)
			writeTestFile(t, game, "StardewModdingAPI.dll", "assembly 9.9.9", 0644)
			return game
		}, StateOK, false},
		{"interrupted", func(t *testing.T) string {
			game := installed(t, art)
			if err := os.WriteFile(filepath.Join(game, IntentFile), []byte("{}"), 0644); err != nil {
				t.Fatal(err)
			}
			return game
		}, StateInterrupted, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			game := tc.setup(t)
			status, err := Inspect(game, testSpec())
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			if status.State != tc.state || status.Managed != tc.managed {
				t.Fatalf("Inspect = %+v (%s), want state %s managed %t", status, status.State, tc.state, tc.managed)
			}
			if status.Managed != (status.Version == "4.5.2") {
				t.Fatalf("Inspect version = %q with managed %t", status.Version, status.Managed)
			}
		})
	}
}

// copyTestFile copies src over dst with mode 0755.
func copyTestFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0755); err != nil {
		t.Fatal(err)
	}
}

// TestLoaderStateString verifies the wire names of loader states.
func TestLoaderStateString(t *testing.T) {
	want := map[LoaderState]string{
		StateUnknown: "unknown", StateNotInstalled: "not_installed", StateOK: "ok", StateLauncherReverted: "launcher_reverted",
		StateIncomplete: "incomplete", StateUnsupportedBuild: "unsupported_build", StateInterrupted: "interrupted",
	}
	for state, name := range want {
		if state.String() != name {
			t.Fatalf("%d.String() = %q, want %q", state, state.String(), name)
		}
	}
}

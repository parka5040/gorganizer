package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/instancelock"
	"github.com/parka/gorganizer/internal/smapi"
	"github.com/parka/gorganizer/internal/steam"
	"github.com/parka/gorganizer/internal/testsafe"
	"github.com/parka/gorganizer/internal/vfs"
)

// TestMain runs the command tests with isolated directories and failing launcher shims.
func TestMain(m *testing.M) {
	if os.Getenv("FAKE_SESSION_DAEMON") == "1" || os.Getenv("FAKE_SESSION_SUPERVISOR") == "1" {
		os.Exit(m.Run())
	}
	os.Exit(testsafe.RunWithSafeEnvironment(m))
}

// TestPrintVersion checks that the maintenance command reports its stamped version.
func TestPrintVersion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var out bytes.Buffer
	printVersion(&out)
	if want := "gorganizerctl " + version + "\n"; out.String() != want {
		t.Fatalf("version output = %q, want %q", out.String(), want)
	}
}

// TestResolveDataPathExplicitPath verifies a supplied path does not require config or Steam discovery.
func TestResolveDataPathExplicitPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "Data")
	got, err := resolveDataPath("", path)
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("resolveDataPath() = %q, want %q", got, path)
	}
}

// recoverFixture creates isolated game settings, a game install and an empty process table.
func recoverFixture(t *testing.T) (recoveryDeps, string, string, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "runtime"))
	t.Setenv("GORGANIZER_ROOT", root)
	if err := os.Mkdir(filepath.Join(root, "runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	install := filepath.Join(root, "game")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Games["skyrimse"] = config.GameConfig{Name: "Skyrim Special Edition", InstallPath: install, DataSubpath: "Data", SteamAppID: 489830}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	procRoot := filepath.Join(root, "proc")
	if err := os.Mkdir(procRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	return recoveryDeps{procRoot: procRoot, out: &stdout, errOut: &stderr}, install, filepath.Join(install, "Data"), &stdout, &stderr
}

// steamRecoveryFixture creates a mounted disposable farm with an outdated Steam baseline and a new loose file.
func steamRecoveryFixture(t *testing.T, known bool) (recoveryDeps, string, string, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	deps, _, _, stdout, stderr := recoverFixture(t)
	steamapps := filepath.Join(t.TempDir(), "steamapps")
	install := filepath.Join(steamapps, "common", "Skyrim Special Edition")
	dataPath := filepath.Join(install, "Data")
	if err := os.MkdirAll(dataPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataPath, "original.esm"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(steamapps, "appmanifest_489830.acf")
	body := `"AppState" { "appid" "489830" "installdir" "Skyrim Special Edition" "buildid" "123" "StateFlags" "4" "UpdateResult" "0" "LastUpdated" "100" "InstalledDepots" { "111" { "manifest" "456" } } }`
	if err := os.WriteFile(manifest, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	state, err := steam.ReadAppState(install, 489830)
	if err != nil {
		t.Fatal(err)
	}
	mm := vfs.NewMountManager(dataPath, filepath.Join(t.TempDir(), "Overwrite"), "skyrimse")
	mm.SetStorefrontBaseline(&vfs.StorefrontSnapshot{
		Store: "steam", AppID: state.AppID, BuildID: state.BuildID,
		StateFlags: state.StateFlags, UpdateResult: state.UpdateResult,
		LastUpdated: state.LastUpdated, DepotFingerprint: state.DepotFingerprint,
		CapturedAt: time.Now().UTC(),
	})
	if err := mm.Activate([]vfs.Layer{{Name: "__base__", RootPath: dataPath, Enabled: true}}, "Default"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataPath, "new.esp"), []byte("steam output"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte(strings.Replace(body, `"buildid" "123"`, `"buildid" "124"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if known {
		cfg, err := config.Load()
		if err != nil {
			t.Fatal(err)
		}
		gc := cfg.Games["skyrimse"]
		gc.InstallPath = install
		cfg.Games["skyrimse"] = gc
		if err := cfg.Save(); err != nil {
			t.Fatal(err)
		}
	}
	return deps, dataPath, manifest, stdout, stderr
}

// TestRecoverDataPathPreservesSteamChanges checks both Data-only commands retain Steam's loose output after a changed build.
func TestRecoverDataPathPreservesSteamChanges(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func([]string, recoveryDeps) int
	}{
		{"recover", runRecoverWith},
		{"recover-confirm", runRecoverConfirmWith},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, dataPath, _, stdout, stderr := steamRecoveryFixture(t, true)
			if code := tc.run([]string{"--data-path", dataPath}, deps); code != 0 {
				t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
			}
			if body, err := os.ReadFile(filepath.Join(dataPath, "original.esm")); err != nil || string(body) != "original" {
				t.Fatalf("original Data = %q, %v", body, err)
			}
			batches, err := vfs.ListPreservedBatches(dataPath)
			if err != nil || len(batches) != 1 {
				t.Fatalf("preserved Steam output = %+v, %v", batches, err)
			}
			if body, err := os.ReadFile(filepath.Join(batches[0].Path, "files", "new.esp")); err != nil || string(body) != "steam output" {
				t.Fatalf("preserved new file = %q, %v", body, err)
			}
			marker, err := vfs.ReadMaintenance(dataPath)
			if err != nil || marker == nil || marker.Reason != "verify" {
				t.Fatalf("maintenance marker = %+v, %v", marker, err)
			}
		})
	}
}

// TestRecoverDataPathWaitsForSteam checks that both Data-only commands leave the farm intact while Steam is busy.
func TestRecoverDataPathWaitsForSteam(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func([]string, recoveryDeps) int
	}{
		{"recover", runRecoverWith},
		{"recover-confirm", runRecoverConfirmWith},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, dataPath, manifest, _, stderr := steamRecoveryFixture(t, true)
			body, err := os.ReadFile(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifest, []byte(strings.Replace(string(body), `"StateFlags" "4"`, `"StateFlags" "2"`, 1)), 0o644); err != nil {
				t.Fatal(err)
			}
			if code := tc.run([]string{"--data-path", dataPath}, deps); code != 2 {
				t.Fatalf("exit = %d, want 2; stderr = %q", code, stderr.String())
			}
			if _, err := vfs.ReadSentinel(dataPath); err != nil {
				t.Fatalf("farm changed: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dataPath, "new.esp")); err != nil {
				t.Fatalf("farm output changed: %v", err)
			}
		})
	}
}

// TestRecoverDataPathUnknownGameWithBaselineRefused checks neither offline command changes an unrecognized Steam-backed farm.
func TestRecoverDataPathUnknownGameWithBaselineRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func([]string, recoveryDeps) int
	}{
		{"recover", runRecoverWith},
		{"recover-confirm", runRecoverConfirmWith},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, dataPath, _, _, stderr := steamRecoveryFixture(t, false)
			if code := tc.run([]string{"--data-path", dataPath}, deps); code != 2 {
				t.Fatalf("exit = %d, want 2; stderr = %q", code, stderr.String())
			}
			if got := stderr.String(); got != "This folder belongs to a game Gorganizer does not know. Use gorganizerctl recover --game <id>.\n" {
				t.Fatalf("refusal = %q", got)
			}
			if _, err := vfs.ReadSentinel(dataPath); err != nil {
				t.Fatalf("farm changed: %v", err)
			}
			if _, err := os.Stat(dataPath + ".orig"); err != nil {
				t.Fatalf("backup changed: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dataPath, "new.esp")); err != nil {
				t.Fatalf("farm output changed: %v", err)
			}
		})
	}
}

// TestRecoverRefusesWhileDaemonHoldsLock checks that a held instance lock leaves an interrupted farm untouched.
func TestRecoverRefusesWhileDaemonHoldsLock(t *testing.T) {
	deps, _, dataPath, _, stderr := recoverFixture(t)
	if err := os.Mkdir(dataPath, 0o755); err != nil {
		t.Fatal(err)
	}
	release, err := instancelock.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if code := runRecoverWith([]string{"--game", "skyrimse"}, deps); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if got := stderr.String(); got != "Gorganizer is running. Close it first, then run this command again.\n" {
		t.Errorf("refusal = %q", got)
	}
	if _, err := os.Stat(dataPath); err != nil {
		t.Fatalf("Data was changed: %v", err)
	}
}

// TestRecoverRefusesWhileGameRuns checks that a process in the game install prevents either recovery command from changing Data.
func TestRecoverRefusesWhileGameRuns(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(recoveryDeps, string) int
	}{
		{name: "recover game", run: func(deps recoveryDeps, _ string) int { return runRecoverWith([]string{"--game", "skyrimse"}, deps) }},
		{name: "recover-confirm", run: func(deps recoveryDeps, dataPath string) int {
			return runRecoverConfirmWith([]string{"--data-path", dataPath}, deps)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, install, dataPath, _, stderr := recoverFixture(t)
			backup := dataPath + ".orig"
			if err := os.Mkdir(backup, 0o755); err != nil {
				t.Fatal(err)
			}
			pidDir := filepath.Join(deps.procRoot, "4242")
			if err := os.Mkdir(pidDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(install, filepath.Join(pidDir, "cwd")); err != nil {
				t.Fatal(err)
			}
			if code := tc.run(deps, dataPath); code != 1 {
				t.Fatalf("exit = %d, want 1", code)
			}
			if !strings.Contains(stderr.String(), "Skyrim Special Edition is running.") || !strings.Contains(stderr.String(), "Nothing was changed.") {
				t.Errorf("refusal = %q", stderr.String())
			}
			if _, err := os.Stat(backup); err != nil {
				t.Fatalf("backup was changed: %v", err)
			}
		})
	}
}

// TestRecoverRelativeDataPathDetectsRunningGame checks a relative Data path still matches an absolute process path for both offline commands.
func TestRecoverRelativeDataPathDetectsRunningGame(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func([]string, recoveryDeps) int
	}{
		{"recover", runRecoverWith},
		{"recover-confirm", runRecoverConfirmWith},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, install, dataPath, _, stderr := recoverFixture(t)
			if err := os.Mkdir(dataPath+".orig", 0o755); err != nil {
				t.Fatal(err)
			}
			pidDir := filepath.Join(deps.procRoot, "4242")
			if err := os.Mkdir(pidDir, 0o755); err != nil {
				t.Fatal(err)
			}
			for link, target := range map[string]string{"cwd": install, "exe": filepath.Join(install, "game")} {
				if err := os.Symlink(target, filepath.Join(pidDir, link)); err != nil {
					t.Fatal(err)
				}
			}
			workingDir, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			relative, err := filepath.Rel(workingDir, dataPath)
			if err != nil || filepath.IsAbs(relative) {
				t.Fatalf("relative path = %q (%v)", relative, err)
			}
			if code := tc.run([]string{"--data-path", relative}, deps); code != 1 {
				t.Fatalf("exit = %d, want 1 (stderr = %q)", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), "Skyrim Special Edition is running.") {
				t.Fatalf("refusal = %q", stderr.String())
			}
			if _, err := os.Stat(dataPath + ".orig"); err != nil {
				t.Fatalf("backup was changed: %v", err)
			}
		})
	}
}

// TestOfflineRecoveryHonoursLaunchTicket checks recent, invalid and unreadable launch records block offline recovery without changing Data.
func TestOfflineRecoveryHonoursLaunchTicket(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ticket     string
		unreadable bool
	}{
		{name: "fresh", ticket: fmt.Sprintf(`{"schema_version":1,"launched_at":%q}`, time.Now().UTC().Format(time.RFC3339Nano))},
		{name: "invalid", ticket: `{"schema_version":0}`},
		{name: "unreadable", unreadable: true},
	} {
		for _, command := range []struct {
			name string
			run  func([]string, recoveryDeps) int
		}{
			{"recover game", runRecoverWith},
			{"recover Data", runRecoverWith},
			{"recover-confirm", runRecoverConfirmWith},
		} {
			t.Run(tc.name+"/"+command.name, func(t *testing.T) {
				deps, _, dataPath, _, stderr := recoverFixture(t)
				if err := os.Mkdir(dataPath+".orig", 0o755); err != nil {
					t.Fatal(err)
				}
				ticketPath := dataPath + vfs.RetainedSessionSiblingSuffix
				if tc.unreadable {
					if err := os.Mkdir(ticketPath, 0o700); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(ticketPath, []byte(tc.ticket), 0o600); err != nil {
					t.Fatal(err)
				}
				args := []string{"--data-path", dataPath}
				if command.name == "recover game" {
					args = []string{"--game", "skyrimse"}
				}
				if code := command.run(args, deps); code != 1 {
					t.Fatalf("exit = %d, want 1 (stderr = %q)", code, stderr.String())
				}
				if !strings.Contains(stderr.String(), "Skyrim Special Edition is running. Close the game") {
					t.Fatalf("refusal = %q", stderr.String())
				}
				for _, path := range []string{dataPath + ".orig", dataPath + vfs.RetainedSessionSiblingSuffix} {
					if _, err := os.Stat(path); err != nil {
						t.Fatalf("recovery changed %s: %v", path, err)
					}
				}
			})
		}
	}
}

// TestRecoverDataPathOnlyMessage checks that explicit Data recovery describes its limited scope.
func TestRecoverDataPathOnlyMessage(t *testing.T) {
	deps, _, dataPath, stdout, stderr := recoverFixture(t)
	if err := os.Mkdir(dataPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if code := runRecoverWith([]string{"--data-path", dataPath}, deps); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Only the Data folder was checked. Use --game to also repair root files and SMAPI.") {
		t.Errorf("output = %q", stdout.String())
	}
}

// TestRecoverGameRestoresInterruptedActivation checks that offline game recovery rolls back an activation intent.
func TestRecoverGameRestoresInterruptedActivation(t *testing.T) {
	deps, _, dataPath, stdout, stderr := recoverFixture(t)
	backup := dataPath + ".orig"
	if err := os.Mkdir(backup, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, "Skyrim.esm"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dataPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataPath, "partial"), []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	intent := &vfs.ActivationIntent{
		SchemaVersion: 1,
		Magic:         vfs.IntentMagic,
		Kind:          vfs.IntentActivating,
		GameID:        "skyrimse",
		DataPath:      dataPath,
		BackupPath:    backup,
		PID:           4242,
	}
	if err := vfs.WriteIntent(dataPath+".gorganizer-activating", intent); err != nil {
		t.Fatal(err)
	}
	if code := runRecoverWith([]string{"--game", "skyrimse"}, deps); code != 0 {
		t.Fatalf("exit = %d, stderr = %s, stdout = %s", code, stderr.String(), stdout.String())
	}
	contents, err := os.ReadFile(filepath.Join(dataPath, "Skyrim.esm"))
	if err != nil || string(contents) != "original" {
		t.Errorf("restored data = %q (%v)", contents, err)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Errorf("backup not consumed: %v", err)
	}
	if !strings.Contains(stdout.String(), "Data folder: recovered.") {
		t.Errorf("output = %q", stdout.String())
	}
}

// TestRecoverRefusesUnknownProcessState checks that a failed process scan leaves recovery untouched.
func TestRecoverRefusesUnknownProcessState(t *testing.T) {
	deps, _, dataPath, _, stderr := recoverFixture(t)
	if err := os.Mkdir(dataPath+".orig", 0o755); err != nil {
		t.Fatal(err)
	}
	deps.procRoot = filepath.Join(t.TempDir(), "missing-proc")
	if code := runRecoverWith([]string{"--game", "skyrimse"}, deps); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "Nothing was changed.") {
		t.Errorf("refusal = %q", stderr.String())
	}
	if _, err := os.Stat(dataPath + ".orig"); err != nil {
		t.Errorf("backup changed: %v", err)
	}
}

// TestRecoverLinkedGameChecksParentSteamAppID checks that a linked game recognizes the parent game's Steam launch wrapper.
func TestRecoverLinkedGameChecksParentSteamAppID(t *testing.T) {
	deps, install, _, _, stderr := recoverFixture(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Games["falloutnv"] = config.GameConfig{Name: "Fallout New Vegas", InstallPath: install, SteamAppID: 22380}
	cfg.Games["ttw"] = config.GameConfig{Name: "Tale of Two Wastelands", LinkedFromGameID: "falloutnv"}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	pidDir := filepath.Join(deps.procRoot, "9999")
	if err := os.Mkdir(pidDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pidDir, "cmdline"), []byte("/steam/reaper\x00SteamLaunch\x00AppId=22380\x00--\x00proton\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runRecoverWith([]string{"--game", "ttw"}, deps); code != 1 {
		t.Fatalf("exit = %d, want 1 (stderr = %q)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Tale of Two Wastelands is running.") {
		t.Errorf("refusal = %q", stderr.String())
	}
}

// TestRecoverConfirmRefusesWhileDaemonHoldsLock checks that confirmation also requires the shared instance lock.
func TestRecoverConfirmRefusesWhileDaemonHoldsLock(t *testing.T) {
	deps, _, dataPath, _, stderr := recoverFixture(t)
	release, err := instancelock.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if code := runRecoverConfirmWith([]string{"--data-path", dataPath}, deps); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if got := stderr.String(); got != "Gorganizer is running. Close it first, then run this command again.\n" {
		t.Errorf("refusal = %q", got)
	}
}

// TestRecoverConfirmRestoresBackup checks that explicit confirmation restores a Data backup when the game is stopped.
func TestRecoverConfirmRestoresBackup(t *testing.T) {
	deps, _, dataPath, stdout, stderr := recoverFixture(t)
	if err := os.Mkdir(dataPath+".orig", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataPath+".orig", "base.esm"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dataPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if code := runRecoverConfirmWith([]string{"--data-path", dataPath}, deps); code != 0 {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
	contents, err := os.ReadFile(filepath.Join(dataPath, "base.esm"))
	if err != nil || string(contents) != "original" {
		t.Errorf("restored data = %q (%v)", contents, err)
	}
}

// TestRecoverGameRestoresOrphanedRootDeployment checks that offline recovery restores unmanaged game-root files after a leftover deployment.
func TestRecoverGameRestoresOrphanedRootDeployment(t *testing.T) {
	deps, install, dataPath, stdout, stderr := recoverFixture(t)
	if err := os.Mkdir(dataPath, 0o755); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(install, "Hook.dll")
	if err := os.WriteFile(original, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	sourceDir := filepath.Join(t.TempDir(), "mod")
	if err := os.MkdirAll(filepath.Join(sourceDir, vfs.RootContentDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, vfs.RootContentDirName, "Hook.dll"), []byte("modded"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager, err := vfs.NewRootDeploymentManager(vfs.RootDeploymentConfig{GameRoot: install, GameID: "skyrimse", ProtectedPaths: []string{"Data"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Apply([]vfs.Layer{{Name: "mod", RootPath: sourceDir, Enabled: true}}, "Default"); err != nil {
		t.Fatal(err)
	}
	if code := runRecoverWith([]string{"--game", "skyrimse"}, deps); code != 0 {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
	contents, err := os.ReadFile(original)
	if err != nil || string(contents) != "original" {
		t.Errorf("restored root file = %q (%v)", contents, err)
	}
	if !strings.Contains(stdout.String(), "Game-root files: recovered.") {
		t.Errorf("output = %q", stdout.String())
	}
}

// TestRecoverGameRollsBackInterruptedSMAPI checks that the game command resolves a journaled loader transaction before Data recovery.
func TestRecoverGameRollsBackInterruptedSMAPI(t *testing.T) {
	deps, install, _, stdout, stderr := recoverFixture(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Games["stardewvalley"] = config.GameConfig{Name: "Stardew Valley", InstallPath: install, DataSubpath: "Mods", SteamAppID: 413150}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	opID := "11111111-2222-4333-8444-555555555555"
	intent := smapi.Intent{
		SchemaVersion: 1, OpID: opID, Op: smapi.OpInstall,
		Phase: smapi.PhaseBackingUp, WorkDir: smapi.WorkDir + "/" + opID,
	}
	contents, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(install, smapi.IntentFile), contents, 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runRecoverWith([]string{"--game", "stardewvalley"}, deps); code != 0 {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(install, smapi.IntentFile)); !os.IsNotExist(err) {
		t.Errorf("loader intent still exists: %v", err)
	}
	if !strings.Contains(stdout.String(), "SMAPI: recovered.") {
		t.Errorf("output = %q", stdout.String())
	}
}

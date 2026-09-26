package smapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/google/uuid"
)

var errInjectedENOSPC = fmt.Errorf("injected write failure: %w", syscall.ENOSPC)

type scenario struct {
	name     string
	commitAt string
	installs bool
	setup    func(t *testing.T) string
	run      func(in *Installer, game string) error
}

// testScenarios returns install, repair, update, external-loader, dropped-original, and uninstall scenarios over synthetic artifacts.
func testScenarios(t *testing.T) []scenario {
	t.Helper()
	dir := t.TempDir()
	current := writeArtifact(t, dir, "4.5.2", testPayloadFiles("4.5.2"))
	oldFiles := testPayloadFiles("4.5.1")
	oldFiles["StardewModdingAPI.Legacy.dll"] = "legacy"
	oldFiles["smapi-internal/Old.dll"] = "old"
	old := writeArtifact(t, dir, "4.5.1", oldFiles)
	withoutAppID := testPayloadFiles("4.5.3")
	delete(withoutAppID, "steam_appid.txt")
	dropped := writeArtifact(t, dir, "4.5.3", withoutAppID)
	install := func(art Artifact) func(in *Installer, game string) error {
		return func(in *Installer, game string) error {
			_, err := in.Install(context.Background(), game, art)
			return err
		}
	}
	return []scenario{
		{name: "install", commitAt: "record", installs: true, setup: newTestGame, run: install(current)},
		{name: "reinstall", commitAt: "record", installs: true, setup: func(t *testing.T) string {
			game := installed(t, current)
			writeTestFile(t, game, "smapi-internal/config.user.json", `{"DeveloperMode": true}`, 0644)
			return game
		}, run: install(current)},
		{name: "update", commitAt: "record", installs: true, setup: func(t *testing.T) string { return installed(t, old) }, run: install(current)},
		{name: "external", commitAt: "record", installs: true, setup: func(t *testing.T) string {
			game := newTestGame(t)
			externalSMAPI(t, game, old)
			moveTestPath(t, game, "Mods/ConsoleCommands", "Mods/SMAPI/ConsoleCommands")
			return game
		}, run: install(current)},
		{name: "drop-original", commitAt: "record", installs: true, setup: func(t *testing.T) string { return installed(t, current) }, run: install(dropped)},
		{name: "uninstall", commitAt: "commit", setup: func(t *testing.T) string { return installed(t, current) }, run: func(in *Installer, game string) error {
			return in.Uninstall(context.Background(), game)
		}},
	}
}

// runScenario runs sc on game with hook as the fault hook and returns the fake runner it used and the operation error.
func runScenario(sc scenario, game string, hook func(string) error) (*fakeRunner, error) {
	runner := &fakeRunner{spec: testSpec()}
	in := newTestInstaller(runner)
	in.fault = hook
	return runner, sc.run(in, game)
}

type preparedScenario struct {
	scenario
	template string
	steps    []string
	post     map[string]string
}

// prepareScenario builds the starting tree of sc once and records the steps and the end state of a successful run on a copy.
func prepareScenario(t *testing.T, sc scenario) preparedScenario {
	t.Helper()
	p := preparedScenario{scenario: sc, template: sc.setup(t)}
	assertNoScratch(t, p.template)
	if _, err := runScenario(sc, p.fresh(t), func(step string) error {
		p.steps = append(p.steps, step)
		return nil
	}); err != nil {
		t.Fatalf("collecting steps: %v", err)
	}
	game := p.fresh(t)
	if _, err := runScenario(sc, game, nil); err != nil {
		t.Fatalf("reference run: %v", err)
	}
	assertNoScratch(t, game)
	p.post = snapshotGame(t, game)
	return p
}

// fresh returns a private copy of the scenario's starting tree.
func (p preparedScenario) fresh(t *testing.T) string {
	t.Helper()
	return cloneGame(t, p.template)
}

// committedAt reports whether an interruption at step leaves the scenario committed.
func (p preparedScenario) committedAt(t *testing.T, step string) bool {
	t.Helper()
	return stepIndex(t, p.steps, step) > stepIndex(t, p.steps, p.commitAt)
}

// crashAt returns a fault hook that simulates a crash exactly at step.
func crashAt(step string) func(string) error {
	return func(current string) error {
		if current == step {
			return errInjectedFault
		}
		return nil
	}
}

// failAt returns a fault hook that fails the operation following step once with err.
func failAt(step string, err error) func(string) error {
	fired := false
	return func(current string) error {
		if current == step && !fired {
			fired = true
			return err
		}
		return nil
	}
}

// stepIndex returns the position of step in steps or fails the test.
func stepIndex(t *testing.T, steps []string, step string) int {
	t.Helper()
	for i, candidate := range steps {
		if candidate == step {
			return i
		}
	}
	t.Fatalf("step %q not in %v", step, steps)
	return -1
}

// outerCrashPoints picks the last backup step, a middle placement step, and the first committed step of the scenario.
func (p preparedScenario) outerCrashPoints(t *testing.T) []string {
	t.Helper()
	var backups, places []string
	for _, step := range p.steps {
		switch {
		case strings.HasPrefix(step, "backup:"):
			backups = append(backups, step)
		case strings.HasPrefix(step, "place:"):
			places = append(places, step)
		}
	}
	if len(backups) == 0 || len(places) == 0 {
		t.Fatalf("scenario %s has no backup or placement steps: %v", p.name, p.steps)
	}
	return []string{backups[len(backups)-1], places[len(places)/2], p.steps[stepIndex(t, p.steps, p.commitAt)+1]}
}

// assertKillerCalls requires every kill request to name the scenario's real installer child, and at least one once that child existed.
func (p preparedScenario) assertKillerCalls(t *testing.T, step string, runner *fakeRunner, killer *recordingKiller) {
	t.Helper()
	runner.mu.Lock()
	pids := append([][2]uint64{}, runner.pids...)
	runner.mu.Unlock()
	killer.mu.Lock()
	calls := append([][2]uint64{}, killer.calls...)
	killer.mu.Unlock()
	if !p.installs {
		if len(calls) != 0 {
			t.Fatalf("killer called without an installer: %v", calls)
		}
		return
	}
	for _, call := range calls {
		if len(pids) == 0 || call != pids[0] {
			t.Fatalf("killer called for %v, installer child was %v", call, pids)
		}
		if uint64(os.Getpid()) == call[0] {
			t.Fatal("killer was asked to kill the test process")
		}
	}
	if stepIndex(t, p.steps, step) >= stepIndex(t, p.steps, "run") && len(calls) == 0 {
		t.Fatalf("no kill request for installer child %v after crash at %s", pids, step)
	}
}

// TestCrashRecoveryMatrix crashes every scenario at every step and proves Recover returns the exact pre- or post-operation tree.
func TestCrashRecoveryMatrix(t *testing.T) {
	t.Parallel()
	for _, sc := range testScenarios(t) {
		p := prepareScenario(t, sc)
		for _, step := range p.steps {
			t.Run(sc.name+"/"+step, func(t *testing.T) {
				t.Parallel()
				game := p.fresh(t)
				pre := snapshotGame(t, game)
				runner, err := runScenario(sc, game, crashAt(step))
				if !errors.Is(err, errInjectedFault) {
					t.Fatalf("run error = %v", err)
				}
				killer := &recordingKiller{}
				result, err := Recover(game, killer.kill)
				if err != nil {
					t.Fatalf("Recover: %v", err)
				}
				assertNoScratch(t, game)
				p.assertKillerCalls(t, step, runner, killer)
				if p.committedAt(t, step) {
					if result.RolledBack {
						t.Fatalf("Recover rolled back a committed operation: %+v", result)
					}
					assertSnapshot(t, "recovered commit", p.post, snapshotGame(t, game))
					return
				}
				if result.Committed {
					t.Fatalf("Recover committed an uncommitted operation: %+v", result)
				}
				assertSnapshot(t, "rolled back", pre, snapshotGame(t, game))
			})
		}
	}
}

// TestRealErrorRollbackMatrix fails every scenario with ENOSPC at every step and proves the in-process rollback leaves the exact pre-operation tree and no intent.
func TestRealErrorRollbackMatrix(t *testing.T) {
	t.Parallel()
	for _, sc := range testScenarios(t) {
		p := prepareScenario(t, sc)
		for _, step := range p.steps {
			t.Run(sc.name+"/"+step, func(t *testing.T) {
				t.Parallel()
				game := p.fresh(t)
				pre := snapshotGame(t, game)
				_, err := runScenario(sc, game, failAt(step, errInjectedENOSPC))
				if isFault(err) {
					t.Fatalf("real error was treated as a crash: %v", err)
				}
				if p.committedAt(t, step) {
					if step == "cleanup" {
						if err != nil {
							t.Fatalf("a cleanup failure failed a committed operation: %v", err)
						}
					} else if !errors.Is(err, ErrInterrupted) || !errors.Is(err, syscall.ENOSPC) {
						t.Fatalf("post-commit error = %v, want ENOSPC and ErrInterrupted", err)
					}
					if _, err := Recover(game, (&recordingKiller{}).kill); err != nil {
						t.Fatalf("Recover: %v", err)
					}
					assertNoScratch(t, game)
					assertSnapshot(t, "finished commit", p.post, snapshotGame(t, game))
					return
				}
				if !errors.Is(err, syscall.ENOSPC) || errors.Is(err, ErrInterrupted) {
					t.Fatalf("run error = %v, want a rolled-back ENOSPC", err)
				}
				assertNoScratch(t, game)
				assertSnapshot(t, "in-process rollback", pre, snapshotGame(t, game))
				result, err := Recover(game, (&recordingKiller{}).kill)
				if err != nil || result.RolledBack || result.Committed {
					t.Fatalf("Recover after in-process rollback = %+v, %v", result, err)
				}
				assertSnapshot(t, "after recover", pre, snapshotGame(t, game))
			})
		}
	}
}

// TestCrashDuringRecovery crashes or fails Recover itself at every recovery step and proves a second Recover still reaches the exact expected tree.
func TestCrashDuringRecovery(t *testing.T) {
	t.Parallel()
	for _, sc := range testScenarios(t) {
		p := prepareScenario(t, sc)
		for _, outer := range p.outerCrashPoints(t) {
			var inner []string
			game := p.fresh(t)
			if _, err := runScenario(sc, game, crashAt(outer)); !errors.Is(err, errInjectedFault) {
				t.Fatalf("crash at %s = %v", outer, err)
			}
			if _, err := recoverGame(game, (&recordingKiller{}).kill, func(step string) error {
				inner = append(inner, step)
				return nil
			}); err != nil {
				t.Fatalf("collecting recovery steps after %s: %v", outer, err)
			}
			if len(inner) == 0 {
				t.Fatalf("recovery after %s emitted no steps", outer)
			}
			for _, step := range inner {
				for _, mode := range []string{"crash", "error"} {
					t.Run(sc.name+"/"+outer+"/"+step+"/"+mode, func(t *testing.T) {
						t.Parallel()
						game := p.fresh(t)
						pre := snapshotGame(t, game)
						if _, err := runScenario(sc, game, crashAt(outer)); !errors.Is(err, errInjectedFault) {
							t.Fatalf("crash at %s = %v", outer, err)
						}
						hook := crashAt(step)
						if mode == "error" {
							hook = failAt(step, errInjectedENOSPC)
						}
						_, err := recoverGame(game, (&recordingKiller{}).kill, hook)
						if mode == "crash" && !isFault(err) {
							t.Fatalf("recovery crash at %s = %v", step, err)
						}
						if _, err := Recover(game, (&recordingKiller{}).kill); err != nil {
							t.Fatalf("second Recover: %v", err)
						}
						assertNoScratch(t, game)
						if p.committedAt(t, outer) {
							assertSnapshot(t, "recovered commit", p.post, snapshotGame(t, game))
						} else {
							assertSnapshot(t, "rolled back", pre, snapshotGame(t, game))
						}
					})
				}
			}
		}
	}
}

// TestCrashDuringInProcessRollback fails every scenario mid-placement, crashes its in-process rollback at each rollback step, and proves Recover completes it.
func TestCrashDuringInProcessRollback(t *testing.T) {
	t.Parallel()
	for _, sc := range testScenarios(t) {
		p := prepareScenario(t, sc)
		outer := p.outerCrashPoints(t)[1]
		var rollbackSteps []string
		failed := false
		if _, err := runScenario(sc, p.fresh(t), func(step string) error {
			if failed {
				rollbackSteps = append(rollbackSteps, step)
			}
			if step == outer && !failed {
				failed = true
				return errInjectedENOSPC
			}
			return nil
		}); !errors.Is(err, syscall.ENOSPC) {
			t.Fatalf("failing at %s = %v", outer, err)
		}
		if !contains(rollbackSteps, "rolled-back") {
			t.Fatalf("in-process rollback steps = %v", rollbackSteps)
		}
		for _, step := range rollbackSteps {
			t.Run(sc.name+"/"+step, func(t *testing.T) {
				t.Parallel()
				game := p.fresh(t)
				pre := snapshotGame(t, game)
				failed := false
				_, err := runScenario(sc, game, func(current string) error {
					if current == outer && !failed {
						failed = true
						return errInjectedENOSPC
					}
					if failed && current == step {
						return errInjectedFault
					}
					return nil
				})
				if !isFault(err) {
					t.Fatalf("run error = %v", err)
				}
				if _, err := Recover(game, (&recordingKiller{}).kill); err != nil {
					t.Fatalf("Recover: %v", err)
				}
				assertNoScratch(t, game)
				assertSnapshot(t, "rolled back", pre, snapshotGame(t, game))
			})
		}
	}
}

// TestRealErrnosRollBackAfterLauncherBackup injects each rename errno after the launcher was backed up and proves the tree is restored byte-identical.
func TestRealErrnosRollBackAfterLauncherBackup(t *testing.T) {
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	for _, errno := range []syscall.Errno{syscall.EEXIST, syscall.EXDEV, syscall.EINVAL, syscall.ENOSPC, syscall.EACCES} {
		t.Run(errno.Error(), func(t *testing.T) {
			game := newTestGame(t)
			pre := snapshotGame(t, game)
			in := newTestInstaller(&fakeRunner{spec: testSpec()})
			in.fault = failAt("place:StardewModdingAPI", &os.LinkError{Op: "rename", Old: "stage", New: "game", Err: errno})
			_, err := in.Install(context.Background(), game, art)
			if !errors.Is(err, errno) || errors.Is(err, ErrInterrupted) {
				t.Fatalf("Install error = %v", err)
			}
			assertNoScratch(t, game)
			assertSnapshot(t, "after "+errno.Error(), pre, snapshotGame(t, game))
		})
	}
}

// TestBackupCollisionRollsBack plants a file where the steam_appid.txt backup goes so the real rename fails with EEXIST after the launcher moved.
func TestBackupCollisionRollsBack(t *testing.T) {
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	game := newTestGame(t)
	pre := snapshotGame(t, game)
	in := newTestInstaller(&fakeRunner{spec: testSpec()})
	in.fault = func(step string) error {
		if step != "backup:steam_appid.txt" {
			return nil
		}
		intent, err := readIntent(game)
		if err != nil || intent == nil {
			return fmt.Errorf("reading intent: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(game, BackupDir, intent.OpID, "StardewValley")); err != nil {
			return fmt.Errorf("launcher was not backed up first: %v", err)
		}
		writeTestFile(t, game, BackupDir+"/"+intent.OpID+"/steam_appid.txt", "planted", 0644)
		return nil
	}
	_, err := in.Install(context.Background(), game, art)
	if !errors.Is(err, syscall.EEXIST) || errors.Is(err, ErrInterrupted) {
		t.Fatalf("Install error = %v", err)
	}
	assertNoScratch(t, game)
	assertSnapshot(t, "after EEXIST", pre, snapshotGame(t, game))
}

// TestUnwritableModsRollsBack makes Mods read-only right before the first placement so the real rename fails with EACCES after the launcher moved.
func TestUnwritableModsRollsBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	game := newTestGame(t)
	mods := filepath.Join(game, "Mods")
	if err := os.Mkdir(mods, 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(mods, 0755) })
	pre := snapshotGame(t, game)
	in := newTestInstaller(&fakeRunner{spec: testSpec()})
	in.fault = func(step string) error {
		if step == "place:Mods/ConsoleCommands" {
			return os.Chmod(mods, 0555)
		}
		return nil
	}
	_, err := in.Install(context.Background(), game, art)
	if !errors.Is(err, syscall.EACCES) || errors.Is(err, ErrInterrupted) {
		t.Fatalf("Install error = %v", err)
	}
	if err := os.Chmod(mods, 0755); err != nil {
		t.Fatal(err)
	}
	assertNoScratch(t, game)
	assertSnapshot(t, "after EACCES", pre, snapshotGame(t, game))
}

// TestFailedRollbackKeepsIntent makes the game directory read-only mid-placement so the in-process rollback fails, then proves Recover finishes it.
func TestFailedRollbackKeepsIntent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	game := newTestGame(t)
	t.Cleanup(func() { _ = os.Chmod(game, 0755) })
	pre := snapshotGame(t, game)
	in := newTestInstaller(&fakeRunner{spec: testSpec()})
	in.fault = func(step string) error {
		if step == "place:StardewModdingAPI" {
			if err := os.Chmod(game, 0555); err != nil {
				return err
			}
			return errInjectedENOSPC
		}
		return nil
	}
	_, err := in.Install(context.Background(), game, art)
	if !errors.Is(err, syscall.ENOSPC) || !errors.Is(err, ErrInterrupted) {
		t.Fatalf("Install error = %v, want ENOSPC with a pending recovery", err)
	}
	if err := os.Chmod(game, 0755); err != nil {
		t.Fatal(err)
	}
	status, err := Inspect(game, testSpec())
	if err != nil || status.State != StateInterrupted {
		t.Fatalf("Inspect = %+v, %v", status, err)
	}
	result, err := Recover(game, (&recordingKiller{}).kill)
	if err != nil || !result.RolledBack {
		t.Fatalf("Recover = %+v, %v", result, err)
	}
	assertNoScratch(t, game)
	assertSnapshot(t, "recovered", pre, snapshotGame(t, game))
}

// TestAncestorSwapIsRefused replaces Mods with a symlink between preflight and placement and proves nothing is written through it.
func TestAncestorSwapIsRefused(t *testing.T) {
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	game := newTestGame(t)
	mods := filepath.Join(game, "Mods")
	if err := os.Mkdir(mods, 0755); err != nil {
		t.Fatal(err)
	}
	pre := snapshotGame(t, game)
	elsewhere := t.TempDir()
	in := newTestInstaller(&fakeRunner{spec: testSpec()})
	in.fault = func(step string) error {
		if step != "place:Mods/ConsoleCommands" {
			return nil
		}
		if err := os.Remove(mods); err != nil {
			return err
		}
		return os.Symlink(elsewhere, mods)
	}
	_, err := in.Install(context.Background(), game, art)
	if !errors.Is(err, ErrUnsafeTarget) || !errors.Is(err, ErrInterrupted) {
		t.Fatalf("Install error = %v", err)
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Fatalf("wrote through the swapped ancestor: %v", entries)
	}
	if err := os.Remove(mods); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(mods, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Recover(game, (&recordingKiller{}).kill); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	assertNoScratch(t, game)
	assertSnapshot(t, "recovered", pre, snapshotGame(t, game))
}

// crashedInstall crashes a fresh install of the synthetic payload at step and returns the game and its intent.
func crashedInstall(t *testing.T, step string) (string, *Intent) {
	t.Helper()
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	game := newTestGame(t)
	in := newTestInstaller(&fakeRunner{spec: testSpec()})
	in.fault = crashAt(step)
	if _, err := in.Install(context.Background(), game, art); !errors.Is(err, errInjectedFault) {
		t.Fatalf("Install error = %v", err)
	}
	intent, err := readIntent(game)
	if err != nil || intent == nil {
		t.Fatalf("readIntent = %+v, %v", intent, err)
	}
	return game, intent
}

// TestRecoverIsIdempotentAfterPartialRollback verifies re-running recovery after a partially restored placement keeps restored files.
func TestRecoverIsIdempotentAfterPartialRollback(t *testing.T) {
	game := newTestGame(t)
	pre := snapshotGame(t, game)
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	in := newTestInstaller(&fakeRunner{spec: testSpec()})
	in.fault = crashAt("commit")
	if _, err := in.Install(context.Background(), game, art); !errors.Is(err, errInjectedFault) {
		t.Fatalf("Install error = %v", err)
	}
	intent, err := readIntent(game)
	if err != nil || intent == nil {
		t.Fatalf("readIntent = %+v, %v", intent, err)
	}
	target := filepath.Join(game, "steam_appid.txt")
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(game, BackupDir, intent.OpID, "steam_appid.txt"), target); err != nil {
		t.Fatal(err)
	}
	if _, err := Recover(game, (&recordingKiller{}).kill); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	assertSnapshot(t, "recovered", pre, snapshotGame(t, game))
	assertNoScratch(t, game)
}

// TestRollbackRefusesForeignEntries verifies rollback never deletes an entry the transaction did not place and keeps the intent.
func TestRollbackRefusesForeignEntries(t *testing.T) {
	game, _ := crashedInstall(t, "place")
	writeTestFile(t, game, "StardewModdingAPI", "user file", 0644)
	if _, err := Recover(game, (&recordingKiller{}).kill); err == nil || !strings.Contains(err.Error(), "did not place") {
		t.Fatalf("Recover error = %v", err)
	}
	if got := readGameFile(t, game, "StardewModdingAPI"); got != "user file" {
		t.Fatalf("foreign file = %q", got)
	}
	if _, err := os.Lstat(filepath.Join(game, IntentFile)); err != nil {
		t.Fatalf("intent removed after refused rollback: %v", err)
	}
	if err := os.Remove(filepath.Join(game, "StardewModdingAPI")); err != nil {
		t.Fatal(err)
	}
	if _, err := Recover(game, (&recordingKiller{}).kill); err != nil {
		t.Fatalf("Recover after removing the foreign file: %v", err)
	}
	assertNoScratch(t, game)
}

// TestRollbackRefusesMissingBackup verifies a placed launcher whose preimage vanished from the backup is surfaced instead of reported as restored.
func TestRollbackRefusesMissingBackup(t *testing.T) {
	game, intent := crashedInstall(t, "place:StardewValley-original")
	if got := readGameFile(t, game, "StardewValley"); got != testSMAPILauncher {
		t.Fatalf("launcher before recovery = %q", got)
	}
	if err := os.Remove(filepath.Join(game, BackupDir, intent.OpID, "StardewValley")); err != nil {
		t.Fatal(err)
	}
	if _, err := Recover(game, (&recordingKiller{}).kill); err == nil || !strings.Contains(err.Error(), "preimage is missing") {
		t.Fatalf("Recover error = %v", err)
	}
	if got := readGameFile(t, game, "StardewValley"); got != testSMAPILauncher {
		t.Fatalf("launcher after refused recovery = %q", got)
	}
	status, err := Inspect(game, testSpec())
	if err != nil || status.State != StateInterrupted {
		t.Fatalf("Inspect = %+v, %v", status, err)
	}
}

// TestRecoverRefusesAmbiguousState verifies a removed target that reappeared next to its backup is surfaced instead of overwritten.
func TestRecoverRefusesAmbiguousState(t *testing.T) {
	game := installed(t, writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2")))
	in := newTestInstaller(&fakeRunner{spec: testSpec()})
	in.fault = crashAt("place")
	if err := in.Uninstall(context.Background(), game); !errors.Is(err, errInjectedFault) {
		t.Fatalf("Uninstall error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(game, "smapi-internal"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Recover(game, (&recordingKiller{}).kill); err == nil {
		t.Fatal("Recover succeeded on an ambiguous state")
	}
	if _, err := os.Stat(filepath.Join(game, IntentFile)); err != nil {
		t.Fatalf("intent removed after failed recovery: %v", err)
	}
	status, err := Inspect(game, testSpec())
	if err != nil || status.State != StateInterrupted {
		t.Fatalf("Inspect = %+v, %v", status, err)
	}
}

// TestRecoverRefusesSymlinkedBackupDir verifies recovery never follows a backup directory swapped for a symlink.
func TestRecoverRefusesSymlinkedBackupDir(t *testing.T) {
	game, intent := crashedInstall(t, "place:smapi-internal")
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Rename(filepath.Join(game, BackupDir), elsewhere); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(game, BackupDir)); err != nil {
		t.Fatal(err)
	}
	outside := hashTreeForTest(t, elsewhere)
	_, err := Recover(game, (&recordingKiller{}).kill)
	var unsafe *UnsafeTargetError
	if !errors.As(err, &unsafe) || unsafe.Path != BackupDir {
		t.Fatalf("Recover error = %v", err)
	}
	if diff := diffSnapshots(outside, hashTreeForTest(t, elsewhere)); diff != "" {
		t.Fatalf("recovery touched the symlink target:\n%s", diff)
	}
	if _, err := readIntent(game); err != nil {
		t.Fatalf("intent damaged: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(elsewhere, intent.OpID, "StardewValley")); err != nil {
		t.Fatalf("backup outside the game was modified: %v", err)
	}
}

// TestRecoverRefusesLiveTransaction verifies Recover refuses while another live process owns the intent or the game lock is held.
func TestRecoverRefusesLiveTransaction(t *testing.T) {
	game, intent := crashedInstall(t, "place:smapi-internal")
	child := exec.Command("/bin/sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	start, err := ProcessStartTime(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	owned := *intent
	owned.OwnerPID, owned.OwnerStart = child.Process.Pid, start
	if err := writeIntent(game, &owned); err != nil {
		t.Fatal(err)
	}
	if _, err := Recover(game, (&recordingKiller{}).kill); !errors.Is(err, ErrTransactionActive) {
		t.Fatalf("Recover with a live owner = %v", err)
	}
	if err := writeIntent(game, intent); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockGame(game)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Recover(game, (&recordingKiller{}).kill); !errors.Is(err, ErrTransactionActive) {
		t.Fatalf("Recover while locked = %v", err)
	}
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	if _, err := newTestInstaller(&fakeRunner{spec: testSpec()}).Install(context.Background(), game, art); !errors.Is(err, ErrTransactionActive) {
		t.Fatalf("Install while locked = %v", err)
	}
	unlock()
	result, err := Recover(game, (&recordingKiller{}).kill)
	if err != nil || !result.RolledBack {
		t.Fatalf("Recover after unlock = %+v, %v", result, err)
	}
	assertNoScratch(t, game)
}

// TestRecoverSweepsLeftovers verifies orphaned backups, atomic temporaries, and read-only trees are swept without failing recovery.
func TestRecoverSweepsLeftovers(t *testing.T) {
	game := installed(t, writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2")))
	before := snapshotGame(t, game)
	orphanID := uuid.NewString()
	writeTestFile(t, game, BackupDir+"/"+orphanID+"/smapi-internal/locked/file", "old", 0644)
	if err := os.Chmod(filepath.Join(game, BackupDir, orphanID, "smapi-internal", "locked"), 0555); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, game, ".tmp-"+IntentFile+"-123456", "{", 0644)
	writeTestFile(t, game, ".tmp-"+RecordFile+"-99", "{", 0644)
	writeTestFile(t, game, OriginalsDir+"/.tmp-steam_appid.txt-4242", "48", 0644)
	writeTestFile(t, game, ".tmp-unrelated-1", "user", 0644)
	result, err := Recover(game, (&recordingKiller{}).kill)
	if err != nil || result.SweptBackups != 1 || len(result.Warnings) != 0 {
		t.Fatalf("Recover = %+v, %v", result, err)
	}
	assertNoScratch(t, game)
	after := snapshotGame(t, game)
	if after[".tmp-unrelated-1"] == "" {
		t.Fatal("an unrelated temporary-looking file was removed")
	}
	delete(after, ".tmp-unrelated-1")
	assertSnapshot(t, "after sweep", before, after)
}

// TestCommittedCleanupFailureDoesNotFailInstall verifies a read-only directory in the replaced tree is still cleaned up after the commit.
func TestCommittedCleanupFailureDoesNotFailInstall(t *testing.T) {
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	game := installed(t, art)
	locked := filepath.Join(game, "smapi-internal", "i18n")
	if err := os.Chmod(locked, 0555); err != nil {
		t.Fatal(err)
	}
	if _, err := newTestInstaller(&fakeRunner{spec: testSpec()}).Install(context.Background(), game, art); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	assertNoScratch(t, game)
	status, err := Inspect(game, testSpec())
	if err != nil || status.State != StateOK || !status.Managed {
		t.Fatalf("Inspect = %+v, %v", status, err)
	}
}

// TestRecoverSweepsWorkDirs verifies abandoned work directories are swept after their recorded process is stopped.
func TestRecoverSweepsWorkDirs(t *testing.T) {
	game := newTestGame(t)
	work := filepath.Join(game, WorkDir, uuid.NewString())
	if err := os.MkdirAll(filepath.Join(work, "stage", "game"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeOwner(work, ownerInfo{PID: 4242, Start: 99}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(game, BackupDir), 0755); err != nil {
		t.Fatal(err)
	}
	killer := &recordingKiller{}
	result, err := Recover(game, killer.kill)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if result.SweptWorkDirs != 1 || result.RolledBack || result.Committed {
		t.Fatalf("result = %+v", result)
	}
	if !reflect.DeepEqual(killer.calls, [][2]uint64{{4242, 99}}) {
		t.Fatalf("killer calls = %v", killer.calls)
	}
	assertNoScratch(t, game)
	failing := func(pid int, start uint64) error { return errors.New("still alive") }
	work = filepath.Join(game, WorkDir, uuid.NewString())
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeOwner(work, ownerInfo{PID: 4242, Start: 99}); err != nil {
		t.Fatal(err)
	}
	if _, err := Recover(game, failing); err == nil {
		t.Fatal("Recover succeeded although the installer could not be stopped")
	}
	if _, err := os.Stat(work); err != nil {
		t.Fatalf("work directory of a live installer was removed: %v", err)
	}
}

// TestApplyPreflightRefusals verifies malformed plans are refused before any change.
func TestApplyPreflightRefusals(t *testing.T) {
	game := newTestGame(t)
	before := snapshotGame(t, game)
	opID := uuid.NewString()
	protected := testSpec().protectedNames()
	cases := []struct {
		name string
		plan TxnPlan
		want string
	}{
		{"bad op id", TxnPlan{OpID: "x", Op: OpInstall}, "invalid operation id"},
		{"unknown op", TxnPlan{OpID: opID, Op: "reinstall"}, "unknown mod-loader operation"},
		{"escaping target", TxnPlan{OpID: opID, Op: OpUninstall, Remove: []string{"../outside"}}, "unsafe mod-loader target"},
		{"reserved target", TxnPlan{OpID: opID, Op: OpUninstall, Remove: []string{RecordFile}}, "unsafe mod-loader target"},
		{"game content", TxnPlan{OpID: opID, Op: OpUninstall, Remove: []string{"Content"}, Protected: protected}, "game-owned"},
		{"whole Mods folder", TxnPlan{OpID: opID, Op: OpUninstall, Remove: []string{"Mods"}}, "game-owned"},
		{"overlapping targets", TxnPlan{OpID: opID, Op: OpUninstall, Remove: []string{"Content", "Content/Data"}}, "overlap"},
		{"foreign work dir", TxnPlan{OpID: opID, Op: OpUninstall, WorkDir: filepath.Join(game, "elsewhere")}, "does not belong"},
		{"missing stage", TxnPlan{OpID: opID, Op: OpInstall, Place: []string{"x"}}, "stage"},
		{"changed input", TxnPlan{OpID: opID, Op: OpUninstall, Expect: map[string]string{"steam_appid.txt": strings.Repeat("0", 64)}}, "changed"},
		{"no farm guard", TxnPlan{OpID: opID, Op: OpUninstall, Farm: FarmGuard{}}, "no farm guard"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name != "no farm guard" {
				tc.plan.Farm = testSpec().Farm
			}
			if _, err := Apply(game, tc.plan); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Apply error = %v, want %q", err, tc.want)
			}
			assertSnapshot(t, tc.name, before, snapshotGame(t, game))
			assertNoScratch(t, game)
		})
	}
	if _, err := Apply("relative/game", TxnPlan{OpID: opID, Op: OpUninstall}); err == nil {
		t.Fatal("Apply accepted a relative game directory")
	}
}

// TestSameMountAndRenameProbe verifies the rename probe passes on the test filesystem and a path on another mount is refused.
func TestSameMountAndRenameProbe(t *testing.T) {
	dir := t.TempDir()
	if err := probeRenameNoReplace(dir); err != nil {
		t.Fatalf("probeRenameNoReplace: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("probe left %v", entries)
	}
	mount, err := mountOf(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range []string{"/proc", "/dev/shm", "/sys"} {
		otherMount, err := mountOf(other)
		if err != nil || otherMount == mount {
			continue
		}
		if err := requireMount(other, mount); !errors.Is(err, ErrCrossDevice) {
			t.Fatalf("requireMount(%s) = %v", other, err)
		}
		return
	}
	t.Skip("no second mount available")
}

// TestIntentFormat verifies the journal is snake_case JSON and rejects foreign work directories on read.
func TestIntentFormat(t *testing.T) {
	game := newTestGame(t)
	intent := Intent{SchemaVersion: intentSchema, OpID: uuid.NewString(), Op: OpInstall, Phase: PhasePlacing, Place: []string{"a"}, Remove: []string{}, Existing: map[string]FileID{"a": {Dev: 1, Ino: 2}}, Placed: map[string]FileID{"a": {Dev: 1, Ino: 3}}}
	intent.WorkDir = WorkDir + "/" + intent.OpID
	if err := writeIntent(game, &intent); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(readGameFile(t, game, IntentFile)), &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"schema_version", "op_id", "op", "phase", "work_dir", "owner_pid", "owner_start", "place", "remove", "existing", "placed", "created_at"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("intent JSON lacks %q: %v", key, raw)
		}
	}
	if _, err := readIntent(game); err != nil {
		t.Fatalf("readIntent: %v", err)
	}
	intent.WorkDir = "../../elsewhere"
	if err := writeIntent(game, &intent); err != nil {
		t.Fatal(err)
	}
	if _, err := readIntent(game); err == nil {
		t.Fatal("readIntent accepted a foreign work directory")
	}
	if _, err := Recover(game, (&recordingKiller{}).kill); err == nil {
		t.Fatal("Recover accepted a foreign work directory")
	}
}

// TestReadRecord verifies missing, valid, and malformed records.
func TestReadRecord(t *testing.T) {
	game := newTestGame(t)
	record, err := ReadRecord(game)
	if record != nil || err != nil {
		t.Fatalf("ReadRecord on fresh game = %+v, %v", record, err)
	}
	digest := strings.Repeat("0", 64)
	valid := &Record{
		SchemaVersion: recordSchema, Loader: recordLoader, OpID: uuid.NewString(), Version: "4.5.2",
		Targets:   []string{"StardewModdingAPI", "steam_appid.txt"},
		Files:     []RecordEntry{{Path: "StardewModdingAPI", SHA256: digest, Size: 1, Mode: 0755}, {Path: "steam_appid.txt", SHA256: digest, Size: 6, Mode: 0644}},
		Originals: []OriginalFile{{Path: "steam_appid.txt", SHA256: digest, Size: 3, Mode: 0644}},
	}
	if err := writeRecord(game, valid); err != nil {
		t.Fatal(err)
	}
	record, err = ReadRecord(game)
	if err != nil || !reflect.DeepEqual(record.Files, valid.Files) || !reflect.DeepEqual(record.Originals, valid.Originals) {
		t.Fatalf("ReadRecord = %+v, %v", record, err)
	}
	for name, mutate := range map[string]func(r *Record){
		"schema":            func(r *Record) { r.SchemaVersion = 2 },
		"loader":            func(r *Record) { r.Loader = "bepinex" },
		"path":              func(r *Record) { r.Files = []RecordEntry{{Path: "../escape", SHA256: digest}} },
		"uncovered file":    func(r *Record) { r.Targets = []string{"StardewModdingAPI"} },
		"empty target":      func(r *Record) { r.Targets = append(r.Targets, "smapi-internal") },
		"whole Mods":        func(r *Record) { r.Targets = append(r.Targets, "Mods") },
		"bad digest":        func(r *Record) { r.Files[0].SHA256 = "x" },
		"untargeted orig":   func(r *Record) { r.Originals = []OriginalFile{{Path: "other.txt", SHA256: digest}} },
		"overlapping":       func(r *Record) { r.Targets = append(r.Targets, "StardewModdingAPI/x") },
		"orig bad mode":     func(r *Record) { r.Originals[0].Mode = 0100644 },
		"duplicate originl": func(r *Record) { r.Originals = append(r.Originals, r.Originals[0]) },
	} {
		broken := *valid
		broken.Targets = append([]string{}, valid.Targets...)
		broken.Files = append([]RecordEntry{}, valid.Files...)
		broken.Originals = append([]OriginalFile{}, valid.Originals...)
		mutate(&broken)
		if err := writeRecord(game, &broken); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadRecord(game); err == nil {
			t.Fatalf("ReadRecord accepted a record with a bad %s", name)
		}
	}
	claimsContent := *valid
	claimsContent.Targets = []string{"Content"}
	claimsContent.Files = []RecordEntry{{Path: "Content/Data/Fish.xnb", SHA256: digest, Size: 4, Mode: 0644}}
	claimsContent.Originals = nil
	if err := claimsContent.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if err := claimsContent.checkOwnership(testSpec()); !errors.Is(err, ErrUnsafeTarget) {
		t.Fatalf("checkOwnership of a record claiming Content = %v", err)
	}
}

package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/mod"
	"github.com/parka/gorganizer/internal/smapi"
	"github.com/parka/gorganizer/internal/vfs"
)

// newUnrecoveredDaemon builds an isolated daemon whose startup recovery has not run yet.
func newUnrecoveredDaemon(t *testing.T, games map[string]config.GameConfig, prepare func(*Daemon)) *Daemon {
	t.Helper()
	isolateDaemonState(t)
	cfg := config.DefaultConfig()
	for id, gc := range games {
		cfg.Games[id] = gc
	}
	d, err := New(cfg)
	if err != nil {
		t.Fatalf("New daemon: %v", err)
	}
	t.Cleanup(d.Shutdown)
	if prepare != nil {
		prepare(d)
	}
	return d
}

// newSkyrimGames returns a Skyrim-only configuration with a Data folder under a fresh install.
func newSkyrimGames(t *testing.T) map[string]config.GameConfig {
	t.Helper()
	install := filepath.Join(t.TempDir(), "Skyrim Special Edition")
	writeFixture(t, filepath.Join(install, "SkyrimSE.exe"))
	writeFixture(t, filepath.Join(install, "Data", "Skyrim.esm"))
	return map[string]config.GameConfig{
		"skyrimse": {Name: "Skyrim Special Edition", InstallPath: install, DataSubpath: "Data", SteamAppID: 489830},
	}
}

// agedDir creates dir with an mtime of age ago.
func agedDir(t *testing.T, dir string, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-age)
	if err := os.Chtimes(dir, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

// TestStageSweepNeverTouchesAnInstallOfThisDaemon locks that the startup sweep reaps only staging folders older than the daemon.
func TestStageSweepNeverTouchesAnInstallOfThisDaemon(t *testing.T) {
	d := newIsolatedDaemon(t, newSkyrimGames(t))
	modsDir, profilesDir := config.ModsDir("skyrimse"), config.ProfilesDir("skyrimse")
	orphanStage := filepath.Join(modsDir, ".stage-orphan")
	orphanImport := filepath.Join(profilesDir, mod.ImportStagePrefix+"orphan")
	agedDir(t, orphanStage, time.Hour)
	agedDir(t, orphanImport, time.Hour)
	liveStage, err := os.MkdirTemp(modsDir, ".stage-")
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(liveStage, "textures", "a.dds"))

	d.sweepOrphanStageDirs("skyrimse")
	for _, orphan := range []string{orphanStage, orphanImport} {
		if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("orphan %s survived the sweep: %v", filepath.Base(orphan), err)
		}
	}
	if _, err := os.Stat(filepath.Join(liveStage, "textures", "a.dds")); err != nil {
		t.Fatalf("the sweep touched the stage of an install this daemon runs: %v", err)
	}
}

// TestRecoverAllSweepsOrphanStagingBeforeRecoveryIsReady locks that the orphan sweep runs inside startup recovery.
func TestRecoverAllSweepsOrphanStagingBeforeRecoveryIsReady(t *testing.T) {
	var orphan string
	d := newUnrecoveredDaemon(t, newSkyrimGames(t), func(*Daemon) {
		orphan = filepath.Join(config.ModsDir("skyrimse"), ".stage-orphan")
		agedDir(t, orphan, time.Hour)
	})
	d.RecoverAll()
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("startup recovery left the orphan stage: %v", err)
	}
}

// TestTrashSweptAtStartup verifies recovery removes uninstall trash for every configured game without following symlinks or touching other entries.
func TestTrashSweptAtStartup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	games := newSkyrimGames(t)
	otherInstall := filepath.Join(t.TempDir(), "Fallout4")
	writeFixture(t, filepath.Join(otherInstall, "Data", "Fallout4.esm"))
	games["fallout4"] = config.GameConfig{Name: "Fallout 4", InstallPath: otherInstall, DataSubpath: "Data", SteamAppID: 377160}
	var trashDirs []string
	var preserved []string
	d := newUnrecoveredDaemon(t, games, func(*Daemon) {
		outside := filepath.Join(t.TempDir(), "outside")
		writeFixture(t, filepath.Join(outside, "keep.txt"))
		for _, gameID := range []string{"skyrimse", "fallout4"} {
			modsDir := config.ModsDir(gameID)
			trash := filepath.Join(modsDir, ".gorganizer-trash-crashed")
			writeFixture(t, filepath.Join(trash, "mod.esp"))
			trashDirs = append(trashDirs, trash)
			link := filepath.Join(modsDir, ".gorganizer-trash-link")
			if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
			regular := filepath.Join(modsDir, ".gorganizer-trash-file")
			writeFixture(t, regular)
			nested := filepath.Join(modsDir, "Ordinary", ".gorganizer-trash-nested", "keep.txt")
			writeFixture(t, nested)
			preserved = append(preserved, link, regular, nested)
		}
		preserved = append(preserved, filepath.Join(outside, "keep.txt"))
	})
	d.RecoverAll()
	for _, dir := range trashDirs {
		if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("startup left trash %s: %v", dir, err)
		}
	}
	for _, path := range preserved {
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("startup removed non-trash path %s: %v", path, err)
		}
	}
}

// TestInstallsWaitForStartupRecovery locks that an install requested before startup recovery finished starts only afterwards.
func TestInstallsWaitForStartupRecovery(t *testing.T) {
	d := newUnrecoveredDaemon(t, newSkyrimGames(t), nil)
	writeZipFiles(t, filepath.Join(config.DownloadsDir("skyrimse"), "Early.zip"), map[string]string{"textures/early.dds": "dds"})

	done := make(chan error, 1)
	go func() {
		_, _, err := d.StartInstall(dto.StartInstallRequest{GameID: "skyrimse", ArchiveRelPath: "Early.zip", Mode: dto.InstallAsNewMod, TargetMod: "Early"})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("StartInstall finished before startup recovery: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if _, err := os.Stat(filepath.Join(config.ModsDir("skyrimse"), "Early")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the install wrote its mod before startup recovery: %v", err)
	}
	d.RecoverAll()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StartInstall after recovery: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("StartInstall never resumed after startup recovery")
	}
	if _, err := os.Stat(filepath.Join(config.ModsDir("skyrimse"), "Early", "textures", "early.dds")); err != nil {
		t.Fatalf("installed file missing: %v", err)
	}
}

// TestConfigureGameWaitsForStartupRecovery locks that ConfigureGame never races the unfenced startup loader recovery.
func TestConfigureGameWaitsForStartupRecovery(t *testing.T) {
	d := newUnrecoveredDaemon(t, nil, nil)
	install := newStardewInstall(t)
	done := make(chan error, 1)
	go func() { done <- d.ConfigureGame("stardewvalley", "Stardew Valley", 413150, install, "Mods") }()
	select {
	case err := <-done:
		t.Fatalf("ConfigureGame finished before startup recovery: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	d.RecoverAll()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ConfigureGame after recovery: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ConfigureGame never resumed after startup recovery")
	}
}

// TestStartupDefersALoaderRecoveryWhoseLockIsHeld locks that a held transaction lock at startup is retried once and never registered as pending.
func TestStartupDefersALoaderRecoveryWhoseLockIsHeld(t *testing.T) {
	cases := []struct {
		name        string
		secondErr   error
		wantPending bool
	}{
		{name: "released", secondErr: nil},
		{name: "still held", secondErr: fmt.Errorf("locked: %w", smapi.ErrTransactionActive)},
		{name: "retry fails", secondErr: errors.New("preimage missing"), wantPending: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			install := newStardewInstall(t)
			if err := os.MkdirAll(filepath.Join(install, smapi.WorkDir, uuid.NewString()), 0o700); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			calls := 0
			engine := &fakeLoaderEngine{recoverFn: func(string) (smapi.RecoverResult, error) {
				mu.Lock()
				defer mu.Unlock()
				calls++
				if calls == 1 {
					return smapi.RecoverResult{}, fmt.Errorf("locked: %w", smapi.ErrTransactionActive)
				}
				return smapi.RecoverResult{}, tc.secondErr
			}}
			d := newUnrecoveredDaemon(t, newStardewGames(install), func(d *Daemon) {
				d.svc.modLoader.engineFor = func(smapi.LoaderSpec) loaderEngine { return engine }
			})
			d.RecoverAll()
			if _, _, recovers := engine.counts(); recovers != 2 {
				t.Fatalf("startup ran %d loader recoveries, want one retry after the held lock", recovers)
			}
			pending := d.recoveryPendingFor("stardewvalley")
			if (pending != nil) != tc.wantPending {
				t.Fatalf("pending = %+v, want pending %v", pending, tc.wantPending)
			}
		})
	}
}

// TestStartupDeactivatesARootDeploymentLeftByAnUnmountedFarm locks that a crash leaving the farm and root deployment active no longer blocks loader operations once the farm is restored.
func TestStartupDeactivatesARootDeploymentLeftByAnUnmountedFarm(t *testing.T) {
	d, install := newLoaderTestDaemon(t, nil, nil)
	writeFixture(t, filepath.Join(config.ModsDir("stardewvalley"), "RootMod", vfs.RootContentDirName, "extra-tool.sh"))
	setStardewModList(t, d, "Default", map[string]bool{"RootMod": true})
	if _, err := d.MountVFS("stardewvalley", "Default"); err != nil {
		t.Fatalf("MountVFS: %v", err)
	}
	rootLink := filepath.Join(install, "extra-tool.sh")
	if _, err := os.Lstat(rootLink); err != nil {
		t.Fatalf("root deployment missing after mount: %v", err)
	}
	unmanaged := filepath.Join(install, "user-notes.txt")
	writeFixture(t, unmanaged)

	restarted := restartDaemon(t, d)
	restarted.RecoverAll()
	if pending := restarted.recoveryPendingFor("stardewvalley"); pending != nil {
		t.Fatalf("recovery registered %+v, want a clean restore", pending)
	}
	if _, err := os.Lstat(rootLink); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the root deployment survived the farm's recovery: %v", err)
	}
	if _, err := os.Stat(unmanaged); err != nil {
		t.Fatalf("recovery removed an unmanaged game-root file: %v", err)
	}
	release, err := reserveExclusive(t, restarted, "stardewvalley")
	if err != nil {
		t.Fatalf("loader operation after recovery: %v", err)
	}
	release()
}

// TestStageSweepKeepsTheGUIsRecentStages locks that a GUI install stage survives the startup sweep for a day, since its top folder's mtime does not change while files are copied below it.
func TestStageSweepKeepsTheGUIsRecentStages(t *testing.T) {
	d := newIsolatedDaemon(t, newSkyrimGames(t))
	modsDir := config.ModsDir("skyrimse")
	recent := filepath.Join(modsDir, ".stage-ui-recent")
	stale := filepath.Join(modsDir, ".stage-ui-stale")
	agedDir(t, recent, time.Hour)
	agedDir(t, stale, 25*time.Hour)
	d.sweepOrphanStageDirs("skyrimse")
	if _, err := os.Stat(recent); err != nil {
		t.Fatalf("the sweep removed a GUI stage younger than a day: %v", err)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a GUI stage older than a day survived the sweep: %v", err)
	}
}

// TestStartupKeepsARootDeploymentWhileAnotherLoaderTransactionHoldsTheInstall locks that a still-held loader lock blocks the orphaned root deployment's removal.
func TestStartupKeepsARootDeploymentWhileAnotherLoaderTransactionHoldsTheInstall(t *testing.T) {
	d, install := newLoaderTestDaemon(t, nil, nil)
	writeFixture(t, filepath.Join(config.ModsDir("stardewvalley"), "RootMod", vfs.RootContentDirName, "extra-tool.sh"))
	setStardewModList(t, d, "Default", map[string]bool{"RootMod": true})
	if _, err := d.MountVFS("stardewvalley", "Default"); err != nil {
		t.Fatalf("MountVFS: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(install, smapi.WorkDir, uuid.NewString()), 0o700); err != nil {
		t.Fatal(err)
	}
	engine := &fakeLoaderEngine{recoverFn: func(string) (smapi.RecoverResult, error) {
		return smapi.RecoverResult{}, fmt.Errorf("locked: %w", smapi.ErrTransactionActive)
	}}
	restarted, err := New(d.config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.Shutdown)
	restarted.svc.modLoader.engineFor = func(smapi.LoaderSpec) loaderEngine { return engine }
	restarted.RecoverAll()
	if _, err := os.Lstat(filepath.Join(install, "extra-tool.sh")); err != nil {
		t.Fatalf("recovery removed the root deployment while a loader transaction held the install: %v", err)
	}
}

// TestDependencyReportStopsWaitingWhenTheCallerGivesUp locks that a report requested before startup recovery returns the caller's context error instead of waiting out the recovery timeout.
func TestDependencyReportStopsWaitingWhenTheCallerGivesUp(t *testing.T) {
	d := newUnrecoveredDaemon(t, newStardewGames(newStardewInstall(t)), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var err error
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err = d.GetModDependencyReport(ctx, "stardewvalley", "Default", false, false)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the report kept waiting for recovery after its caller's deadline")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("report error = %v, want the caller's deadline", err)
	}
}

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/vfs"
)

// sessionFarmFixture mounts a Stardew farm with a mod file and a game-root deployment in isolated directories.
func sessionFarmFixture(t *testing.T) (*Daemon, map[string]config.GameConfig, string, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	install := filepath.Join(t.TempDir(), "Stardew Valley")
	writeFixture(t, filepath.Join(install, "Stardew Valley"))
	games := map[string]config.GameConfig{
		"stardewvalley": {Name: "Stardew Valley", InstallPath: install, DataSubpath: "Mods", SteamAppID: 413150},
	}
	d := newIsolatedDaemon(t, games)
	modDir := filepath.Join(config.ModsDir("stardewvalley"), "SessionMod")
	writeFixture(t, filepath.Join(modDir, "Plain", "readme.txt"))
	writeFixture(t, filepath.Join(modDir, vfs.RootContentDirName, "session-root.txt"))
	if err := d.SetModList("stardewvalley", "Default", []dto.ModListEntryResult{{ModName: "SessionMod", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.MountVFS("stardewvalley", "Default"); err != nil {
		t.Fatal(err)
	}
	return d, games, install, filepath.Join(install, "Mods")
}

// restartSessionDaemon creates another daemon against the same isolated state without shutting down the first one.
func restartSessionDaemon(t *testing.T, games map[string]config.GameConfig, now func() time.Time, scans ...func(string) (bool, error)) *Daemon {
	t.Helper()
	cfg := config.DefaultConfig()
	for gameID, gc := range games {
		cfg.Games[gameID] = gc
	}
	d, err := newWithClock(cfg, now, scans...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Shutdown)
	d.RecoverAll()
	return d
}

// requireDeferred checks the game and operation in a recovery-deferred refusal.
func requireDeferred(t *testing.T, err error, operation string) {
	t.Helper()
	var deferred *dto.RecoveryDeferredError
	if !errors.As(err, &deferred) || deferred.GameID != "stardewvalley" || deferred.Operation != operation {
		t.Fatalf("error = %v, want RecoveryDeferredError for stardewvalley %s", err, operation)
	}
}

// TestRestartDuringGamePreservesDataAndRoot checks a process from the install preserves the farm and game-root links across startup.
func TestRestartDuringGamePreservesDataAndRoot(t *testing.T) {
	_, games, install, dataPath := sessionFarmFixture(t)
	procDir := filepath.Join(t.TempDir(), "proc")
	if err := os.Mkdir(procDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeCommandLine(t, procDir, "12345", filepath.Join(t.TempDir(), "game"), filepath.Join(install, "Mods"), "game")
	useProcessTable(t, procDir)

	restarted := restartSessionDaemon(t, games, time.Now)
	if _, err := os.Stat(filepath.Join(dataPath, vfs.SentinelFilename)); err != nil {
		t.Fatalf("farm sentinel was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataPath, "Plain", "readme.txt")); err != nil {
		t.Fatalf("farm file was removed: %v", err)
	}
	if _, err := os.Readlink(filepath.Join(install, "session-root.txt")); err != nil {
		t.Fatalf("game-root symlink was removed: %v", err)
	}
	if _, err := os.Stat(dataPath + ".orig"); err != nil {
		t.Fatalf("original deploy folder was moved: %v", err)
	}
	restarted.pendingRecoveriesMu.Lock()
	deferred := restarted.deferredRecoveries[filepath.Clean(install)]
	restarted.pendingRecoveriesMu.Unlock()
	if len(deferred.gameIDs) != 1 || deferred.gameIDs[0] != "stardewvalley" || deferred.reason == "" || deferred.detectedAt.IsZero() {
		t.Fatalf("deferred install = %+v", deferred)
	}
	_, err := restarted.MountVFS("stardewvalley", "Default")
	requireDeferred(t, err, dto.BusyOperationMount)
	_, err = restarted.LaunchGame("stardewvalley", false, "Default")
	requireDeferred(t, err, dto.BusyOperationLaunch)
	_, err = restarted.MountVFSWithSwap("stardewvalley", "Default")
	requireDeferred(t, err, dto.BusyOperationMount)
	requireDeferred(t, restarted.UnmountVFS("stardewvalley"), dto.BusyOperationUnmount)
	requireDeferred(t, restarted.RebuildVFS("stardewvalley"), dto.BusyOperationApply)
	requireDeferred(t, restarted.RestoreFromBackup("stardewvalley", dto.RecoveryKindUnspecified, ""), "restore_from_backup")
	_, _, err = restarted.LaunchExecutable("stardewvalley", "unknown", "Default")
	requireDeferred(t, err, dto.BusyOperationTool)
	_, _, err = restarted.StartInstall(dto.StartInstallRequest{GameID: "stardewvalley"})
	requireDeferred(t, err, "install")
	_, _, _, err = restarted.ReinstallMod("stardewvalley", "SessionMod")
	requireDeferred(t, err, dto.BusyOperationReinstall)
	_, err = restarted.InstallModLoader(context.Background(), "stardewvalley", false)
	requireDeferred(t, err, dto.BusyOperationModLoader)
}

// TestRestartDuringLaunchGraceDefersRecovery checks a fresh ticket protects the farm and an expired ticket alone does not.
func TestRestartDuringLaunchGraceDefersRecovery(t *testing.T) {
	for _, tc := range []struct {
		name     string
		elapsed  time.Duration
		deferred bool
	}{
		{name: "fresh", elapsed: time.Minute, deferred: true},
		{name: "grace boundary", elapsed: steamLaunchGrace},
		{name: "expired", elapsed: steamLaunchGrace + time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, games, install, dataPath := sessionFarmFixture(t)
			at := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
			d.now = func() time.Time { return at }
			if err := d.writeLaunchTicketForGame("stardewvalley", "Default", dataPath); err != nil {
				t.Fatal(err)
			}
			now := func() time.Time { return at.Add(tc.elapsed) }
			restarted := restartSessionDaemon(t, games, now)
			if got := restarted.deferredFor("stardewvalley", "mount") != nil; got != tc.deferred {
				t.Fatalf("deferred = %t, want %t", got, tc.deferred)
			}
			_, sentinelErr := os.Stat(filepath.Join(dataPath, vfs.SentinelFilename))
			_, rootErr := os.Lstat(filepath.Join(install, "session-root.txt"))
			_, ticketErr := os.Stat(dataPath + sessionTicketSuffix)
			if tc.deferred {
				if sentinelErr != nil || rootErr != nil || ticketErr != nil {
					t.Fatalf("fresh ticket did not preserve farm, root, and ticket: %v, %v, %v", sentinelErr, rootErr, ticketErr)
				}
			} else if !os.IsNotExist(sentinelErr) || !os.IsNotExist(rootErr) || !os.IsNotExist(ticketErr) {
				t.Fatalf("expired ticket did not restore farm and remove root and ticket: %v, %v, %v", sentinelErr, rootErr, ticketErr)
			}
		})
	}
}

// TestProcessScanFailureDefersRecovery checks an unreadable process table keeps the farm and root deployment intact.
func TestProcessScanFailureDefersRecovery(t *testing.T) {
	_, games, install, dataPath := sessionFarmFixture(t)
	restarted := restartSessionDaemon(t, games, time.Now, func(string) (bool, error) {
		return false, errors.New("unreadable process table")
	})
	requireDeferred(t, restarted.deferredFor("stardewvalley", "mount"), "mount")
	for _, path := range []string{filepath.Join(dataPath, vfs.SentinelFilename), filepath.Join(install, "session-root.txt")} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("scan failure removed %s: %v", path, err)
		}
	}
}

// TestLaunchWritesAndUnmountRemovesTicket checks Steam opens only after a durable session record exists and unmount removes it.
func TestLaunchWritesAndUnmountRemovesTicket(t *testing.T) {
	d, _, _, dataPath := sessionFarmFixture(t)
	at := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return at }
	calls := 0
	d.steamOpener = func(url string) (int, error) {
		calls++
		if url != "steam://rungameid/413150" {
			t.Errorf("Steam URL = %q", url)
		}
		if _, err := os.Stat(dataPath + sessionTicketSuffix); err != nil {
			t.Errorf("ticket missing before Steam opener: %v", err)
		}
		return 4321, nil
	}
	pid, err := d.LaunchGame("stardewvalley", false, "Default")
	if err != nil || pid != 4321 || calls != 1 {
		t.Fatalf("LaunchGame = %d, %v, %d opens", pid, err, calls)
	}
	contents, err := os.ReadFile(dataPath + sessionTicketSuffix)
	if err != nil {
		t.Fatal(err)
	}
	var ticket launchTicket
	if err := json.Unmarshal(contents, &ticket); err != nil {
		t.Fatal(err)
	}
	if ticket.SchemaVersion != 1 || ticket.GameID != "stardewvalley" || ticket.Profile != "Default" || ticket.PID != 0 ||
		len(ticket.AppIDs) != 1 || ticket.AppIDs[0] != 413150 || !ticket.LaunchedAt.Equal(at) || !strings.Contains(string(contents), `"launched_at":"2026-09-26T12:00:00Z"`) {
		t.Fatalf("ticket = %+v; JSON = %s", ticket, contents)
	}
	d.now = func() time.Time { return at.Add(steamLaunchGrace + time.Second) }
	if err := d.UnmountVFS("stardewvalley"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dataPath + sessionTicketSuffix); !os.IsNotExist(err) {
		t.Fatalf("ticket after unmount: %v", err)
	}
}

// TestConstructorRecoverySkipsDeferredInstalls checks constructor reinstall recovery leaves a live farm's mod swap intent untouched.
func TestConstructorRecoverySkipsDeferredInstalls(t *testing.T) {
	_, games, install, dataPath := sessionFarmFixture(t)
	modsDir := config.ModsDir("stardewvalley")
	intent := reinstallIntent{SchemaVersion: reinstallIntentVersion, Mod: "SessionMod", Stage: reinstallStagePrefix + "interrupted", Old: reinstallOldPrefix + "interrupted"}
	intentPath := filepath.Join(modsDir, reinstallIntentPrefix+"interrupted"+reinstallIntentSuffix)
	if err := writeReinstallIntent(intentPath, intent); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(modsDir, "SessionMod"), filepath.Join(modsDir, intent.Old)); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(modsDir, intent.Stage, "staged.txt"))
	procDir := filepath.Join(t.TempDir(), "proc")
	if err := os.Mkdir(procDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeCommandLine(t, procDir, "12345", filepath.Join(t.TempDir(), "game"), install, "game")
	useProcessTable(t, procDir)
	restarted := restartSessionDaemon(t, games, time.Now)
	requireDeferred(t, restarted.deferredFor("stardewvalley", "reinstall"), "reinstall")
	for _, path := range []string{intentPath, filepath.Join(modsDir, intent.Old), filepath.Join(modsDir, intent.Stage), filepath.Join(dataPath, vfs.SentinelFilename)} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("constructor recovery changed %s: %v", path, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(modsDir, "SessionMod")); !os.IsNotExist(err) {
		t.Fatalf("constructor restored mod while farm is live: %v", err)
	}
}

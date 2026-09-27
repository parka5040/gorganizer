package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/vfs"
)

// TestLaunchRefusesWithoutDurableTicket checks Steam is never invoked when its launch record cannot be written.
func TestLaunchRefusesWithoutDurableTicket(t *testing.T) {
	d, _, _, dataPath := sessionFarmFixture(t)
	if err := os.Mkdir(dataPath+sessionTicketSuffix, 0o755); err != nil {
		t.Fatal(err)
	}
	called := false
	d.steamOpener = func(string) (int, error) {
		called = true
		return 0, nil
	}
	_, err := d.LaunchGame("stardewvalley", false, "Default")
	if err == nil || called {
		t.Fatalf("launch without durable ticket = %v; Steam called = %t", err, called)
	}
	if _, err := os.Stat(filepath.Join(dataPath, vfs.SentinelFilename)); err != nil {
		t.Fatalf("failed launch changed farm: %v", err)
	}
}

// TestShutdownDeactivationRemovesTicket checks idle teardown removes the durable launch record with the farm.
func TestShutdownDeactivationRemovesTicket(t *testing.T) {
	d, _, _, dataPath := sessionFarmFixture(t)
	if err := d.writeLaunchTicketForGame("stardewvalley", "Default", dataPath); err != nil {
		t.Fatal(err)
	}
	d.deactivateIdleFarms()
	if _, err := os.Lstat(dataPath + sessionTicketSuffix); !os.IsNotExist(err) {
		t.Fatalf("ticket after idle shutdown teardown: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dataPath, vfs.SentinelFilename)); !os.IsNotExist(err) {
		t.Fatalf("farm after idle shutdown teardown: %v", err)
	}
}

// TestSharedInstallDefersBothGamesWithoutBlockingAnotherInstall checks physical-install grouping and independent recovery.
func TestSharedInstallDefersBothGamesWithoutBlockingAnotherInstall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	parent := t.TempDir()
	shared := filepath.Join(parent, "FNV")
	other := filepath.Join(parent, "Skyrim")
	writeFixture(t, filepath.Join(shared, "Data", "FalloutNV.esm"))
	writeFixture(t, filepath.Join(other, "Data", "Skyrim.esm"))
	games := map[string]config.GameConfig{
		"falloutnv": {Name: "Fallout New Vegas", InstallPath: shared, DataSubpath: "Data", SteamAppID: 22380},
		"ttw":       {Name: "TTW", LinkedFromGameID: "falloutnv", DataSubpath: "Data"},
		"skyrimse":  {Name: "Skyrim", InstallPath: other, DataSubpath: "Data", SteamAppID: 489830},
	}
	d := newIsolatedDaemon(t, games)
	for _, gameID := range []string{"falloutnv", "skyrimse"} {
		if _, err := d.MountVFS(gameID, "Default"); err != nil {
			t.Fatal(err)
		}
	}
	procDir := filepath.Join(t.TempDir(), "proc")
	if err := os.Mkdir(procDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeCommandLine(t, procDir, "12345", filepath.Join(t.TempDir(), "game"), shared, "game")
	useProcessTable(t, procDir)
	restarted := restartSessionDaemon(t, games, time.Now)
	for _, gameID := range []string{"falloutnv", "ttw"} {
		var deferred *dto.RecoveryDeferredError
		if !errors.As(restarted.deferredFor(gameID, "mount"), &deferred) || deferred.GameID != gameID {
			t.Errorf("%s not deferred with its shared install", gameID)
		}
	}
	if restarted.deferredFor("skyrimse", "mount") != nil {
		t.Error("unrelated install was deferred")
	}
	if _, err := os.Stat(filepath.Join(shared, "Data", vfs.SentinelFilename)); err != nil {
		t.Errorf("shared farm was restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "Data", vfs.SentinelFilename)); !os.IsNotExist(err) {
		t.Errorf("unrelated farm was not restored: %v", err)
	}
}

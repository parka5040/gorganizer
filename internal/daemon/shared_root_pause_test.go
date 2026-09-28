package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/steam"
	"github.com/parka/gorganizer/internal/vfs"
)

// TestSharedRootMaintenanceHonorsDeploymentOwner checks an own-root pause and each direction of cross-game refusal.
func TestSharedRootMaintenanceHonorsDeploymentOwner(t *testing.T) {
	for _, tc := range []struct {
		name      string
		deployed  string
		requester string
		farm      bool
	}{
		{name: "falloutnv own root and farm", deployed: "falloutnv", requester: "falloutnv", farm: true},
		{name: "falloutnv root blocks ttw", deployed: "falloutnv", requester: "ttw"},
		{name: "ttw farm and root block falloutnv", deployed: "ttw", requester: "falloutnv", farm: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			install, _ := steamFixture(t, 22380, "Fallout New Vegas")
			d := newIsolatedDaemon(t, map[string]config.GameConfig{
				"falloutnv": {InstallPath: install, DataSubpath: "Data", SteamAppID: 22380},
				"ttw":       {InstallPath: filepath.Join(t.TempDir(), "synthetic"), DataSubpath: "Data", LinkedFromGameID: "falloutnv"},
			})
			d.readSteamAppState = steam.ReadAppState
			rootFile := filepath.Join(install, "root.txt")
			if tc.farm {
				writeFixture(t, filepath.Join(config.ModsDir(tc.deployed), "root-only", vfs.RootContentDirName, "root.txt"))
				if err := d.SetModList(tc.deployed, "Default", []dto.ModListEntryResult{{ModName: "root-only", Enabled: true}}); err != nil {
					t.Fatal(err)
				}
				if _, err := d.MountVFS(tc.deployed, "Default"); err != nil {
					t.Fatal(err)
				}
			} else {
				d.mu.Lock()
				root, err := d.ensureRootDeploymentManager(tc.deployed, d.config.Games[tc.deployed])
				d.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				modRoot := t.TempDir()
				writeFixture(t, filepath.Join(modRoot, vfs.RootContentDirName, "root.txt"))
				if _, err := root.Apply([]vfs.Layer{{Name: "root-only", RootPath: modRoot, Enabled: true}}, "Default"); err != nil {
					t.Fatal(err)
				}
			}
			d.mu.RLock()
			shared := d.rootDeployMgrs["falloutnv"] != nil && d.rootDeployMgrs["falloutnv"] == d.rootDeployMgrs["ttw"]
			d.mu.RUnlock()
			if !shared {
				t.Fatal("FNV and TTW did not share their root deployment manager")
			}
			if _, err := os.Lstat(rootFile); err != nil {
				t.Fatalf("root file was not deployed: %v", err)
			}
			status, err := d.SetSteamMaintenance(tc.requester, true, false)
			if tc.requester != tc.deployed {
				var busy *dto.OperationBusyError
				if !errors.As(err, &busy) || busy.Holder != tc.deployed || busy.Operation != dto.BusyOperationMounted {
					t.Fatalf("pause = %+v, %v, want mounted holder %s", status, err, tc.deployed)
				}
				if _, err := os.Lstat(rootFile); err != nil {
					t.Fatalf("cross-game pause removed the root deployment: %v", err)
				}
				return
			}
			if err != nil || status == nil || status.SteamMaintenance != dto.SteamMaintenanceUser {
				t.Fatalf("own-root pause = %+v, %v", status, err)
			}
			if _, err := os.Lstat(rootFile); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("own-root pause kept the deployed root file: %v", err)
			}
		})
	}
}

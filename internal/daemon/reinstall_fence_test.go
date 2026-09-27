package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
)

func TestReinstallIsFencedAndSwapsUnderTheDaemonLock(t *testing.T) {
	install := newStardewInstall(t)
	d := newIsolatedDaemon(t, newStardewGames(install))
	folder, _ := installForReinstall(t, d, "stardewvalley", "Swap", map[string]string{
		"Swap/manifest.json": `{"Name":"Swap","Author":"t","Version":"1.0.0","UniqueID":"a.swap","EntryDll":"S.dll"}`,
		"Swap/assets/a.png":  "original",
	}, false)
	modDir := filepath.Join(config.ModsDir("stardewvalley"), folder)
	writeFileContent(t, filepath.Join(modDir, "Swap", "assets", "a.png"), "damaged")
	setStardewModList(t, d, "Default", map[string]bool{folder: true})

	held := map[string]bool{}
	var loaderErr error
	mounted := make(chan error, 1)
	d.reinstallFault = func(step string) error {
		switch step {
		case "replayed":
			release, err := reserveExclusive(t, d, "stardewvalley")
			if err == nil {
				release()
			}
			loaderErr = err
		case "move-aside", "moved-aside", "install":
			locked := d.mu.TryLock()
			if locked {
				d.mu.Unlock()
			}
			held[step] = !locked
			if step == "moved-aside" {
				go func() {
					_, err := d.MountVFS("stardewvalley", "Default")
					mounted <- err
				}()
			}
		}
		return nil
	}
	if _, _, _, err := d.ReinstallMod(context.Background(), "stardewvalley", folder, ""); err != nil {
		t.Fatalf("ReinstallMod: %v", err)
	}
	d.reinstallFault = nil
	requireBusy(t, "loader during a reinstall", loaderErr, dto.BusyOperationReinstall)
	if want := map[string]bool{"move-aside": true, "moved-aside": true, "install": true}; !reflect.DeepEqual(held, want) {
		t.Errorf("s.mu held during swap steps = %v, want %v", held, want)
	}
	if err := <-mounted; err != nil {
		t.Fatalf("MountVFS racing the swap: %v", err)
	}
	farmFile := filepath.Join(install, "Mods", "Swap", "assets", "a.png")
	data, err := os.ReadFile(farmFile)
	if err != nil || string(data) != "original" {
		t.Fatalf("farm file = %q (%v), want the reinstalled content", data, err)
	}
	var st syscall.Stat_t
	if err := syscall.Stat(farmFile, &st); err != nil || st.Nlink != 2 {
		t.Errorf("farm file link count = %d (%v), want 2 (linked to the reinstalled mod)", st.Nlink, err)
	}
	if err := d.UnmountVFS("stardewvalley"); err != nil {
		t.Fatalf("UnmountVFS: %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(config.ModsDir("stardewvalley"), "Overwrite")); err == nil && len(entries) != 0 {
		t.Errorf("unmount captured stale files into Overwrite: %v", entries)
	}
	release, err := reserveExclusive(t, d, "stardewvalley")
	if err != nil {
		t.Fatalf("reinstall left the fence reserved: %v", err)
	}
	release()
}

// reinstallLeftovers returns the reinstall stage folders and intents left in the game's mods dir.
func reinstallLeftovers(t *testing.T, gameID string) []string {
	t.Helper()
	entries, err := os.ReadDir(config.ModsDir(gameID))
	if err != nil {
		t.Fatal(err)
	}
	var leftovers []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), reinstallStagePrefix) || strings.HasPrefix(entry.Name(), reinstallIntentPrefix) {
			leftovers = append(leftovers, entry.Name())
		}
	}
	return leftovers
}

// TestReinstallAppliedButDisabledModRebuildsFarm checks that a disabled mod's applied files are removed before swapping its folder.
func TestReinstallAppliedButDisabledModRebuildsFarm(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	install := newStardewInstall(t)
	d := newIsolatedDaemon(t, newStardewGames(install))
	folder, _ := installForReinstall(t, d, "stardewvalley", "Linked", map[string]string{
		"Linked/manifest.json": `{"Name":"Linked","Author":"t","Version":"1.0.0","UniqueID":"a.linked","EntryDll":"L.dll"}`,
		"Linked/assets/a.png":  "original",
	}, false)
	setStardewModList(t, d, "Default", map[string]bool{folder: true})
	if _, err := d.MountVFS("stardewvalley", "Default"); err != nil {
		t.Fatalf("MountVFS: %v", err)
	}
	mounted := true
	t.Cleanup(func() {
		if mounted {
			_ = d.UnmountVFS("stardewvalley")
		}
	})
	setStardewModList(t, d, "Default", map[string]bool{folder: false})

	if _, _, _, err := d.ReinstallMod(context.Background(), "stardewvalley", folder, ""); err != nil {
		t.Fatalf("reinstall of a disabled but still deployed mod: %v", err)
	}
	farmFile := filepath.Join(install, "Mods", "Linked", "assets", "a.png")
	if _, err := os.Lstat(farmFile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("disabled mod still deployed after reinstall: %v", err)
	}
	if status, err := d.GetVFSStatus("stardewvalley"); err != nil || status.Dirty {
		t.Errorf("farm after applying pending disable = %+v, %v", status, err)
	}
	if _, err := os.Stat(filepath.Join(config.ModsDir("stardewvalley"), folder, "Linked", "assets", "a.png")); err != nil {
		t.Errorf("reinstalled mod is missing: %v", err)
	}
	if err := d.UnmountVFS("stardewvalley"); err != nil {
		t.Fatalf("UnmountVFS: %v", err)
	}
	mounted = false
	if entries, err := os.ReadDir(filepath.Join(config.ModsDir("stardewvalley"), "Overwrite")); err == nil && len(entries) != 0 {
		t.Errorf("unmount captured files into Overwrite: %v", entries)
	}
	if leftovers := reinstallLeftovers(t, "stardewvalley"); len(leftovers) != 0 {
		t.Errorf("reinstall left %v behind", leftovers)
	}
}

// TestReinstallSwapRefusalDiscardsTheStageAndIntent checks that a game starting before the swap leaves the original mod intact.
func TestReinstallSwapRefusalDiscardsTheStageAndIntent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	install := newStardewInstall(t)
	d := newIsolatedDaemon(t, newStardewGames(install))
	folder, _ := installForReinstall(t, d, "stardewvalley", "Late", map[string]string{
		"Late/manifest.json": `{"Name":"Late","Author":"t","Version":"1.0.0","UniqueID":"a.late","EntryDll":"L.dll"}`,
		"Late/assets/a.png":  "original",
	}, false)
	modDir := filepath.Join(config.ModsDir("stardewvalley"), folder)
	writeFileContent(t, filepath.Join(modDir, "Late", "assets", "a.png"), "kept")
	setStardewModList(t, d, "Default", map[string]bool{folder: true})
	d.reinstallFault = func(step string) error {
		if step == "intent-written" {
			if _, err := d.MountVFS("stardewvalley", "Default"); err != nil {
				t.Errorf("MountVFS between the intent and the swap: %v", err)
			}
			fakeProcesses(d, true, nil)
		}
		return nil
	}
	t.Cleanup(func() { _ = d.UnmountVFS("stardewvalley") })

	_, _, _, err := d.ReinstallMod(context.Background(), "stardewvalley", folder, "")
	d.reinstallFault = nil
	fakeProcesses(d, false, nil)
	requireGameRunning(t, "reinstall after a game starts", err, dto.GameRunningOperationReinstall)
	if leftovers := reinstallLeftovers(t, "stardewvalley"); len(leftovers) != 0 {
		t.Errorf("refused swap left %v behind", leftovers)
	}
	data, err := os.ReadFile(filepath.Join(modDir, "Late", "assets", "a.png"))
	if err != nil || string(data) != "kept" {
		t.Errorf("original mod file = %q (%v), want it untouched", data, err)
	}
}

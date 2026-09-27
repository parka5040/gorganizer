package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/smapi"
)

// writeSMAPIMod writes a SMAPI manifest folder named folder with uniqueID under root.
func writeSMAPIMod(t *testing.T, root, folder, uniqueID string) {
	t.Helper()
	manifest := fmt.Sprintf(`{"Name":%q,"Author":"test","Version":"1.0.0","UniqueID":%q,"EntryDll":"Mod.dll"}`, folder, uniqueID)
	writeFileContent(t, filepath.Join(root, folder, "manifest.json"), manifest)
}

// writeFileContent writes content to path, creating parent directories.
func writeFileContent(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// setStardewModList writes the profile's modlist with the given mods enabled or disabled.
func setStardewModList(t *testing.T, d *Daemon, profileName string, mods map[string]bool) {
	t.Helper()
	entries := make([]dto.ModListEntryResult, 0, len(mods))
	for name, enabled := range mods {
		entries = append(entries, dto.ModListEntryResult{ModName: name, Enabled: enabled})
	}
	if err := d.SetModList("stardewvalley", profileName, entries); err != nil {
		t.Fatalf("SetModList(%s): %v", profileName, err)
	}
}

// preflight runs the launch loader check for profileName.
func preflight(d *Daemon, profileName string) error {
	d.mu.RLock()
	mm := d.mountMgrs["stardewvalley"]
	d.mu.RUnlock()
	return d.svc.launch.loaderPreflight("stardewvalley", mm, profileName)
}

// requireUnavailable fails unless err is an UnavailableError for state.
func requireUnavailable(t *testing.T, err error, state smapi.LoaderState) {
	t.Helper()
	var unavailable *smapi.UnavailableError
	if !errors.As(err, &unavailable) || unavailable.State != state || unavailable.GameID != "stardewvalley" {
		t.Fatalf("preflight error = %v, want UnavailableError(%s)", err, state)
	}
}

func TestLaunchPreflightChecksTheEffectiveSMAPIModSet(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state smapi.LoaderState
		setup func(t *testing.T, d *Daemon, install, modsDir string)
		want  bool
	}{
		{name: "no mods and not installed", state: smapi.StateNotInstalled},
		{
			name: "enabled mod and not installed", state: smapi.StateNotInstalled, want: true,
			setup: func(t *testing.T, d *Daemon, _, modsDir string) {
				writeSMAPIMod(t, filepath.Join(modsDir, "Mod A"), "ModA", "author.moda")
				setStardewModList(t, d, "Default", map[string]bool{"Mod A": true})
			},
		},
		{
			name: "enabled mod and launcher reverted", state: smapi.StateLauncherReverted, want: true,
			setup: func(t *testing.T, d *Daemon, _, modsDir string) {
				writeSMAPIMod(t, filepath.Join(modsDir, "Mod A"), "ModA", "author.moda")
				setStardewModList(t, d, "Default", map[string]bool{"Mod A": true})
			},
		},
		{
			name: "enabled mod and loader ok", state: smapi.StateOK,
			setup: func(t *testing.T, d *Daemon, _, modsDir string) {
				writeSMAPIMod(t, filepath.Join(modsDir, "Mod A"), "ModA", "author.moda")
				setStardewModList(t, d, "Default", map[string]bool{"Mod A": true})
			},
		},
		{
			name: "disabled mod and not installed", state: smapi.StateNotInstalled,
			setup: func(t *testing.T, d *Daemon, _, modsDir string) {
				writeSMAPIMod(t, filepath.Join(modsDir, "Mod A"), "ModA", "author.moda")
				setStardewModList(t, d, "Default", map[string]bool{"Mod A": false})
			},
		},
		{
			name: "only bundled base mods and not installed", state: smapi.StateNotInstalled,
			setup: func(t *testing.T, _ *Daemon, install, _ string) {
				writeSMAPIMod(t, filepath.Join(install, "Mods"), "ConsoleCommands", "SMAPI.ConsoleCommands")
				writeSMAPIMod(t, filepath.Join(install, "Mods"), "SaveBackup", "smapi.savebackup")
			},
		},
		{
			name: "leftover loader-owned mods and not installed", state: smapi.StateNotInstalled,
			setup: func(t *testing.T, _ *Daemon, install, _ string) {
				writeSMAPIMod(t, filepath.Join(install, "Mods"), "ErrorHandler", "SMAPI.ErrorHandler")
				writeSMAPIMod(t, filepath.Join(install, "Mods"), "trainermod", "SMAPI.TrainerMod")
			},
		},
		{
			name: "mod only named like a loader-owned folder and not installed", state: smapi.StateNotInstalled, want: true,
			setup: func(t *testing.T, _ *Daemon, install, _ string) {
				writeSMAPIMod(t, filepath.Join(install, "Mods"), "ErrorHandlerPlus", "author.errorhandlerplus")
			},
		},
		{
			name: "user mod in the base folder and not installed", state: smapi.StateIncomplete, want: true,
			setup: func(t *testing.T, _ *Daemon, install, _ string) {
				writeSMAPIMod(t, filepath.Join(install, "Mods"), "Manual", "author.manual")
			},
		},
		{
			name: "mod only in overwrite and not installed", state: smapi.StateNotInstalled, want: true,
			setup: func(t *testing.T, _ *Daemon, _, modsDir string) {
				writeSMAPIMod(t, filepath.Join(modsDir, "Overwrite"), "Generated", "author.generated")
			},
		},
		{
			name: "unparseable manifest and not installed", state: smapi.StateNotInstalled, want: true,
			setup: func(t *testing.T, d *Daemon, _, modsDir string) {
				writeFileContent(t, filepath.Join(modsDir, "Broken", "Broken", "manifest.json"), "{not json")
				setStardewModList(t, d, "Default", map[string]bool{"Broken": true})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := &fakeLoaderEngine{status: smapi.Status{State: tc.state}}
			d, install := newLoaderTestDaemon(t, engine, nil)
			modsDir := config.ModsDir("stardewvalley")
			if tc.setup != nil {
				tc.setup(t, d, install, modsDir)
			}
			err := preflight(d, "Default")
			if tc.want {
				requireUnavailable(t, err, tc.state)
				return
			}
			if err != nil {
				t.Fatalf("preflight = %v, want the launch allowed", err)
			}
		})
	}
}

func TestLaunchPreflightEvaluatesTheRequestedProfile(t *testing.T) {
	engine := &fakeLoaderEngine{status: smapi.Status{State: smapi.StateNotInstalled}}
	d, _ := newLoaderTestDaemon(t, engine, nil)
	modsDir := config.ModsDir("stardewvalley")
	writeSMAPIMod(t, filepath.Join(modsDir, "Mod A"), "ModA", "author.moda")
	for _, name := range []string{"Modded", "Vanilla"} {
		if _, err := d.CreateProfile("stardewvalley", name); err != nil {
			t.Fatal(err)
		}
	}
	setStardewModList(t, d, "Modded", map[string]bool{"Mod A": true})
	setStardewModList(t, d, "Vanilla", map[string]bool{"Mod A": false})

	if _, err := d.MountVFS("stardewvalley", "Modded"); err != nil {
		t.Fatalf("MountVFS(Modded): %v", err)
	}
	if err := preflight(d, "Vanilla"); err != nil {
		t.Fatalf("preflight switching to vanilla: %v", err)
	}
	if err := d.UnmountVFS("stardewvalley"); err != nil {
		t.Fatal(err)
	}

	if _, err := d.MountVFS("stardewvalley", "Vanilla"); err != nil {
		t.Fatalf("MountVFS(Vanilla): %v", err)
	}
	t.Cleanup(func() { _ = d.UnmountVFS("stardewvalley") })
	requireUnavailable(t, preflight(d, "Modded"), smapi.StateNotInstalled)
}

func TestLaunchGameRefusesSMAPIModsWithoutTheLoader(t *testing.T) {
	d, _ := newLoaderTestDaemon(t, nil, nil)
	writeSMAPIMod(t, filepath.Join(config.ModsDir("stardewvalley"), "Mod A"), "ModA", "author.moda")
	setStardewModList(t, d, "Default", map[string]bool{"Mod A": true})

	_, err := d.LaunchGame("stardewvalley", false, "Default")
	requireUnavailable(t, err, smapi.StateNotInstalled)
	d.mu.RLock()
	mounted := d.mountMgrs["stardewvalley"].IsMounted()
	d.mu.RUnlock()
	if mounted {
		t.Error("a refused launch still auto-mounted the mods")
	}
	release, err := reserveExclusive(t, d, "stardewvalley")
	if err != nil {
		t.Fatalf("refused launch left its reservation behind: %v", err)
	}
	release()
}

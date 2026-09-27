package daemon

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/vfs"
)

func TestEnsureOptionalDataDir(t *testing.T) {
	for _, tc := range []struct {
		name     string
		gameID   string
		games    func(root string) map[string]config.GameConfig
		dataRel  string
		setup    func(t *testing.T, root string, mm *vfs.MountManager)
		wantErr  []string
		wantData bool
	}{
		{name: "creates absent directory", gameID: "stardewvalley", wantData: true},
		{
			name:   "leaves present directory",
			gameID: "stardewvalley",
			setup: func(t *testing.T, _ string, mm *vfs.MountManager) {
				t.Helper()
				if err := os.Mkdir(mm.DataPath(), 0755); err != nil {
					t.Fatal(err)
				}
			},
			wantData: true,
		},
		{
			name:   "leaves recovery backup state",
			gameID: "stardewvalley",
			setup: func(t *testing.T, _ string, mm *vfs.MountManager) {
				t.Helper()
				if err := os.Mkdir(mm.BackupPath(), 0755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:   "leaves activation intent state",
			gameID: "stardewvalley",
			setup: func(t *testing.T, root string, mm *vfs.MountManager) {
				t.Helper()
				intent := &vfs.ActivationIntent{
					SchemaVersion: 1,
					Magic:         vfs.IntentMagic,
					Kind:          vfs.IntentActivating,
					GameID:        "stardewvalley",
					DataPath:      mm.DataPath(),
					BackupPath:    mm.BackupPath(),
					OverwriteRoot: filepath.Join(root, "Overwrite"),
					PID:           os.Getpid(),
				}
				if err := vfs.WriteIntent(vfs.ActivationIntentPath(mm.DataPath()), intent); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:   "refuses corrupt activation intent",
			gameID: "stardewvalley",
			setup: func(t *testing.T, _ string, mm *vfs.MountManager) {
				t.Helper()
				if err := os.WriteFile(vfs.ActivationIntentPath(mm.DataPath()), []byte("intent"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: []string{"Mods.gorganizer-activating", "gorganizerctl recover --game stardewvalley"},
		},
		{
			name:   "refuses regular file",
			gameID: "stardewvalley",
			setup: func(t *testing.T, _ string, mm *vfs.MountManager) {
				t.Helper()
				if err := os.WriteFile(mm.DataPath(), []byte("file"), 0644); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: []string{"not a real directory"},
		},
		{
			name:   "refuses symlink",
			gameID: "stardewvalley",
			setup: func(t *testing.T, root string, mm *vfs.MountManager) {
				t.Helper()
				target := filepath.Join(root, "elsewhere")
				if err := os.Mkdir(target, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, mm.DataPath()); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: []string{"not a real directory"},
		},
		{
			name:    "refuses non-registry deploy folder",
			gameID:  "stardewvalley",
			dataRel: "Data",
			wantErr: []string{"registry deploy folder"},
		},
		{
			name:   "refuses unconfigured game",
			gameID: "stardewvalley",
			games: func(string) map[string]config.GameConfig {
				return map[string]config.GameConfig{}
			},
			wantErr: []string{"stardewvalley"},
		},
		{
			name:   "refuses relative install path",
			gameID: "stardewvalley",
			games: func(string) map[string]config.GameConfig {
				return map[string]config.GameConfig{"stardewvalley": {InstallPath: "relative", DataSubpath: "Mods"}}
			},
			wantErr: []string{"not absolute"},
		},
		{
			name:   "honors linked parent install path",
			gameID: "stardewvalley",
			games: func(root string) map[string]config.GameConfig {
				return map[string]config.GameConfig{
					"parent":        {InstallPath: root, DataSubpath: "Mods"},
					"stardewvalley": {DataSubpath: "Mods", LinkedFromGameID: "parent"},
				}
			},
			wantData: true,
		},
		{name: "ignores games without optional directories", gameID: "skyrimse"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			games := map[string]config.GameConfig{
				tc.gameID: {InstallPath: root, DataSubpath: "Mods"},
			}
			if tc.games != nil {
				games = tc.games(root)
			}
			dataRel := tc.dataRel
			if dataRel == "" {
				dataRel = "Mods"
			}
			mm := vfs.NewMountManager(filepath.Join(root, dataRel), filepath.Join(root, "Overwrite"), tc.gameID)
			if tc.setup != nil {
				tc.setup(t, root, mm)
			}
			vs := &VFSService{s: &session{config: &config.Config{Games: games}}}
			err := vs.ensureOptionalDataDir(tc.gameID, mm)
			if len(tc.wantErr) == 0 && err != nil {
				t.Fatalf("ensureOptionalDataDir: %v", err)
			}
			if len(tc.wantErr) > 0 {
				if err == nil {
					t.Fatal("ensureOptionalDataDir succeeded, want an error")
				}
				for _, part := range tc.wantErr {
					if !strings.Contains(err.Error(), part) {
						t.Errorf("error %q does not mention %q", err, part)
					}
				}
			}
			for _, path := range []string{mm.DataPath(), filepath.Join(root, "Mods")} {
				info, statErr := os.Lstat(path)
				if tc.wantData {
					if statErr != nil || !info.IsDir() {
						t.Fatalf("data directory %s missing: %v", path, statErr)
					}
					continue
				}
				if statErr == nil && info.IsDir() {
					t.Fatalf("data directory %s was created", path)
				}
				if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
					t.Fatalf("checking %s: %v", path, statErr)
				}
			}
		})
	}
}

// isolateDaemonState points every gorganizer state and XDG directory at a fresh test temp dir.
func isolateDaemonState(t *testing.T) {
	t.Helper()
	state := t.TempDir()
	t.Setenv("GORGANIZER_ROOT", filepath.Join(state, "root"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(state, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(state, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(state, "cache"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(state, "runtime"))
	t.Setenv("TMPDIR", filepath.Join(state, "tmp"))
	if err := os.MkdirAll(filepath.Join(state, "tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
}

// newIsolatedDaemon builds a recovered daemon whose state directories live under a test temp dir.
func newIsolatedDaemon(t *testing.T, games map[string]config.GameConfig) *Daemon {
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
	d.RecoverAll()
	return d
}

// installEntries lists the names directly inside dir.
func installEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestMountVFSCreatesOptionalDeployFolder(t *testing.T) {
	install := filepath.Join(t.TempDir(), "Stardew Valley")
	for _, marker := range []string{"Stardew Valley", "StardewValley"} {
		writeFixture(t, filepath.Join(install, marker))
	}
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		"stardewvalley": {Name: "Stardew Valley", InstallPath: install, DataSubpath: "Mods", SteamAppID: 413150},
	})
	modsPath := filepath.Join(install, "Mods")

	if _, err := d.MountVFS("stardewvalley", "Default"); err != nil {
		t.Fatalf("MountVFS: %v", err)
	}
	info, err := os.Lstat(modsPath)
	if err != nil || !info.IsDir() {
		t.Fatalf("Mods not a directory after mount: %v", err)
	}
	if _, err := os.Stat(filepath.Join(modsPath, vfs.SentinelFilename)); err != nil {
		t.Fatalf("overlay sentinel missing after mount: %v", err)
	}

	if err := d.UnmountVFS("stardewvalley"); err != nil {
		t.Fatalf("UnmountVFS: %v", err)
	}
	info, err = os.Lstat(modsPath)
	if err != nil || !info.IsDir() {
		t.Fatalf("Mods not a real directory after unmount: %v", err)
	}
	if names := installEntries(t, modsPath); len(names) != 0 {
		t.Errorf("Mods contents after unmount = %v, want empty", names)
	}
	if _, err := os.Lstat(modsPath + ".orig"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Mods.orig after unmount: %v, want not exist", err)
	}
}

func TestMountVFSRequiresBethesdaDataDir(t *testing.T) {
	install := filepath.Join(t.TempDir(), "Skyrim Special Edition")
	writeFixture(t, filepath.Join(install, "SkyrimSE.exe"))
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		"skyrimse": {Name: "Skyrim Special Edition", InstallPath: install, DataSubpath: "Data", SteamAppID: 489830},
	})

	_, err := d.MountVFS("skyrimse", "Default")
	if !errors.Is(err, vfs.ErrDataDirMissing) {
		t.Fatalf("MountVFS error = %v, want ErrDataDirMissing", err)
	}
	if names := installEntries(t, install); len(names) != 1 || names[0] != "SkyrimSE.exe" {
		t.Errorf("install contents after refused mount = %v, want only SkyrimSE.exe", names)
	}
}

// writeFixture writes a small regular file at path, creating its parent directories.
func writeFixture(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture"), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestGetVFSStatusReportsDirtyAfterSetModList(t *testing.T) {
	install := filepath.Join(t.TempDir(), "Stardew Valley")
	for _, marker := range []string{"Stardew Valley", "StardewValley"} {
		writeFixture(t, filepath.Join(install, marker))
	}
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		"stardewvalley": {Name: "Stardew Valley", InstallPath: install, DataSubpath: "Mods", SteamAppID: 413150},
	})
	writeFixture(t, filepath.Join(config.ModsDir("stardewvalley"), "ModA", "ModA", "manifest.json"))

	if _, err := d.MountVFS("stardewvalley", "Default"); err != nil {
		t.Fatalf("MountVFS: %v", err)
	}
	t.Cleanup(func() {
		if err := d.UnmountVFS("stardewvalley"); err != nil {
			t.Errorf("UnmountVFS: %v", err)
		}
	})

	clean, err := d.GetVFSStatus("stardewvalley")
	if err != nil {
		t.Fatalf("GetVFSStatus after mount: %v", err)
	}
	if !clean.Mounted || clean.Dirty || clean.ProfileName != "Default" {
		t.Fatalf("status after mount = %+v, want mounted, clean, profile Default", clean)
	}
	if want := filepath.Join(install, "Mods"); clean.MountPoint != want {
		t.Errorf("mount point = %q, want %q", clean.MountPoint, want)
	}
	if clean.AppliedGen == 0 || clean.AppliedGen != clean.DesiredGen {
		t.Errorf("generations after mount = applied %d desired %d, want equal and non-zero", clean.AppliedGen, clean.DesiredGen)
	}

	if err := d.SetModList("stardewvalley", "Default", []dto.ModListEntryResult{{ModName: "ModA", Enabled: true}}); err != nil {
		t.Fatalf("SetModList: %v", err)
	}
	dirty, err := d.GetVFSStatus("stardewvalley")
	if err != nil {
		t.Fatalf("GetVFSStatus after SetModList: %v", err)
	}
	if !dirty.Mounted || !dirty.Dirty {
		t.Fatalf("status after SetModList = %+v, want mounted and dirty", dirty)
	}
	if dirty.ProfileName != "Default" || dirty.EnabledModCount != 1 {
		t.Errorf("status after SetModList = profile %q, %d enabled mods, want Default with 1", dirty.ProfileName, dirty.EnabledModCount)
	}
	if dirty.DesiredGen <= dirty.AppliedGen {
		t.Errorf("generations after SetModList = applied %d desired %d, want desired ahead", dirty.AppliedGen, dirty.DesiredGen)
	}
	if want := filepath.Join(install, "Mods"); dirty.MountPoint != want {
		t.Errorf("mount point after SetModList = %q, want %q", dirty.MountPoint, want)
	}
}

// TestRemountingAMountedGameKeepsItsRootDeployment locks that a repeated mount request never tears down the active root deployment.
func TestRemountingAMountedGameKeepsItsRootDeployment(t *testing.T) {
	install := filepath.Join(t.TempDir(), "Skyrim Special Edition")
	writeFixture(t, filepath.Join(install, "SkyrimSE.exe"))
	writeFixture(t, filepath.Join(install, "Data", "Skyrim.esm"))
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		"skyrimse": {Name: "Skyrim Special Edition", InstallPath: install, DataSubpath: "Data", SteamAppID: 489830},
	})
	writeFixture(t, filepath.Join(config.ModsDir("skyrimse"), "RootMod", ".gorganizer-root", "root-file.txt"))
	if err := d.SetModList("skyrimse", "Default", []dto.ModListEntryResult{{ModName: "RootMod", Enabled: true}}); err != nil {
		t.Fatalf("SetModList: %v", err)
	}
	rootLink := filepath.Join(install, "root-file.txt")

	if _, err := d.MountVFS("skyrimse", "Default"); err != nil {
		t.Fatalf("first MountVFS: %v", err)
	}
	if _, err := os.Lstat(rootLink); err != nil {
		t.Fatalf("root deployment missing after mount: %v", err)
	}
	st, err := d.MountVFS("skyrimse", "Default")
	if err != nil {
		t.Fatalf("repeated MountVFS with the mounted profile: %v", err)
	}
	if st == nil || !st.Mounted || st.ProfileName != "Default" {
		t.Errorf("repeated MountVFS status = %+v, want mounted Default", st)
	}
	if _, err := os.Lstat(rootLink); err != nil {
		t.Fatalf("root deployment torn down by a repeated mount: %v", err)
	}
	if _, err := d.MountVFS("skyrimse", "Other"); !errors.Is(err, vfs.ErrAlreadyMounted) {
		t.Fatalf("MountVFS with another profile error = %v, want ErrAlreadyMounted", err)
	}
	if _, err := os.Lstat(rootLink); err != nil {
		t.Fatalf("root deployment torn down by a refused mount: %v", err)
	}
	if err := d.UnmountVFS("skyrimse"); err != nil {
		t.Fatalf("UnmountVFS: %v", err)
	}
	if _, err := os.Lstat(rootLink); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("root deployment still present after unmount: %v", err)
	}
}

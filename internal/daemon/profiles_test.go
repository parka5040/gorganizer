package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/mod"
	"github.com/parka/gorganizer/internal/profile"
	"github.com/parka/gorganizer/internal/separators"
)

const concurrencyIterations = 200

// newProfileDaemon builds an isolated Skyrim SE daemon with the named profiles created.
func newProfileDaemon(t *testing.T, profiles ...string) *Daemon {
	t.Helper()
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		"skyrimse": {InstallPath: t.TempDir(), DataSubpath: "Data"},
	})
	for _, name := range profiles {
		if _, err := d.CreateProfile("skyrimse", name); err != nil {
			t.Fatalf("CreateProfile(%s): %v", name, err)
		}
	}
	return d
}

// modListNames loads a profile's modlist as name → enabled plus the ordered names.
func modListNames(t *testing.T, d *Daemon, profileName string) (map[string]bool, []string) {
	t.Helper()
	entries, err := d.GetModList("skyrimse", profileName)
	if err != nil {
		t.Fatalf("GetModList(%s): %v", profileName, err)
	}
	byName := make(map[string]bool, len(entries))
	order := make([]string, 0, len(entries))
	for _, e := range entries {
		byName[e.ModName] = e.Enabled
		order = append(order, e.ModName)
	}
	return byName, order
}

// makeModFolders creates an empty mod folder for each name under the Skyrim SE mods dir.
func makeModFolders(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := os.MkdirAll(filepath.Join(config.ModsDir("skyrimse"), name), 0755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSetModListCannotFollowEmbeddedIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newProfileDaemon(t, "Default")
	makeModFolders(t, "SafeMod")
	profileDir := d.profileMgr.ProfileDir("skyrimse", "Default")
	profilePath := filepath.Join(profileDir, "profile.json")
	if err := os.WriteFile(profilePath, []byte(`{"name":"../../escape","game_id":"othergame","created_at":"2026-01-02T03:04:05Z"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := d.SetModList("skyrimse", "Default", []dto.ModListEntryResult{{ModName: "SafeMod", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(profileDir, "modlist.txt"))
	if err != nil || !strings.Contains(string(data), "+SafeMod\n") {
		t.Errorf("modlist in real profile = %q, err = %v", data, err)
	}
	for _, path := range []string{
		filepath.Join(config.ProfilesDir("skyrimse"), "../../escape"),
		filepath.Join(config.ProfilesDir("othergame"), "../../escape"),
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("escaped location %s exists or could not be checked: %v", path, err)
		}
	}
}

func TestSetSeparatorsRejectsUnsafeGameID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newProfileDaemon(t, "Default")
	err := d.SetSeparators("..", "Default", nil, true)
	var invalid *profile.IdentityInvalidError
	if !errors.As(err, &invalid) || invalid.Name != ".." {
		t.Fatalf("SetSeparators error = %v, want IdentityInvalidError", err)
	}
	if _, err := os.Lstat(filepath.Join(config.DataDir(), "profiles", "Default", "separators.yaml")); !os.IsNotExist(err) {
		t.Errorf("unsafe game path was written: %v", err)
	}
}

func TestEnsureInModListConcurrentAppendsLandInEveryProfile(t *testing.T) {
	profiles := []string{"Default", "Second"}
	d := newProfileDaemon(t, profiles...)
	for i := 0; i < concurrencyIterations; i++ {
		names := []string{fmt.Sprintf("A%03d", i), fmt.Sprintf("B%03d", i)}
		errs := make([]error, len(names))
		var wg sync.WaitGroup
		for j, name := range names {
			wg.Add(1)
			go func(j int, name string) {
				defer wg.Done()
				errs[j] = d.ensureInModList("skyrimse", name)
			}(j, name)
		}
		wg.Wait()
		for j, err := range errs {
			if err != nil {
				t.Fatalf("iteration %d ensureInModList(%s): %v", i, names[j], err)
			}
		}
	}
	for _, p := range profiles {
		byName, order := modListNames(t, d, p)
		if len(order) != 2*concurrencyIterations {
			t.Fatalf("profile %s has %d entries, want %d", p, len(order), 2*concurrencyIterations)
		}
		for i := 0; i < concurrencyIterations; i++ {
			for _, name := range []string{fmt.Sprintf("A%03d", i), fmt.Sprintf("B%03d", i)} {
				enabled, ok := byName[name]
				if !ok || enabled {
					t.Fatalf("profile %s entry %s present=%v enabled=%v, want present and disabled", p, name, ok, enabled)
				}
			}
		}
	}
}

func TestSetModListConcurrentWithEnsureInModListKeepsAppendedMod(t *testing.T) {
	d := newProfileDaemon(t, "Default")
	makeModFolders(t, "Base1", "Base2")
	stale := []dto.ModListEntryResult{{ModName: "Base2", Enabled: true}, {ModName: "Base1", Enabled: false}}
	if err := d.SetModList("skyrimse", "Default", stale); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < concurrencyIterations; i++ {
		name := fmt.Sprintf("New%03d", i)
		makeModFolders(t, name)
		var setErr, ensureErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			setErr = d.SetModList("skyrimse", "Default", stale)
		}()
		go func() {
			defer wg.Done()
			ensureErr = d.ensureInModList("skyrimse", name)
		}()
		wg.Wait()
		if setErr != nil || ensureErr != nil {
			t.Fatalf("iteration %d: SetModList = %v, ensureInModList = %v", i, setErr, ensureErr)
		}
		byName, order := modListNames(t, d, "Default")
		if _, ok := byName[name]; !ok {
			t.Fatalf("iteration %d lost appended mod %s: %v", i, name, order)
		}
		if len(order) != i+3 || order[0] != "Base2" || order[1] != "Base1" || !byName["Base2"] || byName["Base1"] {
			t.Fatalf("iteration %d modlist = %v (%v)", i, order, byName)
		}
	}
}

// saveCurrentModList writes entries straight into a Skyrim SE profile modlist.
func saveCurrentModList(t *testing.T, d *Daemon, profileName string, entries []mod.ModListEntry) {
	t.Helper()
	p, _, err := d.profileMgr.Load("skyrimse", profileName)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.profileMgr.Save(p, entries); err != nil {
		t.Fatal(err)
	}
}

// modListEntries returns a profile's modlist as name and enabled pairs.
func modListEntries(t *testing.T, d *Daemon, profileName string) []dto.ModListEntryResult {
	t.Helper()
	entries, err := d.GetModList("skyrimse", profileName)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]dto.ModListEntryResult, 0, len(entries))
	for _, e := range entries {
		out = append(out, dto.ModListEntryResult{ModName: e.ModName, Enabled: e.Enabled})
	}
	return out
}

// writeModMetadataFixture gives a Skyrim SE mod folder a metadata.yaml carrying non-index keys.
func writeModMetadataFixture(t *testing.T, name string) {
	t.Helper()
	modDir := filepath.Join(config.ModsDir("skyrimse"), name)
	if err := os.MkdirAll(modDir, 0755); err != nil {
		t.Fatal(err)
	}
	meta := &download.ModMetadata{
		Name: name + " Pretty", Folder: name, Category: "UI", Separator: "Group",
		SourceArchives: []download.SourceArchiveRef{{Path: "Downloads/" + name + ".zip", InstalledAt: "2020-01-01T00:00:00Z"}},
		Files:          []string{name + ".esp"}, FileCount: 1,
	}
	if err := download.SaveModMetadata(modDir, meta); err != nil {
		t.Fatal(err)
	}
}

func TestSetModListRetainsEntriesWhoseFolderExists(t *testing.T) {
	d := newProfileDaemon(t, "Default")
	for _, name := range []string{"A", "B", "C", "D"} {
		writeModMetadataFixture(t, name)
	}
	saveCurrentModList(t, d, "Default", []mod.ModListEntry{
		{Name: "A", Enabled: true},
		{Name: "Gone", Enabled: true},
		{Name: "B", Enabled: true},
		{Name: "D", Enabled: false},
	})

	stale := []dto.ModListEntryResult{{ModName: "C", Enabled: false}, {ModName: "A", Enabled: false}}
	if err := d.SetModList("skyrimse", "Default", stale); err != nil {
		t.Fatalf("SetModList: %v", err)
	}
	want := []dto.ModListEntryResult{
		{ModName: "C", Enabled: false},
		{ModName: "A", Enabled: false},
		{ModName: "B", Enabled: true},
		{ModName: "D", Enabled: false},
	}
	if got := modListEntries(t, d, "Default"); !reflect.DeepEqual(got, want) {
		t.Errorf("merged modlist = %+v, want %+v", got, want)
	}
	for i, name := range []string{"C", "A", "B", "D"} {
		meta, err := download.LoadModMetadata(filepath.Join(config.ModsDir("skyrimse"), name))
		if err != nil {
			t.Fatal(err)
		}
		if wantIndex := separators.FormatIndex(uint64(i+1) * trueIndexStep); meta.TrueIndex != wantIndex {
			t.Errorf("%s true_index = %q, want %q", name, meta.TrueIndex, wantIndex)
		}
		if meta.Name != name+" Pretty" || meta.Category != "UI" || meta.Separator != "Group" || len(meta.SourceArchives) != 1 || meta.FileCount != 1 {
			t.Errorf("%s metadata keys changed by true_index stamping: %+v", name, meta)
		}
	}
	if _, err := os.Stat(filepath.Join(config.ModsDir("skyrimse"), "Gone")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("dropped entry folder state = %v, want not exist", err)
	}
}

func TestSetModListAnchorsOmittedEntriesAfterTheirPredecessor(t *testing.T) {
	cases := []struct {
		name      string
		current   []mod.ModListEntry
		requested []dto.ModListEntryResult
		want      []dto.ModListEntryResult
	}{
		{
			name: "omitted entries follow their preceding requested neighbour",
			current: []mod.ModListEntry{
				{Name: "X", Enabled: true}, {Name: "A"}, {Name: "Y", Enabled: true}, {Name: "B"}, {Name: "Z"},
			},
			requested: []dto.ModListEntryResult{{ModName: "B", Enabled: true}, {ModName: "A"}},
			want: []dto.ModListEntryResult{
				{ModName: "X", Enabled: true}, {ModName: "B", Enabled: true}, {ModName: "Z"}, {ModName: "A"}, {ModName: "Y", Enabled: true},
			},
		},
		{
			name: "collapsed separator children stay after the row they followed",
			current: []mod.ModListEntry{
				{Name: "P1", Enabled: true}, {Name: "H1", Enabled: true}, {Name: "H2"}, {Name: "P2"}, {Name: "P3", Enabled: true},
			},
			requested: []dto.ModListEntryResult{{ModName: "P3", Enabled: true}, {ModName: "P1", Enabled: true}, {ModName: "P2"}},
			want: []dto.ModListEntryResult{
				{ModName: "P3", Enabled: true}, {ModName: "P1", Enabled: true}, {ModName: "H1", Enabled: true}, {ModName: "H2"}, {ModName: "P2"},
			},
		},
		{
			name:      "new requested entries keep request order",
			current:   []mod.ModListEntry{{Name: "Old"}},
			requested: []dto.ModListEntryResult{{ModName: "New"}, {ModName: "Old", Enabled: true}},
			want:      []dto.ModListEntryResult{{ModName: "New"}, {ModName: "Old", Enabled: true}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newProfileDaemon(t, "Default")
			for _, e := range tc.current {
				makeModFolders(t, e.Name)
			}
			for _, e := range tc.requested {
				makeModFolders(t, e.ModName)
			}
			saveCurrentModList(t, d, "Default", tc.current)
			if err := d.SetModList("skyrimse", "Default", tc.requested); err != nil {
				t.Fatalf("SetModList: %v", err)
			}
			if got := modListEntries(t, d, "Default"); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("merged modlist = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestSetModListValidatesNames(t *testing.T) {
	for _, name := range []string{"../escape", "a/b", "..", ".hidden", "Downloads", "overwrite", ""} {
		t.Run(name, func(t *testing.T) {
			d := newProfileDaemon(t, "Default")
			makeModFolders(t, "Keep")
			current := []mod.ModListEntry{{Name: "Keep", Enabled: true}}
			saveCurrentModList(t, d, "Default", current)
			err := d.SetModList("skyrimse", "Default", []dto.ModListEntryResult{{ModName: "Keep"}, {ModName: name, Enabled: true}})
			var invalid *download.InvalidTargetModError
			if !errors.As(err, &invalid) {
				t.Fatalf("SetModList error = %v, want InvalidTargetModError", err)
			}
			if got := modListEntries(t, d, "Default"); !reflect.DeepEqual(got, []dto.ModListEntryResult{{ModName: "Keep", Enabled: true}}) {
				t.Errorf("modlist after refused SetModList = %+v", got)
			}
		})
	}
}

func TestSetModListDropsReservedEntriesAndNeverTouchesTheDownloadsIndex(t *testing.T) {
	d := newProfileDaemon(t, "Default")
	makeModFolders(t, "A", ".hidden")
	downloadsIndex := filepath.Join(config.DownloadsDir("skyrimse"), "metadata.yaml")
	writeFixture(t, downloadsIndex)
	before, err := os.ReadFile(downloadsIndex)
	if err != nil {
		t.Fatal(err)
	}
	saveCurrentModList(t, d, "Default", []mod.ModListEntry{
		{Name: "Downloads", Enabled: true}, {Name: ".hidden", Enabled: true}, {Name: "A"},
	})

	if err := d.SetModList("skyrimse", "Default", []dto.ModListEntryResult{{ModName: "A", Enabled: true}}); err != nil {
		t.Fatalf("SetModList: %v", err)
	}
	if got := modListEntries(t, d, "Default"); !reflect.DeepEqual(got, []dto.ModListEntryResult{{ModName: "A", Enabled: true}}) {
		t.Errorf("modlist = %+v, want only A", got)
	}
	after, err := os.ReadFile(downloadsIndex)
	if err != nil || string(after) != string(before) {
		t.Errorf("downloads index changed: %q (%v), want %q", after, err, before)
	}
	if _, err := os.Stat(filepath.Join(config.ModsDir("skyrimse"), ".hidden", "metadata.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".hidden metadata.yaml created by SetModList: %v", err)
	}
	if meta, err := download.LoadModMetadata(filepath.Join(config.ModsDir("skyrimse"), "A")); err != nil || meta.Folder != "A" || meta.TrueIndex == "" || !meta.Enabled {
		t.Errorf("A metadata = %+v (%v), want minimal enabled metadata with a true_index", meta, err)
	}
}

func TestRenameModConcurrentWithSetModListKeepsTheRenamedEntry(t *testing.T) {
	d := newProfileDaemon(t, "Default")
	makeModFolders(t, "Base")
	for i := 0; i < concurrencyIterations; i++ {
		oldName, newName := fmt.Sprintf("Old%03d", i), fmt.Sprintf("New%03d", i)
		makeModFolders(t, oldName)
		if err := d.ensureInModList("skyrimse", oldName); err != nil {
			t.Fatal(err)
		}
		var renameErr, setErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			renameErr = d.RenameMod("skyrimse", oldName, newName)
		}()
		go func() {
			defer wg.Done()
			setErr = d.SetModList("skyrimse", "Default", []dto.ModListEntryResult{{ModName: "Base", Enabled: true}})
		}()
		wg.Wait()
		if renameErr != nil || setErr != nil {
			t.Fatalf("iteration %d: RenameMod = %v, SetModList = %v", i, renameErr, setErr)
		}
		byName, order := modListNames(t, d, "Default")
		if _, ok := byName[newName]; !ok {
			t.Fatalf("iteration %d lost the renamed mod %s: %v", i, newName, order)
		}
		if _, ok := byName[oldName]; ok {
			t.Fatalf("iteration %d kept the stale name %s: %v", i, oldName, order)
		}
	}
}

func TestRenameModValidatesTheNewName(t *testing.T) {
	for _, name := range []string{"../escape", ".hidden", "Downloads", "Overwrite"} {
		t.Run(name, func(t *testing.T) {
			d := newProfileDaemon(t, "Default")
			makeModFolders(t, "Mod")
			var invalid *download.InvalidTargetModError
			if err := d.RenameMod("skyrimse", "Mod", name); !errors.As(err, &invalid) {
				t.Fatalf("RenameMod error = %v, want InvalidTargetModError", err)
			}
			if _, err := os.Stat(filepath.Join(config.ModsDir("skyrimse"), "Mod")); err != nil {
				t.Errorf("mod folder moved by a refused rename: %v", err)
			}
		})
	}
}

func TestModListMutationsReportUnlistableProfiles(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	d := newProfileDaemon(t, "Default")
	makeModFolders(t, "Mod")
	if err := d.ensureInModList("skyrimse", "Mod"); err != nil {
		t.Fatal(err)
	}
	profilesDir := config.ProfilesDir("skyrimse")
	if err := os.Chmod(profilesDir, 0311); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(profilesDir, 0755) })

	if err := d.ensureInModList("skyrimse", "Other"); err == nil {
		t.Error("ensureInModList succeeded although the profiles could not be listed")
	}
	if _, err := d.UninstallMod("skyrimse", "Mod", true); err == nil {
		t.Error("UninstallMod succeeded although the profiles could not be listed")
	}
	if err := d.RenameMod("skyrimse", "Mod", "Renamed"); err == nil {
		t.Error("RenameMod succeeded although the profiles could not be listed")
	}
	if err := os.Chmod(profilesDir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(config.ModsDir("skyrimse"), "Mod")); err != nil {
		t.Errorf("mod folder changed by refused mutations: %v", err)
	}
	if byName, _ := modListNames(t, d, "Default"); len(byName) != 1 {
		t.Errorf("modlist = %v, want only Mod", byName)
	}
}

func TestRegisterManualInstallRequiresARealDirectory(t *testing.T) {
	d := newProfileDaemon(t, "Default")
	modsDir := config.ModsDir("skyrimse")
	writeFixture(t, filepath.Join(modsDir, "PlainFile"))
	if err := os.Symlink(t.TempDir(), filepath.Join(modsDir, "Linked")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"PlainFile", "Linked"} {
		var invalid *download.InvalidTargetModError
		if _, err := d.RegisterManualInstall("skyrimse", name, ""); !errors.As(err, &invalid) {
			t.Errorf("RegisterManualInstall(%s) error = %v, want InvalidTargetModError", name, err)
		}
	}
	if byName, _ := modListNames(t, d, "Default"); len(byName) != 0 {
		t.Errorf("modlist after refused registrations = %v, want empty", byName)
	}
}

func TestEnsureInModListReportsUnwritableProfile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	d := newProfileDaemon(t, "Alpha", "Beta")
	readOnly := d.profileMgr.ProfileDir("skyrimse", "Alpha")
	if err := os.Chmod(readOnly, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0755) })

	if err := d.ensureInModList("skyrimse", "Added"); err == nil {
		t.Fatal("ensureInModList succeeded with a read-only profile")
	}
	if byName, _ := modListNames(t, d, "Beta"); len(byName) != 1 {
		t.Errorf("writable profile modlist = %v, want the added mod", byName)
	}
	if byName, _ := modListNames(t, d, "Alpha"); len(byName) != 0 {
		t.Errorf("read-only profile modlist = %v, want empty", byName)
	}

	writeZipFiles(t, filepath.Join(config.DownloadsDir("skyrimse"), "Late.zip"), map[string]string{"plugin.esp": "plugin"})
	_, _, err := d.StartInstall(dto.StartInstallRequest{GameID: "skyrimse", ArchiveRelPath: "Late.zip", Mode: dto.InstallAsNewMod})
	var registration *download.ModRegistrationError
	if !errors.As(err, &registration) || registration.Mod != "Late" {
		t.Fatalf("StartInstall error = %v, want ModRegistrationError for Late", err)
	}
	if _, err := os.Stat(filepath.Join(config.ModsDir("skyrimse"), "Late", "plugin.esp")); err != nil {
		t.Errorf("installed files missing after registration failure: %v", err)
	}

	makeModFolders(t, "Manual")
	if _, err := d.RegisterManualInstall("skyrimse", "Manual", ""); !errors.As(err, &registration) || registration.Mod != "Manual" {
		t.Errorf("RegisterManualInstall error = %v, want ModRegistrationError for Manual", err)
	}
}

func TestEnsureTTWModEnabledSkipsUnreadableModlists(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		"ttw": {InstallPath: t.TempDir(), DataSubpath: "Data"},
	})
	for _, name := range []string{"Locked", "Open"} {
		if _, err := d.CreateProfile("ttw", name); err != nil {
			t.Fatal(err)
		}
		p, _, err := d.profileMgr.Load("ttw", name)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.profileMgr.Save(p, []mod.ModListEntry{{Name: "Existing", Enabled: true}}); err != nil {
			t.Fatal(err)
		}
	}
	locked := filepath.Join(d.profileMgr.ProfileDir("ttw", "Locked"), "modlist.txt")
	before, err := os.ReadFile(locked)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0644) })

	d.svc.ttw.ensureTTWModEnabled("Tale of Two Wastelands")

	if err := os.Chmod(locked, 0644); err != nil {
		t.Fatal(err)
	}
	if after, err := os.ReadFile(locked); err != nil || string(after) != string(before) {
		t.Errorf("unreadable modlist rewritten: %q (%v), want %q", after, err, before)
	}
	_, entries, err := d.profileMgr.Load("ttw", "Open")
	if err != nil {
		t.Fatal(err)
	}
	want := []mod.ModListEntry{{Name: "Existing", Enabled: true}, {Name: "Tale of Two Wastelands", Enabled: true}}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("readable TTW modlist = %+v, want %+v", entries, want)
	}
}

func TestWriteTrueIndexesNeverTouchesReservedOrMissingMetadata(t *testing.T) {
	d := newProfileDaemon(t, "Default")
	downloadsIndex := filepath.Join(config.DownloadsDir("skyrimse"), "metadata.yaml")
	writeFixture(t, downloadsIndex)
	writeFixture(t, filepath.Join(config.ModsDir("skyrimse"), "Overwrite", "metadata.yaml"))
	makeModFolders(t, "NoMeta")
	before := snapshotTree(t, config.ModsDir("skyrimse"))

	d.writeTrueIndexes("skyrimse", []mod.ModListEntry{
		{Name: "Downloads"}, {Name: "Overwrite"}, {Name: "../skyrimse_Mods"}, {Name: ".hidden"}, {Name: "NoMeta"}, {Name: "Missing"},
	})
	after := snapshotTree(t, config.ModsDir("skyrimse"))
	created := filepath.ToSlash(filepath.Join("NoMeta", "metadata.yaml"))
	createdContent, ok := after[created]
	if !ok {
		t.Fatalf("writeTrueIndexes did not create minimal metadata for the real folder-only mod; tree %v", after)
	}
	delete(after, created)
	if !reflect.DeepEqual(after, before) {
		t.Errorf("writeTrueIndexes changed reserved, invalid or missing entries:\n got %v\nwant %v", after, before)
	}
	meta, err := download.LoadModMetadata(filepath.Join(config.ModsDir("skyrimse"), "NoMeta"))
	if err != nil || meta.Folder != "NoMeta" || meta.TrueIndex == "" || meta.Enabled {
		t.Errorf("created metadata = %+v (%v), content %v; want folder NoMeta with a true_index", meta, err, createdContent)
	}
	if _, err := os.Stat(filepath.Join(config.ModsDir("skyrimse"), "Missing")); !os.IsNotExist(err) {
		t.Errorf("writeTrueIndexes created a folder for a missing mod: %v", err)
	}
}

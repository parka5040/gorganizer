package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/smapi"
	"github.com/parka/gorganizer/internal/vfs"
)

const depsGame = "stardewvalley"

type fakeSMAPIMod struct {
	name    string
	nexusID int
	update  string
}

type fakeSMAPIIO struct {
	mu       sync.Mutex
	mods     map[string]fakeSMAPIMod
	status   int
	requests [][]string
	versions []string
}

// ServeHTTP answers a smapi.io mods lookup from the configured metadata, or with the configured error status.
func (f *fakeSMAPIIO) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var decoded struct {
		Mods []struct {
			ID               string `json:"id"`
			InstalledVersion string `json:"installedVersion"`
		} `json:"mods"`
		APIVersion string `json:"apiVersion"`
	}
	_ = json.Unmarshal(body, &decoded)
	var ids []string
	for _, mod := range decoded.Mods {
		ids = append(ids, mod.ID)
	}
	f.mu.Lock()
	f.requests = append(f.requests, ids)
	f.versions = append(f.versions, decoded.APIVersion)
	status := f.status
	mods := make(map[string]fakeSMAPIMod, len(f.mods))
	for id, mod := range f.mods {
		mods[id] = mod
	}
	f.mu.Unlock()
	if status != 0 {
		http.Error(w, "smapi.io is down", status)
		return
	}
	entries := []map[string]any{}
	for _, query := range decoded.Mods {
		entry := map[string]any{"id": query.ID, "metadata": map[string]any{"id": []string{}}, "errors": []string{}}
		if mod, ok := mods[strings.ToLower(query.ID)]; ok {
			entry["metadata"] = map[string]any{
				"id": []string{query.ID}, "name": mod.name, "nexusID": mod.nexusID,
				"main": map[string]any{"version": "9.0.0", "url": "https://www.nexusmods.com/stardewvalley/mods/1"},
			}
			if mod.update != "" && query.InstalledVersion != mod.update {
				entry["suggestedUpdate"] = map[string]any{"version": mod.update, "url": "https://example.test/" + query.ID}
			}
		}
		entries = append(entries, entry)
	}
	_ = json.NewEncoder(w).Encode(entries)
}

// set records metadata smapi.io returns for uniqueID.
func (f *fakeSMAPIIO) set(uniqueID string, mod fakeSMAPIMod) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mods[strings.ToLower(uniqueID)] = mod
}

// fail makes every later lookup answer with status, or succeed again when status is 0.
func (f *fakeSMAPIIO) fail(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
}

// apiVersions returns the loader version each lookup sent.
func (f *fakeSMAPIIO) apiVersions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.versions...)
}

// requestCount returns how many lookups smapi.io received.
func (f *fakeSMAPIIO) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// newDepsDaemon builds an isolated Stardew daemon whose smapi.io client talks to a fake server.
func newDepsDaemon(t *testing.T) (*Daemon, string, *fakeSMAPIIO) {
	t.Helper()
	install := newStardewInstall(t)
	d := newIsolatedDaemon(t, newStardewGames(install))
	api := &fakeSMAPIIO{mods: map[string]fakeSMAPIMod{}}
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	d.svc.modDeps.client = &smapi.Client{BaseURL: server.URL + "/api/", RouteVersion: "4.5.2", HTTP: server.Client(), Cache: smapi.OpenCache(t.TempDir())}
	return d, install, api
}

// writeDepMod writes a SMAPI code mod folder with its DLL under root, requiring deps.
func writeDepMod(t *testing.T, root, folder, uniqueID string, deps ...string) {
	t.Helper()
	writeFileContent(t, filepath.Join(root, folder, "manifest.json"), depManifest(folder, uniqueID, deps...))
	writeFileContent(t, filepath.Join(root, folder, "Mod.dll"), "dll")
}

// depManifest renders a SMAPI code mod manifest requiring deps.
func depManifest(name, uniqueID string, deps ...string) string {
	manifest := map[string]any{"Name": name, "Author": "test", "Version": "1.0.0", "UniqueID": uniqueID, "EntryDll": "Mod.dll", "UpdateKeys": []string{"Nexus:4242"}}
	if len(deps) > 0 {
		var list []map[string]any
		for _, dep := range deps {
			list = append(list, map[string]any{"UniqueID": dep})
		}
		manifest["Dependencies"] = list
	}
	data, _ := json.Marshal(manifest)
	return string(data)
}

// storeMod returns the store folder of modName for the Stardew test game.
func storeMod(modName string) string {
	return filepath.Join(config.ModsDir(depsGame), modName)
}

// enabled returns an enabled modlist entry.
func enabled(name string) dto.ModListEntryResult {
	return dto.ModListEntryResult{ModName: name, Enabled: true}
}

// disabled returns a disabled modlist entry.
func disabled(name string) dto.ModListEntryResult {
	return dto.ModListEntryResult{ModName: name}
}

// setOrderedModList replaces the profile's modlist with entries in order.
func setOrderedModList(t *testing.T, d *Daemon, profileName string, entries ...dto.ModListEntryResult) {
	t.Helper()
	if err := d.SetModList(depsGame, profileName, entries); err != nil {
		t.Fatalf("SetModList(%s): %v", profileName, err)
	}
}

// depReport fetches the dependency report of profileName without contacting smapi.io.
func depReport(t *testing.T, d *Daemon, profileName string) dto.ModDependencyReportResult {
	t.Helper()
	report, err := d.GetModDependencyReport(context.Background(), depsGame, profileName, false, false)
	if err != nil {
		t.Fatalf("GetModDependencyReport(%s): %v", profileName, err)
	}
	return report
}

// componentAt returns the report component deployed at folder, failing when absent.
func componentAt(t *testing.T, report dto.ModDependencyReportResult, folder string) dto.ModComponentResult {
	t.Helper()
	for _, component := range report.Components {
		if strings.EqualFold(component.Folder, folder) {
			return component
		}
	}
	t.Fatalf("no component at %q in %+v", folder, report.Components)
	return dto.ModComponentResult{}
}

// missingDep returns the report's missing dependency uniqueID, or false.
func missingDep(report dto.ModDependencyReportResult, uniqueID string) (dto.MissingDependencyResult, bool) {
	for _, missing := range report.Missing {
		if strings.EqualFold(missing.UniqueID, uniqueID) {
			return missing, true
		}
	}
	return dto.MissingDependencyResult{}, false
}

// issueKinds lists the issue kinds of a component.
func issueKinds(component dto.ModComponentResult) []dto.ModIssueKind {
	var kinds []dto.ModIssueKind
	for _, issue := range component.Issues {
		kinds = append(kinds, issue.Kind)
	}
	return kinds
}

// componentFolders lists the deployed folders of a report.
func componentFolders(report dto.ModDependencyReportResult) []string {
	var folders []string
	for _, component := range report.Components {
		folders = append(folders, component.Folder)
	}
	return folders
}

func TestProjectSMAPIReadsTheDeployFolderOrItsBackupWhenMounted(t *testing.T) {
	d, install, _ := newDepsDaemon(t)
	writeDepMod(t, filepath.Join(install, "Mods"), "LiveOnly", "Test.LiveOnly")
	writeDepMod(t, filepath.Join(install, "Mods.orig"), "BackupOnly", "Test.BackupOnly")

	report := depReport(t, d, "Default")
	if got := componentFolders(report); !reflect.DeepEqual(got, []string{"LiveOnly"}) {
		t.Fatalf("unmounted components = %v, want the live deploy folder", got)
	}
	if component := componentAt(t, report, "LiveOnly"); component.ProviderMod != "" {
		t.Errorf("base provider = %q, want empty", component.ProviderMod)
	}

	d.mu.RLock()
	mm := d.mountMgrs[depsGame]
	d.mu.RUnlock()
	mm.SetMountedForTesting(true)
	t.Cleanup(func() { mm.SetMountedForTesting(false) })
	report = depReport(t, d, "Default")
	if got := componentFolders(report); !reflect.DeepEqual(got, []string{"BackupOnly"}) {
		t.Fatalf("mounted components = %v, want only the backed-up original folder", got)
	}
}

func TestProjectSMAPIUsesTheDesiredOrderOfAMountedDirtyProfile(t *testing.T) {
	d, install, _ := newDepsDaemon(t)
	writeDepMod(t, filepath.Join(install, "Mods"), "ConsoleCommands", "SMAPI.ConsoleCommands")
	writeDepMod(t, storeMod("First"), "Shared", "Test.First")
	writeDepMod(t, storeMod("Second"), "Shared", "Test.Second")
	setOrderedModList(t, d, "Default", enabled("First"), enabled("Second"))
	if _, err := d.MountVFS(depsGame, "Default"); err != nil {
		t.Fatalf("MountVFS: %v", err)
	}
	t.Cleanup(func() { _ = d.UnmountVFS(depsGame) })

	shared := componentAt(t, depReport(t, d, "Default"), "Shared")
	if shared.UniqueID != "Test.Second" || shared.ProviderMod != "Second" {
		t.Fatalf("applied order winner = %+v, want Second", shared)
	}
	setOrderedModList(t, d, "Default", enabled("Second"), enabled("First"))
	d.mu.RLock()
	dirty := d.mountMgrs[depsGame].IsDirty()
	d.mu.RUnlock()
	if !dirty {
		t.Fatal("mount is not dirty after reordering")
	}
	report := depReport(t, d, "Default")
	shared = componentAt(t, report, "Shared")
	if shared.UniqueID != "Test.First" || shared.ProviderMod != "First" {
		t.Fatalf("desired order winner = %+v, want First", shared)
	}
	if kinds := issueKinds(shared); !reflect.DeepEqual(kinds, []dto.ModIssueKind{dto.ModIssueFolderCollision}) || !reflect.DeepEqual(shared.Issues[0].Providers, []string{"Second"}) {
		t.Fatalf("desired order issues = %+v, want a collision with Second", shared.Issues)
	}
	if got := componentFolders(report); !reflect.DeepEqual(got, []string{"ConsoleCommands", "Shared"}) {
		t.Fatalf("mounted components = %v, want the backed-up base plus Shared once", got)
	}
}

func TestProjectSMAPITreatsBundledBaseModsAsProviders(t *testing.T) {
	d, install, _ := newDepsDaemon(t)
	writeDepMod(t, filepath.Join(install, "Mods"), "ConsoleCommands", "SMAPI.ConsoleCommands")
	writeDepMod(t, storeMod("Needy"), "Needy", "Test.Needy", "SMAPI.ConsoleCommands")
	setOrderedModList(t, d, "Default", enabled("Needy"))

	report := depReport(t, d, "Default")
	if len(report.Missing) != 0 {
		t.Fatalf("missing = %+v, want the bundled base mod to satisfy Needy", report.Missing)
	}
	console := componentAt(t, report, "ConsoleCommands")
	if !console.Bundled || console.ProviderMod != "" || console.Kind != dto.ModComponentCode {
		t.Fatalf("ConsoleCommands = %+v, want a bundled base code mod", console)
	}
	if needy := componentAt(t, report, "Needy"); needy.Failed || len(needy.Issues) != 0 {
		t.Fatalf("Needy = %+v, want no issues", needy)
	}
}

func TestProjectSMAPIIgnoresManifestLessOverwriteContributions(t *testing.T) {
	d, _, _ := newDepsDaemon(t)
	writeDepMod(t, storeMod("Alpha"), "Alpha", "Test.Alpha")
	writeFileContent(t, filepath.Join(storeMod("Overwrite"), "alpha", "config.json"), `{"Enabled":true}`)
	writeDepMod(t, storeMod("Beta"), "Beta", "Test.Beta")
	writeDepMod(t, storeMod("Overwrite"), "beta", "Test.Beta")
	setOrderedModList(t, d, "Default", enabled("Alpha"), enabled("Beta"))

	report := depReport(t, d, "Default")
	alpha := componentAt(t, report, "Alpha")
	if alpha.UniqueID != "Test.Alpha" || alpha.ProviderMod != "Alpha" || len(alpha.Issues) != 0 || alpha.Failed {
		t.Fatalf("Alpha = %+v, want Alpha's manifest without competitors", alpha)
	}
	beta := componentAt(t, report, "beta")
	if beta.UniqueID != "Test.Beta" || beta.ProviderMod != "Overwrite" || len(beta.Issues) != 0 {
		t.Fatalf("beta = %+v, want the same-ID Overwrite copy to win without a collision", beta)
	}
	if len(report.Components) != 2 {
		t.Fatalf("components = %v, want Alpha and beta once each", componentFolders(report))
	}
}

func TestProjectSMAPISurfacesDisabledProviders(t *testing.T) {
	d, _, _ := newDepsDaemon(t)
	writeDepMod(t, storeMod("Needy"), "Needy", "Test.Needy", "Test.Core")
	writeDepMod(t, storeMod("Core"), "Core", "Test.Core")
	setOrderedModList(t, d, "Default", enabled("Needy"), disabled("Core"))

	report := depReport(t, d, "Default")
	missing, ok := missingDep(report, "Test.Core")
	if !ok || !reflect.DeepEqual(missing.DisabledProviders, []string{"Core"}) || !reflect.DeepEqual(missing.RequiredBy, []string{"Test.Needy"}) {
		t.Fatalf("missing = %+v, want Test.Core satisfiable by the disabled Core", report.Missing)
	}
	needy := componentAt(t, report, "Needy")
	if !needy.Failed || !reflect.DeepEqual(issueKinds(needy), []dto.ModIssueKind{dto.ModIssueDisabled}) {
		t.Fatalf("Needy = %+v, want a failed mod with a Disabled issue", needy)
	}
	if len(report.Components) != 1 {
		t.Fatalf("components = %v, want the disabled mod left out of the deployment", componentFolders(report))
	}
}

func TestProjectSMAPIReducesCaseVariantFolders(t *testing.T) {
	d, _, _ := newDepsDaemon(t)
	writeDepMod(t, storeMod("Upper"), "Shared", "Test.Upper")
	writeDepMod(t, storeMod("Lower"), "shared", "Test.Lower")
	writeDepMod(t, storeMod("SameA"), "Same", "Test.Same")
	writeDepMod(t, storeMod("SameB"), "SAME", "test.same")
	setOrderedModList(t, d, "Default", enabled("Upper"), enabled("Lower"), enabled("SameA"), enabled("SameB"))

	report := depReport(t, d, "Default")
	if len(report.Components) != 2 {
		t.Fatalf("components = %v, want one per case-insensitive folder", componentFolders(report))
	}
	shared := componentAt(t, report, "shared")
	if shared.UniqueID != "Test.Lower" || shared.ProviderMod != "Lower" {
		t.Fatalf("shared = %+v, want the later layer to win", shared)
	}
	if kinds := issueKinds(shared); !reflect.DeepEqual(kinds, []dto.ModIssueKind{dto.ModIssueFolderCollision}) || !reflect.DeepEqual(shared.Issues[0].Providers, []string{"Upper"}) {
		t.Fatalf("shared issues = %+v, want a collision with Upper", shared.Issues)
	}
	same := componentAt(t, report, "SAME")
	if same.ProviderMod != "SameB" || len(same.Issues) != 0 || same.Failed {
		t.Fatalf("SAME = %+v, want a same-ID case variant to replace silently", same)
	}
}

func TestProjectSMAPIReportsAndExcludesRootManifestMods(t *testing.T) {
	d, _, _ := newDepsDaemon(t)
	writeFileContent(t, filepath.Join(storeMod("Rooted"), "manifest.json"), depManifest("Rooted", "Test.Rooted"))
	writeDepMod(t, storeMod("Rooted"), "Inner", "Test.Inner")
	writeFileContent(t, filepath.Join(storeMod("RootedOff"), "manifest.json"), depManifest("RootedOff", "Test.RootedOff"))
	writeDepMod(t, storeMod("Needy"), "Needy", "Test.Needy", "Test.Inner")
	setOrderedModList(t, d, "Default", enabled("Rooted"), disabled("RootedOff"), enabled("Needy"))

	report := depReport(t, d, "Default")
	if !reflect.DeepEqual(report.RootManifestMods, []string{"Rooted", "RootedOff"}) {
		t.Fatalf("root manifest mods = %v, want Rooted and RootedOff", report.RootManifestMods)
	}
	if got := componentFolders(report); !reflect.DeepEqual(got, []string{"Needy"}) {
		t.Fatalf("components = %v, want the rooted mod excluded like deployment does", got)
	}
	if missing, ok := missingDep(report, "Test.Inner"); !ok || len(missing.DisabledProviders) != 0 {
		t.Fatalf("missing = %+v, want Test.Inner missing because its mod is not deployable", report.Missing)
	}
}

func TestProjectSMAPIChecksEntryDllsAndVersions(t *testing.T) {
	d, install, _ := newDepsDaemon(t)
	engine := &fakeLoaderEngine{status: smapi.Status{State: smapi.StateOK, Recorded: true, RecordedVersion: "4.1.0"}}
	d.svc.modLoader.engineFor = func(smapi.LoaderSpec) loaderEngine { return engine }
	writeFileContent(t, filepath.Join(install, "Stardew Valley.deps.json"), `{"targets":{".NETCoreApp,Version=v6.0":{"Stardew Valley/1.6.15":{}}}}`)
	writeFileContent(t, filepath.Join(storeMod("NoDll"), "NoDll", "manifest.json"), depManifest("NoDll", "Test.NoDll"))
	writeFileContent(t, filepath.Join(storeMod("CaseDll"), "CaseDll", "manifest.json"), depManifest("CaseDll", "Test.CaseDll"))
	writeFileContent(t, filepath.Join(storeMod("CaseDll"), "CaseDll", "MOD.DLL"), "dll")
	newer := `{"Name":"Newer","Author":"t","Version":"1.0.0","UniqueID":"Test.Newer","EntryDll":"Mod.dll","MinimumApiVersion":"4.5.0"}`
	writeFileContent(t, filepath.Join(storeMod("Newer"), "Newer", "manifest.json"), newer)
	writeFileContent(t, filepath.Join(storeMod("Newer"), "Newer", "Mod.dll"), "dll")
	setOrderedModList(t, d, "Default", enabled("NoDll"), enabled("CaseDll"), enabled("Newer"))

	report := depReport(t, d, "Default")
	if report.LoaderVersion != "4.1.0" || report.GameVersion != "1.6.15" {
		t.Fatalf("versions = %q, %q; want the recorded loader and deps game version", report.LoaderVersion, report.GameVersion)
	}
	if noDll := componentAt(t, report, "NoDll"); !noDll.Failed || !reflect.DeepEqual(issueKinds(noDll), []dto.ModIssueKind{dto.ModIssueInvalidManifest}) {
		t.Fatalf("NoDll = %+v, want an invalid manifest for the missing DLL", noDll)
	}
	if caseDll := componentAt(t, report, "CaseDll"); caseDll.Failed {
		t.Fatalf("CaseDll = %+v, want a case-insensitive DLL match", caseDll)
	}
	newerComponent := componentAt(t, report, "Newer")
	if !newerComponent.Failed || !reflect.DeepEqual(issueKinds(newerComponent), []dto.ModIssueKind{dto.ModIssueNeedsNewerLoader}) || newerComponent.Issues[0].FoundVersion != "4.1.0" {
		t.Fatalf("Newer = %+v, want a NeedsNewerLoader issue against 4.1.0", newerComponent)
	}
}

func TestGetModDependencyReportMergesSmapiIOMetadata(t *testing.T) {
	d, _, api := newDepsDaemon(t)
	api.set("Test.Core", fakeSMAPIMod{name: "Core Library", nexusID: 1348})
	api.set("Test.Needy", fakeSMAPIMod{name: "Needy", nexusID: 77, update: "2.0.0"})
	writeDepMod(t, storeMod("Needy"), "Needy", "Test.Needy", "Test.Core", "Test.Elsewhere")
	setOrderedModList(t, d, "Default", enabled("Needy"))

	offline := depReport(t, d, "Default")
	if api.requestCount() != 0 || offline.RemoteChecked {
		t.Fatalf("a report without refresh contacted smapi.io: %d requests, checked %t", api.requestCount(), offline.RemoteChecked)
	}
	if needy := componentAt(t, offline, "Needy"); needy.NexusID != 4242 {
		t.Fatalf("offline Needy nexus id = %d, want the update-key fallback 4242", needy.NexusID)
	}

	report, err := d.GetModDependencyReport(context.Background(), depsGame, "Default", true, false)
	if err != nil {
		t.Fatalf("GetModDependencyReport: %v", err)
	}
	if !report.RemoteChecked || report.RemoteError != "" || api.requestCount() != 1 {
		t.Fatalf("remote report checked=%t err=%q requests=%d, want one successful lookup", report.RemoteChecked, report.RemoteError, api.requestCount())
	}
	core, ok := missingDep(report, "Test.Core")
	if !ok || core.Name != "Core Library" || core.NexusID != 1348 || !core.Resolvable || core.URL != "https://www.nexusmods.com/stardewvalley/mods/1348" {
		t.Fatalf("Test.Core = %+v, want smapi.io metadata and a Nexus page", core)
	}
	if elsewhere, ok := missingDep(report, "Test.Elsewhere"); !ok || elsewhere.Resolvable || elsewhere.NexusID != 0 {
		t.Fatalf("Test.Elsewhere = %+v, want an unresolvable missing dependency", elsewhere)
	}
	needy := componentAt(t, report, "Needy")
	if needy.NexusID != 77 || needy.UpdateVersion != "2.0.0" || needy.UpdateURL != "https://example.test/Test.Needy" || needy.UpdateStale {
		t.Fatalf("Needy = %+v, want the smapi.io update suggestion", needy)
	}

	cached := depReport(t, d, "Default")
	if core, _ := missingDep(cached, "Test.Core"); core.Name != "Core Library" || api.requestCount() != 1 {
		t.Fatalf("cached report Test.Core = %+v after %d requests, want cached metadata without a lookup", core, api.requestCount())
	}

	api.fail(http.StatusServiceUnavailable)
	degraded, err := d.GetModDependencyReport(context.Background(), depsGame, "Default", true, true)
	if err != nil {
		t.Fatalf("GetModDependencyReport with smapi.io down: %v", err)
	}
	if degraded.RemoteChecked || !strings.Contains(degraded.RemoteError, "503") {
		t.Fatalf("degraded report checked=%t err=%q, want a non-fatal remote error", degraded.RemoteChecked, degraded.RemoteError)
	}
	if core, _ := missingDep(degraded, "Test.Core"); core.Name != "Core Library" || !core.Resolvable {
		t.Fatalf("degraded Test.Core = %+v, want the cached metadata", core)
	}
}

func TestModDependencyRPCsRefuseGamesWithoutSMAPI(t *testing.T) {
	d := newStardewDaemon(t)
	var unsupported *dto.ModDependenciesUnsupportedError
	if _, err := d.GetModDependencyReport(context.Background(), "skyrimse", "Default", false, false); !errors.As(err, &unsupported) || unsupported.GameID != "skyrimse" {
		t.Fatalf("GetModDependencyReport(skyrimse) error = %v, want ModDependenciesUnsupportedError", err)
	}
	if _, err := d.FetchModDependencies(context.Background(), "skyrimse", "Default", []string{"a"}); !errors.As(err, &unsupported) {
		t.Fatalf("FetchModDependencies(skyrimse) error = %v, want ModDependenciesUnsupportedError", err)
	}
	if _, err := d.AckDependencyEnable(context.Background(), "skyrimse", "dep-1", nil); !errors.As(err, &unsupported) {
		t.Fatalf("AckDependencyEnable(skyrimse) error = %v, want ModDependenciesUnsupportedError", err)
	}
	var unsafePath *UnsafePathError
	if _, err := d.GetModDependencyReport(context.Background(), depsGame, "../escape", false, false); !errors.As(err, &unsafePath) {
		t.Fatalf("GetModDependencyReport(../escape) error = %v, want UnsafePathError", err)
	}
	if _, err := os.Stat(dependencyRequestsPath("skyrimse")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused game got a dependency request file: %v", err)
	}
}

// TestProjectSMAPINeverCountsACrashedFarmAsTheVanillaBase locks that an unmounted game whose deploy folder still holds a farm, or awaits a Data recovery, projects from its backup and refuses without one.
func TestProjectSMAPINeverCountsACrashedFarmAsTheVanillaBase(t *testing.T) {
	t.Run("farm left on disk", func(t *testing.T) {
		d, install, _ := newDepsDaemon(t)
		writeDepMod(t, filepath.Join(install, "Mods"), "Farmed", "Test.Farmed")
		writeFileContent(t, filepath.Join(install, "Mods", vfs.SentinelFilename), "{}")
		writeDepMod(t, filepath.Join(install, "Mods.orig"), "BackupOnly", "Test.BackupOnly")
		if got := componentFolders(depReport(t, d, "Default")); !reflect.DeepEqual(got, []string{"BackupOnly"}) {
			t.Fatalf("components = %v, want the backup instead of the crashed farm", got)
		}
	})
	t.Run("data recovery pending", func(t *testing.T) {
		d, install, _ := newDepsDaemon(t)
		writeDepMod(t, filepath.Join(install, "Mods"), "Ambiguous", "Test.Ambiguous")
		writeDepMod(t, filepath.Join(install, "Mods.orig"), "BackupOnly", "Test.BackupOnly")
		dataPath, err := filepath.Abs(filepath.Join(install, "Mods"))
		if err != nil {
			t.Fatal(err)
		}
		d.pendingRecoveriesMu.Lock()
		d.pendingRecoveries[dataPath] = &dto.RecoveryPendingResult{GameID: depsGame, DataPath: dataPath, Reason: "ambiguous"}
		d.pendingRecoveriesMu.Unlock()
		if got := componentFolders(depReport(t, d, "Default")); !reflect.DeepEqual(got, []string{"BackupOnly"}) {
			t.Fatalf("components = %v, want the backup while the Data recovery is pending", got)
		}
	})
	t.Run("farm without backup", func(t *testing.T) {
		d, install, _ := newDepsDaemon(t)
		writeDepMod(t, filepath.Join(install, "Mods"), "Farmed", "Test.Farmed")
		writeFileContent(t, filepath.Join(install, "Mods", vfs.SentinelFilename), "{}")
		_, err := d.GetModDependencyReport(context.Background(), depsGame, "Default", false, false)
		if err == nil || strings.Contains(err.Error(), "recovery pending") || !strings.Contains(err.Error(), "gorganizerctl recover --game") {
			t.Fatalf("report over an unrecovered farm without a backup = %v, want the recover step, not a pending confirmation", err)
		}
		dataPath, err := filepath.Abs(filepath.Join(install, "Mods"))
		if err != nil {
			t.Fatal(err)
		}
		d.pendingRecoveriesMu.Lock()
		d.pendingRecoveries[dataPath] = &dto.RecoveryPendingResult{GameID: depsGame, DataPath: dataPath, Reason: "ambiguous"}
		d.pendingRecoveriesMu.Unlock()
		if _, err := d.GetModDependencyReport(context.Background(), depsGame, "Default", false, false); err == nil || !strings.Contains(err.Error(), "recovery pending") {
			t.Fatalf("report over a farm awaiting confirmation = %v, want a recovery-pending refusal", err)
		}
	})
}

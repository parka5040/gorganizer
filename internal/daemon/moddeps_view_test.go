package daemon

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/smapi"
	"github.com/parka/gorganizer/internal/vfs"
)

// symlinkFixture creates a symbolic link at link pointing to target, creating parent folders.
func symlinkFixture(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// viewTree lists every path of a view below dir with its type, recursing into directories.
func viewTree(t *testing.T, view fs.FS, dir string, out map[string]bool) {
	t.Helper()
	entries, err := fs.ReadDir(view, dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if dir != "." {
			name = dir + "/" + name
		}
		out[name] = entry.IsDir()
		if entry.IsDir() {
			viewTree(t, view, name, out)
		}
	}
}

// mergedTreeListing lists every path of a vfs.MergedTree the way the farm materializes it, with its type and real source.
func mergedTreeListing(tree *vfs.MergedTree, dir, display string, types map[string]bool, sources map[string]string) {
	children, _ := tree.Children(dir)
	for key, child := range children {
		vpath := key
		name := child.Name
		if dir != "" {
			vpath = dir + "/" + key
			name = display + "/" + child.Name
		}
		types[name] = child.IsDir
		if child.IsDir {
			mergedTreeListing(tree, vpath, name, types, sources)
			continue
		}
		sources[name], _ = tree.LookupFile(vpath)
	}
}

func TestFarmViewMatchesTheMergedTree(t *testing.T) {
	root := t.TempDir()
	base, first, second := filepath.Join(root, "base"), filepath.Join(root, "first"), filepath.Join(root, "second")
	for rel, content := range map[string]string{
		"A/x.txt": "base", "B": "base file", "Case/one.txt": "base", "metadata.yaml": "base keeps it",
	} {
		writeFileContent(t, filepath.Join(base, rel), content)
	}
	for rel, content := range map[string]string{
		"a/y.txt": "first", "B/inner.txt": "shadowed by the base file", "case/ONE.TXT": "first wins the file", "metadata.yaml": "skipped",
	} {
		writeFileContent(t, filepath.Join(first, rel), content)
	}
	for rel, content := range map[string]string{
		"A/X.txt": "second wins", "C/deep/z.txt": "second", ".gorganizer-root/root.txt": "skipped",
	} {
		writeFileContent(t, filepath.Join(second, rel), content)
	}
	layers := []projectionLayer{{root: base}, {root: first, provider: "First", mod: true}, {root: second, provider: "Second", mod: true}}
	view := newFarmView(depsGame, layers, "")
	tree := vfs.NewMergedTree()
	if err := tree.Build([]vfs.Layer{{Name: "__base__", RootPath: base, Enabled: true}, {Name: "First", RootPath: first, Enabled: true}, {Name: "Second", RootPath: second, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	wantTypes, wantSources := map[string]bool{}, map[string]string{}
	mergedTreeListing(tree, "", "", wantTypes, wantSources)
	gotTypes := map[string]bool{}
	viewTree(t, view, ".", gotTypes)
	if !reflect.DeepEqual(gotTypes, wantTypes) {
		t.Fatalf("view entries = %v, want the merged tree's %v", gotTypes, wantTypes)
	}
	for name, source := range wantSources {
		node, err := view.resolve(name)
		if err != nil || node.real != source {
			t.Errorf("view %s = %q (%v), want the merged tree's %q", name, node.real, err, source)
		}
	}
	if layer := view.layerOf("A/x.txt"); layer != 2 {
		t.Errorf("layerOf(A/x.txt) = %d, want the last layer", layer)
	}
}

func TestProjectSMAPIMergesAnOverwriteManifestWithTheModsDll(t *testing.T) {
	d, _, _ := newDepsDaemon(t)
	writeDepMod(t, storeMod("Foo"), "Foo", "Test.Foo")
	writeFileContent(t, filepath.Join(storeMod("Overwrite"), "Foo", "manifest.json"), depManifest("Foo edited", "Test.Foo"))
	writeDepMod(t, storeMod("Needy"), "Needy", "Test.Needy", "Test.Foo")
	setOrderedModList(t, d, "Default", enabled("Foo"), enabled("Needy"))

	report := depReport(t, d, "Default")
	foo := componentAt(t, report, "Foo")
	if foo.Failed || len(foo.Issues) != 0 || foo.ProviderMod != "Overwrite" || foo.Name != "Foo edited" {
		t.Fatalf("Foo = %+v, want the Overwrite manifest using the mod's DLL", foo)
	}
	if needy := componentAt(t, report, "Needy"); needy.Failed || len(needy.Issues) != 0 {
		t.Fatalf("Needy = %+v, want its dependency satisfied", needy)
	}
}

func TestProjectSMAPIReportsNoPhantomNestedComponents(t *testing.T) {
	for _, order := range [][]string{{"Flat", "Nested"}, {"Nested", "Flat"}} {
		t.Run(order[0]+" first", func(t *testing.T) {
			d, _, _ := newDepsDaemon(t)
			writeDepMod(t, storeMod("Flat"), "Foo", "Test.Foo")
			writeDepMod(t, filepath.Join(storeMod("Nested"), "Foo"), "Sub", "Test.Sub")
			setOrderedModList(t, d, "Default", enabled(order[0]), enabled(order[1]))

			report := depReport(t, d, "Default")
			if got := componentFolders(report); !reflect.DeepEqual(got, []string{"Foo"}) {
				t.Fatalf("components = %v, want only Foo as SMAPI loads it", got)
			}
			foo := componentAt(t, report, "Foo")
			if foo.UniqueID != "Test.Foo" || foo.ProviderMod != "Flat" || foo.Failed {
				t.Fatalf("Foo = %+v, want Flat's mod", foo)
			}
			if kinds := issueKinds(foo); !reflect.DeepEqual(kinds, []dto.ModIssueKind{dto.ModIssueFolderCollision}) || !reflect.DeepEqual(foo.Issues[0].Providers, []string{"Nested"}) || foo.Issues[0].Detail != "Test.Sub" {
				t.Fatalf("Foo issues = %+v, want the hidden nested manifest reported as a collision", foo.Issues)
			}
		})
	}
}

func TestProjectSMAPIFollowsSymlinkedMods(t *testing.T) {
	d, install, _ := newDepsDaemon(t)
	outside := t.TempDir()
	writeDepMod(t, outside, "LinkedMod", "Test.Linked")
	symlinkFixture(t, filepath.Join(outside, "LinkedMod"), filepath.Join(install, "Mods", "Linked"))
	symlinkFixture(t, filepath.Join(install, "Mods"), filepath.Join(install, "Mods", "Loop"))
	writeFileContent(t, filepath.Join(outside, "real.dll"), "dll")
	writeFileContent(t, filepath.Join(storeMod("DllLink"), "DllLink", "manifest.json"), depManifest("DllLink", "Test.DllLink"))
	symlinkFixture(t, filepath.Join(outside, "real.dll"), filepath.Join(storeMod("DllLink"), "DllLink", "Mod.dll"))
	writeDepMod(t, outside, "StoreTarget", "Test.StoreLinked")
	symlinkFixture(t, filepath.Join(outside, "StoreTarget"), filepath.Join(storeMod("Store"), "StoreLinked"))
	writeDepMod(t, storeMod("Needy"), "Needy", "Test.Needy", "Test.Linked", "Test.DllLink", "Test.StoreLinked")
	setOrderedModList(t, d, "Default", enabled("DllLink"), enabled("Store"), enabled("Needy"))

	type reportOutcome struct {
		report dto.ModDependencyReportResult
		err    error
	}
	done := make(chan reportOutcome, 1)
	go func() {
		report, err := d.GetModDependencyReport(context.Background(), depsGame, "Default", false, false)
		done <- reportOutcome{report: report, err: err}
	}()
	var report dto.ModDependencyReportResult
	select {
	case outcome := <-done:
		if outcome.err != nil {
			t.Fatalf("GetModDependencyReport: %v", outcome.err)
		}
		report = outcome.report
	case <-time.After(10 * time.Second):
		t.Fatal("the report did not finish over a symlink loop")
	}
	if len(report.Missing) != 0 {
		t.Fatalf("missing = %+v, want symlinked mods found like the deployed farm", report.Missing)
	}
	for folder, provider := range map[string]string{"Linked": "", "DllLink": "DllLink", "StoreLinked": "Store", "Needy": "Needy"} {
		component := componentAt(t, report, folder)
		if component.Failed || len(component.Issues) != 0 || component.ProviderMod != provider {
			t.Errorf("%s = %+v, want a healthy mod from %q", folder, component, provider)
		}
	}
	if got := componentFolders(report); len(got) != 4 {
		t.Fatalf("components = %v, want the loop skipped", got)
	}
	d.svc.modDeps.nexusFiles = func(string) nexusFileLister { return &fakeNexusFiles{} }
	results, err := d.FetchModDependencies(context.Background(), depsGame, "Default", []string{"Test.Linked"})
	if err != nil || len(results) != 1 || results[0].Outcome != dto.FetchOutcomeAlreadyPresent || results[0].Reason != fetchReasonProvided+": Linked" {
		t.Fatalf("FetchModDependencies = %+v, %v; want the symlinked base mod already present", results, err)
	}
}

func TestScanLayerProvidersChecksDllsThroughSymlinks(t *testing.T) {
	root := t.TempDir()
	writeFileContent(t, filepath.Join(root, "target", "Mod.dll"), "dll")
	writeFileContent(t, filepath.Join(root, "mod", "Foo", "manifest.json"), depManifest("Foo", "Test.Foo"))
	symlinkFixture(t, filepath.Join(root, "target", "Mod.dll"), filepath.Join(root, "mod", "Foo", "Mod.dll"))
	writeFileContent(t, filepath.Join(root, "mod", "Broken", "manifest.json"), depManifest("Broken", "Test.Broken"))
	symlinkFixture(t, filepath.Join(root, "missing.dll"), filepath.Join(root, "mod", "Broken", "Mod.dll"))
	writeFileContent(t, filepath.Join(root, "mod", "Foo", "assets", "sprite-\xff.png"), "not UTF-8")
	writeFileContent(t, filepath.Join(root, "mod", "Bytes-\xfe", "manifest.json"), depManifest("Bytes", "Test.Bytes"))
	writeFileContent(t, filepath.Join(root, "mod", "Bytes-\xfe", "Mod.dll"), "dll")
	providers := scanLayerProviders(depsGame, projectionLayer{root: filepath.Join(root, "mod"), provider: "Mod", mod: true}, &projectionMeta{})
	present := map[string]bool{}
	for _, provider := range providers {
		if provider.EntryDllPresent == nil {
			t.Fatalf("%s has no DLL verdict", provider.Folder)
		}
		present[provider.Folder] = *provider.EntryDllPresent
	}
	if want := map[string]bool{"Foo": true, "Broken": false, "Bytes-\xfe": true}; !reflect.DeepEqual(present, want) {
		t.Fatalf("DLL presence = %v, want %v", present, want)
	}
}

func TestIssueAndComponentKindMapsCoverEveryKind(t *testing.T) {
	issues := map[smapi.IssueKind]dto.ModIssueKind{
		smapi.IssueMissing:          dto.ModIssueMissing,
		smapi.IssueDisabled:         dto.ModIssueDisabled,
		smapi.IssueVersionTooLow:    dto.ModIssueVersionTooLow,
		smapi.IssueDuplicateID:      dto.ModIssueDuplicateID,
		smapi.IssueInvalidManifest:  dto.ModIssueInvalidManifest,
		smapi.IssueNeedsNewerLoader: dto.ModIssueNeedsNewerLoader,
		smapi.IssueNeedsNewerGame:   dto.ModIssueNeedsNewerGame,
		smapi.IssueCircular:         dto.ModIssueCircular,
		smapi.IssueFolderCollision:  dto.ModIssueFolderCollision,
		smapi.IssueDependencyFailed: dto.ModIssueDependencyFailed,
	}
	if !reflect.DeepEqual(issueKindResults, issues) {
		t.Fatalf("issueKindResults = %v, want %v", issueKindResults, issues)
	}
	for kind := smapi.IssueNone; kind <= smapi.IssueDependencyFailed+1; kind++ {
		want, ok := issues[kind]
		if !ok {
			want = dto.ModIssueUnspecified
		}
		if got := issueKindResult(kind); got != want {
			t.Errorf("issueKindResult(%d) = %d, want %d", kind, got, want)
		}
	}
	components := map[smapi.ModKind]dto.ModComponentKind{
		smapi.KindCode:        dto.ModComponentCode,
		smapi.KindContentPack: dto.ModComponentContentPack,
		smapi.KindInvalid:     dto.ModComponentInvalid,
	}
	if !reflect.DeepEqual(componentKindResults, components) {
		t.Fatalf("componentKindResults = %v, want %v", componentKindResults, components)
	}
	for kind := smapi.KindInvalid; kind <= smapi.KindContentPack+1; kind++ {
		want, ok := components[kind]
		if !ok {
			want = dto.ModComponentUnspecified
		}
		if got := componentKindResult(kind); got != want {
			t.Errorf("componentKindResult(%d) = %d, want %d", kind, got, want)
		}
	}
	var values []int
	for _, value := range issues {
		values = append(values, int(value))
	}
	sort.Ints(values)
	for i, value := range values {
		if value != i+1 {
			t.Fatalf("issue wire values = %v, want 1..%d without gaps", values, len(values))
		}
	}
}

func TestFarmViewResolvesRelativeSymlinksFromTheFarm(t *testing.T) {
	root := t.TempDir()
	farmRoot := filepath.Join(root, "game", "Mods")
	if err := os.MkdirAll(farmRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFileContent(t, filepath.Join(root, "game", "game-file.txt"), "game file")
	linker, shared := filepath.Join(root, "store", "Linker"), filepath.Join(root, "store", "Shared")
	writeFileContent(t, filepath.Join(linker, "Linker", "manifest.json"), depManifest("Linker", "Test.Linker"))
	symlinkFixture(t, "../Shared/Mod.dll", filepath.Join(linker, "Linker", "Mod.dll"))
	symlinkFixture(t, "../../game-file.txt", filepath.Join(linker, "Linker", "game.txt"))
	symlinkFixture(t, "..", filepath.Join(linker, "Linker", "self"))
	symlinkFixture(t, "ping", filepath.Join(linker, "Linker", "pong"))
	symlinkFixture(t, "pong", filepath.Join(linker, "Linker", "ping"))
	symlinkFixture(t, "../Shared", filepath.Join(linker, "Linker", "assets"))
	symlinkFixture(t, "../../Mods/Shared/Mod.dll", filepath.Join(linker, "Linker", "reentered.dll"))
	writeFileContent(t, filepath.Join(shared, "Shared", "Mod.dll"), "shared dll")
	layers := []projectionLayer{{root: farmRoot}, {root: linker, provider: "Linker", mod: true}, {root: shared, provider: "Shared", mod: true}}
	view := newFarmView(depsGame, layers, farmRoot)

	for name, want := range map[string]string{"Linker/Mod.dll": "shared dll", "Linker/game.txt": "game file", "Linker/assets/Mod.dll": "shared dll", "Linker/reentered.dll": "shared dll"} {
		data, err := fs.ReadFile(view, name)
		if err != nil || string(data) != want {
			t.Errorf("ReadFile(%s) = %q, %v; want %q resolved from the farm", name, data, err, want)
		}
	}
	if !entryDllInView(view, "Linker", "Mod.dll") {
		t.Error("the relative DLL link is missing from the projected farm")
	}
	if layer := view.layerOf("Linker/Mod.dll"); layer != 1 {
		t.Errorf("layerOf(Linker/Mod.dll) = %d, want the linking mod's layer", layer)
	}
	got := map[string]bool{}
	viewTree(t, view, "Linker", got)
	if _, looped := got["Linker/self"]; looped {
		t.Error("a link back to an ancestor was listed instead of skipped")
	}
	for _, name := range []string{"Linker/ping", "Linker/pong"} {
		info, err := fs.Stat(view, name)
		if err != nil || info.Mode()&fs.ModeIrregular == 0 {
			t.Errorf("Stat(%s) = %v, %v; want a broken link", name, info, err)
		}
	}
	folders, err := smapi.ScanFS(view)
	if err != nil {
		t.Fatalf("ScanFS over relative links: %v", err)
	}
	found := false
	for _, folder := range folders {
		if folder.RelPath == "Linker" && folder.Manifest != nil {
			found = true
		}
	}
	if !found {
		t.Fatalf("ScanFS folders = %+v, want the Linker mod", folders)
	}
}

// writeMutualLinks writes a pack whose two folders link to each other through relative symlinks under root.
func writeMutualLinks(t *testing.T, root string) {
	t.Helper()
	writeFileContent(t, filepath.Join(root, "Pack", "A", "readme.txt"), "a")
	writeFileContent(t, filepath.Join(root, "Pack", "B", "readme.txt"), "b")
	symlinkFixture(t, "../B", filepath.Join(root, "Pack", "A", "linkB"))
	symlinkFixture(t, "../A", filepath.Join(root, "Pack", "B", "linkA"))
	writeDepMod(t, root, "Real", "Test.Real")
}

// finishesWithin fails the test unless run returns within the bound.
func finishesWithin(t *testing.T, what string, bound time.Duration, run func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		run()
	}()
	select {
	case <-done:
	case <-time.After(bound):
		t.Fatalf("%s did not finish within %v over mutually linked folders", what, bound)
	}
}

func TestFarmViewScanStopsAtMutualRelativeLinks(t *testing.T) {
	t.Run("dependency report", func(t *testing.T) {
		d, _, _ := newDepsDaemon(t)
		writeMutualLinks(t, storeMod("Linked"))
		setOrderedModList(t, d, "Default", enabled("Linked"))
		var report dto.ModDependencyReportResult
		finishesWithin(t, "GetModDependencyReport", 10*time.Second, func() { report = depReport(t, d, "Default") })
		if component := componentAt(t, report, "Real"); component.UniqueID != "Test.Real" {
			t.Fatalf("Real = %+v, want the mod beside the linked pack", component)
		}
	})
	t.Run("layer scan without a farm root", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "mod")
		writeMutualLinks(t, root)
		var providers []smapi.Provider
		finishesWithin(t, "scanLayerProviders", 10*time.Second, func() {
			providers = scanLayerProviders(depsGame, projectionLayer{root: root, provider: "Mod", mod: true}, &projectionMeta{})
		})
		if len(providers) != 1 || providers[0].Manifest == nil || providers[0].Manifest.UniqueID != "Test.Real" {
			t.Fatalf("providers = %+v, want only the real mod", providers)
		}
	})
}

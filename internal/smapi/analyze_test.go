package smapi

import (
	"errors"
	"reflect"
	"testing"
)

// codeMod builds a valid code-mod manifest with the given dependencies.
func codeMod(id, version string, deps ...Dependency) *Manifest {
	return &Manifest{Name: id + " name", UniqueID: id, Version: MustParseVersion(version), EntryDll: "Mod.dll", Dependencies: deps}
}

// contentPack builds a valid content pack manifest for parent with an optional minimum version.
func contentPack(id, version, parent, parentMin string) *Manifest {
	m := &Manifest{Name: id + " name", UniqueID: id, Version: MustParseVersion(version), ContentPackFor: &ContentPackFor{UniqueID: parent}}
	if parentMin != "" {
		m.ContentPackFor.MinimumVersion = versionPtr(parentMin)
	}
	return m
}

// requires builds a required dependency with an optional minimum version.
func requires(id, minimum string) Dependency {
	dep := Dependency{UniqueID: id, IsRequired: true}
	if minimum != "" {
		dep.MinimumVersion = versionPtr(minimum)
	}
	return dep
}

// optional builds an optional dependency with an optional minimum version.
func optional(id, minimum string) Dependency {
	dep := requires(id, minimum)
	dep.IsRequired = false
	return dep
}

// activeFolder builds an active projected folder whose provider is named after the folder.
func activeFolder(folder string, m *Manifest) ProjectedFolder {
	return ProjectedFolder{Provider: Provider{Folder: folder, Provider: folder + " (mod)", Manifest: m}}
}

// componentByFolder returns the component result for folder or fails the test.
func componentByFolder(t *testing.T, report Report, folder string) ComponentResult {
	t.Helper()
	for _, component := range report.Components {
		if component.Folder == folder {
			return component
		}
	}
	t.Fatalf("no component for folder %q in %#v", folder, report.Components)
	return ComponentResult{}
}

// assertIssues checks that a component has exactly the expected issues and failure state.
func assertIssues(t *testing.T, component ComponentResult, failed bool, want ...Issue) {
	t.Helper()
	if component.Failed != failed {
		t.Fatalf("component %q Failed = %v, want %v (issues %#v)", component.Folder, component.Failed, failed, component.Issues)
	}
	if len(want) == 0 {
		want = nil
	}
	if !reflect.DeepEqual(component.Issues, want) {
		t.Fatalf("component %q issues = %#v, want %#v", component.Folder, component.Issues, want)
	}
}

// TestAnalyzeCleanSet verifies a consistent mod set produces no issues and sorted component results.
func TestAnalyzeCleanSet(t *testing.T) {
	report := Analyze(AnalyzeInput{
		Active: []ProjectedFolder{
			activeFolder("[CP] Pack", contentPack("author.pack", "1.0.0", "Pathoschild.ContentPatcher", "2.0")),
			activeFolder("ContentPatcher", codeMod("Pathoschild.ContentPatcher", "2.9.1")),
			activeFolder("aaa", codeMod("author.aaa", "1.0", requires("Pathoschild.ContentPatcher", "2.9.1"), optional("not.installed", ""))),
		},
		LoaderVersion: versionPtr("4.5.2"),
		GameVersion:   versionPtr("1.6.15.24356"),
	})
	want := []ComponentResult{
		{Folder: "[CP] Pack", Provider: "[CP] Pack (mod)", UniqueID: "author.pack", Name: "author.pack name", Version: "1.0.0", Kind: KindContentPack},
		{Folder: "aaa", Provider: "aaa (mod)", UniqueID: "author.aaa", Name: "author.aaa name", Version: "1.0.0", Kind: KindCode},
		{Folder: "ContentPatcher", Provider: "ContentPatcher (mod)", UniqueID: "Pathoschild.ContentPatcher", Name: "Pathoschild.ContentPatcher name", Version: "2.9.1", Kind: KindCode},
	}
	if !reflect.DeepEqual(report.Components, want) {
		t.Fatalf("Components = %#v, want %#v", report.Components, want)
	}
	if len(report.Missing) != 0 {
		t.Fatalf("Missing = %#v, want none", report.Missing)
	}
}

// TestAnalyzeMissing verifies an absent required dependency fails the mod and is aggregated.
func TestAnalyzeMissing(t *testing.T) {
	report := Analyze(AnalyzeInput{Active: []ProjectedFolder{activeFolder("A", codeMod("a.mod", "1.0", requires("x.lib", "1.2")))}})
	assertIssues(t, componentByFolder(t, report, "A"), true, Issue{Kind: IssueMissing, TargetID: "x.lib", RequiredVersion: "1.2.0"})
	want := []MissingDependency{{UniqueID: "x.lib", MinimumVersion: "1.2.0", RequiredBy: []string{"a.mod"}}}
	if !reflect.DeepEqual(report.Missing, want) {
		t.Fatalf("Missing = %#v, want %#v", report.Missing, want)
	}
}

// TestAnalyzeDisabled verifies a dependency provided only by a disabled mod is reported as disabled.
func TestAnalyzeDisabled(t *testing.T) {
	report := Analyze(AnalyzeInput{
		Active: []ProjectedFolder{activeFolder("A", codeMod("a.mod", "1.0", requires("X.Lib", "")))},
		Disabled: []Provider{
			{Folder: "XLib", Provider: "X Library", Manifest: codeMod("x.lib", "1.0")},
			{Folder: "XLib", Provider: "X Library (old)", Manifest: codeMod("X.LIB", "0.9")},
			{Folder: "Other", Provider: "Other", Manifest: codeMod("other.mod", "1.0")},
			{Folder: "Broken", Provider: "Broken", ParseErr: errors.New("bad json")},
		},
	})
	assertIssues(t, componentByFolder(t, report, "A"), true, Issue{Kind: IssueDisabled, TargetID: "X.Lib", Providers: []string{"X Library", "X Library (old)"}})
	want := []MissingDependency{{UniqueID: "X.Lib", RequiredBy: []string{"a.mod"}, DisabledProviders: []string{"X Library", "X Library (old)"}}}
	if !reflect.DeepEqual(report.Missing, want) {
		t.Fatalf("Missing = %#v, want %#v", report.Missing, want)
	}
}

// TestAnalyzeDisabledUnsuitable verifies disabled providers that could not satisfy a dependency leave it missing.
func TestAnalyzeDisabledUnsuitable(t *testing.T) {
	invalid := codeMod("x.lib", "3.0")
	invalid.Name = ""
	noDll := codeMod("x.lib", "3.0")
	absent := false
	present := true
	report := Analyze(AnalyzeInput{
		Active: []ProjectedFolder{
			activeFolder("A", codeMod("a.mod", "1.0", requires("x.lib", "2.0"))),
			activeFolder("B", codeMod("b.mod", "1.0", requires("y.lib", "1.0"), optional("x.lib", ""))),
			activeFolder("C", contentPack("c.pack", "1.0", "y.lib", "")),
		},
		Disabled: []Provider{
			{Folder: "XOld", Provider: "X Old", Manifest: codeMod("x.lib", "1.5")},
			{Folder: "XInvalid", Provider: "X Invalid", Manifest: invalid},
			{Folder: "XNoDll", Provider: "X No DLL", Manifest: noDll, EntryDllPresent: &absent},
			{Folder: "XBroken", Provider: "X Broken", Manifest: codeMod("x.lib", "3.0"), ParseErr: errors.New("bad")},
			{Folder: "YOld", Provider: "Y Old", Manifest: codeMod("y.lib", "0.5")},
			{Folder: "YNew", Provider: "Y New", Manifest: codeMod("y.lib", "1.0"), EntryDllPresent: &present},
		},
	})
	assertIssues(t, componentByFolder(t, report, "A"), true, Issue{Kind: IssueMissing, TargetID: "x.lib", RequiredVersion: "2.0.0", Detail: "disabled providers that can't satisfy it: X Broken, X Invalid, X No DLL, X Old"})
	assertIssues(t, componentByFolder(t, report, "B"), true, Issue{Kind: IssueDisabled, TargetID: "y.lib", RequiredVersion: "1.0.0", Providers: []string{"Y New"}})
	assertIssues(t, componentByFolder(t, report, "C"), true, Issue{Kind: IssueDisabled, TargetID: "y.lib", Providers: []string{"Y New", "Y Old"}})
	want := []MissingDependency{
		{UniqueID: "x.lib", MinimumVersion: "2.0.0", RequiredBy: []string{"a.mod"}},
		{UniqueID: "y.lib", MinimumVersion: "1.0.0", RequiredBy: []string{"b.mod", "c.pack"}, DisabledProviders: []string{"Y New"}},
	}
	if !reflect.DeepEqual(report.Missing, want) {
		t.Fatalf("Missing = %#v, want %#v", report.Missing, want)
	}
}

// TestAnalyzeDisabledUsesMergedMinimum verifies suitability is judged against the highest minimum across merged requirements.
func TestAnalyzeDisabledUsesMergedMinimum(t *testing.T) {
	pack := contentPack("a.pack", "1.0", "x.lib", "2.0")
	pack.Dependencies = []Dependency{requires("X.Lib", "1.0")}
	report := Analyze(AnalyzeInput{
		Active:   []ProjectedFolder{activeFolder("A", pack)},
		Disabled: []Provider{{Folder: "X", Provider: "X Lib", Manifest: codeMod("x.lib", "1.5")}},
	})
	assertIssues(t, componentByFolder(t, report, "A"), true, Issue{Kind: IssueMissing, TargetID: "X.Lib", RequiredVersion: "2.0.0", Detail: "disabled providers that can't satisfy it: X Lib"})
}

// TestAnalyzeEntryDllMissing verifies a code mod whose checked DLL is absent fails after the manifest checks, like ValidateManifests.
func TestAnalyzeEntryDllMissing(t *testing.T) {
	absent := false
	present := true
	withPresence := func(folder string, m *Manifest, value *bool) ProjectedFolder {
		projected := activeFolder(folder, m)
		projected.EntryDllPresent = value
		return projected
	}
	needsLoader := codeMod("a.loader", "1.0")
	needsLoader.MinimumApiVersion = versionPtr("9.0")
	invalid := codeMod("a.invalid", "1.0")
	invalid.Name = ""
	report := Analyze(AnalyzeInput{
		Active: []ProjectedFolder{
			withPresence("Missing", codeMod("a.missing", "1.0"), &absent),
			withPresence("Present", codeMod("a.present", "1.0"), &present),
			withPresence("Unchecked", codeMod("a.unchecked", "1.0"), nil),
			withPresence("Pack", contentPack("a.pack", "1.0", "a.present", ""), &absent),
			withPresence("Loader", needsLoader, &absent),
			withPresence("Invalid", invalid, &absent),
			activeFolder("Dependent", codeMod("a.dependent", "1.0", requires("a.missing", ""))),
		},
		LoaderVersion: versionPtr("4.5.2"),
	})
	assertIssues(t, componentByFolder(t, report, "Missing"), true, Issue{Kind: IssueInvalidManifest, Detail: "its DLL 'Mod.dll' doesn't exist."})
	assertIssues(t, componentByFolder(t, report, "Present"), false)
	assertIssues(t, componentByFolder(t, report, "Unchecked"), false)
	assertIssues(t, componentByFolder(t, report, "Pack"), false)
	assertIssues(t, componentByFolder(t, report, "Loader"), true, Issue{Kind: IssueNeedsNewerLoader, RequiredVersion: "9.0.0", FoundVersion: "4.5.2"})
	assertIssues(t, componentByFolder(t, report, "Invalid"), true, Issue{Kind: IssueInvalidManifest, Detail: "manifest is missing required fields (Name)."})
	assertIssues(t, componentByFolder(t, report, "Dependent"), true, Issue{Kind: IssueDependencyFailed, TargetID: "a.missing", Providers: []string{"Missing (mod)"}})
}

// TestAnalyzeVersionTooLow verifies an installed dependency older than the minimum fails the mod.
func TestAnalyzeVersionTooLow(t *testing.T) {
	report := Analyze(AnalyzeInput{Active: []ProjectedFolder{
		activeFolder("A", codeMod("a.mod", "1.0", requires("b.lib", "2.0"))),
		activeFolder("B", codeMod("b.lib", "1.5")),
	}})
	assertIssues(t, componentByFolder(t, report, "A"), true, Issue{Kind: IssueVersionTooLow, TargetID: "b.lib", RequiredVersion: "2.0.0", FoundVersion: "1.5.0", Providers: []string{"B (mod)"}})
	assertIssues(t, componentByFolder(t, report, "B"), false)
	if len(report.Missing) != 0 {
		t.Fatalf("Missing = %#v, want none", report.Missing)
	}
}

// TestAnalyzeOptionalTooOld verifies SMAPI also fails a mod whose installed optional dependency is too old.
func TestAnalyzeOptionalTooOld(t *testing.T) {
	report := Analyze(AnalyzeInput{Active: []ProjectedFolder{
		activeFolder("A", codeMod("a.mod", "1.0", optional("b.lib", "2.0"))),
		activeFolder("B", codeMod("b.lib", "1.5")),
	}})
	assertIssues(t, componentByFolder(t, report, "A"), true, Issue{Kind: IssueVersionTooLow, TargetID: "b.lib", RequiredVersion: "2.0.0", FoundVersion: "1.5.0", Providers: []string{"B (mod)"}})
}

// TestAnalyzeDuplicateID verifies every copy of a duplicated mod ID fails with the other copies listed.
func TestAnalyzeDuplicateID(t *testing.T) {
	report := Analyze(AnalyzeInput{Active: []ProjectedFolder{
		activeFolder("Second", codeMod("A.Mod", "1.1")),
		activeFolder("First", codeMod("a.mod", "1.0")),
		activeFolder("Other", codeMod("other.mod", "1.0", requires("a.mod", ""))),
	}})
	assertIssues(t, componentByFolder(t, report, "First"), true, Issue{Kind: IssueDuplicateID, TargetID: "a.mod", Providers: []string{"Second (mod)"}, Detail: "First, Second"})
	assertIssues(t, componentByFolder(t, report, "Second"), true, Issue{Kind: IssueDuplicateID, TargetID: "A.Mod", Providers: []string{"First (mod)"}, Detail: "First, Second"})
	assertIssues(t, componentByFolder(t, report, "Other"), true, Issue{Kind: IssueDependencyFailed, TargetID: "a.mod", Providers: []string{"First (mod)"}})
}

// TestAnalyzeDuplicateFailureRules verifies SMAPI's rule for which already-failed copies are marked duplicate.
func TestAnalyzeDuplicateFailureRules(t *testing.T) {
	invalid := codeMod("dup.mod", "1.0")
	invalid.Name = ""
	needsLoader := codeMod("dup.mod", "1.0")
	needsLoader.MinimumApiVersion = versionPtr("5.0")
	report := Analyze(AnalyzeInput{
		Active: []ProjectedFolder{
			activeFolder("A", codeMod("dup.mod", "1.0")),
			activeFolder("B", invalid),
			activeFolder("C", needsLoader),
			{Provider: Provider{Folder: "D", Provider: "D (mod)", ParseErr: errors.New("broken")}},
		},
		LoaderVersion: versionPtr("4.5.2"),
	})
	assertIssues(t, componentByFolder(t, report, "A"), true, Issue{Kind: IssueDuplicateID, TargetID: "dup.mod", Providers: []string{"B (mod)", "C (mod)"}, Detail: "A, B, C"})
	assertIssues(t, componentByFolder(t, report, "B"), true,
		Issue{Kind: IssueDuplicateID, TargetID: "dup.mod", Providers: []string{"A (mod)", "C (mod)"}, Detail: "A, B, C"},
		Issue{Kind: IssueInvalidManifest, Detail: "manifest is missing required fields (Name)."},
	)
	assertIssues(t, componentByFolder(t, report, "C"), true, Issue{Kind: IssueNeedsNewerLoader, RequiredVersion: "5.0.0", FoundVersion: "4.5.2"})
	assertIssues(t, componentByFolder(t, report, "D"), true, Issue{Kind: IssueInvalidManifest, Detail: "broken"})
}

// TestAnalyzeInvalidManifest verifies parse failures, missing manifests and validation failures are invalid manifests.
func TestAnalyzeInvalidManifest(t *testing.T) {
	noName := codeMod("a.noname", "1.0")
	noName.Name = ""
	both := codeMod("a.both", "1.0")
	both.ContentPackFor = &ContentPackFor{UniqueID: "x.y"}
	report := Analyze(AnalyzeInput{Active: []ProjectedFolder{
		{Provider: Provider{Folder: "Parse", Provider: "Parse (mod)", ParseErr: errors.New("invalid SMAPI manifest: bad")}},
		{Provider: Provider{Folder: "Nil", Provider: "Nil (mod)"}},
		activeFolder("NoName", noName),
		activeFolder("Both", both),
	}})
	assertIssues(t, componentByFolder(t, report, "Parse"), true, Issue{Kind: IssueInvalidManifest, Detail: "invalid SMAPI manifest: bad"})
	assertIssues(t, componentByFolder(t, report, "Nil"), true, Issue{Kind: IssueInvalidManifest, Detail: "manifest is missing."})
	assertIssues(t, componentByFolder(t, report, "NoName"), true, Issue{Kind: IssueInvalidManifest, Detail: "manifest is missing required fields (Name)."})
	assertIssues(t, componentByFolder(t, report, "Both"), true, Issue{Kind: IssueInvalidManifest, Detail: "manifest sets both EntryDll and ContentPackFor, which are mutually exclusive."})
	if got := componentByFolder(t, report, "Both").Kind; got != KindInvalid {
		t.Fatalf("Both Kind = %v, want KindInvalid", got)
	}
}

// TestAnalyzeNeedsNewerLoaderAndGame verifies SMAPI and game minimum versions fail mods only when versions are known.
func TestAnalyzeNeedsNewerLoaderAndGame(t *testing.T) {
	loaderMod := codeMod("a.loader", "1.0")
	loaderMod.MinimumApiVersion = versionPtr("4.6")
	gameMod := codeMod("a.game", "1.0")
	gameMod.MinimumGameVersion = versionPtr("1.7")
	bothMod := codeMod("a.both", "1.0")
	bothMod.MinimumApiVersion = versionPtr("4.6")
	bothMod.MinimumGameVersion = versionPtr("1.7")
	bothMod.Name = ""
	okMod := codeMod("a.ok", "1.0")
	okMod.MinimumApiVersion = versionPtr("4.5.2")
	okMod.MinimumGameVersion = versionPtr("1.6.15")
	active := []ProjectedFolder{activeFolder("Loader", loaderMod), activeFolder("Game", gameMod), activeFolder("Both", bothMod), activeFolder("Ok", okMod)}

	report := Analyze(AnalyzeInput{Active: active, LoaderVersion: versionPtr("4.5.2"), GameVersion: versionPtr("1.6.15.24356")})
	assertIssues(t, componentByFolder(t, report, "Loader"), true, Issue{Kind: IssueNeedsNewerLoader, RequiredVersion: "4.6.0", FoundVersion: "4.5.2"})
	assertIssues(t, componentByFolder(t, report, "Game"), true, Issue{Kind: IssueNeedsNewerGame, RequiredVersion: "1.7.0", FoundVersion: "1.6.15.24356"})
	assertIssues(t, componentByFolder(t, report, "Both"), true, Issue{Kind: IssueNeedsNewerLoader, RequiredVersion: "4.6.0", FoundVersion: "4.5.2"})
	assertIssues(t, componentByFolder(t, report, "Ok"), false)

	unknown := Analyze(AnalyzeInput{Active: active})
	assertIssues(t, componentByFolder(t, unknown, "Loader"), false)
	assertIssues(t, componentByFolder(t, unknown, "Game"), false)
	assertIssues(t, componentByFolder(t, unknown, "Both"), true, Issue{Kind: IssueInvalidManifest, Detail: "manifest is missing required fields (Name)."})
	assertIssues(t, componentByFolder(t, unknown, "Ok"), false)
}

// TestAnalyzeCircular verifies a dependency loop fails the mod that closes it and the mod that depends on it.
func TestAnalyzeCircular(t *testing.T) {
	report := Analyze(AnalyzeInput{Active: []ProjectedFolder{
		activeFolder("B", codeMod("b.mod", "1.0", requires("a.mod", ""))),
		activeFolder("A", codeMod("a.mod", "1.0", requires("b.mod", ""))),
		activeFolder("Self", codeMod("self.mod", "1.0", optional("SELF.mod", ""))),
	}})
	assertIssues(t, componentByFolder(t, report, "A"), true, Issue{Kind: IssueDependencyFailed, TargetID: "b.mod", Providers: []string{"B (mod)"}})
	assertIssues(t, componentByFolder(t, report, "B"), true, Issue{Kind: IssueCircular, TargetID: "a.mod", Providers: []string{"A (mod)"}, Detail: "a.mod => b.mod => a.mod"})
	assertIssues(t, componentByFolder(t, report, "Self"), true, Issue{Kind: IssueCircular, TargetID: "SELF.mod", Providers: []string{"Self (mod)"}, Detail: "self.mod => self.mod"})
}

// TestAnalyzeTransitiveFailure verifies a mod whose required dependency failed is itself failed.
func TestAnalyzeTransitiveFailure(t *testing.T) {
	report := Analyze(AnalyzeInput{Active: []ProjectedFolder{
		activeFolder("A", codeMod("a.mod", "1.0", requires("b.mod", ""))),
		activeFolder("B", codeMod("b.mod", "1.0", requires("c.missing", "3.0"))),
		activeFolder("C", codeMod("c.mod", "1.0", requires("a.mod", ""))),
	}})
	assertIssues(t, componentByFolder(t, report, "A"), true, Issue{Kind: IssueDependencyFailed, TargetID: "b.mod", Providers: []string{"B (mod)"}})
	assertIssues(t, componentByFolder(t, report, "B"), true, Issue{Kind: IssueMissing, TargetID: "c.missing", RequiredVersion: "3.0.0"})
	assertIssues(t, componentByFolder(t, report, "C"), true, Issue{Kind: IssueDependencyFailed, TargetID: "a.mod", Providers: []string{"A (mod)"}})
	want := []MissingDependency{{UniqueID: "c.missing", MinimumVersion: "3.0.0", RequiredBy: []string{"b.mod"}}}
	if !reflect.DeepEqual(report.Missing, want) {
		t.Fatalf("Missing = %#v, want %#v", report.Missing, want)
	}
}

// TestAnalyzeFoundButInvalidDependency verifies an installed but invalid required dependency is a failed dependency, not a missing one.
func TestAnalyzeFoundButInvalidDependency(t *testing.T) {
	broken := codeMod("b.mod", "1.0")
	broken.EntryDll = "bad:name.dll"
	report := Analyze(AnalyzeInput{Active: []ProjectedFolder{
		activeFolder("A", codeMod("a.mod", "1.0", requires("b.mod", ""))),
		activeFolder("B", broken),
	}})
	assertIssues(t, componentByFolder(t, report, "A"), true, Issue{Kind: IssueDependencyFailed, TargetID: "b.mod", Providers: []string{"B (mod)"}})
	if len(report.Missing) != 0 {
		t.Fatalf("Missing = %#v, want none", report.Missing)
	}
}

// TestAnalyzeOptionalDependencies verifies missing or failed optional dependencies never fail a mod.
func TestAnalyzeOptionalDependencies(t *testing.T) {
	broken := codeMod("b.mod", "1.0")
	broken.Name = ""
	report := Analyze(AnalyzeInput{Active: []ProjectedFolder{
		activeFolder("A", codeMod("a.mod", "1.0", optional("not.installed", "1.0"), optional("b.mod", ""))),
		activeFolder("B", broken),
	}})
	assertIssues(t, componentByFolder(t, report, "A"), false)
	if len(report.Missing) != 0 {
		t.Fatalf("Missing = %#v, want none", report.Missing)
	}
}

// TestAnalyzeContentPackFor verifies ContentPackFor is processed as a required dependency.
func TestAnalyzeContentPackFor(t *testing.T) {
	merged := contentPack("pack.merged", "1.0", "x.framework", "1.0")
	merged.Dependencies = []Dependency{requires("X.Framework", "2.0")}
	report := Analyze(AnalyzeInput{Active: []ProjectedFolder{
		activeFolder("Missing", contentPack("pack.missing", "1.0", "x.framework", "")),
		activeFolder("Merged", merged),
		activeFolder("Old", contentPack("pack.old", "1.0", "y.framework", "3.0")),
		activeFolder("Y", codeMod("y.framework", "2.5")),
	}})
	assertIssues(t, componentByFolder(t, report, "Missing"), true, Issue{Kind: IssueMissing, TargetID: "x.framework"})
	assertIssues(t, componentByFolder(t, report, "Merged"), true, Issue{Kind: IssueMissing, TargetID: "X.Framework", RequiredVersion: "2.0.0"})
	assertIssues(t, componentByFolder(t, report, "Old"), true, Issue{Kind: IssueVersionTooLow, TargetID: "y.framework", RequiredVersion: "3.0.0", FoundVersion: "2.5.0", Providers: []string{"Y (mod)"}})
	want := []MissingDependency{{UniqueID: "X.Framework", MinimumVersion: "2.0.0", RequiredBy: []string{"pack.merged", "pack.missing"}}}
	if !reflect.DeepEqual(report.Missing, want) {
		t.Fatalf("Missing = %#v, want %#v", report.Missing, want)
	}
}

// TestAnalyzeCaseInsensitiveIDs verifies dependency IDs match installed mods ignoring case and whitespace.
func TestAnalyzeCaseInsensitiveIDs(t *testing.T) {
	report := Analyze(AnalyzeInput{Active: []ProjectedFolder{
		activeFolder("A", codeMod("a.mod", "1.0", requires("PATHOSCHILD.contentpatcher", "2.0"))),
		activeFolder("P", codeMod("Pathoschild.ContentPatcher", "2.9.1")),
		activeFolder("C", contentPack("c.pack", "1.0", " pathoschild.CONTENTPATCHER ", "")),
	}})
	for _, component := range report.Components {
		assertIssues(t, component, false)
	}
}

// TestAnalyzeFolderCollisionIsWarning verifies a shadowed provider with another mod ID warns without failing.
func TestAnalyzeFolderCollisionIsWarning(t *testing.T) {
	winner := activeFolder("Foo", codeMod("a.foo", "1.0"))
	winner.Competing = []Provider{
		{Folder: "Foo", Provider: "Other Mod", Manifest: codeMod("b.bar", "1.0")},
		{Folder: "Foo", Provider: "Same Mod Older", Manifest: codeMod("A.FOO", "0.9")},
		{Folder: "Foo", Provider: "Broken Mod", ParseErr: errors.New("bad")},
		{Folder: "Foo", Provider: "Another Mod", Manifest: codeMod("c.baz", "1.0")},
	}
	report := Analyze(AnalyzeInput{Active: []ProjectedFolder{winner}})
	assertIssues(t, componentByFolder(t, report, "Foo"), false, Issue{Kind: IssueFolderCollision, Providers: []string{"Another Mod", "Other Mod"}, Detail: "b.bar, c.baz"})
}

// TestAnalyzeBundled verifies bundled loader mods are flagged case-insensitively.
func TestAnalyzeBundled(t *testing.T) {
	report := Analyze(AnalyzeInput{
		Active: []ProjectedFolder{
			activeFolder("ConsoleCommands", codeMod("smapi.consolecommands", "4.5.2")),
			activeFolder("Other", codeMod("other.mod", "1.0")),
		},
		BundledIDs: []string{"SMAPI.ConsoleCommands", "SMAPI.SaveBackup", ""},
	})
	if !componentByFolder(t, report, "ConsoleCommands").Bundled {
		t.Fatal("ConsoleCommands Bundled = false, want true")
	}
	if componentByFolder(t, report, "Other").Bundled {
		t.Fatal("Other Bundled = true, want false")
	}
}

// TestAnalyzeMissingAggregation verifies missing dependencies are merged across requirers with the highest minimum.
func TestAnalyzeMissingAggregation(t *testing.T) {
	report := Analyze(AnalyzeInput{
		Active: []ProjectedFolder{
			activeFolder("B", codeMod("b.mod", "1.0", requires("X.Lib", "2.0"), requires("y.lib", ""))),
			activeFolder("A", codeMod("a.mod", "1.0", requires("x.lib", "1.5"), requires("z.lib", "1.0"))),
			activeFolder("C", codeMod("c.mod", "1.0", requires("x.LIB", ""))),
		},
		Disabled: []Provider{{Folder: "Z", Provider: "Z Mod", Manifest: codeMod("z.lib", "1.2")}},
	})
	want := []MissingDependency{
		{UniqueID: "x.lib", MinimumVersion: "2.0.0", RequiredBy: []string{"a.mod", "b.mod", "c.mod"}},
		{UniqueID: "y.lib", RequiredBy: []string{"b.mod"}},
		{UniqueID: "z.lib", MinimumVersion: "1.0.0", RequiredBy: []string{"a.mod"}, DisabledProviders: []string{"Z Mod"}},
	}
	if !reflect.DeepEqual(report.Missing, want) {
		t.Fatalf("Missing = %#v, want %#v", report.Missing, want)
	}
	assertIssues(t, componentByFolder(t, report, "A"), true,
		Issue{Kind: IssueMissing, TargetID: "x.lib", RequiredVersion: "1.5.0"},
		Issue{Kind: IssueDisabled, TargetID: "z.lib", RequiredVersion: "1.0.0", Providers: []string{"Z Mod"}},
	)
}

// TestAnalyzeDeterministic verifies the report does not depend on input order.
func TestAnalyzeDeterministic(t *testing.T) {
	active := []ProjectedFolder{
		activeFolder("b", codeMod("b.mod", "1.0", requires("missing.one", ""), requires("missing.two", "1.0"))),
		activeFolder("A", codeMod("a.mod", "1.0", requires("b.mod", ""))),
		activeFolder("c", codeMod("A.MOD", "2.0")),
		activeFolder("D", contentPack("d.pack", "1.0", "missing.two", "2.0")),
	}
	reversed := make([]ProjectedFolder, len(active))
	for i := range active {
		reversed[len(active)-1-i] = active[i]
	}
	first := Analyze(AnalyzeInput{Active: active})
	second := Analyze(AnalyzeInput{Active: reversed})
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("Analyze depends on input order:\n%#v\n%#v", first, second)
	}
	folders := []string{}
	for _, component := range first.Components {
		folders = append(folders, component.Folder)
	}
	if want := []string{"A", "b", "c", "D"}; !reflect.DeepEqual(folders, want) {
		t.Fatalf("component order = %v, want %v", folders, want)
	}
}

// TestAnalyzeEmpty verifies an empty input yields an empty report.
func TestAnalyzeEmpty(t *testing.T) {
	report := Analyze(AnalyzeInput{})
	if len(report.Components) != 0 || len(report.Missing) != 0 {
		t.Fatalf("Analyze(empty) = %#v", report)
	}
}

package smapi

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// namedManifestJSON returns a valid code-mod manifest with an explicit name, ID and version.
func namedManifestJSON(id, name, version string) string {
	return fmt.Sprintf(`{"Name":%q,"Version":%q,"UniqueID":%q,"EntryDll":"Mod.dll"}`, name, version, id)
}

// TestPlanArchive verifies the archive layout plan for every supported archive shape.
func TestPlanArchive(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  []PlannedFolder
	}{
		{
			name:  "single folder",
			files: map[string]string{"Foo/manifest.json": namedManifestJSON("a.foo", "Foo Mod", "1.2.3"), "Foo/Mod.dll": "x"},
			want:  []PlannedFolder{{SourceRel: "Foo", DestName: "Foo", UniqueID: "a.foo", Name: "Foo Mod", Version: "1.2.3"}},
		},
		{
			name:  "wrapper folder",
			files: map[string]string{"Wrapper/Foo/manifest.json": namedManifestJSON("a.foo", "Foo", "1.0")},
			want:  []PlannedFolder{{SourceRel: "Wrapper/Foo", DestName: "Foo", UniqueID: "a.foo", Name: "Foo", Version: "1.0.0"}},
		},
		{
			name: "multiple component folders",
			files: map[string]string{
				"[JA] Foo/manifest.json": namedManifestJSON("a.foo.ja", "Foo JA", "1.0"),
				"[CP] Foo/manifest.json": namedManifestJSON("a.foo.cp", "Foo CP", "1.0"),
				"readme.txt":             "docs",
			},
			want: []PlannedFolder{
				{SourceRel: "[CP] Foo", DestName: "[CP] Foo", UniqueID: "a.foo.cp", Name: "Foo CP", Version: "1.0.0"},
				{SourceRel: "[JA] Foo", DestName: "[JA] Foo", UniqueID: "a.foo.ja", Name: "Foo JA", Version: "1.0.0"},
			},
		},
		{
			name:  "sorted case-insensitively",
			files: map[string]string{"beta/manifest.json": namedManifestJSON("a.b", "B", "1.0"), "Alpha/manifest.json": namedManifestJSON("a.a", "A", "1.0"), "Gamma/manifest.json": namedManifestJSON("a.g", "G", "1.0")},
			want: []PlannedFolder{
				{SourceRel: "Alpha", DestName: "Alpha", UniqueID: "a.a", Name: "A", Version: "1.0.0"},
				{SourceRel: "beta", DestName: "beta", UniqueID: "a.b", Name: "B", Version: "1.0.0"},
				{SourceRel: "Gamma", DestName: "Gamma", UniqueID: "a.g", Name: "G", Version: "1.0.0"},
			},
		},
		{
			name:  "root manifest with unsafe name characters",
			files: map[string]string{"manifest.json": namedManifestJSON("author.mod", "My: Mod/../Thing", "2.0"), "assets/sprite.png": "x"},
			want:  []PlannedFolder{{SourceRel: "", DestName: "My_ Mod_.._Thing", UniqueID: "author.mod", Name: "My: Mod/../Thing", Version: "2.0.0"}},
		},
		{
			name:  "root manifest named dot-dot falls back to id",
			files: map[string]string{"manifest.json": namedManifestJSON("author.mod", "..", "1.0")},
			want:  []PlannedFolder{{SourceRel: "", DestName: "author.mod", UniqueID: "author.mod", Name: "..", Version: "1.0.0"}},
		},
		{
			name:  "root manifest named like the root deployment folder",
			files: map[string]string{"manifest.json": namedManifestJSON("author.mod", ".gorganizer-root", "1.0")},
			want:  []PlannedFolder{{SourceRel: "", DestName: "gorganizer-root", UniqueID: "author.mod", Name: ".gorganizer-root", Version: "1.0.0"}},
		},
		{
			name:  "root manifest with reserved name falls back to id",
			files: map[string]string{"manifest.json": namedManifestJSON("author.mod", "Metadata.YAML", "1.0")},
			want:  []PlannedFolder{{SourceRel: "", DestName: "author.mod", UniqueID: "author.mod", Name: "Metadata.YAML", Version: "1.0.0"}},
		},
		{
			name:  "root manifest without usable name or id",
			files: map[string]string{"manifest.json": `{"Name":" ... ","UniqueID":"..","Version":"1.0","EntryDll":"a.dll"}`},
			want:  []PlannedFolder{{SourceRel: "", DestName: "Mod", UniqueID: "..", Name: "...", Version: "1.0.0"}},
		},
		{
			name:  "root manifest that fails to parse",
			files: map[string]string{"manifest.json": `{"Name": "Broken",`},
			want:  []PlannedFolder{{SourceRel: "", DestName: "Mod"}},
		},
		{
			name:  "root manifest named like a macOS folder falls back to id",
			files: map[string]string{"manifest.json": namedManifestJSON("author.mod", "__MACOSX", "1.0")},
			want:  []PlannedFolder{{SourceRel: "", DestName: "author.mod", UniqueID: "author.mod", Name: "__MACOSX", Version: "1.0.0"}},
		},
		{
			name:  "root manifest named mcs falls back to id",
			files: map[string]string{"manifest.json": namedManifestJSON("author.mcs", "MCS", "1.0")},
			want:  []PlannedFolder{{SourceRel: "", DestName: "author.mcs", UniqueID: "author.mcs", Name: "MCS", Version: "1.0.0"}},
		},
		{
			name:  "root manifest named like an ignored file falls back to id",
			files: map[string]string{"manifest.json": namedManifestJSON("author.mod", "Readme.TXT", "1.0")},
			want:  []PlannedFolder{{SourceRel: "", DestName: "author.mod", UniqueID: "author.mod", Name: "Readme.TXT", Version: "1.0.0"}},
		},
		{
			name:  "root manifest with only scanner-ignored names",
			files: map[string]string{"manifest.json": namedManifestJSON("Thumbs.db", "desktop.ini", "1.0")},
			want:  []PlannedFolder{{SourceRel: "", DestName: "Mod", UniqueID: "Thumbs.db", Name: "desktop.ini", Version: "1.0.0"}},
		},
		{
			name:  "root manifest beside ignored and non-mod folders",
			files: map[string]string{"manifest.json": namedManifestJSON("author.mod", "Root Mod", "1.0"), ".hidden/manifest.json": namedManifestJSON("a.hidden", "Hidden", "1.0"), "Portraits/Abigail.xnb": "x", "assets/": ""},
			want:  []PlannedFolder{{SourceRel: "", DestName: "Root Mod", UniqueID: "author.mod", Name: "Root Mod", Version: "1.0.0"}},
		},
		{
			name:  "unparseable manifest in folder keeps folder name",
			files: map[string]string{"Broken/manifest.json": `{"Version":"x"}`},
			want:  []PlannedFolder{{SourceRel: "Broken", DestName: "Broken"}},
		},
		{
			name:  "macOS sibling ignored",
			files: map[string]string{"__MACOSX/Foo/._manifest.json": "junk", "__MACOSX/Foo/manifest.json": "junk", "Foo/manifest.json": namedManifestJSON("a.foo", "Foo", "1.0")},
			want:  []PlannedFolder{{SourceRel: "Foo", DestName: "Foo", UniqueID: "a.foo", Name: "Foo", Version: "1.0.0"}},
		},
		{
			name:  "nested manifest not planned separately",
			files: map[string]string{"Foo/manifest.json": namedManifestJSON("a.foo", "Foo", "1.0"), "Foo/Bundled/manifest.json": namedManifestJSON("a.inner", "Inner", "1.0")},
			want:  []PlannedFolder{{SourceRel: "Foo", DestName: "Foo", UniqueID: "a.foo", Name: "Foo", Version: "1.0.0"}},
		},
		{
			name: "fomod and loose files are irrelevant",
			files: map[string]string{
				"fomod/ModuleConfig.xml": "<config/>",
				"readme.md":              "docs",
				"loose.dll":              "x",
				"Foo/manifest.json":      namedManifestJSON("a.foo", "Foo", "1.0"),
			},
			want: []PlannedFolder{{SourceRel: "Foo", DestName: "Foo", UniqueID: "a.foo", Name: "Foo", Version: "1.0.0"}},
		},
		{
			name: "unparseable manifest never counts as a duplicate id",
			files: map[string]string{
				"Alpha/manifest.json": namedManifestJSON("a.shared", "Alpha", "1.0"),
				"Beta/manifest.json":  `{"UniqueID":"a.shared",`,
			},
			want: []PlannedFolder{
				{SourceRel: "Alpha", DestName: "Alpha", UniqueID: "a.shared", Name: "Alpha", Version: "1.0.0"},
				{SourceRel: "Beta", DestName: "Beta"},
			},
		},
		{
			name:  "ordinary dot folder is dropped",
			files: map[string]string{".git/config": "x", "Foo/manifest.json": namedManifestJSON("a.foo", "Foo", "1.0")},
			want:  []PlannedFolder{{SourceRel: "Foo", DestName: "Foo", UniqueID: "a.foo", Name: "Foo", Version: "1.0.0"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTree(t, root, tc.files)
			plan, err := PlanArchive(root)
			if err != nil {
				t.Fatalf("PlanArchive: %v", err)
			}
			if !reflect.DeepEqual(plan.Folders, tc.want) {
				t.Fatalf("PlanArchive = %#v, want %#v", plan.Folders, tc.want)
			}
		})
	}
}

// TestPlanArchiveRefusals verifies archives that are not safely installable are refused with a typed reason.
func TestPlanArchiveRefusals(t *testing.T) {
	cases := []struct {
		name       string
		files      map[string]string
		wantReason string
		wantDetail string
	}{
		{
			name: "smapi installer bundle",
			files: map[string]string{
				"SMAPI 4.5.2 installer/install on Linux.sh":        "#!/bin/sh",
				"SMAPI 4.5.2 installer/internal/linux/install.dat": "x",
			},
			wantReason: ReasonLoaderInstaller,
		},
		{
			name:       "installer launcher alone",
			files:      map[string]string{"install on macOS.command": "x", "Foo/manifest.json": namedManifestJSON("a.foo", "Foo", "1.0")},
			wantReason: ReasonLoaderInstaller,
		},
		{
			name:       "installer payload alone",
			files:      map[string]string{"bundle/internal/windows/install.dat": "x", "Foo/manifest.json": namedManifestJSON("a.foo", "Foo", "1.0")},
			wantReason: ReasonLoaderInstaller,
		},
		{
			name:       "installer payload at archive root",
			files:      map[string]string{"internal/macOS/install.dat": "x"},
			wantReason: ReasonLoaderInstaller,
		},
		{
			name:       "no manifest",
			files:      map[string]string{"readme.txt": "x", "Foo/Foo.dll": "x"},
			wantReason: ReasonNoManifest,
		},
		{
			name:       "empty archive",
			files:      map[string]string{},
			wantReason: ReasonNoManifest,
		},
		{
			name:       "install.dat outside an installer layout",
			files:      map[string]string{"x/myinternal/linux/install.dat": "x"},
			wantReason: ReasonNoManifest,
		},
		{
			name:       "only an xnb mod",
			files:      map[string]string{"Portraits/Abigail.xnb": "x"},
			wantReason: ReasonNoManifest,
		},
		{
			name:       "folder named metadata.yaml",
			files:      map[string]string{"metadata.yaml/manifest.json": namedManifestJSON("a.meta", "Meta", "1.0")},
			wantReason: ReasonUnsafeDestination,
			wantDetail: "metadata.yaml",
		},
		{
			name:       "wrapped folder named overwrite",
			files:      map[string]string{"Wrapper/Overwrite/manifest.json": namedManifestJSON("a.over", "Over", "1.0")},
			wantReason: ReasonUnsafeDestination,
			wantDetail: "Overwrite",
		},
		{
			name:       "folder named like the root deployment folder",
			files:      map[string]string{".gorganizer-root/manifest.json": namedManifestJSON("a.root", "Root", "1.0"), "Foo/manifest.json": namedManifestJSON("a.foo", "Foo", "1.0")},
			wantReason: ReasonUnsafeDestination,
			wantDetail: ".gorganizer-root",
		},
		{
			name:       "wrapped gorganizer folder",
			files:      map[string]string{"Wrapper/.GORGANIZER-state/manifest.json": namedManifestJSON("a.state", "State", "1.0")},
			wantReason: ReasonUnsafeDestination,
			wantDetail: ".GORGANIZER-state",
		},
		{
			name:       "case-variant duplicate folders",
			files:      map[string]string{"A/Foo/manifest.json": namedManifestJSON("a.one", "One", "1.0"), "B/foo/manifest.json": namedManifestJSON("a.two", "Two", "1.0")},
			wantReason: ReasonFolderCollision,
			wantDetail: "Foo,foo",
		},
		{
			name:       "root manifest above a sibling mod folder",
			files:      map[string]string{"manifest.json": namedManifestJSON("a.root", "Root", "1.0"), "Sub/manifest.json": namedManifestJSON("a.sub", "Sub", "1.0")},
			wantReason: ReasonNestedMods,
			wantDetail: "Sub",
		},
		{
			name:       "root manifest above deeper mod folders",
			files:      map[string]string{"manifest.json": namedManifestJSON("a.root", "Root", "1.0"), "assets/Bundled/manifest.json": namedManifestJSON("a.b", "B", "1.0"), "Other/manifest.json": `{"broken":`},
			wantReason: ReasonNestedMods,
			wantDetail: "Other,assets/Bundled",
		},
		{
			name:       "unparseable root manifest above a mod folder",
			files:      map[string]string{"manifest.json": `{"Name":`, "Foo/manifest.json": namedManifestJSON("a.foo", "Foo", "1.0")},
			wantReason: ReasonNestedMods,
			wantDetail: "Foo",
		},
		{
			name: "variant folders sharing one unique id",
			files: map[string]string{
				"ModX Full/manifest.json":  namedManifestJSON("author.modx", "ModX", "1.0"),
				"ModX Light/manifest.json": namedManifestJSON("Author.ModX", "ModX", "1.0"),
			},
			wantReason: ReasonDuplicateIDs,
			wantDetail: "author.modx",
		},
		{
			name: "every duplicated id reported once",
			files: map[string]string{
				"A/manifest.json": namedManifestJSON("x.one", "A", "1.0"),
				"B/manifest.json": namedManifestJSON("X.ONE", "B", "1.0"),
				"C/manifest.json": namedManifestJSON("x.one", "C", "1.0"),
				"D/manifest.json": namedManifestJSON("y:two=2", "D", "1.0"),
				"E/manifest.json": namedManifestJSON("y:two=2", "E", "1.0"),
				"F/manifest.json": namedManifestJSON("z.unique", "F", "1.0"),
			},
			wantReason: ReasonDuplicateIDs,
			wantDetail: "x.one,y:two=2",
		},
		{
			name:       "same folder name in two wrappers",
			files:      map[string]string{"v1/Foo/manifest.json": namedManifestJSON("a.foo", "Foo", "1.0"), "v2/Foo/manifest.json": namedManifestJSON("a.foo", "Foo", "2.0")},
			wantReason: ReasonFolderCollision,
			wantDetail: "Foo,Foo",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTree(t, root, tc.files)
			plan, err := PlanArchive(root)
			if !errors.Is(err, ErrNotAMod) {
				t.Fatalf("PlanArchive = %#v, %v; want ErrNotAMod", plan, err)
			}
			var notAMod NotAModError
			if !errors.As(err, &notAMod) {
				t.Fatalf("PlanArchive error %T is not NotAModError", err)
			}
			if notAMod.Reason != tc.wantReason || notAMod.Detail != tc.wantDetail {
				t.Fatalf("PlanArchive error = %#v, want reason %q detail %q", notAMod, tc.wantReason, tc.wantDetail)
			}
			if len(plan.Folders) != 0 {
				t.Fatalf("PlanArchive plan = %#v, want empty", plan)
			}
		})
	}
}

// TestPlanArchiveMissingRoot verifies a missing extraction root is an I/O error, not a refusal.
func TestPlanArchiveMissingRoot(t *testing.T) {
	_, err := PlanArchive(filepath.Join(t.TempDir(), "missing"))
	if err == nil || errors.Is(err, ErrNotAMod) {
		t.Fatalf("PlanArchive(missing) error = %v, want a non-refusal error", err)
	}
}

// TestValidateDestName verifies destination folder names are single safe, visible, unreserved components.
func TestValidateDestName(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{name: "Foo", ok: true},
		{name: "[CP] Foo Bar", ok: true},
		{name: "gorganizer-root", ok: true},
		{name: "Ünïcödé Mod", ok: true},
		{name: "Mod.", ok: true},
		{name: "a:b", ok: true},
		{name: strings.Repeat("a", 255), ok: true},
		{name: strings.Repeat("a", 256)},
		{name: ""},
		{name: "."},
		{name: ".."},
		{name: "a/b"},
		{name: `a\b`},
		{name: "a\x00b"},
		{name: "a\nb"},
		{name: "a\x7fb"},
		{name: "a\u0085b"},
		{name: ".hidden"},
		{name: "metadata.yaml"},
		{name: "METADATA.yaml"},
		{name: "overwrite"},
		{name: "Overwrite"},
		{name: ".gorganizer-root"},
		{name: "\xff\xfe"},
	}
	for _, tc := range cases {
		err := ValidateDestName(tc.name)
		if tc.ok {
			if err != nil {
				t.Fatalf("ValidateDestName(%q) = %v, want nil", tc.name, err)
			}
			continue
		}
		var notAMod NotAModError
		if !errors.As(err, &notAMod) || notAMod.Reason != ReasonUnsafeDestination || notAMod.Detail != tc.name {
			t.Fatalf("ValidateDestName(%q) = %v, want unsafe_destination", tc.name, err)
		}
	}
}

// TestSanitizeFolderName verifies manifest names are turned into safe folder names.
func TestSanitizeFolderName(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{input: "Simple Mod", want: "Simple Mod"},
		{input: "My (Cool) Mod", want: "My (Cool) Mod"},
		{input: `a<b>c:d"e/f\g|h?i*j`, want: "a_b_c_d_e_f_g_h_i_j"},
		{input: "  lots   of \t\n spaces  ", want: "lots of spaces"},
		{input: "..hidden..", want: "hidden"},
		{input: ". . .x. .", want: "x"},
		{input: ".gorganizer-root", want: "gorganizer-root"},
		{input: "..", want: ""},
		{input: ".", want: ""},
		{input: "   ", want: ""},
		{input: "", want: ""},
		{input: "a\x01b\x7f", want: "a_b_"},
		{input: "Ünïcödé", want: "Ünïcödé"},
		{input: strings.Repeat("é", 200), want: strings.Repeat("é", 64)},
		{input: strings.Repeat("a", 127) + "é", want: strings.Repeat("a", 127)},
		{input: strings.Repeat("a", 127) + " b", want: strings.Repeat("a", 127)},
		{input: strings.Repeat("a", 127) + ".b", want: strings.Repeat("a", 127)},
	}
	for _, tc := range cases {
		got := SanitizeFolderName(tc.input)
		if got != tc.want {
			t.Fatalf("SanitizeFolderName(%q) = %q, want %q", tc.input, got, tc.want)
		}
		if len(got) > maxSanitizedNameBytes {
			t.Fatalf("SanitizeFolderName(%q) is %d bytes, want at most %d", tc.input, len(got), maxSanitizedNameBytes)
		}
	}
}

// TestNotAModError verifies the error text, detail escaping and ErrNotAMod matching.
func TestNotAModError(t *testing.T) {
	err := fmt.Errorf("installing: %w", NotAModError{Reason: ReasonFolderCollision, Detail: "Foo,foo"})
	if !errors.Is(err, ErrNotAMod) {
		t.Fatal("errors.Is(wrapped NotAModError, ErrNotAMod) = false")
	}
	if errors.Is(err, ErrInvalidManifest) {
		t.Fatal("errors.Is(wrapped NotAModError, ErrInvalidManifest) = true")
	}
	cases := []struct {
		err  NotAModError
		want string
	}{
		{err: NotAModError{Reason: ReasonNoManifest}, want: "not_a_mod:reason=no_manifest"},
		{err: NotAModError{Reason: ReasonNestedMods}, want: "not_a_mod:reason=nested_mods"},
		{err: NotAModError{Reason: ReasonDuplicateIDs, Detail: "a:b,c"}, want: "not_a_mod:reason=duplicate_ids:detail=a%3Ab%2Cc"},
		{err: NotAModError{Reason: ReasonUnsafeDestination, Detail: "x"}, want: "not_a_mod:reason=unsafe_destination:detail=x"},
		{err: NotAModError{Reason: ReasonFolderCollision, Detail: "Foo,foo"}, want: "not_a_mod:reason=folder_collision:detail=Foo%2Cfoo"},
		{err: NotAModError{Reason: ReasonUnsafeDestination, Detail: "a:b=c"}, want: "not_a_mod:reason=unsafe_destination:detail=a%3Ab%3Dc"},
		{err: NotAModError{Reason: ReasonUnsafeDestination, Detail: "loader_missing:"}, want: "not_a_mod:reason=unsafe_destination:detail=loader_missing%3A"},
		{err: NotAModError{Reason: ReasonNestedMods, Detail: "My Mod/100%,x?y#z"}, want: "not_a_mod:reason=nested_mods:detail=My%20Mod%2F100%25%2Cx%3Fy%23z"},
		{err: NotAModError{Reason: ReasonUnsafeDestination, Detail: "Caf\u00e9\n"}, want: "not_a_mod:reason=unsafe_destination:detail=Caf%C3%A9%0A"},
	}
	for _, tc := range cases {
		got := tc.err.Error()
		if got != tc.want {
			t.Fatalf("Error() = %q, want %q", got, tc.want)
		}
		if tc.err.Detail == "" {
			continue
		}
		escaped := strings.TrimPrefix(got, "not_a_mod:reason="+tc.err.Reason+":detail=")
		if strings.ContainsAny(escaped, ":=") || strings.Contains(escaped, "loader_missing:") {
			t.Fatalf("escaped detail %q still contains a token separator", escaped)
		}
		if decoded, err := url.PathUnescape(escaped); err != nil || decoded != tc.err.Detail {
			t.Fatalf("url.PathUnescape(%q) = %q, %v; want %q", escaped, decoded, err, tc.err.Detail)
		}
	}
}

// TestRootDestNameSkipsScannerIgnoredNames verifies a root manifest is never wrapped in a folder SMAPI's scanner would skip.
func TestRootDestNameSkipsScannerIgnoredNames(t *testing.T) {
	cases := []struct {
		name string
		id   string
		want string
	}{
		{name: "Good Mod", id: "a.b", want: "Good Mod"},
		{name: "mcs", id: "a.b", want: "a.b"},
		{name: "__macosx", id: "a.b", want: "a.b"},
		{name: "Thumbs.db", id: "a.b", want: "a.b"},
		{name: "DESKTOP.INI", id: "a.b", want: "a.b"},
		{name: "__folder_managed_by_vortex", id: "a.b", want: "a.b"},
		{name: ".DS_Store", id: "a.b", want: "DS_Store"},
		{name: "._Foo", id: "a.b", want: "_Foo"},
		{name: "notes.md", id: "a.b", want: "a.b"},
		{name: "Old Stuff.old", id: "a.b", want: "a.b"},
		{name: "archive.tar.gz", id: "a.b", want: "archive.tar.gz"},
		{name: "mcs", id: "author.backup", want: "Mod"},
		{name: "", id: "mcs", want: "Mod"},
	}
	for _, tc := range cases {
		if got := rootDestName(&Manifest{Name: tc.name, UniqueID: tc.id}); got != tc.want {
			t.Fatalf("rootDestName(%q, %q) = %q, want %q", tc.name, tc.id, got, tc.want)
		}
	}
	if got := rootDestName(nil); got != fallbackDestName {
		t.Fatalf("rootDestName(nil) = %q, want %q", got, fallbackDestName)
	}
}

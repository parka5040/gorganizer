package smapi

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

// writeTree creates files under root from a map of slash-separated paths to contents; a trailing slash makes a directory.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if strings.HasSuffix(rel, "/") {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatalf("MkdirAll(%s): %v", full, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", full, err)
		}
	}
}

// codeManifestJSON returns a minimal valid code-mod manifest.
func codeManifestJSON(id, name string) string {
	return fmt.Sprintf(`{"Name":%q,"Author":"Tester","Version":"1.0.0","UniqueID":%q,"EntryDll":"Mod.dll"}`, name, id)
}

type scanSummary struct {
	RelPath      string
	Kind         FolderKind
	Reason       string
	UniqueID     string
	ManifestBase string
	HasParseErr  bool
}

// summarize reduces scanned folders to comparable fields.
func summarize(folders []Folder) []scanSummary {
	result := []scanSummary{}
	for _, folder := range folders {
		summary := scanSummary{RelPath: folder.RelPath, Kind: folder.Kind, Reason: folder.Reason, HasParseErr: folder.ParseErr != nil}
		if folder.Manifest != nil {
			summary.UniqueID = folder.Manifest.UniqueID
		}
		if folder.ManifestPath != "" {
			summary.ManifestBase = filepath.Base(folder.ManifestPath)
		}
		result = append(result, summary)
	}
	return result
}

// TestScan verifies the ModScanner folder classification on real directory trees.
func TestScan(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  []scanSummary
	}{
		{
			name:  "single mod",
			files: map[string]string{"Foo/manifest.json": codeManifestJSON("a.foo", "Foo"), "Foo/Mod.dll": "x"},
			want:  []scanSummary{{RelPath: "Foo", Kind: FolderMod, UniqueID: "a.foo", ManifestBase: "manifest.json"}},
		},
		{
			name:  "manifest at root does not hide siblings",
			files: map[string]string{"manifest.json": codeManifestJSON("a.root", "Root"), "Sub/manifest.json": codeManifestJSON("a.sub", "Sub")},
			want: []scanSummary{
				{RelPath: "", Kind: FolderRootManifest, Reason: reasonRootManifest, UniqueID: "a.root", ManifestBase: "manifest.json"},
				{RelPath: "Sub", Kind: FolderMod, UniqueID: "a.sub", ManifestBase: "manifest.json"},
			},
		},
		{
			name:  "manifest at root alone",
			files: map[string]string{"MANIFEST.json": codeManifestJSON("a.root", "Root"), "Root.dll": "x", "assets/sprite.png": "x"},
			want: []scanSummary{
				{RelPath: "", Kind: FolderRootManifest, Reason: reasonRootManifest, UniqueID: "a.root", ManifestBase: "MANIFEST.json"},
				{RelPath: "assets", Kind: FolderEmpty, Reason: reasonEmptyFolder},
			},
		},
		{
			name:  "unparseable manifest at root",
			files: map[string]string{"manifest.json": `{"Name":`, "Foo/manifest.json": codeManifestJSON("a.foo", "Foo")},
			want: []scanSummary{
				{RelPath: "", Kind: FolderRootManifest, Reason: reasonRootManifest, ManifestBase: "manifest.json", HasParseErr: true},
				{RelPath: "Foo", Kind: FolderMod, UniqueID: "a.foo", ManifestBase: "manifest.json"},
			},
		},
		{
			name:  "root is a search folder even with loose files",
			files: map[string]string{"loose.dll": "x", "Foo/manifest.json": codeManifestJSON("a.foo", "Foo")},
			want:  []scanSummary{{RelPath: "Foo", Kind: FolderMod, UniqueID: "a.foo", ManifestBase: "manifest.json"}},
		},
		{
			name:  "root subfolders are never consolidated",
			files: map[string]string{"A/one.xnb": "x", "B/": ""},
			want: []scanSummary{
				{RelPath: "A", Kind: FolderXnb, Reason: reasonXnbMod},
				{RelPath: "B", Kind: FolderEmpty, Reason: reasonEmptyFolder},
			},
		},
		{
			name: "nested group folder",
			files: map[string]string{
				"Group/[CP] A/manifest.json": codeManifestJSON("a.cp", "A CP"),
				"Group/[JA] A/manifest.json": codeManifestJSON("a.ja", "A JA"),
				"Group/readme.txt":           "ignored text file",
			},
			want: []scanSummary{
				{RelPath: "Group/[CP] A", Kind: FolderMod, UniqueID: "a.cp", ManifestBase: "manifest.json"},
				{RelPath: "Group/[JA] A", Kind: FolderMod, UniqueID: "a.ja", ManifestBase: "manifest.json"},
			},
		},
		{
			name:  "wrapper folder",
			files: map[string]string{"Foo/Foo/manifest.json": codeManifestJSON("a.foo", "Foo")},
			want:  []scanSummary{{RelPath: "Foo/Foo", Kind: FolderMod, UniqueID: "a.foo", ManifestBase: "manifest.json"}},
		},
		{
			name:  "never descends into a mod folder",
			files: map[string]string{"Foo/manifest.json": codeManifestJSON("a.foo", "Foo"), "Foo/Sub/manifest.json": codeManifestJSON("a.sub", "Sub")},
			want:  []scanSummary{{RelPath: "Foo", Kind: FolderMod, UniqueID: "a.foo", ManifestBase: "manifest.json"}},
		},
		{
			name:  "dot folder ignored",
			files: map[string]string{".Disabled/manifest.json": codeManifestJSON("a.off", "Off"), "On/manifest.json": codeManifestJSON("a.on", "On")},
			want: []scanSummary{
				{RelPath: ".Disabled", Kind: FolderIgnored, Reason: reasonDotFolder},
				{RelPath: "On", Kind: FolderMod, UniqueID: "a.on", ManifestBase: "manifest.json"},
			},
		},
		{
			name: "macOS and system folders skipped",
			files: map[string]string{
				"__MACOSX/Foo/._manifest.json": "junk",
				"mcs/file.dll":                 "x",
				"._Foo/file.dll":               "x",
				"mcsMod/manifest.json":         codeManifestJSON("a.mcs", "Mcs"),
				"Foo/manifest.json":            codeManifestJSON("a.foo", "Foo"),
				"Foo/.DS_Store":                "x",
			},
			want: []scanSummary{
				{RelPath: "._Foo", Kind: FolderIgnored, Reason: reasonDotFolder},
				{RelPath: "Foo", Kind: FolderMod, UniqueID: "a.foo", ManifestBase: "manifest.json"},
				{RelPath: "mcsMod", Kind: FolderMod, UniqueID: "a.mcs", ManifestBase: "manifest.json"},
			},
		},
		{
			name:  "empty folders",
			files: map[string]string{"Empty/": "", "DocsOnly/readme.txt": "x", "DocsOnly/preview.PNG": "x", "DocsOnly/.hidden": "x"},
			want: []scanSummary{
				{RelPath: "DocsOnly", Kind: FolderEmpty, Reason: reasonEmptyFolder},
				{RelPath: "Empty", Kind: FolderEmpty, Reason: reasonEmptyFolder},
			},
		},
		{
			name:  "xnb folder",
			files: map[string]string{"XnbMod/Abigail.xnb": "x", "XnbMod/data.json": "{}", "XnbMod/readme.txt": "x"},
			want:  []scanSummary{{RelPath: "XnbMod", Kind: FolderXnb, Reason: reasonXnbMod}},
		},
		{
			name:  "xnb folder with other files is not xnb",
			files: map[string]string{"Mixed/Abigail.xnb": "x", "Mixed/code.dll": "x"},
			want:  []scanSummary{{RelPath: "Mixed", Kind: FolderInvalid, Reason: reasonNoManifest}},
		},
		{
			name:  "consolidated xnb subfolders",
			files: map[string]string{"Pack/A/one.xnb": "x", "Pack/B/two.XWB": "x", "Pack/C/": ""},
			want:  []scanSummary{{RelPath: "Pack", Kind: FolderXnb, Reason: reasonXnbMod}},
		},
		{
			name:  "consolidated empty subfolders",
			files: map[string]string{"Group/A/readme.txt": "x", "Group/B/": ""},
			want:  []scanSummary{{RelPath: "Group", Kind: FolderEmpty, Reason: reasonEmptyFolder}},
		},
		{
			name:  "consolidated xnb keeps first child reason",
			files: map[string]string{"Pack/A/": "", "Pack/B/two.xnb": "x"},
			want:  []scanSummary{{RelPath: "Pack", Kind: FolderXnb, Reason: reasonEmptyFolder}},
		},
		{
			name:  "no consolidation with a mod child",
			files: map[string]string{"Pack/A/one.xnb": "x", "Pack/B/manifest.json": codeManifestJSON("a.b", "B")},
			want: []scanSummary{
				{RelPath: "Pack/A", Kind: FolderXnb, Reason: reasonXnbMod},
				{RelPath: "Pack/B", Kind: FolderMod, UniqueID: "a.b", ManifestBase: "manifest.json"},
			},
		},
		{
			name: "smapi installer folder",
			files: map[string]string{
				"SMAPI 4.5.2 installer/install on Linux.sh":          "#!/bin/sh",
				"SMAPI 4.5.2 installer/install on Windows.bat":       "x",
				"SMAPI 4.5.2 installer/internal/linux/install.dat":   "x",
				"SMAPI 4.5.2 installer/internal/linux/SMAPI.dll":     "x",
				"SMAPI 4.5.2 installer/internal/windows/install.dat": "x",
			},
			want: []scanSummary{{RelPath: "SMAPI 4.5.2 installer", Kind: FolderInvalid, Reason: reasonInstaller}},
		},
		{
			name:  "files without manifest",
			files: map[string]string{"Foo/Foo.dll": "x", "Foo/assets/sprite.png": "x"},
			want:  []scanSummary{{RelPath: "Foo", Kind: FolderInvalid, Reason: reasonNoManifest}},
		},
		{
			name:  "empty vortex folder",
			files: map[string]string{"V/__folder_managed_by_vortex": "", "V/config.json": "{}"},
			want:  []scanSummary{{RelPath: "V", Kind: FolderInvalid, Reason: reasonEmptyVortex}},
		},
		{
			name:  "manifest casing",
			files: map[string]string{"Foo/MANIFEST.JSON": codeManifestJSON("a.upper", "Upper")},
			want:  []scanSummary{{RelPath: "Foo", Kind: FolderMod, UniqueID: "a.upper", ManifestBase: "MANIFEST.JSON"}},
		},
		{
			name:  "exact manifest name preferred",
			files: map[string]string{"Foo/Manifest.json": "not json", "Foo/manifest.json": codeManifestJSON("a.exact", "Exact")},
			want:  []scanSummary{{RelPath: "Foo", Kind: FolderMod, UniqueID: "a.exact", ManifestBase: "manifest.json"}},
		},
		{
			name:  "directory named manifest.json is not a manifest",
			files: map[string]string{"Foo/manifest.json/": "", "Foo/Foo.dll": "x"},
			want:  []scanSummary{{RelPath: "Foo", Kind: FolderInvalid, Reason: reasonNoManifest}},
		},
		{
			name: "lenient manifest",
			files: map[string]string{"Foo/manifest.json": "\xef\xbb\xbf{\n  // comment\n  \"Name\": \"Foo\",\n  \"Version\": \"1.0.0\",\n  \"UniqueID\": \"a.lenient\",\n" +
				"  \"EntryDll\": \"Foo.dll\", /* trailing */\n}\n"},
			want: []scanSummary{{RelPath: "Foo", Kind: FolderMod, UniqueID: "a.lenient", ManifestBase: "manifest.json"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeTree(t, root, tc.files)
			folders, err := Scan(root)
			if err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if got := summarize(folders); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Scan = %#v, want %#v", got, tc.want)
			}
			viewed, err := ScanFS(os.DirFS(root))
			if err != nil {
				t.Fatalf("ScanFS: %v", err)
			}
			if got := summarize(viewed); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ScanFS = %#v, want the Scan result %#v", got, tc.want)
			}
		})
	}
}

// TestScanFSReadsAnAbstractView verifies ScanFS classifies a view by its resolved entries, keeps slash manifest paths and caps manifest size.
func TestScanFSReadsAnAbstractView(t *testing.T) {
	view := fstest.MapFS{
		"Foo/manifest.json":         {Data: []byte(codeManifestJSON("a.foo", "Foo"))},
		"Foo/Mod.dll":               {Data: []byte("x")},
		"Group/Inner/MANIFEST.JSON": {Data: []byte(codeManifestJSON("a.inner", "Inner"))},
		"Linked/manifest.json":      {Mode: fs.ModeSymlink},
		"Huge/manifest.json":        {Data: []byte(`{"Name":"` + strings.Repeat("x", maxManifestSize) + `"}`)},
	}
	folders, err := ScanFS(view)
	if err != nil {
		t.Fatalf("ScanFS: %v", err)
	}
	want := []scanSummary{
		{RelPath: "Foo", Kind: FolderMod, UniqueID: "a.foo", ManifestBase: "manifest.json"},
		{RelPath: "Group/Inner", Kind: FolderMod, UniqueID: "a.inner", ManifestBase: "MANIFEST.JSON"},
		{RelPath: "Huge", Kind: FolderMod, Reason: fmt.Sprintf("%s%v: manifest Huge/manifest.json exceeds %d bytes", reasonParsePrefix, ErrInvalidManifest, maxManifestSize), ManifestBase: "manifest.json", HasParseErr: true},
		{RelPath: "Linked", Kind: FolderInvalid, Reason: reasonNoManifest},
	}
	if got := summarize(folders); !reflect.DeepEqual(got, want) {
		t.Fatalf("ScanFS = %#v, want %#v", got, want)
	}
	for _, folder := range folders {
		switch folder.RelPath {
		case "Group/Inner":
			if folder.ManifestPath != "Group/Inner/MANIFEST.JSON" {
				t.Errorf("ManifestPath = %q, want the slash view path", folder.ManifestPath)
			}
		case "Huge":
			if !errors.Is(folder.ParseErr, ErrInvalidManifest) {
				t.Errorf("Huge ParseErr = %v, want ErrInvalidManifest", folder.ParseErr)
			}
		}
	}
	if _, err := ScanFS(fstest.MapFS{".": {Data: []byte("file root")}}); err == nil {
		t.Fatal("ScanFS over a file root succeeded")
	}
}

// TestScanInvalidManifests verifies unreadable manifests still yield a mod folder with the parse error attached.
func TestScanInvalidManifests(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"Broken/manifest.json":     `{"Name": "Broken",`,
		"Null/manifest.json":       "null",
		"BadVersion/manifest.json": `{"Name":"x","Version":"one","UniqueID":"a.b","EntryDll":"a.dll"}`,
	})
	folders, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(folders) != 3 {
		t.Fatalf("Scan returned %d folders, want 3: %#v", len(folders), folders)
	}
	for _, folder := range folders {
		if folder.Kind != FolderMod || folder.Manifest != nil || !errors.Is(folder.ParseErr, ErrInvalidManifest) {
			t.Fatalf("folder %q = %#v, want FolderMod with nil manifest and ErrInvalidManifest", folder.RelPath, folder)
		}
		if folder.ManifestPath != filepath.Join(root, folder.RelPath, "manifest.json") {
			t.Fatalf("folder %q ManifestPath = %q", folder.RelPath, folder.ManifestPath)
		}
		wantPrefix := reasonParsePrefix
		if folder.RelPath == "Null" {
			wantPrefix = reasonNullManifest
		}
		if !strings.HasPrefix(folder.Reason, wantPrefix) {
			t.Fatalf("folder %q Reason = %q, want prefix %q", folder.RelPath, folder.Reason, wantPrefix)
		}
	}
}

// TestScanOversizedManifest verifies a manifest above the size cap is reported as a parse error.
func TestScanOversizedManifest(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"Huge/manifest.json": `{"Name":"` + strings.Repeat("x", maxManifestSize) + `"}`})
	folders, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(folders) != 1 || folders[0].Kind != FolderMod || !errors.Is(folders[0].ParseErr, ErrInvalidManifest) {
		t.Fatalf("Scan = %#v, want one FolderMod with ErrInvalidManifest", folders)
	}
}

// symlinkTree creates symlinks under root from a map of slash-separated link paths to targets.
func symlinkTree(t *testing.T, root string, links map[string]string) {
	t.Helper()
	for rel, target := range links {
		link := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", filepath.Dir(link), err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("Symlink(%s): %v", rel, err)
		}
	}
}

// symlinkFixture builds a mods folder whose symlinks point outside it, back into it, and nowhere.
func symlinkFixture(t *testing.T) string {
	t.Helper()
	outside := t.TempDir()
	writeTree(t, outside, map[string]string{
		"Linked/manifest.json": codeManifestJSON("a.linked", "Linked"),
		"manifest.json":        codeManifestJSON("a.target", "Target"),
		"Payload/Pack.dll":     "x",
	})
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"Real/manifest.json": codeManifestJSON("a.real", "Real"),
		"Foo/Foo.dll":        "x",
		"Wrap/":              "",
		"Loop/":              "",
		"Deep/file.dll":      "x",
		"Deep/data/":         "",
		"Broken/":            "",
	})
	symlinkTree(t, root, map[string]string{
		"manifest.json":        filepath.Join(outside, "manifest.json"),
		"Linked":               filepath.Join(outside, "Linked"),
		"Foo/manifest.json":    filepath.Join(outside, "manifest.json"),
		"Wrap/Inner":           filepath.Join(outside, "Linked"),
		"Wrap/manifest.json":   filepath.Join(outside, "manifest.json"),
		"Loop/back":            root,
		"Loop/self":            filepath.Join(root, "Loop"),
		"Deep/data/up":         filepath.Join(root, "Deep"),
		"Deep/data/payload":    filepath.Join(outside, "Payload"),
		"Broken/manifest.json": filepath.Join(outside, "missing.json"),
	})
	return root
}

// TestScanSymlinks verifies the strict scan never follows symlinked directories or manifests.
func TestScanSymlinks(t *testing.T) {
	root := symlinkFixture(t)
	folders, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := []scanSummary{
		{RelPath: "Broken", Kind: FolderEmpty, Reason: reasonEmptyFolder},
		{RelPath: "Deep", Kind: FolderInvalid, Reason: reasonNoManifest},
		{RelPath: "Foo", Kind: FolderInvalid, Reason: reasonNoManifest},
		{RelPath: "Loop", Kind: FolderEmpty, Reason: reasonEmptyFolder},
		{RelPath: "Real", Kind: FolderMod, UniqueID: "a.real", ManifestBase: "manifest.json"},
		{RelPath: "Wrap", Kind: FolderEmpty, Reason: reasonEmptyFolder},
	}
	if got := summarize(folders); !reflect.DeepEqual(got, want) {
		t.Fatalf("Scan = %#v, want %#v", got, want)
	}
}

// TestScanFollowSymlinks verifies the following scan reads through symlinks like SMAPI and stops at directory loops.
func TestScanFollowSymlinks(t *testing.T) {
	root := symlinkFixture(t)
	folders, err := ScanWith(root, ScanOptions{FollowSymlinks: true})
	if err != nil {
		t.Fatalf("ScanWith: %v", err)
	}
	want := []scanSummary{
		{RelPath: "", Kind: FolderRootManifest, Reason: reasonRootManifest, UniqueID: "a.target", ManifestBase: "manifest.json"},
		{RelPath: "Broken", Kind: FolderInvalid, Reason: reasonNoManifest},
		{RelPath: "Deep", Kind: FolderInvalid, Reason: reasonNoManifest},
		{RelPath: "Foo", Kind: FolderMod, UniqueID: "a.target", ManifestBase: "manifest.json"},
		{RelPath: "Linked", Kind: FolderMod, UniqueID: "a.linked", ManifestBase: "manifest.json"},
		{RelPath: "Loop", Kind: FolderEmpty, Reason: reasonEmptyFolder},
		{RelPath: "Real", Kind: FolderMod, UniqueID: "a.real", ManifestBase: "manifest.json"},
		{RelPath: "Wrap", Kind: FolderMod, UniqueID: "a.target", ManifestBase: "manifest.json"},
	}
	if got := summarize(folders); !reflect.DeepEqual(got, want) {
		t.Fatalf("ScanWith = %#v, want %#v", got, want)
	}
	for _, folder := range folders {
		if folder.RelPath == "Linked" && folder.ManifestPath != filepath.Join(root, "Linked", "manifest.json") {
			t.Fatalf("Linked ManifestPath = %q, want the path through the symlink", folder.ManifestPath)
		}
	}
}

// TestScanFollowSymlinksSearchesLinkedGroups verifies a symlinked group folder is searched and a link to an ancestor is skipped.
func TestScanFollowSymlinksSearchesLinkedGroups(t *testing.T) {
	outside := t.TempDir()
	writeTree(t, outside, map[string]string{
		"Group/[CP] A/manifest.json": codeManifestJSON("a.cp", "A CP"),
		"Group/[JA] A/manifest.json": codeManifestJSON("a.ja", "A JA"),
	})
	root := t.TempDir()
	symlinkTree(t, root, map[string]string{
		"Group":        filepath.Join(outside, "Group"),
		"Group2":       filepath.Join(outside, "Group"),
		"Nest/Parent":  root,
		"Nest/Sibling": filepath.Join(outside, "Group"),
	})
	folders, err := ScanWith(root, ScanOptions{FollowSymlinks: true})
	if err != nil {
		t.Fatalf("ScanWith: %v", err)
	}
	want := []scanSummary{
		{RelPath: "Group/[CP] A", Kind: FolderMod, UniqueID: "a.cp", ManifestBase: "manifest.json"},
		{RelPath: "Group/[JA] A", Kind: FolderMod, UniqueID: "a.ja", ManifestBase: "manifest.json"},
		{RelPath: "Group2/[CP] A", Kind: FolderMod, UniqueID: "a.cp", ManifestBase: "manifest.json"},
		{RelPath: "Group2/[JA] A", Kind: FolderMod, UniqueID: "a.ja", ManifestBase: "manifest.json"},
		{RelPath: "Nest/Sibling/[CP] A", Kind: FolderMod, UniqueID: "a.cp", ManifestBase: "manifest.json"},
		{RelPath: "Nest/Sibling/[JA] A", Kind: FolderMod, UniqueID: "a.ja", ManifestBase: "manifest.json"},
	}
	if got := summarize(folders); !reflect.DeepEqual(got, want) {
		t.Fatalf("ScanWith = %#v, want %#v", got, want)
	}
	strict, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got := summarize(strict); !reflect.DeepEqual(got, []scanSummary{{RelPath: "Nest", Kind: FolderEmpty, Reason: reasonEmptyFolder}}) {
		t.Fatalf("Scan = %#v, want only the empty Nest folder", got)
	}
}

// TestReadManifestFileRefusesSymlinkWhenStrict verifies the strict manifest reader never opens a symlink.
func TestReadManifestFileRefusesSymlinkWhenStrict(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"target.json": codeManifestJSON("a.b", "B")})
	link := filepath.Join(dir, "manifest.json")
	if err := os.Symlink(filepath.Join(dir, "target.json"), link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if _, err := readManifestFile(link, false); err == nil {
		t.Fatal("readManifestFile(symlink, strict) error = nil, want error")
	}
	if _, err := readManifestFile(link, true); err != nil {
		t.Fatalf("readManifestFile(symlink, follow): %v", err)
	}
}

// TestScanRootErrors verifies Scan fails for a missing root or a file root.
func TestScanRootErrors(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	writeTree(t, root, map[string]string{"file": "x"})
	for _, path := range []string{filepath.Join(root, "missing"), file} {
		if _, err := Scan(path); err == nil {
			t.Fatalf("Scan(%s) error = nil, want error", path)
		}
	}
}

// TestIsRelevant verifies the ModScanner relevance filters for files and folders.
func TestIsRelevant(t *testing.T) {
	cases := []struct {
		name   string
		isFile bool
		want   bool
	}{
		{name: "Mod.dll", isFile: true, want: true},
		{name: "manifest.json", isFile: true, want: true},
		{name: "readme.TXT", isFile: true, want: false},
		{name: "notes.md", isFile: true, want: false},
		{name: "archive.7z", isFile: true, want: false},
		{name: "shortcut.lnk", isFile: true, want: false},
		{name: "bundle.tar.gz", isFile: true, want: true},
		{name: "trailing.", isFile: true, want: true},
		{name: ".hidden", isFile: true, want: false},
		{name: "._resource", isFile: true, want: false},
		{name: "Thumbs.DB", isFile: true, want: false},
		{name: "desktop.ini", isFile: true, want: false},
		{name: "__folder_managed_by_vortex", isFile: true, want: false},
		{name: "__MACOSX", isFile: false, want: false},
		{name: "__macosx", isFile: false, want: false},
		{name: "mcs", isFile: false, want: false},
		{name: "mcsMod", isFile: false, want: true},
		{name: ".git", isFile: false, want: true},
		{name: "._Foo", isFile: false, want: false},
		{name: "docs.txt", isFile: false, want: true},
	}
	for _, tc := range cases {
		if got := isRelevant(tc.name, tc.isFile); got != tc.want {
			t.Fatalf("isRelevant(%q, %v) = %v, want %v", tc.name, tc.isFile, got, tc.want)
		}
	}
}

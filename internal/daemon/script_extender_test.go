package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/gamedef"
)

func TestScriptExtenderInstallPaths(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "games", "Oblivion Remastered")
	tests := []struct {
		name       string
		def        gamedef.ScriptExtenderSource
		wantDir    string
		wantLoader string
	}{
		{
			name:       "legacy root install unchanged",
			def:        gamedef.ScriptExtenderSource{LoaderExe: "skse64_loader.exe"},
			wantDir:    root,
			wantLoader: "skse64_loader.exe",
		},
		{
			name: "nested OBSE64 install",
			def: gamedef.ScriptExtenderSource{
				LoaderExe:      "obse64_loader.exe",
				InstallSubpath: "OblivionRemastered/Binaries/Win64",
			},
			wantDir:    filepath.Join(root, "OblivionRemastered", "Binaries", "Win64"),
			wantLoader: "OblivionRemastered/Binaries/Win64/obse64_loader.exe",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, loader, err := scriptExtenderInstallPaths(root, tc.def)
			if err != nil {
				t.Fatal(err)
			}
			if dir != tc.wantDir {
				t.Errorf("install dir = %q, want %q", dir, tc.wantDir)
			}
			if loader != tc.wantLoader {
				t.Errorf("loader relative path = %q, want %q", loader, tc.wantLoader)
			}
		})
	}
}

func TestScriptExtenderInstallPathsRejectsEscape(t *testing.T) {
	_, _, err := scriptExtenderInstallPaths(t.TempDir(), gamedef.ScriptExtenderSource{
		LoaderExe:      "obse64_loader.exe",
		InstallSubpath: "../outside",
	})
	if err == nil {
		t.Fatal("expected escaping install subpath to be rejected")
	}
}

func TestCopyTreeReplacesDestinationSymlinkWithoutFollowingIt(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	outside := filepath.Join(t.TempDir(), "mod-source.dll")
	if err := os.WriteFile(filepath.Join(source, "loader.dll"), []byte("new loader"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("mod source"), 0644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(destination, "loader.dll")
	if err := os.Symlink(outside, target); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(source, destination); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "mod source" {
		t.Fatalf("symlink target changed: %q, %v", got, err)
	}
	if info, err := os.Lstat(target); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("destination was not replaced by a regular file: %v, %v", info, err)
	}
}

func TestCopyTreeRejectsDestinationDirectorySymlink(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "plugins"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "plugins", "extender.dll"), []byte("payload"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(destination, "plugins")); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(source, destination); err == nil {
		t.Fatal("destination directory symlink was accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "extender.dll")); !os.IsNotExist(err) {
		t.Fatalf("script extender escaped through destination symlink: %v", err)
	}
}

func TestNestedScriptExtenderManifestUsesInstallRootRelativePaths(t *testing.T) {
	src := t.TempDir()
	root := t.TempDir()
	subpath := "OblivionRemastered/Binaries/Win64"
	dst := filepath.Join(root, filepath.FromSlash(subpath))

	for name, contents := range map[string]string{
		"obse64_loader.exe":       "loader",
		"obse64_1_512_105.dll":    "core",
		"src/ignored-by-none.txt": "source payload",
	} {
		path := filepath.Join(src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := copyTree(src, dst); err != nil {
		t.Fatalf("copyTree: %v", err)
	}
	if err := writeScriptExtenderManifest("oblivionremastered", "OBSE64", src, root, subpath); err != nil {
		t.Fatalf("writeScriptExtenderManifest: %v", err)
	}

	manifest, err := loadScriptExtenderManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, entry := range manifest.Entries {
		got = append(got, entry.RelPath)
	}
	sort.Strings(got)
	want := []string{
		"OblivionRemastered/Binaries/Win64/obse64_1_512_105.dll",
		"OblivionRemastered/Binaries/Win64/obse64_loader.exe",
		"OblivionRemastered/Binaries/Win64/src/ignored-by-none.txt",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("manifest paths = %v, want install-root-relative %v", got, want)
	}
	if drifted, err := VerifyScriptExtenderManifest(root); err != nil || len(drifted) != 0 {
		t.Fatalf("fresh install drift = %v, err = %v", drifted, err)
	}

	if err := os.WriteFile(filepath.Join(dst, "obse64_1_512_105.dll"), []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	drifted, err := VerifyScriptExtenderManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(drifted, []string{"OblivionRemastered/Binaries/Win64/obse64_1_512_105.dll"}) {
		t.Fatalf("drifted = %v", drifted)
	}
}

// TestScriptExtenderMainFileOptions pins the I-32 Steam-build selection policy handed to download.SelectMainFile.
func TestScriptExtenderMainFileOptions(t *testing.T) {
	tests := []struct {
		name   string
		needle string
		want   download.MainFileOptions
	}{
		{
			name:   "without runtime needle",
			needle: "",
			want: download.MainFileOptions{
				RejectMentions: []string{"gog"},
				PreferMentions: []string{"steam"},
			},
		},
		{
			name:   "with runtime needle",
			needle: "1.6.1170",
			want: download.MainFileOptions{
				RejectMentions:      []string{"gog"},
				PreferMentions:      []string{"steam"},
				RequireVersionAnyOf: []string{"1.6.1170"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_ = t.TempDir()
			got := scriptExtenderMainFileOptions(tc.needle)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("scriptExtenderMainFileOptions(%q) = %#v, want %#v", tc.needle, got, tc.want)
			}
		})
	}
}

// TestScriptExtenderPolicySelectsSteamBuild verifies the daemon policy rejects GOG, filters runtimes, and picks the newest Steam file.
func TestScriptExtenderPolicySelectsSteamBuild(t *testing.T) {
	files := []download.NexusFileDetails{
		{FileID: 90, Name: "Steam/GOG 1.6.1170", CategoryName: "MAIN"},
		{FileID: 80, Name: "Steam build 1.6.640", CategoryName: "MAIN"},
		{FileID: 70, Name: "Universal build 1.6.1170", CategoryName: "MAIN"},
		{FileID: 30, Name: "Steam build", FileName: "se_1_6_1170.7z", CategoryName: "MAIN"},
		{FileID: 40, Name: "Steam update", Description: "runtime 1-6-1170", CategoryName: "MAIN"},
	}
	got, err := download.SelectMainFile(files, scriptExtenderMainFileOptions("1.6.1170"))
	if err != nil {
		t.Fatalf("SelectMainFile() error = %v", err)
	}
	if got.FileID != 40 {
		t.Fatalf("SelectMainFile() file ID = %d, want 40", got.FileID)
	}
}

// TestScriptExtenderSelectError verifies selection failures keep the legacy no-Steam-build message.
func TestScriptExtenderSelectError(t *testing.T) {
	const legacy = "no Steam-compatible MAIN-category file found for SKSE64 (only GOG builds available?)"
	tests := []struct {
		name       string
		err        error
		wantMsg    string
		wantUnwrap error
	}{
		{name: "no main file", err: download.ErrNoMainFile, wantMsg: legacy},
		{name: "wrapped no main file", err: fmt.Errorf("outer: %w", download.ErrNoMainFile), wantMsg: legacy},
		{
			name:       "other selection error",
			err:        download.ErrAmbiguousMainFile,
			wantMsg:    "selecting SKSE64 file: " + download.ErrAmbiguousMainFile.Error(),
			wantUnwrap: download.ErrAmbiguousMainFile,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_ = t.TempDir()
			got := scriptExtenderSelectError("SKSE64", tc.err)
			if got == nil || got.Error() != tc.wantMsg {
				t.Fatalf("scriptExtenderSelectError() = %v, want %q", got, tc.wantMsg)
			}
			if tc.wantUnwrap != nil && !errors.Is(got, tc.wantUnwrap) {
				t.Fatalf("scriptExtenderSelectError() = %v, want errors.Is(_, %v)", got, tc.wantUnwrap)
			}
		})
	}
}

// TestScriptExtenderArchiveName verifies Nexus-supplied file names cannot escape the download directory.
func TestScriptExtenderArchiveName(t *testing.T) {
	def := gamedef.ScriptExtenderSource{Name: "SKSE64"}
	const fallback = "SKSE64-77.archive"
	tests := []struct {
		name     string
		fileName string
		want     string
	}{
		{name: "parent traversal", fileName: "../../evil.7z", want: "evil.7z"},
		{name: "absolute path", fileName: "/abs/x.7z", want: "x.7z"},
		{name: "parent only", fileName: "..", want: fallback},
		{name: "current directory", fileName: ".", want: fallback},
		{name: "root only", fileName: "/", want: fallback},
		{name: "empty", fileName: "", want: fallback},
		{name: "normal", fileName: "skse64_2_02_06.7z", want: "skse64_2_02_06.7z"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			destDir := t.TempDir()
			got := scriptExtenderArchiveName(def, &download.NexusFileDetails{FileID: 77, FileName: tc.fileName})
			if got != tc.want {
				t.Fatalf("scriptExtenderArchiveName(%q) = %q, want %q", tc.fileName, got, tc.want)
			}
			if dir := filepath.Dir(filepath.Join(destDir, got)); dir != destDir {
				t.Fatalf("archive path parent = %q, want %q", dir, destDir)
			}
		})
	}
}

// TestScriptExtenderManifestIsReplacedAtomically locks the manifest's bytes and mode across a rewrite with no temporary file left in the game root.
func TestScriptExtenderManifestIsReplacedAtomically(t *testing.T) {
	root := t.TempDir()
	for _, extender := range []string{"skse64 2.2.5", "skse64 2.2.6"} {
		manifest := seInstallManifest{GameID: "skyrimse", ExtenderName: extender, Entries: []seManifestEntry{{RelPath: "skse64_loader.exe", Size: 3, SHA256: "abc"}}}
		if err := saveScriptExtenderManifest(root, manifest); err != nil {
			t.Fatalf("saveScriptExtenderManifest(%s): %v", extender, err)
		}
	}
	info, err := os.Stat(filepath.Join(root, seManifestFilename))
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("manifest = %v, %v; want mode 0644", info, err)
	}
	loaded, err := loadScriptExtenderManifest(root)
	if err != nil || loaded.ExtenderName != "skse64 2.2.6" || len(loaded.Entries) != 1 || loaded.Entries[0].RelPath != "skse64_loader.exe" {
		t.Fatalf("loaded manifest = %+v, %v; want the second write", loaded, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("game root holds %v after two manifest writes, want only the manifest", entries)
	}
}

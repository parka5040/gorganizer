package daemon

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

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

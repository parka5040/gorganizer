package smapi

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	testVanillaLauncher = "#!/bin/bash\ncd \"$(dirname \"$0\")\"\n./\"Stardew Valley\" $@\n"
	testSMAPILauncher   = "#!/usr/bin/env bash\ncd \"$(dirname \"$0\")\"\n./StardewModdingAPI \"$@\"\n"
	testDepsJSON        = `{"targets":{".NETCoreApp,Version=v6.0":{"Stardew Valley/1.6.15.24356":{}}}}`
)

type zipEntry struct {
	name string
	data []byte
	mode os.FileMode
}

type recordingKiller struct {
	mu    sync.Mutex
	calls [][2]uint64
}

type fakeRunner struct {
	mode   string
	spec   LoaderSpec
	during func(stage string) error
	mu     sync.Mutex
	calls  []Command
	pids   [][2]uint64
}

// testSpec returns the Stardew Valley loader spec mirrored from the game registry.
func testSpec() LoaderSpec {
	return LoaderSpec{
		InstallerRelPath:    "internal/linux/SMAPI.Installer",
		PayloadRelPath:      "internal/linux/install.dat",
		LauncherName:        "StardewValley",
		LauncherBackupName:  "StardewValley-original",
		LauncherPayloadName: "unix-launcher.sh",
		LoaderExecutable:    "StardewModdingAPI",
		GameAssembly:        "Stardew Valley.dll",
		GameVersionFile:     "Stardew Valley.deps.json",
		GameVersionPackage:  "Stardew Valley",
		LoaderDepsFile:      "StardewModdingAPI.deps.json",
		NativeMarkers:       []string{"Stardew Valley"},
		ForeignMarkers:      []string{"Stardew Valley.exe"},
		BundledModIDs:       []string{"SMAPI.ConsoleCommands", "SMAPI.SaveBackup"},
		UninstallPaths: []string{
			"StardewModdingAPI", "StardewModdingAPI.deps.json", "StardewModdingAPI.dll",
			"StardewModdingAPI.exe", "StardewModdingAPI.exe.config", "StardewModdingAPI.exe.mdb",
			"StardewModdingAPI.pdb", "StardewModdingAPI.runtimeconfig.json", "StardewModdingAPI.xml",
			"smapi-internal", "libgdiplus.dylib", "Mods/.cache", "Mods/ErrorHandler", "Mods/TrainerMod",
			"Mono.Cecil.Rocks.dll", "StardewModdingAPI-settings.json", "StardewModdingAPI.AssemblyRewriters.dll",
			"0Harmony.dll", "0Harmony.pdb", "Mono.Cecil.dll", "Newtonsoft.Json.dll",
			"StardewModdingAPI.config.json", "StardewModdingAPI.crash.marker", "StardewModdingAPI.metadata.json",
			"StardewModdingAPI.update.marker", "StardewModdingAPI.Toolkit.dll", "StardewModdingAPI.Toolkit.pdb",
			"StardewModdingAPI.Toolkit.xml", "StardewModdingAPI.Toolkit.CoreInterfaces.dll",
			"StardewModdingAPI.Toolkit.CoreInterfaces.pdb", "StardewModdingAPI.Toolkit.CoreInterfaces.xml",
			"StardewModdingAPI-x64.exe",
		},
		Farm: FarmGuard{
			DeployDir:       "Mods",
			Sentinel:        ".gorganizer-overlay.json",
			SiblingSuffixes: []string{".gorganizer-activating", ".gorganizer-applying", ".gorganizer-staging", ".gorganizer-oldfarm", ".orig"},
		},
	}
}

// bundledManifest returns a bundled-mod manifest for id at version.
func bundledManifest(id, version string) string {
	return fmt.Sprintf(`{"Name": %q, "Author": "SMAPI", "Version": %q, "UniqueId": %q, "EntryDll": "Mod.dll"}`, id, version, id)
}

// testPayloadFiles returns the synthetic install.dat content for version.
func testPayloadFiles(version string) map[string]string {
	return map[string]string{
		"smapi-internal/config.json":               `{"CheckForUpdates": true}`,
		"smapi-internal/SMAPI.Toolkit.dll":         "toolkit " + version,
		"smapi-internal/i18n/default.json":         `{"hello": "world"}`,
		"StardewModdingAPI":                        "\x7fELF loader " + version,
		"StardewModdingAPI.dll":                    "assembly " + version,
		"StardewModdingAPI.runtimeconfig.json":     `{"runtimeOptions": {}}`,
		"StardewModdingAPI.xml":                    "<doc/>",
		"steam_appid.txt":                          "413150",
		"unix-launcher.sh":                         testSMAPILauncher,
		"Mods/ConsoleCommands/manifest.json":       bundledManifest("SMAPI.ConsoleCommands", version),
		"Mods/ConsoleCommands/ConsoleCommands.dll": "console " + version,
		"Mods/SaveBackup/manifest.json":            bundledManifest("SMAPI.SaveBackup", version),
		"Mods/SaveBackup/SaveBackup.dll":           "backup " + version,
		"Mods/Stray/manifest.json":                 bundledManifest("Someone.Stray", "1.0.0"),
		"Mods/Stray/Stray.dll":                     "stray",
	}
}

// buildZip returns zip bytes for entries in the given order, adding directory entries for names ending in a slash.
func buildZip(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate, Modified: time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)}
		mode := entry.mode
		if mode == 0 {
			mode = 0644
			if strings.HasSuffix(entry.name, "/") {
				mode = os.ModeDir | 0755
			}
		}
		header.SetMode(mode)
		w, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("zip header %s: %v", entry.name, err)
		}
		if _, err := w.Write(entry.data); err != nil {
			t.Fatalf("zip write %s: %v", entry.name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// payloadZip builds install.dat bytes from a file map, including explicit directory entries.
func payloadZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	dirs := map[string]bool{}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
		for dir := filepath.Dir(name); dir != "."; dir = filepath.Dir(dir) {
			dirs[dir+"/"] = true
		}
	}
	sort.Strings(names)
	var entries []zipEntry
	for _, dir := range sortedSet(dirs) {
		entries = append(entries, zipEntry{name: dir})
	}
	for _, name := range names {
		entries = append(entries, zipEntry{name: name, data: []byte(files[name])})
	}
	return buildZip(t, entries)
}

// writeArtifact writes a synthetic installer bundle zip for version into dir and returns it as a verified artifact.
func writeArtifact(t *testing.T, dir, version string, payload map[string]string) Artifact {
	t.Helper()
	top := "SMAPI " + version + " installer/"
	data := buildZip(t, []zipEntry{
		{name: top},
		{name: top + "install on Linux.sh", data: []byte("#!/usr/bin/env bash\n")},
		{name: top + "internal/"},
		{name: top + "internal/linux/"},
		{name: top + "internal/linux/SMAPI.Installer", data: []byte("\x7fELF fake installer")},
		{name: top + "internal/linux/install.dat", data: payloadZip(t, payload)},
		{name: top + "README.txt", data: []byte("readme")},
	})
	name := "SMAPI-" + version + "-installer.zip"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("writing artifact: %v", err)
	}
	sum := sha256.Sum256(data)
	return Artifact{Version: version, Tag: version, AssetName: name, SHA256: hex.EncodeToString(sum[:]), ZipPath: path}
}

// newTestGame creates a stub native Stardew Valley installation and returns its directory.
func newTestGame(t *testing.T) string {
	t.Helper()
	game := filepath.Join(t.TempDir(), "Stardew Valley")
	files := []struct {
		rel  string
		data string
		mode os.FileMode
	}{
		{"Stardew Valley", "\x7fELF game", 0755},
		{"Stardew Valley.dll", "game assembly", 0644},
		{"Stardew Valley.deps.json", testDepsJSON, 0644},
		{"StardewValley", testVanillaLauncher, 0755},
		{"steam_appid.txt", "480", 0644},
		{"Content/Data/Fish.xnb", "fish", 0644},
	}
	for _, f := range files {
		full := filepath.Join(game, filepath.FromSlash(f.rel))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(f.data), f.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(full, f.mode); err != nil {
			t.Fatal(err)
		}
	}
	return game
}

// newTestInstaller returns an installer using runner with a fixed clock.
func newTestInstaller(runner Runner) *Installer {
	return &Installer{
		Spec:   testSpec(),
		Runner: runner,
		Now:    func() time.Time { return time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC) },
	}
}

// kill records a recovery kill request without signalling anything.
func (k *recordingKiller) kill(pid int, start uint64) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.calls = append(k.calls, [2]uint64{uint64(pid), start})
	return nil
}

// Run simulates the upstream SMAPI installer against the staged game path while a real sleep child stands in for its process.
func (f *fakeRunner) Run(ctx context.Context, cmd Command, onStart func(pid int)) (Result, error) {
	child := exec.Command("/bin/sleep", "30")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		return Result{ExitCode: -1}, err
	}
	defer func() {
		_ = syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
		_ = child.Wait()
	}()
	start, err := ProcessStartTime(child.Process.Pid)
	if err != nil {
		return Result{ExitCode: -1}, err
	}
	f.mu.Lock()
	f.calls = append(f.calls, cmd)
	f.pids = append(f.pids, [2]uint64{uint64(child.Process.Pid), start})
	f.mu.Unlock()
	if onStart != nil {
		onStart(child.Process.Pid)
	}
	stage := argAfter(cmd.Args, "--game-path")
	switch f.mode {
	case "noop":
		return Result{Output: []byte("SMAPI is installed! Launch the game the same way as before to play with mods.\n")}, nil
	case "hang":
		if cmd.Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, cmd.Timeout)
			defer cancel()
		}
		<-ctx.Done()
		return Result{ExitCode: -1, Output: []byte("still working")}, fmt.Errorf("running fake installer: %w", ctx.Err())
	case "exit":
		return Result{ExitCode: 3, Output: []byte("Unhandled exception: boom")}, nil
	}
	if err := simulateUpstream(stage, filepath.Join(cmd.Dir, "install.dat"), f.spec); err != nil {
		return Result{ExitCode: 0, Output: []byte(err.Error())}, nil
	}
	switch f.mode {
	case "garbage":
		if err := os.WriteFile(filepath.Join(stage, "garbage.txt"), []byte("junk"), 0644); err != nil {
			return Result{}, err
		}
	case "hardlink":
		if err := os.Link(filepath.Join(stage, "smapi-internal", "config.json"), filepath.Join(filepath.Dir(stage), "outside-link")); err != nil {
			return Result{}, err
		}
	}
	if f.during != nil {
		if err := f.during(stage); err != nil {
			return Result{}, err
		}
	}
	return Result{Output: []byte("SMAPI is installed!\n")}, nil
}

// argAfter returns the argument following flag.
func argAfter(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

// simulateUpstream reproduces the upstream installer's install effects on stage from the payload.
func simulateUpstream(stage, payloadPath string, spec LoaderSpec) error {
	reader, err := zip.OpenReader(payloadPath)
	if err != nil {
		return err
	}
	defer reader.Close()
	var carried []byte
	if data, err := os.ReadFile(filepath.Join(stage, "smapi-internal", "config.user.json")); err == nil {
		carried = data
	}
	launcher := filepath.Join(stage, spec.LauncherName)
	backup := filepath.Join(stage, spec.LauncherBackupName)
	if _, err := os.Stat(backup); err == nil {
		_ = os.Remove(launcher)
		if err := os.Rename(backup, launcher); err != nil {
			return err
		}
	}
	for _, rel := range spec.UninstallPaths {
		_ = os.RemoveAll(filepath.Join(stage, filepath.FromSlash(rel)))
	}
	contents := map[string][]byte{}
	for _, entry := range reader.File {
		if strings.HasSuffix(entry.Name, "/") {
			continue
		}
		rc, err := entry.Open()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return err
		}
		contents[entry.Name] = data
		if strings.HasPrefix(entry.Name, "Mods/") {
			continue
		}
		if err := writeStageFile(stage, entry.Name, data, 0744); err != nil {
			return err
		}
	}
	if carried != nil {
		if err := writeStageFile(stage, "smapi-internal/config.user.json", carried, 0644); err != nil {
			return err
		}
	}
	if _, err := os.Stat(launcher); err == nil {
		if _, err := os.Stat(backup); err != nil {
			if err := os.Rename(launcher, backup); err != nil {
				return err
			}
		} else if err := os.Remove(launcher); err != nil {
			return err
		}
	}
	if err := os.Rename(filepath.Join(stage, spec.LauncherPayloadName), launcher); err != nil {
		return err
	}
	deps, err := os.ReadFile(filepath.Join(stage, spec.GameVersionFile))
	if err != nil {
		return err
	}
	if err := writeStageFile(stage, spec.LoaderDepsFile, deps, 0644); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(stage, "Mods"), 0755); err != nil {
		return err
	}
	for name, data := range contents {
		parts := strings.Split(name, "/")
		if len(parts) != 3 || parts[0] != "Mods" || parts[2] != "manifest.json" {
			continue
		}
		manifest, err := ParseManifest(data)
		if err != nil || !contains(spec.BundledModIDs, manifest.UniqueID) {
			continue
		}
		for other, otherData := range contents {
			if strings.HasPrefix(other, "Mods/"+parts[1]+"/") {
				if err := writeStageFile(stage, other, otherData, 0644); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// writeStageFile writes a stage file, creating parents and replacing any existing file.
func writeStageFile(stage, rel string, data []byte, mode os.FileMode) error {
	full := filepath.Join(stage, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		return err
	}
	return os.WriteFile(full, data, mode)
}

// snapshotGame hashes every entry of gameDir with its mode, skipping the transaction scratch paths and noting the record only by presence.
func snapshotGame(t *testing.T, gameDir string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(gameDir, func(full string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if full == gameDir {
			return nil
		}
		rel, err := filepath.Rel(gameDir, full)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch rel {
		case IntentFile, BackupDir, WorkDir:
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		case RecordFile:
			snap[rel] = "record"
			return nil
		}
		info, err := os.Lstat(full)
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(full)
			if err != nil {
				return err
			}
			snap[rel] = "link " + target
		case info.IsDir():
			snap[rel] = fmt.Sprintf("dir %o", info.Mode().Perm())
		default:
			data, err := os.ReadFile(full)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			snap[rel] = fmt.Sprintf("file %o %x", info.Mode().Perm(), sum[:8])
		}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", gameDir, err)
	}
	return snap
}

// diffSnapshots describes the differences between two snapshots.
func diffSnapshots(want, got map[string]string) string {
	keys := map[string]bool{}
	for key := range want {
		keys[key] = true
	}
	for key := range got {
		keys[key] = true
	}
	var lines []string
	for _, key := range sortedSet(keys) {
		if want[key] != got[key] {
			lines = append(lines, fmt.Sprintf("%s: want %q, got %q", key, want[key], got[key]))
		}
	}
	return strings.Join(lines, "\n")
}

// assertSnapshot fails the test when got differs from want.
func assertSnapshot(t *testing.T, label string, want, got map[string]string) {
	t.Helper()
	if diff := diffSnapshots(want, got); diff != "" {
		t.Fatalf("%s: game tree differs:\n%s", label, diff)
	}
}

// assertNoScratch fails the test when an intent, backup, or work directory remains.
func assertNoScratch(t *testing.T, gameDir string) {
	t.Helper()
	for _, name := range []string{IntentFile, BackupDir, WorkDir} {
		if _, err := os.Lstat(filepath.Join(gameDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s still exists (err=%v)", name, err)
		}
	}
}

// readGameFile returns the content of a game file or fails the test.
func readGameFile(t *testing.T, gameDir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(gameDir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(data)
}

// fileMode returns the permission bits of a game file or fails the test.
func fileMode(t *testing.T, gameDir, rel string) os.FileMode {
	t.Helper()
	info, err := os.Lstat(filepath.Join(gameDir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("stat %s: %v", rel, err)
	}
	return info.Mode().Perm()
}

// assertMissing fails the test when rel exists in gameDir.
func assertMissing(t *testing.T, gameDir, rel string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(gameDir, filepath.FromSlash(rel))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s should not exist (err=%v)", rel, err)
	}
}

// installed runs a successful fake install of art into a fresh game and returns the game directory.
func installed(t *testing.T, art Artifact) string {
	t.Helper()
	game := newTestGame(t)
	in := newTestInstaller(&fakeRunner{spec: testSpec()})
	if _, err := in.Install(context.Background(), game, art); err != nil {
		t.Fatalf("Install: %v", err)
	}
	return game
}

// writeTestFile writes data to rel below root with mode, creating parent directories.
func writeTestFile(t *testing.T, root, rel, data string, mode os.FileMode) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(full, mode); err != nil {
		t.Fatal(err)
	}
}

// moveTestPath renames from to to below root, creating the destination parent.
func moveTestPath(t *testing.T, root, from, to string) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(to))
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, filepath.FromSlash(from)), target); err != nil {
		t.Fatal(err)
	}
}

// externalSMAPI installs the payload of art straight into game the way the upstream installer would, leaving no gorganizer record.
func externalSMAPI(t *testing.T, game string, art Artifact) {
	t.Helper()
	bundle, err := OpenBundle(art, t.TempDir(), testSpec())
	if err != nil {
		t.Fatalf("OpenBundle: %v", err)
	}
	if err := simulateUpstream(game, bundle.PayloadPath, testSpec()); err != nil {
		t.Fatalf("simulating upstream install: %v", err)
	}
	for _, rel := range []string{"StardewValley", "StardewModdingAPI"} {
		if err := os.Chmod(filepath.Join(game, rel), 0755); err != nil {
			t.Fatal(err)
		}
	}
}

// hashTreeForTest maps every entry below root to its type, mode, and content digest.
func hashTreeForTest(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(full string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(full)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, full)
		switch {
		case info.IsDir():
			out[rel] = fmt.Sprintf("dir %o", info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			target, _ := os.Readlink(full)
			out[rel] = "link " + target
		default:
			data, err := os.ReadFile(full)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			out[rel] = fmt.Sprintf("file %o %x", info.Mode().Perm(), sum)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("hashing %s: %v", root, err)
	}
	return out
}

// cloneGame copies the tree at src into a fresh temporary game directory, preserving modes.
func cloneGame(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "Stardew Valley")
	var dirs []string
	modes := map[string]os.FileMode{}
	err := filepath.WalkDir(src, func(full string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, full)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := os.Lstat(full)
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			dirs = append(dirs, target)
			modes[target] = info.Mode().Perm()
			return os.MkdirAll(target, 0755)
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(full)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		default:
			data, err := os.ReadFile(full)
			if err != nil {
				return err
			}
			if err := os.WriteFile(target, data, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chmod(target, info.Mode().Perm())
		}
	})
	if err != nil {
		t.Fatalf("cloning %s: %v", src, err)
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i], modes[dirs[i]]); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

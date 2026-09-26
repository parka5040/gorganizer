//go:build smapi_integration

package smapi

import (
	"archive/zip"
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
	"testing"
	"time"
)

const (
	integrationZipSHA256 = "dd01ddca7b566bfe0d3b3d2d03833496abc56c53da976241f2ab443f5484acc4"
	integrationVersion   = "4.5.2"
	stubLauncher         = "#!/bin/bash\ncd \"$(dirname \"$0\")\"\n./\"Stardew Valley\" $@\n"
)

type observedRun struct {
	path         string
	exitCode     int
	err          error
	duration     time.Duration
	output       string
	launcherMode os.FileMode
	loaderMode   os.FileMode
	stage        []string
	home         []string
	tmp          []string
	bundleExtras []string
	oddModes     []string
}

type observingRunner struct {
	t       *testing.T
	inner   Runner
	zipPath string
	runs    []observedRun
}

// Run executes the real installer and records what it left in the stage, redirected home, temp dir, and bundle.
func (o *observingRunner) Run(ctx context.Context, cmd Command, onStart func(pid int)) (Result, error) {
	started := time.Now()
	result, err := o.inner.Run(ctx, cmd, onStart)
	run := observedRun{exitCode: result.ExitCode, err: err, duration: time.Since(started), output: string(result.Output)}
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "PATH=") {
			run.path = strings.TrimPrefix(kv, "PATH=")
		}
	}
	stage := argAfter(cmd.Args, "--game-path")
	work := filepath.Dir(filepath.Dir(stage))
	run.launcherMode = modeOf(filepath.Join(stage, "StardewValley"))
	run.loaderMode = modeOf(filepath.Join(stage, "StardewModdingAPI"))
	run.stage = listTree(o.t, stage)
	run.home = listTree(o.t, filepath.Join(work, "home"))
	run.tmp = listTree(o.t, filepath.Join(work, "tmp"))
	bundleRoot := filepath.Dir(filepath.Dir(cmd.Dir))
	run.bundleExtras = bundleExtras(o.t, o.zipPath, filepath.Dir(bundleRoot))
	run.oddModes = oddModes(stage)
	o.runs = append(o.runs, run)
	o.t.Logf("installer run %d: exit=%d err=%v duration=%s launcherMode=%o loaderMode=%o", len(o.runs), run.exitCode, run.err, run.duration.Round(time.Millisecond), run.launcherMode, run.loaderMode)
	o.t.Logf("installer run %d output tail:\n%s", len(o.runs), tail(run.output, 3000))
	o.t.Logf("installer run %d stage tree: %s", len(o.runs), strings.Join(run.stage, ", "))
	o.t.Logf("installer run %d redirected HOME entries: %v", len(o.runs), run.home)
	o.t.Logf("installer run %d redirected TMPDIR entries: %v", len(o.runs), run.tmp)
	o.t.Logf("installer run %d files added to the extracted bundle: %v", len(o.runs), run.bundleExtras)
	o.t.Logf("installer run %d stage files whose mode is not 0644 before normalization: %v", len(o.runs), run.oddModes)
	return result, err
}

// modeOf returns the permission bits of path, or zero when it cannot be read.
func modeOf(path string) os.FileMode {
	info, err := os.Lstat(path)
	if err != nil {
		return 0
	}
	return info.Mode().Perm()
}

// oddModes lists stage files whose permission bits are not 0644.
func oddModes(root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(full string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		info, err := os.Lstat(full)
		if err != nil {
			return nil
		}
		if info.Mode().Perm() != 0644 {
			rel, _ := filepath.Rel(root, full)
			out = append(out, fmt.Sprintf("%s=%o", filepath.ToSlash(rel), info.Mode().Perm()))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// listTree lists every entry below root as slash paths with a trailing slash for directories.
func listTree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(full string, entry fs.DirEntry, err error) error {
		if err != nil || full == root {
			return nil
		}
		rel, _ := filepath.Rel(root, full)
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			rel += "/"
		} else if entry.Type()&os.ModeSymlink != 0 {
			rel += " (symlink)"
		}
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out
}

// bundleExtras lists files under dest that the release zip does not contain.
func bundleExtras(t *testing.T, zipPath, dest string) []string {
	t.Helper()
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("opening release zip: %v", err)
	}
	defer reader.Close()
	known := map[string]bool{}
	for _, entry := range reader.File {
		known[strings.TrimSuffix(entry.Name, "/")] = true
	}
	var extras []string
	for _, rel := range listTree(t, dest) {
		if !known[strings.TrimSuffix(rel, "/")] {
			extras = append(extras, rel)
		}
	}
	return extras
}

// tail returns at most n trailing bytes of s.
func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// hashTree maps every entry below root to its type, mode, and content digest.
func hashTree(t *testing.T, root string) map[string]string {
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
			out[rel] = fmt.Sprintf("dir %o %d", info.Mode().Perm(), info.ModTime().UnixNano())
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

// statSummary describes a path's existence, type, and modification time without reading its content.
func statSummary(path string) string {
	info, err := os.Lstat(path)
	if err != nil {
		return "absent"
	}
	return fmt.Sprintf("%s %d", info.Mode(), info.ModTime().UnixNano())
}

// tmpEntries returns the names in dir that look like .NET or SMAPI scratch entries.
func tmpEntries(dir string) map[string]bool {
	out := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, entry := range entries {
		name := entry.Name()
		lower := strings.ToLower(name)
		if strings.Contains(lower, "smapi") || strings.Contains(lower, "dotnet") || strings.HasPrefix(lower, "clr-debug") || strings.HasPrefix(lower, ".net") {
			out[name] = true
		}
	}
	return out
}

// newStubGame builds a realistic native Stardew Valley stub whose executable is a copy of /bin/true.
func newStubGame(t *testing.T) string {
	t.Helper()
	game := filepath.Join(t.TempDir(), "Stardew Valley")
	if err := os.MkdirAll(game, 0755); err != nil {
		t.Fatal(err)
	}
	src, err := os.Open("/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(filepath.Join(game, "Stardew Valley"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"Stardew Valley.dll":       "stub game assembly",
		"Stardew Valley.deps.json": `{"targets":{".NETCoreApp,Version=v6.0":{"Stardew Valley/1.6.15.24356":{}}}}`,
		"steam_appid.txt":          "480",
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(game, name), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(game, "StardewValley"), []byte(stubLauncher), 0755); err != nil {
		t.Fatal(err)
	}
	return game
}

// isolateIntegration skips unless the real release zip is configured, redirects the test HOME to a canary, and returns the artifact plus a final isolation check.
func isolateIntegration(t *testing.T) (Artifact, func()) {
	t.Helper()
	zipPath := os.Getenv("GORGANIZER_SMAPI_ZIP")
	if zipPath == "" {
		t.Skip("GORGANIZER_SMAPI_ZIP is not set")
	}
	if err := verifyFileDigest(zipPath, integrationZipSHA256); err != nil {
		t.Skipf("GORGANIZER_SMAPI_ZIP is not the SMAPI %s installer: %v", integrationVersion, err)
	}
	watchedBefore := map[string]string{}
	if realHome, err := os.UserHomeDir(); err == nil && realHome != "" {
		for _, path := range []string{
			filepath.Join(realHome, ".config", "StardewValley"),
			filepath.Join(realHome, ".config", "StardewValley", "ErrorLogs"),
			filepath.Join(realHome, ".local", "share", "StardewValley"),
			filepath.Join(realHome, ".dotnet"),
		} {
			watchedBefore[path] = statSummary(path)
		}
	}
	tmpBefore := tmpEntries(os.TempDir())
	systemTmpBefore := tmpEntries("/tmp")
	canary := t.TempDir()
	for _, dir := range []string{".config/StardewValley", ".local/share", ".cache"} {
		if err := os.MkdirAll(filepath.Join(canary, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(canary, ".config", "StardewValley", "startup_preferences"), []byte("canary"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", canary)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(canary, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(canary, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(canary, ".cache"))
	canaryBefore := hashTree(t, canary)
	art := Artifact{Version: integrationVersion, Tag: integrationVersion, AssetName: filepath.Base(zipPath), SHA256: integrationZipSHA256, ZipPath: zipPath}
	return art, func() {
		t.Helper()
		if diff := diffSnapshots(canaryBefore, hashTree(t, canary)); diff != "" {
			t.Fatalf("canary HOME changed:\n%s", diff)
		}
		for path, before := range watchedBefore {
			if after := statSummary(path); after != before {
				t.Fatalf("%s changed outside the temp dirs: %s -> %s", path, before, after)
			}
		}
		for label, pair := range map[string][2]map[string]bool{
			os.TempDir(): {tmpBefore, tmpEntries(os.TempDir())},
			"/tmp":       {systemTmpBefore, tmpEntries("/tmp")},
		} {
			for name := range pair[1] {
				if !pair[0][name] {
					t.Fatalf("new .NET/SMAPI entry %s appeared in %s", name, label)
				}
			}
		}
	}
}

// TestIntegrationRealInstaller runs the real SMAPI installer through the staged transaction against a stub game in a temp dir.
func TestIntegrationRealInstaller(t *testing.T) {
	art, checkIsolation := isolateIntegration(t)
	zipPath := art.ZipPath
	game := newStubGame(t)
	runner := &observingRunner{t: t, inner: ExecRunner{}, zipPath: zipPath}
	in := &Installer{Spec: testSpec(), Runner: runner, Timeout: 2 * time.Minute}
	in.Progress = func(phase, detail string) { t.Logf("progress: %s %s", phase, detail) }
	ctx := context.Background()

	record, err := in.Install(ctx, game, art)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	t.Logf("record: version=%s files=%d originals=%v", record.Version, len(record.Files), record.Originals)
	assertIntegrationOK(t, game)
	if got := readGameFile(t, game, "steam_appid.txt"); got != "413150" {
		t.Fatalf("steam_appid.txt after install = %q", got)
	}
	if got := readGameFile(t, game, OriginalsDir+"/steam_appid.txt"); got != "480" {
		t.Fatalf("preserved steam_appid.txt = %q", got)
	}
	if got := readGameFile(t, game, "StardewValley-original"); got != stubLauncher {
		t.Fatalf("launcher backup = %q", got)
	}

	if _, err := in.Install(ctx, game, art); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	assertIntegrationOK(t, game)

	copyTestFile(t, filepath.Join(game, "StardewValley-original"), filepath.Join(game, "StardewValley"))
	status, err := Inspect(game, testSpec())
	if err != nil || status.State != StateLauncherReverted {
		t.Fatalf("Inspect after simulated Steam revert = %+v, %v", status, err)
	}
	if _, err := in.Install(ctx, game, art); err != nil {
		t.Fatalf("repair: %v", err)
	}
	assertIntegrationOK(t, game)

	if err := in.Uninstall(ctx, game); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if got := readGameFile(t, game, "StardewValley"); got != stubLauncher {
		t.Fatalf("launcher after uninstall = %q", got)
	}
	if got := readGameFile(t, game, "steam_appid.txt"); got != "480" {
		t.Fatalf("steam_appid.txt after uninstall = %q", got)
	}
	status, err = Inspect(game, testSpec())
	if err != nil || status.State != StateNotInstalled || status.Managed {
		t.Fatalf("Inspect after uninstall = %+v, %v", status, err)
	}
	for _, rel := range []string{"StardewValley-original", "StardewModdingAPI", "StardewModdingAPI.dll", "StardewModdingAPI.deps.json", "smapi-internal", RecordFile, OriginalsDir} {
		assertMissing(t, game, rel)
	}
	assertNoScratch(t, game)
	t.Logf("game tree after uninstall: %s", strings.Join(listTree(t, game), ", "))

	if len(runner.runs) != 3 {
		t.Fatalf("installer ran %d times, want 3", len(runner.runs))
	}
	for i, run := range runner.runs {
		if run.exitCode != 0 || run.err != nil {
			t.Fatalf("run %d: exit=%d err=%v", i+1, run.exitCode, run.err)
		}
		if run.path != "/usr/bin:/bin" {
			t.Fatalf("run %d: installer PATH = %q", i+1, run.path)
		}
	}
	checkIsolation()
}

// assertIntegrationOK checks that the game holds a complete managed SMAPI install.
func assertIntegrationOK(t *testing.T, game string) {
	t.Helper()
	status, err := Inspect(game, testSpec())
	if err != nil || status.State != StateOK || !status.Managed || status.Version != integrationVersion {
		t.Fatalf("Inspect = %+v, %v", status, err)
	}
	if mode := fileMode(t, game, "StardewModdingAPI"); mode&0111 == 0 {
		t.Fatalf("StardewModdingAPI mode = %o", mode)
	}
	if !isPlainDir(filepath.Join(game, "smapi-internal")) {
		t.Fatal("smapi-internal missing")
	}
	for _, rel := range []string{"Mods/ConsoleCommands/manifest.json", "Mods/SaveBackup/manifest.json", RecordFile} {
		readGameFile(t, game, rel)
	}
	assertNoScratch(t, game)
}

// verifyFileDigest checks that path has the given SHA-256 digest.
func verifyFileDigest(path, want string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != want {
		return errors.New("digest " + got)
	}
	return nil
}

// TestIntegrationExitZeroOnError proves the real installer exits 0 after refusing a game path and that the stage check rejects such a run.
func TestIntegrationExitZeroOnError(t *testing.T) {
	art, checkIsolation := isolateIntegration(t)
	bundle, err := OpenBundle(art, filepath.Join(t.TempDir(), "bundle"), testSpec())
	if err != nil {
		t.Fatalf("OpenBundle: %v", err)
	}
	env, err := installerEnv(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, gamePath := range map[string]string{
		"empty folder":   t.TempDir(),
		"missing folder": filepath.Join(t.TempDir(), "missing"),
	} {
		result, err := ExecRunner{}.Run(context.Background(), Command{
			Path:    bundle.InstallerPath,
			Args:    []string{"--install", "--no-prompt", "--game-path", gamePath},
			Dir:     filepath.Dir(bundle.InstallerPath),
			Env:     env,
			Timeout: 2 * time.Minute,
		}, nil)
		t.Logf("%s: exit=%d err=%v output:\n%s", name, result.ExitCode, err, tail(string(result.Output), 1500))
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("%s: expected exit 0 after an installer error, got exit=%d err=%v", name, result.ExitCode, err)
		}
		if strings.Contains(string(result.Output), "SMAPI is installed") {
			t.Fatalf("%s: installer claimed success", name)
		}
	}
	game := newStubGame(t)
	before := snapshotGame(t, game)
	redirect := &redirectingRunner{inner: ExecRunner{}, gamePath: t.TempDir()}
	_, err = (&Installer{Spec: testSpec(), Runner: redirect, Timeout: 2 * time.Minute}).Install(context.Background(), game, art)
	t.Logf("install whose installer refused its game path: %v", err)
	if !errors.Is(err, ErrStageIncomplete) {
		t.Fatalf("Install error = %v, want ErrStageIncomplete", err)
	}
	assertSnapshot(t, "after exit-0 failure", before, snapshotGame(t, game))
	assertNoScratch(t, game)
	checkIsolation()
}

type pathPrependRunner struct {
	inner Runner
	dir   string
}

// Run prepends dir to the command's PATH before delegating so a wrapper can observe what the installer executes.
func (r *pathPrependRunner) Run(ctx context.Context, cmd Command, onStart func(pid int)) (Result, error) {
	env := append([]string{}, cmd.Env...)
	for i, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			env[i] = "PATH=" + r.dir + string(os.PathListSeparator) + strings.TrimPrefix(kv, "PATH=")
		}
	}
	cmd.Env = env
	return r.inner.Run(ctx, cmd, onStart)
}

type redirectingRunner struct {
	inner    Runner
	gamePath string
}

// Run replaces the game path argument before delegating to the inner runner.
func (r *redirectingRunner) Run(ctx context.Context, cmd Command, onStart func(pid int)) (Result, error) {
	args := append([]string{}, cmd.Args...)
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--game-path" {
			args[i+1] = r.gamePath
		}
	}
	cmd.Args = args
	return r.inner.Run(ctx, cmd, onStart)
}

// TestIntegrationUnawaitedChmod proves a slow un-awaited chmod child is killed with the installer's process group and the result is still normalized.
func TestIntegrationUnawaitedChmod(t *testing.T) {
	art, checkIsolation := isolateIntegration(t)
	realChmod, err := exec.LookPath("chmod")
	if err != nil {
		t.Skipf("chmod not found: %v", err)
	}
	wrapDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "chmod.log")
	script := fmt.Sprintf("#!/bin/sh\necho \"start $$ $(date +%%s.%%N) $*\" >> '%s'\nsleep 4\necho \"done $$ $*\" >> '%s'\nexec '%s' \"$@\"\n", logPath, logPath, realChmod)
	if err := os.WriteFile(filepath.Join(wrapDir, "chmod"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	game := newStubGame(t)
	runner := &observingRunner{t: t, inner: &pathPrependRunner{inner: ExecRunner{}, dir: wrapDir}, zipPath: art.ZipPath}
	started := time.Now()
	if _, err := (&Installer{Spec: testSpec(), Runner: runner, Timeout: 2 * time.Minute}).Install(context.Background(), game, art); err != nil {
		t.Fatalf("Install: %v", err)
	}
	elapsed := time.Since(started)
	startLog, _ := os.ReadFile(logPath)
	t.Logf("install with a 4s chmod wrapper took %s; chmod log right after install:\n%s", elapsed.Round(time.Millisecond), startLog)
	if elapsed > 4*time.Second {
		t.Fatalf("install waited for the un-awaited chmod (%s)", elapsed)
	}
	if !strings.Contains(string(startLog), "start ") {
		t.Fatal("the installer never ran the chmod wrapper, so this test proved nothing")
	}
	time.Sleep(5 * time.Second)
	finalLog, _ := os.ReadFile(logPath)
	t.Logf("chmod log 5s later:\n%s", finalLog)
	if strings.Contains(string(finalLog), "done ") {
		t.Fatal("an un-awaited chmod child survived the installer's process group")
	}
	assertIntegrationOK(t, game)
	for _, rel := range []string{"StardewValley", "StardewModdingAPI", "StardewValley-original"} {
		if mode := fileMode(t, game, rel); mode != 0755 {
			t.Fatalf("mode of %s = %o", rel, mode)
		}
	}
	checkIsolation()
}

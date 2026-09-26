package smapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/ghrelease"
)

// TestOpenBundleExtractsVerifiedArchive verifies the bundle is re-hashed, extracted, and only the installer is executable.
func TestOpenBundleExtractsVerifiedArchive(t *testing.T) {
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	dest := filepath.Join(t.TempDir(), "bundle")
	bundle, err := OpenBundle(art, dest, testSpec())
	if err != nil {
		t.Fatalf("OpenBundle: %v", err)
	}
	if bundle.Root != filepath.Join(dest, "SMAPI 4.5.2 installer") {
		t.Fatalf("root = %s", bundle.Root)
	}
	info, err := os.Lstat(bundle.InstallerPath)
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("installer = %v, %v", info, err)
	}
	info, err = os.Lstat(bundle.PayloadPath)
	if err != nil || info.Mode().Perm() != 0644 {
		t.Fatalf("payload = %v, %v", info, err)
	}
	art.SHA256 = strings.Repeat("0", 64)
	if _, err := OpenBundle(art, t.TempDir(), testSpec()); !errors.Is(err, ghrelease.ErrDigestMismatch) {
		t.Fatalf("OpenBundle with wrong digest = %v", err)
	}
}

// TestOpenBundleRejectsUnsafeArchives verifies traversal, links, and malformed layouts are refused.
func TestOpenBundleRejectsUnsafeArchives(t *testing.T) {
	top := "SMAPI 1.0.0 installer/"
	good := []zipEntry{
		{name: top + "internal/linux/SMAPI.Installer", data: []byte("x")},
		{name: top + "internal/linux/install.dat", data: []byte("x")},
	}
	cases := map[string][]zipEntry{
		"parent traversal":   append([]zipEntry{{name: top + "../evil", data: []byte("x")}}, good...),
		"absolute path":      append([]zipEntry{{name: "/etc/evil", data: []byte("x")}}, good...),
		"backslash":          append([]zipEntry{{name: top + `a\b`, data: []byte("x")}}, good...),
		"symlink entry":      append([]zipEntry{{name: top + "link", data: []byte("/etc/passwd"), mode: os.ModeSymlink | 0777}}, good...),
		"two top-level dirs": append([]zipEntry{{name: "other/file", data: []byte("x")}}, good...),
		"top-level file":     append([]zipEntry{{name: "loose.txt", data: []byte("x")}}, good...),
		"missing installer":  {{name: top + "internal/linux/install.dat", data: []byte("x")}},
		"installer is a dir": {{name: top + "internal/linux/SMAPI.Installer/"}, {name: top + "internal/linux/install.dat", data: []byte("x")}},
		"duplicate entry":    append([]zipEntry{good[0]}, good...),
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			data := buildZip(t, entries)
			path := filepath.Join(dir, "SMAPI-1.0.0-installer.zip")
			if err := os.WriteFile(path, data, 0644); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(data)
			art := Artifact{Version: "1.0.0", AssetName: filepath.Base(path), SHA256: hex.EncodeToString(sum[:]), ZipPath: path}
			dest := filepath.Join(dir, "out")
			if _, err := OpenBundle(art, dest, testSpec()); err == nil {
				t.Fatal("OpenBundle accepted an unsafe archive")
			}
			if _, err := os.Lstat(filepath.Join(dir, "evil")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("archive wrote outside its destination")
			}
		})
	}
}

// TestReadPayload verifies the payload index splits root files, bundled mods, and the launcher.
func TestReadPayload(t *testing.T) {
	payload := readTestPayload(t, writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2")))
	var root []string
	for rel := range payload.Root {
		root = append(root, rel)
	}
	sort.Strings(root)
	wantRoot := []string{
		"StardewModdingAPI", "StardewModdingAPI.dll", "StardewModdingAPI.runtimeconfig.json", "StardewModdingAPI.xml",
		"smapi-internal/SMAPI.Toolkit.dll", "smapi-internal/config.json", "smapi-internal/i18n/default.json", "steam_appid.txt",
	}
	if !reflect.DeepEqual(root, wantRoot) {
		t.Fatalf("root = %v", root)
	}
	if !reflect.DeepEqual(payload.RootDirs, []string{"smapi-internal", "smapi-internal/i18n"}) {
		t.Fatalf("root dirs = %v", payload.RootDirs)
	}
	var mods []string
	for rel := range payload.Mods {
		mods = append(mods, rel)
	}
	sort.Strings(mods)
	wantMods := []string{"Mods/ConsoleCommands/ConsoleCommands.dll", "Mods/ConsoleCommands/manifest.json", "Mods/SaveBackup/SaveBackup.dll", "Mods/SaveBackup/manifest.json"}
	if !reflect.DeepEqual(mods, wantMods) {
		t.Fatalf("mods = %v", mods)
	}
	if payload.Launcher != digestBytes([]byte(testSMAPILauncher)) {
		t.Fatalf("launcher = %+v", payload.Launcher)
	}
	if payload.Root["steam_appid.txt"] != digestBytes([]byte("413150")) {
		t.Fatalf("steam_appid.txt = %+v", payload.Root["steam_appid.txt"])
	}
}

// TestReadPayloadRejectsMalformedPayloads verifies payloads without a launcher or loader, or shipping game-owned names, are refused.
func TestReadPayloadRejectsMalformedPayloads(t *testing.T) {
	cases := map[string]func(files map[string]string){
		"no launcher":        func(files map[string]string) { delete(files, "unix-launcher.sh") },
		"no loader":          func(files map[string]string) { delete(files, "StardewModdingAPI") },
		"ships launcher":     func(files map[string]string) { files["StardewValley"] = "x" },
		"ships deps":         func(files map[string]string) { files["StardewModdingAPI.deps.json"] = "x" },
		"ships user config":  func(files map[string]string) { files["smapi-internal/config.user.json"] = "x" },
		"reserved name":      func(files map[string]string) { files[".gorganizer-modloader.json"] = "x" },
		"nested game assets": func(files map[string]string) { files["Stardew Valley.dll/x"] = "x" },
		"game content":       func(files map[string]string) { files["Content/Data/Fish.xnb"] = "x" },
		"native executable":  func(files map[string]string) { files["Stardew Valley"] = "x" },
		"foreign marker":     func(files map[string]string) { files["Stardew Valley.exe"] = "x" },
		"game version file":  func(files map[string]string) { files["Stardew Valley.deps.json"] = "x" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			files := testPayloadFiles("4.5.2")
			mutate(files)
			path := filepath.Join(t.TempDir(), "install.dat")
			if err := os.WriteFile(path, payloadZip(t, files), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadPayload(path, testSpec()); err == nil {
				t.Fatal("ReadPayload accepted a malformed payload")
			}
		})
	}
}

// TestBuildStage verifies the vanilla launcher choice and user-config carry-over.
func TestBuildStage(t *testing.T) {
	spec := testSpec()
	game := newTestGame(t)
	in, err := BuildStage(game, filepath.Join(t.TempDir(), "stage"), spec)
	if err != nil {
		t.Fatalf("BuildStage: %v", err)
	}
	if in.VanillaSource != "StardewValley" || in.VanillaLauncher != digestBytes([]byte(testVanillaLauncher)) || in.CarriedUserConfig {
		t.Fatalf("stage input = %+v", in)
	}
	if in.DepsSHA != digestBytes([]byte(testDepsJSON)).SHA256 {
		t.Fatalf("deps digest = %s", in.DepsSHA)
	}
	if err := os.WriteFile(filepath.Join(game, "StardewValley-original"), []byte(testVanillaLauncher), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(game, "StardewValley"), []byte(testSMAPILauncher), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(game, "smapi-internal"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(game, "smapi-internal", "config.user.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(t.TempDir(), "stage")
	in, err = BuildStage(game, stage, spec)
	if err != nil {
		t.Fatalf("BuildStage: %v", err)
	}
	if in.VanillaSource != "StardewValley-original" || !in.CarriedUserConfig {
		t.Fatalf("stage input = %+v", in)
	}
	if got := readGameFile(t, stage, "StardewValley"); got != testVanillaLauncher {
		t.Fatalf("staged launcher = %q", got)
	}
	if got := fileMode(t, stage, "StardewValley"); got != 0755 {
		t.Fatalf("staged launcher mode = %o", got)
	}
	for _, rel := range []string{"Stardew Valley.dll", "Stardew Valley.deps.json", "smapi-internal/config.user.json"} {
		readGameFile(t, stage, rel)
	}
	if !isPlainDir(filepath.Join(stage, "Mods")) {
		t.Fatal("staged Mods folder missing")
	}
	if err := os.WriteFile(filepath.Join(game, "StardewValley-original"), []byte(testSMAPILauncher), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildStage(game, filepath.Join(t.TempDir(), "stage"), spec); !errors.Is(err, ErrNoVanillaLauncher) {
		t.Fatalf("BuildStage without vanilla launcher = %v", err)
	}
}

// TestLoaderSpecValidate verifies the spec validation rules.
func TestLoaderSpecValidate(t *testing.T) {
	if err := testSpec().Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	cases := map[string]func(s *LoaderSpec){
		"nested launcher":         func(s *LoaderSpec) { s.LauncherName = "bin/StardewValley" },
		"empty loader":            func(s *LoaderSpec) { s.LoaderExecutable = "" },
		"reserved name":           func(s *LoaderSpec) { s.LauncherBackupName = ".gorganizer-modloader.json" },
		"duplicate names":         func(s *LoaderSpec) { s.LauncherBackupName = s.LauncherName },
		"escaping installer":      func(s *LoaderSpec) { s.InstallerRelPath = "../SMAPI.Installer" },
		"absolute payload":        func(s *LoaderSpec) { s.PayloadRelPath = "/install.dat" },
		"unclean uninstall path":  func(s *LoaderSpec) { s.UninstallPaths = []string{"Mods/../x"} },
		"game-owned uninstall":    func(s *LoaderSpec) { s.UninstallPaths = []string{"StardewValley"} },
		"uninstall of Mods":       func(s *LoaderSpec) { s.UninstallPaths = []string{"Mods"} },
		"no native markers":       func(s *LoaderSpec) { s.NativeMarkers = nil },
		"empty version package":   func(s *LoaderSpec) { s.GameVersionPackage = " " },
		"empty bundled id":        func(s *LoaderSpec) { s.BundledModIDs = []string{""} },
		"control character":       func(s *LoaderSpec) { s.LoaderDepsFile = "a\nb" },
		"same installer, payload": func(s *LoaderSpec) { s.PayloadRelPath = s.InstallerRelPath },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			spec := testSpec()
			mutate(&spec)
			if err := spec.Validate(); err == nil {
				t.Fatal("Validate accepted an invalid spec")
			}
		})
	}
}

// TestReadPayloadBundledIDsAreCaseSensitive verifies a bundled mod whose UniqueID differs only in case is ignored like upstream.
func TestReadPayloadBundledIDsAreCaseSensitive(t *testing.T) {
	files := testPayloadFiles("4.5.2")
	files["Mods/ConsoleCommands/manifest.json"] = bundledManifest("smapi.consolecommands", "4.5.2")
	payload := readTestPayload(t, writeArtifact(t, t.TempDir(), "4.5.2", files))
	for rel := range payload.Mods {
		if strings.HasPrefix(rel, "Mods/ConsoleCommands/") {
			t.Fatalf("case-mismatched bundled mod was accepted: %s", rel)
		}
	}
	if _, ok := payload.Mods["Mods/SaveBackup/SaveBackup.dll"]; !ok {
		t.Fatalf("mods = %v", payload.Mods)
	}
}

// TestBundlePayloadComesFromVerifiedBytes verifies the payload index uses the verified in-memory archive, not the extracted file.
func TestBundlePayloadComesFromVerifiedBytes(t *testing.T) {
	art := writeArtifact(t, t.TempDir(), "4.5.2", testPayloadFiles("4.5.2"))
	bundle, err := OpenBundle(art, filepath.Join(t.TempDir(), "bundle"), testSpec())
	if err != nil {
		t.Fatalf("OpenBundle: %v", err)
	}
	tampered := testPayloadFiles("6.6.6")
	if err := os.WriteFile(bundle.PayloadPath, payloadZip(t, tampered), 0644); err != nil {
		t.Fatal(err)
	}
	payload, err := bundle.Payload(testSpec())
	if err != nil {
		t.Fatalf("Payload: %v", err)
	}
	if payload.Root["StardewModdingAPI.dll"] != digestBytes([]byte("assembly 4.5.2")) {
		t.Fatalf("payload index followed the tampered file: %+v", payload.Root["StardewModdingAPI.dll"])
	}
	if _, err := (Bundle{}).Payload(testSpec()); err == nil {
		t.Fatal("an unverified bundle produced a payload")
	}
}

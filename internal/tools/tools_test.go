package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectTool(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "skse64_loader.exe"), []byte("x"), 0644)

	tool, found := DetectTool(dir, "skyrimse")
	if !found {
		t.Fatal("expected to find SKSE64")
	}
	if tool.ID != "skse64" {
		t.Errorf("tool ID = %q, want \"skse64\"", tool.ID)
	}
}

func TestDetectToolNotFound(t *testing.T) {
	dir := t.TempDir()
	_, found := DetectTool(dir, "skyrimse")
	if found {
		t.Error("expected no tool found in empty directory")
	}
}

func TestDetectToolWrongGame(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "skse64_loader.exe"), []byte("x"), 0644)

	_, found := DetectTool(dir, "falloutnv")
	if found {
		t.Error("SKSE64 should not match falloutnv")
	}
}

func TestToolsForGame(t *testing.T) {
	tools := ToolsForGame("skyrimse")
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool for skyrimse, got %d", len(tools))
	}
	if tools[0].ID != "skse64" {
		t.Errorf("tool = %q, want \"skse64\"", tools[0].ID)
	}
}

func TestDetectObse64InNestedInstallDirectory(t *testing.T) {
	gameRoot := t.TempDir()
	tool := KnownTools["obse64"]
	if err := os.MkdirAll(tool.InstallDir(gameRoot), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tool.LoaderPath(gameRoot), []byte("loader"), 0644); err != nil {
		t.Fatal(err)
	}

	detected, found := DetectTool(gameRoot, "oblivionremastered")
	if !found {
		t.Fatal("expected to find nested OBSE64 loader")
	}
	if detected.ID != "obse64" {
		t.Fatalf("tool ID = %q, want obse64", detected.ID)
	}
	wantRel := "OblivionRemastered/Binaries/Win64/obse64_loader.exe"
	if got := detected.LoaderRelativePath(); got != wantRel {
		t.Errorf("LoaderRelativePath = %q, want %q", got, wantRel)
	}
	if detected.LogSubpath != "OBSE/Logs" {
		t.Errorf("LogSubpath = %q, want OBSE/Logs", detected.LogSubpath)
	}
}

func TestKnownToolsCount(t *testing.T) {
	if len(KnownTools) != 8 {
		t.Errorf("expected 8 known tools, got %d", len(KnownTools))
	}
}

func TestBuildSteamParityEnvCore(t *testing.T) {
	env := buildSteamParityEnv("/compat/489830", "/steam", "489830", "/games/Skyrim", "")
	expected := map[string]string{
		"STEAM_COMPAT_DATA_PATH":           "/compat/489830",
		"STEAM_COMPAT_CLIENT_INSTALL_PATH": "/steam",
		"STEAM_COMPAT_INSTALL_PATH":        "/games/Skyrim",
		"STEAM_COMPAT_APP_ID":              "489830",
		"SteamAppId":                       "489830",
		"SteamGameId":                      "489830",
	}
	got := map[string]string{}
	for _, e := range env {
		parts := splitEnvVar(e)
		got[parts[0]] = parts[1]
	}
	for k, v := range expected {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestBuildSteamParityEnvWithDllOverrides(t *testing.T) {
	t.Setenv("WINEDLLOVERRIDES", "")
	env := buildSteamParityEnv("/compat/22380", "/steam", "22380", "/games/FNV",
		"nvse_1_4=n,b;nvse_steam_loader=n,b")
	var got string
	for _, e := range env {
		parts := splitEnvVar(e)
		if parts[0] == "WINEDLLOVERRIDES" {
			got = parts[1]
		}
	}
	if got == "" {
		t.Fatalf("WINEDLLOVERRIDES not set when dllOverrides passed")
	}
	if got != "nvse_1_4=n,b;nvse_steam_loader=n,b" {
		t.Errorf("WINEDLLOVERRIDES = %q, want the passed-in value verbatim (no parent env set)", got)
	}
}

func TestMergeDllOverridesOursWinsOnCollision(t *testing.T) {
	merged := mergeDllOverrides("nvse_1_4=b;other=n", "nvse_1_4=n,b;d3dx9_38=n,b")
	entries := map[string]string{}
	for _, p := range splitSemi(merged) {
		k, v, _ := strings.Cut(p, "=")
		entries[k] = v
	}
	if entries["nvse_1_4"] != "n,b" {
		t.Errorf("nvse_1_4 merged = %q, want \"n,b\" (ours must win)", entries["nvse_1_4"])
	}
	if entries["other"] != "n" {
		t.Errorf("other merged = %q, want \"n\" (inherited non-conflicting key should carry)", entries["other"])
	}
	if entries["d3dx9_38"] != "n,b" {
		t.Errorf("d3dx9_38 merged = %q, want \"n,b\"", entries["d3dx9_38"])
	}
}

func splitSemi(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ";") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// TestEraAppropriateD3DX9 pins the era-matched DirectX 9 redist ExtraDll for each DX9-era script extender.
func TestEraAppropriateD3DX9(t *testing.T) {
	expected := map[string][]string{
		"xnvse":  {"d3dx9_38.dll"},
		"fose":   {"d3dx9_38.dll"},
		"skse":   {"d3dx9_42.dll"},
		"obse":   {"d3dx9_27.dll", "d3dx9_9.dll"},
		"skse64": nil,
		"f4se":   nil,
		"sfse":   nil,
		"obse64": nil,
	}
	for id, want := range expected {
		tool, ok := KnownTools[id]
		if !ok {
			t.Errorf("missing tool %q in KnownTools", id)
			continue
		}
		got := tool.ExtraDlls
		if len(got) != len(want) {
			t.Errorf("%s ExtraDlls = %v, want %v", id, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s ExtraDlls[%d] = %q, want %q", id, i, got[i], want[i])
			}
		}
	}
}

func TestScanNativeDllsUsesNestedToolDirectory(t *testing.T) {
	gameRoot := t.TempDir()
	tool := KnownTools["obse64"]
	if err := os.MkdirAll(tool.InstallDir(gameRoot), 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"obse64_1_512_105.dll", "unrelated.dll"} {
		if err := os.WriteFile(filepath.Join(tool.InstallDir(gameRoot), name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(gameRoot, "obse64_wrong.dll"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	got := tool.ScanNativeDlls(gameRoot)
	if len(got) != 1 || got[0] != "obse64_1_512_105.dll" {
		t.Fatalf("ScanNativeDlls = %v, want nested OBSE64 DLL only", got)
	}
}

func TestExternalToolCatalogUsesCompatibleRunners(t *testing.T) {
	tests := []struct {
		basename string
		toolID   string
		runner   RunnerKind
	}{
		{basename: "ReSaver.exe", toolID: "resaver", runner: RunnerProton},
		{basename: "ReSaver.jar", toolID: "resaver-java", runner: RunnerJava},
		{basename: "NifSkope.exe", toolID: "nifskope", runner: RunnerProton},
		{basename: "nifskope", toolID: "nifskope-native", runner: RunnerNative},
	}
	root := t.TempDir()
	for _, test := range tests {
		path := filepath.Join(root, test.basename)
		if err := os.WriteFile(path, []byte("tool"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "Wrye Bash Launcher.pyw"), []byte("tool"), 0755); err != nil {
		t.Fatal(err)
	}
	found := DetectExecutablesForGame("skyrimse", []string{root})
	byBase := make(map[string]DetectedTool)
	for _, tool := range found {
		byBase[filepath.Base(tool.ExePath)] = tool
	}
	for _, test := range tests {
		tool, ok := byBase[test.basename]
		if !ok {
			t.Errorf("%s was not detected", test.basename)
			continue
		}
		if tool.CatalogID != test.toolID || tool.Runner != test.runner {
			t.Errorf("%s = (%s, %s), want (%s, %s)", test.basename, tool.CatalogID, tool.Runner, test.toolID, test.runner)
		}
	}
	if _, ok := byBase["Wrye Bash Launcher.pyw"]; ok {
		t.Error("unsupported Wrye Bash .pyw launcher was detected")
	}
}

// TestScanNativeDllsCombinesPrefixesAndExtras checks that prefix matches and extras combine without duplication.
func TestScanNativeDllsCombinesPrefixesAndExtras(t *testing.T) {
	dir := t.TempDir()
	present := []string{
		"fose_loader.exe",
		"fose_1_2b_ng.dll",
		"fose_steam_loader.dll",
		"d3dx9_38.dll",
		"unrelated.dll",
	}
	for _, name := range present {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0644); err != nil {
			t.Fatalf("writing fixture %s: %v", name, err)
		}
	}
	tool := KnownTools["fose"]
	got := tool.ScanNativeDlls(dir)
	set := map[string]bool{}
	for _, g := range got {
		set[g] = true
	}
	wantPresent := []string{"fose_1_2b_ng.dll", "fose_steam_loader.dll", "d3dx9_38.dll"}
	for _, w := range wantPresent {
		if !set[w] {
			t.Errorf("ScanNativeDlls missing %q in %v", w, got)
		}
	}
	if set["unrelated.dll"] {
		t.Errorf("ScanNativeDlls included unrelated.dll: %v", got)
	}
	if set["fose_loader.exe"] {
		t.Errorf("ScanNativeDlls included the .exe: %v", got)
	}
}

// TestScanNativeDllsSkipsMissingExtras verifies missing ExtraDlls are silently skipped.
func TestScanNativeDllsSkipsMissingExtras(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "obse_1_2_416.dll"), []byte("x"), 0644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	tool := KnownTools["obse"]
	got := tool.ScanNativeDlls(dir)
	for _, g := range got {
		if g == "d3dx9_27.dll" || g == "d3dx9_9.dll" {
			t.Errorf("ScanNativeDlls included missing extra %q: %v", g, got)
		}
	}
	foundExtender := false
	for _, g := range got {
		if g == "obse_1_2_416.dll" {
			foundExtender = true
		}
	}
	if !foundExtender {
		t.Errorf("ScanNativeDlls should have returned obse_1_2_416.dll: got %v", got)
	}
}

func TestBuildDllOverrides(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"empty", nil, ""},
		{"single", []string{"nvse_1_4.dll"}, "nvse_1_4=n,b"},
		{"multiple", []string{"nvse_1_4.dll", "nvse_steam_loader.dll"},
			"nvse_1_4=n,b;nvse_steam_loader=n,b"},
		{"uppercase ext stripped", []string{"d3dx9_38.DLL"}, "d3dx9_38=n,b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BuildDllOverrides(tc.in); got != tc.want {
				t.Errorf("BuildDllOverrides(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func splitEnvVar(s string) [2]string {
	for i, c := range s {
		if c == '=' {
			return [2]string{s[:i], s[i+1:]}
		}
	}
	return [2]string{s, ""}
}

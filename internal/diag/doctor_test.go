package diag

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/steam"
	"github.com/parka/gorganizer/internal/testsafe"
	"github.com/parka/gorganizer/internal/vfs"
)

// TestMain isolates diagnostic tests from user settings and desktop launchers.
func TestMain(m *testing.M) {
	os.Exit(testsafe.RunWithSafeEnvironment(m))
}

// fixture prepares private settings, a game, and an empty process table.
func fixture(t *testing.T) (Options, string, string) {
	t.Helper()
	root := t.TempDir()
	for _, entry := range []struct{ name, value string }{
		{"HOME", root}, {"XDG_CONFIG_HOME", filepath.Join(root, "config")},
		{"XDG_DATA_HOME", filepath.Join(root, "data")}, {"XDG_STATE_HOME", filepath.Join(root, "state")},
		{"XDG_RUNTIME_DIR", filepath.Join(root, "run")}, {"GORGANIZER_ROOT", filepath.Join(root, "checkout")},
	} {
		t.Setenv(entry.name, entry.value)
	}
	for _, dir := range []string{"run", "proc", "checkout", "game", "game/Data", "state/gorganizer"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.DefaultConfig()
	cfg.Games["skyrimse"] = config.GameConfig{Name: "Skyrim", InstallPath: filepath.Join(root, "game")}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	return Options{Version: "test", Checkout: filepath.Join(root, "checkout"), ProcRoot: filepath.Join(root, "proc"),
		Health:         func(context.Context) (Health, error) { return Health{}, errors.New("not running") },
		LookPath:       func(string) (string, error) { return "", os.ErrNotExist },
		FindSteamRoots: func() ([]steam.Root, error) { return nil, os.ErrNotExist }}, root, filepath.Join(root, "game", "Data")
}

// TestDoctorReportsProblemsAndFixes checks unsafe permissions, an unfinished farm, and a stopped daemon.
func TestDoctorReportsProblemsAndFixes(t *testing.T) {
	opts, root, data := fixture(t)
	if err := os.Mkdir(filepath.Join(root, "run", "gorganizer"), 0o755); err != nil && !os.IsExist(err) {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "run", "gorganizer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vfs.ActivationIntentPath(data), []byte("unfinished"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := Run(opts)
	checks := map[string]Check{}
	for _, check := range report.Checks {
		checks[check.Name] = check
	}
	for _, tc := range []struct{ name, status, fix string }{
		{"Runtime folder", "Problem", "0700"},
		{"Game Skyrim Data", "Problem", "gorganizerctl recover --game skyrimse"},
		{"Background service", "Note", "Not running"},
	} {
		check, ok := checks[tc.name]
		if !ok || check.Status != tc.status || !strings.Contains(check.Message, tc.fix) {
			t.Errorf("%s = %+v (found %v), want %s and %q", tc.name, check, ok, tc.status, tc.fix)
		}
	}
	if !report.HasProblems() || !strings.Contains(report.Text(), "Summary: 2 problem(s) found.") {
		t.Fatalf("unexpected summary: %s", report.Text())
	}
}

// TestDoctorProtontricks checks native and Flatpak availability without invoking external binaries.
func TestDoctorProtontricks(t *testing.T) {
	for _, tc := range []struct {
		name, native, flatpak, message string
		infoError                      error
		infoCalls                      int
	}{
		{name: "native first", native: "native-tool", flatpak: "flatpak-tool", message: "Available (native)."},
		{name: "Flatpak app installed", flatpak: "flatpak-tool", message: "Available (flatpak).", infoCalls: 1},
		{name: "Flatpak app absent", flatpak: "flatpak-tool", infoError: os.ErrNotExist, message: "Not installed; Windows runtime components cannot be added automatically.", infoCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts, _, _ := fixture(t)
			calls := 0
			opts.LookPath = func(name string) (string, error) {
				switch name {
				case "protontricks":
					if tc.native != "" {
						return tc.native, nil
					}
				case "flatpak":
					if tc.flatpak != "" {
						return tc.flatpak, nil
					}
				}
				return "", os.ErrNotExist
			}
			opts.ProtontricksInfo = func(context.Context, string, ...string) error {
				calls++
				return tc.infoError
			}
			report := Run(opts)
			if calls != tc.infoCalls {
				t.Errorf("flatpak info called %d times, want %d", calls, tc.infoCalls)
			}
			found := false
			for _, check := range report.Checks {
				if check.Name == "Optional tool protontricks" {
					found = true
					if check.Message != tc.message {
						t.Errorf("protontricks check = %q, want %q", check.Message, tc.message)
					}
				}
			}
			if !found {
				t.Error("protontricks check missing")
			}
		})
	}
}

// TestDoctorReportsVersionDisagreement checks nearby binary versions and a mismatched daemon response.
func TestDoctorReportsVersionDisagreement(t *testing.T) {
	opts, root, _ := fixture(t)
	for _, path := range []string{filepath.Join(root, "gorganizerd"), filepath.Join(root, "build", "src", "gorganizer")} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	opts.Executable = filepath.Join(root, "gorganizerctl")
	opts.BinaryVersion = func(_ context.Context, path string) (string, error) {
		if filepath.Base(path) == "gorganizerd" {
			return "gorganizerd test (commit fake)", nil
		}
		return "gorganizer-gui test", nil
	}
	opts.Health = func(context.Context) (Health, error) {
		return Health{Version: "older", InstanceID: "fixture", Stopping: true}, nil
	}
	report := Run(opts)
	for _, fragment := range []string{"Background service version: gorganizerd test", "Window version: gorganizer-gui test", "Shutting down (instance fixture)", "Problem — Version agreement:"} {
		if !strings.Contains(report.Text(), fragment) {
			t.Errorf("missing %q in report:\n%s", fragment, report.Text())
		}
	}
	if !report.HasProblems() {
		t.Fatal("version disagreement was not a problem")
	}
}

// TestDoctorFindsSteamGamesInEveryRoot checks root reporting, library deduplication, and manifests outside the first root.
func TestDoctorFindsSteamGamesInEveryRoot(t *testing.T) {
	opts, home, _ := fixture(t)
	first := filepath.Join(home, "first-steam")
	second := filepath.Join(home, "second-steam")
	shared := filepath.Join(home, "shared-library")
	install := filepath.Join(second, "steamapps", "common", "Skyrim")
	for _, path := range []string{first, second, shared} {
		if err := os.MkdirAll(filepath.Join(path, "steamapps"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(install, "Data"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `"AppState" { "appid" "489830" "installdir" "Skyrim" "StateFlags" "4" }`
	if err := os.WriteFile(filepath.Join(second, "steamapps", "appmanifest_489830.acf"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Games["skyrimse"] = config.GameConfig{Name: "Skyrim", InstallPath: install, SteamAppID: 489830}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	opts.FindSteamRoots = func() ([]steam.Root, error) {
		return []steam.Root{
			{Path: first, Libraries: []string{first, shared}},
			{Path: second, Libraries: []string{second, shared}},
		}, nil
	}
	before := treeSnapshot(t, home)
	report := Run(opts).Text()
	for _, want := range []string{
		"OK — Steam root 1: Found Steam at " + first,
		"OK — Steam root 2: Found Steam at " + second,
		"OK — Steam libraries: 3 distinct Steam libraries found.",
		"OK — Game skyrimse Steam record: Steam installation record is readable.",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("missing %q in report:\n%s", want, report)
		}
	}
	if after := treeSnapshot(t, home); !reflect.DeepEqual(before, after) {
		t.Fatal("Steam diagnosis changed files")
	}
	opts.FindSteamRoots = func() ([]steam.Root, error) {
		return []steam.Root{{Path: first, Libraries: []string{first, shared}}}, nil
	}
	if report := Run(opts).Text(); !strings.Contains(report, "Problem — Game skyrimse Steam record:") {
		t.Errorf("reported an undiscovered library's manifest as readable:\n%s", report)
	}
}

// treeSnapshot records file metadata and content to detect any diagnostic writes.
func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		value := info.Mode().String() + info.ModTime().UTC().String()
		if info.Mode().IsRegular() {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += string(content)
		}
		snapshot[path] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// TestDoctorIsReadOnly verifies a complete diagnosis does not change the fixture tree.
func TestDoctorIsReadOnly(t *testing.T) {
	opts, root, _ := fixture(t)
	before := treeSnapshot(t, root)
	_ = Run(opts)
	if after := treeSnapshot(t, root); !reflect.DeepEqual(before, after) {
		t.Fatalf("doctor changed files:\nbefore: %v\nafter: %v", before, after)
	}
}

// archiveEntries reads a generated archive into a map for content and size assertions.
func archiveEntries(t *testing.T, path string) map[string][]byte {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	entries := map[string][]byte{}
	reader := tar.NewReader(compressed)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		entries[header.Name] = content
	}
	return entries
}

// TestBugReportRedactsSecrets verifies selected text excludes credentials, links, queries, and home paths.
func TestBugReportRedactsSecrets(t *testing.T) {
	opts, root, _ := fixture(t)
	cfgPath := filepath.Join(config.ConfigDir(), "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"games":{"skyrimse":{"name":"Skyrim","install_path":"`+root+`/game","executables":[{"environment":{"PASSWORD":"ENV_CANARY"}}]}},"nexus_api_key":"API_CANARY","log_level":"info"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	logText := "nxm://skyrimse/mods/7?key=NXM_CANARY https://example.test/page?apikey=QUERY_CANARY Authorization: Bearer AUTH_CANARY " + root + "/game\n" +
		"apikey=OTHER_CANARY key=KEY_CANARY https://example.test/path?token=TOKEN_CANARY\n"
	assignments := []struct{ line, key, canary string }{
		{"nexus_api_key=NEXUS_LOG_CANARY", "nexus_api_key", "NEXUS_LOG_CANARY"},
		{"api_key=UNDERSCORE_CANARY", "api_key", "UNDERSCORE_CANARY"},
		{"api-key=HYPHEN_CANARY", "api-key", "HYPHEN_CANARY"},
		{"token=TOKEN_LOG_CANARY", "token", "TOKEN_LOG_CANARY"},
		{"prefixkey=PREFIX_KEY_CANARY", "prefixkey", "PREFIX_KEY_CANARY"},
		{"clientapikey=PREFIX_APIKEY_CANARY", "clientapikey", "PREFIX_APIKEY_CANARY"},
		{"client_api_key: COLON_CANARY", "client_api_key", "COLON_CANARY"},
		{"access-token=PREFIX_TOKEN_CANARY", "access-token", "PREFIX_TOKEN_CANARY"},
		{"sessionsecret: SECRET_CANARY", "sessionsecret", "SECRET_CANARY"},
		{"password='PASSWORD_CANARY'", "password", "PASSWORD_CANARY"},
	}
	for _, assignment := range assignments {
		logText += assignment.line + "\n"
	}
	logText += `{"api_key":"JSON_API_CANARY","nexus_api_key": "JSON_NEXUS_CANARY", "session_token":"JSON_TOKEN_CANARY", "password": "JSON_PASSWORD_CANARY"}` + "\n"
	if err := os.WriteFile(filepath.Join(StateDir(), "daemon.log"), []byte(logText), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(config.ProfilesDir("skyrimse"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(config.ProfilesDir("skyrimse"), "Default"), 0o700); err != nil {
		t.Fatal(err)
	}
	path, err := CreateBundle(BundleOptions{OutDir: filepath.Join(root, "state"), Version: "test", Report: Run(opts), Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	entries := archiveEntries(t, path)
	for _, file := range []string{"doctor.txt", "VERSION", "config.json", "games-and-profiles.txt", "logs/daemon.log"} {
		if _, ok := entries[file]; !ok {
			t.Errorf("missing %s", file)
		}
	}
	all := bytes.Join([][]byte{entries["doctor.txt"], entries["VERSION"], entries["config.json"], entries["games-and-profiles.txt"], entries["logs/daemon.log"]}, nil)
	for _, secret := range []string{"API_CANARY", "ENV_CANARY", "NXM_CANARY", "QUERY_CANARY", "AUTH_CANARY", "OTHER_CANARY", "KEY_CANARY", "TOKEN_CANARY", root, "nxm://", "Authorization: Bearer", "JSON_API_CANARY", "JSON_NEXUS_CANARY", "JSON_TOKEN_CANARY", "JSON_PASSWORD_CANARY"} {
		if bytes.Contains(all, []byte(secret)) {
			t.Errorf("report contains %q", secret)
		}
	}
	for _, assignment := range assignments {
		if bytes.Contains(all, []byte(assignment.canary)) {
			t.Errorf("report contains %q", assignment.canary)
		}
		if !bytes.Contains(entries["logs/daemon.log"], []byte(assignment.key)) {
			t.Errorf("redaction removed log key %q", assignment.key)
		}
	}
	lines := bytes.Split(bytes.TrimSpace(entries["logs/daemon.log"]), []byte("\n"))
	jsonLine := lines[len(lines)-1]
	if !json.Valid(jsonLine) || !bytes.Contains(jsonLine, []byte(`"nexus_api_key": "[redacted]"`)) {
		t.Errorf("redacted JSON log line is invalid or lost its keys: %s", jsonLine)
	}
	if !bytes.Contains(entries["config.json"], []byte("~/game")) || bytes.Contains(entries["config.json"], []byte("nexus_api_key")) || bytes.Contains(entries["config.json"], []byte("environment")) {
		t.Errorf("settings were not redacted: %s", entries["config.json"])
	}
	if !json.Valid(entries["config.json"]) {
		t.Errorf("redacted settings are not valid JSON: %s", entries["config.json"])
	}
	if !bytes.Contains(entries["games-and-profiles.txt"], []byte("Profile: Default")) {
		t.Error("profile name missing")
	}
	for forbidden := range entries {
		if strings.Contains(forbidden, "ledger") || strings.Contains(forbidden, "landing") || strings.Contains(forbidden, ".ini") {
			t.Errorf("unexpected file %q", forbidden)
		}
	}
}

// TestBugReportIncludesCheckoutVersion checks the source version file takes precedence over a development build label.
func TestBugReportIncludesCheckoutVersion(t *testing.T) {
	opts, root, _ := fixture(t)
	for name, body := range map[string]string{"go.mod": "module fixture\n", "VERSION": "1.2.3\n"} {
		if err := os.WriteFile(filepath.Join(opts.Checkout, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path, err := CreateBundle(BundleOptions{OutDir: filepath.Join(root, "state"), Checkout: opts.Checkout, Version: "dev", Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(archiveEntries(t, path)["VERSION"]); got != "1.2.3\n" {
		t.Errorf("VERSION = %q, want source checkout version", got)
	}
}

// TestBugReportPermissions verifies archives are readable only by their owner.
func TestBugReportPermissions(t *testing.T) {
	_, root, _ := fixture(t)
	path, err := CreateBundle(BundleOptions{OutDir: filepath.Join(root, "state"), Version: "test", Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("archive permissions = %04o, want 0600", got)
	}
}

// TestBugReportCapsLogSize verifies large logs are capped and older rotated logs are excluded.
func TestBugReportCapsLogSize(t *testing.T) {
	_, root, _ := fixture(t)
	state := StateDir()
	if err := os.WriteFile(filepath.Join(state, "daemon.log"), bytes.Repeat([]byte("xxxxxxxxxxxxxxx\n"), maxLogBytes/16+10), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"daemon.log.1", "daemon.log.2", "daemon.log.3", "gui.log", "gui.log.1", "gui.log.2", "gorganizer-gui.log"} {
		if err := os.WriteFile(filepath.Join(state, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path, err := CreateBundle(BundleOptions{OutDir: filepath.Join(root, "state"), Version: "test", Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	entries := archiveEntries(t, path)
	if got := len(entries["logs/daemon.log"]); got > maxLogBytes || got < maxLogBytes-16 {
		t.Errorf("log size = %d, want at most %d with one partial line omitted", got, maxLogBytes)
	}
	for _, name := range []string{"daemon.log.1", "daemon.log.2"} {
		if got := string(entries["logs/"+name]); got != name {
			t.Errorf("log %q = %q, want its contents", name, got)
		}
	}
	for _, name := range []string{"daemon.log.3", "gui.log", "gui.log.1", "gui.log.2", "gorganizer-gui.log"} {
		if _, ok := entries["logs/"+name]; ok {
			t.Errorf("included unsupported log %q", name)
		}
	}
}

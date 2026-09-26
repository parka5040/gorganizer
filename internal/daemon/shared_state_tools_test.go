package daemon

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/tools"
)

// toolsRaceWorkers starts a writer and reader together and waits for both to finish.
func toolsRaceWorkers(t *testing.T, writer, reader func(<-chan struct{}) error) {
	t.Helper()
	start := make(chan struct{})
	stop := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, work := range []func(<-chan struct{}) error{writer, reader} {
		wg.Add(1)
		go func(work func(<-chan struct{}) error) {
			defer wg.Done()
			<-start
			results <- work(stop)
		}(work)
	}
	close(start)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(40 * time.Second):
		close(stop)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent workers did not stop after cancellation")
		}
		t.Fatal("concurrent workers did not complete")
	}
	close(results)
	for err := range results {
		if err != nil {
			t.Error(err)
		}
	}
}

// toolsStopped reports whether a concurrent test worker should stop.
func toolsStopped(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

// toolsStopDownloadManager stops the current download manager while holding the session lock.
func toolsStopDownloadManager(d *Daemon) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.downloadMgr != nil {
		d.downloadMgr.Stop()
	}
}

// toolsGameWriter repeatedly configures different games, updating both shared maps.
func toolsGameWriter(d *Daemon, install string, count int) func(<-chan struct{}) error {
	return func(stop <-chan struct{}) error {
		for i := range count {
			if toolsStopped(stop) {
				return nil
			}
			if err := d.ConfigureGame(fmt.Sprintf("tools-race-%d", i), "Race", 0, install, "Data"); err != nil {
				return err
			}
		}
		return nil
	}
}

// toolsMissingNativeSpec returns a registered native tool that cannot start a process.
func toolsMissingNativeSpec(path string) dto.ExecutableSpec {
	return dto.ExecutableSpec{ID: "missing-native", Title: "Missing Native", ExePath: path, Runner: string(tools.RunnerNative)}
}

// TestToolsConfigReadRPCsConcurrentConfigureGame checks scoped game-map readers against live configuration changes.
func TestToolsConfigReadRPCsConcurrentConfigureGame(t *testing.T) {
	for _, tc := range []struct {
		name string
		read func(*Daemon) error
	}{
		{"list ini", func(d *Daemon) error { _, err := d.ListProfileIniFiles("falloutnv", "Default"); return err }},
		{"save ini", func(d *Daemon) error {
			return d.SaveProfileIniFile("falloutnv", "Default", "FalloutCustom.ini", "[General]\n")
		}},
		{"enable ini", func(d *Daemon) error { _, err := d.SetProfileIniEnabled("falloutnv", "Default", true); return err }},
		{"list tweaks", func(d *Daemon) error { _, err := d.ListIniTweaks("falloutnv", "Default"); return err }},
		{"set tweak", func(d *Daemon) error {
			_, err := d.SetIniTweak("falloutnv", "Default", "archive_invalidation", true)
			return err
		}},
		{"ini status", func(d *Daemon) error { _, err := d.GetProfileIniStatus("falloutnv", "Default"); return err }},
		{"plugin order", func(d *Daemon) error { return d.SetPluginOrder("falloutnv", "Default", nil) }},
		{"plugin loadout", func(d *Daemon) error { return d.SetPluginLoadout("falloutnv", "Default", nil) }},
		{"plugin stream", func(d *Daemon) error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stream, err := d.StreamPluginStatus(ctx, "stardewvalley", "Default")
			if err != nil {
				return err
			}
			for range stream {
			}
			return nil
		}},
		{"patch status", func(d *Daemon) error { _, err := d.Get4GBPatchStatus("falloutnv"); return err }},
		{"patch apply", func(d *Daemon) error {
			_, err := d.Apply4GBPatch("falloutnv", "not-an-absolute-patcher")
			if err == nil {
				return errors.New("patcher path was not rejected")
			}
			return nil
		}},
		{"translate path", func(d *Daemon) error { _, err := d.TranslateWinePath("ttw", ""); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			root := t.TempDir()
			d := newIsolatedDaemon(t, map[string]config.GameConfig{
				"falloutnv":     {InstallPath: root, DataSubpath: "Data", SteamAppID: 22380},
				"stardewvalley": {InstallPath: root, DataSubpath: "Mods", SteamAppID: 413150},
				"ttw":           {LinkedFromGameID: "falloutnv", DataSubpath: "Data"},
			})
			if _, _, err := d.profileMgr.Load("falloutnv", "Default"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := d.profileMgr.Load("stardewvalley", "Default"); err != nil {
				t.Fatal(err)
			}
			if _, err := d.SetProfileIniEnabled("falloutnv", "Default", true); err != nil {
				t.Fatal(err)
			}
			if err := tc.read(d); err != nil {
				t.Fatalf("reader setup: %v", err)
			}
			toolsRaceWorkers(t, toolsGameWriter(d, root, 16), func(stop <-chan struct{}) error {
				for range 60 {
					if toolsStopped(stop) {
						return nil
					}
					if err := tc.read(d); err != nil {
						return err
					}
				}
				return nil
			})
		})
	}
}

// TestToolsTTWMountReadsConcurrentConfigureGame checks TTW's mount lookups during manager insertion.
func TestToolsTTWMountReadsConcurrentConfigureGame(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	d := newIsolatedDaemon(t, map[string]config.GameConfig{"falloutnv": {InstallPath: root, DataSubpath: "Data"}})
	t.Setenv("PATH", t.TempDir())
	toolsRaceWorkers(t, toolsGameWriter(d, root, 60), func(stop <-chan struct{}) error {
		for range 800 {
			if toolsStopped(stop) {
				return nil
			}
			if err := d.CheckFNVNotMounted(); err != nil {
				return err
			}
			if _, err := d.CheckTTWPrereqsInternal(TTWBackendNative); err != nil {
				return err
			}
		}
		return nil
	})
}

// TestToolsListExecutablesConcurrentMutations checks detached array reads against replacement, compaction and managed LOOT writes.
func TestToolsListExecutablesConcurrentMutations(t *testing.T) {
	for _, variant := range []string{"replace", "compact", "loot"} {
		t.Run(variant, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			root := t.TempDir()
			game := config.GameConfig{InstallPath: root, DataSubpath: "Data", SteamAppID: 22380}
			for i := range 64 {
				game.Executables = append(game.Executables, config.Executable{ID: fmt.Sprintf("exec-%d", i), Title: "Original", ExePath: filepath.Join(root, "missing"), Args: []string{"Original"}, Environment: map[string]string{"STATE": "Original"}})
			}
			d := newIsolatedDaemon(t, map[string]config.GameConfig{"falloutnv": game})
			writer := func(stop <-chan struct{}) error {
				for i := range 48 {
					if toolsStopped(stop) {
						return nil
					}
					switch variant {
					case "replace":
						value := "Original"
						if i%2 == 0 {
							value = "Replacement"
						}
						_, err := d.UpsertExecutable("falloutnv", dto.ExecutableSpec{ID: "exec-0", Title: value, ExePath: filepath.Join(root, "missing"), Args: []string{value}, Environment: map[string]string{"STATE": value}})
						if err != nil {
							return err
						}
					case "compact":
						if err := d.RemoveExecutable("falloutnv", "exec-0"); err != nil {
							return err
						}
						if _, err := d.UpsertExecutable("falloutnv", dto.ExecutableSpec{ID: "exec-0", Title: "Original", ExePath: filepath.Join(root, "missing"), Args: []string{"Original"}, Environment: map[string]string{"STATE": "Original"}}); err != nil {
							return err
						}
					case "loot":
						if err := d.ExecutableService.syncManagedLOOT(tools.ManagedToolStatus{Installed: true, ExecutablePath: filepath.Join(root, fmt.Sprintf("loot-%d.exe", i))}); err != nil {
							return err
						}
					}
				}
				return nil
			}
			reader := func(stop <-chan struct{}) error {
				for range 400 {
					if toolsStopped(stop) {
						return nil
					}
					list, err := d.ListExecutables("falloutnv")
					if err != nil {
						return err
					}
					for _, e := range list {
						if e.ID == "exec-0" && (len(e.Args) != 1 || e.Title != e.Args[0] || e.Environment["STATE"] != e.Title) {
							return fmt.Errorf("inconsistent executable: %+v", e)
						}
					}
				}
				return nil
			}
			toolsRaceWorkers(t, writer, reader)
		})
	}
}

// TestToolsLaunchExecutableConcurrentMutations checks selection, linked resolution and lazy mount-manager access.
func TestToolsLaunchExecutableConcurrentMutations(t *testing.T) {
	for _, variant := range []string{"replace", "linked", "manager"} {
		t.Run(variant, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			root := t.TempDir()
			gameID := "falloutnv"
			game := config.GameConfig{InstallPath: root, DataSubpath: "Data", Executables: []config.Executable{{ID: "missing-native", Title: "Missing Native", ExePath: filepath.Join(root, "not-present"), Runner: string(tools.RunnerNative)}}}
			games := map[string]config.GameConfig{"falloutnv": game}
			if variant == "linked" {
				gameID = "ttw"
				games["ttw"] = config.GameConfig{LinkedFromGameID: "falloutnv", Executables: game.Executables}
			}
			d := newIsolatedDaemon(t, games)
			if variant == "manager" {
				d.mu.Lock()
				delete(d.mountMgrs, gameID)
				d.mu.Unlock()
			}
			writer := toolsGameWriter(d, root, 40)
			if variant == "replace" {
				writer = func(stop <-chan struct{}) error {
					for i := range 70 {
						if toolsStopped(stop) {
							return nil
						}
						spec := toolsMissingNativeSpec(filepath.Join(root, "not-present"))
						spec.Title = fmt.Sprintf("Missing Native %d", i)
						if _, err := d.UpsertExecutable("falloutnv", spec); err != nil {
							return err
						}
					}
					return nil
				}
			}
			reader := func(stop <-chan struct{}) error {
				for range 130 {
					if toolsStopped(stop) {
						return nil
					}
					_, _, err := d.LaunchExecutable(gameID, "missing-native", "")
					if err == nil || !strings.Contains(err.Error(), "starting native tool") {
						return fmt.Errorf("launch did not reach native start: %v", err)
					}
				}
				return nil
			}
			toolsRaceWorkers(t, writer, reader)
		})
	}
}

// TestToolsGameConfigSnapshotsDetachExecutables checks every nested collection and effective linked inheritance.
func TestToolsGameConfigSnapshotsDetachExecutables(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		"falloutnv": {InstallPath: root, SteamAppID: 22380},
		"ttw":       {LinkedFromGameID: "falloutnv", Executables: []config.Executable{{ID: "snapshot", Title: "Original", ExePath: filepath.Join(root, "missing"), Args: []string{"original"}, Environment: map[string]string{"STATE": "original"}, ExtraRWPaths: []string{"original"}, Runner: string(tools.RunnerNative)}}},
		"empty":     {Executables: make([]config.Executable, 0)},
		"nil":       {},
	})
	raw, ok := d.gameConfigSnapshot("ttw")
	if !ok {
		t.Fatal("raw snapshot missing")
	}
	effective, err := d.effectiveGameConfigSnapshot("ttw")
	if err != nil || effective.InstallPath != root || effective.SteamAppID != 22380 {
		t.Fatalf("effective snapshot = %+v, %v", effective, err)
	}
	for _, gc := range []config.GameConfig{raw, effective} {
		gc.Executables[0].Title = "changed"
		gc.Executables[0].Args[0] = "changed"
		gc.Executables[0].Environment["STATE"] = "changed"
		gc.Executables[0].ExtraRWPaths[0] = "changed"
	}
	check := func(gc config.GameConfig) {
		t.Helper()
		e := gc.Executables[0]
		if e.Title != "Original" || e.Args[0] != "original" || e.Environment["STATE"] != "original" || e.ExtraRWPaths[0] != "original" {
			t.Fatalf("snapshot aliased config: %+v", e)
		}
	}
	fresh, _ := d.gameConfigSnapshot("ttw")
	check(fresh)
	list, err := d.ListExecutables("ttw")
	if err != nil || list[0].Args[0] != "original" {
		t.Fatalf("list shares snapshot: %+v, %v", list, err)
	}
	_, err = d.UpsertExecutable("ttw", dto.ExecutableSpec{ID: "snapshot", Title: "New", ExePath: filepath.Join(root, "missing")})
	if err != nil {
		t.Fatal(err)
	}
	check(fresh)
	if err := d.RemoveExecutable("ttw", "snapshot"); err != nil {
		t.Fatal(err)
	}
	check(fresh)
	empty, _ := d.gameConfigSnapshot("empty")
	missing, _ := d.gameConfigSnapshot("nil")
	if empty.Executables == nil || missing.Executables != nil {
		t.Fatalf("nil/empty slices changed: %+v %+v", empty, missing)
	}
}

// TestToolsScopedConfigReadErrorCompatibility preserves missing-game and missing-parent outcomes.
func TestToolsScopedConfigReadErrorCompatibility(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newIsolatedDaemon(t, map[string]config.GameConfig{"ttw": {LinkedFromGameID: "absent"}})
	if _, err := d.ListProfileIniFiles("absent", "Default"); !errors.Is(err, config.ErrInvalidGameID) {
		t.Fatalf("INI error: %v", err)
	}
	if err := d.SetPluginOrder("absent", "Default", nil); !errors.Is(err, config.ErrInvalidGameID) {
		t.Fatalf("plugin error: %v", err)
	}
	if _, err := d.Get4GBPatchStatus("ttw"); err != nil {
		t.Fatalf("patch status error: %v", err)
	}
	if _, err := d.TranslateWinePath("ttw", ""); err != nil {
		t.Fatalf("translation fallback: %v", err)
	}
	if _, err := d.ListProfileIniFiles("ttw", "Default"); err == nil || !strings.Contains(err.Error(), "missing parent") {
		t.Fatalf("missing-parent error: %v", err)
	}
	if err := d.SaveProfileIniFile("absent", "Default", "FalloutCustom.ini", ""); !errors.Is(err, config.ErrInvalidGameID) {
		t.Fatalf("save error: %v", err)
	}
	if _, err := d.SetProfileIniEnabled("ttw", "Default", true); err == nil || !strings.Contains(err.Error(), "missing parent") {
		t.Fatalf("enable error: %v", err)
	}
	if _, err := d.ListExecutables("absent"); !errors.Is(err, config.ErrInvalidGameID) {
		t.Fatalf("executable error: %v", err)
	}
	if _, _, err := d.LaunchExecutable("absent", "unknown", ""); !errors.Is(err, config.ErrInvalidGameID) {
		t.Fatalf("launch error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(config.ConfigDir(), "config.json")); err == nil {
		t.Fatal("unexpected config save")
	}
}

// toolsNexusTransport replaces network dialing with in-memory HTTP responses.
func toolsNexusTransport(t *testing.T, arrived chan<- struct{}, release <-chan struct{}) {
	t.Helper()
	original := http.DefaultTransport
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	var wg sync.WaitGroup
	transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "api.nexusmods.com:443" {
			return nil, fmt.Errorf("unexpected network destination %s", addr)
		}
		client, server := net.Pipe()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer server.Close()
			_ = server.SetDeadline(time.Now().Add(15 * time.Second))
			request, err := http.ReadRequest(bufio.NewReader(server))
			if err != nil {
				return
			}
			_ = request.Body.Close()
			status := "503 Service Unavailable"
			if request.URL.Path == "/v3/games/skyrimspecialedition/mods/12604" {
				status = "200 OK"
			} else if arrived != nil {
				select {
				case arrived <- struct{}{}:
				default:
				}
				select {
				case <-release:
				case <-time.After(10 * time.Second):
				}
			}
			_, _ = fmt.Fprintf(server, "HTTP/1.1 %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", status)
		}()
		return client, nil
	}
	http.DefaultTransport = transport
	t.Cleanup(func() {
		http.DefaultTransport = original
		transport.CloseIdleConnections()
		wg.Wait()
	})
}

// TestToolsNexusKeyConcurrentReaders checks the plugin cache and both installers against key replacement.
func TestToolsNexusKeyConcurrentReaders(t *testing.T) {
	for _, variant := range []string{"plugin", "script extender", "4gb patcher"} {
		t.Run(variant, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			root := t.TempDir()
			toolsNexusTransport(t, nil, nil)
			if err := os.WriteFile(filepath.Join(root, "nvse_loader.exe"), []byte("loader"), 0600); err != nil {
				t.Fatal(err)
			}
			d := newIsolatedDaemon(t, map[string]config.GameConfig{
				"fallout3":  {InstallPath: root, DataSubpath: "Data", SteamAppID: 22300},
				"falloutnv": {InstallPath: root, DataSubpath: "Data", SteamAppID: 22380},
			})
			t.Cleanup(func() { toolsStopDownloadManager(d) })
			if result, err := d.SetNexusAPIKey(context.Background(), "tools-initial-key"); err != nil || !result.Valid {
				t.Fatalf("initial Nexus key: %+v %v", result, err)
			}
			reader := func(stop <-chan struct{}) error {
				for range 90 {
					if toolsStopped(stop) {
						return nil
					}
					switch variant {
					case "plugin":
						d.softDepFetcherMu.Lock()
						d.softDepFetcher = nil
						d.softDepFetcherMu.Unlock()
						_ = d.PluginStatusService.softDepFetcherLazy()
					case "script extender":
						_, err := d.InstallScriptExtender("fallout3")
						if err == nil || !strings.Contains(err.Error(), "listing") {
							return fmt.Errorf("script extender did not reach metadata request: %v", err)
						}
					case "4gb patcher":
						_, err := d.Install4GBPatcher("falloutnv")
						if err == nil || !strings.Contains(err.Error(), "metadata") {
							return fmt.Errorf("patcher did not reach metadata request: %v", err)
						}
					}
				}
				return nil
			}
			writer := func(stop <-chan struct{}) error {
				for i := range 35 {
					if toolsStopped(stop) {
						return nil
					}
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					result, err := d.SetNexusAPIKey(ctx, fmt.Sprintf("tools-key-%d", i))
					cancel()
					if err != nil || result == nil || !result.Valid {
						return fmt.Errorf("setting Nexus key: %+v %v", result, err)
					}
				}
				return nil
			}
			toolsRaceWorkers(t, writer, reader)
		})
	}
}

// TestToolsNetworkReadersReleaseSessionLock allows configuration to proceed during a stalled Nexus request.
func TestToolsNetworkReadersReleaseSessionLock(t *testing.T) {
	for _, variant := range []string{"script extender", "4gb patcher"} {
		t.Run(variant, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			root := t.TempDir()
			arrived := make(chan struct{}, 1)
			release := make(chan struct{})
			toolsNexusTransport(t, arrived, release)
			if err := os.WriteFile(filepath.Join(root, "nvse_loader.exe"), []byte("loader"), 0600); err != nil {
				t.Fatal(err)
			}
			d := newIsolatedDaemon(t, map[string]config.GameConfig{
				"fallout3":  {InstallPath: root, DataSubpath: "Data"},
				"falloutnv": {InstallPath: root, DataSubpath: "Data"},
			})
			d.mu.Lock()
			d.config.NexusAPIKey = "tools-key"
			d.mu.Unlock()
			requestDone := make(chan error, 1)
			go func() {
				if variant == "script extender" {
					_, err := d.InstallScriptExtender("fallout3")
					requestDone <- err
				} else {
					_, err := d.Install4GBPatcher("falloutnv")
					requestDone <- err
				}
			}()
			select {
			case <-arrived:
			case err := <-requestDone:
				close(release)
				t.Fatalf("request never reached in-memory transport: %v", err)
			case <-time.After(5 * time.Second):
				close(release)
				select {
				case err := <-requestDone:
					t.Fatalf("request did not start: %v", err)
				case <-time.After(5 * time.Second):
					t.Fatal("request did not finish after release")
				}
			}
			configured := make(chan error, 1)
			go func() { configured <- d.ConfigureGame("tools-writer", "Writer", 0, root, "Data") }()
			var writerErr error
			timedOut := false
			select {
			case writerErr = <-configured:
			case <-time.After(3 * time.Second):
				timedOut = true
				writerErr = errors.New("configuration blocked by a network reader")
			}
			close(release)
			if timedOut {
				select {
				case err := <-configured:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("configuration did not finish after releasing the network reader")
				}
			}
			if writerErr != nil {
				t.Error(writerErr)
			}
			select {
			case err := <-requestDone:
				if err == nil {
					t.Error("metadata failure was not returned")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("installer did not finish after releasing response")
			}
		})
	}
}

// TestToolsPreferredProtonConcurrentReaders checks late preference reads in Proton and TTW launch paths.
func TestToolsPreferredProtonConcurrentReaders(t *testing.T) {
	for _, variant := range []string{"executable", "wine installer", "prefix bootstrap"} {
		t.Run(variant, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			root := t.TempDir()
			game := config.GameConfig{InstallPath: root, DataSubpath: "Data", SteamAppID: 22380, Executables: []config.Executable{{ID: "missing-proton", Title: "Missing Proton", ExePath: filepath.Join(root, "missing.exe")}}}
			d := newIsolatedDaemon(t, map[string]config.GameConfig{"falloutnv": game, "fallout3": {InstallPath: root}})
			steamApps := filepath.Join(os.Getenv("XDG_DATA_HOME"), "Steam", "steamapps")
			if err := os.MkdirAll(steamApps, 0700); err != nil {
				t.Fatal(err)
			}
			prefix := filepath.Join(steamApps, "compatdata", "22380", "pfx")
			if variant != "prefix bootstrap" {
				if err := os.MkdirAll(prefix, 0700); err != nil {
					t.Fatal(err)
				}
			}
			path := t.TempDir()
			t.Setenv("PATH", path)
			writer := func(stop <-chan struct{}) error {
				for i := range 100 {
					if toolsStopped(stop) {
						return nil
					}
					d.mu.Lock()
					d.config.PreferredProton = fmt.Sprintf("proton-%d", i)
					err := d.config.Save()
					d.mu.Unlock()
					if err != nil {
						return err
					}
				}
				return nil
			}
			reader := func(stop <-chan struct{}) error {
				for range 300 {
					if toolsStopped(stop) {
						return nil
					}
					switch variant {
					case "executable":
						_, _, err := d.LaunchExecutable("falloutnv", "missing-proton", "")
						if err == nil {
							return errors.New("Proton launcher unexpectedly started")
						}
					case "wine installer":
						_, err := d.launchWineTTW("tools-pref", TTWInstallerInfo{Backend: TTWBackendWine, InstallerExe: filepath.Join(root, "missing.exe")}, filepath.Join(root, "dest"), game, game, "Output")
						if err == nil {
							return errors.New("Wine installer unexpectedly started")
						}
					case "prefix bootstrap":
						err := d.BootstrapFNVPrefix()
						if err == nil || !strings.Contains(err.Error(), "wine not on PATH") {
							return fmt.Errorf("bootstrap did not stop at missing wine: %v", err)
						}
					}
				}
				return nil
			}
			toolsRaceWorkers(t, writer, reader)
		})
	}
}

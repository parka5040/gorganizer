package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/ghrelease"
	"github.com/parka/gorganizer/internal/smapi"
)

// lifecycleRunWorkers runs concurrent test operations and reports their errors after every worker has stopped.
func lifecycleRunWorkers(t *testing.T, workers ...func(<-chan struct{}) error) {
	t.Helper()
	start := make(chan struct{})
	stop := make(chan struct{})
	failures := make(chan error, len(workers))
	var wg sync.WaitGroup
	for _, worker := range workers {
		wg.Add(1)
		go func(worker func(<-chan struct{}) error) {
			defer wg.Done()
			<-start
			failures <- worker(stop)
		}(worker)
	}
	close(start)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(25 * time.Second):
		close(stop)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent lifecycle workers did not stop after cancellation")
		}
		t.Fatal("concurrent lifecycle operations did not finish")
	}
	close(failures)
	for err := range failures {
		if err != nil {
			t.Error(err)
		}
	}
}

// lifecycleStopped reports whether a concurrent test worker should stop.
func lifecycleStopped(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

func TestGetConflictsConcurrentConfigureGame(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	install := filepath.Join(t.TempDir(), "game")
	if err := os.MkdirAll(filepath.Join(install, "Data"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		"custom": {Name: "Original", InstallPath: install, DataSubpath: "Data"},
		"other":  {Name: "Other", InstallPath: install, DataSubpath: "Data"},
	})
	if _, _, err := d.profileMgr.Load("custom", "Default"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.GetConflicts("absent", "Default"); !errors.Is(err, config.ErrInvalidGameID) || err.Error() != fmt.Sprintf("%v: absent", config.ErrInvalidGameID) {
		t.Fatalf("unknown game: %v", err)
	}
	lifecycleRequireReaderLock(t, d, func() error { _, err := d.GetConflicts("custom", "Default"); return err })
	lifecycleRunWorkers(t, func(stop <-chan struct{}) error {
		for i := range 100 {
			if lifecycleStopped(stop) {
				return nil
			}
			if err := d.ConfigureGame("custom", fmt.Sprintf("Custom-%d", i), 0, install, "Data"); err != nil {
				return err
			}
			if err := d.ConfigureGame("other", fmt.Sprintf("Other-%d", i), 0, install, "Data"); err != nil {
				return err
			}
		}
		return nil
	}, func(stop <-chan struct{}) error {
		for range 300 {
			if lifecycleStopped(stop) {
				return nil
			}
			conflicts, err := d.GetConflicts("custom", "Default")
			if err != nil || len(conflicts) != 0 {
				return fmt.Errorf("GetConflicts: %v, %v", conflicts, err)
			}
		}
		return nil
	})
}

func TestPreferredProtonConcurrentGetSet(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newIsolatedDaemon(t, nil)
	values := map[string]bool{"": true, "proton-short": true, "an-entirely-different-proton-path": true}
	lifecycleRequireReaderLock(t, d, func() error { _, err := d.GetPreferredProton(); return err })
	d.mu.RLock()
	setterDone := make(chan error, 1)
	go func() { setterDone <- d.SetPreferredProton("proton-short") }()
	select {
	case err := <-setterDone:
		d.mu.RUnlock()
		t.Fatalf("setter bypassed the daemon read lock: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	d.mu.RUnlock()
	select {
	case err := <-setterDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("setter did not resume after unlocking")
	}
	lifecycleRunWorkers(t, func(stop <-chan struct{}) error {
		for range 80 {
			if lifecycleStopped(stop) {
				return nil
			}
			if err := d.SetPreferredProton("proton-short"); err != nil {
				return err
			}
		}
		return nil
	}, func(stop <-chan struct{}) error {
		for range 80 {
			if lifecycleStopped(stop) {
				return nil
			}
			if err := d.SetPreferredProton("an-entirely-different-proton-path"); err != nil {
				return err
			}
			if err := d.SetPreferredProton(""); err != nil {
				return err
			}
		}
		return nil
	}, func(stop <-chan struct{}) error {
		for range 800 {
			if lifecycleStopped(stop) {
				return nil
			}
			got, err := d.GetPreferredProton()
			if err != nil || !values[got] {
				return fmt.Errorf("GetPreferredProton = %q, %v", got, err)
			}
		}
		return nil
	})
	if err := d.SetPreferredProton("final-proton"); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetPreferredProton()
	if err != nil || got != "final-proton" {
		t.Fatalf("final preference = %q, %v", got, err)
	}
	persisted, err := config.Load()
	if err != nil || persisted.PreferredProton != "final-proton" {
		t.Fatalf("persisted preference = %v, %v", persisted, err)
	}
}

func TestPreferredProtonSaveConcurrentConfigureGame(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	install := filepath.Join(t.TempDir(), "game")
	d := newIsolatedDaemon(t, nil)
	lifecycleRunWorkers(t, func(stop <-chan struct{}) error {
		for i := range 100 {
			if lifecycleStopped(stop) {
				return nil
			}
			if err := d.ConfigureGame("custom", fmt.Sprintf("name-%d", i), 0, install, "Data"); err != nil {
				return err
			}
		}
		return nil
	}, func(stop <-chan struct{}) error {
		for i := range 100 {
			if lifecycleStopped(stop) {
				return nil
			}
			if err := d.SetPreferredProton(fmt.Sprintf("proton-%d", i)); err != nil {
				return err
			}
		}
		return nil
	})
	persisted, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Games["custom"].Name != "name-99" || persisted.PreferredProton != "proton-99" {
		t.Fatalf("persisted config missing final writes: %+v", persisted)
	}
}

func TestLaunchGamePreferredProtonConcurrentSet(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	install := filepath.Join(t.TempDir(), "game")
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		"custom": {Name: "Custom", InstallPath: install, DataSubpath: "Data"},
	})
	lifecycleRunWorkers(t, func(stop <-chan struct{}) error {
		for i := range 120 {
			if lifecycleStopped(stop) {
				return nil
			}
			if err := d.SetPreferredProton(fmt.Sprintf("proton-%d", i)); err != nil {
				return err
			}
		}
		return nil
	}, func(stop <-chan struct{}) error {
		for range 400 {
			if lifecycleStopped(stop) {
				return nil
			}
			_, err := d.LaunchGame("custom", true, "")
			if err == nil || !strings.Contains(err.Error(), "finding Steam root:") {
				return fmt.Errorf("LaunchGame should stop before process launch: %v", err)
			}
		}
		return nil
	})
}

func TestConfigSnapshotsOwnExecutableStorage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	install := filepath.Join(t.TempDir(), "game")
	original := config.Executable{ID: "one", Title: "Old", ExePath: filepath.Join(install, "tool"), Args: []string{"old"}, Environment: map[string]string{"KEY": "old"}, ExtraRWPaths: []string{"old"}}
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		"custom": {InstallPath: install, Executables: []config.Executable{original}},
		"linked": {LinkedFromGameID: "custom", Executables: []config.Executable{original}},
		"nil":    {InstallPath: install},
		"empty":  {InstallPath: install, Executables: []config.Executable{}},
	})
	raw, ok := d.gameConfigSnapshot("custom")
	if !ok {
		t.Fatal("raw snapshot missing")
	}
	effective, err := d.effectiveGameConfigSnapshot("linked")
	if err != nil || effective.InstallPath != install {
		t.Fatalf("linked effective snapshot = %+v, %v", effective, err)
	}
	admitted, _, reservation, err := d.svc.launch.admitLaunch("custom")
	if err != nil {
		t.Fatal(err)
	}
	reservation.Release()
	if nilConfig, _ := d.gameConfigSnapshot("nil"); nilConfig.Executables != nil {
		t.Fatal("nil executable slice changed into a non-nil slice")
	}
	if empty, _ := d.gameConfigSnapshot("empty"); empty.Executables == nil {
		t.Fatal("non-nil empty executable slice changed into nil")
	}
	if _, err := d.UpsertExecutable("custom", dto.ExecutableSpec{ID: "one", Title: "New", ExePath: original.ExePath}); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveExecutable("linked", "one"); err != nil {
		t.Fatal(err)
	}
	beforeCustom, _ := d.gameConfigSnapshot("custom")
	beforeLinked, _ := d.gameConfigSnapshot("linked")
	for name, snap := range map[string]config.GameConfig{"raw": raw, "effective": effective, "admitted": admitted} {
		if !reflect.DeepEqual(snap.Executables[0], original) {
			t.Errorf("%s snapshot changed after live config writes: %+v", name, snap.Executables[0])
		}
		snap.Executables[0].Args[0] = "changed"
		snap.Executables[0].Environment["KEY"] = "changed"
		snap.Executables[0].ExtraRWPaths[0] = "changed"
		liveCustom, _ := d.gameConfigSnapshot("custom")
		liveLinked, _ := d.gameConfigSnapshot("linked")
		if !reflect.DeepEqual(liveCustom, beforeCustom) || !reflect.DeepEqual(liveLinked, beforeLinked) {
			t.Errorf("%s snapshot mutated live executable storage", name)
		}
	}
}

// lifecycleRequireReaderLock checks that reader waits for the daemon write lock and reports its result after release.
func lifecycleRequireReaderLock(t *testing.T, d *Daemon, reader func() error) {
	t.Helper()
	d.mu.Lock()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- reader()
	}()
	<-started
	select {
	case err := <-done:
		d.mu.Unlock()
		t.Fatalf("reader bypassed the daemon write lock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	d.mu.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not resume after unlocking")
	}
}

func TestLifecycleStateReadersConcurrentSettings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	install := filepath.Join(t.TempDir(), "game")
	if err := os.MkdirAll(filepath.Join(install, "Data"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		"parent": {Name: "Parent", InstallPath: install, DataSubpath: "Data"},
		"child":  {Name: "Child", LinkedFromGameID: "parent", DataSubpath: "Data"},
	})
	if _, err := d.MountVFS("child", "Default"); err != nil {
		t.Fatal(err)
	}
	for name, reader := range map[string]func() error{
		"list":   func() error { _, err := d.ListConfiguredGames(); return err },
		"status": func() error { _, err := d.GetVFSStatus("child"); return err },
		"admission": func() error {
			_, _, reservation, err := d.svc.launch.admitLaunch("parent")
			if err == nil {
				reservation.Release()
			}
			return err
		},
		"fence": func() error {
			release, err := d.acquireShared("child", dto.BusyOperationLaunch)
			if err == nil {
				release()
			}
			return err
		},
	} {
		t.Run(name, func(t *testing.T) { lifecycleRequireReaderLock(t, d, reader) })
	}
	lifecycleRunWorkers(t, func(stop <-chan struct{}) error {
		for i := range 55 {
			if lifecycleStopped(stop) {
				return nil
			}
			if err := d.ConfigureGame("other", fmt.Sprintf("Other-%d", i), 0, install, "Data"); err != nil {
				return err
			}
			if _, err := d.UpsertExecutable("parent", dto.ExecutableSpec{ID: "tool", Title: "Tool", ExePath: filepath.Join(install, "missing")}); err != nil {
				return err
			}
			if err := d.SetPreferredProton(fmt.Sprintf("proton-%d", i)); err != nil {
				return err
			}
		}
		return nil
	}, func(stop <-chan struct{}) error {
		for range 180 {
			if lifecycleStopped(stop) {
				return nil
			}
			games, err := d.ListConfiguredGames()
			if err != nil || len(games) < 2 {
				return fmt.Errorf("ListConfiguredGames: %v, %v", games, err)
			}
			status, err := d.GetVFSStatus("child")
			if err != nil || !status.Mounted {
				return fmt.Errorf("GetVFSStatus: %v, %v", status, err)
			}
			_, _, reservation, err := d.svc.launch.admitLaunch("parent")
			if err != nil {
				return err
			}
			reservation.Release()
		}
		return nil
	})
	if err := d.RebuildVFS("child"); err != nil {
		t.Fatal(err)
	}
	if err := d.UnmountVFS("child"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.GetVFSStatus("missing"); err != nil {
		t.Fatalf("unknown game's VFS status: %v", err)
	}
}

func TestRecoverAllConcurrentConfigWriters(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	install := filepath.Join(t.TempDir(), "game")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	games := make(map[string]config.GameConfig)
	for i := range 25 {
		games[fmt.Sprintf("game-%d", i)] = config.GameConfig{InstallPath: install, DataSubpath: "Data"}
	}
	d := newUnrecoveredDaemon(t, games, nil)
	configureDone := make(chan error, 1)
	go func() { configureDone <- d.ConfigureGame("late", "Late", 0, install, "Data") }()
	select {
	case err := <-configureDone:
		t.Fatalf("ConfigureGame completed before startup recovery: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	lifecycleRunWorkers(t, func(stop <-chan struct{}) error {
		if lifecycleStopped(stop) {
			return nil
		}
		d.RecoverAll()
		return nil
	}, func(stop <-chan struct{}) error {
		for i := range 60 {
			if lifecycleStopped(stop) {
				return nil
			}
			if _, err := d.UpsertExecutable("game-0", dto.ExecutableSpec{ID: "tool", Title: "Tool", ExePath: filepath.Join(install, "missing")}); err != nil {
				return err
			}
			if err := d.SetPreferredProton(fmt.Sprintf("proton-%d", i)); err != nil {
				return err
			}
		}
		return nil
	})
	select {
	case err := <-configureDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ConfigureGame did not resume after recovery")
	}
}

func TestLoaderOperationReleasesStateLockBeforeWork(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	started := make(chan struct{})
	proceed := make(chan struct{})
	engine := &fakeLoaderEngine{status: smapi.Status{State: smapi.StateNotInstalled}}
	engine.installFn = func(context.Context, smapi.Artifact, func(string, string)) (*smapi.Record, error) {
		close(started)
		<-proceed
		return &smapi.Record{Version: "test"}, nil
	}
	d, _ := newLoaderTestDaemon(t, engine, &fakeLoaderArtifacts{latest: ghrelease.Release{Version: "test"}})
	finished := make(chan error, 1)
	go func() { _, err := d.InstallModLoader(context.Background(), "stardewvalley", false); finished <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(proceed)
		t.Fatal("loader operation never reached the fake installer")
	}
	otherInstall := filepath.Join(t.TempDir(), "other")
	workersDone := make(chan error, 1)
	go func() {
		workersDone <- d.SetPreferredProton("while-loader-runs")
	}()
	configureDone := make(chan error, 1)
	go func() { configureDone <- d.ConfigureGame("other", "Other", 0, otherInstall, "Data") }()
	statusDone := make(chan error, 1)
	go func() {
		_, err := d.GetModLoaderStatus(context.Background(), "stardewvalley", false)
		statusDone <- err
	}()
	var blocked []<-chan error
	for _, result := range []<-chan error{workersDone, configureDone, statusDone} {
		select {
		case err := <-result:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			blocked = append(blocked, result)
		}
	}
	close(proceed)
	for _, result := range blocked {
		select {
		case <-result:
		case <-time.After(5 * time.Second):
			t.Fatal("state operation did not finish after loader work")
		}
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(5 * time.Second):
		t.Error("loader operation did not finish")
	}
	if len(blocked) > 0 {
		t.Fatal("state operations blocked behind the loader installer")
	}
}

func TestShutdownPreferenceAccessAfterFarmDeactivation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newIsolatedDaemon(t, nil)
	stop := make(chan struct{})
	writerDone := make(chan error, 1)
	readerDone := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				writerDone <- nil
				return
			default:
			}
			if err := d.SetPreferredProton("during-shutdown"); err != nil {
				writerDone <- err
				return
			}
		}
	}()
	go func() {
		for {
			select {
			case <-stop:
				readerDone <- nil
				return
			default:
			}
			if _, err := d.GetPreferredProton(); err != nil {
				readerDone <- err
				return
			}
		}
	}()
	time.Sleep(25 * time.Millisecond)
	callbackDone := make(chan error, 1)
	stopped := make(chan struct{})
	go func() {
		d.shutdownAll(func() {
			close(stop)
			select {
			case err := <-writerDone:
				if err != nil {
					callbackDone <- err
					return
				}
			case <-time.After(5 * time.Second):
				callbackDone <- fmt.Errorf("preference writer did not stop")
				return
			}
			if _, err := d.GetPreferredProton(); err != nil {
				callbackDone <- err
				return
			}
			callbackDone <- d.SetPreferredProton("after-deactivation")
		})
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown did not release the state lock before stopping IPC")
	}
	if err := <-callbackDone; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-readerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("preference reader did not stop")
	}
}

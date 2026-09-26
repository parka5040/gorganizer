package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/gamedef"
	"github.com/parka/gorganizer/internal/ghrelease"
	"github.com/parka/gorganizer/internal/smapi"
	"github.com/parka/gorganizer/internal/vfs"
)

type fakeLoaderEngine struct {
	mu          sync.Mutex
	status      smapi.Status
	inspectErr  error
	installFn   func(ctx context.Context, art smapi.Artifact, progress func(phase, detail string)) (*smapi.Record, error)
	uninstallFn func(ctx context.Context) error
	recoverFn   func(gameDir string) (smapi.RecoverResult, error)
	installs    []string
	uninstalls  int
	recovers    int
	inspects    int
}

type fakeLoaderArtifacts struct {
	mu          sync.Mutex
	latest      ghrelease.Release
	latestErr   error
	latestCalls int
	latestGate  chan struct{}
	latestIn    chan struct{}
	fetchErr    error
	fetched     []string
	active      string
	previous    string
	unusable    map[string]bool
	activated   []string
	rollbacks   int
}

// Inspect returns the configured status.
func (f *fakeLoaderEngine) Inspect(string) (smapi.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inspects++
	return f.status, f.inspectErr
}

// Install records the artifact version and runs the configured install behaviour.
func (f *fakeLoaderEngine) Install(ctx context.Context, _ string, art smapi.Artifact, progress func(phase, detail string)) (*smapi.Record, error) {
	f.mu.Lock()
	f.installs = append(f.installs, art.Version)
	fn := f.installFn
	f.mu.Unlock()
	if fn == nil {
		progress("run", art.Version)
		return &smapi.Record{Version: art.Version}, nil
	}
	return fn(ctx, art, progress)
}

// Uninstall counts the call and runs the configured uninstall behaviour.
func (f *fakeLoaderEngine) Uninstall(ctx context.Context, _ string, _ func(phase, detail string)) error {
	f.mu.Lock()
	f.uninstalls++
	fn := f.uninstallFn
	f.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(ctx)
}

// Recover counts the call and runs the configured recovery behaviour.
func (f *fakeLoaderEngine) Recover(gameDir string) (smapi.RecoverResult, error) {
	f.mu.Lock()
	f.recovers++
	fn := f.recoverFn
	f.mu.Unlock()
	if fn == nil {
		return smapi.RecoverResult{}, nil
	}
	return fn(gameDir)
}

// counts returns the engine's install versions and call counters.
func (f *fakeLoaderEngine) counts() ([]string, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.installs...), f.uninstalls, f.recovers
}

// Latest returns the configured release, first parking on the gate when one is set.
func (f *fakeLoaderArtifacts) Latest(ctx context.Context) (ghrelease.Release, error) {
	f.mu.Lock()
	f.latestCalls++
	gate, entered := f.latestGate, f.latestIn
	rel, err := f.latest, f.latestErr
	f.mu.Unlock()
	if gate != nil {
		close(entered)
		<-gate
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ghrelease.Release{}, ctxErr
	}
	return rel, err
}

// Fetch records the fetched version and returns a fake artifact.
func (f *fakeLoaderArtifacts) Fetch(ctx context.Context, rel ghrelease.Release) (smapi.Artifact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return smapi.Artifact{}, err
	}
	if f.fetchErr != nil {
		return smapi.Artifact{}, f.fetchErr
	}
	f.fetched = append(f.fetched, rel.Version)
	return smapi.Artifact{Version: rel.Version, SHA256: rel.SHA256, ZipPath: "/fake/" + rel.Version}, nil
}

// Artifact returns a fake retained artifact unless the version is marked unusable.
func (f *fakeLoaderArtifacts) Artifact(version string) (smapi.Artifact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unusable[version] {
		return smapi.Artifact{}, fmt.Errorf("artifact %s damaged", version)
	}
	return smapi.Artifact{Version: version, ZipPath: "/fake/" + version}, nil
}

// ActiveVersion returns the active pointer.
func (f *fakeLoaderArtifacts) ActiveVersion() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active, nil
}

// PreviousVersion returns the previous pointer.
func (f *fakeLoaderArtifacts) PreviousVersion() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.previous, nil
}

// Activate records the activation and moves the pointers like ghrelease.Store.
func (f *fakeLoaderArtifacts) Activate(version string) (ghrelease.Current, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activated = append(f.activated, version)
	if f.active != "" && f.active != version {
		f.previous = f.active
	}
	f.active = version
	return ghrelease.Current{ActiveVersion: f.active, PreviousVersion: f.previous}, nil
}

// Rollback swaps the pointers like ghrelease.Store.
func (f *fakeLoaderArtifacts) Rollback() (ghrelease.Current, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rollbacks++
	if f.previous == "" {
		return ghrelease.Current{}, ghrelease.ErrNoPrevious
	}
	f.active, f.previous = f.previous, f.active
	return ghrelease.Current{ActiveVersion: f.active, PreviousVersion: f.previous}, nil
}

// pointers returns the store pointers and mutation counters.
func (f *fakeLoaderArtifacts) pointers() (string, string, []string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active, f.previous, append([]string(nil), f.activated...), f.rollbacks
}

// newStardewInstall creates a native Stardew install directory with its executable markers.
func newStardewInstall(t *testing.T) string {
	t.Helper()
	install := filepath.Join(t.TempDir(), "Stardew Valley")
	for _, marker := range []string{"Stardew Valley", "StardewValley"} {
		writeFixture(t, filepath.Join(install, marker))
	}
	return install
}

// newLoaderTestDaemon builds an isolated Stardew daemon whose mod-loader service uses the given fakes.
func newLoaderTestDaemon(t *testing.T, engine *fakeLoaderEngine, arts *fakeLoaderArtifacts) (*Daemon, string) {
	t.Helper()
	install := newStardewInstall(t)
	d := newIsolatedDaemon(t, map[string]config.GameConfig{
		"stardewvalley": {Name: "Stardew Valley", InstallPath: install, DataSubpath: "Mods", SteamAppID: 413150},
	})
	if engine != nil {
		d.svc.modLoader.engineFor = func(smapi.LoaderSpec) loaderEngine { return engine }
	}
	if arts != nil {
		d.svc.modLoader.newArtifacts = func(*gamedef.ModLoaderSpec) (loaderArtifacts, error) { return arts, nil }
	}
	return d, install
}

// statusLines collects status Info lines until one contains want or the timeout expires.
func statusLines(t *testing.T, d *Daemon, want string) []string {
	t.Helper()
	var lines []string
	deadline := time.After(5 * time.Second)
	for {
		select {
		case evt := <-d.WatchStatus():
			if evt.Info != "" {
				lines = append(lines, evt.Info)
				if strings.Contains(evt.Info, want) {
					return lines
				}
			}
		case <-deadline:
			t.Fatalf("status line containing %q not seen; got %v", want, lines)
			return nil
		}
	}
}

func TestLoaderSpecForMapsTheRegistryRow(t *testing.T) {
	spec, def, ok := loaderSpecFor("stardewvalley")
	if !ok || def == nil || def.Kind != gamedef.ModLoaderSMAPI {
		t.Fatalf("loaderSpecFor(stardewvalley) = %v, %+v, %v", spec, def, ok)
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("mapped spec is invalid: %v", err)
	}
	if spec.LauncherName != def.LauncherName || spec.LoaderExecutable != def.LoaderExecutable ||
		!reflect.DeepEqual(spec.BundledModIDs, def.BundledModIDs) || !reflect.DeepEqual(spec.UninstallPaths, def.UninstallPaths) {
		t.Errorf("mapped spec %+v does not mirror %+v", spec, def)
	}
	for _, gameID := range []string{"skyrimse", "falloutnv", "ttw", "unknown"} {
		if _, _, ok := loaderSpecFor(gameID); ok {
			t.Errorf("loaderSpecFor(%s) reports a loader", gameID)
		}
	}
}

func TestInstallModLoaderActivatesOnlyAfterSuccess(t *testing.T) {
	engine := &fakeLoaderEngine{status: smapi.Status{State: smapi.StateNotInstalled}}
	arts := &fakeLoaderArtifacts{latest: ghrelease.Release{Version: "4.6.0", SHA256: strings.Repeat("a", 64)}, active: "4.5.2"}
	d, _ := newLoaderTestDaemon(t, engine, arts)

	if _, err := d.InstallModLoader(t.Context(), "stardewvalley", false); err != nil {
		t.Fatalf("InstallModLoader: %v", err)
	}
	statusLines(t, d, "[smapi:done]")
	installs, _, _ := engine.counts()
	active, previous, activated, _ := arts.pointers()
	if !reflect.DeepEqual(installs, []string{"4.6.0"}) || !reflect.DeepEqual(activated, []string{"4.6.0"}) || active != "4.6.0" || previous != "4.5.2" {
		t.Fatalf("installs=%v activated=%v active=%s previous=%s", installs, activated, active, previous)
	}

	engine.installFn = func(context.Context, smapi.Artifact, func(string, string)) (*smapi.Record, error) {
		return nil, fmt.Errorf("SMAPI installer result rejected: %w", smapi.ErrStageUnexpected)
	}
	arts.latest = ghrelease.Release{Version: "4.7.0", SHA256: strings.Repeat("b", 64)}
	_, err := d.InstallModLoader(t.Context(), "stardewvalley", false)
	if reason, ok := smapi.FailureReason(err); !ok || reason != smapi.FailureStageUnexpected {
		t.Fatalf("failed install error = %v, want stage_unexpected", err)
	}
	statusLines(t, d, "[smapi:failed]")
	active, previous, activated, _ = arts.pointers()
	if active != "4.6.0" || previous != "4.5.2" || len(activated) != 1 {
		t.Errorf("failed install moved pointers: active=%s previous=%s activated=%v", active, previous, activated)
	}
	if held := d.exclusiveHeld(filepath.Clean(d.config.Games["stardewvalley"].InstallPath)); held {
		t.Error("exclusive fence still held after the operation returned")
	}
}

func TestInstallModLoaderTreatsACommittedInterruptedInstallAsSuccess(t *testing.T) {
	engine := &fakeLoaderEngine{status: smapi.Status{State: smapi.StateNotInstalled}}
	engine.installFn = func(_ context.Context, art smapi.Artifact, _ func(string, string)) (*smapi.Record, error) {
		return &smapi.Record{Version: art.Version}, fmt.Errorf("removing intent: %w", smapi.ErrInterrupted)
	}
	arts := &fakeLoaderArtifacts{latest: ghrelease.Release{Version: "4.6.0"}}
	d, _ := newLoaderTestDaemon(t, engine, arts)

	if _, err := d.InstallModLoader(t.Context(), "stardewvalley", false); err != nil {
		t.Fatalf("InstallModLoader: %v", err)
	}
	_, _, recovers := engine.counts()
	active, _, _, _ := arts.pointers()
	if recovers != 1 || active != "4.6.0" {
		t.Errorf("recovers=%d active=%s, want one in-process recovery and the new version active", recovers, active)
	}
	if pending := d.recoveryPendingFor("stardewvalley"); pending != nil {
		t.Errorf("successful recovery left a pending entry: %+v", pending)
	}
}

func TestInstallModLoaderRegistersPendingWhenInProcessRecoveryFails(t *testing.T) {
	engine := &fakeLoaderEngine{status: smapi.Status{State: smapi.StateNotInstalled}}
	engine.installFn = func(context.Context, smapi.Artifact, func(string, string)) (*smapi.Record, error) {
		return nil, fmt.Errorf("apply failed; rolling back failed: disk: %w", smapi.ErrInterrupted)
	}
	engine.recoverFn = func(string) (smapi.RecoverResult, error) {
		return smapi.RecoverResult{}, errors.New("preimage missing")
	}
	arts := &fakeLoaderArtifacts{latest: ghrelease.Release{Version: "4.6.0"}}
	d, install := newLoaderTestDaemon(t, engine, arts)

	_, err := d.InstallModLoader(t.Context(), "stardewvalley", false)
	if reason, ok := smapi.FailureReason(err); !ok || reason != smapi.FailureInterrupted {
		t.Fatalf("InstallModLoader error = %v, want interrupted", err)
	}
	pending := d.recoveryPendingFor("stardewvalley")
	if pending == nil || pending.DataPath != install || !strings.HasPrefix(pending.Reason, loaderRecoveryPrefix) {
		t.Fatalf("pending = %+v, want a mod-loader entry for %s", pending, install)
	}
	if _, err := d.InstallModLoader(t.Context(), "stardewvalley", false); err == nil || !strings.Contains(err.Error(), "recovery pending") {
		t.Errorf("second install error = %v, want recovery pending", err)
	}
	if active, _, activated, _ := arts.pointers(); active != "" || len(activated) != 0 {
		t.Errorf("failed install activated %v", activated)
	}
}

func TestInstallModLoaderRepairUsesTheActiveArtifactOffline(t *testing.T) {
	engine := &fakeLoaderEngine{status: smapi.Status{State: smapi.StateLauncherReverted}}
	arts := &fakeLoaderArtifacts{latestErr: errors.New("network must not be used"), active: "4.5.2", previous: "4.5.1"}
	d, _ := newLoaderTestDaemon(t, engine, arts)

	if _, err := d.InstallModLoader(t.Context(), "stardewvalley", true); err != nil {
		t.Fatalf("repair: %v", err)
	}
	installs, _, _ := engine.counts()
	active, previous, _, _ := arts.pointers()
	if !reflect.DeepEqual(installs, []string{"4.5.2"}) || arts.latestCalls != 0 || active != "4.5.2" || previous != "4.5.1" {
		t.Errorf("installs=%v latestCalls=%d active=%s previous=%s", installs, arts.latestCalls, active, previous)
	}

	arts.unusable = map[string]bool{"4.5.2": true}
	arts.latest = ghrelease.Release{Version: "4.6.0"}
	_, err := d.InstallModLoader(t.Context(), "stardewvalley", true)
	if reason, ok := smapi.FailureReason(err); !ok || reason != smapi.FailureNoArtifact {
		t.Fatalf("repair with a damaged artifact = %v, want no_artifact", err)
	}
	statusLines(t, d, "[smapi:failed]")
	if installs, _, _ := engine.counts(); !reflect.DeepEqual(installs, []string{"4.5.2"}) || arts.latestCalls != 0 || len(arts.fetched) != 0 {
		t.Errorf("installs=%v latestCalls=%d fetched=%v, want a damaged artifact to fail without downloading", installs, arts.latestCalls, arts.fetched)
	}
	if active, previous, activated, _ := arts.pointers(); active != "4.5.2" || previous != "4.5.1" || len(activated) != 1 {
		t.Errorf("failed repair moved pointers: active=%s previous=%s activated=%v", active, previous, activated)
	}
}

func TestInstallModLoaderRepairWithoutAnActiveArtifactFailsOffline(t *testing.T) {
	engine := &fakeLoaderEngine{status: smapi.Status{State: smapi.StateLauncherReverted}}
	arts := &fakeLoaderArtifacts{latest: ghrelease.Release{Version: "4.6.0"}}
	d, _ := newLoaderTestDaemon(t, engine, arts)

	_, err := d.InstallModLoader(t.Context(), "stardewvalley", true)
	var failed *smapi.FailedError
	if !errors.As(err, &failed) || failed.Reason != smapi.FailureNoArtifact || failed.GameID != "stardewvalley" {
		t.Fatalf("repair without an active artifact = %v, want FailedError(no_artifact)", err)
	}
	if !errors.Is(err, smapi.ErrNoArtifact) {
		t.Errorf("no_artifact error %v does not wrap ErrNoArtifact", err)
	}
	if installs, _, _ := engine.counts(); len(installs) != 0 || arts.latestCalls != 0 || len(arts.fetched) != 0 {
		t.Errorf("installs=%v latestCalls=%d fetched=%v, want nothing downloaded or installed", installs, arts.latestCalls, arts.fetched)
	}
	if active, _, activated, _ := arts.pointers(); active != "" || len(activated) != 0 {
		t.Errorf("failed repair activated %v", activated)
	}
}

func TestInstallModLoaderRPCCancellationDoesNotCancelTheInstall(t *testing.T) {
	started := make(chan struct{})
	proceed := make(chan struct{})
	installCtxErr := make(chan error, 1)
	engine := &fakeLoaderEngine{status: smapi.Status{State: smapi.StateNotInstalled}}
	engine.installFn = func(ctx context.Context, art smapi.Artifact, _ func(string, string)) (*smapi.Record, error) {
		close(started)
		<-proceed
		installCtxErr <- ctx.Err()
		return &smapi.Record{Version: art.Version}, nil
	}
	arts := &fakeLoaderArtifacts{latest: ghrelease.Release{Version: "4.6.0"}}
	d, _ := newLoaderTestDaemon(t, engine, arts)

	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := d.InstallModLoader(ctx, "stardewvalley", false)
		result <- err
	}()
	<-started
	cancel()
	close(proceed)
	if err := <-installCtxErr; err != nil {
		t.Fatalf("install context cancelled by the RPC context: %v", err)
	}
	if err := <-result; err != nil {
		t.Fatalf("InstallModLoader: %v", err)
	}
}

func TestInstallModLoaderRefusesUnsupportedBuildsBeforeDownloading(t *testing.T) {
	engine := &fakeLoaderEngine{status: smapi.Status{State: smapi.StateUnsupportedBuild, Detail: "found Stardew Valley.exe"}}
	arts := &fakeLoaderArtifacts{latest: ghrelease.Release{Version: "4.6.0"}}
	d, _ := newLoaderTestDaemon(t, engine, arts)

	_, err := d.InstallModLoader(t.Context(), "stardewvalley", false)
	var unavailable *smapi.UnavailableError
	if !errors.As(err, &unavailable) || unavailable.State != smapi.StateUnsupportedBuild {
		t.Fatalf("error = %v, want UnavailableError(unsupported_build)", err)
	}
	if installs, _, _ := engine.counts(); len(installs) != 0 || arts.latestCalls != 0 {
		t.Errorf("installs=%v latestCalls=%d, want nothing attempted", installs, arts.latestCalls)
	}
}

func TestInstallModLoaderMapsATransactionLockToBusy(t *testing.T) {
	engine := &fakeLoaderEngine{status: smapi.Status{State: smapi.StateNotInstalled}}
	engine.installFn = func(context.Context, smapi.Artifact, func(string, string)) (*smapi.Record, error) {
		return nil, fmt.Errorf("game is locked: %w", smapi.ErrTransactionActive)
	}
	d, _ := newLoaderTestDaemon(t, engine, &fakeLoaderArtifacts{latest: ghrelease.Release{Version: "4.6.0"}})

	_, err := d.InstallModLoader(t.Context(), "stardewvalley", false)
	var busy *dto.OperationBusyError
	if !errors.As(err, &busy) || busy.Operation != dto.BusyOperationTransaction || busy.GameID != "stardewvalley" {
		t.Fatalf("error = %v, want OperationBusyError(transaction)", err)
	}
}

func TestRollbackModLoaderSwapsPointersOnlyAfterSuccess(t *testing.T) {
	engine := &fakeLoaderEngine{status: smapi.Status{State: smapi.StateOK}}
	arts := &fakeLoaderArtifacts{active: "4.6.0"}
	d, _ := newLoaderTestDaemon(t, engine, arts)

	_, err := d.RollbackModLoader(t.Context(), "stardewvalley")
	if reason, ok := smapi.FailureReason(err); !ok || reason != smapi.FailureNoPrevious {
		t.Fatalf("rollback without previous = %v, want no_previous", err)
	}

	arts.previous = "4.5.2"
	engine.installFn = func(context.Context, smapi.Artifact, func(string, string)) (*smapi.Record, error) {
		return nil, fmt.Errorf("stage: %w", smapi.ErrStageIncomplete)
	}
	if _, err := d.RollbackModLoader(t.Context(), "stardewvalley"); err == nil {
		t.Fatal("failed rollback install reported success")
	}
	if active, previous, _, rollbacks := arts.pointers(); active != "4.6.0" || previous != "4.5.2" || rollbacks != 0 {
		t.Fatalf("failed rollback moved pointers: active=%s previous=%s rollbacks=%d", active, previous, rollbacks)
	}

	engine.installFn = nil
	if _, err := d.RollbackModLoader(t.Context(), "stardewvalley"); err != nil {
		t.Fatalf("RollbackModLoader: %v", err)
	}
	installs, _, _ := engine.counts()
	active, previous, _, rollbacks := arts.pointers()
	if !reflect.DeepEqual(installs, []string{"4.5.2", "4.5.2"}) || active != "4.5.2" || previous != "4.6.0" || rollbacks != 1 {
		t.Errorf("installs=%v active=%s previous=%s rollbacks=%d", installs, active, previous, rollbacks)
	}
}

func TestUninstallModLoaderLeavesPointersAlone(t *testing.T) {
	engine := &fakeLoaderEngine{status: smapi.Status{State: smapi.StateOK, Managed: true, Recorded: true, RecordedVersion: "4.6.0"}}
	arts := &fakeLoaderArtifacts{active: "4.6.0", previous: "4.5.2"}
	d, _ := newLoaderTestDaemon(t, engine, arts)

	if _, err := d.UninstallModLoader(t.Context(), "stardewvalley"); err != nil {
		t.Fatalf("UninstallModLoader: %v", err)
	}
	_, uninstalls, _ := engine.counts()
	active, previous, activated, rollbacks := arts.pointers()
	if uninstalls != 1 || active != "4.6.0" || previous != "4.5.2" || len(activated) != 0 || rollbacks != 0 {
		t.Errorf("uninstalls=%d active=%s previous=%s activated=%v rollbacks=%d", uninstalls, active, previous, activated, rollbacks)
	}

	engine.uninstallFn = func(context.Context) error {
		return fmt.Errorf("committed but did not finish: %w", smapi.ErrInterrupted)
	}
	engine.recoverFn = func(string) (smapi.RecoverResult, error) { return smapi.RecoverResult{Committed: true}, nil }
	if _, err := d.UninstallModLoader(t.Context(), "stardewvalley"); err != nil {
		t.Errorf("uninstall that recovery committed = %v, want success", err)
	}
}

func TestModLoaderRPCsRefuseGamesWithoutALoader(t *testing.T) {
	d, _ := newLoaderTestDaemon(t, &fakeLoaderEngine{}, &fakeLoaderArtifacts{})
	var unsupported *dto.ModLoaderUnsupportedError
	for name, call := range map[string]func() error{
		"status":    func() error { _, err := d.GetModLoaderStatus(t.Context(), "skyrimse", false); return err },
		"install":   func() error { _, err := d.InstallModLoader(t.Context(), "skyrimse", false); return err },
		"uninstall": func() error { _, err := d.UninstallModLoader(t.Context(), "skyrimse"); return err },
		"rollback":  func() error { _, err := d.RollbackModLoader(t.Context(), "skyrimse"); return err },
	} {
		if err := call(); !errors.As(err, &unsupported) || unsupported.GameID != "skyrimse" {
			t.Errorf("%s error = %v, want ModLoaderUnsupportedError", name, err)
		}
	}
}

func TestGetModLoaderStatusReportsVersionsLatestAndBusy(t *testing.T) {
	engine := &fakeLoaderEngine{status: smapi.Status{State: smapi.StateOK, Managed: true, Version: "4.5.2", Recorded: true, RecordedVersion: "4.5.2", Detail: "SMAPI is installed"}}
	arts := &fakeLoaderArtifacts{latest: ghrelease.Release{Version: "4.6.0"}, active: "4.5.2", previous: "4.5.1"}
	d, _ := newLoaderTestDaemon(t, engine, arts)

	got, err := d.GetModLoaderStatus(t.Context(), "stardewvalley", true)
	if err != nil {
		t.Fatalf("GetModLoaderStatus: %v", err)
	}
	want := dto.ModLoaderStatusResult{
		GameID: "stardewvalley", Kind: dto.ModLoaderKindSMAPI, State: dto.ModLoaderStateOK, Managed: true,
		InstalledVersion: "4.5.2", ActiveVersion: "4.5.2", PreviousVersion: "4.5.1", LatestVersion: "4.6.0",
		UpdateAvailable: true, Detail: "SMAPI is installed",
	}
	if got != want {
		t.Fatalf("status = %+v\nwant %+v", got, want)
	}
	if _, err := d.GetModLoaderStatus(t.Context(), "stardewvalley", true); err != nil || arts.latestCalls != 1 {
		t.Errorf("cached latest lookup made %d network calls (err %v), want 1", arts.latestCalls, err)
	}

	d.mu.Lock()
	release, err := d.reserveExclusiveLocked("stardewvalley", dto.BusyOperationModLoader)
	d.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	got, err = d.GetModLoaderStatus(t.Context(), "stardewvalley", false)
	release()
	if err != nil || !got.Busy || got.LatestVersion != "" {
		t.Errorf("status while reserved = %+v (%v), want busy without latest", got, err)
	}

	engine.status = smapi.Status{State: smapi.StateOK, Recorded: true, RecordedVersion: "4.5.2", Detail: "SMAPI is installed"}
	got, err = d.GetModLoaderStatus(t.Context(), "stardewvalley", true)
	if err != nil || got.Managed || got.InstalledVersion != "4.5.2" || got.UpdateAvailable || !strings.Contains(got.Detail, "files changed") {
		t.Errorf("drifted status = %+v (%v), want unmanaged 4.5.2 without an update offer", got, err)
	}

	d.svc.modLoader.latestMu.Lock()
	d.svc.modLoader.latest = map[gamedef.ModLoaderKind]latestRelease{}
	d.svc.modLoader.latestMu.Unlock()
	arts.latestErr = errors.New("rate limited")
	got, err = d.GetModLoaderStatus(t.Context(), "stardewvalley", true)
	if err != nil || got.LatestVersion != "" || !strings.Contains(got.Detail, "rate limited") {
		t.Errorf("status with a failing lookup = %+v (%v), want the failure in detail", got, err)
	}
}

func TestLatestVersionCacheIgnoresAStaleInFlightLookup(t *testing.T) {
	arts := &fakeLoaderArtifacts{latest: ghrelease.Release{Version: "4.5.0"}, latestGate: make(chan struct{}), latestIn: make(chan struct{})}
	d, _ := newLoaderTestDaemon(t, &fakeLoaderEngine{}, arts)
	ml := d.svc.modLoader
	_, def, _ := loaderSpecFor("stardewvalley")

	stale := make(chan string, 1)
	go func() {
		version, _ := ml.latestVersion(context.Background(), def)
		stale <- version
	}()
	<-arts.latestIn
	ml.rememberLatest(def.Kind, "4.6.0")
	close(arts.latestGate)
	if version := <-stale; version != "4.5.0" {
		t.Fatalf("in-flight lookup returned %q, want its own answer 4.5.0", version)
	}
	arts.mu.Lock()
	arts.latestGate = nil
	arts.mu.Unlock()
	if version, err := ml.latestVersion(t.Context(), def); err != nil || version != "4.6.0" || arts.latestCalls != 1 {
		t.Errorf("latestVersion = %q (%v) after %d lookups, want the newer 4.6.0 kept over the stale answer", version, err, arts.latestCalls)
	}
}

func TestShutdownCancelsAndWaitsForALoaderOperation(t *testing.T) {
	started := make(chan struct{})
	engine := &fakeLoaderEngine{status: smapi.Status{State: smapi.StateNotInstalled}}
	engine.installFn = func(ctx context.Context, _ smapi.Artifact, _ func(string, string)) (*smapi.Record, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	d, _ := newLoaderTestDaemon(t, engine, &fakeLoaderArtifacts{latest: ghrelease.Release{Version: "4.6.0"}})

	result := make(chan error, 1)
	go func() {
		_, err := d.InstallModLoader(context.Background(), "stardewvalley", false)
		result <- err
	}()
	<-started
	stopped := make(chan struct{})
	go func() {
		d.shutdownAll(nil)
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(20 * time.Second):
		t.Fatal("shutdown deadlocked while a mod-loader operation was running")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled install error = %v, want context.Canceled", err)
		}
	default:
		t.Fatal("shutdown returned before the loader operation finished")
	}
	if _, err := d.InstallModLoader(context.Background(), "stardewvalley", false); err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Errorf("install after shutdown = %v, want a shutdown refusal", err)
	}
}

func TestShutdownCompletesWhenALoaderOperationIgnoresCancellation(t *testing.T) {
	started := make(chan struct{})
	proceed := make(chan struct{})
	engine := &fakeLoaderEngine{status: smapi.Status{State: smapi.StateNotInstalled}}
	engine.installFn = func(_ context.Context, _ smapi.Artifact, progress func(string, string)) (*smapi.Record, error) {
		close(started)
		<-proceed
		progress("apply", "late")
		return nil, &smapi.RollbackFailedError{Cause: errors.New("disk full"), RollbackErr: errors.New("device busy")}
	}
	engine.recoverFn = func(string) (smapi.RecoverResult, error) {
		return smapi.RecoverResult{}, errors.New("preimage missing")
	}
	d, install := newLoaderTestDaemon(t, engine, &fakeLoaderArtifacts{latest: ghrelease.Release{Version: "4.6.0"}})
	const wait = 100 * time.Millisecond
	d.svc.modLoader.opWaitTimeout = wait

	rpcReturned := make(chan error, 1)
	go func() {
		_, err := d.InstallModLoader(context.Background(), "stardewvalley", false)
		rpcReturned <- err
	}()
	<-started
	var rpcErr error
	opFinishedUnderShutdownLock := false
	gracefulStop := func() {
		rpcErr = <-rpcReturned
		close(proceed)
		opDone := make(chan struct{})
		go func() {
			d.svc.modLoader.opWG.Wait()
			close(opDone)
		}()
		select {
		case <-opDone:
			opFinishedUnderShutdownLock = true
		case <-time.After(5 * time.Second):
		}
	}
	stopped := make(chan struct{})
	begin := time.Now()
	go func() {
		d.shutdownAll(gracefulStop)
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(wait + 15*time.Second):
		t.Fatal("shutdown did not complete while a mod-loader operation ignored cancellation")
	}
	if elapsed := time.Since(begin); elapsed > wait+10*time.Second {
		t.Errorf("shutdown took %v with a %v loader wait", elapsed, wait)
	}
	if rpcErr == nil || !strings.Contains(rpcErr.Error(), "shutting down") {
		t.Errorf("abandoned install RPC = %v, want a shutdown error", rpcErr)
	}
	if !opFinishedUnderShutdownLock {
		t.Fatal("the abandoned operation's failure path blocked while shutdown held the daemon lock")
	}
	pending := d.recoveryPendingFor("stardewvalley")
	if pending == nil || pending.DataPath != install {
		t.Errorf("failed in-process recovery after shutdown registered %+v, want a pending entry for %s", pending, install)
	}
	if held := d.exclusiveHeld(filepath.Clean(install)); held {
		t.Error("the abandoned operation kept its exclusive fence after finishing")
	}
}

func TestStatusPublishesAfterShutdownAreDropped(t *testing.T) {
	d, _ := newLoaderTestDaemon(t, &fakeLoaderEngine{}, &fakeLoaderArtifacts{})
	d.shutdownAll(nil)
	d.publishRecoveryEvent(dto.StatusEventResult{RecoveryPending: &dto.RecoveryPendingResult{GameID: "stardewvalley"}})
	d.svc.modLoader.loaderPublish(dto.StatusEventResult{Info: "[smapi:warning] late"})
	d.publishGuarded(dto.StatusEventResult{Info: "ready"})
	d.closeStatus()
}

func TestShutdownTimingsFitUnderTheWatchdog(t *testing.T) {
	const ipcStopBound = 5 * time.Second
	const minDeactivation = 8 * time.Second
	if loaderShutdownWait+shutdownBackgroundWait >= shutdownLaunchDeadline {
		t.Fatalf("loader wait %v + background wait %v leave no launch wait before the %v deadline",
			loaderShutdownWait, shutdownBackgroundWait, shutdownLaunchDeadline)
	}
	if shutdownLaunchDeadline+minDeactivation+ipcStopBound > ShutdownWatchdogTimeout {
		t.Fatalf("launch deadline %v + deactivation %v + IPC stop %v exceeds the watchdog %v",
			shutdownLaunchDeadline, minDeactivation, ipcStopBound, ShutdownWatchdogTimeout)
	}
	d, _ := newLoaderTestDaemon(t, &fakeLoaderEngine{}, &fakeLoaderArtifacts{})
	if d.svc.modLoader.opWaitTimeout != loaderShutdownWait {
		t.Errorf("service loader wait = %v, want %v", d.svc.modLoader.opWaitTimeout, loaderShutdownWait)
	}
	begin := time.Now()
	d.svc.modLoader.stopLoaderOps(time.Hour)
	if elapsed := time.Since(begin); elapsed > time.Second {
		t.Errorf("stopLoaderOps with no running operation took %v", elapsed)
	}
}

func TestLoaderOperationsRecoverAnInterruptedStateFirst(t *testing.T) {
	var order []string
	engine := &fakeLoaderEngine{status: smapi.Status{State: smapi.StateInterrupted}}
	engine.recoverFn = func(string) (smapi.RecoverResult, error) {
		order = append(order, "recover")
		return smapi.RecoverResult{RolledBack: true}, nil
	}
	engine.installFn = func(_ context.Context, art smapi.Artifact, _ func(string, string)) (*smapi.Record, error) {
		order = append(order, "install")
		return &smapi.Record{Version: art.Version}, nil
	}
	d, _ := newLoaderTestDaemon(t, engine, &fakeLoaderArtifacts{latest: ghrelease.Release{Version: "4.6.0"}})

	if _, err := d.InstallModLoader(t.Context(), "stardewvalley", false); err != nil {
		t.Fatalf("InstallModLoader over an interrupted state: %v", err)
	}
	if !reflect.DeepEqual(order, []string{"recover", "install"}) {
		t.Fatalf("operation order = %v, want recovery before the install", order)
	}

	order = nil
	engine.recoverFn = func(string) (smapi.RecoverResult, error) {
		order = append(order, "recover")
		return smapi.RecoverResult{}, errors.New("preimage missing")
	}
	_, err := d.InstallModLoader(t.Context(), "stardewvalley", false)
	if err == nil || !strings.Contains(err.Error(), "preimage missing") {
		t.Fatalf("install after a failed recovery = %v, want the recovery failure", err)
	}
	if !reflect.DeepEqual(order, []string{"recover"}) {
		t.Errorf("operation order = %v, want no install after a failed recovery", order)
	}
	if pending := d.recoveryPendingFor("stardewvalley"); pending == nil {
		t.Error("a failed in-process recovery registered no pending entry")
	}
}

func TestLoaderOperationsReportTheCauseOnceRecoveryRolledBack(t *testing.T) {
	cause := fmt.Errorf("Steam updated the game: %w", smapi.ErrGameChanged)
	rolledBack := func(string) (smapi.RecoverResult, error) { return smapi.RecoverResult{RolledBack: true}, nil }
	for name, run := range map[string]func(d *Daemon, engine *fakeLoaderEngine) error{
		"install": func(d *Daemon, engine *fakeLoaderEngine) error {
			engine.installFn = func(context.Context, smapi.Artifact, func(string, string)) (*smapi.Record, error) {
				return nil, &smapi.RollbackFailedError{Cause: cause, RollbackErr: errors.New("device busy")}
			}
			_, err := d.InstallModLoader(t.Context(), "stardewvalley", false)
			return err
		},
		"uninstall": func(d *Daemon, engine *fakeLoaderEngine) error {
			engine.uninstallFn = func(context.Context) error {
				return &smapi.RollbackFailedError{Cause: cause, RollbackErr: errors.New("device busy")}
			}
			_, err := d.UninstallModLoader(t.Context(), "stardewvalley")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			engine := &fakeLoaderEngine{status: smapi.Status{State: smapi.StateOK}, recoverFn: rolledBack}
			arts := &fakeLoaderArtifacts{latest: ghrelease.Release{Version: "4.6.0"}, active: "4.5.2"}
			d, _ := newLoaderTestDaemon(t, engine, arts)
			err := run(d, engine)
			if reason, ok := smapi.FailureReason(err); !ok || reason != smapi.FailureGameChanged {
				t.Fatalf("%s error = %v, want game_changed after a successful rollback", name, err)
			}
			if errors.Is(err, smapi.ErrInterrupted) {
				t.Errorf("%s error %v still reports an interruption that recovery resolved", name, err)
			}
			if _, _, recovers := engine.counts(); recovers != 1 {
				t.Errorf("recoveries = %d, want 1", recovers)
			}
			if pending := d.recoveryPendingFor("stardewvalley"); pending != nil {
				t.Errorf("successful recovery left %+v", pending)
			}
			if active, _, activated, _ := arts.pointers(); active != "4.5.2" || len(activated) != 0 {
				t.Errorf("failed %s moved pointers: active=%s activated=%v", name, active, activated)
			}
		})
	}
}

func TestInterruptedFailureWithoutACauseReportsARetryAfterRecovery(t *testing.T) {
	engine := &fakeLoaderEngine{status: smapi.Status{State: smapi.StateNotInstalled}}
	engine.installFn = func(context.Context, smapi.Artifact, func(string, string)) (*smapi.Record, error) {
		return nil, fmt.Errorf("%s exists: %w", smapi.IntentFile, smapi.ErrInterrupted)
	}
	d, _ := newLoaderTestDaemon(t, engine, &fakeLoaderArtifacts{latest: ghrelease.Release{Version: "4.6.0"}})

	_, err := d.InstallModLoader(t.Context(), "stardewvalley", false)
	if err == nil || errors.Is(err, smapi.ErrInterrupted) || !strings.Contains(err.Error(), "retry") {
		t.Fatalf("install whose interruption recovery resolved = %v, want a retry message without interrupted", err)
	}
	if _, ok := smapi.FailureReason(err); ok {
		t.Errorf("resolved interruption %v still carries a failure reason", err)
	}
}

func TestInstallModLoaderEmitsDoneOnceAfterTheInstallerApplied(t *testing.T) {
	engine := &fakeLoaderEngine{status: smapi.Status{State: smapi.StateNotInstalled}}
	engine.installFn = func(_ context.Context, art smapi.Artifact, progress func(string, string)) (*smapi.Record, error) {
		progress("apply", "")
		progress("applied", art.Version)
		return &smapi.Record{Version: art.Version}, nil
	}
	d, _ := newLoaderTestDaemon(t, engine, &fakeLoaderArtifacts{latest: ghrelease.Release{Version: "4.6.0"}})

	if _, err := d.InstallModLoader(t.Context(), "stardewvalley", false); err != nil {
		t.Fatalf("InstallModLoader: %v", err)
	}
	lines := statusLines(t, d, "[smapi:done]")
	var applied, done int
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "[smapi:applied]"):
			applied++
		case strings.HasPrefix(line, "[smapi:done]"):
			done++
			if applied == 0 {
				t.Errorf("[smapi:done] arrived before the installer's applied phase: %v", lines)
			}
		}
	}
	if applied != 1 || done != 1 {
		t.Errorf("status lines %v carry %d applied and %d done, want one each", lines, applied, done)
	}
}

// TestLoaderFarmGuardMatchesTheVFSNames locks that loader transactions check the configured deploy folder with exactly the sentinel and sibling names the VFS writes.
func TestLoaderFarmGuardMatchesTheVFSNames(t *testing.T) {
	engine := &fakeLoaderEngine{status: smapi.Status{State: smapi.StateNotInstalled}}
	d, install := newLoaderTestDaemon(t, engine, &fakeLoaderArtifacts{latest: ghrelease.Release{Version: "4.6.0"}})
	var mu sync.Mutex
	var specs []smapi.LoaderSpec
	d.svc.modLoader.engineFor = func(spec smapi.LoaderSpec) loaderEngine {
		mu.Lock()
		specs = append(specs, spec)
		mu.Unlock()
		return engine
	}
	if _, err := d.InstallModLoader(context.Background(), "stardewvalley", false); err != nil {
		t.Fatalf("InstallModLoader: %v", err)
	}
	want := smapi.FarmGuard{DeployDir: "Mods", Sentinel: vfs.SentinelFilename, SiblingSuffixes: vfs.FarmSiblingSuffixes()}
	mu.Lock()
	defer mu.Unlock()
	if len(specs) == 0 || !reflect.DeepEqual(specs[0].Farm, want) {
		t.Fatalf("loader operation farm guards = %+v, want %+v", specs, want)
	}

	d.mu.Lock()
	gc := d.config.Games["stardewvalley"]
	gc.DataSubpath = "Deploy/Mods"
	d.config.Games["stardewvalley"] = gc
	delete(d.mountMgrs, "stardewvalley")
	d.ensureMountManager("stardewvalley", gc)
	guard := d.loaderFarmGuardLocked("stardewvalley", install)
	d.mu.Unlock()
	if guard.DeployDir != "Deploy/Mods" {
		t.Errorf("farm guard deploy folder = %q, want the configured deploy folder", guard.DeployDir)
	}
}

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
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
)

// installOutcomeFixture creates a private daemon and an archive with enough files to trigger copy progress.
func installOutcomeFixture(t *testing.T) (*Daemon, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	d := newStardewDaemon(t)
	files := make(map[string]string)
	for i := range 40 {
		files[fmt.Sprintf("file-%03d.esp", i)] = "content"
	}
	archive := filepath.Join(t.TempDir(), "Update.zip")
	writeZipFiles(t, archive, files)
	return d, archive
}

// assertInstallOutcome checks that a recorded request reaches the expected final state.
func assertInstallOutcome(t *testing.T, d *Daemon, id string, want dto.InstallOutcomeState) dto.InstallOutcome {
	t.Helper()
	outcome, err := d.GetInstallOutcome("skyrimse", id)
	if err != nil || outcome.State != want {
		t.Fatalf("GetInstallOutcome(%q) = %+v, %v; want state %d", id, outcome, err, want)
	}
	return outcome
}

// TestStartInstallCancelledBeforePublishLeavesNothing cancels during staged copying without publishing or registering a mod.
func TestStartInstallCancelledBeforePublishLeavesNothing(t *testing.T) {
	d, archive := installOutcomeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.installCopyProgress = func(p download.InstallProgress) {
		if p.FilesDone >= 32 {
			cancel()
		}
	}
	_, _, err := d.StartInstall(ctx, dto.StartInstallRequest{
		ClientRequestID: "cancel-before-publish", GameID: "skyrimse", ExternalArchivePath: archive,
		Mode: dto.InstallAsNewMod, TargetMod: "Cancelled Mod",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("StartInstall error = %v, want context cancellation", err)
	}
	if _, err := os.Lstat(filepath.Join(config.ModsDir("skyrimse"), "Cancelled Mod")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled mod exists: %v", err)
	}
	assertNoReinstallLeftovers(t, config.ModsDir("skyrimse"))
	entries, err := d.GetModList("skyrimse", "Default")
	if err != nil || len(entries) != 0 {
		t.Fatalf("modlist after cancellation = %+v, %v", entries, err)
	}
	assertInstallOutcome(t, d, "cancel-before-publish", dto.InstallOutcomeCancelled)
}

// TestStartInstallCancelledAtFinalizingLeavesNothing checks cancellation immediately before publishing a completed stage.
func TestStartInstallCancelledAtFinalizingLeavesNothing(t *testing.T) {
	d, archive := installOutcomeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.installBeforePublish = cancel
	_, _, err := d.StartInstall(ctx, dto.StartInstallRequest{
		ClientRequestID: "cancel-at-finalizing", GameID: "skyrimse", ExternalArchivePath: archive,
		Mode: dto.InstallAsNewMod, TargetMod: "Finalizing Mod",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("StartInstall = %v, want cancellation", err)
	}
	if _, err := os.Lstat(filepath.Join(config.ModsDir("skyrimse"), "Finalizing Mod")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled mod exists: %v", err)
	}
	assertNoReinstallLeftovers(t, config.ModsDir("skyrimse"))
	entries, err := d.GetModList("skyrimse", "Default")
	if err != nil || len(entries) != 0 {
		t.Fatalf("modlist = %+v, %v", entries, err)
	}
	assertInstallOutcome(t, d, "cancel-at-finalizing", dto.InstallOutcomeCancelled)
}

// TestStartInstallCancelAfterPublishCompletes keeps a mod when cancellation follows its publishing rename.
func TestStartInstallCancelAfterPublishCompletes(t *testing.T) {
	d, archive := installOutcomeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.installAfterPublish = cancel
	folder, count, err := d.StartInstall(ctx, dto.StartInstallRequest{
		ClientRequestID: "cancel-after-publish", GameID: "skyrimse", ExternalArchivePath: archive,
		Mode: dto.InstallAsNewMod, TargetMod: "Published Mod",
	})
	if err != nil || folder != "Published Mod" || count != 40 {
		t.Fatalf("StartInstall = %q, %d, %v", folder, count, err)
	}
	if _, err := os.Stat(filepath.Join(config.ModsDir("skyrimse"), folder)); err != nil {
		t.Fatal(err)
	}
	entries, err := d.GetModList("skyrimse", "Default")
	if err != nil || len(entries) != 1 || entries[0].ModName != folder {
		t.Fatalf("modlist = %+v, %v", entries, err)
	}
	outcome := assertInstallOutcome(t, d, "cancel-after-publish", dto.InstallOutcomeSucceeded)
	if outcome.ModFolder != folder || outcome.FileCount != count {
		t.Fatalf("outcome = %+v", outcome)
	}
}

// TestReinstallCancelledBeforeCommitKeepsOriginal refuses a cancelled replay without changing the original tree.
func TestReinstallCancelledBeforeCommitKeepsOriginal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newStardewDaemon(t)
	folder, _ := installForReinstall(t, d, "skyrimse", "Original", map[string]string{"base.esp": "original"}, false)
	before := snapshotTree(t, filepath.Join(config.ModsDir("skyrimse"), folder))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.installCopyProgress = func(p download.InstallProgress) { cancel() }
	_, _, _, err := d.ReinstallMod(ctx, "skyrimse", folder, "reinstall-cancel")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReinstallMod error = %v", err)
	}
	if got := snapshotTree(t, filepath.Join(config.ModsDir("skyrimse"), folder)); !reflect.DeepEqual(got, before) {
		t.Fatalf("reinstall changed original: %v, want %v", got, before)
	}
	assertNoReinstallLeftovers(t, config.ModsDir("skyrimse"))
	assertInstallOutcome(t, d, "reinstall-cancel", dto.InstallOutcomeCancelled)
}

// TestMergeCancelledBeforePublishKeepsTarget preserves the original mod when a staged merge is cancelled.
func TestMergeCancelledBeforePublishKeepsTarget(t *testing.T) {
	d, folder, archive := mergeFixture(t)
	modDir := filepath.Join(config.ModsDir("skyrimse"), folder)
	before := snapshotTree(t, modDir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.installCopyProgress = func(p download.InstallProgress) { cancel() }
	_, _, err := d.StartInstall(ctx, dto.StartInstallRequest{
		ClientRequestID: "merge-cancel", GameID: "skyrimse", ExternalArchivePath: archive,
		Mode: dto.InstallMergeIntoMod, TargetMod: folder,
	})
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(snapshotTree(t, modDir), before) {
		t.Fatalf("merge = %v; target changed", err)
	}
	assertNoReinstallState(t, config.ModsDir("skyrimse"))
	assertInstallOutcome(t, d, "merge-cancel", dto.InstallOutcomeCancelled)
}

// TestInstallOutcomeRegistryLifecycle verifies running results, final errors, duplicate ids, bounded eviction, and expiry.
func TestInstallOutcomeRegistryLifecycle(t *testing.T) {
	base := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	current := base
	r := installOutcomeRegistry{now: func() time.Time { return current }}
	for _, id := range []string{"running", "success", "failure"} {
		if err := r.register("skyrimse", id); err != nil {
			t.Fatal(err)
		}
		outcome, err := r.lookup("skyrimse", id)
		if err != nil || outcome.State != dto.InstallOutcomeRunning {
			t.Fatalf("running outcome = %+v, %v", outcome, err)
		}
	}
	r.finish("success", context.Background(), true, dto.InstallOutcome{ModFolder: "Mod", FileCount: 3}, nil)
	failure := errors.New("install failed")
	r.finish("failure", context.Background(), false, dto.InstallOutcome{}, failure)
	for _, tc := range []struct {
		id    string
		state dto.InstallOutcomeState
	}{
		{"success", dto.InstallOutcomeSucceeded}, {"failure", dto.InstallOutcomeFailed},
		{"missing", dto.InstallOutcomeUnknown},
	} {
		outcome, err := r.lookup("skyrimse", tc.id)
		if err != nil || outcome.State != tc.state {
			t.Errorf("lookup(%s) = %+v, %v", tc.id, outcome, err)
		}
	}
	if got, _ := r.lookup("skyrimse", "failure"); !errors.Is(got.Err, failure) {
		t.Errorf("failed outcome lost error: %v", got.Err)
	}
	if err := r.register("skyrimse", "running"); !errors.Is(err, dto.ErrDuplicateClientRequestID) {
		t.Errorf("duplicate id error = %v", err)
	}
	if _, err := r.lookup("falloutnv", "running"); !errors.Is(err, dto.ErrInstallOutcomeGameMismatch) {
		t.Errorf("game mismatch error = %v", err)
	}
	for _, id := range []string{strings.Repeat("x", 65), "bad_id", "á"} {
		if err := r.register("skyrimse", id); !errors.Is(err, dto.ErrInvalidClientRequestID) {
			t.Errorf("invalid id %q error = %v", id, err)
		}
	}
	for i := range installOutcomeLimit - 3 {
		id := fmt.Sprintf("id-%d", i)
		if err := r.register("skyrimse", id); err != nil {
			t.Fatal(err)
		}
		r.finish(id, context.Background(), true, dto.InstallOutcome{}, nil)
	}
	if err := r.register("skyrimse", "next"); err != nil {
		t.Fatal(err)
	}
	if len(r.entries) != installOutcomeLimit {
		t.Fatalf("registry size = %d", len(r.entries))
	}
	if got, _ := r.lookup("skyrimse", "running"); got.State != dto.InstallOutcomeRunning {
		t.Fatal("running entry evicted")
	}
	current = current.Add(2 * time.Hour)
	if got, _ := r.lookup("skyrimse", "success"); got.State != dto.InstallOutcomeUnknown {
		t.Fatalf("expired outcome = %+v", got)
	}
	if got, _ := r.lookup("skyrimse", "next"); got.State != dto.InstallOutcomeRunning {
		t.Fatal("running entry expired")
	}
}

// TestInstallOutcomeConcurrentPolling races independent installs against status polls without sharing mod locks.
func TestInstallOutcomeConcurrentPolling(t *testing.T) {
	d, archive := installOutcomeFixture(t)
	var workers sync.WaitGroup
	const installs = 12
	for i := range installs {
		workers.Add(1)
		go func() {
			defer workers.Done()
			id := fmt.Sprintf("concurrent-%d", i)
			request := dto.StartInstallRequest{GameID: "skyrimse", ClientRequestID: id, ExternalArchivePath: archive,
				Mode: dto.InstallAsNewMod, TargetMod: id}
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				if _, _, err := d.StartInstall(context.Background(), request); err != nil {
					t.Errorf("install %s: %v", id, err)
				}
			}()
			for {
				select {
				case <-finished:
					if got := assertInstallOutcome(t, d, id, dto.InstallOutcomeSucceeded); got.ModFolder != id {
						t.Errorf("folder for %s = %q", id, got.ModFolder)
					}
					return
				default:
					if _, err := d.GetInstallOutcome("skyrimse", id); err != nil {
						t.Errorf("poll %s: %v", id, err)
					}
					time.Sleep(time.Millisecond)
				}
			}
		}()
	}
	workers.Wait()
}

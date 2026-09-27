package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/profile"
)

// TestCopyProfileRPCHoldsProfileLock verifies a mod-list writer waits for the entire copy.
func TestCopyProfileRPCHoldsProfileLock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newProfileDaemon(t, "Original")
	makeModFolders(t, "Old", "New")
	if err := d.SetModList("skyrimse", "Original", []dto.ModListEntryResult{{ModName: "Old", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	continueCopy := make(chan struct{})
	d.profileMgr.CopyFileDurable = func(src, dst string, perm os.FileMode, replace bool) (atomicfile.Outcome, error) {
		if filepath.Base(src) == "modlist.txt" {
			close(entered)
			<-continueCopy
		}
		return atomicfile.CopyFileDurable(src, dst, perm, replace)
	}
	copyDone := make(chan struct {
		profile *dto.ProfileResult
		err     error
	}, 1)
	go func() {
		result, err := d.CopyProfile("skyrimse", "Original", "Copied")
		copyDone <- struct {
			profile *dto.ProfileResult
			err     error
		}{result, err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("copy did not reach the modlist")
	}
	writerStarted := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		close(writerStarted)
		writerDone <- d.SetModList("skyrimse", "Original", []dto.ModListEntryResult{{ModName: "New", Enabled: false}})
	}()
	<-writerStarted
	select {
	case err := <-writerDone:
		t.Fatalf("writer finished before copy: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(continueCopy)
	result := <-copyDone
	if result.err != nil {
		t.Fatal(result.err)
	}
	persisted, _, err := d.profileMgr.Load("skyrimse", "Copied")
	if err != nil {
		t.Fatal(err)
	}
	if result.profile == nil || result.profile.CreatedAt != persisted.CreatedAt.UTC().Format("2006-01-02T15:04:05Z") {
		t.Fatalf("RPC timestamp %q differs from copied profile %s", result.profile.CreatedAt, persisted.CreatedAt)
	}
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	copied, err := os.ReadFile(filepath.Join(d.profileMgr.ProfileDir("skyrimse", "Copied"), "modlist.txt"))
	if err != nil || !strings.Contains(string(copied), "+Old\n") || strings.Contains(string(copied), "New") {
		t.Fatalf("copied modlist = %q, %v", copied, err)
	}
	original, err := os.ReadFile(filepath.Join(d.profileMgr.ProfileDir("skyrimse", "Original"), "modlist.txt"))
	if err != nil || !strings.Contains(string(original), "-New\n") {
		t.Fatalf("updated original modlist = %q, %v", original, err)
	}
	d.beginShutdown()
	if _, err := d.CopyProfile("skyrimse", "Original", "Refused"); err == nil {
		t.Fatal("copy succeeded during shutdown")
	}
}

// TestCopyProfileStagingSwept verifies startup recovery deletes only old copy stages.
func TestCopyProfileStagingSwept(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var old, recent, linked, outside string
	d := newUnrecoveredDaemon(t, newSkyrimGames(t), func(*Daemon) {
		profilesDir := config.ProfilesDir("skyrimse")
		old = filepath.Join(profilesDir, profile.CopyStagePrefix+"orphan")
		recent = filepath.Join(profilesDir, profile.CopyStagePrefix+"recent")
		linked = filepath.Join(profilesDir, profile.CopyStagePrefix+"link")
		outside = filepath.Join(t.TempDir(), "outside")
		agedDir(t, old, time.Hour)
		if err := os.Mkdir(recent, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(outside, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, linked); err != nil {
			t.Fatal(err)
		}
	})
	d.RecoverAll()
	for _, path := range []string{recent, linked, outside} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("sweep removed %s: %v", path, err)
		}
	}
	if _, err := os.Lstat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan copy stage survived recovery: %v", err)
	}
}

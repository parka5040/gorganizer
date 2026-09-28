package vfs

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// newMountedRetargetManager prepares two profiles with distinct mod files in a temporary installation.
func newMountedRetargetManager(t *testing.T) (*MountManager, string, []Layer) {
	t.Helper()
	root := t.TempDir()
	data := filepath.Join(root, "Data")
	modA := filepath.Join(root, "ModA")
	modB := filepath.Join(root, "ModB")
	mustFile(t, filepath.Join(data, "Skyrim.esm"), "base")
	mustFile(t, filepath.Join(modA, "A.esp"), "A")
	mustFile(t, filepath.Join(modB, "B.esp"), "B")
	mm := NewMountManager(data, "", "skyrimse")
	if err := mm.Activate([]Layer{{Name: "__base__", RootPath: data, Enabled: true}, {Name: "ModA", RootPath: modA, Enabled: true}}, "A"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mm.Deactivate() })
	return mm, data, []Layer{{Name: "__base__", RootPath: data, Enabled: true}, {Name: "ModB", RootPath: modB, Enabled: true}}
}

// TestRetargetRollsBackManagerOnExchangeFailure checks generations, applied layers, and desired layout survive a refused swap.
func TestRetargetRollsBackManagerOnExchangeFailure(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "Data")
	modA := filepath.Join(root, "ModA")
	modB := filepath.Join(root, "ModB")
	mustFile(t, filepath.Join(data, "Skyrim.esm"), "base")
	mustFile(t, filepath.Join(modA, "A.esp"), "A")
	mustFile(t, filepath.Join(modB, "B.esp"), "B")
	mm := NewMountManager(data, "", "skyrimse")
	initial := []Layer{{Name: "__base__", RootPath: data, Enabled: true}, {Name: "ModA", RootPath: modA, Enabled: true}}
	if err := mm.Activate(initial, "A"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mm.Deactivate() })
	if err := mm.MarkDirty([]Layer{{Name: "__base__", RootPath: data, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	applied, desired := mm.Generations()
	appliedLayers := mm.AppliedLayers()
	oldTree := mm.Tree()
	before := farmFiles(t, data)
	exchangeErr := errors.New("exchange refused")
	originalExchange := renameExchange
	renameExchange = func(_, _ string) error { return exchangeErr }
	t.Cleanup(func() { renameExchange = originalExchange })
	if err := mm.Retarget([]Layer{{Name: "__base__", RootPath: data, Enabled: true}, {Name: "ModB", RootPath: modB, Enabled: true}}, "B"); !errors.Is(err, exchangeErr) {
		t.Fatalf("Retarget error = %v, want exchange failure", err)
	}
	if a, d := mm.Generations(); a != applied || d != desired || !mm.IsDirty() {
		t.Errorf("generations = %d/%d dirty=%t, want %d/%d dirty", a, d, mm.IsDirty(), applied, desired)
	}
	if got := mm.AppliedLayers(); !reflect.DeepEqual(got, appliedLayers) {
		t.Errorf("applied layers = %+v, want %+v", got, appliedLayers)
	}
	if mm.Tree() != oldTree {
		t.Error("failed retarget discarded the previous desired tree")
	}
	if got := farmFiles(t, data); !reflect.DeepEqual(got, before) {
		t.Errorf("farm after failed exchange = %v, want %v", got, before)
	}
	sentinel, err := ReadSentinel(data)
	if err != nil || sentinel.ProfileName != "A" {
		t.Fatalf("sentinel = %+v, %v, want A", sentinel, err)
	}
	renameExchange = originalExchange
	if err := mm.ReMaterialize(); err != nil {
		t.Fatalf("applying old pending changes: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(data, "A.esp")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old pending layout was not restored: %v", err)
	}
}

// TestRetargetFailedExchangeAndCleanupNeedsRecovery checks a refused swap with leftover staging keeps A and reports pending cleanup.
func TestRetargetFailedExchangeAndCleanupNeedsRecovery(t *testing.T) {
	mm, data, layers := newMountedRetargetManager(t)
	originalExchange, originalRemove := renameExchange, removeFailedStaging
	exchangeErr := errors.New("exchange refused")
	cleanupErr := errors.New("staging removal refused")
	renameExchange = func(_, _ string) error { return exchangeErr }
	removeFailedStaging = func(string) error { return cleanupErr }
	t.Cleanup(func() { renameExchange, removeFailedStaging = originalExchange, originalRemove })
	err := mm.Retarget(layers, "B")
	var cleanup *RetargetCleanupError
	if !errors.As(err, &cleanup) || !errors.Is(err, exchangeErr) || !errors.Is(err, cleanupErr) {
		t.Fatalf("retarget = %v, want failed exchange and cleanup", err)
	}
	if sentinel, readErr := ReadSentinel(data); readErr != nil || sentinel.ProfileName != "A" {
		t.Fatalf("sentinel = %+v, %v, want A", sentinel, readErr)
	}
	if got := mm.AppliedLayers(); len(got) != 2 || got[1].Name != "ModA" {
		t.Errorf("applied layers = %+v, want ModA", got)
	}
}

// TestRetargetSyncFailureRestoresOldFarm checks a failed post-exchange sync reverses the swap and restores manager state.
func TestRetargetSyncFailureRestoresOldFarm(t *testing.T) {
	mm, data, layers := newMountedRetargetManager(t)
	originalSync := syncFarmParent
	calls := 0
	syncFarmParent = func(path string) error {
		calls++
		if calls == 1 {
			return errors.New("sync refused")
		}
		return originalSync(path)
	}
	t.Cleanup(func() { syncFarmParent = originalSync })
	before := farmFiles(t, data)
	if err := mm.Retarget(layers, "B"); err == nil {
		t.Fatal("retarget succeeded despite the failed sync")
	}
	if calls != 2 {
		t.Errorf("sync calls = %d, want 2", calls)
	}
	if got := farmFiles(t, data); !reflect.DeepEqual(got, before) {
		t.Errorf("farm after rollback = %v, want %v", got, before)
	}
	if sentinel, err := ReadSentinel(data); err != nil || sentinel.ProfileName != "A" {
		t.Fatalf("sentinel = %+v, %v, want A", sentinel, err)
	}
	if applied, desired := mm.Generations(); applied != desired || mm.IsDirty() {
		t.Errorf("generations = %d/%d dirty=%t, want unchanged clean", applied, desired, mm.IsDirty())
	}
}

// TestRetargetFailedRollbackSyncReportsCleanup checks an undurable reverse swap keeps A live but requires recovery.
func TestRetargetFailedRollbackSyncReportsCleanup(t *testing.T) {
	mm, data, layers := newMountedRetargetManager(t)
	originalSync := syncFarmParent
	syncErr := errors.New("parent sync refused")
	syncFarmParent = func(string) error { return syncErr }
	t.Cleanup(func() { syncFarmParent = originalSync })
	err := mm.Retarget(layers, "B")
	var cleanup *RetargetCleanupError
	if !errors.As(err, &cleanup) || !errors.Is(err, syncErr) {
		t.Fatalf("retarget = %v, want rollback cleanup failure", err)
	}
	if sentinel, readErr := ReadSentinel(data); readErr != nil || sentinel.ProfileName != "A" {
		t.Fatalf("sentinel = %+v, %v, want A", sentinel, readErr)
	}
	if got := mm.AppliedLayers(); len(got) != 2 || got[1].Name != "ModA" {
		t.Errorf("applied layers = %+v, want ModA", got)
	}
}

// TestRetargetFailedReverseSwapReportsCommittedFarm checks an unreversed swap retains B as the applied profile.
func TestRetargetFailedReverseSwapReportsCommittedFarm(t *testing.T) {
	mm, data, layers := newMountedRetargetManager(t)
	originalSync, originalExchange := syncFarmParent, renameExchange
	calls := 0
	syncFarmParent = func(string) error { return errors.New("sync refused") }
	renameExchange = func(a, b string) error {
		calls++
		if calls == 2 {
			return errors.New("reverse refused")
		}
		return originalExchange(a, b)
	}
	t.Cleanup(func() { syncFarmParent, renameExchange = originalSync, originalExchange })
	err := mm.Retarget(layers, "B")
	var committed *RetargetCommittedError
	if !errors.As(err, &committed) || calls != 2 {
		t.Fatalf("retarget = %v, exchanges = %d; want committed failure after reverse attempt", err, calls)
	}
	if sentinel, readErr := ReadSentinel(data); readErr != nil || sentinel.ProfileName != "B" {
		t.Fatalf("sentinel = %+v, %v, want B", sentinel, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(data, "B.esp")); statErr != nil {
		t.Errorf("new farm file missing: %v", statErr)
	}
	if applied, desired := mm.Generations(); applied != desired || mm.IsDirty() {
		t.Errorf("generations = %d/%d dirty=%t, want committed clean", applied, desired, mm.IsDirty())
	}
	if got := mm.AppliedLayers(); len(got) != 2 || got[1].Name != "ModB" {
		t.Errorf("applied layers = %+v, want ModB", got)
	}
}

// TestRetargetOldFarmCleanupFailureReportsCommittedFarm checks that failed removal leaves the new farm applied.
func TestRetargetOldFarmCleanupFailureReportsCommittedFarm(t *testing.T) {
	mm, data, layers := newMountedRetargetManager(t)
	originalRemove := removeOldFarm
	removeOldFarm = func(string) error { return errors.New("cleanup refused") }
	t.Cleanup(func() { removeOldFarm = originalRemove })
	err := mm.Retarget(layers, "B")
	var committed *RetargetCommittedError
	if !errors.As(err, &committed) {
		t.Fatalf("retarget = %v, want committed cleanup failure", err)
	}
	if sentinel, readErr := ReadSentinel(data); readErr != nil || sentinel.ProfileName != "B" {
		t.Fatalf("sentinel = %+v, %v, want B", sentinel, readErr)
	}
	if got := mm.AppliedLayers(); len(got) != 2 || got[1].Name != "ModB" {
		t.Errorf("applied layers = %+v, want ModB", got)
	}
	if applied, desired := mm.Generations(); applied != desired || mm.IsDirty() {
		t.Errorf("generations = %d/%d dirty=%t, want committed clean", applied, desired, mm.IsDirty())
	}
}

package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/ipc"
	"github.com/parka/gorganizer/internal/vfs"
)

func TestRecovery_PathKeyedSharedFNVAndTTW(t *testing.T) {
	dir := t.TempDir()
	fnvInstall := filepath.Join(dir, "fnv-install")
	dataPath := filepath.Join(fnvInstall, "Data")
	backupPath := dataPath + ".orig"
	if err := os.MkdirAll(dataPath, 0o755); err != nil {
		t.Fatalf("creating data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataPath, "rogue.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("writing rogue file: %v", err)
	}
	if err := os.MkdirAll(backupPath, 0o755); err != nil {
		t.Fatalf("creating backup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(backupPath, "FalloutNV.esm"), []byte("y"), 0o644); err != nil {
		t.Fatalf("writing master: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.Games["falloutnv"] = config.GameConfig{
		Name: "Fallout: New Vegas", InstallPath: fnvInstall,
		DataSubpath: "Data", SteamAppID: 22380,
	}
	cfg.Games["ttw"] = config.GameConfig{
		Name: "Tale of Two Wastelands", InstallPath: fnvInstall,
		DataSubpath: "Data", LinkedFromGameID: "falloutnv",
	}

	d, err := New(cfg)
	if err != nil {
		t.Fatalf("New daemon: %v", err)
	}
	defer d.Shutdown()

	d.RecoverAll()

	d.pendingRecoveriesMu.Lock()
	if len(d.pendingRecoveries) != 1 {
		t.Fatalf("expected exactly 1 pending entry, got %d", len(d.pendingRecoveries))
	}
	resolved, _ := filepath.Abs(dataPath)
	pending, ok := d.pendingRecoveries[resolved]
	if !ok {
		t.Fatalf("pending entry not keyed by resolved path %q; have keys: %v",
			resolved, mapKeys(d.pendingRecoveries))
	}
	if pending.DataPath != resolved {
		t.Errorf("pending DataPath = %q, want %q", pending.DataPath, resolved)
	}
	siblings := d.gamesAtPath[resolved]
	d.pendingRecoveriesMu.Unlock()

	if !contains(siblings, "falloutnv") || !contains(siblings, "ttw") {
		t.Errorf("gamesAtPath = %v, want both falloutnv and ttw", siblings)
	}

	for _, gid := range []string{"falloutnv", "ttw"} {
		if got := d.recoveryPendingFor(gid); got == nil {
			t.Errorf("recoveryPendingFor(%q) = nil; expected the shared pending record", gid)
		}
	}

	if _, err := d.MountVFS("falloutnv", "Default"); err == nil {
		t.Errorf("MountVFS(falloutnv) succeeded while recovery pending; should refuse")
	}
	if _, err := d.MountVFS("ttw", "Default"); err == nil {
		t.Errorf("MountVFS(ttw) succeeded while recovery pending; should refuse")
	}

	if err := d.RestoreFromBackup("ttw"); err != nil {
		t.Fatalf("RestoreFromBackup(ttw): %v", err)
	}
	for _, gid := range []string{"falloutnv", "ttw"} {
		if got := d.recoveryPendingFor(gid); got != nil {
			t.Errorf("recoveryPendingFor(%q) still non-nil after restore; want cleared", gid)
		}
	}

	if _, err := os.Stat(filepath.Join(dataPath, "rogue.txt")); !os.IsNotExist(err) {
		t.Errorf("rogue.txt still present at %s — restore did not wipe Data/", dataPath)
	}
	if _, err := os.Stat(filepath.Join(dataPath, "FalloutNV.esm")); err != nil {
		t.Errorf("FalloutNV.esm missing after restore: %v", err)
	}
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Errorf("Data.orig/ still present after restore (should have been renamed)")
	}
}

// TestRecovery_PathKeyedSingleGameUnaffected verifies that a non-shared
func TestRecovery_PathKeyedSingleGameUnaffected(t *testing.T) {
	dir := t.TempDir()
	skyrimInstall := filepath.Join(dir, "skyrim-install")
	dataPath := filepath.Join(skyrimInstall, "Data")
	backupPath := dataPath + ".orig"
	if err := os.MkdirAll(dataPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataPath, "rogue.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(backupPath, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultConfig()
	cfg.Games["skyrim"] = config.GameConfig{
		Name: "Skyrim", InstallPath: skyrimInstall, DataSubpath: "Data", SteamAppID: 72850,
	}
	d, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer d.Shutdown()

	d.RecoverAll()

	d.pendingRecoveriesMu.Lock()
	resolved, _ := filepath.Abs(dataPath)
	siblings := d.gamesAtPath[resolved]
	d.pendingRecoveriesMu.Unlock()

	if len(siblings) != 1 || siblings[0] != "skyrim" {
		t.Errorf("siblings = %v, want [skyrim]", siblings)
	}
}

// TestRecovery_NoAmbiguityNoPending — if the on-disk state is clean,
// no pendingRecoveries entry is created. Catches the inverse failure
func TestRecovery_NoAmbiguityNoPending(t *testing.T) {
	dir := t.TempDir()
	install := filepath.Join(dir, "install")
	if err := os.MkdirAll(filepath.Join(install, "Data"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultConfig()
	cfg.Games["falloutnv"] = config.GameConfig{
		Name: "FNV", InstallPath: install, DataSubpath: "Data", SteamAppID: 22380,
	}
	cfg.Games["ttw"] = config.GameConfig{
		Name: "TTW", InstallPath: install, DataSubpath: "Data", LinkedFromGameID: "falloutnv",
	}

	d, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Shutdown()

	d.RecoverAll()

	d.pendingRecoveriesMu.Lock()
	defer d.pendingRecoveriesMu.Unlock()
	if len(d.pendingRecoveries) != 0 {
		t.Errorf("expected no pending recoveries on clean state; got %d", len(d.pendingRecoveries))
	}
}

// TestRecovery_SyntheticVFSMutexSurfacesError — mounting one game in a
// mutex group while the other is already mounted must surface
func TestRecovery_SyntheticVFSMutexSurfacesError(t *testing.T) {
	dir := t.TempDir()
	install := filepath.Join(dir, "install")
	dataPath := filepath.Join(install, "Data")
	if err := os.MkdirAll(dataPath, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultConfig()
	cfg.Games["falloutnv"] = config.GameConfig{
		Name: "FNV", InstallPath: install, DataSubpath: "Data", SteamAppID: 22380,
	}
	cfg.Games["ttw"] = config.GameConfig{
		Name: "TTW", InstallPath: install, DataSubpath: "Data", LinkedFromGameID: "falloutnv",
	}
	d, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Shutdown()

	d.mu.Lock()
	d.mountMgrs["falloutnv"] = mockMountedManager(dataPath)
	d.mountStates["falloutnv"] = mountState{profileName: "Default"}
	d.mu.Unlock()

	// The recovery gate (H-8) blocks mount RPCs until recovery has run; this
	// test never calls Run(), so signal it directly to exercise the mutex path.
	d.RecoverAll()

	if d.findMutexConflict("ttw") != "falloutnv" {
		t.Fatalf("findMutexConflict(ttw) = %q; want falloutnv",
			d.findMutexConflict("ttw"))
	}
	_, err = d.MountVFS("ttw", "Default")
	if err == nil {
		t.Fatal("MountVFS(ttw) succeeded while FNV mounted; want VFSMutexError")
	}
	mutex, ok := err.(*ipc.VFSMutexError)
	if !ok {
		t.Fatalf("err type = %T (%v); want *ipc.VFSMutexError", err, err)
	}
	if mutex.GameID != "ttw" || mutex.Conflicting != "falloutnv" || mutex.Group != "fnv-data" {
		t.Errorf("VFSMutexError = %+v; want game=ttw conflicting=falloutnv group=fnv-data", mutex)
	}
}

// mockMountedManager returns a vfs.MountManager whose IsMounted reports
// true without performing real filesystem work. Used by the mutex tests
func mockMountedManager(dataPath string) *vfs.MountManager {
	mm := vfs.NewMountManager(dataPath, "", "testgame")
	mm.SetMountedForTesting(true)
	return mm
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func mapKeys(m map[string]*ipc.RecoveryPendingResult) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

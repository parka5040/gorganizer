package vfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func rootLayer(t *testing.T, base, name string, files map[string]string) Layer {
	t.Helper()
	root := filepath.Join(base, name)
	for rel, body := range files {
		path := filepath.Join(root, RootContentDirName, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return Layer{Name: name, RootPath: root, Enabled: true}
}

func newRootManager(t *testing.T, gameRoot string, protected ...string) *RootDeploymentManager {
	t.Helper()
	m, err := NewRootDeploymentManager(RootDeploymentConfig{
		GameRoot: gameRoot, GameID: "testgame", ProtectedPaths: protected,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestWalkLayersExcludesRootDeploymentContent(t *testing.T) {
	dir := t.TempDir()
	layer := rootLayer(t, dir, "mod", map[string]string{"SkyrimSE.exe": "modded"})
	if err := os.WriteFile(filepath.Join(layer.RootPath, "ordinary.esp"), []byte("plugin"), 0644); err != nil {
		t.Fatal(err)
	}
	var visited []string
	if err := WalkLayers([]Layer{layer}, func(vpath, _ string, _ int, _ Layer, _ bool) error {
		visited = append(visited, vpath)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(visited) != 1 || visited[0] != "ordinary.esp" {
		t.Fatalf("Data traversal visited %v, want only ordinary.esp", visited)
	}
	plan, err := BuildRootDeploymentPlan([]Layer{layer})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Entries) != 1 || plan.Entries[0].RelativePath != "SkyrimSE.exe" {
		t.Fatalf("root plan = %+v", plan.Entries)
	}
}

func TestRootContentDirectoryIsCaseInsensitiveButUnambiguous(t *testing.T) {
	dir := t.TempDir()
	layer := rootLayer(t, dir, "mod", map[string]string{"Hook.dll": "mod"})
	lower := filepath.Join(layer.RootPath, RootContentDirName)
	upper := filepath.Join(layer.RootPath, ".GORGANIZER-ROOT")
	if err := os.Rename(lower, upper); err != nil {
		t.Fatal(err)
	}
	if err := WalkLayers([]Layer{layer}, func(vpath, _ string, _ int, _ Layer, _ bool) error {
		t.Fatalf("reserved root content leaked into Data traversal as %q", vpath)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	plan, err := BuildRootDeploymentPlan([]Layer{layer})
	if err != nil || len(plan.Entries) != 1 {
		t.Fatalf("case-insensitive root plan = %+v err=%v", plan, err)
	}
	if err := os.MkdirAll(lower, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildRootDeploymentPlan([]Layer{layer}); !errors.Is(err, ErrRootCasefoldCollision) {
		t.Fatalf("ambiguous reserved directories = %v, want ErrRootCasefoldCollision", err)
	}
}

func TestBuildRootDeploymentPlanPriorityAndCollisions(t *testing.T) {
	dir := t.TempDir()
	low := rootLayer(t, dir, "low", map[string]string{"Bin/Hook.dll": "low"})
	high := rootLayer(t, dir, "high", map[string]string{"bin/hook.DLL": "high"})
	plan, err := BuildRootDeploymentPlan([]Layer{low, high})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Entries) != 1 || plan.Entries[0].LayerName != "high" || plan.Entries[0].RelativePath != "bin/hook.DLL" {
		t.Fatalf("priority plan = %+v", plan.Entries)
	}

	collision := rootLayer(t, dir, "collision", map[string]string{"Foo.dll": "a", "foo.DLL": "b"})
	if _, err := BuildRootDeploymentPlan([]Layer{collision}); !errors.Is(err, ErrRootCasefoldCollision) {
		t.Fatalf("same-layer case collision = %v, want ErrRootCasefoldCollision", err)
	}

	conflictFile := rootLayer(t, dir, "conflict-file", map[string]string{"thing": "file"})
	conflictChild := rootLayer(t, dir, "conflict-child", map[string]string{"thing/child": "child"})
	if _, err := BuildRootDeploymentPlan([]Layer{conflictFile, conflictChild}); !errors.Is(err, ErrRootPathConflict) {
		t.Fatalf("file/ancestor conflict = %v, want ErrRootPathConflict", err)
	}
}

func TestBuildRootDeploymentPlanRejectsEscapingAndDanglingSymlinks(t *testing.T) {
	dir := t.TempDir()
	layer := rootLayer(t, dir, "mod", nil)
	content := filepath.Join(layer.RootPath, RootContentDirName)
	if err := os.MkdirAll(content, 0755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "outside.dll")
	if err := os.WriteFile(outside, []byte("outside"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(content, "escape.dll")); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildRootDeploymentPlan([]Layer{layer}); err == nil {
		t.Fatal("escaping source symlink should be rejected")
	}
	if err := os.Remove(filepath.Join(content, "escape.dll")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "missing.dll"), filepath.Join(content, "dangling.dll")); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildRootDeploymentPlan([]Layer{layer}); err == nil {
		t.Fatal("dangling source symlink should be rejected")
	}
}

func TestRootDeploymentApplyReapplyDeactivateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	gameRoot := filepath.Join(dir, "game")
	if err := os.MkdirAll(filepath.Join(gameRoot, "Bin"), 0755); err != nil {
		t.Fatal(err)
	}
	basePath := filepath.Join(gameRoot, "Bin", "Hook.dll")
	if err := os.WriteFile(basePath, []byte("base"), 0751); err != nil {
		t.Fatal(err)
	}
	low := rootLayer(t, dir, "low", map[string]string{"bin/hook.DLL": "low", "New/Nested.dll": "new"})
	high := rootLayer(t, dir, "high", map[string]string{"BIN/HOOK.dll": "high"})
	m := newRootManager(t, gameRoot, "Data")

	stats, err := m.Apply([]Layer{low}, "Low Profile")
	if err != nil {
		t.Fatal(err)
	}
	if stats.BackupsCreated != 1 || stats.LinksCreated != 2 {
		t.Fatalf("initial stats = %+v", stats)
	}
	target, err := os.Readlink(basePath)
	if err != nil {
		t.Fatalf("base collision is not a symlink: %v", err)
	}
	if !filepath.IsAbs(target) || target != filepath.Join(low.RootPath, RootContentDirName, "bin", "hook.DLL") {
		t.Fatalf("link target = %q, want absolute low source", target)
	}
	if got, _ := os.ReadFile(basePath); string(got) != "low" {
		t.Fatalf("deployed content = %q", got)
	}

	if _, err := m.Apply([]Layer{low, high}, "High Profile"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(basePath); string(got) != "high" {
		t.Fatalf("reapplied content = %q", got)
	}
	manifest, err := m.ActiveManifest()
	if err != nil || manifest == nil || manifest.ProfileName != "High Profile" {
		t.Fatalf("active manifest = %+v err=%v", manifest, err)
	}

	stats, err = m.Deactivate()
	if err != nil {
		t.Fatal(err)
	}
	if stats.BackupsRestored != 1 {
		t.Fatalf("deactivate stats = %+v", stats)
	}
	if got, _ := os.ReadFile(basePath); string(got) != "base" {
		t.Fatalf("restored base content = %q", got)
	}
	info, err := os.Stat(basePath)
	if err != nil || info.Mode().Perm() != 0751 {
		t.Fatalf("restored base mode = %v err=%v", info.Mode(), err)
	}
	if _, err := os.Lstat(filepath.Join(gameRoot, "New", "Nested.dll")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new managed path survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(gameRoot, "New")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("manager-created directory survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(gameRoot, RootManifestFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("manifest survived deactivation: %v", err)
	}
}

func TestRootDeploymentRejectsProtectedDataAndExistingCaseAmbiguity(t *testing.T) {
	dir := t.TempDir()
	gameRoot := filepath.Join(dir, "game")
	if err := os.MkdirAll(gameRoot, 0755); err != nil {
		t.Fatal(err)
	}
	m := newRootManager(t, gameRoot, "OblivionRemastered/Content/Dev/ObvData/Data")
	protected := rootLayer(t, dir, "protected", map[string]string{
		"oblivionremastered/content/dev/obvdata/DATA/mod.esp": "bad",
	})
	if _, err := m.Apply([]Layer{protected}, "profile"); !errors.Is(err, ErrRootPathConflict) {
		t.Fatalf("protected Data apply = %v", err)
	}
	validSibling := rootLayer(t, dir, "valid-sibling", map[string]string{
		"OblivionRemastered/Content/Paks/~mods/example.pak": "pak",
	})
	if _, err := m.Apply([]Layer{validSibling}, "profile"); err != nil {
		t.Fatalf("root target sharing only a Data ancestor should be allowed: %v", err)
	}
	if _, err := m.Deactivate(); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(gameRoot, "Engine"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(gameRoot, "ENGINE"), 0755); err != nil {
		t.Fatal(err)
	}
	ambiguous := rootLayer(t, dir, "ambiguous", map[string]string{"engine/hook.dll": "x"})
	if _, err := m.Apply([]Layer{ambiguous}, "profile"); !errors.Is(err, ErrRootCasefoldCollision) {
		t.Fatalf("existing case ambiguity = %v, want ErrRootCasefoldCollision", err)
	}
}

func TestRootDeploymentDriftIsRecoveryPendingAndUntouched(t *testing.T) {
	dir := t.TempDir()
	gameRoot := filepath.Join(dir, "game")
	if err := os.MkdirAll(gameRoot, 0755); err != nil {
		t.Fatal(err)
	}
	layer := rootLayer(t, dir, "mod", map[string]string{"Hook.dll": "mod"})
	m := newRootManager(t, gameRoot)
	if _, err := m.Apply([]Layer{layer}, "profile"); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(gameRoot, "Hook.dll")
	if err := os.Remove(dest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("user replacement"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Deactivate(); !errors.Is(err, ErrRootRecoveryPending) {
		t.Fatalf("Deactivate drift = %v, want recovery pending", err)
	}
	outcome, err := m.Recover()
	if err != nil || outcome.Pending == nil {
		t.Fatalf("Recover = %+v err=%v, want Pending", outcome, err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "user replacement" {
		t.Fatalf("drifted destination was changed: %q", got)
	}
}

func TestRootDeploymentRecoversInterruptedBackupThenLink(t *testing.T) {
	dir := t.TempDir()
	gameRoot := filepath.Join(dir, "game")
	if err := os.MkdirAll(gameRoot, 0755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(gameRoot, "Hook.dll")
	if err := os.WriteFile(dest, []byte("base"), 0644); err != nil {
		t.Fatal(err)
	}
	layer := rootLayer(t, dir, "mod", map[string]string{"Hook.dll": "mod"})
	m := newRootManager(t, gameRoot)
	plan, err := BuildRootDeploymentPlan([]Layer{layer})
	if err != nil {
		t.Fatal(err)
	}
	desired, err := m.prepareManifest(plan, nil, "profile")
	if err != nil {
		t.Fatal(err)
	}
	intent := &rootIntent{SchemaVersion: rootCurrentSchema, Magic: rootIntentMagic, GameID: "testgame", GameRoot: m.gameRoot, PID: 1, Desired: desired}
	if err := m.writeIntent(intent); err != nil {
		t.Fatal(err)
	}
	backup := m.backupPath("Hook.dll")
	if err := os.MkdirAll(filepath.Dir(backup), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dest, backup); err != nil {
		t.Fatal(err)
	}

	outcome, err := m.Recover()
	if err != nil || !outcome.Recovered || outcome.Pending != nil {
		t.Fatalf("Recover = %+v err=%v", outcome, err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "mod" {
		t.Fatalf("recovered deployment content = %q", got)
	}
	if _, err := os.Stat(m.intentPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("intent survived recovery: %v", err)
	}
	if _, err := m.Deactivate(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "base" {
		t.Fatalf("base was not restorable after recovery: %q", got)
	}
}

func TestRootDeploymentRecoversInterruptedRemoval(t *testing.T) {
	dir := t.TempDir()
	gameRoot := filepath.Join(dir, "game")
	if err := os.MkdirAll(gameRoot, 0755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(gameRoot, "Hook.dll")
	if err := os.WriteFile(dest, []byte("base"), 0644); err != nil {
		t.Fatal(err)
	}
	layer := rootLayer(t, dir, "mod", map[string]string{"Hook.dll": "mod"})
	m := newRootManager(t, gameRoot)
	if _, err := m.Apply([]Layer{layer}, "profile"); err != nil {
		t.Fatal(err)
	}
	previous, err := m.readManifest()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.writeIntent(&rootIntent{
		SchemaVersion: rootCurrentSchema, Magic: rootIntentMagic,
		GameID: "testgame", GameRoot: m.gameRoot, PID: 1, Previous: previous,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dest); err != nil {
		t.Fatal(err)
	}
	outcome, err := m.Recover()
	if err != nil || !outcome.Recovered || outcome.Pending != nil {
		t.Fatalf("Recover = %+v err=%v", outcome, err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "base" {
		t.Fatalf("interrupted removal did not restore base: %q", got)
	}
	if manifest, err := m.ActiveManifest(); err != nil || manifest != nil {
		t.Fatalf("manifest after recovered deactivate = %+v err=%v", manifest, err)
	}
}

func TestRootDeploymentBackupTamperingBecomesPending(t *testing.T) {
	dir := t.TempDir()
	gameRoot := filepath.Join(dir, "game")
	if err := os.MkdirAll(gameRoot, 0755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(gameRoot, "Hook.dll")
	if err := os.WriteFile(dest, []byte("base"), 0644); err != nil {
		t.Fatal(err)
	}
	layer := rootLayer(t, dir, "mod", map[string]string{"Hook.dll": "mod"})
	m := newRootManager(t, gameRoot)
	if _, err := m.Apply([]Layer{layer}, "profile"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.backupPath("Hook.dll"), []byte("tampered"), 0644); err != nil {
		t.Fatal(err)
	}
	outcome, err := m.Recover()
	if err != nil || outcome.Pending == nil {
		t.Fatalf("Recover = %+v err=%v, want pending", outcome, err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "mod" {
		t.Fatalf("active link was changed during pending recovery: %q", got)
	}
}

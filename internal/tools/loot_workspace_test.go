package tools

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestBuildLOOTWorkspaceCopiesPluginsAndLinksAssets(t *testing.T) {
	root := t.TempDir()
	library := filepath.Join(root, "library")
	install := filepath.Join(library, "steamapps", "common", "Skyrim Special Edition")
	data := filepath.Join(install, "Data")
	if err := os.MkdirAll(data, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(install, "SkyrimSE.exe"), []byte("marker"), 0755); err != nil {
		t.Fatal(err)
	}
	plugin := filepath.Join(data, "Example.esp")
	asset := filepath.Join(data, "Example.bsa")
	pluginsState := filepath.Join(data, "Plugins.txt")
	loadOrderState := filepath.Join(data, "loadorder.txt")
	if err := os.WriteFile(plugin, []byte("plugin"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(asset, []byte("archive"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pluginsState, []byte("*Example.esp\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(loadOrderState, []byte("Example.esp\n"), 0644); err != nil {
		t.Fatal(err)
	}
	originalTime := time.Unix(12345, 0)
	if err := os.Chtimes(plugin, originalTime, originalTime); err != nil {
		t.Fatal(err)
	}
	workspace, err := BuildLOOTWorkspace(LOOTWorkspaceOptions{
		SteamLibrary: library, AppID: 489830, RunID: "test", GameID: "skyrimse",
		InstallPath: install, DataPath: data, DataSubpath: "Data",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Remove()

	workspacePlugin := filepath.Join(workspace.DataPath, "Example.esp")
	workspaceAsset := filepath.Join(workspace.DataPath, "Example.bsa")
	if sameInode(t, plugin, workspacePlugin) {
		t.Fatal("plugin was hardlinked into LOOT workspace")
	}
	if !sameInode(t, asset, workspaceAsset) {
		t.Fatal("same-filesystem immutable asset was not hardlinked")
	}
	for _, stateFile := range []string{"Plugins.txt", "loadorder.txt"} {
		if sameInode(t, filepath.Join(data, stateFile), filepath.Join(workspace.DataPath, stateFile)) {
			t.Fatalf("%s was hardlinked into LOOT workspace", stateFile)
		}
	}
	if err := os.Chtimes(workspacePlugin, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(plugin)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(originalTime) {
		t.Fatalf("workspace mutation changed source plugin mtime: %v", info.ModTime())
	}
}

func TestSweepLOOTWorkspaces(t *testing.T) {
	library := t.TempDir()
	stale := filepath.Join(library, "steamapps", "common", ".gorganizer-workspaces", "489830", "stale", "game")
	if err := os.MkdirAll(stale, 0755); err != nil {
		t.Fatal(err)
	}
	if err := SweepLOOTWorkspaces(library, 489830); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(filepath.Dir(stale))); !os.IsNotExist(err) {
		t.Fatalf("workspace root still exists after sweep: %v", err)
	}
}

func TestBuildLOOTWorkspaceSupportsOblivionRemasteredNestedData(t *testing.T) {
	root := t.TempDir()
	library := filepath.Join(root, "library")
	install := filepath.Join(library, "steamapps", "common", "Oblivion Remastered")
	dataSubpath := filepath.Join("OblivionRemastered", "Content", "Dev", "ObvData", "Data")
	data := filepath.Join(install, dataSubpath)
	if err := os.MkdirAll(data, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(install, "OblivionRemastered.exe"), []byte("marker"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "Oblivion.esm"), []byte("plugin"), 0644); err != nil {
		t.Fatal(err)
	}
	workspace, err := BuildLOOTWorkspace(LOOTWorkspaceOptions{
		SteamLibrary: library, AppID: 2623190, RunID: "nested", GameID: "oblivionremastered",
		InstallPath: install, DataPath: data, DataSubpath: dataSubpath,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Remove()
	if _, err := os.Stat(filepath.Join(workspace.DataPath, "Oblivion.esm")); err != nil {
		t.Fatal(err)
	}
}

func sameInode(t *testing.T, left, right string) bool {
	t.Helper()
	leftInfo, err := os.Stat(left)
	if err != nil {
		t.Fatal(err)
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		t.Fatal(err)
	}
	leftStat, leftOK := leftInfo.Sys().(*syscall.Stat_t)
	rightStat, rightOK := rightInfo.Sys().(*syscall.Stat_t)
	return leftOK && rightOK && leftStat.Dev == rightStat.Dev && leftStat.Ino == rightStat.Ino
}

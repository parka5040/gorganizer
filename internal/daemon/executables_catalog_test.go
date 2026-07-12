package daemon

import (
	"path/filepath"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/dto"
)

func TestUpsertCatalogExecutableUsesGameRootWorkingDirectory(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	gameRoot := filepath.Join(t.TempDir(), "Skyrim Special Edition")
	cfg := config.DefaultConfig()
	cfg.Games["skyrimse"] = config.GameConfig{
		Name: "Skyrim Special Edition", InstallPath: gameRoot, DataSubpath: "Data", SteamAppID: 489830,
	}
	s := &session{config: cfg}
	service := &ExecutableService{s: s}
	saved, err := service.UpsertExecutable("skyrimse", dto.ExecutableSpec{
		Title: "Skyrim Launcher", ToolID: "skyrim-launcher",
		ExePath: filepath.Join(gameRoot, "SkyrimSELauncher.exe"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.WorkingDir != gameRoot {
		t.Fatalf("working dir = %q, want game root %q", saved.WorkingDir, gameRoot)
	}
}

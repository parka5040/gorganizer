package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("default LogLevel = %q, want \"info\"", cfg.LogLevel)
	}
	if len(cfg.Games) != 0 {
		t.Errorf("default Games should be empty, got %d", len(cfg.Games))
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfg := &Config{
		Games: map[string]GameConfig{
			"skyrimse": {
				Name:        "Skyrim SE",
				InstallPath: "/games/skyrim",
				DataSubpath: "Data",
				SteamAppID:  489830,
				Tool:        "skse64",
			},
		},
		LogLevel:    "debug",
		NexusAPIKey: "test-key-123",
	}

	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	configPath := filepath.Join(dir, "gorganizer", "config.json")
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("permissions = %o, want 0600", info.Mode().Perm())
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want \"debug\"", loaded.LogLevel)
	}
	if loaded.NexusAPIKey != "test-key-123" {
		t.Errorf("NexusAPIKey = %q, want \"test-key-123\"", loaded.NexusAPIKey)
	}
	gc, ok := loaded.Games["skyrimse"]
	if !ok {
		t.Fatal("skyrimse game config not found")
	}
	if gc.Tool != "skse64" {
		t.Errorf("Tool = %q, want \"skse64\"", gc.Tool)
	}
}

func TestGameDataPath(t *testing.T) {
	cfg := &Config{
		Games: map[string]GameConfig{
			"skyrimse": {InstallPath: "/games/skyrim", DataSubpath: "Data"},
		},
	}

	path, err := cfg.GameDataPath("skyrimse")
	if err != nil {
		t.Fatalf("GameDataPath: %v", err)
	}
	if path != "/games/skyrim/Data" {
		t.Errorf("path = %q, want \"/games/skyrim/Data\"", path)
	}

	_, err = cfg.GameDataPath("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent game")
	}
}

func TestXDGPaths(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/custom/data")
	t.Setenv("XDG_CONFIG_HOME", "/custom/config")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	if got := DataDir(); got != "/custom/data/gorganizer" {
		t.Errorf("DataDir = %q", got)
	}
	if got := ConfigDir(); got != "/custom/config/gorganizer" {
		t.Errorf("ConfigDir = %q", got)
	}
	if got := RuntimeDir(); got != "/run/user/1000/gorganizer" {
		t.Errorf("RuntimeDir = %q", got)
	}
	if got := SocketPath(); got != "/run/user/1000/gorganizer/gorganizer.sock" {
		t.Errorf("SocketPath = %q", got)
	}
	if got := ModsDir("skyrimse"); got != "/custom/data/gorganizer/skyrimse/mods" {
		t.Errorf("ModsDir = %q", got)
	}
	if got := ProfilesDir("skyrimse"); got != "/custom/data/gorganizer/skyrimse/profiles" {
		t.Errorf("ProfilesDir = %q", got)
	}
}

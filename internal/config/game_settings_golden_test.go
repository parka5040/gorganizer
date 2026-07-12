package config

import (
	"os"
	"path/filepath"
	"testing"
)

// setGameRoot points GORGANIZER_ROOT at a temp dir and returns the mods dir for gameID.
func setGameRoot(t *testing.T, gameID string) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("GORGANIZER_ROOT", root)
	return filepath.Join(root, gameID+"_Mods")
}

func TestSaveGameSettingsGoldenBytes(t *testing.T) {
	tests := []struct {
		name     string
		settings GameSettings
		want     string
	}{
		{
			name:     "auto install false",
			settings: GameSettings{AutoInstall: false},
			want:     "# Gorganizer per-game settings — auto-generated\nauto_install: false\n",
		},
		{
			name:     "auto install true",
			settings: GameSettings{AutoInstall: true},
			want:     "# Gorganizer per-game settings — auto-generated\nauto_install: true\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modsDir := setGameRoot(t, "testgame")
			if err := SaveGameSettings("testgame", tt.settings); err != nil {
				t.Fatalf("SaveGameSettings: %v", err)
			}
			got, err := os.ReadFile(filepath.Join(modsDir, ".gorganizer-game.yaml"))
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("bytes = %q, want %q", got, tt.want)
			}
			loaded, err := LoadGameSettings("testgame")
			if err != nil {
				t.Fatalf("LoadGameSettings: %v", err)
			}
			if loaded != tt.settings {
				t.Errorf("round-trip = %+v, want %+v", loaded, tt.settings)
			}
		})
	}
}

func TestLoadGameSettingsParsing(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    GameSettings
	}{
		{name: "line without colon skipped", content: "auto_install true\n", want: GameSettings{AutoInstall: false}},
		{name: "no space after colon", content: "auto_install:true\n", want: GameSettings{AutoInstall: true}},
		{name: "padded key and value", content: "  auto_install :   true  \n", want: GameSettings{AutoInstall: true}},
		{name: "non true value is false", content: "auto_install: yes\n", want: GameSettings{AutoInstall: false}},
		{name: "comments and blanks ignored", content: "# note\n\nauto_install: true\n", want: GameSettings{AutoInstall: true}},
		{name: "unknown keys ignored", content: "mystery: true\nauto_install: false\n", want: GameSettings{AutoInstall: false}},
		{name: "last occurrence wins", content: "auto_install: true\nauto_install: false\n", want: GameSettings{AutoInstall: false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			modsDir := setGameRoot(t, "testgame")
			if err := os.MkdirAll(modsDir, 0755); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			path := filepath.Join(modsDir, ".gorganizer-game.yaml")
			if err := os.WriteFile(path, []byte(tt.content), 0644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			got, err := LoadGameSettings("testgame")
			if err != nil {
				t.Fatalf("LoadGameSettings: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestLoadGameSettingsMissingFileReturnsDefaults(t *testing.T) {
	setGameRoot(t, "testgame")
	got, err := LoadGameSettings("testgame")
	if err != nil {
		t.Fatalf("LoadGameSettings: %v", err)
	}
	if got != DefaultGameSettings() {
		t.Errorf("got %+v, want defaults %+v", got, DefaultGameSettings())
	}
}

func TestGameSettingsByteStability(t *testing.T) {
	fixture := "# Gorganizer per-game settings — auto-generated\nauto_install: true\n"
	modsDir := setGameRoot(t, "testgame")
	if err := os.MkdirAll(modsDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(modsDir, ".gorganizer-game.yaml")
	if err := os.WriteFile(path, []byte(fixture), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	loaded, err := LoadGameSettings("testgame")
	if err != nil {
		t.Fatalf("LoadGameSettings: %v", err)
	}
	if err := SaveGameSettings("testgame", loaded); err != nil {
		t.Fatalf("SaveGameSettings: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != fixture {
		t.Errorf("Save(Load(fixture)) = %q, want %q", got, fixture)
	}
}

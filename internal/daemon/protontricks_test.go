package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/protontricks"
)

// TestExecutableRuntimeMissingProtontricks includes the tool title, packages, and installation guidance.
func TestExecutableRuntimeMissingProtontricks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newIsolatedDaemon(t, nil)
	steamRoot := filepath.Join(os.Getenv("XDG_DATA_HOME"), "Steam")
	game := config.GameConfig{SteamAppID: 489830, InstallPath: filepath.Join(steamRoot, "steamapps", "common", "Skyrim Special Edition")}
	if err := os.MkdirAll(filepath.Join(steamRoot, "steamapps", "compatdata", "489830", "pfx"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(game.InstallPath, 0o755); err != nil {
		t.Fatal(err)
	}
	d.session.resolveProtontricks = func(ctx context.Context, _ protontricks.Options) (protontricks.Invocation, error) {
		return protontricks.Resolve(ctx, protontricks.Options{LookPath: func(string) (string, error) { return "", os.ErrNotExist }})
	}
	executable := config.Executable{ToolID: "loot", ExePath: filepath.Join(config.ToolsDir(), "loot", "test", "LOOT.exe")}
	err := d.ensureExecutableRuntime("skyrimse", game, executable)
	var notInstalled *protontricks.NotInstalledError
	if !errors.As(err, &notInstalled) {
		t.Fatalf("ensureExecutableRuntime error = %v, want NotInstalledError", err)
	}
	for _, part := range []string{"LOOT", "vcrun2022", notInstalled.Error()} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q is missing %q", err, part)
		}
	}
}

// TestTTWFlatpakWithoutAppIsUnavailable checks that a Flatpak executable alone does not satisfy TTW prerequisites.
func TestTTWFlatpakWithoutAppIsUnavailable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := newIsolatedDaemon(t, nil)
	infoCalls := 0
	d.session.resolveProtontricks = func(ctx context.Context, _ protontricks.Options) (protontricks.Invocation, error) {
		return protontricks.Resolve(ctx, protontricks.Options{
			LookPath: func(name string) (string, error) {
				if name == "flatpak" {
					return "flatpak-tool", nil
				}
				return "", os.ErrNotExist
			},
			RunInfo: func(context.Context, string, ...string) error {
				infoCalls++
				return errors.New("app not installed")
			},
		})
	}
	status := TTWPrereqStatus{Backend: TTWBackendWine}
	d.populateWinePrereqs(&status)
	if status.ProtontricksAvailable || infoCalls != 1 {
		t.Errorf("ProtontricksAvailable = %v, flatpak info called %d times", status.ProtontricksAvailable, infoCalls)
	}
	if missing := collectMissing(&status); !strings.Contains(strings.Join(missing, ", "), "Protontricks (from your software centre or Flathub)") {
		t.Errorf("missing = %v", missing)
	}
}

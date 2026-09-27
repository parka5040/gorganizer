package protontricks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// TestResolve selects an installed native executable or confirmed Flatpak app.
func TestResolve(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct {
		name      string
		paths     map[string]string
		infoError error
		kind      Kind
		infoCalls int
	}{
		{name: "native first", paths: map[string]string{"protontricks": "native-tool", "flatpak": "flatpak-tool"}, kind: Native},
		{name: "Flatpak app installed", paths: map[string]string{"flatpak": "flatpak-tool"}, kind: Flatpak, infoCalls: 1},
		{name: "Flatpak app missing", paths: map[string]string{"flatpak": "flatpak-tool"}, infoError: errors.New("exit status 1"), infoCalls: 1},
		{name: "neither installed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			invocation, err := Resolve(context.Background(), Options{
				LookPath: func(name string) (string, error) {
					if path := tc.paths[name]; path != "" {
						return path, nil
					}
					return "", os.ErrNotExist
				},
				RunInfo: func(ctx context.Context, path string, args ...string) error {
					calls++
					if path != "flatpak-tool" || !reflect.DeepEqual(args, []string{"info", flatpakApp}) {
						t.Errorf("info = %q %v", path, args)
					}
					if _, ok := ctx.Deadline(); !ok {
						t.Error("flatpak info has no deadline")
					}
					return tc.infoError
				},
			})
			if calls != tc.infoCalls {
				t.Errorf("flatpak info called %d times, want %d", calls, tc.infoCalls)
			}
			if tc.kind == "" {
				var notInstalled *NotInstalledError
				if !errors.As(err, &notInstalled) {
					t.Fatalf("Resolve error = %v, want NotInstalledError", err)
				}
				if got := err.Error(); got != "Protontricks is not installed. Gorganizer needs it to add Windows components (such as Visual C++ and DirectX runtimes) to the game's Proton prefix. Install Protontricks from your software centre or Flathub, then try again." {
					t.Errorf("error text = %q", got)
				}
			} else if err != nil || invocation.Kind() != tc.kind {
				t.Errorf("Resolve = (%v, %v), want %s", invocation.Kind(), err, tc.kind)
			}
		})
	}
}

// TestCommand builds exact native and Flatpak arguments without splitting paths containing spaces.
func TestCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	compat := filepath.Join(t.TempDir(), "Steam Library", "steamapps", "compatdata", "123")
	library := filepath.Join(t.TempDir(), "Steam Library")
	external := filepath.Join(t.TempDir(), "external games")
	packages := []string{"vcrun2022", "dxvk"}
	nativeTool := filepath.Join(t.TempDir(), "native-tool")
	flatpakTool := filepath.Join(t.TempDir(), "flatpak-tool")
	for _, tc := range []struct {
		name string
		kind Kind
		want []string
	}{
		{name: "native", kind: Native, want: []string{nativeTool, "--no-bwrap", "123", "-q", "vcrun2022", "dxvk"}},
		{name: "Flatpak", kind: Flatpak, want: []string{flatpakTool, "run", "--env=STEAM_COMPAT_DATA_PATH=" + compat, "--filesystem=" + library, "--filesystem=" + external, "--filesystem=" + compat, flatpakApp, "123", "-q", "vcrun2022", "dxvk"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := nativeTool
			if tc.kind == Flatpak {
				path = flatpakTool
			}
			cmd := (Invocation{kind: tc.kind, path: path}).Command(context.Background(), 123, compat, []string{library, external, library + "/", external}, packages)
			if !reflect.DeepEqual(cmd.Args, tc.want) {
				t.Errorf("argv = %#v, want %#v", cmd.Args, tc.want)
			}
			if tc.kind == Native {
				want := append(os.Environ(), "STEAM_COMPAT_DATA_PATH="+compat)
				if !reflect.DeepEqual(cmd.Env, want) {
					t.Errorf("env = %#v, want %#v", cmd.Env, want)
				}
			} else if cmd.Env != nil {
				t.Errorf("Flatpak command overrides inherited environment: %v", cmd.Env)
			}
		})
	}
}

// TestResolveInfoTimeout treats a timed-out Flatpak check as an unavailable app.
func TestResolveInfoTimeout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := Resolve(ctx, Options{
		LookPath: func(name string) (string, error) {
			if name == "flatpak" {
				return "flatpak-tool", nil
			}
			return "", os.ErrNotExist
		},
		RunInfo: func(ctx context.Context, path string, args ...string) error {
			<-ctx.Done()
			return ctx.Err()
		},
	})
	var notInstalled *NotInstalledError
	if !errors.As(err, &notInstalled) {
		t.Fatalf("timed-out Resolve = %v, want NotInstalledError", err)
	}
}

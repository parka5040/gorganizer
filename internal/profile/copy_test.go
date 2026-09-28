package profile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/atomicfile"
	"golang.org/x/sys/unix"
)

// TestCopyProfileCopiesEverything verifies complete recursive copying and fresh profile identity.
func TestCopyProfileCopiesEverything(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pm := NewManager(t.TempDir())
	original, err := pm.Create("skyrimse", "Original")
	if err != nil {
		t.Fatal(err)
	}
	original.UseCustomIni = true
	if err := pm.Save(original, nil); err != nil {
		t.Fatal(err)
	}
	from := pm.ProfileDir("skyrimse", "Original")
	files := map[string]string{
		"modlist.txt":        "+SkyUI\n-HD textures\n",
		"plugin_state.txt":   "+Skyrim.esm\n-SkyUI.esp\n",
		"plugin_order.txt":   "Skyrim.esm\nSkyUI.esp\n",
		"separators.json":    `{"view_enabled":true}`,
		"SkyrimPrefs.ini":    "[Display]\nbrightness=1\n",
		"nested/options.ini": "[General]\nvalue=ok\n",
	}
	for name, data := range files {
		path := filepath.Join(from, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	profileJSON := filepath.Join(from, "profile.json")
	data, err := os.ReadFile(profileJSON)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	raw["future_setting"] = json.RawMessage(`{"keep":true}`)
	data, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profileJSON, data, 0644); err != nil {
		t.Fatal(err)
	}

	before := time.Now()
	copied, err := pm.Copy("skyrimse", "Original", "Copy")
	if err != nil {
		t.Fatal(err)
	}
	if copied.Name != "Copy" || copied.GameID != "skyrimse" || !copied.UseCustomIni || copied.CreatedAt.Before(before) || copied.CreatedAt.Equal(original.CreatedAt) {
		t.Fatalf("copied profile = %+v", copied)
	}
	to := pm.ProfileDir("skyrimse", "Copy")
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(to, name))
		if err != nil || string(got) != want {
			t.Errorf("copied %s = %q, %v; want %q", name, got, err, want)
		}
	}
	data, err = os.ReadFile(filepath.Join(to, "profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	var future struct {
		Keep bool `json:"keep"`
	}
	if err := json.Unmarshal(raw["future_setting"], &future); err != nil {
		t.Fatal(err)
	}
	if string(raw["name"]) != `"Copy"` || string(raw["game_id"]) != `"skyrimse"` || !future.Keep {
		t.Fatalf("copied profile.json = %s", data)
	}
	loaded, _, err := pm.Load("skyrimse", "Copy")
	if err != nil || !loaded.CreatedAt.Equal(copied.CreatedAt) {
		t.Fatalf("loaded profile = %+v, %v", loaded, err)
	}
}

// TestCopyProfileRefusesExistingAndSymlinks verifies that copying never follows links or replaces a target.
func TestCopyProfileRefusesExistingAndSymlinks(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func(*testing.T, *Manager)
	}{
		{"existing", func(t *testing.T, pm *Manager) {
			if _, err := pm.Create("skyrimse", "Copy"); err != nil {
				t.Fatal(err)
			}
		}},
		{"source link", func(t *testing.T, pm *Manager) {
			if err := os.Symlink("Original", pm.ProfileDir("skyrimse", "Linked")); err != nil {
				t.Fatal(err)
			}
		}},
		{"target link", func(t *testing.T, pm *Manager) {
			if err := os.Symlink("Original", pm.ProfileDir("skyrimse", "Copy")); err != nil {
				t.Fatal(err)
			}
		}},
		{"nested link", func(t *testing.T, pm *Manager) {
			if err := os.Symlink("profile.json", filepath.Join(pm.ProfileDir("skyrimse", "Original"), "nested-link")); err != nil {
				t.Fatal(err)
			}
		}},
		{"special file", func(t *testing.T, pm *Manager) {
			if err := unix.Mkfifo(filepath.Join(pm.ProfileDir("skyrimse", "Original"), "special"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			pm := NewManager(t.TempDir())
			if _, err := pm.Create("skyrimse", "Original"); err != nil {
				t.Fatal(err)
			}
			tc.make(t, pm)
			source := "Original"
			if tc.name == "source link" {
				source = "Linked"
			}
			if _, err := pm.Copy("skyrimse", source, "Copy"); err == nil {
				t.Fatal("Copy accepted an existing target or symlink")
			}
			if tc.name != "existing" && tc.name != "target link" {
				if _, err := os.Lstat(pm.ProfileDir("skyrimse", "Copy")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("target exists after refused copy: %v", err)
				}
			}
		})
	}
	pm := NewManager(t.TempDir())
	if _, err := pm.Copy("skyrimse", "Missing", "Copy"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing source: %v", err)
	}
	for _, invalid := range []struct{ game, source, target string }{
		{"../bad", "Original", "Copy"}, {"skyrimse", "../bad", "Copy"}, {"skyrimse", "Original", "../bad"},
	} {
		if _, err := pm.Copy(invalid.game, invalid.source, invalid.target); err == nil {
			t.Fatalf("Copy accepted unsafe name: %+v", invalid)
		}
	}
}

// TestCopyProfileFailureLeavesNoTarget verifies an interrupted copy removes its stage and target.
func TestCopyProfileFailureLeavesNoTarget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	pm := NewManager(t.TempDir())
	if _, err := pm.Create("skyrimse", "Original"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	pm.CopyFileDurable = func(src, dst string, perm os.FileMode, replace bool) (atomicfile.Outcome, error) {
		calls++
		if calls == 2 {
			return atomicfile.NotPublished, errors.New("injected copy failure")
		}
		return atomicfile.CopyFileDurable(src, dst, perm, replace)
	}
	if _, err := pm.Copy("skyrimse", "Original", "Copy"); err == nil || !strings.Contains(err.Error(), "injected copy failure") {
		t.Fatalf("Copy error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("copied %d files, want one successful copy before failure", calls)
	}
	entries, err := os.ReadDir(filepath.Dir(pm.ProfileDir("skyrimse", "Original")))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "Original" {
		t.Fatalf("failed copy left directories: %v", entries)
	}
}

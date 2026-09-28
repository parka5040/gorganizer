package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUpdateMimeappsCreatesFile checks registration creates the missing settings file.
func TestUpdateMimeappsCreatesFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	path := filepath.Join(root, "config", "mimeapps.list")
	if err := UpdateMimeapps(path, true); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil || !HasAssociation(body) {
		t.Fatalf("new settings = %q, %v", body, err)
	}
}

// TestUnregisterLeavesOtherDefault checks removal never clears a different application's default.
func TestUnregisterLeavesOtherDefault(t *testing.T) {
	original := "# untouched\n[Default Applications]\n" + mimeKey + "other.desktop;\n[Added Associations]\n" + mimeKey + Handler + ";other.desktop;\n"
	got, err := EditMimeapps([]byte(original), false)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(original, Handler+";", "", 1)
	if string(got) != want {
		t.Fatalf("unregister = %q, want %q", got, want)
	}
}

// TestUnregisterLegacyBareAssociation checks older handler values without semicolons are removed.
func TestUnregisterLegacyBareAssociation(t *testing.T) {
	original := "[Default Applications]\n" + mimeKey + Handler + "\n[Added Associations]\n" + mimeKey + Handler + "\n"
	updated, err := EditMimeapps([]byte(original), false)
	if err != nil || string(updated) != "[Default Applications]\n[Added Associations]\n" {
		t.Fatalf("old association = %q, %v", updated, err)
	}
}

// TestUnregisterKeepsForeignNXM checks an unrecognized handler retains its association.
func TestUnregisterKeepsForeignNXM(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	paths, err := EnvironmentPaths()
	if err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(root, "checkout")
	if err := WriteLauncher(paths.Launcher, checkout); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.NXM), 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := "[Desktop Entry]\nName=Someone else\nExec=/other/program\n"
	if err := os.WriteFile(paths.NXM, []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.Mimeapps), 0o700); err != nil {
		t.Fatal(err)
	}
	settings := "[Default Applications]\n" + mimeKey + Handler + ";\n"
	if err := os.WriteFile(paths.Mimeapps, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Unregister(paths, checkout); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{paths.NXM: foreign, paths.Mimeapps: settings} {
		body, err := os.ReadFile(path)
		if err != nil || string(body) != want {
			t.Errorf("foreign file %s = %q, %v", path, body, err)
		}
	}
}

// TestEditMimeappsRetainsOtherLinesWithCRLF checks mixed content stays unchanged outside edited keys.
func TestEditMimeappsRetainsOtherLinesWithCRLF(t *testing.T) {
	original := "# keep\r\n[Default Applications]\r\n" + mimeKey + "other.desktop;\r\n[Added Associations]\r\n# keep too\r\n" + mimeKey + "other.desktop;\r\n"
	updated, err := EditMimeapps([]byte(original), true)
	if err != nil || !HasAssociation(updated) || !strings.Contains(string(updated), "# keep too\r\n") || !strings.Contains(string(updated), mimeKey+Handler+";other.desktop;\r\n") {
		t.Fatalf("CRLF settings = %q, %v", updated, err)
	}
}

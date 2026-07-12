package steam

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseVDF_LibraryFolders(t *testing.T) {
	input := `"libraryfolders"
{
	"0"
	{
		"path"		"/home/user/.local/share/Steam"
		"label"		""
		"contentid"		"1234567890"
		"totalsize"		"0"
		"update_clean_bytes_tally"		"0"
		"time_last_update_corruption"		"0"
		"apps"
		{
			"489830"		"12345"
			"22380"		"67890"
		}
	}
	"1"
	{
		"path"		"/mnt/games/SteamLibrary"
		"label"		""
		"apps"
		{
			"377160"		"99999"
		}
	}
}`

	result, err := ParseVDF(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseVDF: %v", err)
	}

	lf, ok := result["libraryfolders"]
	if !ok {
		t.Fatal("missing libraryfolders key")
	}

	lfMap, ok := lf.(map[string]interface{})
	if !ok {
		t.Fatal("libraryfolders is not a map")
	}

	entry0, ok := lfMap["0"].(map[string]interface{})
	if !ok {
		t.Fatal("entry 0 is not a map")
	}
	if path := entry0["path"].(string); path != "/home/user/.local/share/Steam" {
		t.Errorf("entry 0 path = %q", path)
	}

	apps0, ok := entry0["apps"].(map[string]interface{})
	if !ok {
		t.Fatal("entry 0 apps is not a map")
	}
	if v := apps0["489830"].(string); v != "12345" {
		t.Errorf("app 489830 = %q", v)
	}

	entry1, ok := lfMap["1"].(map[string]interface{})
	if !ok {
		t.Fatal("entry 1 is not a map")
	}
	if path := entry1["path"].(string); path != "/mnt/games/SteamLibrary" {
		t.Errorf("entry 1 path = %q", path)
	}
}

func TestParseVDF_AppManifest(t *testing.T) {
	input := `"AppState"
{
	"appid"		"489830"
	"Universe"		"1"
	"name"		"The Elder Scrolls V: Skyrim Special Edition"
	"StateFlags"		"4"
	"installdir"		"Skyrim Special Edition"
	"LastUpdated"		"1234567890"
	"SizeOnDisk"		"12345678901"
}`

	result, err := ParseVDF(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseVDF: %v", err)
	}

	appState, ok := result["AppState"].(map[string]interface{})
	if !ok {
		t.Fatal("AppState is not a map")
	}
	if appState["appid"] != "489830" {
		t.Errorf("appid = %v", appState["appid"])
	}
	if appState["installdir"] != "Skyrim Special Edition" {
		t.Errorf("installdir = %v", appState["installdir"])
	}
	if appState["name"] != "The Elder Scrolls V: Skyrim Special Edition" {
		t.Errorf("name = %v", appState["name"])
	}
}

func TestParseVDF_EscapedQuotes(t *testing.T) {
	input := `"root"
{
	"key"		"value with \"quotes\" inside"
	"path"		"C:\\Program Files\\Steam"
}`

	result, err := ParseVDF(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseVDF: %v", err)
	}

	root := result["root"].(map[string]interface{})
	if root["key"] != `value with "quotes" inside` {
		t.Errorf("key = %q", root["key"])
	}
	if root["path"] != `C:\Program Files\Steam` {
		t.Errorf("path = %q", root["path"])
	}
}

func TestParseVDF_Comments(t *testing.T) {
	input := `"root"
{
	// This is a comment
	"key"		"value"
	// Another comment
}`

	result, err := ParseVDF(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseVDF: %v", err)
	}

	root := result["root"].(map[string]interface{})
	if root["key"] != "value" {
		t.Errorf("key = %q", root["key"])
	}
}

func TestParseVDF_UnclosedBrace(t *testing.T) {
	input := `"root"
{
	"key"		"value"
	"sub"
	{
		"inner"		"1"`

	result, err := ParseVDF(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseVDF: %v", err)
	}

	root, ok := result["root"].(map[string]interface{})
	if !ok {
		t.Fatal("root is not a map")
	}
	if root["key"] != "value" {
		t.Errorf("key = %q", root["key"])
	}
	sub, ok := root["sub"].(map[string]interface{})
	if !ok {
		t.Fatal("sub is not a map")
	}
	if sub["inner"] != "1" {
		t.Errorf("inner = %q", sub["inner"])
	}
}

func TestParseVDF_MissingRootBrace(t *testing.T) {
	if _, err := ParseVDF(strings.NewReader(`"root" "value"`)); err == nil {
		t.Fatal("expected error for root key without '{'")
	}
	if _, err := ParseVDF(strings.NewReader(`{`)); err == nil {
		t.Fatal("expected error for missing root key")
	}
}

func TestParseVDFFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.acf")
	if err := os.WriteFile(path, []byte(`"AppState"
{
	"appid"		"22380"
}`), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := ParseVDFFromFile(path)
	if err != nil {
		t.Fatalf("ParseVDFFromFile: %v", err)
	}
	appState, ok := result["AppState"].(map[string]interface{})
	if !ok {
		t.Fatal("AppState is not a map")
	}
	if appState["appid"] != "22380" {
		t.Errorf("appid = %v", appState["appid"])
	}

	if _, err := ParseVDFFromFile(filepath.Join(dir, "missing.acf")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

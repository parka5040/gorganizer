package daemon

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/plugins"
)

func TestReadLOOTOrderUsesLoadOrderFile(t *testing.T) {
	stateDir := t.TempDir()
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "loadorder.txt"), []byte("Second.esp\nFirst.esm\nIgnored.esl\n"), 0644); err != nil {
		t.Fatal(err)
	}
	spec := plugins.Spec{LoadOrderFileName: "loadorder.txt", SupportedExts: []string{".esm", ".esp"}}
	got, err := readLOOTOrder(spec, stateDir, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Second.esp", "First.esm"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("readLOOTOrder = %v, want %v", got, want)
	}
}

func TestReadLOOTOrderUsesPrivatePluginTimestamps(t *testing.T) {
	dataDir := t.TempDir()
	first := filepath.Join(dataDir, "First.esm")
	second := filepath.Join(dataDir, "Second.esp")
	if err := os.WriteFile(first, []byte("first"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("second"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(first, time.Unix(10, 0), time.Unix(10, 0)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(second, time.Unix(20, 0), time.Unix(20, 0)); err != nil {
		t.Fatal(err)
	}
	got, err := readLOOTOrder(plugins.Spec{}, t.TempDir(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"First.esm", "Second.esp"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("readLOOTOrder = %v, want %v", got, want)
	}
}

func TestReadLOOTOrderUsesPluginsFileWhenConfigured(t *testing.T) {
	stateDir := t.TempDir()
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "Plugins.txt"), []byte("*Second.esm\nFirst.esm\n*Third.esp\n"), 0644); err != nil {
		t.Fatal(err)
	}
	spec := plugins.Spec{PluginsFileName: "Plugins.txt", StarPrefix: true, OrderFromPlugins: true}
	got, err := readLOOTOrder(spec, stateDir, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Second.esm", "First.esm", "Third.esp"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("readLOOTOrder = %v, want %v", got, want)
	}
}

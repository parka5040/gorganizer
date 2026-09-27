package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/atomicfile"
	"github.com/parka/gorganizer/internal/config"
)

// TestImportRecoveryPrecedesSweep verifies daemon construction restores moved-aside mods and profiles before orphan staging is swept.
func TestImportRecoveryPrecedesSweep(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	isolateDaemonState(t)
	cfg := config.DefaultConfig()
	cfg.Games["skyrimse"] = newSkyrimGames(t)["skyrimse"]
	id := strings.Repeat("a", 32)
	for _, tc := range []struct {
		root string
		kind string
		name string
	}{
		{config.ModsDir("skyrimse"), "mod", "Existing"},
		{config.ProfilesDir("skyrimse"), "profile", "Default"},
	} {
		old := filepath.Join(tc.root, ".transfer-old-"+id)
		writeFixture(t, filepath.Join(old, "original.txt"))
		stage := filepath.Join(tc.root, ".gorganizer-import-orphan")
		agedDir(t, stage, time.Hour)
		journal := filepath.Join(tc.root, ".gorganizer-transfer-intent-"+id+".json")
		data := `{"schema_version":1,"op_id":"` + id + `","kind":"` + tc.kind + `","name":"` + tc.name + `","staged":".transfer-stage-` + id + `","old":".transfer-old-` + id + `"}`
		if _, err := atomicfile.WriteFileDurable(journal, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	d, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Shutdown)
	d.RecoverAll()
	for _, tc := range []struct {
		root string
		name string
	}{
		{config.ModsDir("skyrimse"), "Existing"},
		{config.ProfilesDir("skyrimse"), "Default"},
	} {
		if _, err := os.Stat(filepath.Join(tc.root, tc.name, "original.txt")); err != nil {
			t.Errorf("original %s was not restored: %v", tc.name, err)
		}
		for _, name := range []string{".transfer-old-" + id, ".gorganizer-transfer-intent-" + id + ".json", ".gorganizer-import-orphan"} {
			if _, err := os.Lstat(filepath.Join(tc.root, name)); !os.IsNotExist(err) {
				t.Errorf("%s remains after recovery and sweep: %v", name, err)
			}
		}
	}
}

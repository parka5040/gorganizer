package daemon

import (
	"reflect"
	"testing"

	"github.com/parka/gorganizer/internal/config"
)

// sampleExecutableGame returns a game configuration whose executables carry every reference-typed field.
func sampleExecutableGame() config.GameConfig {
	return config.GameConfig{
		Name:        "Sample",
		InstallPath: "/games/sample",
		Executables: []config.Executable{
			{
				ID:           "tool",
				Args:         []string{"-a", "-b"},
				Environment:  map[string]string{"K": "V"},
				ExtraRWPaths: []string{"/rw"},
			},
			{ID: "bare"},
		},
	}
}

// TestCloneGameConfigSharesNoMemory locks that a cloned configuration survives mutation of the original's executables and vice versa.
func TestCloneGameConfigSharesNoMemory(t *testing.T) {
	orig := sampleExecutableGame()
	want := sampleExecutableGame()
	clone := cloneGameConfig(orig)
	if !reflect.DeepEqual(clone, want) {
		t.Fatalf("clone = %+v, want %+v", clone, want)
	}

	orig.Executables[0].Args[0] = "changed"
	orig.Executables[0].Environment["K"] = "changed"
	orig.Executables[0].ExtraRWPaths[0] = "changed"
	orig.Executables[1] = config.Executable{ID: "replaced"}
	if !reflect.DeepEqual(clone, want) {
		t.Fatalf("clone changed with the original: %+v", clone)
	}

	clone.Executables[0].Args[1] = "clone"
	clone.Executables[0].Environment["new"] = "clone"
	if orig.Executables[0].Args[1] != "-b" {
		t.Fatalf("original args changed with the clone: %v", orig.Executables[0].Args)
	}
	if _, ok := orig.Executables[0].Environment["new"]; ok {
		t.Fatalf("original environment changed with the clone: %v", orig.Executables[0].Environment)
	}
}

// TestCloneGameConfigKeepsNilAndEmptyCollections locks that nil collections stay nil and empty ones stay empty and non-nil.
func TestCloneGameConfigKeepsNilAndEmptyCollections(t *testing.T) {
	if got := cloneGameConfig(config.GameConfig{}); got.Executables != nil {
		t.Fatalf("nil executables became %#v", got.Executables)
	}
	empty := config.GameConfig{Executables: []config.Executable{{
		Args:         []string{},
		Environment:  map[string]string{},
		ExtraRWPaths: []string{},
	}, {}}}
	got := cloneGameConfig(empty)
	if got.Executables == nil || len(got.Executables) != 2 {
		t.Fatalf("executables = %#v", got.Executables)
	}
	first, second := got.Executables[0], got.Executables[1]
	if first.Args == nil || first.Environment == nil || first.ExtraRWPaths == nil {
		t.Fatalf("empty collections became nil: %#v", first)
	}
	if second.Args != nil || second.Environment != nil || second.ExtraRWPaths != nil {
		t.Fatalf("nil collections became non-nil: %#v", second)
	}
}

// TestConfigSnapshotsAreDetachedFromTheSession locks that raw and effective snapshots never alias the session's stored executables.
func TestConfigSnapshotsAreDetachedFromTheSession(t *testing.T) {
	s := &session{config: &config.Config{Games: map[string]config.GameConfig{
		"parent": {Name: "Parent", InstallPath: "/games/parent", SteamAppID: 7},
		"child":  {Name: "Child", LinkedFromGameID: "parent", Executables: sampleExecutableGame().Executables},
	}, PreferredProton: "proton", NexusAPIKey: "key"}}

	raw, ok := s.gameConfigSnapshot("child")
	if !ok || raw.InstallPath != "" {
		t.Fatalf("raw snapshot = %+v, %v", raw, ok)
	}
	eff, err := s.effectiveGameConfigSnapshot("child")
	if err != nil || eff.InstallPath != "/games/parent" || eff.SteamAppID != 7 {
		t.Fatalf("effective snapshot = %+v, %v", eff, err)
	}
	stored := s.config.Games["child"]
	stored.Executables[0].Args[0] = "changed"
	if raw.Executables[0].Args[0] != "-a" || eff.Executables[0].Args[0] != "-a" {
		t.Fatalf("snapshots alias the stored executables: raw %v, effective %v", raw.Executables[0].Args, eff.Executables[0].Args)
	}

	if _, ok := s.gameConfigSnapshot("missing"); ok {
		t.Fatal("snapshot of a missing game reported ok")
	}
	if _, err := s.effectiveGameConfigSnapshot("missing"); err == nil {
		t.Fatal("effective snapshot of a missing game returned no error")
	}
	delete(s.config.Games, "parent")
	if _, err := s.effectiveGameConfigSnapshot("child"); err == nil {
		t.Fatal("effective snapshot with a missing parent returned no error")
	}
	if s.preferredProton() != "proton" || s.nexusAPIKey() != "key" {
		t.Fatalf("scalar snapshots = %q, %q", s.preferredProton(), s.nexusAPIKey())
	}
}

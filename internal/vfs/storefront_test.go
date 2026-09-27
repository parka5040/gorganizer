package vfs

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/steam"
)

func TestSentinelStorefrontRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	data := filepath.Join(t.TempDir(), "Data")
	backup := data + ".orig"
	for _, path := range []string{data, backup} {
		if err := os.Mkdir(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	baseline := &StorefrontSnapshot{
		Store: "steam", AppID: 489830, BuildID: "123", StateFlags: 4,
		UpdateResult: "0", LastUpdated: 100, DepotFingerprint: "digest",
		CapturedAt: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC),
	}
	for _, tc := range []struct {
		name     string
		schema   int
		baseline *StorefrontSnapshot
	}{
		{"with storefront", CurrentSentinelSchema, baseline},
		{"older without storefront", 1, nil},
		{"v2 without storefront", 2, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			layers := []SentinelLayer{{Name: "__base__", Root: backup, Enabled: true}}
			s := &Sentinel{SchemaVersion: tc.schema, Magic: SentinelMagic, GameID: "skyrimse",
				BackupPath: backup, Layers: layers, Hash: ComputeLayerHash(layers), Storefront: tc.baseline}
			if tc.schema == CurrentSentinelSchema {
				s.FarmID = "12345678-1234-1234-1234-123456789abc"
				s.Manifest = ".gorganizer-farm-" + s.FarmID + ".jsonl"
			}
			if err := WriteSentinel(data, s); err != nil {
				t.Fatal(err)
			}
			got, err := ReadSentinel(data)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateSentinel(got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Storefront, tc.baseline) {
				t.Errorf("round trip storefront = %+v, want %+v", got.Storefront, tc.baseline)
			}
			mm := NewMountManager(data, "", "skyrimse")
			live, err := mm.StorefrontBaseline()
			if err != nil || !reflect.DeepEqual(live, tc.baseline) {
				t.Errorf("StorefrontBaseline = %+v, %v; want %+v", live, err, tc.baseline)
			}
		})
	}
}

func TestReMaterializeKeepsStorefrontBaseline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	data := filepath.Join(root, "Data")
	mod := filepath.Join(root, "Mod")
	mustFile(t, filepath.Join(data, "Skyrim.esm"), "base")
	mustFile(t, filepath.Join(mod, "Mod.esp"), "mod")
	mm := NewMountManager(data, "", "skyrimse")
	baseline := &StorefrontSnapshot{Store: "steam", AppID: 489830, BuildID: "first",
		StateFlags: 4, UpdateResult: "0", LastUpdated: 123, DepotFingerprint: "original",
		CapturedAt: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)}
	mm.SetStorefrontBaseline(baseline)
	baseline.BuildID = "modified after setter"
	layers := []Layer{{Name: "__base__", RootPath: data, Enabled: true}}
	if err := mm.Activate(layers, "Default"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mm.Deactivate() })
	before, err := mm.StorefrontBaseline()
	if err != nil || before == nil || before.BuildID != "first" {
		t.Fatalf("baseline after activation = %+v, %v", before, err)
	}
	if err := mm.MarkDirty([]Layer{{Name: "__base__", RootPath: data, Enabled: true}, {Name: "Mod", RootPath: mod, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	mm.SetStorefrontBaseline(&StorefrontSnapshot{Store: "steam", AppID: 489830, BuildID: "new"})
	if err := mm.ReMaterialize(); err != nil {
		t.Fatal(err)
	}
	otherManager := NewMountManager(data, "", "skyrimse")
	after, err := otherManager.StorefrontBaseline()
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Errorf("baseline after apply/restart = %+v, %v; want %+v", after, err, before)
	}
}

func TestCompareStorefront(t *testing.T) {
	baseline := &StorefrontSnapshot{Store: "steam", AppID: 489830, BuildID: "42", LastUpdated: 123, DepotFingerprint: "hash"}
	current := steam.AppState{AppID: 489830, BuildID: "42", LastUpdated: 123, DepotFingerprint: "hash", StateFlags: 4}
	for _, tc := range []struct {
		name     string
		baseline *StorefrontSnapshot
		current  steam.AppState
		err      error
		want     StorefrontChange
	}{
		{"no baseline", nil, current, nil, StorefrontUnknown},
		{"manifest read failure", baseline, current, errors.New("manifest missing"), StorefrontUnknown},
		{"different app", baseline, steam.AppState{AppID: 22380, StateFlags: 4}, nil, StorefrontUnknown},
		{"updating", baseline, steam.AppState{AppID: 489830, StateFlags: 2}, nil, StorefrontBusy},
		{"build changed", baseline, steam.AppState{AppID: 489830, BuildID: "43", LastUpdated: 123, DepotFingerprint: "hash", StateFlags: 4}, nil, StorefrontChanged},
		{"time changed", baseline, steam.AppState{AppID: 489830, BuildID: "42", LastUpdated: 124, DepotFingerprint: "hash", StateFlags: 4}, nil, StorefrontChanged},
		{"depots changed", baseline, steam.AppState{AppID: 489830, BuildID: "42", LastUpdated: 123, DepotFingerprint: "new", StateFlags: 4}, nil, StorefrontChanged},
		{"unchanged", baseline, current, nil, StorefrontUnchanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CompareStorefront(tc.baseline, tc.current, tc.err); got != tc.want {
				t.Errorf("CompareStorefront = %q, want %q", got, tc.want)
			}
		})
	}
}

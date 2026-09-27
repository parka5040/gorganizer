package vfs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func testManifestSentinel(t *testing.T, dir, body string, count int) *Sentinel {
	t.Helper()
	id := "12345678-1234-1234-1234-123456789abc"
	name := farmManifestPrefix + id + ".jsonl"
	header := fmt.Sprintf("{\"schema_version\":1,\"farm_id\":%q,\"created_at\":\"2026-09-27T00:00:00Z\",\"entries\":%d}\n", id, count)
	contents := []byte(header + body)
	if err := os.WriteFile(filepath.Join(dir, name), contents, 0644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(contents)
	return &Sentinel{SchemaVersion: 3, FarmID: id, Manifest: name, ManifestSHA256: hex.EncodeToString(sum[:]), ManifestEntries: count}
}

// TestManifestRecordsPlacedEntries checks hardlink, cross-device fallback symlink, source symlink, and copied-file identities.
func TestManifestRecordsPlacedEntries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := fixture(t, map[string]string{"Meshes/Item.NIF": "mesh", "Readme.txt": "readme"})
	if err := os.Symlink("Readme.txt", filepath.Join(base, "Alias.txt")); err != nil {
		t.Fatal(err)
	}
	farm := filepath.Join(t.TempDir(), "Data")
	tree := NewMergedTree()
	if err := tree.Build([]Layer{{Name: "__base__", RootPath: base, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	original := devIDOf
	devIDOf = func(p string) (uint64, error) {
		if filepath.Base(p) == "Item.NIF" {
			return ^uint64(0), nil
		}
		return original(p)
	}
	t.Cleanup(func() { devIDOf = original })
	stats, err := BuildInto(farm, tree, tree.Layers(), "")
	if err != nil {
		t.Fatal(err)
	}
	if stats.FilesSymlinked != 1 || stats.FilesHardlinked != 2 || stats.ManifestEntries != 3 {
		t.Fatalf("stats = %+v, want two hardlinks and a fallback symlink", stats)
	}
	s := &Sentinel{SchemaVersion: 3, FarmID: stats.FarmID, Manifest: stats.Manifest, ManifestSHA256: stats.ManifestSHA256, ManifestEntries: stats.ManifestEntries}
	manifest, err := ReadFarmManifest(farm, s)
	if err != nil {
		t.Fatal(err)
	}
	for path, wantType := range map[string]string{"Meshes/Item.NIF": "l", "Readme.txt": "f", "Alias.txt": "l"} {
		entry, ok := manifest.Entries[path]
		if !ok || entry.Type != wantType {
			t.Errorf("entry %q = %+v, present %t; want %s", path, entry, ok, wantType)
			continue
		}
		info, err := os.Lstat(filepath.Join(farm, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		stat := info.Sys().(*syscall.Stat_t)
		if entry.Dev != uint64(stat.Dev) || entry.Ino != stat.Ino {
			t.Errorf("identity %q = %d:%d, want %d:%d", path, entry.Dev, entry.Ino, stat.Dev, stat.Ino)
		}
		if wantType == "l" {
			target, err := os.Readlink(filepath.Join(farm, filepath.FromSlash(path)))
			if err != nil || entry.LinkTarget != target {
				t.Errorf("symlink target %q = %q, %v; want %q", path, entry.LinkTarget, err, target)
			}
		}
		if manifest.Casefold[NormalizePath(path)] != path {
			t.Errorf("casefold index lost %q", path)
		}
	}
	if manifest.Entries["Meshes/Item.NIF"].LinkTarget != filepath.Join(base, "Meshes", "Item.NIF") {
		t.Error("fallback symlink did not record its absolute source")
	}

	copied := filepath.Join(farm, "Copied.txt")
	if err := os.WriteFile(copied, []byte("copy"), 0644); err != nil {
		t.Fatal(err)
	}
	writer, err := newFarmManifestWriter(farm)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.close()
	if err := writer.record(copied, "Copied.txt"); err != nil {
		t.Fatal(err)
	}
	name, digest, count, err := writer.finish(farm)
	if err != nil {
		t.Fatal(err)
	}
	copyManifest, err := ReadFarmManifest(farm, &Sentinel{SchemaVersion: 3, FarmID: writer.farmID, Manifest: name, ManifestSHA256: digest, ManifestEntries: count})
	if err != nil {
		t.Fatal(err)
	}
	entry := copyManifest.Entries["Copied.txt"]
	info, err := os.Lstat(copied)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	if entry.Type != "f" || entry.Dev != uint64(stat.Dev) || entry.Ino != stat.Ino {
		t.Errorf("copy identity = %+v, want f %d:%d", entry, stat.Dev, stat.Ino)
	}
}

// TestManifestRoundTripLargeFarm checks streaming write and read of fifty thousand entries.
func TestManifestRoundTripLargeFarm(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	farm := t.TempDir()
	writer, err := newFarmManifestWriter(farm)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.close()
	for i := 0; i < 50000; i++ {
		entry := FarmManifestEntry{Path: fmt.Sprintf("Meshes/File%05d.txt", i), Dev: 42, Ino: uint64(i + 1), Type: "f"}
		if err := writer.encoder.Encode(entry); err != nil {
			t.Fatal(err)
		}
		writer.count++
	}
	name, digest, count, err := writer.finish(farm)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadFarmManifest(farm, &Sentinel{SchemaVersion: 3, FarmID: writer.farmID, Manifest: name, ManifestSHA256: digest, ManifestEntries: count})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Entries) != 50000 || manifest.Entries["Meshes/File49999.txt"].Ino != 50000 {
		t.Errorf("round trip entries = %d, last = %+v", len(manifest.Entries), manifest.Entries["Meshes/File49999.txt"])
	}
	info, err := os.Stat(filepath.Join(farm, name))
	if err != nil || info.Mode().Perm() != 0644 {
		t.Errorf("manifest mode = %v, %v; want 0644", info, err)
	}
}

// TestManifestRejectsUnsafeAndDuplicatePaths checks that unsafe and ambiguous entry names are rejected.
func TestManifestRejectsUnsafeAndDuplicatePaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	entry := func(p string) string {
		body, err := json.Marshal(FarmManifestEntry{Path: p, Dev: 1, Ino: 2, Type: "f"})
		if err != nil {
			t.Fatal(err)
		}
		return string(body) + "\n"
	}
	for _, tc := range []struct {
		name  string
		body  string
		count int
	}{
		{"absolute", entry("/unsafe"), 1},
		{"parent", entry("Assets/../unsafe"), 1},
		{"hidden metadata", entry(".gorganizer-secret"), 1},
		{"backslash", entry(`Assets\unsafe`), 1},
		{"duplicate", entry("A.txt") + entry("A.txt"), 2},
		{"casefold duplicate", entry("A.txt") + entry("a.TXT"), 2},
		{"count mismatch", entry("A.txt"), 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			farm := t.TempDir()
			s := testManifestSentinel(t, farm, tc.body, tc.count)
			if _, err := ReadFarmManifest(farm, s); !errors.Is(err, ErrManifestInvalid) {
				t.Fatalf("ReadFarmManifest = %v, want ErrManifestInvalid", err)
			}
		})
	}
}

// TestManifestHashMismatchRejected checks that a changed entry body is rejected by its sentinel digest.
func TestManifestHashMismatchRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	farm := t.TempDir()
	s := testManifestSentinel(t, farm, "{\"p\":\"saved.ini\",\"d\":1,\"i\":2,\"t\":\"f\"}\n", 1)
	body, err := os.ReadFile(filepath.Join(farm, s.Manifest))
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.Replace(string(body), "saved.ini", "other.ini", 1))
	if err := os.WriteFile(filepath.Join(farm, s.Manifest), body, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFarmManifest(farm, s); !errors.Is(err, ErrManifestInvalid) {
		t.Errorf("ReadFarmManifest = %v, want ErrManifestInvalid", err)
	}
}

// TestSentinelV3RequiresMatchingManifest checks v3 references and the v1/v2 compatibility path.
func TestSentinelV3RequiresMatchingManifest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	backup := t.TempDir()
	id := "12345678-1234-1234-1234-123456789abc"
	base := Sentinel{Magic: SentinelMagic, GameID: "testgame", BackupPath: backup}
	base.Hash = ComputeLayerHash(base.Layers)
	for _, tc := range []struct {
		name    string
		version int
		id      string
		nameRef string
		valid   bool
	}{
		{"v1", 1, "", "", true},
		{"v2", 2, "", "", true},
		{"v3 without manifest", 3, id, "", false},
		{"v3 without id", 3, "", farmManifestPrefix + id + ".jsonl", false},
		{"v3 mismatched id", 3, "00000000-0000-0000-0000-000000000000", farmManifestPrefix + id + ".jsonl", false},
		{"v3 traversal", 3, id, "../" + farmManifestPrefix + id + ".jsonl", false},
		{"v3 valid", 3, id, farmManifestPrefix + id + ".jsonl", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := base
			s.SchemaVersion, s.FarmID, s.Manifest = tc.version, tc.id, tc.nameRef
			err := ValidateSentinel(&s)
			if tc.valid && err != nil || !tc.valid && !errors.Is(err, ErrSentinelInvalid) {
				t.Errorf("ValidateSentinel = %v, want valid %t", err, tc.valid)
			}
		})
	}
}

// TestRecoveryCapturesV3FarmWrites checks recovery keeps user writes but discards farm metadata.
func TestRecoveryCapturesV3FarmWrites(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	data := filepath.Join(root, "Data")
	overwrite := filepath.Join(root, "Overwrite")
	if err := os.MkdirAll(data, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "base.esm"), []byte("master"), 0644); err != nil {
		t.Fatal(err)
	}
	mm := NewMountManager(data, overwrite, "testgame")
	if err := mm.Activate([]Layer{{Name: "__base__", RootPath: data, Enabled: true}}, "Default"); err != nil {
		t.Fatal(err)
	}
	s, err := ReadSentinel(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "save.ini"), []byte("settings"), 0644); err != nil {
		t.Fatal(err)
	}
	outcome, err := CleanupStale(data)
	if err != nil || !outcome.Restored || outcome.Pending != nil {
		t.Fatalf("CleanupStale = %+v, %v; want restored", outcome, err)
	}
	if body, err := os.ReadFile(filepath.Join(overwrite, "save.ini")); err != nil || string(body) != "settings" {
		t.Errorf("captured user write = %q, %v", body, err)
	}
	if _, err := os.Lstat(filepath.Join(overwrite, s.Manifest)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("farm manifest was captured: %v", err)
	}
}

// TestCaptureIgnoresManifestFile checks that farm manifests and their temporary files never enter Overwrite.
func TestCaptureIgnoresManifestFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	farm := t.TempDir()
	overwrite := t.TempDir()
	for _, name := range []string{
		farmManifestPrefix + "12345678-1234-1234-1234-123456789abc.jsonl",
		".tmp-" + farmManifestPrefix + "12345678-1234-1234-1234-123456789abc.jsonl-123",
		".tmp-" + farmManifestPrefix + "entries-123",
	} {
		if err := os.WriteFile(filepath.Join(farm, name), []byte("state"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(farm, "save.ini"), []byte("user data"), 0644); err != nil {
		t.Fatal(err)
	}
	moved, err := CaptureNewFilesInto(farm, overwrite, true, false)
	if err != nil || moved != 1 {
		t.Fatalf("CaptureNewFilesInto = %d, %v; want only save.ini", moved, err)
	}
	entries, err := os.ReadDir(overwrite)
	if err != nil || len(entries) != 1 || entries[0].Name() != "save.ini" {
		t.Fatalf("Overwrite entries = %v, %v; want only save.ini", entries, err)
	}
}

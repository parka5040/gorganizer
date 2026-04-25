package vfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Real /proc/self/mountinfo line captured from the user's stale-FUSE state
// after the daemon crashed. The mountpoint contains a space, encoded as the
// kernel's \040 octal escape — that's the case the parser must handle, since
// the real Bethesda install paths all contain spaces.
const stalePosixMountinfoFixture = `21 26 0:20 / /proc rw,nosuid,nodev,noexec,relatime shared:5 - proc proc rw
22 26 0:21 / /sys rw,nosuid,nodev,noexec,relatime shared:6 - sysfs sysfs rw
26 1 259:2 / / rw,relatime shared:1 - ext4 /dev/nvme0n1p2 rw
138 26 0:51 / /home/parka/.local/share/Steam/steamapps/common/Fallout\040New\040Vegas/Data rw,nosuid,nodev,relatime shared:90 - fuse.gorganizer gorganizer rw,user_id=1000,group_id=1000,default_permissions
`

func TestParseMountinfo_FindsStaleFuseMount(t *testing.T) {
	target := "/home/parka/.local/share/Steam/steamapps/common/Fallout New Vegas/Data"
	got := parseMountinfo(strings.NewReader(stalePosixMountinfoFixture), target)
	if got == nil {
		t.Fatalf("expected to find FUSE mount at %q, got nil", target)
	}
	if got.Mountpoint != target {
		t.Errorf("Mountpoint = %q, want %q", got.Mountpoint, target)
	}
	if got.FSType != "fuse.gorganizer" {
		t.Errorf("FSType = %q, want %q", got.FSType, "fuse.gorganizer")
	}
	if got.Source != "gorganizer" {
		t.Errorf("Source = %q, want %q", got.Source, "gorganizer")
	}
}

func TestParseMountinfo_NoMatchReturnsNil(t *testing.T) {
	got := parseMountinfo(strings.NewReader(stalePosixMountinfoFixture),
		"/some/other/path")
	if got != nil {
		t.Errorf("expected nil for unmatched path, got %+v", got)
	}
}

func TestParseMountinfo_IgnoresNonFuseMountsAtSamePath(t *testing.T) {
	// Hypothetical: same path mounted as ext4. We must NOT treat that as a
	// stale FUSE we own — running fusermount on an ext4 mount would be
	// catastrophic.
	fixture := `99 26 0:99 / /home/parka/Data rw,relatime shared:99 - ext4 /dev/sda1 rw
`
	got := parseMountinfo(strings.NewReader(fixture), "/home/parka/Data")
	if got != nil {
		t.Errorf("expected nil for non-FUSE mount, got %+v", got)
	}
}

func TestParseMountinfo_HandlesOptionalFields(t *testing.T) {
	// Lines with no optional fields between (7) and (8) — the dash is right
	// after the propagation-flags column. Verifies the dash-walk is correct.
	fixture := `42 1 0:42 / /m rw,relatime - fuse.x src rw
`
	got := parseMountinfo(strings.NewReader(fixture), "/m")
	if got == nil {
		t.Fatal("expected match, got nil")
	}
	if got.FSType != "fuse.x" || got.Source != "src" {
		t.Errorf("got %+v", got)
	}
}

// TestCleanupStale_SentinelCrashRecovery: a daemon that activated then
// died (without calling Deactivate) leaves Data/ as a hardlink farm and
// Data.orig/ as the original. The next CleanupStale must detect the
// sentinel, tear down the farm, and restore Data.orig.
func TestCleanupStale_SentinelCrashRecovery(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")
	backupPath := dataPath + ".orig"

	// Simulate post-crash state.
	if err := os.MkdirAll(backupPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backupPath, "FalloutNV.esm"),
		[]byte("master"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataPath, 0755); err != nil {
		t.Fatal(err)
	}
	// Overlay placeholder: pretend the materialized tree had a hardlink.
	if err := os.WriteFile(filepath.Join(dataPath, "FalloutNV.esm"), []byte("master"), 0444); err != nil {
		t.Fatal(err)
	}
	// Write a valid sentinel pointing at backupPath.
	s := &Sentinel{
		SchemaVersion:       CurrentSentinelSchema,
		Magic:               SentinelMagic,
		GameID:              "falloutnv",
		BackupPath:          backupPath,
		MaterializerVersion: CurrentMaterializerVersion,
	}
	if err := WriteSentinel(dataPath, s); err != nil {
		t.Fatal(err)
	}

	if _, err := CleanupStale(dataPath); err != nil {
		t.Fatalf("CleanupStale: %v", err)
	}

	// Data.orig must be gone, Data must contain only the original master.
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Errorf("backup should be removed, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dataPath, SentinelFilename)); !os.IsNotExist(err) {
		t.Errorf("sentinel should be gone after recovery, got err=%v", err)
	}
	got, err := os.ReadFile(filepath.Join(dataPath, "FalloutNV.esm"))
	if err != nil {
		t.Fatalf("FalloutNV.esm should be in restored Data/: %v", err)
	}
	if string(got) != "master" {
		t.Errorf("FalloutNV.esm = %q, want %q", string(got), "master")
	}
}

// TestCleanupStale_RejectsBadSentinel: a sentinel-shaped file that doesn't
// validate must NOT trigger destructive recovery — that file could be from
// some unrelated tool, and rm -rf'ing the dir on that signal would be a
// dataloss bug. CleanupStale should bail with the dir intact.
func TestCleanupStale_RejectsBadSentinel(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")
	if err := os.MkdirAll(dataPath, 0755); err != nil {
		t.Fatal(err)
	}
	// Invalid sentinel: bad magic.
	if err := os.WriteFile(filepath.Join(dataPath, SentinelFilename),
		[]byte(`{"schema_version":1,"magic":"not-us","backup_path":"/bogus"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataPath, "user_file.txt"),
		[]byte("important"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := CleanupStale(dataPath); err != nil {
		t.Fatalf("CleanupStale should return nil for unrecognized sentinel, got %v", err)
	}

	if got, err := os.ReadFile(filepath.Join(dataPath, "user_file.txt")); err != nil ||
		string(got) != "important" {
		t.Errorf("user file should be untouched: got=%q err=%v", string(got), err)
	}
}

// TestCleanupStale_PendingForUnrecognizedDataAndBackup: the daemon must
// NOT auto-restore when Data/ is non-empty with no recognizable sentinel
// AND Data.orig/ exists. That state could be a hand-edited install or a
// failed activate from a different gorganizer build — destroying Data/
// without confirmation is a dataloss bug. CleanupStale returns Pending
// so the GUI can prompt.
func TestCleanupStale_PendingForUnrecognizedDataAndBackup(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")
	backupPath := dataPath + ".orig"
	if err := os.MkdirAll(backupPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backupPath, "FalloutNV.esm"), []byte("orig"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataPath, 0755); err != nil {
		t.Fatal(err)
	}
	// Hand-edited Data/: real content but no sentinel.
	if err := os.WriteFile(filepath.Join(dataPath, "user_edit.txt"), []byte("mine"), 0644); err != nil {
		t.Fatal(err)
	}

	outcome, err := CleanupStale(dataPath)
	if err != nil {
		t.Fatalf("CleanupStale should not error on ambiguous state, got %v", err)
	}
	if outcome.Pending == nil {
		t.Fatal("expected Pending to be set for unrecognized Data + backup")
	}
	if outcome.Restored {
		t.Error("must not auto-restore over unrecognized Data/")
	}
	if outcome.Pending.DataPath == "" || outcome.Pending.BackupPath == "" {
		t.Errorf("Pending paths must be populated, got %+v", outcome.Pending)
	}
	// Both Data/ and Data.orig/ must remain untouched.
	if _, err := os.Stat(filepath.Join(dataPath, "user_edit.txt")); err != nil {
		t.Errorf("user_edit.txt should still be in Data/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(backupPath, "FalloutNV.esm")); err != nil {
		t.Errorf("Data.orig/ should still be intact: %v", err)
	}
}

// TestRestoreFromBackup_HappyPath: the post-consent destructive restore.
// Verifies the rm -rf + rename actually removes Data/ and renames Data.orig
// → Data, and refuses cleanly when there's no backup to restore.
func TestRestoreFromBackup_HappyPath(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")
	backupPath := dataPath + ".orig"
	if err := os.MkdirAll(dataPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataPath, "junk.txt"), []byte("discard me"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(backupPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backupPath, "FalloutNV.esm"), []byte("master"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := RestoreFromBackup(dataPath); err != nil {
		t.Fatalf("RestoreFromBackup: %v", err)
	}

	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Errorf("backup should be gone, err=%v", err)
	}
	got, err := os.ReadFile(filepath.Join(dataPath, "FalloutNV.esm"))
	if err != nil || string(got) != "master" {
		t.Errorf("Data/FalloutNV.esm = %q err=%v", string(got), err)
	}
	if _, err := os.Stat(filepath.Join(dataPath, "junk.txt")); !os.IsNotExist(err) {
		t.Errorf("junk.txt should be discarded, err=%v", err)
	}
}

func TestRestoreFromBackup_NoBackupErrors(t *testing.T) {
	dir := t.TempDir()
	dataPath := filepath.Join(dir, "Data")
	if err := os.MkdirAll(dataPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := RestoreFromBackup(dataPath); err == nil {
		t.Error("expected error when no Data.orig exists")
	}
}

func TestUnescapeMountinfoField(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`/foo/bar`, `/foo/bar`},
		{`/Fallout\040New\040Vegas`, `/Fallout New Vegas`},
		{`tab\011here`, "tab\there"},
		{`\134backslash`, "\\backslash"},
		{`no\esc`, `no\esc`}, // unknown escape — leave intact
	}
	for _, c := range cases {
		if got := unescapeMountinfoField(c.in); got != c.want {
			t.Errorf("unescapeMountinfoField(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

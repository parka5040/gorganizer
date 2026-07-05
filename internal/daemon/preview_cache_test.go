package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newLeasedEntry(t *testing.T, dir, name string) *previewEntry {
	t.Helper()
	root := filepath.Join(dir, name)
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	return &previewEntry{ExtractRoot: root}
}

// A leased entry must survive eviction pressure from put(), and only be removed
// once the last lease is released (H-12 / Guard R3).
func TestPreviewCache_LeasedSurvivesEvictionThenReclaimed(t *testing.T) {
	dir := t.TempDir()
	c := newPreviewCache(time.Hour, 1) // maxLen 1 => aggressive eviction

	id := c.put(newLeasedEntry(t, dir, "leased"))
	pe := c.acquire(id)
	if pe == nil {
		t.Fatal("acquire returned nil")
	}
	root := pe.ExtractRoot

	// Force eviction pressure: two more puts, each exceeding maxLen.
	c.put(newLeasedEntry(t, dir, "a"))
	c.put(newLeasedEntry(t, dir, "b"))

	// The leased extract root must still exist.
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("leased extract root was removed under eviction pressure: %v", err)
	}

	// discard while leased just marks pending; still present.
	c.discard(id)
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("leased extract root removed by discard: %v", err)
	}

	// Final release performs the removal exactly once.
	c.release(id)
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("extract root should be removed after final release, err=%v", err)
	}
	// Releasing again must not panic or double-remove.
	c.release(id)
}

// sweep must defer removal of a leased-but-expired entry.
func TestPreviewCache_SweepDefersLeased(t *testing.T) {
	dir := t.TempDir()
	c := newPreviewCache(time.Nanosecond, 10)

	id := c.put(newLeasedEntry(t, dir, "leased"))
	pe := c.acquire(id)
	root := pe.ExtractRoot

	time.Sleep(time.Millisecond)
	c.sweep() // expired, but leased => kept

	if _, err := os.Stat(root); err != nil {
		t.Fatalf("leased entry removed by sweep: %v", err)
	}
	c.release(id)
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("entry should be gone after release, err=%v", err)
	}
}

package transfer

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestManifestValidationIsLinear validates a large unique manifest and rejects both entry caps.
func TestManifestValidationIsLinear(t *testing.T) {
	isolatedImportRoot(t)
	m := craftedManifest()
	m.Mods = make([]ModEntry, 100_000)
	for i := range m.Mods {
		m.Mods[i].Folder = fmt.Sprintf("mod-%06d", i)
	}
	archive := writeArchiveFile(t, buildTarBytes(t, m, nil))
	started := time.Now()
	if _, err := ReadManifest(context.Background(), testGame, archive); err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Errorf("validating 100,000 unique mod names took %v", elapsed)
	}
	for _, kind := range []string{"mods", "profiles"} {
		t.Run(kind, func(t *testing.T) {
			m := craftedManifest()
			if kind == "mods" {
				m.Mods = make([]ModEntry, 100_001)
			} else {
				m.Profiles = make([]string, 100_001)
			}
			archive := writeArchiveFile(t, buildTarBytes(t, m, nil))
			_, err := ReadManifest(context.Background(), testGame, archive)
			requireBundleRejected(t, err, BundleRejectedLimit, manifestEntryName)
		})
	}
}

// TestManifestUnicodeCaseFolding distinguishes lowercase collisions from actual folded duplicates.
func TestManifestUnicodeCaseFolding(t *testing.T) {
	isolatedImportRoot(t)
	for _, tc := range []struct {
		name    string
		folders []string
		item    string
	}{
		{"folded across lowercase keys", []string{"Σ", "ς"}, "ς"},
		{"different folds with one lowercase key", []string{"İ", "i"}, ""},
		{"duplicate after lowercase collision", []string{"İ", "i", "İ"}, "İ"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := craftedManifest()
			m.Mods = nil
			for _, folder := range tc.folders {
				m.Mods = append(m.Mods, ModEntry{Folder: folder})
			}
			archive := writeArchiveFile(t, buildTarBytes(t, m, nil))
			_, err := ReadManifest(context.Background(), testGame, archive)
			if tc.item != "" {
				requireBundleRejected(t, err, BundleRejectedDuplicate, tc.item)
			} else if err != nil {
				t.Fatalf("ReadManifest: %v", err)
			}
		})
	}
}

// TestManifestValidationHonorsCancellation stops validation of a large manifest when its context is cancelled.
func TestManifestValidationHonorsCancellation(t *testing.T) {
	isolatedImportRoot(t)
	m := craftedManifest()
	m.Mods = make([]ModEntry, 10_000)
	for i := range m.Mods {
		m.Mods[i].Folder = fmt.Sprintf("mod-%05d", i)
	}
	archive := writeArchiveFile(t, buildTarBytes(t, m, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	controlled := &cancelAfterChecks{Context: ctx, cancel: cancel, at: 50}
	if _, err := ReadManifest(controlled, testGame, archive); err != context.Canceled {
		t.Errorf("ReadManifest error = %v, want context.Canceled", err)
	}
}

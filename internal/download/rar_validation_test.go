package download

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestValidateRarExtractionConsumesSharedBudget enforces entry, per-file and total limits without removing the directory.
func TestValidateRarExtractionConsumesSharedBudget(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	for _, tc := range []struct {
		name   string
		limits extractLimits
	}{
		{name: "entry count includes directories", limits: extractLimits{MaxEntries: 1, MaxEntryBytes: 8, MaxTotalBytes: 8}},
		{name: "entry size", limits: extractLimits{MaxEntries: 2, MaxEntryBytes: 2, MaxTotalBytes: 8}},
		{name: "total size", limits: extractLimits{MaxEntries: 2, MaxEntryBytes: 8, MaxTotalBytes: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			file := filepath.Join(root, "subdir", "file.txt")
			if err := os.Mkdir(filepath.Dir(file), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte("abc"), 0644); err != nil {
				t.Fatal(err)
			}
			err := validateRarExtraction(root, newExtractBudget(tc.limits))
			var rejected *ArchiveRejectedError
			if !errors.As(err, &rejected) || rejected.Reason != ArchiveRejectedLimit {
				t.Fatalf("validateRarExtraction error = %v, want limit", err)
			}
			data, err := os.ReadFile(file)
			if err != nil || string(data) != "abc" {
				t.Fatalf("file after rejection = %q, %v, want abc", data, err)
			}
		})
	}
}

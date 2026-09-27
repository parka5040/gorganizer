package transfer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/config"
)

// TestExportRefusesBundlesImportWouldReject rejects oversized entries and payloads before creating an archive.
func TestExportRefusesBundlesImportWouldReject(t *testing.T) {
	cases := []struct {
		name    string
		content string
		adjust  func(*importLimits, int64)
		reason  string
	}{
		{"entry count", "mod", func(l *importLimits, _ int64) { l.entries = 2 }, "entry count"},
		{"manifest size", "mod", func(l *importLimits, _ int64) { l.manifestBytes = 1 }, "manifest size"},
		{"file size", "mod", func(l *importLimits, _ int64) { l.fileBytes = 3 }, "file size"},
		{"total payload", "mod", func(l *importLimits, size int64) { l.payloadBytes = size + 3 }, "total payload"},
		{"profile included", "profile", func(l *importLimits, _ int64) { l.fileBytes = 3 }, "file size"},
		{"overwrite included", "overwrite", func(l *importLimits, _ int64) { l.fileBytes = 3 }, "file size"},
		{"game settings included", "gamesettings", func(l *importLimits, _ int64) { l.fileBytes = 3 }, "file size"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolatedImportRoot(t)
			var opts ExportOptions
			opts.GameID = testGame
			output := filepath.Join(t.TempDir(), "bundle.tar.zst")
			opts.OutputPath = output
			m := &Manifest{SchemaVersion: SchemaVersion, GorganizerVersion: GorganizerVersion, GameID: testGame}
			switch tc.content {
			case "mod":
				writeFileT(t, filepath.Join(config.ModsDir(testGame), "M", "data"), "four")
				m.Mods = []ModEntry{{Folder: "M", Name: "M", FileCount: 1, TotalBytes: 4}}
			case "profile":
				writeFileT(t, filepath.Join(config.ProfilesDir(testGame), "P", "data"), "four")
				m.Profiles = []string{"P"}
			case "overwrite":
				writeFileT(t, filepath.Join(config.ModsDir(testGame), "Overwrite", "data"), "four")
				opts.IncludeOverwrite = true
				m.IncludesOverwrite = true
			case "gamesettings":
				writeFileT(t, config.GameSettingsPath(testGame), "four")
				opts.IncludeGameSettings = true
				m.IncludesGameSettings = true
			}
			manifest, err := EncodeManifest(m)
			if err != nil {
				t.Fatal(err)
			}
			limits := defaultImportLimits()
			tc.adjust(&limits, int64(len(manifest)))
			opts.limits = &limits
			_, err = Export(context.Background(), opts, nil)
			var tooLarge *TransferTooLargeError
			if !errors.As(err, &tooLarge) || tooLarge.Reason != tc.reason {
				t.Fatalf("Export error = %v, want TransferTooLargeError reason %q", err, tc.reason)
			}
			if want := "This export is larger than Gorganizer can import again (" + tc.reason + "). Export fewer mods at a time."; tooLarge.Error() != want {
				t.Errorf("export error text = %q, want %q", tooLarge.Error(), want)
			}
			if _, err := os.Lstat(output); !os.IsNotExist(err) {
				t.Errorf("refused export created an archive: %v", err)
			}
		})
	}
}

// TestExportRefusalPreservesExistingOutput leaves an existing destination unchanged on limit rejection.
func TestExportRefusalPreservesExistingOutput(t *testing.T) {
	isolatedImportRoot(t)
	writeFileT(t, filepath.Join(config.ModsDir(testGame), "M", "data"), "four")
	output := filepath.Join(t.TempDir(), "bundle.tar.zst")
	writeFileT(t, output, "untouched")
	limits := defaultImportLimits()
	limits.fileBytes = 3
	_, err := Export(context.Background(), ExportOptions{GameID: testGame, OutputPath: output, limits: &limits}, nil)
	var tooLarge *TransferTooLargeError
	if !errors.As(err, &tooLarge) || !strings.Contains(tooLarge.Error(), "file size") {
		t.Fatalf("Export error = %v", err)
	}
	if got := readFileT(t, output); got != "untouched" {
		t.Errorf("existing output = %q", got)
	}
}

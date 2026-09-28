package download

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parka/gorganizer/internal/config"
)

type destinationResolver struct {
	filename string
	url      string
}

func (r destinationResolver) ResolveDownloadURL(*NXMLink) (string, error) {
	return r.url, nil
}

func (r destinationResolver) GetModInfo(string, int) (*NexusModInfo, error) {
	return &NexusModInfo{Name: "Example"}, nil
}

func (r destinationResolver) GetFileDetails(string, int, int) (*NexusFileDetails, error) {
	return &NexusFileDetails{FileName: r.filename}, nil
}

type destinationTransport func(*http.Request) (*http.Response, error)

func (f destinationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// isolatedDownloadRoot redirects downloads and all test state to a temporary root.
func isolatedDownloadRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("GORGANIZER_ROOT", root)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "runtime"))
	t.Setenv("TMPDIR", root)
	return root
}

// destinationManager returns a download manager whose HTTP response stays in memory.
func destinationManager(r destinationResolver, snapshot *DownloadSnapshot) *Manager {
	return &Manager{
		nexus: r,
		httpClient: &http.Client{Transport: destinationTransport(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("archive data")),
				Header:     make(http.Header),
				Request:    req,
			}, nil
		})},
		hooks: ManagerHooks{OnDownloadProgress: func(s DownloadSnapshot) {
			*snapshot = s
		}},
	}
}

// TestSafeArchiveFilename checks traversal reduction, invalid names, and the length limit.
func TestSafeArchiveFilename(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "../../evil", want: "evil", ok: true},
		{input: "a/b.zip", want: "b.zip", ok: true},
		{input: `a\b.zip`, want: "b.zip", ok: true},
		{input: "/abs.zip", want: "abs.zip", ok: true},
		{input: ".hidden"},
		{input: ".."},
		{input: ""},
		{input: "a\x00.zip"},
		{input: "a\x7f.zip"},
		{input: strings.Repeat("a", 201) + ".zip"},
		{input: strings.Repeat("é", 100), want: strings.Repeat("é", 100), ok: true},
		{input: strings.Repeat("é", 101)},
		{input: "archive.7z", want: "archive.7z", ok: true},
		{input: " archive.zip ", want: "archive.zip", ok: true},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got, ok := SafeArchiveFilename(tc.input)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("SafeArchiveFilename(%q) = %q, %v; want %q, %v", tc.input, got, ok, tc.want, tc.ok)
			}
		})
	}

	link := &NXMLink{ModID: 7, FileID: 8}
	for _, tc := range []struct {
		name, url, want string
	}{
		{name: "../../evil", url: "https://cdn.example/a/b.zip", want: "evil"},
		{name: ".hidden", url: "https://cdn.example/a/b.zip", want: "b.zip"},
		{name: ".hidden", url: "https://cdn.example/a/.hidden", want: "7_8.archive"},
	} {
		if got := pickArchiveFilename(&NexusFileDetails{FileName: tc.name}, tc.url, link); got != tc.want {
			t.Errorf("pickArchiveFilename(%q, %q) = %q, want %q", tc.name, tc.url, got, tc.want)
		}
	}
}

// TestNexusFilenameCannotEscapeDownloads checks hostile metadata lands only under Downloads.
func TestNexusFilenameCannotEscapeDownloads(t *testing.T) {
	root := isolatedDownloadRoot(t)
	var snapshot DownloadSnapshot
	m := destinationManager(destinationResolver{filename: "../../escape.zip", url: "https://cdn.example/archive.zip"}, &snapshot)
	dl := &Download{ID: "dl-00000000-0000-4000-8000-000000000001", GameID: "skyrimse", NXMURI: "nxm://skyrimse/mods/7/files/8"}
	m.runPipeline(context.Background(), dl)
	if snapshot.Status != StatusDownloaded {
		t.Fatalf("status = %v, error = %s", snapshot.Status, snapshot.Error)
	}
	path := filepath.Join(config.DownloadsDir(dl.GameID), "7_Example", "escape.zip")
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "archive data" {
		t.Fatalf("archive at %q = %q, %v", path, got, err)
	}
	for _, path := range []string{filepath.Join(root, "escape.zip"), filepath.Join(filepath.Dir(config.DownloadsDir(dl.GameID)), "escape.zip")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("outside path %q exists or cannot be checked: %v", path, err)
		}
	}
}

// TestResumeRejectsUnsafeLedgerDestination checks rehydration and retry refuse stored paths.
func TestResumeRejectsUnsafeLedgerDestination(t *testing.T) {
	tests := []string{"../../x.zip", "/abs.zip", `safe\..\x.zip`, "safe/.hidden", "safe/../x.zip", "safe/x.zip/extra"}
	for _, rel := range tests {
		t.Run(rel, func(t *testing.T) {
			root := isolatedDownloadRoot(t)
			outside := filepath.Join(root, "x.zip")
			if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			e := LedgerEntry{ID: "dl-00000000-0000-4000-8000-000000000001", GameID: "skyrimse", NXMURI: "nxm://skyrimse/mods/7/files/8", ArchiveRelPath: rel, Status: LedgerDownloading}
			if err := SaveLedger(e.GameID, []LedgerEntry{e}); err != nil {
				t.Fatal(err)
			}
			initial, err := LoadLedger(e.GameID)
			if err != nil || len(initial) != 1 {
				t.Fatalf("loading seeded ledger: %#v, %v", initial, err)
			}
			storedRel := initial[0].ArchiveRelPath
			var snapshot DownloadSnapshot
			m := destinationManager(destinationResolver{}, &snapshot)
			m.RehydrateLedger([]string{e.GameID})
			if snapshot.Status != StatusFailed || !strings.Contains(snapshot.Error, "archive rejected: destination") || len(m.queued) != 0 {
				t.Fatalf("rehydrate status = %v, error = %q, queued = %d", snapshot.Status, snapshot.Error, len(m.queued))
			}
			entries, err := LoadLedger(e.GameID)
			if err != nil || len(entries) != 1 || entries[0].Status != LedgerFailed ||
				strings.ReplaceAll(entries[0].ArchiveRelPath, `\`, "") != strings.ReplaceAll(storedRel, `\`, "") {
				t.Fatalf("ledger after rehydration = %#v, %v", entries, err)
			}
			_, err = m.RetryDownload(e.ID, []string{e.GameID})
			var rejected *ArchiveRejectedError
			if !errors.As(err, &rejected) || rejected.Reason != ArchiveRejectedDestination || rejected.Detail != entries[0].ArchiveRelPath {
				t.Fatalf("retry error = %v, want destination rejection for %q", err, rel)
			}
			got, err := os.ReadFile(outside)
			if err != nil || string(got) != "keep" {
				t.Fatalf("outside file = %q, %v", got, err)
			}
		})
	}
}

// TestDownloadRejectsSymlinkPart checks a symlinked part cannot overwrite its target.
func TestDownloadRejectsSymlinkPart(t *testing.T) {
	root := isolatedDownloadRoot(t)
	outside := filepath.Join(root, "outside.zip")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(config.DownloadsDir("skyrimse"), "7_Example", "archive.zip")
	if err := os.MkdirAll(filepath.Dir(archive), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, PartPath(archive)); err != nil {
		t.Fatal(err)
	}
	var snapshot DownloadSnapshot
	m := destinationManager(destinationResolver{filename: "archive.zip", url: "https://cdn.example/archive.zip"}, &snapshot)
	m.runPipeline(context.Background(), &Download{ID: "dl-00000000-0000-4000-8000-000000000001", GameID: "skyrimse", NXMURI: "nxm://skyrimse/mods/7/files/8"})
	if snapshot.Status != StatusFailed || !strings.Contains(snapshot.Error, "archive rejected: destination") {
		t.Fatalf("status = %v, error = %s", snapshot.Status, snapshot.Error)
	}
	got, err := os.ReadFile(outside)
	if err != nil || string(got) != "keep" {
		t.Fatalf("outside file = %q, %v", got, err)
	}
	if _, err := os.Lstat(archive); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive exists or cannot be checked: %v", err)
	}
}

// TestDownloadRejectsNonRegularPart checks a directory cannot be used as a part file.
func TestDownloadRejectsNonRegularPart(t *testing.T) {
	isolatedDownloadRoot(t)
	archive := filepath.Join(config.DownloadsDir("skyrimse"), "7_Example", "archive.zip")
	if err := os.MkdirAll(PartPath(archive), 0755); err != nil {
		t.Fatal(err)
	}
	var snapshot DownloadSnapshot
	m := destinationManager(destinationResolver{filename: "archive.zip", url: "https://cdn.example/archive.zip"}, &snapshot)
	m.runPipeline(context.Background(), &Download{ID: "dl-00000000-0000-4000-8000-000000000001", GameID: "skyrimse", NXMURI: "nxm://skyrimse/mods/7/files/8"})
	if snapshot.Status != StatusFailed || !strings.Contains(snapshot.Error, "archive rejected: destination") {
		t.Fatalf("status = %v, error = %s", snapshot.Status, snapshot.Error)
	}
}

// TestValidateLedgerDestination checks game identity, queued entries, and path normalization.
func TestValidateLedgerDestination(t *testing.T) {
	isolatedDownloadRoot(t)
	e := LedgerEntry{GameID: "fallout4", ArchiveRelPath: "7_Example/archive.zip", Status: LedgerDownloading}
	err := validateLedgerDestination("skyrimse", e)
	var rejected *ArchiveRejectedError
	if !errors.As(err, &rejected) || rejected.Reason != ArchiveRejectedDestination || rejected.Detail != e.ArchiveRelPath {
		t.Fatalf("validateLedgerDestination() = %v, want destination rejection", err)
	}
	if err := validateLedgerDestination("skyrimse", LedgerEntry{GameID: "skyrimse", Status: LedgerQueued}); err != nil {
		t.Fatalf("queued entry without a selected archive = %v", err)
	}
	if _, err := resolveArchiveDestination("skyrimse", "safe/x.zip "); !errors.As(err, &rejected) || rejected.Reason != ArchiveRejectedDestination {
		t.Fatalf("destination with trailing space = %v, want destination rejection", err)
	}
}

// TestDownloadRejectsSymlinkFolder checks the download cannot enter a symlinked mod folder.
func TestDownloadRejectsSymlinkFolder(t *testing.T) {
	root := isolatedDownloadRoot(t)
	downloads := config.DownloadsDir("skyrimse")
	if err := os.MkdirAll(downloads, 0755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(downloads, "7_Example")); err != nil {
		t.Fatal(err)
	}
	var snapshot DownloadSnapshot
	m := destinationManager(destinationResolver{filename: "archive.zip", url: "https://cdn.example/archive.zip"}, &snapshot)
	m.runPipeline(context.Background(), &Download{ID: "dl-00000000-0000-4000-8000-000000000001", GameID: "skyrimse", NXMURI: "nxm://skyrimse/mods/7/files/8"})
	if snapshot.Status != StatusFailed || !strings.Contains(snapshot.Error, "archive rejected: destination") {
		t.Fatalf("status = %v, error = %s", snapshot.Status, snapshot.Error)
	}
	if _, err := os.Lstat(filepath.Join(outside, "archive.zip.part")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside part exists or cannot be checked: %v", err)
	}
}

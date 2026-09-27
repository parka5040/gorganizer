package daemon

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
	"github.com/parka/gorganizer/internal/download"
	"github.com/parka/gorganizer/internal/dto"
)

const lifecycleURI = "nxm://skyrimspecialedition/mods/7/files/8"

type lifecycleResolver struct {
	key     string
	resolve chan<- string
}

func (r *lifecycleResolver) ValidateAPIKey(context.Context) error { return nil }
func (r *lifecycleResolver) GetModInfo(string, int) (*download.NexusModInfo, error) {
	return &download.NexusModInfo{Name: "Example"}, nil
}
func (r *lifecycleResolver) GetFileDetails(string, int, int) (*download.NexusFileDetails, error) {
	return &download.NexusFileDetails{FileName: "archive.zip"}, nil
}
func (r *lifecycleResolver) ResolveDownloadURL(*download.NXMLink) (string, error) {
	if r.resolve != nil {
		r.resolve <- r.key
	}
	return "https://cdn.example/" + r.key + ".zip", nil
}

type lifecycleTransport func(*http.Request) (*http.Response, error)

func (f lifecycleTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type heldDownloadBody struct {
	entered chan struct{}
	release chan struct{}
	reads   int
}

func (b *heldDownloadBody) Read(p []byte) (int, error) {
	b.reads++
	switch b.reads {
	case 1:
		return copy(p, "first-"), nil
	case 2:
		close(b.entered)
		<-b.release
		return copy(p, "last"), nil
	default:
		return 0, io.EOF
	}
}

func (b *heldDownloadBody) Close() error { return nil }

// lifecycleManager injects a client that never contacts a real network.
func lifecycleManager(t *testing.T, transport http.RoundTripper) {
	t.Helper()
	original := newDownloadManager
	newDownloadManager = func(resolver download.URLResolver, concurrent int, hooks download.ManagerHooks) *download.Manager {
		return download.NewManagerWithClient(resolver, concurrent, hooks, &http.Client{Transport: transport})
	}
	t.Cleanup(func() { newDownloadManager = original })
}

// lifecycleWaitLedger waits until the pipeline persists its terminal status.
func lifecycleWaitLedger(t *testing.T, status download.LedgerStatus) download.LedgerEntry {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := download.LoadLedger("skyrimse")
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) == 1 && entries[0].Status == status {
			return entries[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	entries, err := download.LoadLedger("skyrimse")
	t.Fatalf("ledger never reached %q: entries=%+v, err=%v", status, entries, err)
	return download.LedgerEntry{}
}

// lifecycleWaitInactive waits for a completed pipeline to release its manager slot.
func lifecycleWaitInactive(t *testing.T, manager *download.Manager, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := manager.GetProgress(id); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pipeline %s did not finish", id)
}

func TestAPIKeyChangeKeepsOneManagerAndWriter(t *testing.T) {
	d := downloadsDaemon(t)
	downloadsInstallFakeNexus(t, func(key string) nexusDownloadClient { return &lifecycleResolver{key: key} })
	body := &heldDownloadBody{entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(body.release) }) })
	var requests atomic.Int32
	lifecycleManager(t, lifecycleTransport(func(req *http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header), Request: req}, nil
	}))
	downloadsSetKey(t, d, "old")
	manager := d.downloadStateSnapshot().manager
	id, _, err := d.StartDownload(lifecycleURI)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-body.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not reach the middle of the body")
	}
	downloadsSetKey(t, d, "new")
	if current := d.downloadStateSnapshot().manager; current != manager {
		t.Fatal("changing the key replaced the download manager")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("HTTP requests before release = %d, want one", got)
	}
	if snap, err := manager.GetProgress(id); err != nil || snap.Status != dto.DownloadStatusDownloading {
		t.Fatalf("in-flight download = %+v, %v", snap, err)
	}
	release.Do(func() { close(body.release) })
	path := filepath.Join(config.DownloadsDir("skyrimse"), "7_Example", "archive.zip")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			if string(data) != "first-last" || requests.Load() != 1 {
				t.Fatalf("archive = %q, requests = %d", data, requests.Load())
			}
			lifecycleWaitInactive(t, manager, id)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("archive did not land")
}

func TestFailedKeySaveKeepsPreviousResolver(t *testing.T) {
	d := downloadsDaemon(t)
	resolved := make(chan string, 2)
	downloadsInstallFakeNexus(t, func(key string) nexusDownloadClient {
		return &lifecycleResolver{key: key, resolve: resolved}
	})
	lifecycleManager(t, lifecycleTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(bytes.NewReader(nil)), Header: make(http.Header), Request: req}, nil
	}))
	downloadsSetKey(t, d, "old")
	manager := d.downloadStateSnapshot().manager
	cfgPath := filepath.Join(config.ConfigDir(), "config.json")
	if err := os.Rename(cfgPath, cfgPath+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cfgPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if result, err := d.SetNexusAPIKey(context.Background(), "new"); err == nil || result != nil {
		t.Fatalf("save failure = %+v, %v", result, err)
	}
	if state := d.downloadStateSnapshot(); state.key != "old" || state.manager != manager {
		t.Fatalf("state after failed save = %+v", state)
	}
	id, _, err := d.StartDownload(lifecycleURI)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case key := <-resolved:
		if key != "old" {
			t.Fatalf("pipeline resolved with %q, want old", key)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline did not resolve")
	}
	if id == "" {
		t.Fatal("download was not admitted")
	}
	lifecycleWaitLedger(t, download.LedgerFailed)
	lifecycleWaitInactive(t, manager, id)
}

func TestListArchivesPreservesFailedRetry(t *testing.T) {
	d := downloadsDaemon(t)
	downloadsInstallFakeNexus(t, func(key string) nexusDownloadClient { return &lifecycleResolver{key: key} })
	var requests atomic.Int32
	lifecycleManager(t, lifecycleTransport(func(req *http.Request) (*http.Response, error) {
		status := http.StatusInternalServerError
		body := ""
		if requests.Add(1) == 2 {
			status = http.StatusOK
			body = "retried archive"
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header), Request: req}, nil
	}))
	downloadsSetKey(t, d, "test")
	id, _, err := d.StartDownload(lifecycleURI)
	if err != nil {
		t.Fatal(err)
	}
	entry := lifecycleWaitLedger(t, download.LedgerFailed)
	if entry.ID != id {
		t.Fatalf("failed ledger ID = %q, want %q", entry.ID, id)
	}
	for range 2 {
		rows, err := d.ListArchives("skyrimse")
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || rows[0].DownloadID != id || rows[0].Status != dto.DownloadStatusFailed {
			t.Fatalf("failed archive rows = %+v", rows)
		}
	}
	lifecycleWaitInactive(t, d.downloadStateSnapshot().manager, id)
	if _, err := d.RetryDownload(id); err != nil {
		t.Fatalf("RetryDownload(%q): %v", id, err)
	}
	path := filepath.Join(config.DownloadsDir("skyrimse"), entry.ArchiveRelPath)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			if string(data) != "retried archive" {
				t.Fatalf("retried archive = %q", data)
			}
			lifecycleWaitInactive(t, d.downloadStateSnapshot().manager, id)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("retried archive did not land")
}

func TestRepeatedListingsDoNotWriteLedger(t *testing.T) {
	d := downloadsDaemon(t)
	const gameID = "skyrimse"
	for _, path := range []string{"indexed.zip", "indexed-failed.zip"} {
		if err := download.UpsertEntry(gameID, download.IndexEntry{Path: path, ModID: 7}); err != nil {
			t.Fatal(err)
		}
	}
	entries := []download.LedgerEntry{
		{ID: "failed", ArchiveRelPath: "failed.zip", Status: download.LedgerFailed},
		{ID: "cancelled", ArchiveRelPath: "cancelled.zip", Status: download.LedgerCancelled},
		{ID: "indexed", ArchiveRelPath: "indexed.zip", Status: download.LedgerQueued},
		{ID: "indexed-failed", ArchiveRelPath: "indexed-failed.zip", Status: download.LedgerFailed},
		{ID: "empty", Status: ""},
	}
	if err := download.SaveLedger(gameID, entries); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(config.DownloadsDir(gameID), "inflight.yaml")
	modified := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		rows, err := d.ListArchives(gameID)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 5 {
			t.Fatalf("listed rows = %+v", rows)
		}
		byID := make(map[string]dto.DownloadStatus)
		for _, row := range rows {
			byID[row.DownloadID] = row.Status
		}
		for _, expected := range []struct {
			id     string
			status dto.DownloadStatus
		}{
			{"failed", dto.DownloadStatusFailed},
			{"cancelled", dto.DownloadStatusCancelled},
			{"indexed", dto.DownloadStatusQueued},
			{"indexed-failed", dto.DownloadStatusFailed},
			{"empty", dto.DownloadStatusUnknown},
		} {
			if byID[expected.id] != expected.status {
				t.Errorf("row %s = %v, want %v", expected.id, byID[expected.id], expected.status)
			}
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(after, before) || !info.ModTime().Equal(modified) {
			t.Fatalf("listing changed ledger bytes or mtime: equal=%v, mtime=%s, want=%s", bytes.Equal(after, before), info.ModTime(), modified)
		}
	}
}

func TestRemoveFailedArchiveRemovesLedger(t *testing.T) {
	for _, tc := range []struct {
		name       string
		downloadID string
	}{
		{"path only", ""},
		{"path and ID", "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := downloadsDaemon(t)
			const gameID = "skyrimse"
			if err := download.SaveLedger(gameID, []download.LedgerEntry{{ID: "failed", ArchiveRelPath: "failed.zip", Status: download.LedgerFailed}}); err != nil {
				t.Fatal(err)
			}
			if err := d.RemoveArchive(gameID, "failed.zip", tc.downloadID); err != nil {
				t.Fatal(err)
			}
			entries, err := download.LoadLedger(gameID)
			if err != nil || len(entries) != 0 {
				t.Fatalf("ledger after remove = %+v, %v", entries, err)
			}
			rows, err := d.ListArchives(gameID)
			if err != nil || len(rows) != 0 {
				t.Fatalf("rows after remove = %+v, %v", rows, err)
			}
		})
	}
}

func TestDeleteLandedArchiveWithStaleDownloadID(t *testing.T) {
	d := downloadsDaemon(t)
	const gameID = "skyrimse"
	archive := filepath.Join(config.DownloadsDir(gameID), "landed.zip")
	if err := os.MkdirAll(filepath.Dir(archive), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, []byte("zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := download.UpsertEntry(gameID, download.IndexEntry{Path: "landed.zip"}); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveArchive(gameID, "landed.zip", "finished-this-session"); err != nil {
		t.Fatalf("deleting a landed archive whose ledger entry is gone: %v", err)
	}
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Fatalf("archive still present: %v", err)
	}
	if err := d.RemoveArchive(gameID, "", "finished-this-session"); err == nil {
		t.Fatal("removing an unknown download by ID alone succeeded")
	}
}

func TestRemoveFailedDownloadWithoutArchivePath(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status download.LedgerStatus
	}{
		{"failed", download.LedgerFailed},
		{"cancelled", download.LedgerCancelled},
		{"empty status", ""},
		{"unknown status", "unrecognized"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := downloadsDaemon(t)
			const gameID = "skyrimse"
			if err := download.UpsertEntry(gameID, download.IndexEntry{Path: "unrelated.zip"}); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(config.DownloadsDir(gameID), "unrelated.zip")
			if err := os.WriteFile(archive, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			index := filepath.Join(config.DownloadsDir(gameID), "metadata.yaml")
			before, err := os.ReadFile(index)
			if err != nil {
				t.Fatal(err)
			}
			if err := download.SaveLedger(gameID, []download.LedgerEntry{{ID: "failed-no-path", Status: tc.status}}); err != nil {
				t.Fatal(err)
			}
			rows, err := d.ListArchives(gameID)
			if err != nil || len(rows) != 2 || rows[1].DownloadID != "failed-no-path" {
				t.Fatalf("rows before removal = %+v, %v", rows, err)
			}
			if err := d.RemoveArchive(gameID, "", "failed-no-path"); err != nil {
				t.Fatal(err)
			}
			entries, err := download.LoadLedger(gameID)
			if err != nil || len(entries) != 0 {
				t.Fatalf("ledger after removal = %+v, %v", entries, err)
			}
			rows, err = d.ListArchives(gameID)
			if err != nil || len(rows) != 1 || rows[0].ArchiveRelPath != "unrelated.zip" {
				t.Fatalf("rows after removal = %+v, %v", rows, err)
			}
			after, err := os.ReadFile(index)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("index after ID-only removal = %q, %v", after, err)
			}
			data, err := os.ReadFile(archive)
			if err != nil || string(data) != "keep" {
				t.Fatalf("unrelated archive = %q, %v", data, err)
			}
			var invalid *UnsafePathError
			if err := d.RemoveArchive(gameID, "", ""); !errors.As(err, &invalid) {
				t.Fatalf("removal without a path or ID = %v, want invalid argument", err)
			}
			var missing *download.DownloadNotFoundError
			if err := d.RemoveArchive(gameID, "", "missing"); !errors.As(err, &missing) {
				t.Fatalf("removal with unknown ID = %v, want not found", err)
			}
		})
	}
}

func TestRemoveActiveDownloadByIDRefused(t *testing.T) {
	d := downloadsDaemon(t)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	manager := download.NewManager(&downloadsFakeNexus{resolveEntered: entered, resolveRelease: release}, 1, d.svc.archives.managerHooks())
	d.mu.Lock()
	d.downloadMgr = manager
	d.mu.Unlock()
	activeID, _, err := d.StartDownload(lifecycleURI)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("active download did not reach the resolver")
	}
	queuedID, _, err := d.StartDownload(lifecycleURI)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{activeID, queuedID} {
		if err := d.RemoveArchive("skyrimse", "", id); !errors.Is(err, download.ErrArchiveDownloadBusy) {
			t.Errorf("RemoveArchive(%q) = %v, want busy", id, err)
		}
	}
	entries, err := download.LoadLedger("skyrimse")
	if err != nil || len(entries) != 2 {
		t.Errorf("ledger after refused removals = %+v, %v", entries, err)
	}
	once.Do(func() { close(release) })
	lifecycleWaitInactive(t, manager, activeID)
	lifecycleWaitInactive(t, manager, queuedID)
}

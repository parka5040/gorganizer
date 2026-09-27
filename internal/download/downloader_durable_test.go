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

type failingArchivePart struct {
	*os.File
	write  func([]byte) (int, error)
	sync   error
	close  error
	closes int
}

func (p *failingArchivePart) Write(b []byte) (int, error) {
	if p.write != nil {
		return p.write(b)
	}
	return p.File.Write(b)
}

func (p *failingArchivePart) Sync() error {
	if p.sync != nil {
		return p.sync
	}
	return p.File.Sync()
}

func (p *failingArchivePart) Close() error {
	p.closes++
	err := p.File.Close()
	if p.close != nil {
		return p.close
	}
	return err
}

// durableDownloadManager builds a pipeline with an in-memory response and landing counter.
func durableDownloadManager(respond destinationTransport, landed *int) (*Manager, *Download) {
	m := &Manager{
		nexus:      destinationResolver{filename: "archive.zip", url: "https://cdn.example/archive.zip"},
		httpClient: &http.Client{Transport: respond},
		hooks: ManagerHooks{OnArchiveLanded: func(DownloadSnapshot, string, ArchiveSidecar) {
			(*landed)++
		}},
	}
	return m, &Download{ID: "dl-00000000-0000-4000-8000-000000000001", GameID: "skyrimse", NXMURI: pipelineURI}
}

// downloadPartPaths returns the archive and part locations for a test download.
func downloadPartPaths() (string, string) {
	archive := filepath.Join(config.DownloadsDir("skyrimse"), "7_Example", "archive.zip")
	return archive, PartPath(archive)
}

// seedDownloadPart creates a partial archive in the isolated Downloads folder.
func seedDownloadPart(t *testing.T, data string) {
	t.Helper()
	_, part := downloadPartPaths()
	if err := os.MkdirAll(filepath.Dir(part), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(part, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

// assertDownloadDidNotLand checks the failed ledger, retained part and absent archive.
func assertDownloadDidNotLand(t *testing.T, m *Manager, dl *Download, landed int, partContent string) {
	t.Helper()
	state := m.snapshot(dl)
	if state.Status != StatusFailed || landed != 0 {
		t.Fatalf("download = %+v, landed = %d", state, landed)
	}
	archive, part := downloadPartPaths()
	if _, err := os.Lstat(archive); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive unexpectedly exists: %v", err)
	}
	data, err := os.ReadFile(part)
	if err != nil || string(data) != partContent {
		t.Fatalf("part = %q, %v; want %q", data, err, partContent)
	}
	entries, err := LoadLedger(dl.GameID)
	if err != nil || len(entries) != 1 || entries[0].Status != LedgerFailed {
		t.Fatalf("retryable ledger = %+v, %v", entries, err)
	}
}

// TestDownloadSyncAndCloseFailure checks that failed flushes and closes cannot publish a download.
func TestDownloadSyncAndCloseFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		sync bool
	}{
		{name: "sync", sync: true},
		{name: "close"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolatedDownloadRoot(t)
			landed := 0
			m, dl := durableDownloadManager(destinationTransport(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, ContentLength: 7, Body: io.NopCloser(strings.NewReader("archive")), Header: make(http.Header), Request: req}, nil
			}), &landed)
			cause := errors.New("injected disk failure")
			var part *failingArchivePart
			m.openPart = func(path, rel string, appendData bool) (archivePart, error) {
				file, err := openArchivePart(path, rel, appendData)
				if err != nil {
					return nil, err
				}
				part = &failingArchivePart{File: file}
				if tc.sync {
					part.sync = cause
				} else {
					part.close = cause
				}
				return part, nil
			}
			m.runPipeline(context.Background(), dl)
			assertDownloadDidNotLand(t, m, dl, landed, "archive")
			if part == nil || part.closes != 1 {
				t.Fatalf("part closes = %v, want 1", part)
			}
			if got := m.snapshot(dl).Error; got != "The download could not be saved completely. Check free space and choose Retry." {
				t.Fatalf("download error = %q", got)
			}
		})
	}
}

// TestShortWriteFailsDownload checks that a nil-error short write leaves a retryable part.
func TestShortWriteFailsDownload(t *testing.T) {
	isolatedDownloadRoot(t)
	landed := 0
	m, dl := durableDownloadManager(destinationTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, ContentLength: 7, Body: io.NopCloser(strings.NewReader("archive")), Header: make(http.Header), Request: req}, nil
	}), &landed)
	m.openPart = func(path, rel string, appendData bool) (archivePart, error) {
		file, err := openArchivePart(path, rel, appendData)
		if err != nil {
			return nil, err
		}
		return &failingArchivePart{File: file, write: func(b []byte) (int, error) { return file.Write(b[:2]) }}, nil
	}
	m.runPipeline(context.Background(), dl)
	assertDownloadDidNotLand(t, m, dl, landed, "ar")
	if got := m.snapshot(dl).Error; got != "The download could not be saved completely. Check free space and choose Retry." {
		t.Fatalf("download error = %q", got)
	}
}

// TestLandedPartSizeMustMatchProgress checks the part's actual size before renaming.
func TestLandedPartSizeMustMatchProgress(t *testing.T) {
	isolatedDownloadRoot(t)
	landed := 0
	m, dl := durableDownloadManager(destinationTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, ContentLength: 7, Body: io.NopCloser(strings.NewReader("archive")), Header: make(http.Header), Request: req}, nil
	}), &landed)
	m.openPart = func(path, rel string, appendData bool) (archivePart, error) {
		file, err := openArchivePart(path, rel, appendData)
		if err != nil {
			return nil, err
		}
		return &failingArchivePart{File: file, write: func(b []byte) (int, error) { return len(b), nil }}, nil
	}
	m.runPipeline(context.Background(), dl)
	assertDownloadDidNotLand(t, m, dl, landed, "")
	if !strings.Contains(m.snapshot(dl).Error, "incomplete archive part") {
		t.Fatalf("download error = %q", m.snapshot(dl).Error)
	}
}

// TestResumeRejectsWrongRange refuses invalid partial responses without changing the part.
func TestResumeRejectsWrongRange(t *testing.T) {
	for _, tc := range []struct {
		name, contentRange string
		contentLength      int64
	}{
		{name: "wrong start", contentRange: "bytes 2-9/10"},
		{name: "missing range"},
		{name: "short range", contentRange: "bytes 4-8/10"},
		{name: "malformed range", contentRange: "bytes 4-x/10"},
		{name: "end before start", contentRange: "bytes 4-3/*"},
		{name: "length mismatch", contentRange: "bytes 4-9/*", contentLength: 5},
		{name: "unknown total overflow", contentRange: "bytes 4-9223372036854775807/*"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolatedDownloadRoot(t)
			seedDownloadPart(t, "1234")
			landed := 0
			calls := 0
			m, dl := durableDownloadManager(destinationTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					if got := req.Header.Get("Range"); got != "bytes=4-" {
						t.Errorf("Range = %q, want bytes=4-", got)
					}
					h := make(http.Header)
					if tc.contentRange != "" {
						h.Set("Content-Range", tc.contentRange)
					}
					return &http.Response{StatusCode: http.StatusPartialContent, ContentLength: tc.contentLength, Body: io.NopCloser(strings.NewReader("567890")), Header: h, Request: req}, nil
				}
				if req.Header.Get("Range") != "" {
					t.Errorf("retry sent Range = %q", req.Header.Get("Range"))
				}
				return &http.Response{StatusCode: http.StatusOK, ContentLength: 6, Body: io.NopCloser(strings.NewReader("fresh!")), Header: make(http.Header), Request: req}, nil
			}), &landed)
			m.runPipeline(context.Background(), dl)
			assertDownloadDidNotLand(t, m, dl, landed, "1234")
			entries, err := LoadLedger(dl.GameID)
			if err != nil || entries[0].BytesDone != 0 {
				t.Fatalf("failed range ledger = %+v, %v", entries, err)
			}
			if _, err := m.RetryDownload(dl.ID, []string{dl.GameID}); err != nil {
				t.Fatal(err)
			}
			if len(m.queued) != 1 || m.queued[0].BytesDownloaded != 0 {
				t.Fatalf("queued retry = %+v", m.queued)
			}
			m.runPipeline(context.Background(), m.queued[0])
			archive, _ := downloadPartPaths()
			data, err := os.ReadFile(archive)
			if err != nil || string(data) != "fresh!" || landed != 1 || calls != 2 {
				t.Fatalf("restarted archive = %q, %v; landed = %d, requests = %d", data, err, landed, calls)
			}
		})
	}
}

// TestResumeHonoursRangeTotal checks resumed downloads against range totals and response lengths.
func TestResumeHonoursRangeTotal(t *testing.T) {
	for _, tc := range []struct {
		name, contentRange string
		contentLength      int64
	}{
		{name: "numeric total", contentRange: "bytes 4-9/10", contentLength: -1},
		{name: "unknown total", contentRange: "bytes 4-9/*", contentLength: 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolatedDownloadRoot(t)
			seedDownloadPart(t, "1234")
			landed := 0
			m, dl := durableDownloadManager(destinationTransport(func(req *http.Request) (*http.Response, error) {
				if req.Header.Get("Range") != "bytes=4-" {
					t.Errorf("Range = %q", req.Header.Get("Range"))
				}
				h := make(http.Header)
				h.Set("Content-Range", tc.contentRange)
				return &http.Response{StatusCode: http.StatusPartialContent, ContentLength: tc.contentLength, Body: io.NopCloser(strings.NewReader("567890")), Header: h, Request: req}, nil
			}), &landed)
			m.runPipeline(context.Background(), dl)
			archive, _ := downloadPartPaths()
			data, err := os.ReadFile(archive)
			state := m.snapshot(dl)
			if err != nil || string(data) != "1234567890" || state.BytesTotal != 10 || state.BytesDownloaded != 10 || state.Status != StatusDownloaded || landed != 1 {
				t.Fatalf("archive = %q, %v; download = %+v; landed = %d", data, err, state, landed)
			}
		})
	}
}

// TestRangeTotalDetectsTruncatedBody checks that the range total overrides unknown response length.
func TestRangeTotalDetectsTruncatedBody(t *testing.T) {
	isolatedDownloadRoot(t)
	seedDownloadPart(t, "1234")
	landed := 0
	m, dl := durableDownloadManager(destinationTransport(func(req *http.Request) (*http.Response, error) {
		h := make(http.Header)
		h.Set("Content-Range", "bytes 4-9/10")
		return &http.Response{StatusCode: http.StatusPartialContent, ContentLength: -1, Body: io.NopCloser(strings.NewReader("56")), Header: h, Request: req}, nil
	}), &landed)
	m.runPipeline(context.Background(), dl)
	assertDownloadDidNotLand(t, m, dl, landed, "123456")
	if state := m.snapshot(dl); state.BytesTotal != 10 || !strings.Contains(state.Error, "incomplete") {
		t.Fatalf("download = %+v", state)
	}
}

// TestUnknownTotalRangeEnforcesAdvertisedEnd keeps incomplete and oversized resumed parts retryable.
func TestUnknownTotalRangeEnforcesAdvertisedEnd(t *testing.T) {
	for _, tc := range []struct {
		name, body, wantPart, wantError string
	}{
		{name: "short", body: "4", wantPart: "abc4", wantError: "incomplete"},
		{name: "overrun", body: "4567", wantPart: "abc", wantError: "exceeds expected total"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolatedDownloadRoot(t)
			seedDownloadPart(t, "abc")
			landed := 0
			m, dl := durableDownloadManager(destinationTransport(func(req *http.Request) (*http.Response, error) {
				if got := req.Header.Get("Range"); got != "bytes=3-" {
					t.Errorf("Range = %q, want bytes=3-", got)
				}
				h := make(http.Header)
				h.Set("Content-Range", "bytes 3-5/*")
				return &http.Response{StatusCode: http.StatusPartialContent, ContentLength: -1, TransferEncoding: []string{"chunked"}, Body: io.NopCloser(strings.NewReader(tc.body)), Header: h, Request: req}, nil
			}), &landed)
			m.runPipeline(context.Background(), dl)
			assertDownloadDidNotLand(t, m, dl, landed, tc.wantPart)
			state := m.snapshot(dl)
			if state.BytesTotal != 6 || !strings.Contains(state.Error, tc.wantError) {
				t.Fatalf("download = %+v", state)
			}
			if _, err := m.RetryDownload(dl.ID, []string{dl.GameID}); err != nil || len(m.queued) != 1 {
				t.Fatalf("retry = %+v, %v", m.queued, err)
			}
		})
	}
}

// TestUnknownTotalRangeCompletes lands a resumed part with the exact advertised end.
func TestUnknownTotalRangeCompletes(t *testing.T) {
	isolatedDownloadRoot(t)
	seedDownloadPart(t, "abc")
	landed := 0
	m, dl := durableDownloadManager(destinationTransport(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Range"); got != "bytes=3-" {
			t.Errorf("Range = %q, want bytes=3-", got)
		}
		h := make(http.Header)
		h.Set("Content-Range", "bytes 3-5/*")
		return &http.Response{StatusCode: http.StatusPartialContent, ContentLength: -1, TransferEncoding: []string{"chunked"}, Body: io.NopCloser(strings.NewReader("456")), Header: h, Request: req}, nil
	}), &landed)
	m.runPipeline(context.Background(), dl)
	archive, part := downloadPartPaths()
	data, err := os.ReadFile(archive)
	state := m.snapshot(dl)
	if err != nil || string(data) != "abc456" || state.Status != StatusDownloaded || state.BytesDownloaded != 6 || state.BytesTotal != 6 || landed != 1 {
		t.Fatalf("archive = %q, %v; download = %+v; landed = %d", data, err, state, landed)
	}
	if _, err := os.Lstat(part); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("part remains: %v", err)
	}
}

// TestServerIgnoringRangeRestarts checks that 200 and 416 responses restart from zero.
func TestServerIgnoringRangeRestarts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		calls  int
	}{
		{name: "ignored range", status: http.StatusOK, calls: 1},
		{name: "unsatisfiable range", status: http.StatusRequestedRangeNotSatisfiable, calls: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolatedDownloadRoot(t)
			seedDownloadPart(t, "old data")
			landed := 0
			calls := 0
			m, dl := durableDownloadManager(destinationTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 && req.Header.Get("Range") != "bytes=8-" {
					t.Errorf("first Range = %q", req.Header.Get("Range"))
				}
				if calls > 1 && req.Header.Get("Range") != "" {
					t.Errorf("second Range = %q", req.Header.Get("Range"))
				}
				if tc.status == http.StatusRequestedRangeNotSatisfiable && calls == 1 {
					return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header), Request: req}, nil
				}
				return &http.Response{StatusCode: http.StatusOK, ContentLength: 5, Body: io.NopCloser(strings.NewReader("fresh")), Header: make(http.Header), Request: req}, nil
			}), &landed)
			m.runPipeline(context.Background(), dl)
			archive, _ := downloadPartPaths()
			data, err := os.ReadFile(archive)
			if err != nil || string(data) != "fresh" || landed != 1 || calls != tc.calls {
				t.Fatalf("archive = %q, %v; landed = %d, requests = %d", data, err, landed, calls)
			}
		})
	}
}

// TestRepeatedRangeRefusalStops checks that a second 416 cannot truncate the part.
func TestRepeatedRangeRefusalStops(t *testing.T) {
	isolatedDownloadRoot(t)
	seedDownloadPart(t, "old")
	landed := 0
	calls := 0
	m, dl := durableDownloadManager(destinationTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 2 && req.Header.Get("Range") != "" {
			t.Errorf("retry Range = %q", req.Header.Get("Range"))
		}
		return &http.Response{StatusCode: http.StatusRequestedRangeNotSatisfiable, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header), Request: req}, nil
	}), &landed)
	m.runPipeline(context.Background(), dl)
	assertDownloadDidNotLand(t, m, dl, landed, "old")
	if calls != 2 {
		t.Fatalf("requests = %d, want 2", calls)
	}
}

// TestTruncatedBodyNeverLands checks that EOF before the declared length retains the part.
func TestTruncatedBodyNeverLands(t *testing.T) {
	isolatedDownloadRoot(t)
	landed := 0
	m, dl := durableDownloadManager(destinationTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, ContentLength: 12, Body: io.NopCloser(strings.NewReader("short")), Header: make(http.Header), Request: req}, nil
	}), &landed)
	m.runPipeline(context.Background(), dl)
	assertDownloadDidNotLand(t, m, dl, landed, "short")
	if !strings.Contains(m.snapshot(dl).Error, "incomplete") {
		t.Fatalf("error = %q", m.snapshot(dl).Error)
	}
}

// TestFailedStreamNeverLands checks that failed writes cannot trigger archive installation.
func TestFailedStreamNeverLands(t *testing.T) {
	isolatedDownloadRoot(t)
	landed := 0
	m, dl := durableDownloadManager(destinationTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, ContentLength: 10, Body: io.NopCloser(strings.NewReader("unfinished")), Header: make(http.Header), Request: req}, nil
	}), &landed)
	cause := errors.New("injected write failure")
	m.openPart = func(path, rel string, appendData bool) (archivePart, error) {
		file, err := openArchivePart(path, rel, appendData)
		if err != nil {
			return nil, err
		}
		return &failingArchivePart{File: file, write: func(b []byte) (int, error) {
			count, err := file.Write(b[:3])
			if err != nil {
				return count, err
			}
			return count, cause
		}}, nil
	}
	m.runPipeline(context.Background(), dl)
	assertDownloadDidNotLand(t, m, dl, landed, "unf")
	if got := m.snapshot(dl).Error; got != "The download could not be saved completely. Check free space and choose Retry." {
		t.Fatalf("download error = %q", got)
	}
}

// TestUnexpectedPartialResponseAndOverrunNeverLand checks unsolicited ranges and excessive bodies.
func TestUnexpectedPartialResponseAndOverrunNeverLand(t *testing.T) {
	for _, tc := range []struct {
		name, body, rangeHeader string
		status                  int
		contentLength           int64
		wantPart                string
	}{
		{name: "unexpected 206", status: http.StatusPartialContent, body: "archive", rangeHeader: "bytes 0-6/7", wantPart: "keep"},
		{name: "body overrun", status: http.StatusOK, body: "archive", contentLength: 3, wantPart: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolatedDownloadRoot(t)
			if tc.wantPart != "" {
				seedDownloadPart(t, tc.wantPart)
			}
			landed := 0
			m, dl := durableDownloadManager(destinationTransport(func(req *http.Request) (*http.Response, error) {
				h := make(http.Header)
				if tc.rangeHeader != "" {
					h.Set("Content-Range", tc.rangeHeader)
				}
				return &http.Response{StatusCode: tc.status, ContentLength: tc.contentLength, Body: io.NopCloser(strings.NewReader(tc.body)), Header: h, Request: req}, nil
			}), &landed)
			if tc.wantPart != "" {
				dl.ArchiveRel = "7_Example/archive.zip"
				dl.BytesDownloaded = 0
			}
			m.runPipeline(context.Background(), dl)
			assertDownloadDidNotLand(t, m, dl, landed, tc.wantPart)
		})
	}
}

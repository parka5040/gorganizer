package download

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
)

const pipelineURI = "nxm://skyrimspecialedition/mods/7/files/8"

type slowPipelineBody struct {
	started   chan struct{}
	release   chan struct{}
	remaining int
	startedMu sync.Once
}

func (b *slowPipelineBody) Read(p []byte) (int, error) {
	b.startedMu.Do(func() { close(b.started) })
	<-b.release
	if b.remaining == 0 {
		return 0, io.EOF
	}
	time.Sleep(time.Millisecond)
	b.remaining--
	return copy(p, bytes.Repeat([]byte("a"), 1024)), nil
}

func (b *slowPipelineBody) Close() error { return nil }

// waitDownloadSnapshot waits for a download to reach a terminal status.
func waitDownloadSnapshot(t *testing.T, results <-chan DownloadSnapshot) DownloadSnapshot {
	t.Helper()
	select {
	case s := <-results:
		return s
	case <-time.After(10 * time.Second):
		t.Fatal("download did not finish")
		return DownloadSnapshot{}
	}
}

// TestPipelineSnapshotsRaceFree reads live snapshots while a real pipeline publishes progress.
func TestPipelineSnapshotsRaceFree(t *testing.T) {
	isolatedDownloadRoot(t)
	body := &slowPipelineBody{started: make(chan struct{}), release: make(chan struct{}), remaining: 256}
	finished := make(chan DownloadSnapshot, 1)
	m := NewManager(destinationResolver{filename: "archive.zip", url: "https://cdn.example/archive.zip"}, 1, ManagerHooks{
		OnDownloadProgress: func(s DownloadSnapshot) {
			if s.Status == StatusDownloaded || s.Status == StatusFailed {
				finished <- s
			}
		},
	})
	defer m.Stop()
	m.httpClient = &http.Client{Transport: destinationTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, ContentLength: 256 * 1024, Body: body, Header: make(http.Header), Request: req}, nil
	})}
	id, _, err := m.StartDownload(pipelineURI)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-body.started:
	case <-time.After(10 * time.Second):
		t.Fatal("download did not start")
	}
	archive := filepath.Join(config.DownloadsDir("skyrimse"), "7_Example", "archive.zip")
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = m.GetProgress(id)
				m.ActiveDownloadIDByArchive(archive)
				m.ActiveDownloadIDByNXM(pipelineURI)
				m.mu.RLock()
				for _, dl := range m.active {
					_ = snapshotOf(dl)
				}
				for _, dl := range m.queued {
					_ = snapshotOf(dl)
				}
				m.mu.RUnlock()
			}
		}()
	}
	close(body.release)
	s := waitDownloadSnapshot(t, finished)
	close(stop)
	readers.Wait()
	if s.ID != id || s.Status != StatusDownloaded || s.BytesDownloaded != 256*1024 || s.ModName != "Example" {
		t.Fatalf("final snapshot = %+v", s)
	}
	data, err := os.ReadFile(archive)
	if err != nil || len(data) != 256*1024 {
		t.Fatalf("downloaded archive size = %d, %v", len(data), err)
	}
}

// TestProgressHookCanReenterManager checks that queue notifications never hold the manager lock.
func TestProgressHookCanReenterManager(t *testing.T) {
	isolatedDownloadRoot(t)
	body := &slowPipelineBody{started: make(chan struct{}), release: make(chan struct{}), remaining: 1}
	finished := make(chan DownloadSnapshot, 3)
	reentered := make(chan string, 16)
	var manager *Manager
	var calls atomic.Int32
	var promoting, writerChecked atomic.Bool
	manager = NewManager(destinationResolver{filename: "archive.zip", url: "https://cdn.example/archive.zip"}, 1, ManagerHooks{
		OnDownloadProgress: func(s DownloadSnapshot) {
			if s.Status == StatusQueued && promoting.Load() && writerChecked.CompareAndSwap(false, true) {
				writerDone := make(chan struct{})
				go func() {
					manager.mu.Lock()
					manager.mu.Unlock()
					close(writerDone)
				}()
				select {
				case <-writerDone:
				case <-time.After(3 * time.Second):
					reentered <- "queue hook held the manager lock"
				}
			}
			if _, err := manager.GetProgress(s.ID); err != nil && s.Status != StatusCancelled {
				reentered <- err.Error()
			}
			if s.Status == StatusDownloaded || s.Status == StatusFailed {
				promoting.Store(true)
				finished <- s
			}
		},
	})
	defer manager.Stop()
	manager.httpClient = &http.Client{Transport: destinationTransport(func(req *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header), Request: req}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("archive data")), Header: make(http.Header), Request: req}, nil
	})}
	if _, _, err := manager.StartDownload(pipelineURI); err != nil {
		t.Fatal(err)
	}
	select {
	case <-body.started:
	case <-time.After(10 * time.Second):
		t.Fatal("first download did not start")
	}
	for range 2 {
		if _, _, err := manager.StartDownload(pipelineURI); err != nil {
			t.Fatal(err)
		}
	}
	close(body.release)
	for range 3 {
		if s := waitDownloadSnapshot(t, finished); s.Status != StatusDownloaded {
			t.Fatalf("download = %+v", s)
		}
	}
	if !writerChecked.Load() {
		t.Fatal("queue hook did not exercise lock reentry")
	}
	select {
	case err := <-reentered:
		t.Fatal(err)
	default:
	}
}

// TestConcurrentRetryHasOneWriter checks that one retry owns a failed ledger entry.
func TestConcurrentRetryHasOneWriter(t *testing.T) {
	isolatedDownloadRoot(t)
	const gameID = "skyrimse"
	if err := SaveLedger(gameID, []LedgerEntry{{ID: "retry", GameID: gameID, GameSlug: "skyrimspecialedition", ModID: 7, FileID: 8, ArchiveRelPath: "7_Example/archive.zip", NXMURI: pipelineURI, Status: LedgerFailed}}); err != nil {
		t.Fatal(err)
	}
	body := &slowPipelineBody{started: make(chan struct{}), release: make(chan struct{}), remaining: 1}
	finished := make(chan DownloadSnapshot, 2)
	var calls atomic.Int32
	m := NewManager(destinationResolver{filename: "archive.zip", url: "https://cdn.example/archive.zip"}, 2, ManagerHooks{
		OnDownloadProgress: func(s DownloadSnapshot) {
			if s.Status == StatusDownloaded || s.Status == StatusFailed {
				finished <- s
			}
		},
	})
	defer m.Stop()
	m.httpClient = &http.Client{Transport: destinationTransport(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header), Request: req}, nil
	})}
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := m.RetryDownload("retry", []string{gameID})
			results <- err
		}()
	}
	close(start)
	succeeded := 0
	for range 2 {
		if err := <-results; err != nil {
			if !errors.Is(err, ErrRetryInProgress) {
				t.Fatalf("retry error = %v", err)
			}
		} else {
			succeeded++
		}
	}
	if succeeded == 0 {
		t.Fatal("neither retry was admitted")
	}
	select {
	case <-body.started:
	case <-time.After(10 * time.Second):
		t.Fatal("retry did not start")
	}
	close(body.release)
	if s := waitDownloadSnapshot(t, finished); s.Status != StatusDownloaded {
		t.Fatalf("retried download = %+v", s)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("HTTP bodies consumed = %d, want 1", got)
	}
}

// TestSameDestinationSerializes checks that a second pipeline cannot replace an owned archive.
func TestSameDestinationSerializes(t *testing.T) {
	isolatedDownloadRoot(t)
	body := &slowPipelineBody{started: make(chan struct{}), release: make(chan struct{}), remaining: 2}
	failed := make(chan DownloadSnapshot, 1)
	finished := make(chan DownloadSnapshot, 1)
	var calls atomic.Int32
	m := NewManager(destinationResolver{filename: "archive.zip", url: "https://cdn.example/archive.zip"}, 2, ManagerHooks{
		OnDownloadProgress: func(s DownloadSnapshot) {
			if s.Status == StatusFailed {
				failed <- s
			}
			if s.Status == StatusDownloaded {
				finished <- s
			}
		},
	})
	defer m.Stop()
	m.httpClient = &http.Client{Transport: destinationTransport(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header), Request: req}, nil
	})}
	first, _, err := m.StartDownload(pipelineURI)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-body.started:
	case <-time.After(10 * time.Second):
		t.Fatal("first download did not start")
	}
	second, _, err := m.StartDownload(pipelineURI)
	if err != nil {
		t.Fatal(err)
	}
	s := waitDownloadSnapshot(t, failed)
	if s.ID != second || !strings.Contains(s.Error, "this archive is already being downloaded") {
		t.Fatalf("competing download = %+v", s)
	}
	close(body.release)
	if s := waitDownloadSnapshot(t, finished); s.ID != first {
		t.Fatalf("owner download = %+v", s)
	}
	path := filepath.Join(config.DownloadsDir("skyrimse"), "7_Example", "archive.zip")
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, bytes.Repeat([]byte("a"), 2048)) || calls.Load() != 1 {
		t.Fatalf("archive = %q, %v; HTTP bodies = %d", data, err, calls.Load())
	}
}

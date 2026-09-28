package download

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/config"
)

type swappingResolver struct {
	name    string
	entered chan struct{}
	release chan struct{}
	callsMu sync.Mutex
	calls   []string
}

func (r *swappingResolver) record(method string) {
	r.callsMu.Lock()
	r.calls = append(r.calls, method)
	r.callsMu.Unlock()
}

func (r *swappingResolver) GetModInfo(string, int) (*NexusModInfo, error) {
	r.record("mod")
	if r.entered != nil {
		close(r.entered)
		<-r.release
	}
	return &NexusModInfo{Name: r.name}, nil
}

func (r *swappingResolver) GetFileDetails(string, int, int) (*NexusFileDetails, error) {
	r.record("file")
	return &NexusFileDetails{FileName: r.name + ".zip"}, nil
}

func (r *swappingResolver) ResolveDownloadURL(*NXMLink) (string, error) {
	r.record("url")
	return "https://cdn.example/" + r.name + ".zip", nil
}

func TestSetResolverAffectsOnlyNewPipelines(t *testing.T) {
	isolatedDownloadRoot(t)
	old := &swappingResolver{name: "Old", entered: make(chan struct{}), release: make(chan struct{})}
	newResolver := &swappingResolver{name: "New"}
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(old.release) }) })
	finished := make(chan DownloadSnapshot, 2)
	m := NewManagerWithClient(old, 2, ManagerHooks{OnDownloadProgress: func(s DownloadSnapshot) {
		if s.Status == StatusDownloaded || s.Status == StatusFailed {
			finished <- s
		}
	}}, &http.Client{Transport: destinationTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(req.URL.Path)), Header: make(http.Header), Request: req}, nil
	})})
	defer m.Stop()
	oldID, _, err := m.StartDownload(pipelineURI)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-old.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("old pipeline did not enter the resolver")
	}
	m.SetResolver(newResolver)
	newID, _, err := m.StartDownload("nxm://skyrimspecialedition/mods/9/files/10")
	if err != nil {
		t.Fatal(err)
	}
	release.Do(func() { close(old.release) })
	seen := make(map[string]DownloadSnapshot)
	for range 2 {
		s := waitDownloadSnapshot(t, finished)
		seen[s.ID] = s
	}
	for _, id := range []string{oldID, newID} {
		if seen[id].Status != StatusDownloaded {
			t.Fatalf("download %s = %+v", id, seen[id])
		}
	}
	for _, tc := range []struct {
		resolver *swappingResolver
		modID    string
	}{
		{old, "7"}, {newResolver, "9"},
	} {
		tc.resolver.callsMu.Lock()
		calls := append([]string(nil), tc.resolver.calls...)
		tc.resolver.callsMu.Unlock()
		if !strings.Contains(strings.Join(calls, ","), "mod,file,url") {
			t.Fatalf("%s resolver calls = %v", tc.resolver.name, calls)
		}
		path := filepath.Join(config.DownloadsDir("skyrimse"), tc.modID+"_"+tc.resolver.name, tc.resolver.name+".zip")
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, []byte("/"+tc.resolver.name+".zip")) {
			t.Fatalf("archive %s = %q, %v", path, data, err)
		}
	}
}

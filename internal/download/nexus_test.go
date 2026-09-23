package download

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestResolveDownloadURLEscapesKey verifies Nexus keys are query escaped.
func TestResolveDownloadURLEscapesKey(t *testing.T) {
	key := "key&fragment#with space"
	var rawQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[{"URI":"https://cdn.example/archive"}]`))
	}))
	defer server.Close()

	client := &NexusClient{
		baseURL:    server.URL,
		httpClient: server.Client(),
	}
	_, err := client.ResolveDownloadURL(&NXMLink{
		GameSlug: "skyrimse",
		ModID:    1,
		FileID:   2,
		Key:      key,
		Expires:  42,
	})
	if err != nil {
		t.Fatalf("ResolveDownloadURL() error = %v", err)
	}
	want := "key=" + url.QueryEscape(key) + "&expires=42"
	if rawQuery != want {
		t.Fatalf("RawQuery = %q, want %q", rawQuery, want)
	}
}

package download

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type nexusErrorTransport func(*http.Request) (*http.Response, error)

// RoundTrip returns a bounded-test Nexus response.
func (f nexusErrorTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// TestNexusErrorBodiesAreBoundedAndRedacted verifies every Nexus error path bounds bodies and strips link credentials.
func TestNexusErrorBodiesAreBoundedAndRedacted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const apiKey = "API-SECRET"
	const linkKey = "LINK-SECRET"
	body := "key=" + linkKey + " expires=12345 user_id=5678 API-SECRET https://cdn.example/asset?token=CDN-SECRET " + strings.Repeat("x", 8192) + "AFTER-LIMIT"
	calls := []struct {
		name string
		call func(*NexusClient) error
	}{
		{name: "ResolveDownloadURL", call: func(c *NexusClient) error {
			_, err := c.ResolveDownloadURL(&NXMLink{GameSlug: "test", ModID: 1, FileID: 2, Key: linkKey, Expires: 12345})
			return err
		}},
		{name: "ResolveDownloadURLByID", call: func(c *NexusClient) error { _, err := c.ResolveDownloadURLByID("test", 1, 2); return err }},
		{name: "GetModInfo", call: func(c *NexusClient) error { _, err := c.GetModInfo("test", 1); return err }},
		{name: "GetFileDetails", call: func(c *NexusClient) error { _, err := c.GetFileDetails("test", 1, 2); return err }},
		{name: "ListModFiles", call: func(c *NexusClient) error { _, err := c.ListModFiles("test", 1); return err }},
		{name: "ValidateAPIKey", call: func(c *NexusClient) error { return c.ValidateAPIKey(context.Background()) }},
		{name: "ValidateUser", call: func(c *NexusClient) error { _, err := c.ValidateUser(context.Background()); return err }},
		{name: "GetModFile", call: func(c *NexusClient) error { _, err := c.GetModFile(context.Background(), "test", "1"); return err }},
		{name: "GetModFileDependencyRanges", call: func(c *NexusClient) error {
			_, err := c.GetModFileDependencyRanges(context.Background(), "1")
			return err
		}},
	}
	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			reader := strings.NewReader(body)
			client := NewNexusClient(apiKey)
			client.httpClient = &http.Client{Transport: nexusErrorTransport(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(reader), Header: make(http.Header)}, nil
			})}
			client.baseURL = "https://api.example"
			err := tc.call(client)
			if err == nil {
				t.Fatal("expected Nexus error")
			}
			for _, secret := range []string{apiKey, linkKey, "expires=12345", "user_id=5678", "CDN-SECRET", "AFTER-LIMIT"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("Nexus error exposes %q: %s", secret, err)
				}
			}
			if n := len(body) - reader.Len(); n > 4096 {
				t.Fatalf("read %d error-body bytes, want at most 4096", n)
			}
		})
	}
	t.Run("link key outside parameter", func(t *testing.T) {
		client := NewNexusClient(apiKey)
		client.baseURL = "https://api.example"
		client.httpClient = &http.Client{Transport: nexusErrorTransport(func(req *http.Request) (*http.Response, error) {
			if got := req.URL.Query().Get("key"); got != linkKey {
				t.Errorf("query key = %q, want %q", got, linkKey)
			}
			return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader("link key: " + linkKey)), Header: make(http.Header)}, nil
		})}
		_, err := client.ResolveDownloadURL(&NXMLink{GameSlug: "test", ModID: 1, FileID: 2, Key: linkKey, Expires: 12345})
		if err == nil || strings.Contains(err.Error(), linkKey) {
			t.Fatalf("link credential in error: %v", err)
		}
	})
	t.Run("transport error", func(t *testing.T) {
		client := NewNexusClient(apiKey)
		client.baseURL = "https://api.example"
		client.httpClient = &http.Client{Transport: nexusErrorTransport(func(req *http.Request) (*http.Response, error) {
			return nil, &url.Error{Op: "Get", URL: req.URL.String(), Err: io.ErrUnexpectedEOF}
		})}
		_, err := client.ResolveDownloadURL(&NXMLink{GameSlug: "test", ModID: 1, FileID: 2, Key: linkKey, Expires: 12345})
		if err == nil || strings.Contains(err.Error(), linkKey) || strings.Contains(err.Error(), "expires=12345") {
			t.Fatalf("link credential in transport error: %v", err)
		}
	})
}

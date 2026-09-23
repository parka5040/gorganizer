package httpx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNewClientRefusesHTTPRedirect verifies HTTPS-only redirects are enforced.
func TestNewClientRefusesHTTPRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/destination", http.StatusFound)
	}))
	defer server.Close()

	client := NewClient(Options{RequireHTTPS: true})
	_, err := client.Get(server.URL)
	if err == nil {
		t.Fatal("Get() error = nil, want redirect refusal")
	}
	if !strings.Contains(err.Error(), "non-https scheme") {
		t.Fatalf("Get() error = %q, want scheme refusal", err)
	}
}

// TestNewClientStopsAfterMaxRedirects verifies redirect limits are enforced.
func TestNewClientStopsAfterMaxRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/middle", http.StatusFound)
		case "/middle":
			http.Redirect(w, r, "/final", http.StatusFound)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	client := NewClient(Options{MaxRedirects: 1})
	_, err := client.Get(server.URL + "/start")
	if err == nil {
		t.Fatal("Get() error = nil, want redirect limit")
	}
	if !strings.Contains(err.Error(), "stopped after 1 redirects") {
		t.Fatalf("Get() error = %q, want redirect limit", err)
	}
}

// TestNewClientFollowsHTTPSRedirect verifies HTTPS redirects are allowed.
func TestNewClientFollowsHTTPSRedirect(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	client := NewClient(Options{RequireHTTPS: true, MaxRedirects: 5})
	client.Transport = server.Client().Transport
	resp, err := client.Get(server.URL + "/start")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(body) != "ok" {
		t.Fatalf("body = %q, want %q", body, "ok")
	}
}

// TestNewClientLeavesZeroTimeoutUnset verifies zero timeout remains unset.
func TestNewClientLeavesZeroTimeoutUnset(t *testing.T) {
	client := NewClient(Options{})
	if client.Timeout != 0 {
		t.Fatalf("Timeout = %v, want 0", client.Timeout)
	}
}

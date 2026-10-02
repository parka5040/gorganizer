package httpx

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// TestAllowOriginChecksInitialAndRedirectedRequests rejects disallowed origins before sending.
func TestAllowOriginChecksInitialAndRedirectedRequests(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, r.URL.Query().Get("to"), http.StatusFound)
			return
		}
		io.WriteString(w, "ok")
	}))
	defer server.Close()
	client := NewClient(Options{AllowOrigin: func(u *url.URL) error {
		if u.Host == strings.TrimPrefix(server.URL, "http://") && u.User == nil {
			return nil
		}
		return fmt.Errorf("disallowed origin")
	}})
	if _, err := client.Get("http://outside.invalid/path"); err == nil || calls != 0 {
		t.Fatalf("initial origin reached transport: %d: %v", calls, err)
	}
	for i, destination := range []string{"http://outside.invalid/", "http://" + strings.Split(strings.TrimPrefix(server.URL, "http://"), ":")[0] + ":444/", "http://user@" + strings.TrimPrefix(server.URL, "http://") + "/"} {
		if _, err := client.Get(server.URL + "/redirect?to=" + url.QueryEscape(destination)); err == nil || calls != i+1 {
			t.Fatalf("redirect to %s reached transport: %d: %v", destination, calls, err)
		}
	}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if calls != 4 {
		t.Fatalf("allowed origin not reached: %d", calls)
	}
}

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestFixtureServerStatusModes checks latest-response statuses and retry headers.
func TestFixtureServerStatusModes(t *testing.T) {
	bundles, out := fixtureBundle(t)
	for _, tc := range []struct {
		mode string
		code int
	}{
		{"status403", http.StatusForbidden},
		{"status429", http.StatusTooManyRequests},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			state := t.TempDir()
			handler, err := newFixtureServer(bundles, state, tc.mode)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewTLSServer(handler)
			defer server.Close()
			response, err := server.Client().Get(server.URL + "/latest")
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != tc.code || response.Header.Get("Retry-After") != "600" {
				t.Fatalf("latest status = %d, retry = %q", response.StatusCode, response.Header.Get("Retry-After"))
			}
			log, err := os.ReadFile(filepath.Join(state, "requests.log"))
			if err != nil || !strings.Contains(string(log), "GET /latest "+strconv.Itoa(tc.code)+"\n") {
				t.Fatalf("request log = %q: %v", log, err)
			}
		})
	}
	if handler, err := newFixtureServer(out, t.TempDir(), "normal"); err != nil || handler.latest != "v0.0.9" {
		t.Fatalf("single-bundle directory: %+v: %v", handler, err)
	}
}

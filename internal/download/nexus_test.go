package download

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
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

// TestValidateUser verifies user validation decoding, key forwarding, and error mapping.
func TestValidateUser(t *testing.T) {
	const key = "test-key"
	oversizedUser := `{"user_id":1,"name":"` + strings.Repeat("a", 70*1024) + `"}`
	tests := []struct {
		name         string
		status       int
		body         string
		wantUser     *NexusUser
		wantErr      error
		wantErrMsg   string
		wantErrExact string
	}{
		{
			name:     "premium user",
			status:   http.StatusOK,
			body:     `{"user_id":12,"name":"Premium User","is_premium":true,"is_supporter":true}`,
			wantUser: &NexusUser{UserID: 12, Name: "Premium User", IsPremium: true, IsSupporter: true},
		},
		{
			name:     "non-premium user",
			status:   http.StatusOK,
			body:     `{"user_id":13,"name":"Regular User","is_premium":false,"is_supporter":false}`,
			wantUser: &NexusUser{UserID: 13, Name: "Regular User"},
		},
		{
			name:    "unauthorized key",
			status:  http.StatusUnauthorized,
			wantErr: ErrInvalidKey,
		},
		{
			name:    "forbidden key",
			status:  http.StatusForbidden,
			wantErr: ErrInvalidKey,
		},
		{
			name:       "server failure",
			status:     http.StatusInternalServerError,
			body:       "unavailable",
			wantErrMsg: "HTTP 500: unavailable",
		},
		{
			name:         "server failure body truncated to 1 KiB",
			status:       http.StatusBadGateway,
			body:         strings.Repeat("x", 4096),
			wantErrExact: "Nexus user validation failed: HTTP 502: " + strings.Repeat("x", 1024),
		},
		{
			name:       "oversized success body fails to decode",
			status:     http.StatusOK,
			body:       oversizedUser,
			wantErrMsg: "decoding Nexus user validation response",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_ = t.TempDir()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/users/validate.json" {
					t.Errorf("request = %s %s, want GET /v1/users/validate.json", r.Method, r.URL.Path)
					http.Error(w, "unexpected request", http.StatusBadRequest)
					return
				}
				if got := r.Header.Get("apikey"); got != key {
					t.Errorf("apikey header = %q, want %q", got, key)
					http.Error(w, "missing apikey", http.StatusBadRequest)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			client := NewNexusClient(key)
			client.baseURL = server.URL
			client.httpClient = server.Client()
			got, err := client.ValidateUser(context.Background())
			wantAnyErr := tc.wantErr != nil || tc.wantErrMsg != "" || tc.wantErrExact != ""
			if !wantAnyErr && err != nil {
				t.Fatalf("ValidateUser() error = %v", err)
			}
			if wantAnyErr && err == nil {
				t.Fatalf("ValidateUser() = %#v, want error", got)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("ValidateUser() error = %v, want errors.Is(_, %v)", err, tc.wantErr)
			}
			if tc.wantErrMsg != "" && !strings.Contains(err.Error(), tc.wantErrMsg) {
				t.Fatalf("ValidateUser() error = %q, want substring %q", err, tc.wantErrMsg)
			}
			if tc.wantErrExact != "" && err.Error() != tc.wantErrExact {
				t.Fatalf("ValidateUser() error length = %d, want exactly %d", len(err.Error()), len(tc.wantErrExact))
			}
			if tc.wantUser == nil {
				return
			}
			if got == nil || *got != *tc.wantUser {
				t.Fatalf("ValidateUser() = %#v, want %#v", got, tc.wantUser)
			}
		})
	}
}

// TestNexusClientErrorsRedactAPIKey verifies no Nexus method echoes the API key from an error body.
func TestNexusClientErrorsRedactAPIKey(t *testing.T) {
	const key = "s3cr3t-nexus-api-key"
	calls := []struct {
		name string
		call func(*NexusClient) error
	}{
		{name: "ValidateUser", call: func(c *NexusClient) error {
			_, err := c.ValidateUser(context.Background())
			return err
		}},
		{name: "ValidateAPIKey", call: func(c *NexusClient) error {
			return c.ValidateAPIKey(context.Background())
		}},
		{name: "ResolveDownloadURL", call: func(c *NexusClient) error {
			_, err := c.ResolveDownloadURL(&NXMLink{GameSlug: "skyrimse", ModID: 1, FileID: 2})
			return err
		}},
		{name: "ResolveDownloadURLByID", call: func(c *NexusClient) error {
			_, err := c.ResolveDownloadURLByID("skyrimse", 1, 2)
			return err
		}},
		{name: "GetModInfo", call: func(c *NexusClient) error {
			_, err := c.GetModInfo("skyrimse", 1)
			return err
		}},
		{name: "GetFileDetails", call: func(c *NexusClient) error {
			_, err := c.GetFileDetails("skyrimse", 1, 2)
			return err
		}},
		{name: "ListModFiles", call: func(c *NexusClient) error {
			_, err := c.ListModFiles("skyrimse", 1)
			return err
		}},
		{name: "GetModFile", call: func(c *NexusClient) error {
			_, err := c.GetModFile(context.Background(), "skyrimspecialedition", "1")
			return err
		}},
		{name: "GetModFileDependencyRanges", call: func(c *NexusClient) error {
			_, err := c.GetModFileDependencyRanges(context.Background(), "1")
			return err
		}},
	}

	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			_ = t.TempDir()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "bad key "+r.Header.Get("apikey")+" rejected", http.StatusInternalServerError)
			}))
			defer server.Close()

			client := NewNexusClient(key)
			client.baseURL = server.URL
			client.httpClient = server.Client()
			err := tc.call(client)
			if err == nil {
				t.Fatal("error = nil, want HTTP 500 error")
			}
			if strings.Contains(err.Error(), key) {
				t.Fatalf("error %q contains the API key", err)
			}
			if !strings.Contains(err.Error(), redactedAPIKey) {
				t.Fatalf("error %q, want redaction placeholder", err)
			}
		})
	}
}

// TestReadErrorBodyDropsKeyCutAtLimit verifies a key straddling the snippet limit leaks no prefix.
func TestReadErrorBodyDropsKeyCutAtLimit(t *testing.T) {
	const limit = 1024
	key := "k3y-" + strings.Repeat("abcdef0123", 8)
	client := NewNexusClient(key)
	for pad := limit - len(key) - 2; pad <= limit+1; pad++ {
		body := strings.Repeat("x", pad) + key + strings.Repeat("y", 16)
		got := client.readErrorBody(strings.NewReader(body), limit)
		if len(got) > limit {
			t.Fatalf("pad %d: snippet length = %d, want <= %d", pad, len(got), limit)
		}
		rest := strings.ReplaceAll(got, redactedAPIKey, "")
		rest = strings.TrimRight(strings.TrimLeft(rest, "x"), "y")
		if rest != "" {
			t.Fatalf("pad %d: snippet leaks %q", pad, rest)
		}
	}
}

// TestNexusClientRedirectPolicy verifies redirects keep the API key on the original host only.
func TestNexusClientRedirectPolicy(t *testing.T) {
	const key = "redirect-key"
	tests := []struct {
		name          string
		target        string
		wantErr       error
		wantErrMsg    string
		wantOtherHits int
		wantFinalKey  bool
	}{
		{name: "cross-host redirect refused", target: "other", wantErr: ErrCrossHostRedirect},
		{name: "same-host redirect followed with key", target: "same", wantFinalKey: true},
		{name: "same-host redirect loop capped", target: "loop", wantErrMsg: "stopped after 5 redirects"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_ = t.TempDir()
			var mu sync.Mutex
			otherHits := 0
			otherKeys := []string{}
			finalKey := ""
			other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				otherHits++
				otherKeys = append(otherKeys, r.Header.Get("apikey"))
				mu.Unlock()
				_, _ = w.Write([]byte(`{"is_premium":true}`))
			}))
			defer other.Close()
			origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/final":
					mu.Lock()
					finalKey = r.Header.Get("apikey")
					mu.Unlock()
					_, _ = w.Write([]byte(`{"is_premium":true}`))
				case tc.target == "other":
					http.Redirect(w, r, other.URL+r.URL.Path, http.StatusFound)
				case tc.target == "same":
					http.Redirect(w, r, "/final", http.StatusFound)
				default:
					http.Redirect(w, r, r.URL.Path, http.StatusFound)
				}
			}))
			defer origin.Close()

			client := NewNexusClient(key)
			client.baseURL = origin.URL
			client.httpClient.Transport = origin.Client().Transport
			user, err := client.ValidateUser(context.Background())

			mu.Lock()
			defer mu.Unlock()
			if otherHits != tc.wantOtherHits {
				t.Fatalf("other host hits = %d (apikey headers %q), want %d", otherHits, otherKeys, tc.wantOtherHits)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("ValidateUser() error = %v, want errors.Is(_, %v)", err, tc.wantErr)
			}
			if tc.wantErrMsg != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErrMsg)) {
				t.Fatalf("ValidateUser() error = %v, want substring %q", err, tc.wantErrMsg)
			}
			if !tc.wantFinalKey {
				return
			}
			if err != nil {
				t.Fatalf("ValidateUser() error = %v", err)
			}
			if user == nil || !user.IsPremium {
				t.Fatalf("ValidateUser() = %#v, want premium user", user)
			}
			if finalKey != key {
				t.Fatalf("apikey at redirect target = %q, want %q", finalKey, key)
			}
		})
	}
}

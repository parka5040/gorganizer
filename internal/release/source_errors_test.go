//go:build !releasefixture

package release

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/httpx"
)

// TestReleaseNetworkErrorClassification distinguishes connectivity from HTTP and policy errors.
func TestReleaseNetworkErrorClassification(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := closed.Addr().String()
	closed.Close()
	failDial := http.DefaultTransport.(*http.Transport).Clone()
	failDial.DialContext = func(context.Context, string, string) (net.Conn, error) {
		return nil, &net.DNSError{Err: "no such host", Name: "fixture.invalid", IsNotFound: true}
	}
	body := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.Write([]byte("first bytes"))
	}))
	defer body.Close()
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer tlsServer.Close()
	status := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "https://outside.invalid/", http.StatusFound)
		case "/403":
			w.WriteHeader(403)
		case "/429":
			w.Header().Set("Retry-After", "99999")
			w.WriteHeader(429)
		case "/502":
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprint(time.Now().Add(90*time.Second).Unix()))
			w.WriteHeader(502)
		}
	}))
	defer status.Close()
	policy := httpxClientForTest(func(u *url.URL) error {
		if u.Hostname() == "outside.invalid" {
			return fmt.Errorf("refusing release origin")
		}
		return nil
	})
	cases := []struct {
		name, address string
		client        *http.Client
		unreachable   bool
		body          bool
		code          int
	}{
		{"refused", "http://" + address, &http.Client{}, true, false, 0},
		{"dns", "http://fixture.invalid", &http.Client{Transport: failDial}, true, false, 0},
		{"mid body", body.URL, body.Client(), true, true, 0},
		{"certificate", tlsServer.URL, &http.Client{}, true, false, 0},
		{"forbidden", status.URL + "/403", status.Client(), false, false, 403},
		{"limited", status.URL + "/429", status.Client(), false, false, 429},
		{"gateway", status.URL + "/502", status.Client(), false, false, 502},
		{"redirect policy", status.URL + "/redirect", policy, false, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := Source{Client: tc.client}
			_, err := s.fetch(context.Background(), tc.address, 1024)
			if err == nil || errors.Is(err, ErrUnreachable) != tc.unreachable {
				t.Fatalf("classification: %v", err)
			}
			var transport *TransportError
			var read *readError
			var status *StatusError
			switch {
			case tc.code != 0:
				if !errors.As(err, &status) || status.Code != tc.code || err.Error() != "release download returned "+status.Status {
					t.Fatalf("status error: %v", err)
				}
			case tc.body:
				if !errors.As(err, &read) || err.Error() != "reading release file: "+read.err.Error() || errors.Unwrap(read) == nil {
					t.Fatalf("read error: %v", err)
				}
			default:
				if !errors.As(err, &transport) || err.Error() != "downloading release: "+transport.Err.Error() {
					t.Fatalf("transport error: %v", err)
				}
			}
			if tc.code == 429 && status.RetryAfter != time.Hour || tc.code == 502 && (status.RetryAfter < time.Minute || status.RetryAfter > 2*time.Minute) {
				t.Fatalf("retry interval: %v", status.RetryAfter)
			}
		})
	}
}

// TestStatusErrorRetainsCustomReason checks the exact server-supplied reason phrase.
func TestStatusErrorRetainsCustomReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		fmt.Fprint(buffered, "HTTP/1.1 418 I'm a teapot\r\nContent-Length: 0\r\n\r\n")
		buffered.Flush()
	}))
	defer server.Close()
	_, err := (Source{Client: server.Client()}).fetch(context.Background(), server.URL, 100)
	if err == nil || err.Error() != "release download returned 418 I'm a teapot" || errors.Is(err, ErrUnreachable) {
		t.Fatalf("custom status: %v", err)
	}
}

// httpxClientForTest constructs a policy client for an HTTP redirect test.
func httpxClientForTest(allow func(*url.URL) error) *http.Client {
	return httpx.NewClient(httpx.Options{AllowOrigin: allow})
}

// TestRetryAfterBounds checks header precedence, rate-limit reset and clamping.
func TestRetryAfterBounds(t *testing.T) {
	for _, tc := range []struct {
		header   http.Header
		min, max time.Duration
	}{
		{http.Header{"Retry-After": {"-5"}}, 0, 0},
		{http.Header{"Retry-After": {"999999"}}, time.Hour, time.Hour},
		{http.Header{"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {fmt.Sprint(time.Now().Add(10 * time.Second).Unix())}}, 8 * time.Second, 10 * time.Second},
		{http.Header{}, 0, 0},
	} {
		got := retryAfter(tc.header)
		if got < tc.min || got > tc.max {
			t.Fatalf("retryAfter(%v) = %v", tc.header, got)
		}
	}
}

// TestOriginRulesChecksGitHubHosts checks the pinned API and release asset host rules.
func TestOriginRulesChecksGitHubHosts(t *testing.T) {
	for _, tc := range []struct {
		u          string
		api, asset bool
	}{
		{"https://api.github.com/path", true, false},
		{"https://github.com/path", false, true},
		{"https://objects.githubusercontent.com/path", false, true},
		{"https://github.com:443/path", false, true},
		{"https://github.com:444/path", false, false},
		{"https://user@github.com/path", false, false},
		{"http://github.com/path", false, false},
		{"https://github.com.evil.invalid/path", false, false},
		{"https://githubusercontent.com/path", false, false},
	} {
		u, err := url.Parse(tc.u)
		if err != nil {
			t.Fatal(err)
		}
		if (allowLatestOrigin(u) == nil) != tc.api || (allowAssetOrigin(u) == nil) != tc.asset {
			t.Fatalf("origin policy for %s", tc.u)
		}
	}
}

package httpx

import (
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type Options struct {
	ResponseHeaderTimeout time.Duration
	OverallTimeout        time.Duration
	MaxRedirects          int
	RequireHTTPS          bool
	AllowOrigin           func(*url.URL) error
}

type originTransport struct {
	base  http.RoundTripper
	allow func(*url.URL) error
}

// RoundTrip checks the request origin before sending it.
func (t originTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.allow(req.URL); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(req)
}

// NewClient returns an HTTP client configured from o.
func NewClient(o Options) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if o.ResponseHeaderTimeout != 0 {
		transport.ResponseHeaderTimeout = o.ResponseHeaderTimeout
	}

	var roundTripper http.RoundTripper = transport
	if o.AllowOrigin != nil {
		roundTripper = originTransport{base: transport, allow: o.AllowOrigin}
	}
	client := &http.Client{Transport: roundTripper}
	if o.OverallTimeout != 0 {
		client.Timeout = o.OverallTimeout
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if o.MaxRedirects != 0 && len(via) >= o.MaxRedirects {
			return fmt.Errorf("stopped after %d redirects", o.MaxRedirects)
		}
		if o.RequireHTTPS && req.URL.Scheme != "https" {
			return fmt.Errorf("refusing redirect to non-https scheme")
		}
		return nil
	}
	return client
}

// APIClient returns an HTTP client for API requests.
func APIClient() *http.Client {
	return NewClient(Options{
		ResponseHeaderTimeout: 15 * time.Second,
		OverallTimeout:        30 * time.Second,
		MaxRedirects:          5,
		RequireHTTPS:          true,
	})
}

// DownloadClient returns an HTTP client for archive downloads.
func DownloadClient() *http.Client {
	return NewClient(Options{
		ResponseHeaderTimeout: 30 * time.Second,
		MaxRedirects:          5,
		RequireHTTPS:          true,
	})
}

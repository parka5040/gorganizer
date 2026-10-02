package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parka/gorganizer/internal/httpx"
)

const repositoryURL = "https://github.com/parka5040/gorganizer/releases/download/"
const notesURLBase = "https://github.com/parka5040/gorganizer/releases/tag/"
const latestURL = "https://api.github.com/repos/parka5040/gorganizer/releases/latest"

var tagPattern = regexp.MustCompile(`^v[0-9]{1,9}\.[0-9]{1,9}\.[0-9]{1,9}$`)
var idleTimeout = 60 * time.Second

type Source struct {
	BaseURL   string
	LatestURL string
	Client    *http.Client
	Latest    func(context.Context) (string, error)
}

// allowLatestOrigin permits only the pinned GitHub API origin.
func allowLatestOrigin(u *url.URL) error { return githubOrigin(u, u.Hostname() == "api.github.com") }

// allowAssetOrigin permits GitHub releases and GitHubusercontent asset hosts.
func allowAssetOrigin(u *url.URL) error {
	return githubOrigin(u, u.Hostname() == "github.com" || strings.HasSuffix(u.Hostname(), ".githubusercontent.com"))
}

// githubOrigin checks the common HTTPS GitHub origin constraints.
func githubOrigin(u *url.URL, host bool) error {
	if !host || u.Scheme != "https" || u.User != nil || u.Port() != "" && u.Port() != "443" {
		return fmt.Errorf("refusing release origin")
	}
	return nil
}

// LatestClient constructs the pinned latest-release API client.
func LatestClient() *http.Client {
	return httpx.NewClient(httpx.Options{ResponseHeaderTimeout: 15 * time.Second, MaxRedirects: 5, RequireHTTPS: true, AllowOrigin: allowLatestOrigin})
}

// assetClient constructs the pinned release-asset client.
func assetClient() *http.Client {
	return httpx.NewClient(httpx.Options{ResponseHeaderTimeout: 30 * time.Second, MaxRedirects: 5, RequireHTTPS: true, AllowOrigin: allowAssetOrigin})
}

// ResolveTag resolves a requested tag or the latest published tag.
func (s Source) ResolveTag(ctx context.Context, requested string) (string, error) {
	tag := requested
	if tag == "" {
		if s.Latest != nil {
			var err error
			tag, err = s.Latest(ctx)
			if err != nil {
				return "", fmt.Errorf("checking the latest release: %w", err)
			}
		} else {
			endpoint := s.LatestURL
			if endpoint == "" {
				endpoint = latestURL
			}
			body, err := s.fetchLatest(ctx, endpoint, 1<<20)
			if err != nil {
				return "", fmt.Errorf("checking the latest release: %w", err)
			}
			var result struct {
				Tag string `json:"tag_name"`
			}
			if err := json.Unmarshal(body, &result); err != nil {
				return "", fmt.Errorf("invalid latest release information: %w", err)
			}
			tag = result.Tag
		}
	}
	if err := ValidateTag(tag); err != nil {
		return "", err
	}
	return tag, nil
}

// AssetURL returns the URL for an asset of a validated tag.
func (s Source) AssetURL(tag, name string) (string, error) {
	if err := ValidateTag(tag); err != nil {
		return "", err
	}
	base := s.BaseURL
	if base == "" {
		base = repositoryURL
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	u, err := url.Parse(base + tag + "/" + name)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("invalid release source")
	}
	return u.String(), nil
}

type watchedBody struct {
	io.ReadCloser
	cancel context.CancelCauseFunc
	timer  *time.Timer
	mu     sync.Mutex
	closed bool
}

// Read resets the idle deadline whenever the response delivers bytes.
func (b *watchedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.mu.Lock()
		if !b.closed {
			b.timer.Reset(idleTimeout)
		}
		b.mu.Unlock()
	}
	return n, err
}

// Close releases the body, deadline and request context.
func (b *watchedBody) Close() error {
	b.mu.Lock()
	b.closed = true
	b.timer.Stop()
	b.mu.Unlock()
	b.cancel(context.Canceled)
	return b.ReadCloser.Close()
}

// fetch reads an asset within a strict size limit.
func (s Source) fetch(ctx context.Context, address string, limit int64) ([]byte, error) {
	return s.readResource(ctx, address, limit, false)
}

// fetchLatest reads the latest-release API response within a strict size limit.
func (s Source) fetchLatest(ctx context.Context, address string, limit int64) ([]byte, error) {
	return s.readResource(ctx, address, limit, true)
}

// readResource reads one release resource using the corresponding client.
func (s Source) readResource(ctx context.Context, address string, limit int64, latest bool) ([]byte, error) {
	response, err := s.openResource(ctx, address, latest)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.ContentLength > limit {
		return nil, fmt.Errorf("release file is too large")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, &readError{err: err, timedOut: errors.Is(context.Cause(response.Request.Context()), context.DeadlineExceeded)}
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("release file is too large")
	}
	return body, nil
}

// open sends a request using the release asset client.
func (s Source) open(ctx context.Context, address string) (*http.Response, error) {
	return s.openResource(ctx, address, false)
}

// openResource sends a watched request with the client for its operation.
func (s Source) openResource(ctx context.Context, address string, latest bool) (*http.Response, error) {
	client := s.Client
	if client == nil {
		client = assetClient()
		if latest {
			client = LatestClient()
		}
	}
	requestCtx, cancel := context.WithCancelCause(ctx)
	timer := time.AfterFunc(idleTimeout, func() { cancel(context.DeadlineExceeded) })
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, address, nil)
	if err != nil {
		timer.Stop()
		cancel(context.Canceled)
		return nil, fmt.Errorf("preparing release download: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		timer.Stop()
		timedOut := errors.Is(context.Cause(requestCtx), context.DeadlineExceeded)
		cancel(context.Canceled)
		return nil, &TransportError{Err: err, timedOut: timedOut}
	}
	response.Body = &watchedBody{ReadCloser: response.Body, cancel: cancel, timer: timer}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, statusError(response.StatusCode, response.Status, retryAfter(response.Header))
	}
	return response, nil
}

// retryAfter reads HTTP retry timing headers without trusting unbounded delays.
func retryAfter(h http.Header) time.Duration {
	if seconds, err := strconv.ParseInt(h.Get("Retry-After"), 10, 64); err == nil {
		return retryDelay(seconds)
	}
	if h.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			return retryDelay(reset - time.Now().Unix())
		}
	}
	return 0
}

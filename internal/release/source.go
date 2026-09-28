package release

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/parka/gorganizer/internal/httpx"
)

const repositoryURL = "https://github.com/parka5040/gorganizer/releases/download/"
const latestURL = "https://api.github.com/repos/parka5040/gorganizer/releases/latest"

var tagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

type Source struct {
	BaseURL   string
	LatestURL string
	Client    *http.Client
	Latest    func(context.Context) (string, error)
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
			body, err := s.fetch(ctx, endpoint, 1<<20)
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
	if !tagPattern.MatchString(tag) {
		return "", fmt.Errorf("invalid release tag %q", tag)
	}
	return tag, nil
}

// AssetURL returns the URL for an asset of a validated tag.
func (s Source) AssetURL(tag, name string) (string, error) {
	if !tagPattern.MatchString(tag) {
		return "", fmt.Errorf("invalid release tag %q", tag)
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

// fetch reads a small release resource within a strict size limit.
func (s Source) fetch(ctx context.Context, address string, limit int64) ([]byte, error) {
	response, err := s.open(ctx, address)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.ContentLength > limit {
		return nil, fmt.Errorf("release file is too large")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("reading release file: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("release file is too large")
	}
	return body, nil
}

// open sends a request using the configured release download client.
func (s Source) open(ctx context.Context, address string) (*http.Response, error) {
	client := s.Client
	if client == nil {
		client = httpx.DownloadClient()
		if address == latestURL {
			client = httpx.APIClient()
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, fmt.Errorf("preparing release download: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("downloading release: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("release download returned %s", response.Status)
	}
	return response, nil
}

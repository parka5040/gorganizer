package smapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/parka/gorganizer/internal/httpx"
)

type ModQuery struct {
	ID               string
	InstalledVersion string
	UpdateKeys       []string
}

type LookupRequest struct {
	Mods        []ModQuery
	APIVersion  string
	GameVersion string
	Force       bool
}

type ModInfo struct {
	ID                     string
	Known                  bool
	Name                   string
	NexusID                int
	MainVersion            string
	MainURL                string
	CompatibilityStatus    string
	CompatibilitySummary   string
	SuggestedUpdateVersion string
	SuggestedUpdateURL     string
	Errors                 []string
	Stale                  bool
}

type Client struct {
	BaseURL      string
	RouteVersion string
	UserAgent    string
	HTTP         *http.Client
	Cache        *Cache
	Now          func() time.Time
}

const (
	DefaultBaseURL      = "https://smapi.io/api/"
	DefaultRouteVersion = "4.5.2"
	defaultUserAgent    = "gorganizer"
	maxLookupBatch      = 100
	maxLookupResponse   = 8 << 20
	lookupTimeout       = 15 * time.Second
)

type apiQuery struct {
	ID               string   `json:"id"`
	InstalledVersion string   `json:"installedVersion,omitempty"`
	UpdateKeys       []string `json:"updateKeys"`
}

type apiRequest struct {
	Mods                    []apiQuery `json:"mods"`
	APIVersion              string     `json:"apiVersion"`
	GameVersion             string     `json:"gameVersion"`
	Platform                string     `json:"platform"`
	IncludeExtendedMetadata bool       `json:"includeExtendedMetadata"`
}

type apiVersionLink struct {
	Version string `json:"version"`
	URL     string `json:"url"`
}

type apiMetadata struct {
	ID                   []string       `json:"id"`
	Name                 string         `json:"name"`
	NexusID              int            `json:"nexusID"`
	Main                 apiVersionLink `json:"main"`
	CompatibilityStatus  string         `json:"compatibilityStatus"`
	CompatibilitySummary string         `json:"compatibilitySummary"`
}

type apiResult struct {
	ID              string         `json:"id"`
	Metadata        apiMetadata    `json:"metadata"`
	SuggestedUpdate apiVersionLink `json:"suggestedUpdate"`
	Errors          []string       `json:"errors"`
}

var defaultHTTPClient = sync.OnceValue(httpx.APIClient)

type cacheWrite struct {
	id        string
	updateKey string
	info      ModInfo
}

// Lookup returns smapi.io metadata keyed by lower-case mod ID, querying only mods without fresh cache entries and falling back to stale ones when smapi.io gives no answer.
func (c *Client) Lookup(ctx context.Context, req LookupRequest) (map[string]ModInfo, error) {
	req.APIVersion = wireVersion(req.APIVersion)
	req.GameVersion = wireVersion(req.GameVersion)
	now := c.now()
	result := map[string]ModInfo{}
	fallbacks := map[string]ModInfo{}
	seen := map[string]bool{}
	var pending []ModQuery
	for _, query := range req.Mods {
		id := strings.ToLower(query.ID)
		if strings.TrimSpace(id) == "" || seen[id] {
			continue
		}
		seen[id] = true
		cached := c.Cache.lookup(id, updateCacheKey(query, req), now)
		if cached.staticFresh {
			result[id] = cached.freshInfo()
		}
		if req.Force || !cached.staticFresh || !cached.updateFresh {
			pending = append(pending, query)
			if info, ok := cached.fallback(); ok {
				fallbacks[id] = info
			}
		}
	}
	for start := 0; start < len(pending); start += maxLookupBatch {
		batch := pending[start:min(start+maxLookupBatch, len(pending))]
		received, err := c.lookupBatch(ctx, batch, req)
		if err != nil {
			for _, query := range pending[start:] {
				id := strings.ToLower(query.ID)
				if info, ok := fallbacks[id]; ok {
					result[id] = info
				}
			}
			return result, err
		}
		writes := make([]cacheWrite, 0, len(batch))
		for _, query := range batch {
			id := strings.ToLower(query.ID)
			info, ok := received[id]
			if !ok {
				if stale, found := fallbacks[id]; found {
					result[id] = stale
				} else {
					result[id] = ModInfo{ID: query.ID}
				}
				continue
			}
			result[id] = info
			writes = append(writes, cacheWrite{id: id, updateKey: updateCacheKey(query, req), info: info})
		}
		c.Cache.store(writes, now)
	}
	return result, nil
}

// Cached answers req from the retained cache only, never touching the network, marking entries with an expired part stale.
func (c *Client) Cached(req LookupRequest) map[string]ModInfo {
	req.APIVersion = wireVersion(req.APIVersion)
	req.GameVersion = wireVersion(req.GameVersion)
	now := c.now()
	result := map[string]ModInfo{}
	for _, query := range req.Mods {
		id := strings.ToLower(query.ID)
		if strings.TrimSpace(id) == "" {
			continue
		}
		if _, done := result[id]; done {
			continue
		}
		if info, ok := c.Cache.lookup(id, updateCacheKey(query, req), now).fallback(); ok {
			result[id] = info
		}
	}
	return result
}

// wireVersion reduces a version to the major.minor.patch and prerelease form SMAPI sends, keeping unparseable text as is.
func wireVersion(text string) string {
	parsed, err := ParseVersion(text, true)
	if err != nil {
		return text
	}
	return Version{Major: parsed.Major, Minor: parsed.Minor, Patch: parsed.Patch, Prerelease: parsed.Prerelease}.String()
}

// now returns the client clock, defaulting to time.Now.
func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// endpoint returns the mods lookup URL for the configured base URL and route version.
func (c *Client) endpoint() string {
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	route := c.RouteVersion
	if route == "" {
		route = DefaultRouteVersion
	}
	return base + "v" + route + "/mods"
}

// lookupBatch posts one batch of at most maxLookupBatch mods and decodes the response.
func (c *Client) lookupBatch(ctx context.Context, queries []ModQuery, req LookupRequest) (map[string]ModInfo, error) {
	body := apiRequest{
		Mods:                    make([]apiQuery, 0, len(queries)),
		APIVersion:              req.APIVersion,
		GameVersion:             req.GameVersion,
		Platform:                "Linux",
		IncludeExtendedMetadata: true,
	}
	for _, query := range queries {
		keys := append([]string{}, query.UpdateKeys...)
		body.Mods = append(body.Mods, apiQuery{ID: query.ID, InstalledVersion: query.InstalledVersion, UpdateKeys: keys})
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encoding smapi.io lookup: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("creating smapi.io lookup request: %w", err)
	}
	userAgent := c.UserAgent
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", userAgent)
	client := c.HTTP
	if client == nil {
		client = defaultHTTPClient()
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("sending smapi.io lookup: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return nil, fmt.Errorf("smapi.io lookup returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxLookupResponse+1))
	if err != nil {
		return nil, fmt.Errorf("reading smapi.io lookup response: %w", err)
	}
	if len(data) > maxLookupResponse {
		return nil, fmt.Errorf("reading smapi.io lookup response: exceeds %d bytes", maxLookupResponse)
	}
	return decodeLookupResponse(data)
}

// decodeLookupResponse converts the smapi.io response array into ModInfo values keyed by lower-case ID.
func decodeLookupResponse(data []byte) (map[string]ModInfo, error) {
	var entries []apiResult
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("decoding smapi.io lookup response: %w", err)
	}
	result := make(map[string]ModInfo, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.ID) == "" {
			continue
		}
		result[strings.ToLower(entry.ID)] = ModInfo{
			ID:                     entry.ID,
			Known:                  entry.Metadata.Name != "" || len(entry.Metadata.ID) != 0,
			Name:                   entry.Metadata.Name,
			NexusID:                entry.Metadata.NexusID,
			MainVersion:            entry.Metadata.Main.Version,
			MainURL:                entry.Metadata.Main.URL,
			CompatibilityStatus:    entry.Metadata.CompatibilityStatus,
			CompatibilitySummary:   entry.Metadata.CompatibilitySummary,
			SuggestedUpdateVersion: entry.SuggestedUpdate.Version,
			SuggestedUpdateURL:     entry.SuggestedUpdate.URL,
			Errors:                 append([]string(nil), entry.Errors...),
		}
	}
	return result, nil
}

// updateCacheKey builds the update-cache key from the mod ID, installed version, update keys and API/game versions.
func updateCacheKey(query ModQuery, req LookupRequest) string {
	keys := make([]string, len(query.UpdateKeys))
	for i, key := range query.UpdateKeys {
		keys[i] = strings.ToLower(key)
	}
	sort.Strings(keys)
	return strings.Join([]string{strings.ToLower(query.ID), query.InstalledVersion, strings.Join(keys, ","), req.APIVersion, req.GameVersion}, "|")
}

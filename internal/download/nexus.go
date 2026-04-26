package download

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"sync"
	"time"
)

// ErrInvalidKey is returned by ValidateAPIKey when the Nexus API rejects the key
// (HTTP 401/403). Callers should distinguish this from transport errors.
var ErrInvalidKey = errors.New("invalid API key")

// URLResolver abstracts Nexus API calls for testability (DIP).
type URLResolver interface {
	ResolveDownloadURL(link *NXMLink) (string, error)
	GetModInfo(gameSlug string, modID int) (*NexusModInfo, error)
	GetFileDetails(gameSlug string, modID, fileID int) (*NexusFileDetails, error)
}

// NexusFileList is the envelope returned by /v1/games/{game}/mods/{id}/files.
type NexusFileList struct {
	Files []NexusFileDetails `json:"files"`
}

// NexusModInfo holds basic mod metadata from the Nexus v1 API.
type NexusModInfo struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	Summary      string `json:"summary,omitempty"`
	PictureURL   string `json:"picture_url,omitempty"`
	ContainsAdult bool  `json:"contains_adult_content,omitempty"`
	DomainName   string `json:"domain_name,omitempty"`
	ModID        int    `json:"mod_id,omitempty"`
}

// NexusFileDetails carries the fields we need from the v1 file-details
// endpoint, named to match the v3 MinimalModFile shape from openapi.yaml so
// the metadata.yaml schema stays spec-aligned.
type NexusFileDetails struct {
	FileID       int    `json:"file_id"`
	Name         string `json:"name"`
	Version      string `json:"version"`
	CategoryName string `json:"category_name"`   // "MAIN" | "UPDATE" | "OPTIONAL" | ...
	UploadedTime string `json:"uploaded_time"`   // ISO 8601
	SizeKB       int64  `json:"size_kb"`
	FileName     string `json:"file_name"`
	Description  string `json:"description,omitempty"`
}

// NexusClient talks to the Nexus Mods API using MO2-compatible headers.
type NexusClient struct {
	apiKey     string
	httpClient *http.Client
	baseURL    string

	// rlMu guards the rate-limit fields below. We update them on every
	// response from any endpoint (v1 or v3) and consult them before
	// firing soft-dep batches so we don't burn through the daily quota.
	rlMu             sync.Mutex
	rlDailyRemaining int
	rlHourlyRemaining int
	rlLastSeen       time.Time
}

// NewNexusClient creates a new Nexus API client.
func NewNexusClient(apiKey string) *NexusClient {
	return &NexusClient{
		apiKey:  apiKey,
		baseURL: "https://api.nexusmods.com",
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		rlDailyRemaining:  -1, // -1 = unknown until first response observed
		rlHourlyRemaining: -1,
	}
}

// captureRateLimit reads the X-RL-* headers Nexus emits on every response.
// Field names per https://help.nexusmods.com (legacy v1) and v3 OpenAPI.
func (c *NexusClient) captureRateLimit(h http.Header) {
	d, dOk := strconv.Atoi(h.Get("X-RL-Daily-Remaining"))
	hh, hOk := strconv.Atoi(h.Get("X-RL-Hourly-Remaining"))
	if dOk != nil && hOk != nil {
		return
	}
	c.rlMu.Lock()
	defer c.rlMu.Unlock()
	if dOk == nil {
		c.rlDailyRemaining = d
	}
	if hOk == nil {
		c.rlHourlyRemaining = hh
	}
	c.rlLastSeen = time.Now()
}

// RateLimitRemaining returns the most-recently-observed (daily, hourly)
// remaining counts. Either field is -1 when no response has been observed
// yet. Callers use these to throttle large soft-dep batches.
func (c *NexusClient) RateLimitRemaining() (daily, hourly int) {
	c.rlMu.Lock()
	defer c.rlMu.Unlock()
	return c.rlDailyRemaining, c.rlHourlyRemaining
}

// setHeaders applies MO2-compatible API headers that the Nexus API expects.
// MO2 sends: apikey, User-Agent, Application-Name, Application-Version,
// Protocol-Version, Content-Type.
func (c *NexusClient) setHeaders(req *http.Request) {
	req.Header.Set("apikey", c.apiKey)
	req.Header.Set("User-Agent", fmt.Sprintf("Gorganizer/0.1.0 (%s) Go", runtime.GOOS))
	req.Header.Set("Application-Name", "Gorganizer")
	req.Header.Set("Application-Version", "0.1.0")
	req.Header.Set("Protocol-Version", "1.0.0")
	req.Header.Set("Content-Type", "application/json")
}

// ResolveDownloadURL calls the Nexus API to get a CDN download URL.
// Supports both premium (no key needed) and non-premium (key+expires from NXM URI) flows,
// matching MO2's behavior.
func (c *NexusClient) ResolveDownloadURL(link *NXMLink) (string, error) {
	// Build URL matching the official Nexus API client endpoint.
	// Non-premium users need key+expires from the NXM link.
	// Premium users can omit these. The API accepts both.
	endpoint := fmt.Sprintf("%s/v1/games/%s/mods/%d/files/%d/download_link",
		c.baseURL, link.GameSlug, link.ModID, link.FileID)

	if link.Key != "" {
		endpoint += fmt.Sprintf("?key=%s&expires=%d", link.Key, link.Expires)
	}

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return "", err
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("nexus API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("nexus download_link API returned %d: %s", resp.StatusCode, string(body))
	}

	var links []struct {
		URI string `json:"URI"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&links); err != nil {
		return "", fmt.Errorf("decoding download links: %w", err)
	}
	if len(links) == 0 {
		return "", fmt.Errorf("no download links returned")
	}
	return links[0].URI, nil
}

// GetModInfo fetches mod metadata from the Nexus API.
// GET /v1/games/{game}/mods/{mod_id}
func (c *NexusClient) GetModInfo(gameSlug string, modID int) (*NexusModInfo, error) {
	endpoint := fmt.Sprintf("%s/v1/games/%s/mods/%d", c.baseURL, gameSlug, modID)

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nexus API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("nexus mods API returned %d: %s", resp.StatusCode, string(body))
	}

	var info NexusModInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decoding mod info: %w", err)
	}
	return &info, nil
}

// GetFileDetails fetches detailed file metadata (name, version, category,
// uploaded_time, size_kb, etc.) from the v1 endpoint. Field selection and
// naming mirror the v3 MinimalModFile schema in openapi.yaml.
//
// GET /v1/games/{game}/mods/{mod_id}/files/{file_id}.json
func (c *NexusClient) GetFileDetails(gameSlug string, modID, fileID int) (*NexusFileDetails, error) {
	endpoint := fmt.Sprintf("%s/v1/games/%s/mods/%d/files/%d.json",
		c.baseURL, gameSlug, modID, fileID)

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nexus API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("nexus file details API returned %d: %s", resp.StatusCode, string(body))
	}

	var details NexusFileDetails
	if err := json.NewDecoder(resp.Body).Decode(&details); err != nil {
		return nil, fmt.Errorf("decoding file details: %w", err)
	}
	return &details, nil
}

// ListModFiles returns all files for a mod via /v1/games/{game}/mods/{id}/files.json.
// Useful when you know the mod ID but not which file to grab — the caller
// picks the best candidate (e.g. latest MAIN category).
func (c *NexusClient) ListModFiles(gameSlug string, modID int) (*NexusFileList, error) {
	endpoint := fmt.Sprintf("%s/v1/games/%s/mods/%d/files.json",
		c.baseURL, gameSlug, modID)
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nexus API request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("nexus files API returned %d: %s", resp.StatusCode, string(body))
	}
	var list NexusFileList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("decoding file list: %w", err)
	}
	return &list, nil
}

// ResolveDownloadURLByID returns a CDN URL for a given mod+file ID without an
// NXM-style key/expires tuple. This works for premium accounts only; non-
// premium returns 403 with a message about requiring premium or a managed
// NXM link. The caller is expected to handle that cleanly.
func (c *NexusClient) ResolveDownloadURLByID(gameSlug string, modID, fileID int) (string, error) {
	endpoint := fmt.Sprintf("%s/v1/games/%s/mods/%d/files/%d/download_link",
		c.baseURL, gameSlug, modID, fileID)
	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return "", err
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("nexus API request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("nexus download_link returned %d: %s", resp.StatusCode, string(body))
	}
	var links []struct {
		URI string `json:"URI"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&links); err != nil {
		return "", err
	}
	if len(links) == 0 {
		return "", fmt.Errorf("no download links returned")
	}
	return links[0].URI, nil
}

// ValidateAPIKey checks if the API key is valid by making an authenticated v3
// probe call against a stable public mod (SkyUI, SSE #12604). 200 means valid,
// 401/403 means the key was rejected (ErrInvalidKey), anything else is a
// transport/service error.
//
// v3 does not expose a /users/validate equivalent, so this probe approach is
// the intended pattern per the v3 OpenAPI spec.
func (c *NexusClient) ValidateAPIKey(ctx context.Context) error {
	endpoint := fmt.Sprintf("%s/v3/games/skyrimspecialedition/mods/12604", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return err
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("API key validation request failed: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrInvalidKey
	default:
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("validation failed: HTTP %d: %s", resp.StatusCode, string(body))
	}
}

// --- Nexus API v3 (dependency endpoints) ---
//
// The v3 surface is documented in api/proto/openapi.yaml. We only consume
// two endpoints here:
//
//   GET /games/{game_domain}/mod-files/{game_scoped_id}
//   GET /mod-files/{id}/dependencies/ranges
//
// The first call resolves the *game-scoped* file id (what we record in
// metadata.yaml from nxm:// URIs) into the *global* file id used by the
// dependency endpoints. Global ids are immutable, so callers persist the
// translation and only round-trip once per file.

// V3ModFile is the subset of v3 GetModFile we need.
type V3ModFile struct {
	ID                string `json:"id"`             // global file id
	GameScopedID      string `json:"game_scoped_id"` // matches what we have on disk
	GameID            string `json:"game_id"`
	ModGameScopedID   string `json:"mod_game_scoped_id"`
}

// V3MinimalMod is a small slice of MinimalMod surfaced by the dependency
// range responses; carrying just the fields we need for soft-dep display.
type V3MinimalMod struct {
	ID            string `json:"id"`
	GameScopedID  string `json:"game_scoped_id"`
	Name          string `json:"name"`
	ThumbnailURL  string `json:"thumbnail_url"`
}

// V3DepDefinition is one dependency definition with its ranges.
type V3DepDefinition struct {
	ID     string         `json:"id"`
	Ranges []V3DepRange   `json:"ranges"`
}

// V3DepRange represents one alternative within a dep definition. We keep
// only the parts we resolve against (target_group.mod) — version satisfaction
// is intentionally not enforced (presence is enough).
type V3DepRange struct {
	ID          string `json:"id"`
	TargetGroup struct {
		ID   string       `json:"id"`
		Name string       `json:"name"`
		Mod  V3MinimalMod `json:"mod"`
	} `json:"target_group"`
}

// V3DepRangesResponse is the envelope of GET /mod-files/{id}/dependencies/ranges.
type V3DepRangesResponse struct {
	DependencyDefinitions []V3DepDefinition `json:"dependency_definitions"`
}

// GetModFile resolves a game-scoped file id into the global file id used by
// the v3 dependency endpoints.
func (c *NexusClient) GetModFile(ctx context.Context, gameDomain, gameScopedID string) (*V3ModFile, error) {
	endpoint := fmt.Sprintf("%s/v3/games/%s/mod-files/%s", c.baseURL, gameDomain, gameScopedID)
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nexus v3 mod-file request failed: %w", err)
	}
	defer resp.Body.Close()
	c.captureRateLimit(resp.Header)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("nexus v3 mod-file returned %d: %s", resp.StatusCode, string(body))
	}
	var env struct {
		Data V3ModFile `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("decoding v3 mod-file: %w", err)
	}
	return &env.Data, nil
}

// GetModFileDependencyRanges fetches stored dependency ranges for a global
// file id. The empty `dependency_definitions` array is a normal response —
// the file simply has no declared deps.
func (c *NexusClient) GetModFileDependencyRanges(ctx context.Context, globalFileID string) (*V3DepRangesResponse, error) {
	endpoint := fmt.Sprintf("%s/v3/mod-files/%s/dependencies/ranges", c.baseURL, globalFileID)
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nexus v3 dep-ranges request failed: %w", err)
	}
	defer resp.Body.Close()
	c.captureRateLimit(resp.Header)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("nexus v3 dep-ranges returned %d: %s", resp.StatusCode, string(body))
	}
	// The dep-ranges endpoint replies with the response body directly,
	// not wrapped in a `data` envelope (per openapi.yaml ModFileDependencyRangesResponse).
	var out V3DepRangesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding v3 dep-ranges: %w", err)
	}
	return &out, nil
}

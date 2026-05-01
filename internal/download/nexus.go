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

// ErrInvalidKey is returned by ValidateAPIKey when the Nexus API rejects the key.
var ErrInvalidKey = errors.New("invalid API key")

// URLResolver abstracts Nexus API calls for testability.
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
	Name          string `json:"name"`
	Version       string `json:"version"`
	Summary       string `json:"summary,omitempty"`
	PictureURL    string `json:"picture_url,omitempty"`
	ContainsAdult bool   `json:"contains_adult_content,omitempty"`
	DomainName    string `json:"domain_name,omitempty"`
	ModID         int    `json:"mod_id,omitempty"`
}

// NexusFileDetails mirrors the v3 MinimalModFile shape we surface from v1.
type NexusFileDetails struct {
	FileID       int    `json:"file_id"`
	Name         string `json:"name"`
	Version      string `json:"version"`
	CategoryName string `json:"category_name"`
	UploadedTime string `json:"uploaded_time"`
	SizeKB       int64  `json:"size_kb"`
	FileName     string `json:"file_name"`
	Description  string `json:"description,omitempty"`
}

// NexusClient talks to the Nexus Mods API using MO2-compatible headers.
type NexusClient struct {
	apiKey     string
	httpClient *http.Client
	baseURL    string

	rlMu              sync.Mutex
	rlDailyRemaining  int
	rlHourlyRemaining int
	rlLastSeen        time.Time
}

func NewNexusClient(apiKey string) *NexusClient {
	return &NexusClient{
		apiKey:  apiKey,
		baseURL: "https://api.nexusmods.com",
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		rlDailyRemaining:  -1,
		rlHourlyRemaining: -1,
	}
}

// captureRateLimit reads the X-RL-* headers Nexus emits on every response.
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

// RateLimitRemaining returns most-recently-observed (daily, hourly) counts; -1 = unknown.
func (c *NexusClient) RateLimitRemaining() (daily, hourly int) {
	c.rlMu.Lock()
	defer c.rlMu.Unlock()
	return c.rlDailyRemaining, c.rlHourlyRemaining
}

// setHeaders applies MO2-compatible API headers.
func (c *NexusClient) setHeaders(req *http.Request) {
	req.Header.Set("apikey", c.apiKey)
	req.Header.Set("User-Agent", fmt.Sprintf("Gorganizer/0.1.0 (%s) Go", runtime.GOOS))
	req.Header.Set("Application-Name", "Gorganizer")
	req.Header.Set("Application-Version", "0.1.0")
	req.Header.Set("Protocol-Version", "1.0.0")
	req.Header.Set("Content-Type", "application/json")
}

// ResolveDownloadURL calls the Nexus API to get a CDN download URL.
func (c *NexusClient) ResolveDownloadURL(link *NXMLink) (string, error) {
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

// GetFileDetails fetches detailed file metadata from the v1 endpoint.
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

// ListModFiles returns all files for a mod.
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

// ResolveDownloadURLByID returns a CDN URL by mod+file ID; premium accounts only.
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

// ValidateAPIKey probes a stable public mod via v3; 401/403 returns ErrInvalidKey.
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

// V3ModFile is the subset of v3 GetModFile we need.
type V3ModFile struct {
	ID              string `json:"id"`
	GameScopedID    string `json:"game_scoped_id"`
	GameID          string `json:"game_id"`
	ModGameScopedID string `json:"mod_game_scoped_id"`
}

// V3MinimalMod is the slice of MinimalMod surfaced by dependency range responses.
type V3MinimalMod struct {
	ID           string `json:"id"`
	GameScopedID string `json:"game_scoped_id"`
	Name         string `json:"name"`
	ThumbnailURL string `json:"thumbnail_url"`
}

// V3DepDefinition is one dependency definition with its ranges.
type V3DepDefinition struct {
	ID     string       `json:"id"`
	Ranges []V3DepRange `json:"ranges"`
}

// V3DepRange is one alternative within a dep definition.
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

// GetModFile resolves a game-scoped file id into the global file id.
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

// GetModFileDependencyRanges fetches stored dependency ranges for a global file id.
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
	var out V3DepRangesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding v3 dep-ranges: %w", err)
	}
	return &out, nil
}

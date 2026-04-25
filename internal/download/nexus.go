package download

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
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
}

// NewNexusClient creates a new Nexus API client.
func NewNexusClient(apiKey string) *NexusClient {
	return &NexusClient{
		apiKey:  apiKey,
		baseURL: "https://api.nexusmods.com",
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
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

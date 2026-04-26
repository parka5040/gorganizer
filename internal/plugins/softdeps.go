package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// V3DepRangesFields are the fields softdeps needs from
// GetModFileDependencyRanges. We define our own struct rather than import
// the download package's nexus types from a deeper layer.
type V3DepRangesFields struct {
	Definitions []V3DepDefinitionFields
}

type V3DepDefinitionFields struct {
	Ranges []V3DepRangeFields
}

type V3DepRangeFields struct {
	TargetModID    string
	TargetModName  string
	TargetModSlug  string
}

// V3Adapter narrows a concrete v3 client to just what softdeps consumes.
// Lets us mock the Nexus surface in tests without dragging in the full
// download.NexusClient.
type V3Adapter interface {
	ResolveGlobalFileID(ctx context.Context, gameDomain, gameScopedID string) (string, error)
	FetchDependencyRanges(ctx context.Context, globalFileID string) (V3DepRangesFields, error)
	RateLimitRemaining() (daily, hourly int)
}

// SoftDepRequest queues one plugin's soft-dep lookup.
type SoftDepRequest struct {
	Filename     string // plugin filename — echoed back on the result
	GameDomain   string // nexus game slug (e.g. "skyrimspecialedition")
	ModID        int    // nexus mod id (display + URL building only)
	ModURL       string // mod page URL for the result UI
	FileID       int    // game-scoped file id from nxm:// metadata
}

// SoftDepResult is one plugin's resolved soft-dep status.
type SoftDepResult struct {
	Filename string
	Issues   []DepIssue
	Err      error // populated only when the lookup itself failed (network, rate-limit etc.)
}

// SoftDepFetcher resolves V3 soft dependencies for a batch of plugins. It
// caches both the global-file-id translation (immutable, persistent) and
// the dependency-range responses (24h TTL).
type SoftDepFetcher struct {
	resolver V3Adapter
	cacheDir string

	mu          sync.Mutex
	globalIDs   map[string]string // key: gameDomain + "|" + gameScopedID
	globalDirty bool
}

const (
	depRangesTTL    = 24 * time.Hour
	rateLimitFloor  = 50 // pause batch when daily-remaining drops below this
	fetcherWorkers  = 4
)

// NewSoftDepFetcher returns a fetcher that persists caches under cacheDir
// (e.g. config.CacheDir() + "/nexus"). The directory is created lazily on
// first write — passing a path that doesn't exist yet is fine.
func NewSoftDepFetcher(resolver V3Adapter, cacheDir string) *SoftDepFetcher {
	f := &SoftDepFetcher{
		resolver:  resolver,
		cacheDir:  cacheDir,
		globalIDs: map[string]string{},
	}
	f.loadGlobalIDs()
	return f
}

// Run drains reqs, dispatching results to out as each plugin's lookup
// completes. Run returns when reqs is closed AND every job has produced a
// result (or been cancelled). Cancelling ctx aborts in-flight requests and
// closes out promptly.
//
// The caller is responsible for closing reqs once all requests are queued.
func (f *SoftDepFetcher) Run(ctx context.Context, reqs <-chan SoftDepRequest, out chan<- SoftDepResult) {
	var wg sync.WaitGroup
	for i := 0; i < fetcherWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case req, ok := <-reqs:
					if !ok {
						return
					}
					res := f.resolveOne(ctx, req)
					select {
					case <-ctx.Done():
						return
					case out <- res:
					}
				}
			}
		}()
	}
	wg.Wait()
	close(out)
}

func (f *SoftDepFetcher) resolveOne(ctx context.Context, req SoftDepRequest) SoftDepResult {
	if req.GameDomain == "" || req.FileID == 0 {
		// Plugin came from a non-Nexus mod folder (manual install, drag-drop) —
		// nothing to resolve, return empty success.
		return SoftDepResult{Filename: req.Filename}
	}
	if d, _ := f.resolver.RateLimitRemaining(); d >= 0 && d < rateLimitFloor {
		return SoftDepResult{
			Filename: req.Filename,
			Err:      fmt.Errorf("rate-limit floor reached (daily remaining %d)", d),
		}
	}

	gameScopedID := fmt.Sprintf("%d", req.FileID)
	globalID, err := f.resolveGlobalID(ctx, req.GameDomain, gameScopedID)
	if err != nil {
		return SoftDepResult{Filename: req.Filename, Err: err}
	}
	ranges, err := f.fetchRanges(ctx, globalID)
	if err != nil {
		return SoftDepResult{Filename: req.Filename, Err: err}
	}

	// Resolution: every definition must be satisfied. A definition is
	// satisfied if any of its ranges resolves to a mod whose ID is in the
	// installed-and-enabled set. Since the installed set isn't known at
	// fetch time, we just collect the per-definition candidates and let
	// the caller (Daemon) cross-check against ModMetadata. Encode that as
	// "issues with kind=DepSoftMissing, populated as if missing" — the
	// daemon will filter out satisfied ones.
	res := SoftDepResult{Filename: req.Filename}
	for _, def := range ranges.Definitions {
		if len(def.Ranges) == 0 {
			continue
		}
		// Use the first range for display — the Daemon will check ALL ranges
		// against the install set; if any matches, the issue is dropped.
		// We surface every range's mod id as part of the issue so the
		// daemon can do the OR-resolution.
		first := def.Ranges[0]
		ref := &SoftDepRef{
			ModName: first.TargetModName,
			URL:     buildModURL(req.GameDomain, first.TargetModID),
		}
		if id, ok := parseInt(first.TargetModID); ok {
			ref.ModID = id
		}
		issue := DepIssue{Kind: DepSoftMissing, SoftRef: ref}
		// Encode alternative mod ids in Master so the Daemon's filter can
		// see them — separated by "|". A bit ugly, but avoids growing
		// DepIssue with a new field used only by this transport. Format:
		// "v1|altModID1|altModID2|..."
		var alts []string
		alts = append(alts, "v1")
		for _, r := range def.Ranges {
			if r.TargetModID != "" {
				alts = append(alts, r.TargetModID)
			}
		}
		issue.Master = strings.Join(alts, "|")
		res.Issues = append(res.Issues, issue)
	}
	return res
}

// FilterSatisfiedSoftDeps drops issues from result whose definition is
// already satisfied by an installed-and-enabled mod. installedModIDs is
// the set of mod ids present in the active profile.
func FilterSatisfiedSoftDeps(result *SoftDepResult, installedModIDs map[int]bool) {
	if result == nil || len(result.Issues) == 0 {
		return
	}
	keep := result.Issues[:0]
	for _, issue := range result.Issues {
		if issue.Kind != DepSoftMissing {
			keep = append(keep, issue)
			continue
		}
		alts := decodeAlts(issue.Master)
		satisfied := false
		for _, idStr := range alts {
			if id, ok := parseInt(idStr); ok && installedModIDs[id] {
				satisfied = true
				break
			}
		}
		if !satisfied {
			// Strip the encoded alts before sending to the wire.
			cleaned := issue
			cleaned.Master = ""
			keep = append(keep, cleaned)
		}
	}
	result.Issues = keep
}

func decodeAlts(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "|")
	if len(parts) < 2 || parts[0] != "v1" {
		return nil
	}
	return parts[1:]
}

func parseInt(s string) (int, bool) {
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	if s == "" {
		return 0, false
	}
	return n, true
}

func buildModURL(gameDomain, modID string) string {
	if modID == "" {
		return ""
	}
	return fmt.Sprintf("https://www.nexusmods.com/%s/mods/%s", gameDomain, modID)
}

// resolveGlobalID returns the v3 global file id for a (gameDomain,
// gameScopedID), consulting the persistent translation cache first. Global
// ids are immutable so this cache never expires.
func (f *SoftDepFetcher) resolveGlobalID(ctx context.Context, gameDomain, gameScopedID string) (string, error) {
	key := gameDomain + "|" + gameScopedID
	f.mu.Lock()
	if id, ok := f.globalIDs[key]; ok {
		f.mu.Unlock()
		return id, nil
	}
	f.mu.Unlock()

	id, err := f.resolver.ResolveGlobalFileID(ctx, gameDomain, gameScopedID)
	if err != nil {
		return "", err
	}
	f.mu.Lock()
	f.globalIDs[key] = id
	f.globalDirty = true
	f.mu.Unlock()
	f.persistGlobalIDs()
	return id, nil
}

type cachedDepRanges struct {
	FetchedAt time.Time         `json:"fetched_at"`
	Ranges    V3DepRangesFields `json:"ranges"`
}

// fetchRanges returns the dep-ranges response, hitting the disk cache when
// fresh, otherwise fetching from Nexus and writing through.
func (f *SoftDepFetcher) fetchRanges(ctx context.Context, globalFileID string) (V3DepRangesFields, error) {
	if cached, ok := f.readDiskCache(globalFileID); ok {
		return cached, nil
	}
	r, err := f.resolver.FetchDependencyRanges(ctx, globalFileID)
	if err != nil {
		return V3DepRangesFields{}, err
	}
	f.writeDiskCache(globalFileID, r)
	return r, nil
}

func (f *SoftDepFetcher) readDiskCache(globalFileID string) (V3DepRangesFields, bool) {
	path := filepath.Join(f.cacheDir, "dependencies", globalFileID+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		return V3DepRangesFields{}, false
	}
	var c cachedDepRanges
	if err := json.Unmarshal(b, &c); err != nil {
		return V3DepRangesFields{}, false
	}
	if time.Since(c.FetchedAt) > depRangesTTL {
		return V3DepRangesFields{}, false
	}
	return c.Ranges, true
}

func (f *SoftDepFetcher) writeDiskCache(globalFileID string, r V3DepRangesFields) {
	dir := filepath.Join(f.cacheDir, "dependencies")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	c := cachedDepRanges{FetchedAt: time.Now(), Ranges: r}
	b, err := json.Marshal(c)
	if err != nil {
		return
	}
	tmp := filepath.Join(dir, globalFileID+".json.tmp")
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return
	}
	_ = os.Rename(tmp, filepath.Join(dir, globalFileID+".json"))
}

func (f *SoftDepFetcher) globalIDsPath() string {
	return filepath.Join(f.cacheDir, "file_global_ids.json")
}

func (f *SoftDepFetcher) loadGlobalIDs() {
	b, err := os.ReadFile(f.globalIDsPath())
	if err != nil {
		return
	}
	_ = json.Unmarshal(b, &f.globalIDs)
}

func (f *SoftDepFetcher) persistGlobalIDs() {
	f.mu.Lock()
	if !f.globalDirty {
		f.mu.Unlock()
		return
	}
	snap := make(map[string]string, len(f.globalIDs))
	for k, v := range f.globalIDs {
		snap[k] = v
	}
	f.globalDirty = false
	f.mu.Unlock()

	if err := os.MkdirAll(f.cacheDir, 0755); err != nil {
		return
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return
	}
	tmp := f.globalIDsPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return
	}
	_ = os.Rename(tmp, f.globalIDsPath())
}

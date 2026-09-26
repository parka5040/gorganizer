package smapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type capturedRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
	IDs    []string
}

type lookupServer struct {
	mu       sync.Mutex
	requests []capturedRequest
	status   int
	raw      []byte
	known    map[string]bool
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// Now returns the fake current time.
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the fake clock forward.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// ServeHTTP records the request and answers with the configured status, raw body, or generated entries.
func (s *lookupServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var decoded struct {
		Mods []struct {
			ID               string   `json:"id"`
			InstalledVersion string   `json:"installedVersion"`
			UpdateKeys       []string `json:"updateKeys"`
		} `json:"mods"`
	}
	_ = json.Unmarshal(body, &decoded)
	captured := capturedRequest{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: body}
	for _, mod := range decoded.Mods {
		captured.IDs = append(captured.IDs, mod.ID)
	}
	s.mu.Lock()
	s.requests = append(s.requests, captured)
	status, raw, known := s.status, s.raw, s.known
	s.mu.Unlock()
	if status != 0 {
		http.Error(w, "server exploded", status)
		return
	}
	if raw != nil {
		_, _ = w.Write(raw)
		return
	}
	entries := []map[string]any{}
	for _, mod := range decoded.Mods {
		entry := map[string]any{"id": mod.ID, "metadata": map[string]any{"id": []string{}}, "errors": []string{}}
		if known[strings.ToLower(mod.ID)] {
			entry["metadata"] = map[string]any{
				"id":                   []string{mod.ID},
				"name":                 mod.ID + " Name",
				"nexusID":              7,
				"main":                 map[string]any{"version": "2.0.0", "url": "https://example.test/" + mod.ID},
				"compatibilityStatus":  "Ok",
				"compatibilitySummary": "use latest version.",
			}
			if mod.InstalledVersion != "2.0.0" {
				entry["suggestedUpdate"] = map[string]any{"version": "2.0.0", "url": "https://example.test/" + mod.ID}
			}
		}
		entries = append(entries, entry)
	}
	_ = json.NewEncoder(w).Encode(entries)
}

// set replaces the server's response behaviour.
func (s *lookupServer) set(status int, raw []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.raw = status, raw
}

// captured returns a copy of the recorded requests.
func (s *lookupServer) captured() []capturedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]capturedRequest(nil), s.requests...)
}

// newLookupServer starts an httptest server for the smapi.io mods endpoint.
func newLookupServer(t *testing.T, known ...string) (*lookupServer, *httptest.Server) {
	t.Helper()
	handler := &lookupServer{known: map[string]bool{}}
	for _, id := range known {
		handler.known[strings.ToLower(id)] = true
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return handler, server
}

// newTestClient builds a client for server with a cache in dir and the given clock.
func newTestClient(server *httptest.Server, dir string, clock *fakeClock) *Client {
	client := &Client{BaseURL: server.URL + "/api/", RouteVersion: "4.5.2", UserAgent: "gorganizer-test/1.0", HTTP: server.Client(), Cache: OpenCache(dir)}
	if clock != nil {
		client.Now = clock.Now
	}
	return client
}

// newFakeClock returns a clock fixed at a known instant.
func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)}
}

// lookupRequest builds a lookup request for mods with fixed API and game versions.
func lookupRequest(mods ...ModQuery) LookupRequest {
	return LookupRequest{Mods: mods, APIVersion: "4.5.2", GameVersion: "1.6.15"}
}

// TestLookupRequestShape verifies the exact method, path, headers and body sent to smapi.io.
func TestLookupRequestShape(t *testing.T) {
	handler, server := newLookupServer(t)
	handler.set(0, []byte(`[]`))
	client := newTestClient(server, "", nil)
	got, err := client.Lookup(context.Background(), lookupRequest(
		ModQuery{ID: "Pathoschild.ContentPatcher", InstalledVersion: "2.0.0", UpdateKeys: []string{"Nexus:1915"}},
		ModQuery{ID: "nonexistent.fake.mod"},
	))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	requests := handler.captured()
	if len(requests) != 1 {
		t.Fatalf("server received %d requests, want 1", len(requests))
	}
	request := requests[0]
	if request.Method != http.MethodPost || request.Path != "/api/v4.5.2/mods" {
		t.Fatalf("request = %s %s, want POST /api/v4.5.2/mods", request.Method, request.Path)
	}
	for header, want := range map[string]string{"Content-Type": "application/json", "Accept": "application/json", "User-Agent": "gorganizer-test/1.0"} {
		if got := request.Header.Get(header); got != want {
			t.Fatalf("header %s = %q, want %q", header, got, want)
		}
	}
	wantBody := `{"mods":[{"id":"Pathoschild.ContentPatcher","installedVersion":"2.0.0","updateKeys":["Nexus:1915"]},{"id":"nonexistent.fake.mod","updateKeys":[]}],"apiVersion":"4.5.2","gameVersion":"1.6.15","platform":"Linux","includeExtendedMetadata":true}`
	if string(request.Body) != wantBody {
		t.Fatalf("body = %s, want %s", request.Body, wantBody)
	}
	want := map[string]ModInfo{
		"pathoschild.contentpatcher": {ID: "Pathoschild.ContentPatcher"},
		"nonexistent.fake.mod":       {ID: "nonexistent.fake.mod"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Lookup = %#v, want %#v", got, want)
	}
}

// TestLookupDefaults verifies the default route, user agent and HTTP client are applied.
func TestLookupDefaults(t *testing.T) {
	handler, server := newLookupServer(t, "a.mod")
	client := &Client{BaseURL: server.URL + "/api"}
	got, err := client.Lookup(context.Background(), lookupRequest(ModQuery{ID: "a.mod"}))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	requests := handler.captured()
	if len(requests) != 1 || requests[0].Path != "/api/v"+DefaultRouteVersion+"/mods" || requests[0].Header.Get("User-Agent") != defaultUserAgent {
		t.Fatalf("requests = %#v", requests)
	}
	if !got["a.mod"].Known {
		t.Fatalf("Lookup = %#v, want a known a.mod", got)
	}
}

// TestLookupParsesRecordedResponse verifies decoding of a recorded smapi.io response.
func TestLookupParsesRecordedResponse(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "webapi.json"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	handler, server := newLookupServer(t)
	handler.set(0, raw)
	client := newTestClient(server, "", nil)
	got, err := client.Lookup(context.Background(), lookupRequest(
		ModQuery{ID: "pathoschild.contentpatcher", InstalledVersion: "2.9.1", UpdateKeys: []string{"Nexus:1915"}},
		ModQuery{ID: "spacechase0.SpaceCore", InstalledVersion: "1.20.0", UpdateKeys: []string{"Nexus:1348"}},
		ModQuery{ID: "nonexistent.fake.mod"},
	))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	want := map[string]ModInfo{
		"pathoschild.contentpatcher": {
			ID: "Pathoschild.ContentPatcher", Known: true, Name: "Content Patcher", NexusID: 1915,
			MainVersion: "2.9.1", MainURL: "https://www.nexusmods.com/stardewvalley/mods/1915",
			CompatibilityStatus: "Ok", CompatibilitySummary: "use latest version.",
		},
		"spacechase0.spacecore": {
			ID: "spacechase0.SpaceCore", Known: true, Name: "SpaceCore", NexusID: 1348,
			MainVersion: "1.28.4", MainURL: "https://www.nexusmods.com/stardewvalley/mods/1348",
			CompatibilityStatus: "Ok", CompatibilitySummary: "use latest version.",
			SuggestedUpdateVersion: "1.28.4", SuggestedUpdateURL: "https://www.nexusmods.com/stardewvalley/mods/1348",
		},
		"nonexistent.fake.mod": {ID: "nonexistent.fake.mod"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Lookup = %#v, want %#v", got, want)
	}
}

// TestDecodeLookupResponseErrors verifies null metadata and entry errors are decoded.
func TestDecodeLookupResponseErrors(t *testing.T) {
	got, err := decodeLookupResponse([]byte(`[{"id":"A.B","metadata":null,"suggestedUpdate":null,"errors":["The update key 'Nexus:x' is invalid."]},{"id":"","metadata":{"name":"ignored"}}]`))
	if err != nil {
		t.Fatalf("decodeLookupResponse: %v", err)
	}
	want := map[string]ModInfo{"a.b": {ID: "A.B", Errors: []string{"The update key 'Nexus:x' is invalid."}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decodeLookupResponse = %#v, want %#v", got, want)
	}
	if _, err := decodeLookupResponse([]byte(`{"error":"not an array"}`)); err == nil {
		t.Fatal("decodeLookupResponse(object) error = nil, want error")
	}
}

// TestLookupBatches verifies requests are split into batches of at most 100 mods.
func TestLookupBatches(t *testing.T) {
	handler, server := newLookupServer(t)
	client := newTestClient(server, t.TempDir(), newFakeClock())
	var mods []ModQuery
	for i := 0; i < 250; i++ {
		mods = append(mods, ModQuery{ID: fmt.Sprintf("mod.%03d", i)})
	}
	got, err := client.Lookup(context.Background(), lookupRequest(mods...))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 250 {
		t.Fatalf("Lookup returned %d mods, want 250", len(got))
	}
	var sizes []int
	for _, request := range handler.captured() {
		sizes = append(sizes, len(request.IDs))
	}
	if want := []int{100, 100, 50}; !reflect.DeepEqual(sizes, want) {
		t.Fatalf("batch sizes = %v, want %v", sizes, want)
	}
}

// TestLookupDeduplicatesQueries verifies duplicate and blank mod IDs are not sent.
func TestLookupDeduplicatesQueries(t *testing.T) {
	handler, server := newLookupServer(t, "a.mod")
	client := newTestClient(server, "", nil)
	got, err := client.Lookup(context.Background(), lookupRequest(ModQuery{ID: "A.Mod"}, ModQuery{ID: "a.mod"}, ModQuery{ID: "  "}))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	requests := handler.captured()
	if len(requests) != 1 || !reflect.DeepEqual(requests[0].IDs, []string{"A.Mod"}) {
		t.Fatalf("requests = %#v, want one request for A.Mod", requests)
	}
	if len(got) != 1 || !got["a.mod"].Known {
		t.Fatalf("Lookup = %#v", got)
	}
}

// TestLookupCacheHit verifies cached mods are served without a request, including after reopening the cache.
func TestLookupCacheHit(t *testing.T) {
	handler, server := newLookupServer(t, "a.mod")
	dir := t.TempDir()
	clock := newFakeClock()
	request := lookupRequest(ModQuery{ID: "a.mod", InstalledVersion: "1.0.0", UpdateKeys: []string{"Nexus:1"}}, ModQuery{ID: "b.unknown"})
	first, err := newTestClient(server, dir, clock).Lookup(context.Background(), request)
	if err != nil {
		t.Fatalf("first Lookup: %v", err)
	}
	client := newTestClient(server, dir, clock)
	second, err := client.Lookup(context.Background(), request)
	if err != nil {
		t.Fatalf("second Lookup: %v", err)
	}
	third, err := client.Lookup(context.Background(), request)
	if err != nil {
		t.Fatalf("third Lookup: %v", err)
	}
	if n := len(handler.captured()); n != 1 {
		t.Fatalf("server received %d requests, want 1", n)
	}
	if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(first, third) {
		t.Fatalf("cached results differ:\n%#v\n%#v\n%#v", first, second, third)
	}
	if first["a.mod"].SuggestedUpdateVersion != "2.0.0" || first["b.unknown"].Known {
		t.Fatalf("first = %#v", first)
	}
}

// TestLookupCacheTTL verifies unknown results expire after 6 hours and known results after 24 hours.
func TestLookupCacheTTL(t *testing.T) {
	handler, server := newLookupServer(t, "known.mod")
	clock := newFakeClock()
	client := newTestClient(server, t.TempDir(), clock)
	request := lookupRequest(ModQuery{ID: "known.mod"}, ModQuery{ID: "unknown.mod"})
	steps := []struct {
		advance time.Duration
		wantIDs []string
	}{
		{advance: 0, wantIDs: []string{"known.mod", "unknown.mod"}},
		{advance: 5 * time.Hour, wantIDs: nil},
		{advance: 2 * time.Hour, wantIDs: []string{"unknown.mod"}},
		{advance: 16 * time.Hour, wantIDs: []string{"unknown.mod"}},
		{advance: 2 * time.Hour, wantIDs: []string{"known.mod"}},
	}
	for i, step := range steps {
		clock.Advance(step.advance)
		before := len(handler.captured())
		got, err := client.Lookup(context.Background(), request)
		if err != nil {
			t.Fatalf("step %d Lookup: %v", i, err)
		}
		if !got["known.mod"].Known || got["unknown.mod"].Known {
			t.Fatalf("step %d Lookup = %#v", i, got)
		}
		requests := handler.captured()[before:]
		var ids []string
		for _, request := range requests {
			ids = append(ids, request.IDs...)
		}
		if !reflect.DeepEqual(ids, step.wantIDs) {
			t.Fatalf("step %d requested %v, want %v", i, ids, step.wantIDs)
		}
	}
}

// TestLookupUpdateKeyChange verifies a changed update context misses the update cache but still hits the static cache.
func TestLookupUpdateKeyChange(t *testing.T) {
	handler, server := newLookupServer(t, "a.mod")
	client := newTestClient(server, t.TempDir(), newFakeClock())
	original := ModQuery{ID: "a.mod", InstalledVersion: "1.0.0", UpdateKeys: []string{"Nexus:1", "GitHub:a/b"}}
	if _, err := client.Lookup(context.Background(), lookupRequest(original)); err != nil {
		t.Fatalf("first Lookup: %v", err)
	}
	handler.set(http.StatusInternalServerError, nil)
	changed := original
	changed.InstalledVersion = "1.1.0"
	got, err := client.Lookup(context.Background(), lookupRequest(changed))
	if err == nil {
		t.Fatal("Lookup with changed installed version error = nil, want the server error")
	}
	if n := len(handler.captured()); n != 2 {
		t.Fatalf("server received %d requests, want 2", n)
	}
	info := got["a.mod"]
	if !info.Known || info.Name != "a.mod Name" || info.SuggestedUpdateVersion != "" {
		t.Fatalf("static-only cached info = %#v", info)
	}
	reordered := ModQuery{ID: "A.MOD", InstalledVersion: "1.0.0", UpdateKeys: []string{"github:A/B", "NEXUS:1"}}
	got, err = client.Lookup(context.Background(), lookupRequest(reordered))
	if err != nil {
		t.Fatalf("Lookup with equivalent update keys: %v", err)
	}
	if n := len(handler.captured()); n != 2 {
		t.Fatalf("server received %d requests, want 2", n)
	}
	if got["a.mod"].SuggestedUpdateVersion != "2.0.0" {
		t.Fatalf("fully cached info = %#v", got["a.mod"])
	}
	otherGame := lookupRequest(original)
	otherGame.GameVersion = "1.6.16"
	if _, err := client.Lookup(context.Background(), otherGame); err == nil {
		t.Fatal("Lookup with changed game version error = nil, want a request and the server error")
	}
}

// TestLookupForce verifies Force bypasses fresh cache entries.
func TestLookupForce(t *testing.T) {
	handler, server := newLookupServer(t, "a.mod")
	client := newTestClient(server, t.TempDir(), newFakeClock())
	request := lookupRequest(ModQuery{ID: "a.mod"})
	if _, err := client.Lookup(context.Background(), request); err != nil {
		t.Fatalf("first Lookup: %v", err)
	}
	request.Force = true
	if _, err := client.Lookup(context.Background(), request); err != nil {
		t.Fatalf("forced Lookup: %v", err)
	}
	if n := len(handler.captured()); n != 2 {
		t.Fatalf("server received %d requests, want 2", n)
	}
}

// TestLookupServerErrorReturnsCache verifies HTTP failures return cached data together with the error.
func TestLookupServerErrorReturnsCache(t *testing.T) {
	handler, server := newLookupServer(t, "a.mod", "b.mod")
	client := newTestClient(server, t.TempDir(), newFakeClock())
	if _, err := client.Lookup(context.Background(), lookupRequest(ModQuery{ID: "a.mod"})); err != nil {
		t.Fatalf("first Lookup: %v", err)
	}
	handler.set(http.StatusInternalServerError, nil)
	got, err := client.Lookup(context.Background(), lookupRequest(ModQuery{ID: "a.mod"}, ModQuery{ID: "b.mod"}))
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("Lookup error = %v, want HTTP 500", err)
	}
	if len(got) != 1 || !got["a.mod"].Known {
		t.Fatalf("Lookup = %#v, want only cached a.mod", got)
	}
	requests := handler.captured()
	if last := requests[len(requests)-1]; !reflect.DeepEqual(last.IDs, []string{"b.mod"}) {
		t.Fatalf("last request IDs = %v, want only the uncached b.mod", last.IDs)
	}
}

// TestLookupOversizedResponse verifies responses above 8 MiB are rejected.
func TestLookupOversizedResponse(t *testing.T) {
	handler, server := newLookupServer(t)
	handler.set(0, []byte("["+strings.Repeat(" ", maxLookupResponse)+"]"))
	client := newTestClient(server, t.TempDir(), newFakeClock())
	got, err := client.Lookup(context.Background(), lookupRequest(ModQuery{ID: "a.mod"}))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Lookup error = %v, want size limit error", err)
	}
	if len(got) != 0 {
		t.Fatalf("Lookup = %#v, want empty", got)
	}
}

// TestLookupCanceledContext verifies a canceled context fails the request.
func TestLookupCanceledContext(t *testing.T) {
	_, server := newLookupServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newTestClient(server, "", nil).Lookup(ctx, lookupRequest(ModQuery{ID: "a.mod"})); err == nil {
		t.Fatal("Lookup with canceled context error = nil, want error")
	}
}

// TestLookupNilCache verifies a client without a cache always queries the server.
func TestLookupNilCache(t *testing.T) {
	handler, server := newLookupServer(t, "a.mod")
	client := &Client{BaseURL: server.URL + "/api/", HTTP: server.Client()}
	for i := 0; i < 2; i++ {
		if _, err := client.Lookup(context.Background(), lookupRequest(ModQuery{ID: "a.mod"})); err != nil {
			t.Fatalf("Lookup %d: %v", i, err)
		}
	}
	if n := len(handler.captured()); n != 2 {
		t.Fatalf("server received %d requests, want 2", n)
	}
}

// TestLookupConcurrent verifies concurrent lookups sharing one cache are race-free.
func TestLookupConcurrent(t *testing.T) {
	_, server := newLookupServer(t, "a.mod", "c.mod")
	client := newTestClient(server, t.TempDir(), newFakeClock())
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := client.Lookup(context.Background(), lookupRequest(ModQuery{ID: "a.mod"}, ModQuery{ID: fmt.Sprintf("b.%d", i)}, ModQuery{ID: "c.mod"}))
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Lookup: %v", err)
		}
	}
}

// TestCacheFileFormat verifies the persisted cache document layout, keys and permissions.
func TestCacheFileFormat(t *testing.T) {
	_, server := newLookupServer(t, "a.mod")
	dir := filepath.Join(t.TempDir(), "nested", "smapi")
	client := newTestClient(server, dir, newFakeClock())
	if _, err := client.Lookup(context.Background(), lookupRequest(ModQuery{ID: "A.Mod", InstalledVersion: "1.0.0", UpdateKeys: []string{"Nexus:1", "GitHub:x/y"}})); err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	path := filepath.Join(dir, cacheFileName)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s): %v", path, err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cache file mode = %v, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var document struct {
		SchemaVersion int                        `json:"schema_version"`
		Static        map[string]json.RawMessage `json:"static"`
		Updates       map[string]json.RawMessage `json:"updates"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("cache file is not JSON: %v", err)
	}
	if document.SchemaVersion != 1 || len(document.Static) != 1 || document.Static["a.mod"] == nil {
		t.Fatalf("cache document = %s", data)
	}
	wantKey := "a.mod|1.0.0|github:x/y,nexus:1|4.5.2|1.6.15"
	if len(document.Updates) != 1 || document.Updates[wantKey] == nil {
		t.Fatalf("cache updates = %s, want key %q", data, wantKey)
	}
}

// cachedStaticIDs returns the sorted static IDs persisted in the cache file under dir.
func cachedStaticIDs(dir string) []string {
	var ids []string
	for id := range OpenCache(dir).static {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// TestCachePrunesExpiredEntries verifies expired entries stay on disk as fallbacks until they pass the 30-day retention.
func TestCachePrunesExpiredEntries(t *testing.T) {
	_, server := newLookupServer(t)
	dir := t.TempDir()
	clock := newFakeClock()
	client := newTestClient(server, dir, clock)
	if _, err := client.Lookup(context.Background(), lookupRequest(ModQuery{ID: "old.mod"})); err != nil {
		t.Fatalf("first Lookup: %v", err)
	}
	clock.Advance(7 * time.Hour)
	if _, err := client.Lookup(context.Background(), lookupRequest(ModQuery{ID: "new.mod"})); err != nil {
		t.Fatalf("second Lookup: %v", err)
	}
	if ids := cachedStaticIDs(dir); !reflect.DeepEqual(ids, []string{"new.mod", "old.mod"}) {
		t.Fatalf("cached static IDs after 7h = %v, want both kept", ids)
	}
	clock.Advance(cacheRetention - 7*time.Hour)
	if _, err := client.Lookup(context.Background(), lookupRequest(ModQuery{ID: "third.mod"})); err != nil {
		t.Fatalf("third Lookup: %v", err)
	}
	if ids := cachedStaticIDs(dir); !reflect.DeepEqual(ids, []string{"new.mod", "third.mod"}) {
		t.Fatalf("cached static IDs after 30 days = %v, want [new.mod third.mod]", ids)
	}
	if n := len(OpenCache(dir).updates); n != 2 {
		t.Fatalf("cached update entries = %d, want 2", n)
	}
}

// TestLookupServerErrorReturnsStaleCache verifies HTTP failures fall back to expired entries within the retention window.
func TestLookupServerErrorReturnsStaleCache(t *testing.T) {
	handler, server := newLookupServer(t, "a.mod")
	dir := t.TempDir()
	clock := newFakeClock()
	request := lookupRequest(ModQuery{ID: "a.mod", InstalledVersion: "1.0.0", UpdateKeys: []string{"Nexus:1"}}, ModQuery{ID: "u.mod"})
	fresh, err := newTestClient(server, dir, clock).Lookup(context.Background(), request)
	if err != nil {
		t.Fatalf("first Lookup: %v", err)
	}
	if fresh["a.mod"].Stale || fresh["u.mod"].Stale {
		t.Fatalf("fresh Lookup = %#v, want no stale entries", fresh)
	}
	handler.set(http.StatusServiceUnavailable, nil)
	clock.Advance(3 * 24 * time.Hour)
	got, err := newTestClient(server, dir, clock).Lookup(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("Lookup error = %v, want HTTP 503", err)
	}
	wantA := fresh["a.mod"]
	wantA.Stale = true
	wantU := fresh["u.mod"]
	wantU.Stale = true
	if want := map[string]ModInfo{"a.mod": wantA, "u.mod": wantU}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stale Lookup = %#v, want %#v", got, want)
	}
	if got["a.mod"].SuggestedUpdateVersion != "2.0.0" || !got["a.mod"].Known {
		t.Fatalf("stale a.mod = %#v, want the cached update suggestion", got["a.mod"])
	}
	clock.Advance(cacheRetention - 3*24*time.Hour)
	got, err = newTestClient(server, dir, clock).Lookup(context.Background(), request)
	if err == nil || len(got) != 0 {
		t.Fatalf("Lookup past retention = %#v, %v; want no entries and an error", got, err)
	}
}

// TestLookupServerErrorMarksExpiredUpdateStale verifies a fresh static entry with an expired update part is returned stale on failure.
func TestLookupServerErrorMarksExpiredUpdateStale(t *testing.T) {
	handler, server := newLookupServer(t, "a.mod")
	clock := newFakeClock()
	client := newTestClient(server, t.TempDir(), clock)
	original := ModQuery{ID: "a.mod", InstalledVersion: "1.0.0", UpdateKeys: []string{"Nexus:1"}}
	other := ModQuery{ID: "a.mod", InstalledVersion: "1.1.0", UpdateKeys: []string{"Nexus:1"}}
	if _, err := client.Lookup(context.Background(), lookupRequest(original)); err != nil {
		t.Fatalf("first Lookup: %v", err)
	}
	clock.Advance(23 * time.Hour)
	if _, err := client.Lookup(context.Background(), lookupRequest(other)); err != nil {
		t.Fatalf("second Lookup: %v", err)
	}
	clock.Advance(2 * time.Hour)
	handler.set(http.StatusInternalServerError, nil)
	got, err := client.Lookup(context.Background(), lookupRequest(original))
	if err == nil {
		t.Fatal("Lookup error = nil, want the server error")
	}
	info := got["a.mod"]
	if !info.Stale || !info.Known || info.SuggestedUpdateVersion != "2.0.0" {
		t.Fatalf("Lookup = %#v, want a stale entry with the expired update suggestion", info)
	}
	if n := len(handler.captured()); n != 3 {
		t.Fatalf("server received %d requests, want 3", n)
	}
}

// TestLookupForcedFailureKeepsFreshEntries verifies a forced lookup that fails still returns fresh cached entries unmarked.
func TestLookupForcedFailureKeepsFreshEntries(t *testing.T) {
	handler, server := newLookupServer(t, "a.mod")
	client := newTestClient(server, t.TempDir(), newFakeClock())
	request := lookupRequest(ModQuery{ID: "a.mod", InstalledVersion: "1.0.0"})
	fresh, err := client.Lookup(context.Background(), request)
	if err != nil {
		t.Fatalf("first Lookup: %v", err)
	}
	handler.set(http.StatusInternalServerError, nil)
	request.Force = true
	got, err := client.Lookup(context.Background(), request)
	if err == nil {
		t.Fatal("forced Lookup error = nil, want the server error")
	}
	if !reflect.DeepEqual(got, fresh) {
		t.Fatalf("forced Lookup = %#v, want %#v", got, fresh)
	}
}

// TestLookupErroredUpdatesExpireQuickly verifies update results that carry smapi.io errors are cached for 15 minutes only.
func TestLookupErroredUpdatesExpireQuickly(t *testing.T) {
	handler, server := newLookupServer(t)
	handler.set(0, []byte(`[{"id":"a.mod","metadata":{"id":["a.mod"],"name":"A"},"errors":["The Nexus mod with ID 1 was not found."]},{"id":"b.mod","metadata":{"id":["b.mod"],"name":"B"},"errors":[]}]`))
	clock := newFakeClock()
	client := newTestClient(server, t.TempDir(), clock)
	request := lookupRequest(ModQuery{ID: "a.mod", UpdateKeys: []string{"Nexus:1"}}, ModQuery{ID: "b.mod", UpdateKeys: []string{"Nexus:2"}})
	steps := []struct {
		advance time.Duration
		wantIDs []string
	}{
		{advance: 0, wantIDs: []string{"a.mod", "b.mod"}},
		{advance: 14 * time.Minute, wantIDs: nil},
		{advance: time.Minute, wantIDs: []string{"a.mod"}},
		{advance: 10 * time.Minute, wantIDs: nil},
	}
	for i, step := range steps {
		clock.Advance(step.advance)
		before := len(handler.captured())
		got, err := client.Lookup(context.Background(), request)
		if err != nil {
			t.Fatalf("step %d Lookup: %v", i, err)
		}
		if !reflect.DeepEqual(got["a.mod"].Errors, []string{"The Nexus mod with ID 1 was not found."}) {
			t.Fatalf("step %d a.mod = %#v, want its errors", i, got["a.mod"])
		}
		var ids []string
		for _, captured := range handler.captured()[before:] {
			ids = append(ids, captured.IDs...)
		}
		if !reflect.DeepEqual(ids, step.wantIDs) {
			t.Fatalf("step %d requested %v, want %v", i, ids, step.wantIDs)
		}
	}
}

// TestLookupOmittedIDsNotCached verifies IDs missing from a successful response are not cached and fall back to stale data.
func TestLookupOmittedIDsNotCached(t *testing.T) {
	handler, server := newLookupServer(t, "c.mod")
	dir := t.TempDir()
	clock := newFakeClock()
	client := newTestClient(server, dir, clock)
	if _, err := client.Lookup(context.Background(), lookupRequest(ModQuery{ID: "c.mod"})); err != nil {
		t.Fatalf("seeding Lookup: %v", err)
	}
	clock.Advance(25 * time.Hour)
	handler.set(0, []byte(`[{"id":"a.mod","metadata":{"id":["a.mod"],"name":"A"}}]`))
	request := lookupRequest(ModQuery{ID: "a.mod"}, ModQuery{ID: "b.mod"}, ModQuery{ID: "c.mod"})
	got, err := client.Lookup(context.Background(), request)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !got["a.mod"].Known || got["a.mod"].Stale {
		t.Fatalf("a.mod = %#v, want a fresh known entry", got["a.mod"])
	}
	if !reflect.DeepEqual(got["b.mod"], ModInfo{ID: "b.mod"}) {
		t.Fatalf("b.mod = %#v, want an unknown placeholder", got["b.mod"])
	}
	if c := got["c.mod"]; !c.Known || !c.Stale {
		t.Fatalf("c.mod = %#v, want the stale cached entry", c)
	}
	if ids := cachedStaticIDs(dir); !reflect.DeepEqual(ids, []string{"a.mod", "c.mod"}) {
		t.Fatalf("cached static IDs = %v, want [a.mod c.mod]", ids)
	}
	before := len(handler.captured())
	if _, err := client.Lookup(context.Background(), request); err != nil {
		t.Fatalf("second Lookup: %v", err)
	}
	requests := handler.captured()[before:]
	if len(requests) != 1 || !reflect.DeepEqual(requests[0].IDs, []string{"b.mod", "c.mod"}) {
		t.Fatalf("second Lookup requests = %#v, want one request for the uncached b.mod and c.mod", requests)
	}
}

// TestLookupNormalizesVersions verifies the API and game versions are sent as major.minor.patch with any prerelease tag.
func TestLookupNormalizesVersions(t *testing.T) {
	cases := []struct {
		api, game         string
		wantAPI, wantGame string
	}{
		{api: "4.5.2", game: "1.6.15.24356", wantAPI: "4.5.2", wantGame: "1.6.15"},
		{api: "4.5.2-beta.1+sha.abc", game: "1.6", wantAPI: "4.5.2-beta.1", wantGame: "1.6.0"},
		{api: " 4.5 ", game: "1.6.15.3-beta+b", wantAPI: "4.5.0", wantGame: "1.6.15-beta"},
		{api: "", game: "custom", wantAPI: "", wantGame: "custom"},
	}
	for _, tc := range cases {
		handler, server := newLookupServer(t)
		client := newTestClient(server, t.TempDir(), newFakeClock())
		request := LookupRequest{Mods: []ModQuery{{ID: "a.mod"}}, APIVersion: tc.api, GameVersion: tc.game}
		if _, err := client.Lookup(context.Background(), request); err != nil {
			t.Fatalf("Lookup(%q, %q): %v", tc.api, tc.game, err)
		}
		var body struct {
			APIVersion  string `json:"apiVersion"`
			GameVersion string `json:"gameVersion"`
		}
		requests := handler.captured()
		if err := json.Unmarshal(requests[0].Body, &body); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		if body.APIVersion != tc.wantAPI || body.GameVersion != tc.wantGame {
			t.Fatalf("Lookup(%q, %q) sent apiVersion %q gameVersion %q, want %q and %q", tc.api, tc.game, body.APIVersion, body.GameVersion, tc.wantAPI, tc.wantGame)
		}
		request.GameVersion = tc.wantGame
		request.APIVersion = tc.wantAPI
		if _, err := client.Lookup(context.Background(), request); err != nil {
			t.Fatalf("repeat Lookup: %v", err)
		}
		if n := len(handler.captured()); n != 1 {
			t.Fatalf("normalized repeat Lookup sent %d requests, want a cache hit", n)
		}
	}
}

// TestDefaultHTTPClientShared verifies the fallback HTTP client is created once and reused.
func TestDefaultHTTPClientShared(t *testing.T) {
	first := defaultHTTPClient()
	if first == nil || first != defaultHTTPClient() {
		t.Fatal("defaultHTTPClient() did not return one shared client")
	}
}

// TestOpenCacheTolerant verifies corrupt, foreign-schema and missing cache files start an empty cache.
func TestOpenCacheTolerant(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{name: "corrupt", content: "not json {"},
		{name: "wrong schema", content: `{"schema_version":99,"static":{"a.mod":{"id":"a.mod","known":true,"stored_at":"2026-09-25T12:00:00Z"}}}`},
		{name: "empty", content: ""},
		{name: "missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.name != "missing" {
				writeTree(t, dir, map[string]string{cacheFileName: tc.content})
			}
			handler, server := newLookupServer(t, "a.mod")
			client := newTestClient(server, dir, newFakeClock())
			got, err := client.Lookup(context.Background(), lookupRequest(ModQuery{ID: "a.mod"}))
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			if n := len(handler.captured()); n != 1 || !got["a.mod"].Known {
				t.Fatalf("requests = %d, result = %#v", n, got)
			}
			if reopened := OpenCache(dir); len(reopened.static) != 1 {
				t.Fatalf("rewritten cache has %d static entries, want 1", len(reopened.static))
			}
		})
	}
}

// TestCacheFutureTimestampIsStale verifies entries stored in the future are not trusted.
func TestCacheFutureTimestampIsStale(t *testing.T) {
	cache := OpenCache("")
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	cache.store([]cacheWrite{{id: "a.mod", updateKey: "k", info: ModInfo{ID: "a.mod", Known: true}}}, now.Add(time.Hour))
	if cached := cache.lookup("a.mod", "k", now); cached.hasStatic || cached.hasUpdate || cached.staticFresh || cached.updateFresh {
		t.Fatalf("lookup of future entry = %#v; want nothing usable", cached)
	}
	if cached := cache.lookup("a.mod", "k", now.Add(2*time.Hour)); !cached.staticFresh || !cached.updateFresh {
		t.Fatalf("lookup of fresh entry = %#v; want fresh", cached)
	}
}

// TestDefaultCacheDir verifies the cache lives under the user cache directory.
func TestDefaultCacheDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", root)
	got, err := DefaultCacheDir()
	if err != nil {
		t.Fatalf("DefaultCacheDir: %v", err)
	}
	if want := filepath.Join(root, "gorganizer", "smapi"); got != want {
		t.Fatalf("DefaultCacheDir = %q, want %q", got, want)
	}
}

// TestUpdateCacheKey verifies the update cache key normalizes IDs and update keys.
func TestUpdateCacheKey(t *testing.T) {
	req := LookupRequest{APIVersion: "4.5.2", GameVersion: "1.6.15"}
	a := updateCacheKey(ModQuery{ID: "A.Mod", InstalledVersion: "1.0", UpdateKeys: []string{"Nexus:1", "GitHub:X/Y"}}, req)
	b := updateCacheKey(ModQuery{ID: "a.mod", InstalledVersion: "1.0", UpdateKeys: []string{"github:x/y", "NEXUS:1"}}, req)
	if a != b || a != "a.mod|1.0|github:x/y,nexus:1|4.5.2|1.6.15" {
		t.Fatalf("updateCacheKey = %q and %q", a, b)
	}
	if c := updateCacheKey(ModQuery{ID: "a.mod", InstalledVersion: "1.1", UpdateKeys: []string{"Nexus:1"}}, req); c == a {
		t.Fatalf("updateCacheKey ignored the installed version: %q", c)
	}
}

// TestCachedAnswersFromTheCacheWithoutNetwork verifies Cached never contacts smapi.io and flags expired entries stale.
func TestCachedAnswersFromTheCacheWithoutNetwork(t *testing.T) {
	handler, server := newLookupServer(t, "a.mod")
	dir := t.TempDir()
	clock := newFakeClock()
	request := lookupRequest(ModQuery{ID: "a.mod", InstalledVersion: "1.0.0", UpdateKeys: []string{"Nexus:1"}}, ModQuery{ID: "u.mod"})
	client := newTestClient(server, dir, clock)
	if got := client.Cached(request); len(got) != 0 {
		t.Fatalf("Cached before any lookup = %#v, want empty", got)
	}
	fresh, err := client.Lookup(context.Background(), request)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	requests := len(handler.captured())
	got := newTestClient(server, dir, clock).Cached(lookupRequest(ModQuery{ID: "A.Mod", InstalledVersion: "1.0.0", UpdateKeys: []string{"nexus:1"}}, ModQuery{ID: "u.mod"}, ModQuery{ID: "missing.mod"}))
	if !reflect.DeepEqual(got, fresh) {
		t.Fatalf("Cached = %#v, want the fresh lookup %#v", got, fresh)
	}
	clock.Advance(2 * 24 * time.Hour)
	got = client.Cached(request)
	if !got["a.mod"].Stale || !got["u.mod"].Stale || got["a.mod"].NexusID != 7 || got["a.mod"].SuggestedUpdateVersion != "2.0.0" {
		t.Fatalf("expired Cached = %#v, want stale entries keeping their metadata", got)
	}
	if n := len(handler.captured()); n != requests {
		t.Fatalf("Cached contacted smapi.io: %d requests, want %d", n, requests)
	}
	var nilCache Client
	if got := nilCache.Cached(request); len(got) != 0 {
		t.Fatalf("Cached without a cache = %#v, want empty", got)
	}
}

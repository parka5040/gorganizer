package smapi

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/parka/gorganizer/internal/atomicfile"
)

const (
	cacheFileName      = "metadata.json"
	cacheSchemaVersion = 1
	knownStaticTTL     = 24 * time.Hour
	unknownStaticTTL   = 6 * time.Hour
	updateTTL          = 24 * time.Hour
	erroredUpdateTTL   = 15 * time.Minute
	cacheRetention     = 30 * 24 * time.Hour
)

type staticEntry struct {
	ID                   string    `json:"id"`
	Known                bool      `json:"known"`
	Name                 string    `json:"name,omitempty"`
	NexusID              int       `json:"nexus_id,omitempty"`
	MainVersion          string    `json:"main_version,omitempty"`
	MainURL              string    `json:"main_url,omitempty"`
	CompatibilityStatus  string    `json:"compatibility_status,omitempty"`
	CompatibilitySummary string    `json:"compatibility_summary,omitempty"`
	StoredAt             time.Time `json:"stored_at"`
}

type updateEntry struct {
	SuggestedUpdateVersion string    `json:"suggested_update_version,omitempty"`
	SuggestedUpdateURL     string    `json:"suggested_update_url,omitempty"`
	Errors                 []string  `json:"errors,omitempty"`
	StoredAt               time.Time `json:"stored_at"`
}

type cacheDocument struct {
	SchemaVersion int                    `json:"schema_version"`
	Static        map[string]staticEntry `json:"static"`
	Updates       map[string]updateEntry `json:"updates"`
}

type cachedInfo struct {
	static      staticEntry
	update      updateEntry
	hasStatic   bool
	hasUpdate   bool
	staticFresh bool
	updateFresh bool
}

type Cache struct {
	mu      sync.Mutex
	dir     string
	static  map[string]staticEntry
	updates map[string]updateEntry
}

// OpenCache loads the smapi.io cache from dir, starting empty when the file is missing or corrupt.
func OpenCache(dir string) *Cache {
	cache := &Cache{dir: dir, static: map[string]staticEntry{}, updates: map[string]updateEntry{}}
	if dir == "" {
		return cache
	}
	data, err := os.ReadFile(filepath.Join(dir, cacheFileName))
	if err != nil {
		return cache
	}
	var document cacheDocument
	if json.Unmarshal(data, &document) != nil || document.SchemaVersion != cacheSchemaVersion {
		return cache
	}
	for id, entry := range document.Static {
		cache.static[id] = entry
	}
	for key, entry := range document.Updates {
		cache.updates[key] = entry
	}
	return cache
}

// DefaultCacheDir returns the per-user smapi.io cache directory.
func DefaultCacheDir() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locating user cache directory: %w", err)
	}
	return filepath.Join(root, "gorganizer", "smapi"), nil
}

// lookup returns the entries retained for id and updateKey at now and whether each is still within its TTL.
func (c *Cache) lookup(id, updateKey string, now time.Time) cachedInfo {
	if c == nil {
		return cachedInfo{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var cached cachedInfo
	cached.static, cached.hasStatic = c.static[id]
	cached.hasStatic = cached.hasStatic && retained(cached.static.StoredAt, now)
	cached.staticFresh = cached.hasStatic && cached.static.fresh(now)
	cached.update, cached.hasUpdate = c.updates[updateKey]
	cached.hasUpdate = cached.hasUpdate && retained(cached.update.StoredAt, now)
	cached.updateFresh = cached.hasUpdate && cached.update.fresh(now)
	return cached
}

// store records freshly fetched results, prunes entries past the retention window and persists the cache once.
func (c *Cache) store(writes []cacheWrite, now time.Time) {
	if c == nil || len(writes) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, write := range writes {
		info := write.info
		c.static[write.id] = staticEntry{
			ID:                   info.ID,
			Known:                info.Known,
			Name:                 info.Name,
			NexusID:              info.NexusID,
			MainVersion:          info.MainVersion,
			MainURL:              info.MainURL,
			CompatibilityStatus:  info.CompatibilityStatus,
			CompatibilitySummary: info.CompatibilitySummary,
			StoredAt:             now,
		}
		c.updates[write.updateKey] = updateEntry{
			SuggestedUpdateVersion: info.SuggestedUpdateVersion,
			SuggestedUpdateURL:     info.SuggestedUpdateURL,
			Errors:                 append([]string(nil), info.Errors...),
			StoredAt:               now,
		}
	}
	for id, entry := range c.static {
		if !retained(entry.StoredAt, now) {
			delete(c.static, id)
		}
	}
	for key, entry := range c.updates {
		if !retained(entry.StoredAt, now) {
			delete(c.updates, key)
		}
	}
	c.saveLocked()
}

// saveLocked writes the cache file atomically, ignoring failures because the cache is disposable.
func (c *Cache) saveLocked() {
	if c.dir == "" || os.MkdirAll(c.dir, 0o700) != nil {
		return
	}
	data, err := json.Marshal(cacheDocument{SchemaVersion: cacheSchemaVersion, Static: c.static, Updates: c.updates})
	if err != nil {
		return
	}
	_ = atomicfile.WriteFile(filepath.Join(c.dir, cacheFileName), data, 0o600)
}

// fresh reports whether a static entry is within the TTL for known or unknown mods.
func (e staticEntry) fresh(now time.Time) bool {
	ttl := unknownStaticTTL
	if e.Known {
		ttl = knownStaticTTL
	}
	return fresh(e.StoredAt, now, ttl)
}

// fresh reports whether an update entry is within its TTL, which is short when smapi.io reported errors.
func (e updateEntry) fresh(now time.Time) bool {
	ttl := updateTTL
	if len(e.Errors) != 0 {
		ttl = erroredUpdateTTL
	}
	return fresh(e.StoredAt, now, ttl)
}

// fresh reports whether storedAt lies within ttl before now and not in the future.
func fresh(storedAt, now time.Time, ttl time.Duration) bool {
	return !storedAt.IsZero() && !storedAt.After(now) && now.Sub(storedAt) < ttl
}

// retained reports whether an entry stored at storedAt is still kept as a stale fallback at now.
func retained(storedAt, now time.Time) bool {
	return fresh(storedAt, now, cacheRetention)
}

// freshInfo returns the cached static info with the update part only while it is fresh.
func (c cachedInfo) freshInfo() ModInfo {
	info := c.static.info()
	if c.updateFresh {
		c.update.apply(&info)
	}
	return info
}

// fallback returns every retained part for use when smapi.io gives no answer, marking it stale when any part expired.
func (c cachedInfo) fallback() (ModInfo, bool) {
	if !c.hasStatic {
		return ModInfo{}, false
	}
	info := c.static.info()
	if c.hasUpdate {
		c.update.apply(&info)
	}
	info.Stale = !c.staticFresh || c.hasUpdate && !c.updateFresh
	return info, true
}

// info converts a static entry into ModInfo without update data.
func (e staticEntry) info() ModInfo {
	return ModInfo{
		ID:                   e.ID,
		Known:                e.Known,
		Name:                 e.Name,
		NexusID:              e.NexusID,
		MainVersion:          e.MainVersion,
		MainURL:              e.MainURL,
		CompatibilityStatus:  e.CompatibilityStatus,
		CompatibilitySummary: e.CompatibilitySummary,
	}
}

// apply copies the update suggestion and errors into info.
func (e updateEntry) apply(info *ModInfo) {
	info.SuggestedUpdateVersion = e.SuggestedUpdateVersion
	info.SuggestedUpdateURL = e.SuggestedUpdateURL
	info.Errors = append([]string(nil), e.Errors...)
}

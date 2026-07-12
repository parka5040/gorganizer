package daemon

import (
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
)

type previewEntry struct {
	GameID         string
	ArchiveRelPath string
	ExtractRoot    string
	CreatedAt      time.Time
	HasFomod       bool
	ModuleRoot     string

	leases       int
	pendingEvict bool
}

type previewCache struct {
	mu      sync.Mutex
	entries map[string]*previewEntry
	ttl     time.Duration
	maxLen  int
}

func newPreviewCache(ttl time.Duration, maxLen int) *previewCache {
	return &previewCache{
		entries: make(map[string]*previewEntry),
		ttl:     ttl,
		maxLen:  maxLen,
	}
}

func (c *previewCache) put(entry *previewEntry) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.entries) >= c.maxLen && c.evictOldestUnleasedLocked() {
	}
	id := "prev-" + uuid.NewString()
	entry.CreatedAt = time.Now()
	c.entries[id] = entry
	return id
}

// evictOldestUnleasedLocked removes the oldest entry that has no active lease; caller holds c.mu.
func (c *previewCache) evictOldestUnleasedLocked() bool {
	var oldestID string
	var oldestT time.Time
	for id, e := range c.entries {
		if e.leases > 0 || e.pendingEvict {
			continue
		}
		if oldestID == "" || e.CreatedAt.Before(oldestT) {
			oldestID, oldestT = id, e.CreatedAt
		}
	}
	if oldestID == "" {
		return false
	}
	old := c.entries[oldestID]
	delete(c.entries, oldestID)
	os.RemoveAll(old.ExtractRoot)
	return true
}

func (c *previewCache) get(id string) *previewEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[id]
	if !ok {
		return nil
	}
	e.CreatedAt = time.Now()
	return e
}

// acquire takes a lease on the entry so its ExtractRoot cannot be removed while read; pair with release.
func (c *previewCache) acquire(id string) *previewEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[id]
	if !ok {
		return nil
	}
	e.leases++
	e.CreatedAt = time.Now()
	return e
}

// release drops a lease; the final release of an eviction-marked entry removes it exactly once.
func (c *previewCache) release(id string) {
	c.mu.Lock()
	var toRemove string
	if e, ok := c.entries[id]; ok {
		if e.leases > 0 {
			e.leases--
		}
		if e.leases == 0 && e.pendingEvict {
			delete(c.entries, id)
			toRemove = e.ExtractRoot
		}
	}
	c.mu.Unlock()
	if toRemove != "" {
		os.RemoveAll(toRemove)
	}
}

func (c *previewCache) discard(id string) bool {
	c.mu.Lock()
	var toRemove string
	e, ok := c.entries[id]
	if ok {
		if e.leases > 0 {
			e.pendingEvict = true
			ok = false
		} else {
			delete(c.entries, id)
			toRemove = e.ExtractRoot
		}
	}
	c.mu.Unlock()
	if toRemove != "" {
		os.RemoveAll(toRemove)
		return true
	}
	return ok
}

// sweep evicts expired entries. Called periodically by runPreviewSweeper.
func (c *previewCache) sweep() {
	c.mu.Lock()
	now := time.Now()
	var expired []string
	for id, e := range c.entries {
		if now.Sub(e.CreatedAt) <= c.ttl {
			continue
		}
		if e.leases > 0 {
			e.pendingEvict = true
			continue
		}
		expired = append(expired, e.ExtractRoot)
		delete(c.entries, id)
	}
	c.mu.Unlock()
	for _, root := range expired {
		os.RemoveAll(root)
	}
}

package plugins

import (
	"container/list"
	"context"
	"os"
	"sync"
)

// HeaderCache memoises ParseHeader results by (realPath, mtime, size). The
// daemon parses every enabled plugin's header on profile activation; without
// caching, mounting a 200-mod profile re-reads the same files on every
// status refresh.
//
// Keying on the underlying mod-folder path (Plugin.Source) — never the
// Data/ rename target — survives VFS activate/deactivate cycles where Data/
// is recreated. A plugin file's mtime/size pair changes the moment a user
// reinstalls the mod, so stale entries self-evict on the next stat.
type HeaderCache struct {
	mu      sync.Mutex
	max     int
	entries map[cacheKey]*list.Element
	order   *list.List
}

type cacheKey struct {
	path  string
	mtime int64
	size  int64
}

type cacheValue struct {
	key cacheKey
	hdr *Header
	err error
}

// NewHeaderCache returns a bounded LRU cache. Pass max <= 0 for the default
// (1024 entries — comfortably above any realistic load order).
func NewHeaderCache(max int) *HeaderCache {
	if max <= 0 {
		max = 1024
	}
	return &HeaderCache{
		max:     max,
		entries: make(map[cacheKey]*list.Element, max),
		order:   list.New(),
	}
}

// Get returns the parsed header for path, parsing on miss. Stat failures and
// parse errors are cached too — re-parsing a corrupt plugin every refresh
// would log-spam the Activity Log.
func (c *HeaderCache) Get(ctx context.Context, path string) (*Header, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	key := cacheKey{path: path, mtime: st.ModTime().UnixNano(), size: st.Size()}

	c.mu.Lock()
	if elem, ok := c.entries[key]; ok {
		c.order.MoveToFront(elem)
		v := elem.Value.(*cacheValue)
		c.mu.Unlock()
		return v.hdr, v.err
	}
	c.mu.Unlock()

	hdr, perr := ParseHeader(ctx, path)

	c.mu.Lock()
	defer c.mu.Unlock()
	// Re-check: another goroutine may have populated while we were parsing.
	if elem, ok := c.entries[key]; ok {
		c.order.MoveToFront(elem)
		v := elem.Value.(*cacheValue)
		return v.hdr, v.err
	}
	v := &cacheValue{key: key, hdr: hdr, err: perr}
	c.entries[key] = c.order.PushFront(v)
	if c.order.Len() > c.max {
		oldest := c.order.Back()
		if oldest != nil {
			c.order.Remove(oldest)
			delete(c.entries, oldest.Value.(*cacheValue).key)
		}
	}
	return hdr, perr
}

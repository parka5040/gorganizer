package daemon

import (
	"sync"

	"github.com/parka/gorganizer/internal/dto"
)

type statusCoalescer struct {
	mu     sync.Mutex
	cond   *sync.Cond
	latest map[string]dto.StatusEventResult
	order  []string
	sticky []dto.StatusEventResult
	closed bool
}

func newStatusCoalescer() *statusCoalescer {
	c := &statusCoalescer{
		latest: make(map[string]dto.StatusEventResult),
	}
	c.cond = sync.NewCond(&c.mu)
	return c
}

// Push inserts or updates an event in the coalescer. Never blocks beyond
func (c *statusCoalescer) Push(evt dto.StatusEventResult) {
	id, terminal, coalescable := coalesceKey(evt)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}

	if !coalescable || terminal {
		if id != "" {
			if prev, ok := c.latest[id]; ok {
				c.sticky = append(c.sticky, prev)
				delete(c.latest, id)
				c.order = removeString(c.order, id)
			}
		}
		c.sticky = append(c.sticky, evt)
		c.cond.Signal()
		return
	}

	if _, exists := c.latest[id]; !exists {
		c.order = append(c.order, id)
	}
	c.latest[id] = evt
	c.cond.Signal()
}

// Drain blocks until an event is available and returns it. Returns
func (c *statusCoalescer) Drain() (dto.StatusEventResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		if len(c.sticky) > 0 {
			evt := c.sticky[0]
			c.sticky = c.sticky[1:]
			return evt, true
		}
		if len(c.order) > 0 {
			id := c.order[0]
			c.order = c.order[1:]
			evt := c.latest[id]
			delete(c.latest, id)
			return evt, true
		}
		if c.closed {
			return dto.StatusEventResult{}, false
		}
		c.cond.Wait()
	}
}

// Close wakes all Drain callers and refuses further Pushes. Any events
func (c *statusCoalescer) Close() {
	c.mu.Lock()
	c.closed = true
	c.cond.Broadcast()
	c.mu.Unlock()
}

func coalesceKey(evt dto.StatusEventResult) (string, bool, bool) {
	if vs := evt.VFSStatus; vs != nil {
		return "vfs:" + vs.GameID, false, true
	}
	return "", false, false
}

func removeString(s []string, target string) []string {
	for i, v := range s {
		if v == target {
			return append(s[:i], s[i+1:]...)
		}
	}
	return s
}

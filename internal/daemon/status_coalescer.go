package daemon

import (
	"sync"

	"github.com/parka/gorganizer/internal/ipc"
)

// statusCoalescer buffers StatusEventResult values. Interim events for the
// same logical ID (download id, install archive path, vfs game id) are
// replaced in-place by newer events. Terminal events (COMPLETE / FAILED)
// and untyped notices (Info, Error) are "sticky" — they pass through
// distinctly so consumers never miss a state transition.
//
// Design rationale: the old non-blocking drop-send pattern on statusCh
// (buffer 64) silently dropped every send past the buffer. With a slow
// consumer under a noisy download loop, interim progress ticks piled up
// and the terminal event was lost to the buffer wall. Coalescing keeps
// only the newest interim per ID, so the buffer never fills from one
// noisy download; stickies still get through.
type statusCoalescer struct {
	mu       sync.Mutex
	cond     *sync.Cond
	latest   map[string]ipc.StatusEventResult // keyed by coalesceKey, interim only
	order    []string                         // FIFO of keys in latest
	sticky   []ipc.StatusEventResult          // terminal / untyped — preserved
	closed   bool
}

func newStatusCoalescer() *statusCoalescer {
	c := &statusCoalescer{
		latest: make(map[string]ipc.StatusEventResult),
	}
	c.cond = sync.NewCond(&c.mu)
	return c
}

// Push inserts or updates an event in the coalescer. Never blocks beyond
// a single mutex acquisition.
func (c *statusCoalescer) Push(evt ipc.StatusEventResult) {
	id, terminal, coalescable := coalesceKey(evt)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}

	if !coalescable || terminal {
		// Flush any pending interim for this id before appending the
		// terminal — consumers get the last interim AND the terminal.
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
// ok=false once the coalescer is closed AND drained.
func (c *statusCoalescer) Drain() (ipc.StatusEventResult, bool) {
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
			return ipc.StatusEventResult{}, false
		}
		c.cond.Wait()
	}
}

// Close wakes all Drain callers and refuses further Pushes. Any events
// still buffered at Close time are delivered before Drain returns false.
func (c *statusCoalescer) Close() {
	c.mu.Lock()
	c.closed = true
	c.cond.Broadcast()
	c.mu.Unlock()
}

// coalesceKey groups events for coalescing. Returns:
//   id          — grouping key; "" for non-groupable events
//   terminal    — true when this event is the last in its logical sequence
//   coalescable — false for events that must always pass through (Info/Error)
//
// After the v2 stream split, only VFSStatus flows through this coalescer.
// Download and install progress moved to their own per-game buses where
// each subscriber paces independently (see streams.go). The sticky
// Info/Error events kept the coalescer in the picture so shutdown
// semantics don't diverge across stream types.
func coalesceKey(evt ipc.StatusEventResult) (string, bool, bool) {
	if vs := evt.VFSStatus; vs != nil {
		return "vfs:" + vs.GameID, false, true
	}
	// Info / Error have no ID — never coalesce, always pass through.
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

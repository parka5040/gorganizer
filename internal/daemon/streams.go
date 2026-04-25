package daemon

import (
	"context"
	"sync"
)

// streamBus fans progress events out to per-game subscribers of the two
// streaming RPCs (StreamArchiveEvents, StreamInstallEvents). One bus per
// topic lets each subscriber pace independently — a slow UI consumer on
// one stream can't stall the other.
//
// Invariants:
//   - All methods are safe under concurrent use.
//   - Publish never blocks: if a subscriber is slow, the event is dropped
//     into that subscriber's channel with default semantics. Subscribers
//     size their buffer to tolerate transient stalls; the daemon does not
//     apply backpressure to whoever is publishing (that'd stall the
//     download pipeline).
//   - Subscribe returns an <-chan T and a cleanup closure; the caller
//     MUST call the closure when the stream is done so the bus releases
//     the buffer.
type streamBus[T any] struct {
	mu          sync.Mutex
	subscribers map[string]map[int]chan T // gameID → subscriber-id → channel
	nextID      int
	bufSize     int
}

func newStreamBus[T any](bufSize int) *streamBus[T] {
	if bufSize < 1 {
		bufSize = 64
	}
	return &streamBus[T]{
		subscribers: make(map[string]map[int]chan T),
		bufSize:     bufSize,
	}
}

// Subscribe registers a listener for `gameID`. Returns a receive-only
// channel and a close function that unregisters the subscriber and closes
// the channel. The bus drops events on a full subscriber buffer.
func (b *streamBus[T]) Subscribe(ctx context.Context, gameID string) (<-chan T, func()) {
	b.mu.Lock()
	id := b.nextID
	b.nextID++
	ch := make(chan T, b.bufSize)
	if _, ok := b.subscribers[gameID]; !ok {
		b.subscribers[gameID] = make(map[int]chan T)
	}
	b.subscribers[gameID][id] = ch
	b.mu.Unlock()

	// Auto-unsubscribe when the caller's context is cancelled. Callers who
	// manage lifetime explicitly (via the returned closure) can ignore ctx.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
		}
		b.mu.Lock()
		if subs, ok := b.subscribers[gameID]; ok {
			if c, ok := subs[id]; ok {
				close(c)
				delete(subs, id)
			}
			if len(subs) == 0 {
				delete(b.subscribers, gameID)
			}
		}
		b.mu.Unlock()
	}()
	return ch, func() { close(done) }
}

// Publish delivers the event to every subscriber of `gameID`. Non-blocking:
// drops on a full subscriber buffer.
func (b *streamBus[T]) Publish(gameID string, evt T) {
	b.mu.Lock()
	subs := b.subscribers[gameID]
	// Snapshot under lock, deliver outside lock so a slow subscriber doesn't
	// hold up other publishers.
	channels := make([]chan T, 0, len(subs))
	for _, c := range subs {
		channels = append(channels, c)
	}
	b.mu.Unlock()
	for _, c := range channels {
		select {
		case c <- evt:
		default:
			// Subscriber buffer full; drop rather than stall.
		}
	}
}

// PublishAll publishes the same event to every game's subscribers. Used
// for events that are logically broadcast (e.g. VFS status may apply to
// multiple games but currently only one is the "active" game).
// Unused for now but kept for symmetry.
func (b *streamBus[T]) PublishAll(evt T) {
	b.mu.Lock()
	channels := make([]chan T, 0)
	for _, subs := range b.subscribers {
		for _, c := range subs {
			channels = append(channels, c)
		}
	}
	b.mu.Unlock()
	for _, c := range channels {
		select {
		case c <- evt:
		default:
		}
	}
}

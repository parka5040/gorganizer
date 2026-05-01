package daemon

import (
	"context"
	"sync"
)

type streamBus[T any] struct {
	mu          sync.Mutex
	subscribers map[string]map[int]chan T
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
	channels := make([]chan T, 0, len(subs))
	for _, c := range subs {
		channels = append(channels, c)
	}
	b.mu.Unlock()
	for _, c := range channels {
		select {
		case c <- evt:
		default:
		}
	}
}

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

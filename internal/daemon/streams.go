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

// Subscribe returns a channel of gameID's events and an unsubscribe func; cancelling ctx also unsubscribes.
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

// Publish delivers the event to every subscriber of gameID, dropping it for any subscriber whose buffer is full.
func (b *streamBus[T]) Publish(gameID string, evt T) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range b.subscribers[gameID] {
		select {
		case c <- evt:
		default:
		}
	}
}

func (b *streamBus[T]) PublishAll(evt T) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, subs := range b.subscribers {
		for _, c := range subs {
			select {
			case c <- evt:
			default:
			}
		}
	}
}

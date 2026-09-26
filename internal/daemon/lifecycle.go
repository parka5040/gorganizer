package daemon

import (
	"log/slog"
	"sync"
	"time"

	"github.com/parka/gorganizer/internal/dto"
)

type backgroundWork struct {
	mu     sync.Mutex
	wg     sync.WaitGroup
	closed bool
}

// begin registers one background task and returns its completion callback, or false once shutdown stopped accepting tasks.
func (b *backgroundWork) begin() (func(), bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, false
	}
	b.wg.Add(1)
	var once sync.Once
	return func() { once.Do(b.wg.Done) }, true
}

// stop refuses new tasks and waits up to timeout for the running ones, reporting whether they all finished.
func (b *backgroundWork) stop(timeout time.Duration) bool {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	finished := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(finished)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-finished:
		return true
	case <-timer.C:
		return false
	}
}

// wait blocks until every registered task finished.
func (b *backgroundWork) wait() {
	b.wg.Wait()
}

// goBackground runs task in a goroutine tracked for shutdown, reporting false and running nothing once shutdown began.
func (s *session) goBackground(name string, task func()) bool {
	done, ok := s.background.begin()
	if !ok {
		slog.Warn("skipping background work during shutdown; the next start consumes a skipped landing for waiting dependency requests but never auto-installs it", "task", name)
		return false
	}
	go func() {
		defer done()
		task()
	}()
	return true
}

// beginShutdown marks the daemon as shutting down so every fenced or installing entry point refuses new work.
func (s *session) beginShutdown() {
	s.shuttingDown.Store(true)
}

// refuseWhenShuttingDown returns a ShuttingDownError for op once shutdown began.
func (s *session) refuseWhenShuttingDown(op string) error {
	if s.shuttingDown.Load() {
		return &dto.ShuttingDownError{Operation: op}
	}
	return nil
}

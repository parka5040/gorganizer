package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/parka/gorganizer/internal/dto"
)

const installOutcomeLimit = 512
const installOutcomeRetention = time.Hour

type installOutcomeEntry struct {
	gameID     string
	outcome    dto.InstallOutcome
	finishedAt time.Time
}

type installOutcomeRegistry struct {
	mu      sync.Mutex
	entries map[string]installOutcomeEntry
	now     func() time.Time
}

// register marks a new client request as running and refuses invalid or previously seen ids.
func (r *installOutcomeRegistry) register(gameID, id string) error {
	if id == "" {
		return nil
	}
	if len(id) > 64 {
		return dto.ErrInvalidClientRequestID
	}
	for _, ch := range id {
		if ch < 'a' || ch > 'z' {
			if ch < 'A' || ch > 'Z' {
				if ch < '0' || ch > '9' {
					if ch != '-' {
						return dto.ErrInvalidClientRequestID
					}
				}
			}
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries == nil {
		r.entries = make(map[string]installOutcomeEntry)
	}
	r.expireLocked()
	if _, exists := r.entries[id]; exists {
		return dto.ErrDuplicateClientRequestID
	}
	if len(r.entries) == installOutcomeLimit {
		var oldestID string
		var oldest time.Time
		for key, entry := range r.entries {
			if entry.outcome.State != dto.InstallOutcomeRunning && (oldestID == "" || entry.finishedAt.Before(oldest)) {
				oldestID, oldest = key, entry.finishedAt
			}
		}
		if oldestID == "" {
			return fmt.Errorf("too many running installations: %w", dto.ErrInstallOutcomeFull)
		}
		delete(r.entries, oldestID)
	}
	r.entries[id] = installOutcomeEntry{gameID: gameID, outcome: dto.InstallOutcome{State: dto.InstallOutcomeRunning}}
	return nil
}

// finish records the final result of one client request without changing other entries.
func (r *installOutcomeRegistry) finish(id string, ctx context.Context, published bool, outcome dto.InstallOutcome, err error) {
	if id == "" {
		return
	}
	if err != nil {
		outcome.State = dto.InstallOutcomeFailed
		outcome.Err = err
		if !published && ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			outcome.State = dto.InstallOutcomeCancelled
		}
	} else {
		outcome.State = dto.InstallOutcomeSucceeded
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.entries[id]
	entry.outcome = outcome
	entry.finishedAt = r.clock()()
	r.entries[id] = entry
}

// lookup returns an immutable snapshot of the request's result or UNKNOWN when the id is absent.
func (r *installOutcomeRegistry) lookup(gameID, id string) (dto.InstallOutcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expireLocked()
	entry, ok := r.entries[id]
	if !ok {
		return dto.InstallOutcome{State: dto.InstallOutcomeUnknown}, nil
	}
	if entry.gameID != gameID {
		return dto.InstallOutcome{}, dto.ErrInstallOutcomeGameMismatch
	}
	return entry.outcome, nil
}

// expireLocked discards finished entries older than the retention window.
func (r *installOutcomeRegistry) expireLocked() {
	cutoff := r.clock()().Add(-installOutcomeRetention)
	for id, entry := range r.entries {
		if entry.outcome.State != dto.InstallOutcomeRunning && !entry.finishedAt.After(cutoff) {
			delete(r.entries, id)
		}
	}
}

// clock returns the registry's injected clock or the wall clock.
func (r *installOutcomeRegistry) clock() func() time.Time {
	if r.now != nil {
		return r.now
	}
	return time.Now
}

// GetInstallOutcome returns an install result without waiting for recovery or shutdown.
func (is *InstallService) GetInstallOutcome(gameID, clientRequestID string) (dto.InstallOutcome, error) {
	return is.s.installOutcomes.lookup(gameID, clientRequestID)
}

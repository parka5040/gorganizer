package daemon

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/parka/gorganizer/internal/download"
)

const premiumTestKey = "premium-key"

type fakeNexusUsers struct {
	mu      sync.Mutex
	calls   int
	errs    map[string]error
	started chan struct{}
	release chan struct{}
}

// ValidateUser reports keys equal to premiumTestKey as premium, blocking the first call when release is set.
func (f *fakeNexusUsers) ValidateUser(ctx context.Context, key string) (*download.NexusUser, error) {
	f.mu.Lock()
	f.calls++
	first := f.calls == 1
	f.mu.Unlock()
	if first && f.release != nil {
		f.started <- struct{}{}
		<-f.release
	}
	if err := f.errs[key]; err != nil {
		return nil, err
	}
	return &download.NexusUser{IsPremium: key == premiumTestKey}, nil
}

// callCount returns how many times ValidateUser has been called.
func (f *fakeNexusUsers) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestNexusPremiumCache verifies TTL expiry, per-key caching, invalidation, and error handling.
func TestNexusPremiumCache(t *testing.T) {
	type step struct {
		at         time.Duration
		key        string
		invalidate bool
		want       bool
		wantErr    error
		wantCalls  int
	}
	tests := []struct {
		name  string
		errs  map[string]error
		steps []step
	}{
		{
			name: "hits cache at 59 minutes and refetches at 61 minutes",
			steps: []step{
				{at: 0, key: premiumTestKey, want: true, wantCalls: 1},
				{at: 59 * time.Minute, key: premiumTestKey, want: true, wantCalls: 1},
				{at: 61 * time.Minute, key: premiumTestKey, want: true, wantCalls: 2},
				{at: 62 * time.Minute, key: premiumTestKey, want: true, wantCalls: 2},
			},
		},
		{
			name: "misses cache after API key changes",
			steps: []step{
				{at: 0, key: premiumTestKey, want: true, wantCalls: 1},
				{at: time.Minute, key: "regular-key", want: false, wantCalls: 2},
			},
		},
		{
			name: "invalidation forces refetch for the same key",
			steps: []step{
				{at: 0, key: premiumTestKey, want: true, wantCalls: 1},
				{at: time.Minute, key: premiumTestKey, invalidate: true, want: true, wantCalls: 2},
				{at: 2 * time.Minute, key: premiumTestKey, want: true, wantCalls: 2},
			},
		},
		{
			name: "invalid key error is returned and not cached",
			errs: map[string]error{"revoked-key": download.ErrInvalidKey},
			steps: []step{
				{at: 0, key: "revoked-key", wantErr: download.ErrInvalidKey, wantCalls: 1},
				{at: time.Minute, key: "revoked-key", wantErr: download.ErrInvalidKey, wantCalls: 2},
			},
		},
		{
			name: "empty API key skips network",
			steps: []step{
				{at: 0, key: "", want: false, wantCalls: 0},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_ = t.TempDir()
			base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
			current := base
			fake := &fakeNexusUsers{errs: tc.errs}
			s := &session{nexusUsers: fake, now: func() time.Time { return current }}
			for i, st := range tc.steps {
				current = base.Add(st.at)
				if st.invalidate {
					s.invalidateNexusPremiumCache()
				}
				got, err := s.nexusPremium(context.Background(), st.key)
				if st.wantErr != nil {
					if !errors.Is(err, st.wantErr) {
						t.Fatalf("step %d: nexusPremium() error = %v, want errors.Is(_, %v)", i, err, st.wantErr)
					}
				} else if err != nil {
					t.Fatalf("step %d: nexusPremium() error = %v", i, err)
				}
				if got != st.want {
					t.Fatalf("step %d: nexusPremium() = %t, want %t", i, got, st.want)
				}
				if calls := fake.callCount(); calls != st.wantCalls {
					t.Fatalf("step %d: validator calls = %d, want %d", i, calls, st.wantCalls)
				}
			}
		})
	}
}

// TestNexusPremiumInFlightFetchDoesNotOverwriteInvalidation verifies a fetch finishing after an invalidation is not cached.
func TestNexusPremiumInFlightFetchDoesNotOverwriteInvalidation(t *testing.T) {
	fake := &fakeNexusUsers{started: make(chan struct{}), release: make(chan struct{})}
	s := &session{nexusUsers: fake}

	type result struct {
		premium bool
		err     error
	}
	done := make(chan result, 1)
	go func() {
		premium, err := s.nexusPremium(context.Background(), premiumTestKey)
		done <- result{premium: premium, err: err}
	}()

	<-fake.started
	s.invalidateNexusPremiumCache()
	close(fake.release)
	res := <-done
	if res.err != nil || !res.premium {
		t.Fatalf("in-flight nexusPremium() = %t, %v, want true, nil", res.premium, res.err)
	}

	s.nexusPremiumMu.Lock()
	cached := s.nexusPremiumCache
	s.nexusPremiumMu.Unlock()
	if cached.keyFingerprint != "" {
		t.Fatalf("cache repopulated by stale fetch: %+v", cached)
	}

	if _, err := s.nexusPremium(context.Background(), premiumTestKey); err != nil {
		t.Fatalf("nexusPremium() error = %v", err)
	}
	if calls := fake.callCount(); calls != 2 {
		t.Fatalf("validator calls = %d, want 2", calls)
	}
}

// TestNexusPremiumDoesNotTakeSessionLock verifies the premium check runs while s.mu is held elsewhere.
func TestNexusPremiumDoesNotTakeSessionLock(t *testing.T) {
	fake := &fakeNexusUsers{}
	s := &session{nexusUsers: fake}
	s.mu.Lock()
	defer s.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		_, err := s.nexusPremium(context.Background(), premiumTestKey)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("nexusPremium() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nexusPremium() blocked on the session lock")
	}
}

// TestNexusPremiumCacheStoresFingerprintOnly verifies the cache never holds the raw API key.
func TestNexusPremiumCacheStoresFingerprintOnly(t *testing.T) {
	s := &session{nexusUsers: &fakeNexusUsers{}}
	if _, err := s.nexusPremium(context.Background(), premiumTestKey); err != nil {
		t.Fatalf("nexusPremium() error = %v", err)
	}
	s.nexusPremiumMu.Lock()
	cached := s.nexusPremiumCache
	s.nexusPremiumMu.Unlock()
	if cached.keyFingerprint == premiumTestKey || cached.keyFingerprint != nexusKeyFingerprint(premiumTestKey) {
		t.Fatalf("keyFingerprint = %q, want SHA-256 fingerprint of the key", cached.keyFingerprint)
	}
}

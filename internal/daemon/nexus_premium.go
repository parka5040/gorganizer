package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/parka/gorganizer/internal/download"
)

const nexusPremiumTTL = time.Hour

type nexusPremiumCache struct {
	keyFingerprint string
	premium        bool
	fetchedAt      time.Time
	gen            uint64
}

type nexusUserValidator interface {
	ValidateUser(ctx context.Context, key string) (*download.NexusUser, error)
}

type nexusClientUserValidator struct{}

// ValidateUser asks the Nexus API which account owns key.
func (nexusClientUserValidator) ValidateUser(ctx context.Context, key string) (*download.NexusUser, error) {
	return download.NewNexusClient(key).ValidateUser(ctx)
}

// nexusPremium reports whether key belongs to a Nexus Premium account, reusing a cached answer younger than nexusPremiumTTL.
func (s *session) nexusPremium(ctx context.Context, key string) (bool, error) {
	if key == "" {
		return false, nil
	}
	fingerprint := nexusKeyFingerprint(key)
	now := s.clock()

	s.nexusPremiumMu.Lock()
	cached := s.nexusPremiumCache
	s.nexusPremiumMu.Unlock()
	if age := now.Sub(cached.fetchedAt); cached.keyFingerprint == fingerprint && age >= 0 && age < nexusPremiumTTL {
		return cached.premium, nil
	}

	user, err := s.userValidator().ValidateUser(ctx, key)
	if err != nil {
		return false, err
	}

	s.nexusPremiumMu.Lock()
	if s.nexusPremiumCache.gen == cached.gen {
		s.nexusPremiumCache = nexusPremiumCache{
			keyFingerprint: fingerprint,
			premium:        user.IsPremium,
			fetchedAt:      now,
			gen:            cached.gen,
		}
	}
	s.nexusPremiumMu.Unlock()
	return user.IsPremium, nil
}

// invalidateNexusPremiumCache drops the cached premium answer and advances its generation.
func (s *session) invalidateNexusPremiumCache() {
	s.nexusPremiumMu.Lock()
	s.nexusPremiumCache = nexusPremiumCache{gen: s.nexusPremiumCache.gen + 1}
	s.nexusPremiumMu.Unlock()
}

// userValidator returns the session's Nexus user validator, defaulting to the live Nexus client.
func (s *session) userValidator() nexusUserValidator {
	if s.nexusUsers == nil {
		return nexusClientUserValidator{}
	}
	return s.nexusUsers
}

// clock returns the session's current time, defaulting to time.Now.
func (s *session) clock() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

// nexusKeyFingerprint returns the hex SHA-256 of key so the key itself is never cached.
func nexusKeyFingerprint(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

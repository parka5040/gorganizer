package daemon

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/release"
)

var updateFetchTimeout = 20 * time.Second
var updateTagTTL = 60 * time.Second
var updateBackoffMin = 5 * time.Minute
var updateBackoffMax = time.Hour

var errUpdateBackoff = errors.New("GitHub asked Gorganizer to wait before checking again.")

type updateFetch struct {
	done     chan struct{}
	tag      string
	err      error
	timedOut bool
}

type updateServiceEmbedding struct{ *UpdateService }

type UpdateService struct {
	ctx    context.Context
	cancel context.CancelFunc
	fetch  func(context.Context) (string, error)
	now    func() time.Time

	mu           sync.Mutex
	inflight     *updateFetch
	cachedTag    string
	cachedAt     time.Time
	backoffUntil time.Time
}

// newUpdateService creates an update checker that stops its fetches on session shutdown.
func newUpdateService(s *session) *UpdateService {
	ctx, cancel := context.WithCancel(context.Background())
	u := &UpdateService{
		ctx: ctx, cancel: cancel,
		fetch: func(ctx context.Context) (string, error) {
			return (release.Source{}).ResolveTag(ctx, "")
		},
		now: s.clock,
	}
	if s.shutdownCh != nil {
		go func() {
			select {
			case <-s.shutdownCh:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	return u
}

// CheckForUpdate compares the GUI's release version with the latest published tag.
func (u *UpdateService) CheckForUpdate(ctx context.Context, runningVersion string) (dto.UpdateCheckResult, error) {
	base, ok := release.BaseVersion(runningVersion)
	if !ok {
		return dto.UpdateCheckResult{Outcome: dto.UpdateCheckNotSupported}, nil
	}
	if err := ctx.Err(); err != nil {
		return dto.UpdateCheckResult{}, err
	}
	tag, err, timedOut := u.latestTag(ctx)
	if ctx.Err() != nil {
		return dto.UpdateCheckResult{}, ctx.Err()
	}
	if err != nil {
		if errors.Is(err, errUpdateBackoff) {
			return dto.UpdateCheckResult{Outcome: dto.UpdateCheckUnavailable, Detail: errUpdateBackoff.Error()}, nil
		}
		outcome := dto.UpdateCheckUnavailable
		if errors.Is(err, release.ErrUnreachable) || timedOut {
			outcome = dto.UpdateCheckOffline
		}
		return dto.UpdateCheckResult{Outcome: outcome, Detail: updateDetail(err)}, nil
	}
	latest := strings.TrimPrefix(tag, "v")
	order, err := release.Compare(latest, base)
	if err != nil {
		detail := updateDetail(err)
		slog.Info("update check failed", "error", detail)
		return dto.UpdateCheckResult{Outcome: dto.UpdateCheckUnavailable, Detail: detail}, nil
	}
	result := dto.UpdateCheckResult{Outcome: dto.UpdateCheckUpToDate, LatestVersion: latest}
	if order > 0 {
		notes, err := release.NotesURL(tag)
		if err != nil {
			detail := updateDetail(err)
			slog.Info("update check failed", "error", detail)
			return dto.UpdateCheckResult{Outcome: dto.UpdateCheckUnavailable, Detail: detail}, nil
		}
		result.Outcome = dto.UpdateCheckUpdateAvailable
		result.NotesURL = notes
	}
	return result, nil
}

// latestTag returns a fresh cached tag or waits for one service-owned fetch.
func (u *UpdateService) latestTag(ctx context.Context) (string, error, bool) {
	u.mu.Lock()
	now := u.now()
	if u.cachedTag != "" && now.Sub(u.cachedAt) < updateTagTTL {
		tag := u.cachedTag
		u.mu.Unlock()
		return tag, nil, false
	}
	if now.Before(u.backoffUntil) {
		u.mu.Unlock()
		return "", errUpdateBackoff, false
	}
	call := u.inflight
	start := call == nil
	if start {
		call = &updateFetch{done: make(chan struct{})}
		u.inflight = call
	}
	u.mu.Unlock()
	if start {
		go u.fetchTag(call)
	}

	select {
	case <-call.done:
		return call.tag, call.err, call.timedOut
	case <-ctx.Done():
		return "", ctx.Err(), false
	}
}

// fetchTag resolves a tag within the service budget and publishes it to all waiting callers.
func (u *UpdateService) fetchTag(call *updateFetch) {
	ctx, cancel := context.WithTimeout(u.ctx, updateFetchTimeout)
	defer cancel()
	tag, err := u.fetch(ctx)
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	if ctx.Err() != nil {
		err = ctx.Err()
	}

	u.mu.Lock()
	if err == nil {
		u.cachedTag = tag
		u.cachedAt = u.now()
	} else {
		var status *release.StatusError
		if errors.As(err, &status) && (status.Code == 403 || status.Code == 429) {
			delay := status.RetryAfter
			if delay < updateBackoffMin {
				delay = updateBackoffMin
			}
			if delay > updateBackoffMax {
				delay = updateBackoffMax
			}
			u.backoffUntil = u.now().Add(delay)
		}
	}
	call.tag, call.err, call.timedOut = tag, err, timedOut
	u.inflight = nil
	close(call.done)
	u.mu.Unlock()
	if err != nil {
		slog.Info("update check failed", "error", updateDetail(err))
	}
}

// updateDetail removes control characters and caps an error at 512 UTF-8 bytes.
func updateDetail(err error) string {
	var detail strings.Builder
	for _, r := range err.Error() {
		if unicode.IsControl(r) {
			continue
		}
		if detail.Len()+utf8.RuneLen(r) > 512 {
			break
		}
		detail.WriteRune(r)
	}
	return detail.String()
}

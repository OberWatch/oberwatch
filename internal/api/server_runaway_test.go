package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OberWatch/oberwatch/internal/alert"
	"github.com/OberWatch/oberwatch/internal/budget"
	"github.com/OberWatch/oberwatch/internal/config"
	"github.com/OberWatch/oberwatch/internal/pricing"
	oberproxy "github.com/OberWatch/oberwatch/internal/proxy"
	"github.com/OberWatch/oberwatch/internal/storage"
)

//nolint:govet // keep mutex first for readability of the test clock.
type runawayTestClock struct {
	mu  sync.RWMutex
	now time.Time
}

func (c *runawayTestClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

func (c *runawayTestClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	c.mu.Unlock()
}

// runawayStack is a proxy with the management API mounted, a persistent
// budget manager whose alerts flow through Server.PublishAlert into storage,
// and a mock upstream that counts hits.
type runawayStack struct {
	client     *http.Client
	store      storage.Store
	clock      *runawayTestClock
	upstream   *int32
	dispatched *int32
	proxyURL   string
}

func newRunawayStack(t *testing.T, maxRequests, windowSeconds int) *runawayStack {
	t.Helper()

	var upstreamHits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&upstreamHits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"mock","usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	}))
	t.Cleanup(upstream.Close)

	cfg := config.DefaultConfig()
	cfg.Upstream.OpenAI.BaseURL = upstream.URL
	cfg.Upstream.Anthropic.BaseURL = upstream.URL
	cfg.Upstream.DefaultProvider = config.ProviderOpenAI
	cfg.Gate.Runaway = config.RunawayConfig{Enabled: true, MaxRequests: maxRequests, WindowSeconds: windowSeconds}

	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "runaway-integration.db"), 0, nil)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	// Mirror the production wiring: the manager dispatches into the management
	// server, which persists the alert and publishes the SSE event.
	var management *Server
	var dispatched int32
	dispatcher := dispatcherFunc(func(_ context.Context, entry alert.Alert) {
		atomic.AddInt32(&dispatched, 1)
		if management != nil {
			management.PublishAlert(entry)
		}
	})

	clock := &runawayTestClock{now: time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC)}
	manager, err := budget.NewPersistentManagerWithClockAndDispatcher(cfg.Gate, nil, store, clock, dispatcher)
	if err != nil {
		t.Fatalf("NewPersistentManagerWithClockAndDispatcher() error = %v", err)
	}
	t.Cleanup(func() {
		_ = manager.Close()
	})
	management = New(cfg, manager, store, "test")

	proxyHandler, err := oberproxy.New(cfg, oberproxy.Hooks{
		Budget:     manager,
		Pricing:    pricing.NewPricingTableFromConfig(cfg.Pricing, nil),
		Management: management,
	})
	if err != nil {
		t.Fatalf("proxy.New() error = %v", err)
	}
	proxyServer := httptest.NewServer(proxyHandler)
	t.Cleanup(proxyServer.Close)

	return &runawayStack{
		proxyURL:   proxyServer.URL,
		client:     proxyServer.Client(),
		store:      store,
		clock:      clock,
		upstream:   &upstreamHits,
		dispatched: &dispatched,
	}
}

// tryCompletion posts one chat completion and returns the transport error
// instead of failing the test, so spawned goroutines can call it.
func (s *runawayStack) tryCompletion(agent string) (int, string, error) {
	req, err := http.NewRequest(http.MethodPost, s.proxyURL+"/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		return 0, "", fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Oberwatch-Agent", agent)
	return s.tryDo(req)
}

// completion posts one chat completion for the agent. Call it only from the
// goroutine running the test.
func (s *runawayStack) completion(t *testing.T, agent string) (int, string) {
	t.Helper()

	status, body, err := s.tryCompletion(agent)
	if err != nil {
		t.Fatalf("completion(%q) error = %v", agent, err)
	}
	return status, body
}

func (s *runawayStack) management(t *testing.T, method, path string) (int, string) {
	t.Helper()

	req, err := http.NewRequest(method, s.proxyURL+basePath+path, nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	token := seedSession(t, s.store, time.Now().UTC().Add(time.Hour))
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	return s.do(t, req)
}

func (s *runawayStack) tryDo(req *http.Request) (int, string, error) {
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("do request: %w", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return 0, "", fmt.Errorf("read body: %w", err)
	}
	return resp.StatusCode, string(body), nil
}

func (s *runawayStack) do(t *testing.T, req *http.Request) (int, string) {
	t.Helper()

	status, body, err := s.tryDo(req)
	if err != nil {
		t.Fatalf("%s %s error = %v", req.Method, req.URL.Path, err)
	}
	return status, body
}

func (s *runawayStack) alertCounts(t *testing.T, agent string) map[alert.Type]int {
	t.Helper()

	items, err := s.store.QueryAlerts(context.Background(), storage.AlertQuery{Agent: agent})
	if err != nil {
		t.Fatalf("QueryAlerts() error = %v", err)
	}
	counts := make(map[alert.Type]int)
	for _, item := range items {
		counts[item.Type]++
	}
	return counts
}

func (s *runawayStack) budgetStatus(t *testing.T, agent string) string {
	t.Helper()

	status, body := s.management(t, http.MethodGet, "/budgets/"+agent)
	if status != http.StatusOK {
		t.Fatalf("GET /budgets/%s status = %d body = %s", agent, status, body)
	}
	payload := decodeJSONMap(t, strings.NewReader(body))
	return mustString(t, payload, "status")
}

func TestServer_RunawayProxyAPIIntegration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		maxRequests   int
		windowSeconds int
		repeatKilled  int
	}{
		{name: "limit of two", maxRequests: 2, windowSeconds: 60, repeatKilled: 4},
		{name: "limit of five", maxRequests: 5, windowSeconds: 10, repeatKilled: 2},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stack := newRunawayStack(t, tt.maxRequests, tt.windowSeconds)
			const agent = "runaway-agent"
			const bystander = "calm-agent"

			for i := 0; i < tt.maxRequests; i++ {
				if status, body := stack.completion(t, agent); status != http.StatusOK {
					t.Fatalf("allowed request %d status = %d body = %s", i+1, status, body)
				}
			}
			if got := stack.budgetStatus(t, agent); got != "active" {
				t.Fatalf("status before kill = %q, want active", got)
			}

			status, body := stack.completion(t, agent)
			if status != http.StatusTooManyRequests {
				t.Fatalf("max+1 status = %d, want 429 body = %s", status, body)
			}
			if !strings.Contains(body, `"code":"agent_killed"`) {
				t.Fatalf("max+1 body = %s, want agent_killed", body)
			}
			for i := 0; i < tt.repeatKilled; i++ {
				status, body := stack.completion(t, agent)
				if status != http.StatusTooManyRequests || !strings.Contains(body, "agent_killed") {
					t.Fatalf("killed request %d status = %d body = %s", i+1, status, body)
				}
			}
			if status, body := stack.completion(t, bystander); status != http.StatusOK {
				t.Fatalf("bystander status = %d body = %s", status, body)
			}
			if got := atomic.LoadInt32(stack.upstream); got != int32(tt.maxRequests+1) {
				t.Fatalf("upstream hits = %d, want %d", got, tt.maxRequests+1)
			}

			if got := stack.budgetStatus(t, agent); got != "killed" {
				t.Fatalf("status after kill = %q, want killed", got)
			}

			counts := stack.alertCounts(t, agent)
			if counts[alert.TypeRunawayDetected] != 1 || counts[alert.TypeAgentKilled] != 1 {
				t.Fatalf("stored alerts for %s = %v, want exactly one runaway_detected and one agent_killed", agent, counts)
			}
			if got := atomic.LoadInt32(stack.dispatched); got != 2 {
				t.Fatalf("dispatched alerts = %d, want 2", got)
			}
			if len(stack.alertCounts(t, bystander)) != 0 {
				t.Fatalf("bystander should have no alerts")
			}

			alertsStatus, alertsBody := stack.management(t, http.MethodGet, "/alerts?agent="+agent)
			if alertsStatus != http.StatusOK {
				t.Fatalf("GET /alerts status = %d body = %s", alertsStatus, alertsBody)
			}
			if strings.Count(alertsBody, `"type":"runaway_detected"`) != 1 || strings.Count(alertsBody, `"type":"agent_killed"`) != 1 {
				t.Fatalf("GET /alerts body = %s, want one runaway_detected and one agent_killed", alertsBody)
			}

			// Sticky: the kill outlives the sliding window.
			stack.clock.Advance(time.Duration(tt.windowSeconds+1) * time.Second)
			if status, body := stack.completion(t, agent); status != http.StatusTooManyRequests || !strings.Contains(body, "agent_killed") {
				t.Fatalf("sticky request status = %d body = %s", status, body)
			}
			if got := stack.budgetStatus(t, agent); got != "killed" {
				t.Fatalf("status after window = %q, want killed", got)
			}

			// Manual enable through the management API restores traffic.
			enableStatus, enableBody := stack.management(t, http.MethodPost, "/budgets/"+agent+"/enable")
			if enableStatus != http.StatusOK {
				t.Fatalf("POST enable status = %d body = %s", enableStatus, enableBody)
			}
			if got := stack.budgetStatus(t, agent); got != "active" {
				t.Fatalf("status after enable = %q, want active", got)
			}
			for i := 0; i < tt.maxRequests; i++ {
				if status, body := stack.completion(t, agent); status != http.StatusOK {
					t.Fatalf("post-enable request %d status = %d body = %s", i+1, status, body)
				}
			}
			if got := atomic.LoadInt32(stack.upstream); got != int32(2*tt.maxRequests+1) {
				t.Fatalf("upstream hits after enable = %d, want %d", got, 2*tt.maxRequests+1)
			}

			// Enabling never replays alerts.
			counts = stack.alertCounts(t, agent)
			if counts[alert.TypeRunawayDetected] != 1 || counts[alert.TypeAgentKilled] != 1 {
				t.Fatalf("stored alerts after enable = %v, want unchanged", counts)
			}
			if got := atomic.LoadInt32(stack.dispatched); got != 2 {
				t.Fatalf("dispatched alerts after enable = %d, want 2", got)
			}
		})
	}
}

func TestServer_RunawayConcurrentAgentsThroughProxyAndAPI(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test case fields ordered for readability.
	tests := []struct {
		name        string
		maxRequests int
		agents      []string
		perAgent    int
	}{
		{name: "two agents", maxRequests: 3, agents: []string{"agent-a", "agent-b"}, perAgent: 15},
		{name: "three agents tight limit", maxRequests: 1, agents: []string{"agent-a", "agent-b", "agent-c"}, perAgent: 8},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stack := newRunawayStack(t, tt.maxRequests, 60)

			var mu sync.Mutex
			okCount := make(map[string]int)
			killedCount := make(map[string]int)
			var wg sync.WaitGroup
			start := make(chan struct{})
			for _, agent := range tt.agents {
				agent := agent
				for i := 0; i < tt.perAgent; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						status, body, err := stack.tryCompletion(agent)
						if err != nil {
							t.Errorf("%s request failed: %v", agent, err)
							return
						}
						mu.Lock()
						defer mu.Unlock()
						switch {
						case status == http.StatusOK:
							okCount[agent]++
						case status == http.StatusTooManyRequests && strings.Contains(body, "agent_killed"):
							killedCount[agent]++
						default:
							t.Errorf("%s unexpected response %d %s", agent, status, body)
						}
					}()
				}
			}
			close(start)
			wg.Wait()

			for _, agent := range tt.agents {
				if okCount[agent] != tt.maxRequests || killedCount[agent] != tt.perAgent-tt.maxRequests {
					t.Fatalf("%s 200/429 = %d/%d, want %d/%d", agent, okCount[agent], killedCount[agent], tt.maxRequests, tt.perAgent-tt.maxRequests)
				}
				if got := stack.budgetStatus(t, agent); got != "killed" {
					t.Fatalf("%s status = %q, want killed", agent, got)
				}
				counts := stack.alertCounts(t, agent)
				if counts[alert.TypeRunawayDetected] != 1 || counts[alert.TypeAgentKilled] != 1 {
					t.Fatalf("%s stored alerts = %v, want exactly one of each", agent, counts)
				}
			}
			if got := atomic.LoadInt32(stack.dispatched); got != int32(2*len(tt.agents)) {
				t.Fatalf("dispatched alerts = %d, want %d", got, 2*len(tt.agents))
			}
			if got := atomic.LoadInt32(stack.upstream); got != int32(len(tt.agents)*tt.maxRequests) {
				t.Fatalf("upstream hits = %d, want %d", got, len(tt.agents)*tt.maxRequests)
			}
		})
	}
}

package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OberWatch/oberwatch/internal/alert"
	"github.com/OberWatch/oberwatch/internal/budget"
	"github.com/OberWatch/oberwatch/internal/config"
	"github.com/OberWatch/oberwatch/internal/pricing"
)

const runawayRequestBody = `{"model":"gpt-5.6-terra","messages":[{"role":"user","content":"hello"}]}`

//nolint:govet // keep mutex first for readability of the concurrency helper.
type runawayAlertRecorder struct {
	mu     sync.Mutex
	events []alert.Alert
}

func (r *runawayAlertRecorder) Dispatch(_ context.Context, event alert.Alert) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *runawayAlertRecorder) count(agent string, alertType alert.Type) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, event := range r.events {
		if event.Agent == agent && event.Type == alertType {
			count++
		}
	}
	return count
}

func (r *runawayAlertRecorder) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

//nolint:govet // keep mutex first for readability of the concurrency helper.
type runawayClock struct {
	mu  sync.RWMutex
	now time.Time
}

func (c *runawayClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

func (c *runawayClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	c.mu.Unlock()
}

// runawayHarness wires a proxy in front of a counting mock upstream with a
// runaway-enabled budget manager, a controllable clock and an alert recorder.
type runawayHarness struct {
	client       *http.Client
	manager      *budget.BudgetManager
	clock        *runawayClock
	alerts       *runawayAlertRecorder
	upstreamHits *int32
	proxyURL     string
}

func newRunawayHarness(t *testing.T, maxRequests, windowSeconds int) *runawayHarness {
	t.Helper()

	var upstreamHits int32
	openAIServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&upstreamHits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"mock","usage":{"prompt_tokens":10,"completion_tokens":5}}`))
	}))
	t.Cleanup(openAIServer.Close)

	anthropicServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(anthropicServer.Close)

	cfg := testConfig(openAIServer.URL, anthropicServer.URL)
	cfg.Gate.Runaway = config.RunawayConfig{Enabled: true, MaxRequests: maxRequests, WindowSeconds: windowSeconds}

	clock := &runawayClock{now: time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC)}
	alerts := &runawayAlertRecorder{}
	manager := budget.NewManagerWithClockAndDispatcher(cfg.Gate, nil, clock, alerts)

	proxyServer, err := New(cfg, Hooks{
		Budget:  manager,
		Pricing: pricing.NewPricingTableFromConfig(cfg.Pricing, nil),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server := newTestServer(t, proxyServer)
	t.Cleanup(server.Close)

	return &runawayHarness{
		proxyURL:     server.URL,
		client:       server.Client(),
		manager:      manager,
		clock:        clock,
		alerts:       alerts,
		upstreamHits: &upstreamHits,
	}
}

func (h *runawayHarness) hits() int32 {
	return atomic.LoadInt32(h.upstreamHits)
}

// send posts one chat completion for the agent and returns status and body.
func (h *runawayHarness) send(t *testing.T, agent string) (int, []byte) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, h.proxyURL+"/v1/chat/completions", strings.NewReader(runawayRequestBody))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Oberwatch-Agent", agent)

	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	return resp.StatusCode, body
}

func assertAgentKilledBody(t *testing.T, body []byte, agent string) {
	t.Helper()

	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Agent   string `json:"agent"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("Unmarshal(%s) error = %v", string(body), err)
	}
	if payload.Error.Code != "agent_killed" {
		t.Fatalf("error.code = %q, want agent_killed (body %s)", payload.Error.Code, string(body))
	}
	if payload.Error.Agent != agent {
		t.Fatalf("error.agent = %q, want %q", payload.Error.Agent, agent)
	}
	if strings.TrimSpace(payload.Error.Message) == "" {
		t.Fatalf("error.message empty in %s", string(body))
	}
}

func TestServer_RunawayKillReturns429AgentKilledExactlyOnceAlerts(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test case fields ordered for readability.
	tests := []struct {
		name         string
		maxRequests  int
		extraKilled  int
		otherAgent   string
		otherCalls   int
		wantUpstream int32
	}{
		{name: "limit of one", maxRequests: 1, extraKilled: 3, otherAgent: "calm-agent", otherCalls: 1, wantUpstream: 2},
		{name: "limit of three", maxRequests: 3, extraKilled: 5, otherAgent: "calm-agent", otherCalls: 3, wantUpstream: 6},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newRunawayHarness(t, tt.maxRequests, 60)
			const agent = "runaway-agent"

			for i := 0; i < tt.maxRequests; i++ {
				status, body := h.send(t, agent)
				if status != http.StatusOK {
					t.Fatalf("request %d status = %d, want 200 (body %s)", i+1, status, body)
				}
			}
			if got := h.hits(); got != int32(tt.maxRequests) {
				t.Fatalf("upstream hits after %d allowed requests = %d", tt.maxRequests, got)
			}

			status, body := h.send(t, agent)
			if status != http.StatusTooManyRequests {
				t.Fatalf("max+1 status = %d, want 429 (body %s)", status, body)
			}
			assertAgentKilledBody(t, body, agent)
			if !strings.Contains(string(body), "runaway request volume") {
				t.Fatalf("max+1 body = %s, want runaway kill message", body)
			}

			for i := 0; i < tt.extraKilled; i++ {
				status, body := h.send(t, agent)
				if status != http.StatusTooManyRequests {
					t.Fatalf("post-kill request %d status = %d, want 429", i+1, status)
				}
				assertAgentKilledBody(t, body, agent)
			}

			for i := 0; i < tt.otherCalls; i++ {
				if status, body := h.send(t, tt.otherAgent); status != http.StatusOK {
					t.Fatalf("%s request %d status = %d, want 200 (body %s)", tt.otherAgent, i+1, status, body)
				}
			}

			if got := h.hits(); got != tt.wantUpstream {
				t.Fatalf("upstream hits = %d, want %d (killed requests must not reach upstream)", got, tt.wantUpstream)
			}
			if got := h.alerts.count(agent, alert.TypeRunawayDetected); got != 1 {
				t.Fatalf("runaway_detected alerts = %d, want exactly 1", got)
			}
			if got := h.alerts.count(agent, alert.TypeAgentKilled); got != 1 {
				t.Fatalf("agent_killed alerts = %d, want exactly 1", got)
			}
			if got := h.alerts.total(); got != 2 {
				t.Fatalf("total alerts = %d, want 2 (no alerts for %s)", got, tt.otherAgent)
			}
		})
	}
}

func TestServer_RunawayKillStickyAfterWindowAndManualEnableRestoresTraffic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		maxRequests   int
		windowSeconds int
		waitAfterKill time.Duration
	}{
		{name: "one window later", maxRequests: 2, windowSeconds: 30, waitAfterKill: 31 * time.Second},
		{name: "many windows later", maxRequests: 2, windowSeconds: 5, waitAfterKill: time.Hour},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newRunawayHarness(t, tt.maxRequests, tt.windowSeconds)
			const agent = "runaway-agent"

			for i := 0; i <= tt.maxRequests; i++ {
				h.send(t, agent)
			}
			if !h.manager.Snapshot(agent).Killed {
				t.Fatalf("agent should be killed after max+1 requests")
			}

			h.clock.Advance(tt.waitAfterKill)
			status, body := h.send(t, agent)
			if status != http.StatusTooManyRequests {
				t.Fatalf("sticky status = %d, want 429 (body %s)", status, body)
			}
			assertAgentKilledBody(t, body, agent)
			if got := h.hits(); got != int32(tt.maxRequests) {
				t.Fatalf("upstream hits while killed = %d, want %d", got, tt.maxRequests)
			}

			h.manager.EnableAgent(agent)
			for i := 0; i < tt.maxRequests; i++ {
				status, body := h.send(t, agent)
				if status != http.StatusOK {
					t.Fatalf("post-enable request %d status = %d, want 200 (body %s)", i+1, status, body)
				}
			}
			if got := h.hits(); got != int32(2*tt.maxRequests) {
				t.Fatalf("upstream hits after enable = %d, want %d", got, 2*tt.maxRequests)
			}
			if h.manager.Snapshot(agent).Killed {
				t.Fatalf("agent should stay enabled while under the limit")
			}

			// Enable does not replay the old kill alerts.
			if got := h.alerts.total(); got != 2 {
				t.Fatalf("total alerts = %d, want 2", got)
			}
		})
	}
}

func TestServer_RunawayConcurrentAgentsRace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		maxRequests int
		agents      int
		perAgent    int
	}{
		{name: "two agents flood at once", maxRequests: 5, agents: 2, perAgent: 25},
		{name: "four agents with tight limit", maxRequests: 2, agents: 4, perAgent: 12},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newRunawayHarness(t, tt.maxRequests, 60)

			var mu sync.Mutex
			okCount := make(map[string]int)
			killedCount := make(map[string]int)
			var wg sync.WaitGroup
			start := make(chan struct{})
			for a := 0; a < tt.agents; a++ {
				agent := fmt.Sprintf("flood-%d", a)
				for i := 0; i < tt.perAgent; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						status, body := h.send(t, agent)
						mu.Lock()
						defer mu.Unlock()
						switch status {
						case http.StatusOK:
							okCount[agent]++
						case http.StatusTooManyRequests:
							if !strings.Contains(string(body), "agent_killed") {
								t.Errorf("%s 429 body = %s, want agent_killed", agent, body)
							}
							killedCount[agent]++
						default:
							t.Errorf("%s unexpected status %d body %s", agent, status, body)
						}
					}()
				}
			}
			close(start)
			wg.Wait()

			for a := 0; a < tt.agents; a++ {
				agent := fmt.Sprintf("flood-%d", a)
				if okCount[agent] != tt.maxRequests {
					t.Fatalf("%s 200s = %d, want %d", agent, okCount[agent], tt.maxRequests)
				}
				if killedCount[agent] != tt.perAgent-tt.maxRequests {
					t.Fatalf("%s 429s = %d, want %d", agent, killedCount[agent], tt.perAgent-tt.maxRequests)
				}
				if got := h.alerts.count(agent, alert.TypeRunawayDetected); got != 1 {
					t.Fatalf("%s runaway_detected alerts = %d, want 1", agent, got)
				}
				if got := h.alerts.count(agent, alert.TypeAgentKilled); got != 1 {
					t.Fatalf("%s agent_killed alerts = %d, want 1", agent, got)
				}
			}
			if got := h.hits(); got != int32(tt.agents*tt.maxRequests) {
				t.Fatalf("upstream hits = %d, want %d", got, tt.agents*tt.maxRequests)
			}
			if got := h.alerts.total(); got != 2*tt.agents {
				t.Fatalf("total alerts = %d, want %d", got, 2*tt.agents)
			}
		})
	}
}

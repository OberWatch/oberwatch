package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/OberWatch/oberwatch/internal/budget"
	"github.com/OberWatch/oberwatch/internal/config"
	"github.com/OberWatch/oberwatch/internal/pricing"
)

func TestServer_BudgetRejectsBeforeUpstream(t *testing.T) {
	t.Parallel()

	var upstreamHits int32
	openAIServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(openAIServer.Close)

	anthropicServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(anthropicServer.Close)

	cfg := testConfig(openAIServer.URL, anthropicServer.URL)
	cfg.Gate.DefaultBudget.LimitUSD = 1
	cfg.Gate.DefaultBudget.ActionOnExceed = config.BudgetActionReject

	manager := budget.NewManager(cfg.Gate, nil)
	manager.RecordSpend("email-agent", 1.0)

	proxyServer, err := New(cfg, Hooks{
		Budget:  manager,
		Pricing: pricing.NewPricingTableFromConfig(cfg.Pricing, nil),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	server := newTestServer(t, proxyServer)
	t.Cleanup(server.Close)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("X-Oberwatch-Agent", "email-agent")

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	t.Cleanup(func() {
		_ = resp.Body.Close()
	})

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d", resp.StatusCode, http.StatusTooManyRequests)
	}
	if got := atomic.LoadInt32(&upstreamHits); got != 0 {
		t.Fatalf("upstream hits = %d, want 0", got)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	errorBody, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("error payload shape invalid: %s", string(body))
	}
	if errorBody["code"] != "budget_exceeded" {
		t.Fatalf("error.code = %v, want budget_exceeded", errorBody["code"])
	}
}

func TestServer_BudgetDowngradeRewritesModelAndRecordsSpend(t *testing.T) {
	t.Parallel()

	capturedBody := make(chan string, 1)
	openAIServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll() error = %v", err)
		}
		capturedBody <- string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"usage":{"prompt_tokens":100,"completion_tokens":50}}`))
	}))
	t.Cleanup(openAIServer.Close)

	anthropicServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(anthropicServer.Close)

	cfg := testConfig(openAIServer.URL, anthropicServer.URL)
	cfg.Gate.DefaultBudget.LimitUSD = 10
	cfg.Gate.DefaultBudget.ActionOnExceed = config.BudgetActionDowngrade
	cfg.Gate.DowngradeThresholdPct = 50
	cfg.Gate.DefaultDowngradeChain = []string{"claude-opus-4-6", "claude-sonnet-4-6", "claude-haiku-4-5"}

	manager := budget.NewManager(cfg.Gate, nil)
	manager.RecordSpend("email-agent", 5.5)

	priceTable := pricing.NewPricingTableFromConfig(cfg.Pricing, nil)
	proxyServer, err := New(cfg, Hooks{Budget: manager, Pricing: priceTable})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	server := newTestServer(t, proxyServer)
	t.Cleanup(server.Close)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"claude-opus-4-6","stream":false,"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("X-Oberwatch-Agent", "email-agent")

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	t.Cleanup(func() {
		_ = resp.Body.Close()
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	_, _ = io.ReadAll(resp.Body)

	gotBody := <-capturedBody
	if !strings.Contains(gotBody, `"model":"claude-sonnet-4-6"`) {
		t.Fatalf("forwarded body = %s, want downgraded model", gotBody)
	}

	snapshot := manager.Snapshot("email-agent")
	if snapshot.SpentUSD <= 5.5 {
		t.Fatalf("spent = %v, want > 5.5 after response accounting", snapshot.SpentUSD)
	}
}

func TestServer_BudgetStreamingAccumulatesUsage(t *testing.T) {
	t.Parallel()

	openAIServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"usage\":{\"prompt_tokens\":40,\"completion_tokens\":20}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(openAIServer.Close)

	anthropicServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(anthropicServer.Close)

	cfg := testConfig(openAIServer.URL, anthropicServer.URL)
	manager := budget.NewManager(cfg.Gate, nil)
	priceTable := pricing.NewPricingTableFromConfig(cfg.Pricing, nil)
	proxyServer, err := New(cfg, Hooks{Budget: manager, Pricing: priceTable})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	server := newTestServer(t, proxyServer)
	t.Cleanup(server.Close)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("X-Oberwatch-Agent", "stream-agent")

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	t.Cleanup(func() {
		_ = resp.Body.Close()
	})

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if !strings.Contains(string(body), "[DONE]") {
		t.Fatalf("stream body = %q, want final done chunk", string(body))
	}

	snapshot := manager.Snapshot("stream-agent")
	if snapshot.SpentUSD <= 0 {
		t.Fatalf("stream spent = %v, want > 0", snapshot.SpentUSD)
	}
}

func TestServer_EmergencyStopBlocksProxyButNotManagement(t *testing.T) {
	t.Parallel()

	openAIServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(openAIServer.Close)

	anthropicServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(anthropicServer.Close)

	cfg := testConfig(openAIServer.URL, anthropicServer.URL)
	manager := budget.NewManager(cfg.Gate, nil)
	manager.SetEmergencyStop(true)

	proxyServer, err := New(cfg, Hooks{
		Budget:  manager,
		Pricing: pricing.NewPricingTableFromConfig(cfg.Pricing, nil),
		Management: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	server := newTestServer(t, proxyServer)
	t.Cleanup(server.Close)

	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{name: "proxy request returns 503", path: "/v1/chat/completions", wantStatus: http.StatusServiceUnavailable},
		{name: "management request still works", path: "/_oberwatch/api/v1/health", wantStatus: http.StatusNoContent},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`
			if strings.Contains(tt.path, "/_oberwatch/") {
				body = ""
			}
			req, err := http.NewRequest(http.MethodPost, server.URL+tt.path, strings.NewReader(body))
			if err != nil {
				t.Fatalf("NewRequest() error = %v", err)
			}
			if tt.path == "/v1/chat/completions" {
				req.Method = http.MethodPost
			} else {
				req.Method = http.MethodGet
				req.Body = nil
			}

			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatalf("Do() error = %v", err)
			}
			t.Cleanup(func() {
				_ = resp.Body.Close()
			})

			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
		})
	}
}

func TestProxyHelpers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		method             string
		path               string
		wantServeDashboard bool
		wantKnownProxy     bool
	}{
		{name: "dashboard route served", method: http.MethodGet, path: "/", wantServeDashboard: true, wantKnownProxy: false},
		{name: "proxy route not served by dashboard", method: http.MethodGet, path: "/v1/chat/completions", wantServeDashboard: false, wantKnownProxy: true},
		{name: "non-get never serves dashboard", method: http.MethodPost, path: "/", wantServeDashboard: false, wantKnownProxy: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := shouldServeDashboard(tt.method, tt.path); got != tt.wantServeDashboard {
				t.Fatalf("shouldServeDashboard(%q, %q) = %v, want %v", tt.method, tt.path, got, tt.wantServeDashboard)
			}
			if got := isKnownProxyPath(tt.path); got != tt.wantKnownProxy {
				t.Fatalf("isKnownProxyPath(%q) = %v, want %v", tt.path, got, tt.wantKnownProxy)
			}
		})
	}
}

func TestWriteEmergencyStopError(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	writeEmergencyStopError(recorder)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(recorder.Body.String(), "emergency_stop") {
		t.Fatalf("body = %q, want emergency_stop payload", recorder.Body.String())
	}
}

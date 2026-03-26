package proxy

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/OberWatch/oberwatch/internal/budget"
	"github.com/OberWatch/oberwatch/internal/config"
	"github.com/OberWatch/oberwatch/internal/pricing"
)

func TestDetectProvider_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		path            string
		defaultProvider config.ProviderConfigName
		want            config.ProviderConfigName
	}{
		{
			name:            "chat completions routes to openai",
			path:            "/v1/chat/completions",
			defaultProvider: config.ProviderAnthropic,
			want:            config.ProviderOpenAI,
		},
		{
			name:            "completions routes to openai",
			path:            "/v1/completions",
			defaultProvider: config.ProviderAnthropic,
			want:            config.ProviderOpenAI,
		},
		{
			name:            "messages routes to anthropic",
			path:            "/v1/messages",
			defaultProvider: config.ProviderOpenAI,
			want:            config.ProviderAnthropic,
		},
		{
			name:            "unknown path uses default",
			path:            "/v1/models",
			defaultProvider: config.ProviderCustom,
			want:            config.ProviderCustom,
		},
		{
			name:            "trailing slash normalized",
			path:            "/v1/chat/completions/",
			defaultProvider: config.ProviderAnthropic,
			want:            config.ProviderOpenAI,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := detectProvider(tt.path, tt.defaultProvider)
			if got != tt.want {
				t.Fatalf("detectProvider(%q, %q) = %q, want %q", tt.path, tt.defaultProvider, got, tt.want)
			}
		})
	}
}

func TestStripOberwatchHeaders_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input http.Header
		want  http.Header
		name  string
	}{
		{
			name: "removes all x-oberwatch variants and keeps others",
			input: http.Header{
				"Authorization":         []string{"Bearer key"},
				"X-Custom":              []string{"ok"},
				"X-Oberwatch-Agent":     []string{"agent"},
				"X-OBERWATCH-Trace-ID":  []string{"trace"},
				"X-oberwatch-parent-id": []string{"parent"},
			},
			want: http.Header{
				"Authorization": []string{"Bearer key"},
				"X-Custom":      []string{"ok"},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.input.Clone()
			stripOberwatchHeaders(got)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("stripOberwatchHeaders() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestBuildTargets_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		upstream   config.UpstreamConfig
		wantErrSub string
		wantCount  int
	}{
		{
			name: "valid openai anthropic and custom urls",
			upstream: config.UpstreamConfig{
				OpenAI:    config.ProviderEndpoint{BaseURL: "https://api.openai.com"},
				Anthropic: config.ProviderEndpoint{BaseURL: "https://api.anthropic.com"},
				Custom:    config.ProviderEndpoint{BaseURL: "https://llm.example.com"},
			},
			wantCount: 3,
		},
		{
			name: "invalid url is rejected",
			upstream: config.UpstreamConfig{
				OpenAI:    config.ProviderEndpoint{BaseURL: "://bad-url"},
				Anthropic: config.ProviderEndpoint{BaseURL: "https://api.anthropic.com"},
			},
			wantErrSub: "parse upstream",
		},
		{
			name: "missing scheme is rejected",
			upstream: config.UpstreamConfig{
				OpenAI:    config.ProviderEndpoint{BaseURL: "api.openai.com"},
				Anthropic: config.ProviderEndpoint{BaseURL: "https://api.anthropic.com"},
			},
			wantErrSub: "must include scheme and host",
		},
		{
			name: "missing required openai target",
			upstream: config.UpstreamConfig{
				Anthropic: config.ProviderEndpoint{BaseURL: "https://api.anthropic.com"},
			},
			wantErrSub: "upstream \"openai\" base URL must be configured",
		},
		{
			name: "missing required anthropic target",
			upstream: config.UpstreamConfig{
				OpenAI: config.ProviderEndpoint{BaseURL: "https://api.openai.com"},
			},
			wantErrSub: "upstream \"anthropic\" base URL must be configured",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := buildTargets(tt.upstream)
			if tt.wantErrSub != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("buildTargets() error = %v, want substring %q", err, tt.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildTargets() error = %v", err)
			}
			if len(got) != tt.wantCount {
				t.Fatalf("len(buildTargets()) = %d, want %d", len(got), tt.wantCount)
			}
		})
	}
}

func TestWriteHealthResponse_TableDriven(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep table fields explicit for test readability.
	tests := []struct {
		wantStatusCode int
		wantStatus     string
	}{
		{
			wantStatusCode: http.StatusOK,
			wantStatus:     "ok",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run("write health response", func(t *testing.T) {
			t.Parallel()

			recorder := httptest.NewRecorder()
			writeHealthResponse(recorder)

			if recorder.Code != tt.wantStatusCode {
				t.Fatalf("status code = %d, want %d", recorder.Code, tt.wantStatusCode)
			}
			if got := recorder.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q, want %q", got, "application/json")
			}

			var payload map[string]string
			if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			if payload["status"] != tt.wantStatus {
				t.Fatalf("payload status = %q, want %q", payload["status"], tt.wantStatus)
			}
		})
	}
}

func TestNew_HealthPathRunsHookChainWithoutUpstreamCalls(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		wantHookOrder []string
	}{
		{
			name:          "gate then trace for health path",
			wantHookOrder: []string{"gate", "trace"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := config.DefaultConfig()
			cfg.Upstream.OpenAI.BaseURL = "https://api.openai.com"
			cfg.Upstream.Anthropic.BaseURL = "https://api.anthropic.com"
			cfg.Upstream.DefaultProvider = config.ProviderOpenAI

			var mu sync.Mutex
			order := make([]string, 0, 2)
			hooks := Hooks{
				Gate: func(*http.Request) {
					mu.Lock()
					order = append(order, "gate")
					mu.Unlock()
				},
				Trace: func(*http.Request) {
					mu.Lock()
					order = append(order, "trace")
					mu.Unlock()
				},
			}

			server, err := New(cfg, hooks)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			req := httptest.NewRequest(http.MethodGet, healthPath, nil)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusOK)
			}

			var payload map[string]string
			if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			if payload["status"] != "ok" {
				t.Fatalf("payload status = %q, want %q", payload["status"], "ok")
			}

			mu.Lock()
			gotOrder := append([]string(nil), order...)
			mu.Unlock()
			if !reflect.DeepEqual(gotOrder, tt.wantHookOrder) {
				t.Fatalf("hook order = %#v, want %#v", gotOrder, tt.wantHookOrder)
			}
		})
	}
}

func TestGateMiddleware_BudgetRejectAndDowngrade(t *testing.T) {
	t.Parallel()

	t.Run("reject stops before next", func(t *testing.T) {
		t.Parallel()

		cfg := config.DefaultConfig()
		cfg.Gate.DefaultBudget.LimitUSD = 1
		cfg.Gate.DefaultBudget.ActionOnExceed = config.BudgetActionReject

		manager := budget.NewManager(cfg.Gate, nil)
		manager.RecordSpend("agent-a", 1)
		table := pricing.NewPricingTableFromConfig(cfg.Pricing, nil)

		var called bool
		handler := gateMiddleware(Hooks{
			Budget:  manager,
			Pricing: table,
		})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(http.StatusNoContent)
		}))

		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o"}`))
		req.Header.Set("X-Oberwatch-Agent", "agent-a")
		recorder := httptest.NewRecorder()

		handler.ServeHTTP(recorder, req)
		if called {
			t.Fatal("next handler was called, want blocked")
		}
		if recorder.Code != http.StatusTooManyRequests {
			t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusTooManyRequests)
		}
	})

	t.Run("downgrade rewrites request body", func(t *testing.T) {
		t.Parallel()

		cfg := config.DefaultConfig()
		cfg.Gate.DefaultBudget.LimitUSD = 10
		cfg.Gate.DefaultBudget.ActionOnExceed = config.BudgetActionDowngrade
		cfg.Gate.DowngradeThresholdPct = 50
		cfg.Gate.DefaultDowngradeChain = []string{"claude-opus-4-6", "claude-sonnet-4-6", "claude-haiku-4-5"}

		manager := budget.NewManager(cfg.Gate, nil)
		manager.RecordSpend("agent-b", 6)
		table := pricing.NewPricingTableFromConfig(cfg.Pricing, nil)

		handler := gateMiddleware(Hooks{
			Budget:  manager,
			Pricing: table,
		})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			payload, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}
			if !strings.Contains(string(payload), `"model":"claude-sonnet-4-6"`) {
				t.Fatalf("rewritten payload = %s, want downgraded model", string(payload))
			}
			w.WriteHeader(http.StatusNoContent)
		}))

		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"claude-opus-4-6","stream":false}`))
		req.Header.Set("X-Oberwatch-Agent", "agent-b")
		recorder := httptest.NewRecorder()

		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusNoContent)
		}
	})
}

func TestGateMiddleware_ConfigErrorOnReadFailure(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	manager := budget.NewManager(cfg.Gate, nil)
	table := pricing.NewPricingTableFromConfig(cfg.Pricing, nil)

	var called bool
	handler := gateMiddleware(Hooks{
		Budget:  manager,
		Pricing: table,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Body = errorReadCloser{err: errors.New("boom")}
	req.ContentLength = 10
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)
	if called {
		t.Fatal("next handler was called, want blocked on config error")
	}
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(recorder.Body.String(), "config_error") {
		t.Fatalf("response body = %q, want config_error payload", recorder.Body.String())
	}
}

func TestBudgetTrackingBody_FinalizePaths(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	manager := budget.NewManager(cfg.Gate, nil)
	table := pricing.NewPricingTableFromConfig(cfg.Pricing, nil)

	tracker := newBudgetTrackingBody(
		io.NopCloser(strings.NewReader(`{"usage":{"prompt_tokens":100,"completion_tokens":50}}`)),
		http.StatusOK,
		"application/json",
		budgetRequestMeta{agent: "agent-usage", model: "gpt-4o", provider: "openai"},
		manager,
		table,
		nil,
	)
	if _, err := io.ReadAll(tracker); err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if err := tracker.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if spent := manager.Snapshot("agent-usage").SpentUSD; spent <= 0 {
		t.Fatalf("spent = %v, want > 0", spent)
	}

	tracker = newBudgetTrackingBody(
		io.NopCloser(strings.NewReader("upstream failure")),
		http.StatusBadGateway,
		"text/plain",
		budgetRequestMeta{agent: "agent-usage", model: "gpt-4o", provider: "openai"},
		manager,
		table,
		nil,
	)
	if _, err := io.ReadAll(tracker); err != nil {
		t.Fatalf("ReadAll(non2xx) error = %v", err)
	}
	if err := tracker.Close(); err != nil {
		t.Fatalf("Close(non2xx) error = %v", err)
	}
}

func TestHelperFunctions_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		body       string
		wantModel  string
		wantStream bool
	}{
		{name: "valid body", body: `{"model":"gpt-4o","stream":true}`, wantModel: "gpt-4o", wantStream: true},
		{name: "invalid body", body: `{`, wantModel: "", wantStream: false},
		{name: "empty body", body: ``, wantModel: "", wantStream: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			model, streaming := extractModelAndStream([]byte(tt.body))
			if model != tt.wantModel || streaming != tt.wantStream {
				t.Fatalf("extractModelAndStream(%q) = (%q,%v), want (%q,%v)", tt.body, model, streaming, tt.wantModel, tt.wantStream)
			}
		})
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	logDowngrade(logger, "agent-a", "a", "b")

	recorder := httptest.NewRecorder()
	writeConfigError(recorder, "bad config")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("writeConfigError status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}

	recorder = httptest.NewRecorder()
	writeBudgetError(recorder, budget.Decision{
		Code:     "agent_killed",
		Message:  "killed",
		Agent:    "agent-x",
		LimitUSD: 10,
		SpentUSD: 11,
		Period:   config.BudgetPeriodDaily,
	}, http.StatusTooManyRequests)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("writeBudgetError status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if !strings.Contains(recorder.Body.String(), `"agent_killed"`) {
		t.Fatalf("writeBudgetError body = %q, want agent_killed code", recorder.Body.String())
	}
}

type errorReadCloser struct {
	err error
}

func (e errorReadCloser) Read([]byte) (int, error) {
	return 0, e.err
}

func (e errorReadCloser) Close() error {
	return nil
}

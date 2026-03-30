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
	"github.com/OberWatch/oberwatch/internal/storage"
)

func TestDetectProviderForModel_TableDriven(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test table fields grouped for readability.
	tests := []struct {
		name  string
		model string
		want  config.ProviderConfigName
		ok    bool
	}{
		{
			name:  "gpt routes to openai",
			model: "gpt-4o-mini",
			want:  config.ProviderOpenAI,
			ok:    true,
		},
		{
			name:  "gemini routes to google",
			model: "gemini-2.5-flash",
			want:  config.ProviderGoogle,
			ok:    true,
		},
		{
			name:  "claude routes to anthropic",
			model: "claude-haiku-4-5",
			want:  config.ProviderAnthropic,
			ok:    true,
		},
		{
			name:  "llama routes to ollama",
			model: "llama3.1:8b",
			want:  config.ProviderOllama,
			ok:    true,
		},
		{
			name:  "unknown model is rejected",
			model: "mystery-provider-model",
			ok:    false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := detectProviderForModel(tt.model)
			if ok != tt.ok {
				t.Fatalf("detectProviderForModel(%q) ok = %v, want %v", tt.model, ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("detectProviderForModel(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

func TestResolveProvider_TableDriven(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test table fields grouped for readability.
	tests := []struct {
		name        string
		path        string
		requestBody string
		header      string
		want        resolvedRoute
		wantErrSub  string
	}{
		{
			name:        "messages uses anthropic",
			path:        "/v1/messages",
			requestBody: `{"model":"claude-haiku-4-5"}`,
			want:        resolvedRoute{provider: config.ProviderAnthropic},
		},
		{
			name:        "openai-compatible gemini uses google",
			path:        "/v1/chat/completions",
			requestBody: `{"model":"gemini-2.5-flash"}`,
			want:        resolvedRoute{provider: config.ProviderGoogle},
		},
		{
			name:        "claude chat uses anthropic compatibility",
			path:        "/v1/chat/completions",
			requestBody: `{"model":"claude-haiku-4-5"}`,
			want:        resolvedRoute{provider: config.ProviderAnthropic, anthropicCompat: true},
		},
		{
			name:       "models requires provider header",
			path:       "/v1/models",
			wantErrSub: providerOverrideHeader,
		},
		{
			name:   "models accepts explicit provider header",
			path:   "/v1/models",
			header: string(config.ProviderOpenAI),
			want:   resolvedRoute{provider: config.ProviderOpenAI},
		},
		{
			name:        "unknown model fails",
			path:        "/v1/chat/completions",
			requestBody: `{"model":"mystery-provider-model"}`,
			wantErrSub:  "could not resolve provider",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.requestBody))
			if tt.header != "" {
				req.Header.Set(providerOverrideHeader, tt.header)
			}

			got, err := resolveProvider(req, []byte(tt.requestBody), config.ProviderOpenAI)
			if tt.wantErrSub != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("resolveProvider() error = %v, want substring %q", err, tt.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveProvider() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("resolveProvider() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestOpenAIChatToAnthropic_TableDriven(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test table fields grouped for readability.
	tests := []struct {
		name         string
		requestBody  string
		wantContains []string
		wantModel    string
		wantStream   bool
		wantErrSub   string
	}{
		{
			name:        "translates simple chat request",
			requestBody: `{"model":"claude-haiku-4-5","messages":[{"role":"system","content":"be concise"},{"role":"user","content":"hello"}]}`,
			wantContains: []string{
				`"model":"claude-haiku-4-5"`,
				`"system":"be concise"`,
				`"role":"user"`,
				`"content":"hello"`,
				`"max_tokens":1024`,
			},
			wantModel:  "claude-haiku-4-5",
			wantStream: false,
		},
		{
			name:        "missing model fails",
			requestBody: `{"messages":[{"role":"user","content":"hello"}]}`,
			wantErrSub:  "request model is required",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotBody, gotModel, gotStream, err := openAIChatToAnthropic([]byte(tt.requestBody))
			if tt.wantErrSub != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("openAIChatToAnthropic() error = %v, want substring %q", err, tt.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("openAIChatToAnthropic() error = %v", err)
			}
			if gotModel != tt.wantModel {
				t.Fatalf("model = %q, want %q", gotModel, tt.wantModel)
			}
			if gotStream != tt.wantStream {
				t.Fatalf("stream = %v, want %v", gotStream, tt.wantStream)
			}
			for _, want := range tt.wantContains {
				if !strings.Contains(string(gotBody), want) {
					t.Fatalf("body = %q, want substring %q", string(gotBody), want)
				}
			}
		})
	}
}

func TestAnthropicToOpenAIChatCompletion_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		responseBody string
		wantContains []string
	}{
		{
			name:         "translates anthropic response",
			responseBody: `{"id":"msg_123","model":"claude-haiku-4-5","content":[{"type":"text","text":"hello back"}],"usage":{"input_tokens":18,"output_tokens":10},"stop_reason":"end_turn"}`,
			wantContains: []string{`"object":"chat.completion"`, `"model":"claude-haiku-4-5"`, `"content":"hello back"`, `"prompt_tokens":18`, `"completion_tokens":10`},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := anthropicToOpenAIChatCompletion([]byte(tt.responseBody), "claude-haiku-4-5")
			if err != nil {
				t.Fatalf("anthropicToOpenAIChatCompletion() error = %v", err)
			}
			for _, want := range tt.wantContains {
				if !strings.Contains(string(got), want) {
					t.Fatalf("body = %q, want substring %q", string(got), want)
				}
			}
		})
	}
}

func TestProviderRoutingHelpers_TableDriven(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test table fields grouped for readability.
	tests := []struct {
		name     string
		header   string
		want     config.ProviderConfigName
		ok       bool
		rawPath  string
		wantOpen bool
	}{
		{name: "provider override parses google", header: "google", want: config.ProviderGoogle, ok: true},
		{name: "provider override rejects unknown", header: "grok", ok: false},
		{name: "openai-compatible chat path is recognized", rawPath: "/v1/chat/completions", wantOpen: true},
		{name: "native ollama path is not openai-compatible", rawPath: "/api/chat", wantOpen: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if tt.header != "" {
				req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
				req.Header.Set(providerOverrideHeader, tt.header)
				got, ok := parseProviderOverride(req)
				if ok != tt.ok {
					t.Fatalf("parseProviderOverride() ok = %v, want %v", ok, tt.ok)
				}
				if got != tt.want {
					t.Fatalf("parseProviderOverride() = %q, want %q", got, tt.want)
				}
			}
			if tt.rawPath != "" {
				if got := isOpenAICompatiblePath(tt.rawPath); got != tt.wantOpen {
					t.Fatalf("isOpenAICompatiblePath(%q) = %v, want %v", tt.rawPath, got, tt.wantOpen)
				}
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

func TestNew_ManagementRoutesTakePrecedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
	}{
		{
			name: "management budgets path is served by management handler",
			path: "/_oberwatch/api/v1/budgets",
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

			var managementHits int
			server, err := New(cfg, Hooks{
				Management: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					managementHits++
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusCreated)
					if _, writeErr := w.Write([]byte(`{"status":"management"}`)); writeErr != nil {
						t.Fatalf("Write() error = %v", writeErr)
					}
				}),
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusCreated {
				t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusCreated)
			}
			if managementHits != 1 {
				t.Fatalf("management hits = %d, want %d", managementHits, 1)
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

func TestBudgetTrackingBody_EnqueuesCostRecord(t *testing.T) {
	t.Parallel()

	type sink struct {
		records []storage.CostRecord
		mu      sync.Mutex
	}
	var testSink sink
	enqueue := &struct{ storage.CostRecordSink }{}
	enqueue.CostRecordSink = sinkFunc(func(record storage.CostRecord) {
		testSink.mu.Lock()
		testSink.records = append(testSink.records, record)
		testSink.mu.Unlock()
	})

	cfg := config.DefaultConfig()
	manager := budget.NewManager(cfg.Gate, nil)
	table := pricing.NewPricingTableFromConfig(cfg.Pricing, nil)

	reader := newBudgetTrackingBody(
		io.NopCloser(strings.NewReader(`{"usage":{"prompt_tokens":10,"completion_tokens":5}}`)),
		http.StatusOK,
		"application/json",
		budgetRequestMeta{
			agent:     "agent-a",
			model:     "gpt-4o",
			provider:  "openai",
			traceID:   "tr-1",
			taskID:    "task-1",
			streaming: false,
		},
		manager,
		table,
		enqueue.CostRecordSink,
		nil,
	)
	if _, err := io.ReadAll(reader); err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	testSink.mu.Lock()
	defer testSink.mu.Unlock()
	if len(testSink.records) != 1 {
		t.Fatalf("sink record count = %d, want 1", len(testSink.records))
	}
	if testSink.records[0].TraceID != "tr-1" || testSink.records[0].TaskID != "task-1" {
		t.Fatalf("persisted cost record metadata = %#v, want trace/task IDs", testSink.records[0])
	}
}

func TestServer_DirectProxyErrorPathWithoutNetworkListener(t *testing.T) {
	t.Parallel()

	//nolint:govet // Keep table fields grouped for request/expectation readability.
	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "proxy path returns bad gateway when upstream is unreachable",
			path:       "/v1/chat/completions",
			wantStatus: http.StatusBadGateway,
			wantBody:   "upstream proxy error",
		},
		{
			name:       "management path without handler falls through to proxy and fails upstream",
			path:       "/_oberwatch/api/v1/budgets",
			wantStatus: http.StatusBadGateway,
			wantBody:   "upstream proxy error",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := config.DefaultConfig()
			cfg.Upstream.OpenAI.BaseURL = "http://127.0.0.1:1"
			cfg.Upstream.Anthropic.BaseURL = "http://127.0.0.1:1"
			cfg.Upstream.DefaultProvider = config.ProviderOpenAI

			server, err := New(cfg, Hooks{})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(`{"model":"gpt-4o","stream":false}`))
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)

			if recorder.Code != tt.wantStatus {
				t.Fatalf("status code = %d, want %d", recorder.Code, tt.wantStatus)
			}
			if !strings.Contains(recorder.Body.String(), tt.wantBody) {
				t.Fatalf("body = %q, want substring %q", recorder.Body.String(), tt.wantBody)
			}
		})
	}
}

type errorReadCloser struct {
	err error
}

type sinkFunc func(storage.CostRecord)

func (f sinkFunc) Enqueue(record storage.CostRecord) {
	f(record)
}

func (e errorReadCloser) Read([]byte) (int, error) {
	return 0, e.err
}

func (e errorReadCloser) Close() error {
	return nil
}

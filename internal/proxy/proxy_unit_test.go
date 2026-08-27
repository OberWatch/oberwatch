package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestWriteProviderResolutionError_TableDriven(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	writeProviderResolutionError(recorder, "no provider", http.StatusBadRequest)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	if !strings.Contains(recorder.Body.String(), "provider_resolution_failed") {
		t.Fatalf("body = %q, want provider resolution error code", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "no provider") {
		t.Fatalf("body = %q, want original message", recorder.Body.String())
	}
}

func TestCompatibilityHelpers_TableDriven(t *testing.T) {
	t.Parallel()

	t.Run("extracts message content text", func(t *testing.T) {
		t.Parallel()

		got := extractMessageContentText([]any{
			map[string]any{"type": "text", "text": "hello"},
			map[string]any{"type": "image", "text": "ignore"},
			map[string]any{"type": "text", "text": "world"},
		})
		if got != "hello\nworld" {
			t.Fatalf("extractMessageContentText() = %q, want %q", got, "hello\nworld")
		}
	})

	t.Run("extracts bearer token", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer test-token")
		if got := extractBearerToken(req); got != "test-token" {
			t.Fatalf("extractBearerToken() = %q, want %q", got, "test-token")
		}
	})

	t.Run("copies compatibility headers safely", func(t *testing.T) {
		t.Parallel()

		src := http.Header{
			"Authorization":     []string{"Bearer token"},
			"X-API-Key":         []string{"secret"},
			"X-Oberwatch-Agent": []string{"agent"},
			"X-Custom":          []string{"ok"},
		}
		dst := make(http.Header)
		copyCompatibilityHeaders(dst, src)
		if dst.Get("Authorization") != "" || dst.Get("X-API-Key") != "" || dst.Get("X-Oberwatch-Agent") != "" {
			t.Fatalf("copyCompatibilityHeaders() leaked protected headers: %#v", dst)
		}
		if dst.Get("X-Custom") != "ok" {
			t.Fatalf("copyCompatibilityHeaders() missing custom header: %#v", dst)
		}
	})

	t.Run("maps anthropic stop reasons", func(t *testing.T) {
		t.Parallel()

		if got := mapAnthropicStopReason("max_tokens"); got != "length" {
			t.Fatalf("mapAnthropicStopReason(max_tokens) = %q, want %q", got, "length")
		}
		if got := mapAnthropicStopReason("end_turn"); got != "stop" {
			t.Fatalf("mapAnthropicStopReason(end_turn) = %q, want %q", got, "stop")
		}
	})
}

func TestProviderRoutingMiddleware_TableDriven(t *testing.T) {
	t.Parallel()

	mustParseURL := func(t *testing.T, raw string) *url.URL {
		t.Helper()
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("url.Parse(%q) error = %v", raw, err)
		}
		return parsed
	}

	//nolint:govet // keep test table fields grouped for readability.
	tests := []struct {
		name           string
		path           string
		body           string
		header         string
		readErr        error
		targets        map[config.ProviderConfigName]*url.URL
		wantStatus     int
		wantProvider   config.ProviderConfigName
		wantCalledNext bool
		wantBodySub    string
	}{
		{
			name:           "non proxy path passes through untouched",
			path:           "/",
			targets:        map[config.ProviderConfigName]*url.URL{config.ProviderOpenAI: mustParseURL(t, "https://api.openai.com")},
			wantStatus:     http.StatusNoContent,
			wantCalledNext: true,
		},
		{
			name:           "gemini request resolves to google",
			path:           "/v1/chat/completions",
			body:           `{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}]}`,
			targets:        map[config.ProviderConfigName]*url.URL{config.ProviderGoogle: mustParseURL(t, "https://generativelanguage.googleapis.com")},
			wantStatus:     http.StatusNoContent,
			wantProvider:   config.ProviderGoogle,
			wantCalledNext: true,
		},
		{
			name:        "models without header fail",
			path:        "/v1/models",
			targets:     map[config.ProviderConfigName]*url.URL{config.ProviderOpenAI: mustParseURL(t, "https://api.openai.com")},
			wantStatus:  http.StatusBadRequest,
			wantBodySub: providerOverrideHeader,
		},
		{
			name:        "resolved provider must be configured",
			path:        "/v1/chat/completions",
			body:        `{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}]}`,
			targets:     map[config.ProviderConfigName]*url.URL{config.ProviderOpenAI: mustParseURL(t, "https://api.openai.com")},
			wantStatus:  http.StatusBadRequest,
			wantBodySub: `provider \"google\" is not configured`,
		},
		{
			name:        "body read error returns config error",
			path:        "/v1/chat/completions",
			readErr:     errors.New("boom"),
			targets:     map[config.ProviderConfigName]*url.URL{config.ProviderOpenAI: mustParseURL(t, "https://api.openai.com")},
			wantStatus:  http.StatusInternalServerError,
			wantBodySub: "config_error",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var called bool
			var gotRoute resolvedRoute
			handler := providerRoutingMiddleware(config.ProviderOpenAI, tt.targets, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				route, ok := resolvedRouteFromContext(r.Context())
				if ok {
					gotRoute = route
				}
				w.WriteHeader(http.StatusNoContent)
			}))

			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			if tt.header != "" {
				req.Header.Set(providerOverrideHeader, tt.header)
			}
			if tt.readErr != nil {
				req.Body = errorReadCloser{err: tt.readErr}
				req.ContentLength = 10
			}
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, req)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status code = %d, want %d", recorder.Code, tt.wantStatus)
			}
			if called != tt.wantCalledNext {
				t.Fatalf("next called = %v, want %v", called, tt.wantCalledNext)
			}
			if tt.wantProvider != "" && gotRoute.provider != tt.wantProvider {
				t.Fatalf("resolved provider = %q, want %q", gotRoute.provider, tt.wantProvider)
			}
			if tt.wantBodySub != "" && !strings.Contains(recorder.Body.String(), tt.wantBodySub) {
				t.Fatalf("body = %q, want substring %q", recorder.Body.String(), tt.wantBodySub)
			}
		})
	}
}

func TestServeAnthropicOpenAICompat_TableDriven(t *testing.T) {
	withHTTPTransport := func(t *testing.T, transport http.RoundTripper) {
		t.Helper()
		original := http.DefaultClient.Transport
		http.DefaultClient.Transport = transport
		t.Cleanup(func() {
			http.DefaultClient.Transport = original
		})
	}

	t.Run("streaming requests are rejected", func(t *testing.T) {
		target, err := url.Parse("https://api.anthropic.com")
		if err != nil {
			t.Fatalf("url.Parse() error = %v", err)
		}

		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"claude-haiku-4-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
		recorder := httptest.NewRecorder()

		serveAnthropicOpenAICompat(recorder, req, resolvedRoute{provider: config.ProviderAnthropic, anthropicCompat: true}, map[config.ProviderConfigName]*url.URL{config.ProviderAnthropic: target}, Hooks{})

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusBadRequest)
		}
		if !strings.Contains(recorder.Body.String(), "non-streaming") {
			t.Fatalf("body = %q, want non-streaming error", recorder.Body.String())
		}
	})

	t.Run("upstream error is passed through", func(t *testing.T) {
		target, err := url.Parse("https://api.anthropic.com")
		if err != nil {
			t.Fatalf("url.Parse() error = %v", err)
		}
		withHTTPTransport(t, roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":"bad api key"}`)),
			}, nil
		}))

		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"claude-haiku-4-5","messages":[{"role":"user","content":"hello"}]}`))
		req.Header.Set("Authorization", "Bearer bad-key")
		recorder := httptest.NewRecorder()

		serveAnthropicOpenAICompat(recorder, req, resolvedRoute{provider: config.ProviderAnthropic, anthropicCompat: true}, map[config.ProviderConfigName]*url.URL{config.ProviderAnthropic: target}, Hooks{})

		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusUnauthorized)
		}
		if !strings.Contains(recorder.Body.String(), "bad api key") {
			t.Fatalf("body = %q, want upstream error", recorder.Body.String())
		}
	})

	t.Run("successful translation records spend and sink data", func(t *testing.T) {
		target, err := url.Parse("https://api.anthropic.com")
		if err != nil {
			t.Fatalf("url.Parse() error = %v", err)
		}
		withHTTPTransport(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if got := r.Header.Get("x-api-key"); got != "anthropic-key" {
				t.Fatalf("x-api-key = %q, want %q", got, "anthropic-key")
			}
			if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
				t.Fatalf("anthropic-version = %q, want %q", got, "2023-06-01")
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"id":"msg_123","model":"claude-haiku-4-5","content":[{"type":"text","text":"hello back"}],"usage":{"input_tokens":18,"output_tokens":10},"stop_reason":"end_turn"}`)),
			}, nil
		}))

		cfg := config.DefaultConfig()
		manager := budget.NewManager(cfg.Gate, nil)
		table := pricing.NewPricingTableFromConfig(cfg.Pricing, nil)
		var records []storage.CostRecord
		sink := sinkFunc(func(record storage.CostRecord) {
			records = append(records, record)
		})

		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"claude-haiku-4-5","messages":[{"role":"user","content":"hello"}]}`))
		req.Header.Set("Authorization", "Bearer anthropic-key")
		meta := budgetRequestMeta{
			agent:    "agent-a",
			model:    "claude-haiku-4-5",
			provider: string(config.ProviderAnthropic),
		}
		req = req.WithContext(context.WithValue(req.Context(), budgetContextKey{}, meta))
		recorder := httptest.NewRecorder()

		serveAnthropicOpenAICompat(recorder, req, resolvedRoute{provider: config.ProviderAnthropic, anthropicCompat: true}, map[config.ProviderConfigName]*url.URL{config.ProviderAnthropic: target}, Hooks{
			Budget:   manager,
			Pricing:  table,
			CostSink: sink,
		})

		if recorder.Code != http.StatusOK {
			t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusOK)
		}
		if !strings.Contains(recorder.Body.String(), `"object":"chat.completion"`) {
			t.Fatalf("body = %q, want OpenAI-compatible response", recorder.Body.String())
		}
		if spent := manager.Snapshot("agent-a").SpentUSD; spent <= 0 {
			t.Fatalf("spent = %v, want > 0", spent)
		}
		if len(records) != 1 {
			t.Fatalf("sink records = %d, want 1", len(records))
		}
	})
}

func TestProviderRoutingAdditionalBranches_TableDriven(t *testing.T) {
	t.Parallel()

	t.Run("resolve provider handles api path override and missing model branches", func(t *testing.T) {
		t.Parallel()

		//nolint:govet // keep test table fields grouped for readability.
		tests := []struct {
			name       string
			path       string
			body       string
			header     string
			want       resolvedRoute
			wantErrSub string
		}{
			{name: "native ollama api path", path: "/api/chat", want: resolvedRoute{provider: config.ProviderOllama}},
			{name: "provider override on openai-compatible path", path: "/v1/chat/completions", body: `{"model":"gemini-2.5-flash"}`, header: string(config.ProviderGoogle), want: resolvedRoute{provider: config.ProviderGoogle}},
			{name: "anthropic override on chat path enables compatibility", path: "/v1/chat/completions", body: `{"model":"claude-haiku-4-5"}`, header: string(config.ProviderAnthropic), want: resolvedRoute{provider: config.ProviderAnthropic, anthropicCompat: true}},
			{name: "missing model is rejected", path: "/v1/chat/completions", body: `{}`, wantErrSub: "request model is required"},
			{name: "claude embeddings path is rejected", path: "/v1/embeddings", body: `{"model":"claude-haiku-4-5"}`, wantErrSub: "only supports /v1/chat/completions"},
			{name: "empty path normalizes to slash and uses default", path: "", want: resolvedRoute{provider: config.ProviderOpenAI}},
		}

		for _, tt := range tests {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				requestPath := tt.path
				if requestPath == "" {
					requestPath = "/"
				}
				req := httptest.NewRequest(http.MethodPost, requestPath, strings.NewReader(tt.body))
				if tt.path == "" {
					req.URL.Path = ""
				}
				if tt.header != "" {
					req.Header.Set(providerOverrideHeader, tt.header)
				}
				got, err := resolveProvider(req, []byte(tt.body), config.ProviderOpenAI)
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
	})

	t.Run("resolve provider rejects anthropic override on unsupported path", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"claude-haiku-4-5"}`))
		req.Header.Set(providerOverrideHeader, string(config.ProviderAnthropic))

		_, err := resolveProvider(req, []byte(`{"model":"claude-haiku-4-5"}`), config.ProviderOpenAI)
		if err == nil || !strings.Contains(err.Error(), "only supports /v1/chat/completions") {
			t.Fatalf("resolveProvider() error = %v, want anthropic path restriction", err)
		}
	})

	t.Run("resolve provider falls back to default on unknown non-proxy path", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/internal/healthz", nil)
		got, err := resolveProvider(req, nil, config.ProviderCustom)
		if err != nil {
			t.Fatalf("resolveProvider() error = %v", err)
		}
		if got.provider != config.ProviderCustom {
			t.Fatalf("resolved provider = %q, want %q", got.provider, config.ProviderCustom)
		}
	})

	t.Run("resolve provider fails when unknown path has no default", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, "/internal/healthz", nil)
		_, err := resolveProvider(req, nil, "")
		if err == nil || !strings.Contains(err.Error(), "could not resolve provider") {
			t.Fatalf("resolveProvider() error = %v, want missing default failure", err)
		}
	})

	t.Run("openai to anthropic requires non-system message", func(t *testing.T) {
		t.Parallel()

		_, _, _, err := openAIChatToAnthropic([]byte(`{"model":"claude-haiku-4-5","messages":[{"role":"system","content":"only system"}]}`))
		if err == nil || !strings.Contains(err.Error(), "non-system message") {
			t.Fatalf("openAIChatToAnthropic() error = %v, want non-system message failure", err)
		}
	})

	t.Run("openai to anthropic rejects malformed json", func(t *testing.T) {
		t.Parallel()

		_, _, _, err := openAIChatToAnthropic([]byte(`{`))
		if err == nil || !strings.Contains(err.Error(), "decode OpenAI chat completion request") {
			t.Fatalf("openAIChatToAnthropic() error = %v, want decode failure", err)
		}
	})

	t.Run("openai to anthropic preserves temperature", func(t *testing.T) {
		t.Parallel()

		body, _, _, err := openAIChatToAnthropic([]byte(`{"model":"claude-haiku-4-5","temperature":0.25,"messages":[{"role":"user","content":"hello"}]}`))
		if err != nil {
			t.Fatalf("openAIChatToAnthropic() error = %v", err)
		}
		if !strings.Contains(string(body), `"temperature":0.25`) {
			t.Fatalf("body = %q, want temperature preserved", string(body))
		}
	})

	t.Run("anthropic response decode failure surfaces", func(t *testing.T) {
		t.Parallel()

		_, err := anthropicToOpenAIChatCompletion([]byte(`{`), "claude-haiku-4-5")
		if err == nil || !strings.Contains(err.Error(), "decode anthropic response") {
			t.Fatalf("anthropicToOpenAIChatCompletion() error = %v, want decode failure", err)
		}
	})

	t.Run("copy compatibility response headers skips content length", func(t *testing.T) {
		t.Parallel()

		src := http.Header{
			"Content-Length": []string{"99"},
			"X-Upstream":     []string{"ok"},
		}
		dst := make(http.Header)
		copyCompatibilityResponseHeaders(dst, src)
		if dst.Get("Content-Length") != "" {
			t.Fatalf("copyCompatibilityResponseHeaders() copied Content-Length: %#v", dst)
		}
		if dst.Get("X-Upstream") != "ok" {
			t.Fatalf("copyCompatibilityResponseHeaders() missing X-Upstream header: %#v", dst)
		}
	})

	t.Run("serve anthropic compat rejects missing target", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"claude-haiku-4-5","messages":[{"role":"user","content":"hello"}]}`))
		recorder := httptest.NewRecorder()

		serveAnthropicOpenAICompat(recorder, req, resolvedRoute{provider: config.ProviderAnthropic, anthropicCompat: true}, map[config.ProviderConfigName]*url.URL{}, Hooks{})
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusBadRequest)
		}
		if !strings.Contains(recorder.Body.String(), "not configured") {
			t.Fatalf("body = %q, want missing target error", recorder.Body.String())
		}
	})

	t.Run("middleware preserves body via GetBody and resolves route with logger", func(t *testing.T) {
		t.Parallel()

		target, err := url.Parse("https://generativelanguage.googleapis.com")
		if err != nil {
			t.Fatalf("url.Parse() error = %v", err)
		}

		var gotBody string
		var gotRoute resolvedRoute
		handler := providerRoutingMiddleware(config.ProviderOpenAI, map[config.ProviderConfigName]*url.URL{
			config.ProviderGoogle: target,
		}, slog.New(slog.NewTextHandler(io.Discard, nil)))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			route, ok := resolvedRouteFromContext(r.Context())
			if !ok {
				t.Fatal("resolved route missing from context")
			}
			gotRoute = route
			reader, err := r.GetBody()
			if err != nil {
				t.Fatalf("GetBody() error = %v", err)
			}
			defer func() { _ = reader.Close() }()
			body, err := io.ReadAll(reader)
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}
			gotBody = string(body)
			w.WriteHeader(http.StatusNoContent)
		}))

		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hello"}]}`))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)

		if recorder.Code != http.StatusNoContent {
			t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusNoContent)
		}
		if gotRoute.provider != config.ProviderGoogle {
			t.Fatalf("resolved provider = %q, want %q", gotRoute.provider, config.ProviderGoogle)
		}
		if !strings.Contains(gotBody, `"model":"gemini-2.5-flash"`) {
			t.Fatalf("GetBody() payload = %q, want original request body", gotBody)
		}
	})

	t.Run("compat helpers cover remaining branches", func(t *testing.T) {
		t.Parallel()

		if got := extractMessageContentText([]any{"skip", map[string]any{"type": "text", "text": "ok"}}); got != "ok" {
			t.Fatalf("extractMessageContentText() = %q, want %q", got, "ok")
		}
		if got := extractMessageContentText(123); got != "" {
			t.Fatalf("extractMessageContentText(non-text) = %q, want empty", got)
		}

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if got := extractBearerToken(req); got != "" {
			t.Fatalf("extractBearerToken() = %q, want empty", got)
		}
		req.Header.Set("Authorization", "Basic nope")
		if got := extractBearerToken(req); got != "" {
			t.Fatalf("extractBearerToken() with basic auth = %q, want empty", got)
		}

		dst := make(http.Header)
		copyCompatibilityHeaders(dst, http.Header{
			"Host":           []string{"example.com"},
			"Content-Length": []string{"123"},
			"X-API-Key":      []string{"secret"},
			"X-Custom":       []string{"ok"},
		})
		if dst.Get("Host") != "" || dst.Get("Content-Length") != "" || dst.Get("X-API-Key") != "" {
			t.Fatalf("copyCompatibilityHeaders() copied restricted header: %#v", dst)
		}
		if dst.Get("X-Custom") != "ok" {
			t.Fatalf("copyCompatibilityHeaders() missing custom header: %#v", dst)
		}

		if got := mapAnthropicStopReason("tool_use"); got != "tool_calls" {
			t.Fatalf("mapAnthropicStopReason(tool_use) = %q, want %q", got, "tool_calls")
		}
	})

	t.Run("anthropic conversion covers non-text response blocks", func(t *testing.T) {
		t.Parallel()

		got, err := anthropicToOpenAIChatCompletion([]byte(`{"id":"msg_123","model":"claude-haiku-4-5","content":[{"type":"image","text":"ignore"},{"type":"text","text":"hello"}],"usage":{"input_tokens":18,"output_tokens":10},"stop_reason":"tool_use"}`), "claude-haiku-4-5")
		if err != nil {
			t.Fatalf("anthropicToOpenAIChatCompletion() error = %v", err)
		}
		if !strings.Contains(string(got), `"finish_reason":"tool_calls"`) {
			t.Fatalf("body = %q, want tool_calls finish reason", string(got))
		}
		if !strings.Contains(string(got), `"content":"hello"`) {
			t.Fatalf("body = %q, want text block only", string(got))
		}
	})

	t.Run("serve anthropic compat returns config error for unreadable body", func(t *testing.T) {
		t.Parallel()

		target, err := url.Parse("https://api.anthropic.com")
		if err != nil {
			t.Fatalf("url.Parse() error = %v", err)
		}

		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Body = errorReadCloser{err: errors.New("boom")}
		req.ContentLength = 10
		recorder := httptest.NewRecorder()

		serveAnthropicOpenAICompat(recorder, req, resolvedRoute{provider: config.ProviderAnthropic, anthropicCompat: true}, map[config.ProviderConfigName]*url.URL{config.ProviderAnthropic: target}, Hooks{})
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusInternalServerError)
		}
		if !strings.Contains(recorder.Body.String(), "config_error") {
			t.Fatalf("body = %q, want config_error payload", recorder.Body.String())
		}
	})

	t.Run("serve anthropic compat rejects invalid openai payload", func(t *testing.T) {
		t.Parallel()

		target, err := url.Parse("https://api.anthropic.com")
		if err != nil {
			t.Fatalf("url.Parse() error = %v", err)
		}

		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{`))
		recorder := httptest.NewRecorder()

		serveAnthropicOpenAICompat(recorder, req, resolvedRoute{provider: config.ProviderAnthropic, anthropicCompat: true}, map[config.ProviderConfigName]*url.URL{config.ProviderAnthropic: target}, Hooks{})
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusBadRequest)
		}
		if !strings.Contains(recorder.Body.String(), "provider_resolution_failed") {
			t.Fatalf("body = %q, want provider resolution payload", recorder.Body.String())
		}
	})
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

		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.6-terra"}`))
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
		cfg.Gate.DefaultDowngradeChain = []string{"claude-fable-5", "claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5"}

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
			if !strings.Contains(string(payload), `"model":"claude-opus-5"`) {
				t.Fatalf("rewritten payload = %s, want downgraded model", string(payload))
			}
			w.WriteHeader(http.StatusNoContent)
		}))

		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"claude-fable-5","stream":false}`))
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
		budgetRequestMeta{agent: "agent-usage", model: "gpt-5.6-terra", provider: "openai"},
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
		budgetRequestMeta{agent: "agent-usage", model: "gpt-5.6-terra", provider: "openai"},
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f sinkFunc) Enqueue(record storage.CostRecord) {
	f(record)
}

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func (e errorReadCloser) Read([]byte) (int, error) {
	return 0, e.err
}

func (e errorReadCloser) Close() error {
	return nil
}

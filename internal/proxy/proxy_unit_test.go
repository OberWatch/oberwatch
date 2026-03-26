package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/OberWatch/oberwatch/internal/config"
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
		name  string
		want  http.Header
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

	tests := []struct {
		name           string
		wantStatusCode int
		wantStatus     string
	}{
		{
			name:           "returns json ok payload",
			wantStatusCode: http.StatusOK,
			wantStatus:     "ok",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
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

package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OberWatch/oberwatch/internal/config"
)

type capturedRequest struct {
	header http.Header
	body   string
	method string
	path   string
}

func TestServer_ProviderDetectionRouting(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test table fields grouped for readability.
	tests := []struct {
		name            string
		path            string
		requestBody     string
		defaultProvider config.ProviderConfigName
		wantProvider    string
		requestHeaders  map[string]string
	}{
		{
			name:            "messages route to anthropic",
			path:            "/v1/messages",
			requestBody:     `{"model":"claude-haiku-4-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`,
			defaultProvider: config.ProviderOpenAI,
			wantProvider:    "anthropic",
		},
		{
			name:            "models uses explicit provider header",
			path:            "/v1/models",
			defaultProvider: config.ProviderAnthropic,
			wantProvider:    "openai",
			requestHeaders:  map[string]string{providerOverrideHeader: string(config.ProviderOpenAI)},
		},
		{
			name:            "routes with trailing slash by model family",
			path:            "/v1/chat/completions/",
			requestBody:     `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}`,
			defaultProvider: config.ProviderAnthropic,
			wantProvider:    "openai",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			received := make(chan string, 1)
			openAIServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received <- "openai"
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(openAIServer.Close)

			anthropicServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received <- "anthropic"
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(anthropicServer.Close)

			googleServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received <- "google"
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(googleServer.Close)

			ollamaServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received <- "ollama"
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(ollamaServer.Close)

			cfg := testConfig(openAIServer.URL, anthropicServer.URL)
			cfg.Upstream.Google.BaseURL = googleServer.URL
			cfg.Upstream.Ollama.BaseURL = ollamaServer.URL
			cfg.Upstream.DefaultProvider = tt.defaultProvider
			proxyServer, err := New(cfg, Hooks{})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			server := newTestServer(t, proxyServer)
			t.Cleanup(server.Close)

			req, err := http.NewRequest(http.MethodPost, server.URL+tt.path, strings.NewReader(tt.requestBody))
			if err != nil {
				t.Fatalf("NewRequest() error = %v", err)
			}
			for key, value := range tt.requestHeaders {
				req.Header.Set(key, value)
			}

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

			select {
			case got := <-received:
				if got != tt.wantProvider {
					t.Fatalf("routed provider = %q, want %q", got, tt.wantProvider)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for upstream request")
			}
		})
	}
}

func TestServer_OpenAICompatibleRoutesByModel(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test table fields grouped for readability.
	tests := []struct {
		name         string
		model        string
		wantProvider string
	}{
		{name: "gpt routes to openai", model: "gpt-4o-mini", wantProvider: "openai"},
		{name: "gemini routes to google", model: "gemini-2.5-flash", wantProvider: "google"},
		{name: "llama routes to ollama", model: "llama3.2", wantProvider: "ollama"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			received := make(chan string, 1)
			openAIServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received <- "openai"
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(openAIServer.Close)

			anthropicServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received <- "anthropic"
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(anthropicServer.Close)

			googleServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received <- "google"
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(googleServer.Close)

			ollamaServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received <- "ollama"
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(ollamaServer.Close)

			cfg := testConfig(openAIServer.URL, anthropicServer.URL)
			cfg.Upstream.Google.BaseURL = googleServer.URL
			cfg.Upstream.Ollama.BaseURL = ollamaServer.URL
			proxyServer, err := New(cfg, Hooks{})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			server := newTestServer(t, proxyServer)
			t.Cleanup(server.Close)

			reqBody := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, tt.model)
			req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(reqBody))
			if err != nil {
				t.Fatalf("NewRequest() error = %v", err)
			}

			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatalf("Do() error = %v", err)
			}
			t.Cleanup(func() { _ = resp.Body.Close() })

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status code = %d, want %d", resp.StatusCode, http.StatusOK)
			}

			select {
			case got := <-received:
				if got != tt.wantProvider {
					t.Fatalf("routed provider = %q, want %q", got, tt.wantProvider)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for upstream request")
			}
		})
	}
}

func TestServer_AnthropicOpenAICompatTransformsRequestAndResponse(t *testing.T) {
	t.Parallel()

	requestBodies := make(chan string, 1)
	anthropicServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll() error = %v", err)
		}
		requestBodies <- string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","model":"claude-haiku-4-5","content":[{"type":"text","text":"anthropic ok"}],"usage":{"input_tokens":18,"output_tokens":10},"stop_reason":"end_turn"}`))
	}))
	t.Cleanup(anthropicServer.Close)

	openAIServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("openai upstream should not be called for claude compatibility request")
	}))
	t.Cleanup(openAIServer.Close)

	cfg := testConfig(openAIServer.URL, anthropicServer.URL)
	proxyServer, err := New(cfg, Hooks{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	server := newTestServer(t, proxyServer)
	t.Cleanup(server.Close)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"claude-haiku-4-5","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer anthropic-key")

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if !strings.Contains(string(body), `"object":"chat.completion"`) {
		t.Fatalf("response body = %q, want OpenAI-compatible shape", string(body))
	}
	if !strings.Contains(string(body), `"content":"anthropic ok"`) {
		t.Fatalf("response body = %q, want translated content", string(body))
	}

	gotRequest := <-requestBodies
	if !strings.Contains(gotRequest, `"model":"claude-haiku-4-5"`) || !strings.Contains(gotRequest, `"max_tokens":1024`) {
		t.Fatalf("anthropic request = %s, want translated anthropic body", gotRequest)
	}
}

func TestServer_UnknownOpenAICompatibleModelReturnsProviderResolutionError(t *testing.T) {
	t.Parallel()

	openAIServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream should not be called when provider resolution fails")
	}))
	t.Cleanup(openAIServer.Close)

	anthropicServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream should not be called when provider resolution fails")
	}))
	t.Cleanup(anthropicServer.Close)

	cfg := testConfig(openAIServer.URL, anthropicServer.URL)
	proxyServer, err := New(cfg, Hooks{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	server := newTestServer(t, proxyServer)
	t.Cleanup(server.Close)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"mystery-model","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if !strings.Contains(string(body), "provider_resolution_failed") {
		t.Fatalf("body = %q, want provider resolution error", string(body))
	}
}

func TestServer_OllamaNativeAPIRoutesToOllama(t *testing.T) {
	t.Parallel()

	received := make(chan string, 1)
	ollamaServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ollamaServer.Close)

	openAIServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("openai upstream should not be called for native ollama requests")
	}))
	t.Cleanup(openAIServer.Close)

	anthropicServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("anthropic upstream should not be called for native ollama requests")
	}))
	t.Cleanup(anthropicServer.Close)

	cfg := testConfig(openAIServer.URL, anthropicServer.URL)
	cfg.Upstream.Ollama.BaseURL = ollamaServer.URL
	proxyServer, err := New(cfg, Hooks{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	server := newTestServer(t, proxyServer)
	t.Cleanup(server.Close)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/chat", strings.NewReader(`{"model":"llama3.2","messages":[{"role":"user","content":"hello"}],"stream":false}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := <-received; got != "/api/chat" {
		t.Fatalf("routed path = %q, want %q", got, "/api/chat")
	}
}

func TestServer_NonStreamingPassthroughAndHeaderStripping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		requestHeaders     map[string]string
		name               string
		requestBody        string
		requestPath        string
		wantResponseHeader string
		wantBody           string
		wantStatusCode     int
	}{
		{
			name:        "passthrough body status and non-oberwatch headers",
			requestPath: "/v1/chat/completions",
			requestBody: `{"model":"gpt-4o","stream":false}`,
			requestHeaders: map[string]string{
				"Authorization":         "Bearer test-key",
				"X-Custom-Header":       "custom-value",
				"X-Oberwatch-Agent":     "email-agent",
				"X-OBERWATCH-Trace-ID":  "trace-123",
				"X-Oberwatch-Parent-ID": "parent-456",
			},
			wantStatusCode:     http.StatusCreated,
			wantBody:           `{"id":"abc123","object":"chat.completion"}`,
			wantResponseHeader: "present",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			upstreamReq := make(chan capturedRequest, 1)

			openAIServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("ReadAll() error = %v", err)
				}

				upstreamReq <- capturedRequest{
					method: r.Method,
					path:   r.URL.Path,
					body:   string(body),
					header: r.Header.Clone(),
				}

				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Upstream-Header", tt.wantResponseHeader)
				w.WriteHeader(tt.wantStatusCode)
				if _, err := w.Write([]byte(tt.wantBody)); err != nil {
					t.Fatalf("Write() error = %v", err)
				}
			}))
			t.Cleanup(openAIServer.Close)

			anthropicServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(anthropicServer.Close)

			cfg := testConfig(openAIServer.URL, anthropicServer.URL)
			proxyServer, err := New(cfg, Hooks{})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			server := newTestServer(t, proxyServer)
			t.Cleanup(server.Close)

			req, err := http.NewRequest(http.MethodPost, server.URL+tt.requestPath, strings.NewReader(tt.requestBody))
			if err != nil {
				t.Fatalf("NewRequest() error = %v", err)
			}
			for key, value := range tt.requestHeaders {
				req.Header.Set(key, value)
			}

			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatalf("Do() error = %v", err)
			}
			t.Cleanup(func() {
				_ = resp.Body.Close()
			})

			respBody, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}

			if resp.StatusCode != tt.wantStatusCode {
				t.Fatalf("status code = %d, want %d", resp.StatusCode, tt.wantStatusCode)
			}
			if string(respBody) != tt.wantBody {
				t.Fatalf("body = %q, want %q", string(respBody), tt.wantBody)
			}
			if got := resp.Header.Get("X-Upstream-Header"); got != tt.wantResponseHeader {
				t.Fatalf("response header X-Upstream-Header = %q, want %q", got, tt.wantResponseHeader)
			}

			select {
			case got := <-upstreamReq:
				if got.method != http.MethodPost {
					t.Fatalf("upstream method = %q, want %q", got.method, http.MethodPost)
				}
				if got.path != tt.requestPath {
					t.Fatalf("upstream path = %q, want %q", got.path, tt.requestPath)
				}
				if got.body != tt.requestBody {
					t.Fatalf("upstream body = %q, want %q", got.body, tt.requestBody)
				}
				if got.header.Get("Authorization") != "Bearer test-key" {
					t.Fatalf("upstream Authorization = %q, want %q", got.header.Get("Authorization"), "Bearer test-key")
				}
				if got.header.Get("X-Custom-Header") != "custom-value" {
					t.Fatalf("upstream X-Custom-Header = %q, want %q", got.header.Get("X-Custom-Header"), "custom-value")
				}

				for key := range got.header {
					if strings.HasPrefix(strings.ToLower(key), "x-oberwatch-") {
						t.Fatalf("upstream header %q should have been stripped", key)
					}
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for captured upstream request")
			}
		})
	}
}

func TestServer_SSEStreamingPassthrough(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		requestBody string
		firstChunk  string
		secondChunk string
	}{
		{
			name:        "chat completions stream chunks are forwarded",
			requestBody: `{"model":"gpt-4o","stream":true}`,
			firstChunk:  "data: first\n\n",
			secondChunk: "data: second\n\n",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			firstChunkWritten := make(chan struct{})
			releaseSecondChunk := make(chan struct{})

			openAIServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				flusher, ok := w.(http.Flusher)
				if !ok {
					t.Fatal("response writer does not implement Flusher")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				if _, err := w.Write([]byte(tt.firstChunk)); err != nil {
					t.Fatalf("Write(firstChunk) error = %v", err)
				}
				flusher.Flush()
				close(firstChunkWritten)

				<-releaseSecondChunk
				if _, err := w.Write([]byte(tt.secondChunk)); err != nil {
					t.Fatalf("Write(secondChunk) error = %v", err)
				}
				flusher.Flush()
			}))
			t.Cleanup(openAIServer.Close)

			anthropicServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(anthropicServer.Close)

			cfg := testConfig(openAIServer.URL, anthropicServer.URL)
			proxyServer, err := New(cfg, Hooks{})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			server := newTestServer(t, proxyServer)
			t.Cleanup(server.Close)

			req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(tt.requestBody))
			if err != nil {
				t.Fatalf("NewRequest() error = %v", err)
			}

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
			if got := resp.Header.Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
				t.Fatalf("Content-Type = %q, want %q", got, "text/event-stream")
			}

			select {
			case <-firstChunkWritten:
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for first upstream chunk")
			}

			firstRead := make(chan string, 1)
			readErr := make(chan error, 1)
			go func() {
				buffer := make([]byte, len(tt.firstChunk))
				_, readFullErr := io.ReadFull(resp.Body, buffer)
				if readFullErr != nil {
					readErr <- readFullErr
					return
				}
				firstRead <- string(buffer)
			}()

			select {
			case got := <-firstRead:
				if got != tt.firstChunk {
					t.Fatalf("first streamed chunk = %q, want %q", got, tt.firstChunk)
				}
			case streamErr := <-readErr:
				t.Fatalf("stream read error = %v", streamErr)
			case <-time.After(2 * time.Second):
				close(releaseSecondChunk)
				t.Fatal("timed out waiting for first streamed chunk through proxy")
			}

			close(releaseSecondChunk)
			remaining, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}
			if string(remaining) != tt.secondChunk {
				t.Fatalf("remaining stream = %q, want %q", string(remaining), tt.secondChunk)
			}
		})
	}
}

func TestServer_HealthAndMiddlewareChain(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep table fields readable for test intent.
	tests := []struct {
		wantHookOrder []string
		name          string
		path          string
	}{
		{
			name:          "health request runs gate then trace and does not proxy upstream",
			path:          healthPath,
			wantHookOrder: []string{"gate", "trace"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var openAIHits int32
			var anthropicHits int32

			openAIServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&openAIHits, 1)
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(openAIServer.Close)

			anthropicServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&anthropicHits, 1)
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(anthropicServer.Close)

			cfg := testConfig(openAIServer.URL, anthropicServer.URL)

			var orderMu sync.Mutex
			order := make([]string, 0, 2)
			hooks := Hooks{
				Gate: func(r *http.Request) {
					orderMu.Lock()
					order = append(order, "gate")
					orderMu.Unlock()
				},
				Trace: func(r *http.Request) {
					orderMu.Lock()
					order = append(order, "trace")
					orderMu.Unlock()
				},
			}

			proxyServer, err := New(cfg, hooks)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			server := newTestServer(t, proxyServer)
			t.Cleanup(server.Close)

			resp, err := server.Client().Get(server.URL + tt.path)
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			t.Cleanup(func() {
				_ = resp.Body.Close()
			})

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status code = %d, want %d", resp.StatusCode, http.StatusOK)
			}

			payload, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}

			var decoded map[string]string
			if err := json.Unmarshal(payload, &decoded); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			if decoded["status"] != "ok" {
				t.Fatalf("health status = %q, want %q", decoded["status"], "ok")
			}

			orderMu.Lock()
			gotOrder := append([]string(nil), order...)
			orderMu.Unlock()
			if len(gotOrder) != len(tt.wantHookOrder) {
				t.Fatalf("hook calls = %v, want %v", gotOrder, tt.wantHookOrder)
			}
			for i, want := range tt.wantHookOrder {
				if gotOrder[i] != want {
					t.Fatalf("hook order = %v, want %v", gotOrder, tt.wantHookOrder)
				}
			}

			totalHits := atomic.LoadInt32(&openAIHits) + atomic.LoadInt32(&anthropicHits)
			if totalHits != 0 {
				t.Fatalf("upstream hits = %d, want %d", totalHits, 0)
			}
		})
	}
}

func TestNew_ReturnsErrorsForInvalidUpstreamConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		mutate     func(*config.Config)
		wantErrSub string
	}{
		{
			name: "invalid openai base url",
			mutate: func(cfg *config.Config) {
				cfg.Upstream.OpenAI.BaseURL = "://bad-openai-url"
			},
			wantErrSub: "parse upstream \"openai\"",
		},
		{
			name: "anthropic base url missing scheme",
			mutate: func(cfg *config.Config) {
				cfg.Upstream.Anthropic.BaseURL = "api.anthropic.com"
			},
			wantErrSub: "must include scheme and host",
		},
		{
			name: "default custom provider without custom target",
			mutate: func(cfg *config.Config) {
				cfg.Upstream.DefaultProvider = config.ProviderCustom
				cfg.Upstream.Custom.BaseURL = ""
			},
			wantErrSub: "default upstream provider",
		},
		{
			name: "missing required openai target",
			mutate: func(cfg *config.Config) {
				cfg.Upstream.OpenAI.BaseURL = ""
			},
			wantErrSub: "upstream \"openai\" base URL must be configured",
		},
		{
			name: "missing required anthropic target",
			mutate: func(cfg *config.Config) {
				cfg.Upstream.Anthropic.BaseURL = ""
			},
			wantErrSub: "upstream \"anthropic\" base URL must be configured",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := config.DefaultConfig()
			tt.mutate(&cfg)

			_, err := New(cfg, Hooks{})
			if err == nil {
				t.Fatal("New() error = nil, want non-nil")
			}
			if !strings.Contains(err.Error(), tt.wantErrSub) {
				t.Fatalf("New() error = %q, want substring %q", err.Error(), tt.wantErrSub)
			}
		})
	}
}

func TestServer_ProxyErrorReturnsBadGateway(t *testing.T) {
	t.Parallel()

	tests := []struct {
		wantBody   string
		name       string
		path       string
		upstream   string
		wantStatus int
	}{
		{
			name:       "connection error is translated to bad gateway",
			upstream:   "http://127.0.0.1:1",
			path:       "/v1/chat/completions",
			wantStatus: http.StatusBadGateway,
			wantBody:   "upstream proxy error",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := config.DefaultConfig()
			cfg.Upstream.OpenAI.BaseURL = tt.upstream
			cfg.Upstream.Anthropic.BaseURL = tt.upstream
			cfg.Upstream.DefaultProvider = config.ProviderOpenAI

			proxyServer, err := New(cfg, Hooks{})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			server := newTestServer(t, proxyServer)
			t.Cleanup(server.Close)

			req, err := http.NewRequest(http.MethodPost, server.URL+tt.path, bytes.NewBufferString(`{"model":"gpt-4o-mini","stream":false}`))
			if err != nil {
				t.Fatalf("NewRequest() error = %v", err)
			}

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

			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status code = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if !strings.Contains(string(body), tt.wantBody) {
				t.Fatalf("body = %q, want substring %q", string(body), tt.wantBody)
			}
		})
	}
}

func testConfig(openAIURL, anthropicURL string) config.Config {
	cfg := config.DefaultConfig()
	cfg.Upstream.OpenAI.BaseURL = openAIURL
	cfg.Upstream.Anthropic.BaseURL = anthropicURL
	cfg.Upstream.DefaultProvider = config.ProviderOpenAI
	return cfg
}

func newTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()

	var server *httptest.Server
	func() {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}

			message := fmt.Sprint(recovered)
			if strings.Contains(message, "httptest: failed to listen on a port") {
				t.Skipf("skipping integration test in restricted environment: %s", message)
			}

			panic(recovered)
		}()
		server = httptest.NewServer(handler)
	}()

	return server
}

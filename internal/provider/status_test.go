package provider

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestChecker_CheckOpenAI_MapsIndicatorToStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		indicator  string
		wantStatus Status
		statusCode int
	}{
		{name: "operational", indicator: "none", statusCode: http.StatusOK, wantStatus: StatusOperational},
		{name: "degraded", indicator: "minor", statusCode: http.StatusOK, wantStatus: StatusDegraded},
		{name: "outage major", indicator: "major", statusCode: http.StatusOK, wantStatus: StatusOutage},
		{name: "outage critical", indicator: "critical", statusCode: http.StatusOK, wantStatus: StatusOutage},
		{name: "unknown indicator", indicator: "surprise", statusCode: http.StatusOK, wantStatus: StatusUnavailable},
		{name: "server error", indicator: "none", statusCode: http.StatusInternalServerError, wantStatus: StatusUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if auth := r.Header.Get("Authorization"); auth != "" {
					t.Errorf("request must not carry credentials, got Authorization=%q", auth)
				}
				if key := r.Header.Get("X-Api-Key"); key != "" {
					t.Errorf("request must not carry credentials, got X-Api-Key=%q", key)
				}
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(`{"status":{"indicator":"` + tt.indicator + `","description":"test"}}`))
			}))
			defer server.Close()

			checker := &Checker{HTTPClient: server.Client(), OpenAIStatusURL: server.URL}
			row := checker.CheckOpenAI(context.Background())

			if row.Status != tt.wantStatus {
				t.Fatalf("Status = %q, want %q", row.Status, tt.wantStatus)
			}
			if row.Provider != "openai" {
				t.Fatalf("Provider = %q, want %q", row.Provider, "openai")
			}
			if !row.Public {
				t.Fatal("Public = false, want true for a public status page")
			}
		})
	}
}

func TestChecker_CheckAnthropic_UsesAnthropicURL(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":{"indicator":"none"}}`))
	}))
	defer server.Close()

	checker := &Checker{HTTPClient: server.Client(), AnthropicStatusURL: server.URL}
	row := checker.CheckAnthropic(context.Background())

	if row.Status != StatusOperational {
		t.Fatalf("Status = %q, want %q", row.Status, StatusOperational)
	}
	if row.Provider != "anthropic" {
		t.Fatalf("Provider = %q, want %q", row.Provider, "anthropic")
	}
}

func TestChecker_CheckOpenAI_UnreachableIsStatusUnavailable(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	unreachableURL := server.URL
	server.Close()

	checker := &Checker{HTTPClient: http.DefaultClient, OpenAIStatusURL: unreachableURL}
	row := checker.CheckOpenAI(context.Background())

	if row.Status != StatusUnavailable {
		t.Fatalf("Status = %q, want %q", row.Status, StatusUnavailable)
	}
}

func TestChecker_CheckOpenAI_SlowServerTimesOutWithinProbeTimeout(t *testing.T) {
	t.Parallel()

	block := make(chan struct{})
	defer close(block)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()

	checker := &Checker{HTTPClient: server.Client(), OpenAIStatusURL: server.URL}

	start := time.Now()
	row := checker.CheckOpenAI(context.Background())
	elapsed := time.Since(start)

	if row.Status != StatusUnavailable {
		t.Fatalf("Status = %q, want %q", row.Status, StatusUnavailable)
	}
	if elapsed > ProbeTimeout+2*time.Second {
		t.Fatalf("probe took %s, want bounded near ProbeTimeout=%s", elapsed, ProbeTimeout)
	}
}

func TestChecker_CheckOllama_OmittedWhenNoBaseURL(t *testing.T) {
	t.Parallel()

	checker := NewChecker()
	row, ok := checker.CheckOllama(context.Background(), "")

	if ok {
		t.Fatalf("ok = true, want false for empty base URL, got row %+v", row)
	}
}

// TestChecker_CheckOllama_ConfiguredButNotAnsweringKeepsRow is the regression
// test for Issue #85. Before the fix a configured Ollama that failed its probe
// was dropped from the result entirely (ok = false), so its card vanished from
// the dashboard until a restart. A configured server must always yield a row;
// only its status changes.
func TestChecker_CheckOllama_ConfiguredButNotAnsweringKeepsRow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		serve      func(t *testing.T) string
		wantStatus Status
	}{
		{
			name: "connection refused",
			serve: func(t *testing.T) string {
				t.Helper()
				server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
				closedURL := server.URL
				server.Close()
				return closedURL
			},
			wantStatus: StatusUnreachable,
		},
		{
			name: "non-200 response",
			serve: func(t *testing.T) string {
				t.Helper()
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusServiceUnavailable)
				}))
				t.Cleanup(server.Close)
				return server.URL
			},
			wantStatus: StatusUnreachable,
		},
		{
			name: "answers 200",
			serve: func(t *testing.T) string {
				t.Helper()
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"models":[]}`))
				}))
				t.Cleanup(server.Close)
				return server.URL
			},
			wantStatus: StatusOperational,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			baseURL := tt.serve(t)
			checker := &Checker{HTTPClient: http.DefaultClient}
			row, ok := checker.CheckOllama(context.Background(), baseURL)

			if !ok {
				t.Fatalf("ok = false for configured loopback %q, want true: a configured server must keep its row", baseURL)
			}
			if row.Status != tt.wantStatus {
				t.Fatalf("Status = %q, want %q", row.Status, tt.wantStatus)
			}
			if row.Provider != "ollama" || row.Label != OllamaLabel {
				t.Fatalf("row identity = %q/%q, want ollama/%q", row.Provider, row.Label, OllamaLabel)
			}
			if row.Public {
				t.Fatal("Public = true, want false for a local server")
			}
			if row.Detail == "" {
				t.Fatal("Detail is empty; the row must say what was actually checked")
			}
		})
	}
}

func TestChecker_CheckOllama_RecoversAfterServerStarts(t *testing.T) {
	t.Parallel()

	// A server that is first down, then up, then down again at the same address.
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	listener := server.Listener
	baseURL := "http://" + listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close reserved listener: %v", err)
	}

	checker := &Checker{HTTPClient: http.DefaultClient}

	row, ok := checker.CheckOllama(context.Background(), baseURL)
	if !ok || row.Status != StatusUnreachable {
		t.Fatalf("before start: (status %q, ok %v), want (%q, true)", row.Status, ok, StatusUnreachable)
	}

	listener, err := net.Listen("tcp", listener.Addr().String())
	if err != nil {
		t.Skipf("could not rebind %s: %v", baseURL, err)
	}
	server.Listener = listener
	server.Start()

	row, ok = checker.CheckOllama(context.Background(), baseURL)
	if !ok || row.Status != StatusOperational {
		t.Fatalf("after start: (status %q, ok %v), want (%q, true)", row.Status, ok, StatusOperational)
	}

	server.Close()

	row, ok = checker.CheckOllama(context.Background(), baseURL)
	if !ok || row.Status != StatusUnreachable {
		t.Fatalf("after stop: (status %q, ok %v), want (%q, true)", row.Status, ok, StatusUnreachable)
	}
}

func TestOllamaConfigured_MatchesCheckOllamaRowPresence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
		want    bool
	}{
		{name: "empty", baseURL: "", want: false},
		{name: "whitespace", baseURL: "   ", want: false},
		{name: "localhost", baseURL: "http://localhost:11434", want: true},
		{name: "ipv4 loopback", baseURL: "http://127.0.0.1:11434", want: true},
		{name: "ipv6 loopback", baseURL: "http://[::1]:11434", want: true},
		{name: "private network", baseURL: "http://10.0.0.5:11434", want: false},
		{name: "cloud metadata", baseURL: "http://169.254.169.254", want: false},
		{name: "remote hostname", baseURL: "https://ollama.example.com", want: false},
		{name: "credentials", baseURL: "http://user:pw@localhost:11434", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := OllamaConfigured(tt.baseURL); got != tt.want {
				t.Fatalf("OllamaConfigured(%q) = %v, want %v", tt.baseURL, got, tt.want)
			}

			// The helper must agree with the checker, which never sends a request
			// for a rejected URL and always returns a row for an accepted one.
			checker := &Checker{ollamaTransport: &recordingTransport{}}
			if _, ok := checker.CheckOllama(context.Background(), tt.baseURL); ok != tt.want {
				t.Fatalf("CheckOllama(%q) ok = %v, want %v to match OllamaConfigured", tt.baseURL, ok, tt.want)
			}
		})
	}
}

func TestChecker_CheckOllama_IncludedWhenTagsEndpointAnswers(t *testing.T) {
	t.Parallel()

	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("request must not carry credentials, got Authorization=%q", auth)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer server.Close()

	checker := &Checker{HTTPClient: server.Client()}
	row, ok := checker.CheckOllama(context.Background(), server.URL)

	if !ok {
		t.Fatal("ok = false, want true when /api/tags answers 200")
	}
	if gotPath != "/api/tags" {
		t.Fatalf("path = %q, want %q", gotPath, "/api/tags")
	}
	if row.Status != StatusOperational {
		t.Fatalf("Status = %q, want %q", row.Status, StatusOperational)
	}
	if row.Public {
		t.Fatal("Public = true, want false for a local server")
	}
}

func TestChecker_CheckOllama_TrimsTrailingSlash(t *testing.T) {
	t.Parallel()

	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	checker := &Checker{HTTPClient: server.Client()}
	if _, ok := checker.CheckOllama(context.Background(), server.URL+"/"); !ok {
		t.Fatal("ok = false, want true")
	}
	if gotPath != "/api/tags" {
		t.Fatalf("path = %q, want %q", gotPath, "/api/tags")
	}
}

func TestNewChecker_DefaultsToRealPublicStatusURLs(t *testing.T) {
	t.Parallel()

	checker := NewChecker()

	if checker.OpenAIStatusURL != defaultOpenAIStatusURL {
		t.Fatalf("OpenAIStatusURL = %q, want %q", checker.OpenAIStatusURL, defaultOpenAIStatusURL)
	}
	if checker.AnthropicStatusURL != defaultAnthropicStatusURL {
		t.Fatalf("AnthropicStatusURL = %q, want %q", checker.AnthropicStatusURL, defaultAnthropicStatusURL)
	}
	if checker.HTTPClient == nil || checker.HTTPClient.Timeout != ProbeTimeout {
		t.Fatalf("HTTPClient timeout = %v, want %v", checker.HTTPClient.Timeout, ProbeTimeout)
	}
}

package provider

import (
	"context"
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
		statusCode int
		wantStatus Status
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

func TestChecker_CheckOllama_OmittedWhenUnreachable(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	unreachableURL := server.URL
	server.Close()

	checker := &Checker{HTTPClient: http.DefaultClient}
	row, ok := checker.CheckOllama(context.Background(), unreachableURL)

	if ok {
		t.Fatalf("ok = true, want false for unreachable server, got row %+v", row)
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

func TestChecker_CheckOllama_OmittedOnNon200(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	checker := &Checker{HTTPClient: server.Client()}
	row, ok := checker.CheckOllama(context.Background(), server.URL)

	if ok {
		t.Fatalf("ok = true, want false for a non-200 response, got row %+v", row)
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

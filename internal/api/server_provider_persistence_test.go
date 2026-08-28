package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/OberWatch/oberwatch/internal/config"
	"github.com/OberWatch/oberwatch/internal/provider"
)

// These tests cover Issue #85: provider cards must be a fixed set decided by
// configuration, and must stay on screen through probe failures and recover
// while a session is open. Only a card's status may change between refreshes;
// never its presence.

func TestInitialProviderRows_OllamaOnlyWhenLoopbackConfigured(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		ollamaBaseURL string
		wantProviders []string
	}{
		{name: "no ollama configured", ollamaBaseURL: "", wantProviders: []string{"openai", "anthropic"}},
		{name: "whitespace only", ollamaBaseURL: "  ", wantProviders: []string{"openai", "anthropic"}},
		{name: "loopback localhost", ollamaBaseURL: "http://localhost:11434", wantProviders: []string{"openai", "anthropic", "ollama"}},
		{name: "loopback ipv4", ollamaBaseURL: "http://127.0.0.1:11434", wantProviders: []string{"openai", "anthropic", "ollama"}},
		{name: "loopback ipv6", ollamaBaseURL: "http://[::1]:11434", wantProviders: []string{"openai", "anthropic", "ollama"}},
		{name: "non-loopback is never a card", ollamaBaseURL: "http://10.0.0.5:11434", wantProviders: []string{"openai", "anthropic"}},
		{name: "cloud metadata is never a card", ollamaBaseURL: "http://169.254.169.254", wantProviders: []string{"openai", "anthropic"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rows := initialProviderRows(tt.ollamaBaseURL)
			assertProviderOrder(t, providerNames(rows), tt.wantProviders)

			for _, row := range rows {
				if row.Status == provider.StatusOperational {
					t.Fatalf("pending row %q claims operational before any probe ran", row.Provider)
				}
				if row.ObservedAt != nil {
					t.Fatalf("pending row %q carries observed_at before any probe ran", row.Provider)
				}
				if row.Provider == "ollama" && row.Public {
					t.Fatal("pending ollama row claims to come from a public feed")
				}
			}
		})
	}
}

func TestNew_InitialRowsFollowOllamaConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		ollamaBaseURL string
		wantProviders []string
	}{
		{name: "default config has a loopback ollama and shows its card at once", ollamaBaseURL: config.DefaultConfig().Upstream.Ollama.BaseURL, wantProviders: []string{"openai", "anthropic", "ollama"}},
		{name: "empty base URL never shows an ollama card", ollamaBaseURL: "", wantProviders: []string{"openai", "anthropic"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := config.DefaultConfig()
			cfg.Upstream.Ollama.BaseURL = tt.ollamaBaseURL

			server := New(cfg, nil, nil, "0.1.0")
			assertProviderOrder(t, providerNames(server.providerStatusSnapshot()), tt.wantProviders)
		})
	}
}

// TestServer_RefreshProviderStatus_CardSetNeverChangesAcrossTransitions is the
// API-level regression test for Issue #85. Before the fix a failed Ollama
// probe removed the row from the snapshot, so the card vanished until a
// restart. Every step of every sequence below must serve the same cards, with
// only their statuses and observed_at moving.
func TestServer_RefreshProviderStatus_CardSetNeverChangesAcrossTransitions(t *testing.T) {
	t.Parallel()

	type step struct {
		name      string
		openai    provider.Status
		anthropic provider.Status
		ollama    provider.Status
	}

	tests := []struct {
		name          string
		ollamaBaseURL string
		steps         []step
		wantProviders []string
	}{
		{
			name:          "ollama down, started, stopped again",
			ollamaBaseURL: "http://localhost:11434",
			wantProviders: []string{"openai", "anthropic", "ollama"},
			steps: []step{
				{name: "ollama not running", openai: provider.StatusOperational, anthropic: provider.StatusOperational, ollama: provider.StatusUnreachable},
				{name: "ollama started", openai: provider.StatusOperational, anthropic: provider.StatusOperational, ollama: provider.StatusOperational},
				{name: "ollama stopped", openai: provider.StatusOperational, anthropic: provider.StatusOperational, ollama: provider.StatusUnreachable},
			},
		},
		{
			name:          "public feeds fail and recover",
			ollamaBaseURL: "http://127.0.0.1:11434",
			wantProviders: []string{"openai", "anthropic", "ollama"},
			steps: []step{
				{name: "all feeds unreadable", openai: provider.StatusUnavailable, anthropic: provider.StatusUnavailable, ollama: provider.StatusUnreachable},
				{name: "openai back, anthropic degraded", openai: provider.StatusOperational, anthropic: provider.StatusDegraded, ollama: provider.StatusUnreachable},
				{name: "anthropic outage", openai: provider.StatusOperational, anthropic: provider.StatusOutage, ollama: provider.StatusOperational},
				{name: "everything healthy", openai: provider.StatusOperational, anthropic: provider.StatusOperational, ollama: provider.StatusOperational},
			},
		},
		{
			name:          "no ollama configured never grows an ollama card",
			ollamaBaseURL: "",
			wantProviders: []string{"openai", "anthropic"},
			steps: []step{
				{name: "feeds unreadable", openai: provider.StatusUnavailable, anthropic: provider.StatusUnavailable},
				{name: "feeds healthy", openai: provider.StatusOperational, anthropic: provider.StatusOperational},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			clock := newFakeClock()
			server := newFreshnessTestServer(nil, clock)
			server.ollamaBaseURL = tt.ollamaBaseURL
			server.providerRows = initialProviderRows(tt.ollamaBaseURL)

			// Before any probe: the same card set, all pending.
			assertProviderOrder(t, providerNames(server.providerStatusSnapshot()), tt.wantProviders)

			var lastObserved time.Time
			for _, st := range tt.steps {
				server.providerChecker = fakeProviderChecker{
					openai:    provider.StatusRow{Provider: "openai", Label: "OpenAI", Status: st.openai, Public: true},
					anthropic: provider.StatusRow{Provider: "anthropic", Label: "Anthropic", Status: st.anthropic, Public: true},
					ollama:    provider.StatusRow{Provider: "ollama", Label: provider.OllamaLabel, Status: st.ollama, Public: false},
					ollamaOK:  provider.OllamaConfigured(tt.ollamaBaseURL),
				}

				clock.advance(providerStatusTTL + time.Second)
				if !server.refreshProviderStatus(context.Background()) {
					t.Fatalf("step %q: refreshProviderStatus() = false, want true", st.name)
				}

				rows := server.providerStatusSnapshot()
				assertProviderOrder(t, providerNames(rows), tt.wantProviders)

				want := map[string]provider.Status{"openai": st.openai, "anthropic": st.anthropic, "ollama": st.ollama}
				for _, row := range rows {
					if row.Status != want[row.Provider] {
						t.Fatalf("step %q: %s status = %q, want %q", st.name, row.Provider, row.Status, want[row.Provider])
					}
					if row.ObservedAt == nil || !row.ObservedAt.After(lastObserved) {
						t.Fatalf("step %q: %s observed_at = %v, want it to advance past %s", st.name, row.Provider, row.ObservedAt, lastObserved)
					}
				}
				lastObserved = server.providerStatusObservedAt()
			}
		})
	}
}

// TestServer_HandleHealth_OllamaCardSurvivesOutageAndRecoversInSession drives
// the real provider.Checker through the health endpoint: public feeds served by
// httptest, and an Ollama address on loopback that is first closed, then
// serving, then closed again. This is the exact sequence the issue reports:
// Ollama started after the dashboard was already open.
func TestServer_HandleHealth_OllamaCardSurvivesOutageAndRecoversInSession(t *testing.T) {
	t.Parallel()

	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":{"indicator":"none"}}`))
	}))
	t.Cleanup(feed.Close)

	// Reserve a loopback port for Ollama and close it so the first probe finds
	// nothing listening.
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	ollamaAddr := reserved.Addr().String()
	if closeErr := reserved.Close(); closeErr != nil {
		t.Fatalf("release reserved port: %v", closeErr)
	}
	ollamaBaseURL := "http://" + ollamaAddr

	checker := provider.NewChecker()
	checker.HTTPClient = feed.Client()
	checker.OpenAIStatusURL = feed.URL
	checker.AnthropicStatusURL = feed.URL

	clock := newFakeClock()
	server := newFreshnessTestServer(checker, clock)
	server.ollamaBaseURL = ollamaBaseURL
	server.providerRows = initialProviderRows(ollamaBaseURL)
	server.registerRoutes()

	// Page load: pending rows, all three cards present, nothing claimed.
	rows := serveHealthProviders(t, server)
	assertProviderOrder(t, healthProviderNames(rows), []string{"openai", "anthropic", "ollama"})
	if got := rows[2].Status; got != provider.StatusUnreachable {
		t.Fatalf("pending ollama status = %q, want %q", got, provider.StatusUnreachable)
	}

	// The first request kicked off a refresh; wait for it and read again.
	waitForProviderObservation(t, server, time.Time{})
	rows = serveHealthProviders(t, server)
	assertProviderOrder(t, healthProviderNames(rows), []string{"openai", "anthropic", "ollama"})
	if got := rows[2].Status; got != provider.StatusUnreachable {
		t.Fatalf("ollama status with nothing listening = %q, want %q", got, provider.StatusUnreachable)
	}
	if rows[2].ObservedAt == "" {
		t.Fatal("unreachable ollama row has no observed_at; a failed check is still a check")
	}
	for _, row := range rows[:2] {
		if row.Status != provider.StatusOperational {
			t.Fatalf("%s status = %q, want %q", row.Provider, row.Status, provider.StatusOperational)
		}
	}

	// Operator starts Ollama on the configured address. No restart, no login.
	listener, err := net.Listen("tcp", ollamaAddr)
	if err != nil {
		t.Skipf("could not rebind %s: %v", ollamaAddr, err)
	}
	ollama := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("ollama probe path = %q, want /api/tags", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("ollama probe carried credentials")
		}
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	ollama.Listener = listener
	ollama.Start()

	// Inside the TTL the snapshot is served as-is; past it the next request
	// refreshes and the card recovers.
	first := server.providerStatusObservedAt()
	clock.advance(providerStatusTTL + time.Second)
	_ = serveHealthProviders(t, server)
	waitForProviderObservation(t, server, first)

	rows = serveHealthProviders(t, server)
	assertProviderOrder(t, healthProviderNames(rows), []string{"openai", "anthropic", "ollama"})
	if got := rows[2].Status; got != provider.StatusOperational {
		t.Fatalf("ollama status after start = %q, want %q", got, provider.StatusOperational)
	}

	// Operator stops Ollama again: the card stays, status drops.
	ollama.Close()
	second := server.providerStatusObservedAt()
	clock.advance(providerStatusTTL + time.Second)
	_ = serveHealthProviders(t, server)
	waitForProviderObservation(t, server, second)

	rows = serveHealthProviders(t, server)
	assertProviderOrder(t, healthProviderNames(rows), []string{"openai", "anthropic", "ollama"})
	if got := rows[2].Status; got != provider.StatusUnreachable {
		t.Fatalf("ollama status after stop = %q, want %q", got, provider.StatusUnreachable)
	}
}

func TestServer_HandleHealth_PublicCardsPersistWhenFeedsUnreadable(t *testing.T) {
	t.Parallel()

	// A feed that answers with garbage, and one whose port is closed.
	garbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	t.Cleanup(garbage.Close)
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closed.URL
	closed.Close()

	checker := provider.NewChecker()
	checker.HTTPClient = garbage.Client()
	checker.OpenAIStatusURL = garbage.URL
	checker.AnthropicStatusURL = closedURL

	clock := newFakeClock()
	server := newFreshnessTestServer(checker, clock)
	server.ollamaBaseURL = ""
	server.providerRows = initialProviderRows("")
	server.registerRoutes()

	_ = serveHealthProviders(t, server)
	waitForProviderObservation(t, server, time.Time{})

	rows := serveHealthProviders(t, server)
	assertProviderOrder(t, healthProviderNames(rows), []string{"openai", "anthropic"})
	for _, row := range rows {
		if row.Status != provider.StatusUnavailable {
			t.Fatalf("%s status = %q, want %q when its feed cannot be read", row.Provider, row.Status, provider.StatusUnavailable)
		}
		if !row.Public {
			t.Fatalf("%s public = false, want true", row.Provider)
		}
		if row.ObservedAt == "" {
			t.Fatalf("%s has no observed_at", row.Provider)
		}
	}
}

// healthProviderRow is the wire shape of one provider row in the health payload.
type healthProviderRow struct {
	Provider   string          `json:"provider"`
	Label      string          `json:"label"`
	Status     provider.Status `json:"status"`
	Detail     string          `json:"detail"`
	ObservedAt string          `json:"observed_at"`
	Public     bool            `json:"public"`
}

// serveHealthProviders performs GET /health through the full ServeHTTP path
// (health is a public endpoint, so no session is needed) and decodes the
// provider rows.
func serveHealthProviders(t *testing.T, server *Server) []healthProviderRow {
	t.Helper()

	recorder := httptest.NewRecorder()
	start := time.Now()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, basePath+"/health", nil))
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("GET /health took %s, want an immediate answer that never waits on a probe", elapsed)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /health status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var payload struct {
		Providers []healthProviderRow `json:"providers"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode health payload: %v", err)
	}
	return payload.Providers
}

// waitForProviderObservation blocks until a refresh has completed after the
// given observation time.
func waitForProviderObservation(t *testing.T, server *Server, after time.Time) {
	t.Helper()

	deadline := time.Now().Add(provider.ProbeTimeout + 3*time.Second)
	for {
		if observed := server.providerStatusObservedAt(); observed.After(after) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no provider refresh completed after %s", after)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// providerNames lists the providers of snapshot rows in order.
func providerNames(rows []provider.StatusRow) []string {
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.Provider)
	}
	return names
}

// healthProviderNames lists the providers of decoded health rows in order.
func healthProviderNames(rows []healthProviderRow) []string {
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.Provider)
	}
	return names
}

// assertProviderOrder checks the exact card set and order.
func assertProviderOrder(t *testing.T, got, want []string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("provider cards = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("provider cards = %v, want %v", got, want)
		}
	}
}

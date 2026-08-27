package api

import (
	"context"
	"time"

	"testing"

	"github.com/OberWatch/oberwatch/internal/config"
	"github.com/OberWatch/oberwatch/internal/provider"
)

// fakeProviderChecker lets tests control provider probe results without any
// real network access.
type fakeProviderChecker struct {
	openai    provider.StatusRow
	anthropic provider.StatusRow
	ollama    provider.StatusRow
	ollamaOK  bool
}

func (f fakeProviderChecker) CheckOpenAI(context.Context) provider.StatusRow    { return f.openai }
func (f fakeProviderChecker) CheckAnthropic(context.Context) provider.StatusRow { return f.anthropic }
func (f fakeProviderChecker) CheckOllama(context.Context, string) (provider.StatusRow, bool) {
	return f.ollama, f.ollamaOK
}

// newProviderStatusTestServer builds a Server directly, bypassing New's
// background probe goroutine, so tests control exactly when refreshes run.
func newProviderStatusTestServer(checker providerStatusChecker, ollamaBaseURL string) *Server {
	return &Server{
		providerChecker: checker,
		ollamaBaseURL:   ollamaBaseURL,
		providerRows: []provider.StatusRow{
			pendingProviderRow("openai", "OpenAI"),
			pendingProviderRow("anthropic", "Anthropic"),
		},
	}
}

func TestNew_ReturnsQuicklyWithoutWaitingForProviderProbes(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()

	start := time.Now()
	server := New(cfg, nil, nil, "0.1.0")
	elapsed := time.Since(start)

	if server == nil {
		t.Fatal("New() returned nil server")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("New() took %s, want it to return immediately without waiting on provider probes", elapsed)
	}
}

func TestNew_InitialProviderRowsDoNotFalselyClaimOperational(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	server := New(cfg, nil, nil, "0.1.0")

	rows := server.providerStatusSnapshot()
	if len(rows) == 0 {
		t.Fatal("providerStatusSnapshot() returned no rows before the first probe completed")
	}
	for _, row := range rows {
		if row.Status == provider.StatusOperational {
			t.Fatalf("row %+v reports operational before any probe ran; must not fabricate availability", row)
		}
	}
}

func TestServer_RefreshProviderStatus_ReplacesRowsWithProbeResults(t *testing.T) {
	t.Parallel()

	fake := fakeProviderChecker{
		openai:    provider.StatusRow{Provider: "openai", Label: "OpenAI", Status: provider.StatusOperational, Public: true},
		anthropic: provider.StatusRow{Provider: "anthropic", Label: "Anthropic", Status: provider.StatusDegraded, Public: true},
		ollamaOK:  false,
	}

	server := newProviderStatusTestServer(fake, "")
	server.refreshProviderStatus(context.Background())

	rows := server.providerStatusSnapshot()
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2 (openai, anthropic only)", len(rows))
	}

	byProvider := make(map[string]provider.StatusRow, len(rows))
	for _, row := range rows {
		byProvider[row.Provider] = row
	}

	if got := byProvider["openai"].Status; got != provider.StatusOperational {
		t.Fatalf("openai status = %q, want %q", got, provider.StatusOperational)
	}
	if got := byProvider["anthropic"].Status; got != provider.StatusDegraded {
		t.Fatalf("anthropic status = %q, want %q", got, provider.StatusDegraded)
	}
}

func TestServer_RefreshProviderStatus_IncludesOllamaWhenCheckerConfirms(t *testing.T) {
	t.Parallel()

	fake := fakeProviderChecker{
		openai:    provider.StatusRow{Provider: "openai", Status: provider.StatusOperational, Public: true},
		anthropic: provider.StatusRow{Provider: "anthropic", Status: provider.StatusOperational, Public: true},
		ollama:    provider.StatusRow{Provider: "ollama", Label: "Ollama (local)", Status: provider.StatusOperational, Public: false},
		ollamaOK:  true,
	}

	server := newProviderStatusTestServer(fake, "http://localhost:11434")
	server.refreshProviderStatus(context.Background())

	rows := server.providerStatusSnapshot()
	if len(rows) != 3 {
		t.Fatalf("len(rows) = %d, want 3 (openai, anthropic, ollama)", len(rows))
	}

	found := false
	for _, row := range rows {
		if row.Provider == "ollama" {
			found = true
		}
	}
	if !found {
		t.Fatal("ollama row missing even though the checker confirmed it")
	}
}

func TestServer_RefreshProviderStatus_OmitsOllamaWhenCheckerDenies(t *testing.T) {
	t.Parallel()

	fake := fakeProviderChecker{
		openai:    provider.StatusRow{Provider: "openai", Status: provider.StatusOperational, Public: true},
		anthropic: provider.StatusRow{Provider: "anthropic", Status: provider.StatusOperational, Public: true},
		ollamaOK:  false,
	}

	server := newProviderStatusTestServer(fake, "")
	server.refreshProviderStatus(context.Background())

	rows := server.providerStatusSnapshot()
	for _, row := range rows {
		if row.Provider == "ollama" {
			t.Fatalf("ollama row present even though the checker denied it: %+v", row)
		}
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2 when ollama is omitted", len(rows))
	}
}

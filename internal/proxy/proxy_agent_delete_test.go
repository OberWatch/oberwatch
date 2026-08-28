package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/OberWatch/oberwatch/internal/budget"
	"github.com/OberWatch/oberwatch/internal/pricing"
	"github.com/OberWatch/oberwatch/internal/storage"
)

type deleteFixture struct {
	manager *budget.BudgetManager
	store   *storage.SQLiteStore
	proxy   string
}

func newDeleteFixture(t *testing.T) deleteFixture {
	t.Helper()

	upstream := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-5.6-terra","usage":{"prompt_tokens":100,"completion_tokens":50}}`))
	}))
	t.Cleanup(upstream.Close)

	cfg := testConfig(upstream.URL, upstream.URL)
	cfg.Gate.DefaultBudget.LimitUSD = 100

	store, err := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "proxy-delete.db"), 0, nil)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	manager, err := budget.NewPersistentManager(cfg.Gate, nil, store)
	if err != nil {
		t.Fatalf("NewPersistentManager() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	proxyServer, err := New(cfg, Hooks{
		Budget:  manager,
		Pricing: pricing.NewPricingTableFromConfig(cfg.Pricing, nil),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server := newTestServer(t, proxyServer)
	t.Cleanup(server.Close)

	return deleteFixture{manager: manager, store: store, proxy: server.URL}
}

// sendAs sends one proxied request as the named agent and returns its status.
//
// The body is read to EOF before returning: the proxy does its cost accounting
// inside the read that reports EOF upstream, so draining the response is what
// makes the resulting spend visible to the caller instead of racing it.
func (f deleteFixture) sendAs(t *testing.T, agent string) int {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, f.proxy+"/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.6-terra","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("X-Oberwatch-Agent", agent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("ReadAll(response body) error = %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("Close(response body) error = %v", err)
	}
	return resp.StatusCode
}

func (f deleteFixture) persisted(t *testing.T, agent string) bool {
	t.Helper()
	_, found, err := f.store.GetAgent(context.Background(), agent)
	if err != nil {
		t.Fatalf("GetAgent(%q) error = %v", agent, err)
	}
	return found
}

func TestServer_DeletedAgentIsRediscoveredByNextRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		agent string
	}{
		{name: "proxied request recreates the agent after deletion", agent: "scratch-agent"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fixture := newDeleteFixture(t)

			if code := fixture.sendAs(t, tt.agent); code != http.StatusOK {
				t.Fatalf("first request status = %d, want 200", code)
			}
			if !fixture.persisted(t, tt.agent) {
				t.Fatal("agent not persisted after first proxied request")
			}
			if spent := fixture.manager.GetBudget(tt.agent).SpentUSD; spent <= 0 {
				t.Fatalf("spent after first request = %v, want > 0", spent)
			}

			if _, err := fixture.manager.DeleteAgent(context.Background(), tt.agent); err != nil {
				t.Fatalf("DeleteAgent() error = %v", err)
			}
			if fixture.persisted(t, tt.agent) {
				t.Fatal("agent still persisted after delete")
			}

			if code := fixture.sendAs(t, tt.agent); code != http.StatusOK {
				t.Fatalf("request after delete status = %d, want 200", code)
			}
			if !fixture.persisted(t, tt.agent) {
				t.Fatal("agent not recreated by the request after delete")
			}
			view := fixture.manager.GetBudget(tt.agent)
			if view.SpentUSD <= 0 || view.LimitUSD != 100 {
				t.Fatalf("rediscovered view = %#v, want fresh spend from one request and default limit", view)
			}
		})
	}
}

func TestServer_DeleteAgentRacesProxiedRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		requests int
		deletes  int
	}{
		{name: "deletes during live traffic never fail requests or corrupt state", requests: 40, deletes: 10},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fixture := newDeleteFixture(t)
			const agent = "racer"

			var wg sync.WaitGroup
			statuses := make(chan int, tt.requests)
			for index := 0; index < tt.requests; index++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					statuses <- fixture.sendAs(t, agent)
				}()
			}
			for index := 0; index < tt.deletes; index++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, err := fixture.manager.DeleteAgent(context.Background(), agent); err != nil && !errors.Is(err, storage.ErrAgentNotFound) {
						t.Errorf("DeleteAgent() error = %v", err)
					}
				}()
			}
			wg.Wait()
			close(statuses)

			for code := range statuses {
				if code != http.StatusOK {
					t.Fatalf("proxied request during delete returned %d, want 200", code)
				}
			}

			// Settle into both terminal states and check memory and store agree.
			if code := fixture.sendAs(t, agent); code != http.StatusOK {
				t.Fatalf("settling request status = %d, want 200", code)
			}
			if err := fixture.manager.Flush(context.Background()); err != nil {
				t.Fatalf("Flush() error = %v", err)
			}
			if !fixture.persisted(t, agent) {
				t.Fatal("agent not persisted after settling request")
			}
			if _, err := fixture.manager.DeleteAgent(context.Background(), agent); err != nil {
				t.Fatalf("final DeleteAgent() error = %v", err)
			}
			if err := fixture.manager.Flush(context.Background()); err != nil {
				t.Fatalf("Flush() error = %v", err)
			}
			if fixture.persisted(t, agent) {
				t.Fatal("agent persisted again after final delete")
			}
		})
	}
}

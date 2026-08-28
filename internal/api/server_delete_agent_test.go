package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/OberWatch/oberwatch/internal/alert"
	"github.com/OberWatch/oberwatch/internal/budget"
	"github.com/OberWatch/oberwatch/internal/config"
	"github.com/OberWatch/oberwatch/internal/storage"
)

// seedDiscoveredAgent makes the manager discover an agent the way a proxied
// request would and flushes it, so it exists both in memory and in SQLite.
func seedDiscoveredAgent(t *testing.T, manager *budget.BudgetManager, store storage.Store, name string) {
	t.Helper()
	manager.RecordSpend(name, 0.5)
	if err := manager.Flush(context.Background()); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	seedCostRecords(t, store, []storage.CostRecord{
		{Agent: name, Model: "gpt-4o", Provider: "openai", InputTokens: 10, OutputTokens: 5, CostUSD: 0.5, CreatedAt: time.Now().UTC()},
	})
	seedAlerts(t, store, []alert.Alert{
		alert.NewBudgetThresholdAlert(name, 80, 8, 10, "threshold", time.Now().UTC()),
	})
}

func TestServer_DeleteAgentEndpoint(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test case fields ordered for readability.
	tests := []struct {
		name       string
		method     string
		target     string
		auth       bool
		prepare    func(*testing.T, *budget.BudgetManager, storage.Store)
		wantStatus int
		wantCode   string
		assert     func(*testing.T, *budget.BudgetManager, storage.Store, map[string]any)
	}{
		{
			name:   "deletes a discovered agent and reports what was removed",
			method: http.MethodDelete,
			target: "scratch-agent",
			auth:   true,
			prepare: func(t *testing.T, manager *budget.BudgetManager, store storage.Store) {
				t.Helper()
				seedDiscoveredAgent(t, manager, store, "scratch-agent")
				seedDiscoveredAgent(t, manager, store, "bystander")
			},
			wantStatus: http.StatusOK,
			assert: func(t *testing.T, manager *budget.BudgetManager, store storage.Store, payload map[string]any) {
				t.Helper()
				if mustString(t, payload, "status") != "deleted" || mustString(t, payload, "agent") != "scratch-agent" {
					t.Fatalf("payload = %#v", payload)
				}
				removed, ok := payload["removed"].(map[string]any)
				if !ok {
					t.Fatalf("payload.removed = %#v, want object", payload["removed"])
				}
				if mustFloat(t, removed, "cost_records") != 1 || mustFloat(t, removed, "alerts") != 1 {
					t.Fatalf("removed = %#v, want one cost record and one alert", removed)
				}
				if recreated, _ := payload["recreated_on_next_request"].(bool); !recreated {
					t.Fatalf("recreated_on_next_request = %#v, want true", payload["recreated_on_next_request"])
				}

				ctx := context.Background()
				if _, found, err := store.GetAgent(ctx, "scratch-agent"); err != nil || found {
					t.Fatalf("GetAgent(scratch-agent) = found %v, err %v; want gone", found, err)
				}
				if _, found, err := store.GetAgent(ctx, "bystander"); err != nil || !found {
					t.Fatalf("GetAgent(bystander) = found %v, err %v; want kept", found, err)
				}
				if _, found, err := store.GetAgent(ctx, "email-agent"); err != nil || !found {
					t.Fatalf("GetAgent(email-agent) = found %v, err %v; want configured agent kept", found, err)
				}
				rows, err := store.QueryCosts(ctx, storage.CostQuery{Agent: "bystander", GroupBy: "none"})
				if err != nil || len(rows) != 1 {
					t.Fatalf("QueryCosts(bystander) = %d rows, err %v; want 1", len(rows), err)
				}
				for _, view := range manager.ListBudgets() {
					if view.Agent == "scratch-agent" {
						t.Fatalf("ListBudgets() still tracks scratch-agent: %#v", view)
					}
				}
			},
		},
		{
			name:       "requires authentication",
			method:     http.MethodDelete,
			target:     "scratch-agent",
			auth:       false,
			wantStatus: http.StatusUnauthorized,
			wantCode:   "auth_required",
		},
		{
			name:       "missing agent is not found",
			method:     http.MethodDelete,
			target:     "never-seen",
			auth:       true,
			wantStatus: http.StatusNotFound,
			wantCode:   "agent_not_found",
		},
		{
			name:       "malformed agent name is rejected",
			method:     http.MethodDelete,
			target:     "bad%20name",
			auth:       true,
			wantStatus: http.StatusBadRequest,
			wantCode:   "config_error",
		},
		{
			name:       "configured agent is protected",
			method:     http.MethodDelete,
			target:     "email-agent",
			auth:       true,
			wantStatus: http.StatusConflict,
			wantCode:   "agent_protected",
			assert: func(t *testing.T, _ *budget.BudgetManager, store storage.Store, _ map[string]any) {
				t.Helper()
				if _, found, err := store.GetAgent(context.Background(), "email-agent"); err != nil || !found {
					t.Fatalf("GetAgent(email-agent) = found %v, err %v; want kept", found, err)
				}
			},
		},
		{
			name:   "already deleted agent is not found",
			method: http.MethodDelete,
			target: "scratch-agent",
			auth:   true,
			prepare: func(t *testing.T, manager *budget.BudgetManager, store storage.Store) {
				t.Helper()
				seedDiscoveredAgent(t, manager, store, "scratch-agent")
				if _, err := manager.DeleteAgent(context.Background(), "scratch-agent"); err != nil {
					t.Fatalf("DeleteAgent() error = %v", err)
				}
			},
			wantStatus: http.StatusNotFound,
			wantCode:   "agent_not_found",
		},
		{
			name:       "other methods on the agent path are not allowed",
			method:     http.MethodGet,
			target:     "scratch-agent",
			auth:       true,
			wantStatus: http.StatusMethodNotAllowed,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, manager, store := newTestServer(t)
			if tt.prepare != nil {
				tt.prepare(t, manager, store)
			}

			req := httptest.NewRequest(tt.method, basePath+"/agents/"+tt.target, nil)
			if tt.auth {
				addAuthenticatedSessionCookie(t, store, req)
			}
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)

			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", recorder.Code, tt.wantStatus, recorder.Body.String())
			}
			if tt.wantCode != "" {
				assertErrorCode(t, recorder.Body, tt.wantCode)
				if tt.assert != nil {
					tt.assert(t, manager, store, nil)
				}
				return
			}
			if tt.assert != nil && recorder.Code == http.StatusOK {
				tt.assert(t, manager, store, decodeJSONMap(t, recorder.Body))
			}
		})
	}
}

// TestServer_DeleteAgentPathHandling pins what the agent path accepts, so the
// delete route cannot swallow a mistyped action or a missing name.
func TestServer_DeleteAgentPathHandling(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test case fields ordered for readability.
	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "no agent name is not found",
			method:     http.MethodDelete,
			path:       basePath + "/agents/",
			wantStatus: http.StatusNotFound,
			wantCode:   "config_error",
		},
		{
			name:       "an unknown action is not treated as a delete",
			method:     http.MethodDelete,
			path:       basePath + "/agents/scratch-agent/purge",
			wantStatus: http.StatusNotFound,
			wantCode:   "config_error",
		},
		{
			name:       "DELETE on the rename action is not allowed",
			method:     http.MethodDelete,
			path:       basePath + "/agents/scratch-agent/rename",
			wantStatus: http.StatusMethodNotAllowed,
			wantCode:   "config_error",
		},
		{
			name:       "POST on an agent is not allowed",
			method:     http.MethodPost,
			path:       basePath + "/agents/scratch-agent",
			wantStatus: http.StatusMethodNotAllowed,
			wantCode:   "config_error",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, manager, store := newTestServer(t)
			seedDiscoveredAgent(t, manager, store, "scratch-agent")

			req := httptest.NewRequest(tt.method, tt.path, nil)
			addAuthenticatedSessionCookie(t, store, req)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)

			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", recorder.Code, tt.wantStatus, recorder.Body.String())
			}
			assertErrorCode(t, recorder.Body, tt.wantCode)

			// Whatever the path did, it must not have removed the agent.
			if _, found, err := store.GetAgent(context.Background(), "scratch-agent"); err != nil || !found {
				t.Fatalf("GetAgent(scratch-agent) = found %v, err %v; want untouched", found, err)
			}
		})
	}
}

// TestServer_DeleteAgentPublishesEvent covers the stream side of the delete:
// dashboards that are already open are told the agent is gone, and a delete
// that did not happen says nothing.
func TestServer_DeleteAgentPublishesEvent(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test case fields ordered for readability.
	tests := []struct {
		name      string
		target    string
		wantEvent bool
	}{
		{name: "a successful delete announces the agent", target: "scratch-agent", wantEvent: true},
		{name: "a missing agent announces nothing", target: "never-seen", wantEvent: false},
		{name: "a protected agent announces nothing", target: "email-agent", wantEvent: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, manager, store := newTestServer(t)
			seedDiscoveredAgent(t, manager, store, "scratch-agent")

			events := server.subscribe()
			t.Cleanup(func() { server.unsubscribe(events) })

			req := httptest.NewRequest(http.MethodDelete, basePath+"/agents/"+tt.target, nil)
			addAuthenticatedSessionCookie(t, store, req)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)

			// publish is synchronous, so anything the handler sent is already
			// buffered on the subscription by the time it returns.
			var got []sseEvent
			for {
				select {
				case event := <-events:
					got = append(got, event)
					continue
				default:
				}
				break
			}

			if !tt.wantEvent {
				for _, event := range got {
					if event.name == "agent_deleted" {
						t.Fatalf("published agent_deleted %#v for a delete that did not happen", event.data)
					}
				}
				return
			}

			if len(got) != 1 {
				t.Fatalf("published %d events, want exactly agent_deleted: %#v", len(got), got)
			}
			if got[0].name != "agent_deleted" {
				t.Fatalf("event name = %q, want agent_deleted", got[0].name)
			}
			if agent, _ := got[0].data["agent"].(string); agent != tt.target {
				t.Fatalf("event agent = %#v, want %q", got[0].data["agent"], tt.target)
			}
		})
	}
}

// TestServer_DeleteAgentStoreFailure covers the case where the rows cannot be
// removed. A half-done delete is the worst outcome here, so the agent has to
// survive intact in memory and no dashboard may be told it is gone.
func TestServer_DeleteAgentStoreFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "a failed store delete reports an error and keeps the agent tracked"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := config.DefaultConfig()
			store := failingStore{deleteAgentErr: errors.New("boom")}
			manager, err := budget.NewPersistentManager(cfg.Gate, nil, store)
			if err != nil {
				t.Fatalf("NewPersistentManager() error = %v", err)
			}
			t.Cleanup(func() { _ = manager.Close() })
			manager.RecordSpend("scratch-agent", 1.5)

			server := New(cfg, manager, store, "0.1.0")
			events := server.subscribe()
			t.Cleanup(func() { server.unsubscribe(events) })

			// failingStore has no sessions, so the handler is called directly
			// rather than through the auth middleware.
			req := httptest.NewRequest(http.MethodDelete, basePath+"/agents/scratch-agent", nil)
			recorder := httptest.NewRecorder()
			server.handleAgentByName(recorder, req)

			if recorder.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500 (body %s)", recorder.Code, recorder.Body.String())
			}
			assertErrorCode(t, recorder.Body, "config_error")

			select {
			case event := <-events:
				t.Fatalf("published %q after a failed delete, want nothing", event.name)
			default:
			}

			if view := manager.GetBudget("scratch-agent"); view.SpentUSD != 1.5 {
				t.Fatalf("spent after failed delete = %v, want the agent left intact at 1.5", view.SpentUSD)
			}
			found := false
			for _, view := range manager.ListBudgets() {
				if view.Agent == "scratch-agent" {
					found = true
				}
			}
			if !found {
				t.Fatal("ListBudgets() dropped the agent even though the store delete failed")
			}
		})
	}
}

func TestServer_DeleteAgentThenRediscover(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "agent list drops the agent and lists it again after a new request"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, manager, store := newTestServer(t)
			seedDiscoveredAgent(t, manager, store, "scratch-agent")

			listAgents := func() map[string]bool {
				req := httptest.NewRequest(http.MethodGet, basePath+"/agents", nil)
				addAuthenticatedSessionCookie(t, store, req)
				recorder := httptest.NewRecorder()
				server.ServeHTTP(recorder, req)
				if recorder.Code != http.StatusOK {
					t.Fatalf("GET /agents status = %d", recorder.Code)
				}
				payload := decodeJSONMap(t, recorder.Body)
				entries, _ := payload["agents"].([]any)
				names := make(map[string]bool, len(entries))
				for _, entry := range entries {
					if agent, ok := entry.(map[string]any); ok {
						names[mustString(t, agent, "name")] = true
					}
				}
				return names
			}

			if names := listAgents(); !names["scratch-agent"] {
				t.Fatalf("agents before delete = %v, want scratch-agent", names)
			}

			req := httptest.NewRequest(http.MethodDelete, basePath+"/agents/scratch-agent", nil)
			addAuthenticatedSessionCookie(t, store, req)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusOK {
				t.Fatalf("DELETE status = %d (body %s)", recorder.Code, recorder.Body.String())
			}

			if names := listAgents(); names["scratch-agent"] {
				t.Fatalf("agents after delete = %v, want scratch-agent gone", names)
			}

			// The budget read path must not bring it back.
			budgetReq := httptest.NewRequest(http.MethodGet, basePath+"/budgets/scratch-agent", nil)
			addAuthenticatedSessionCookie(t, store, budgetReq)
			budgetRecorder := httptest.NewRecorder()
			server.ServeHTTP(budgetRecorder, budgetReq)
			if budgetRecorder.Code != http.StatusNotFound {
				t.Fatalf("GET /budgets/scratch-agent status = %d, want 404", budgetRecorder.Code)
			}
			if names := listAgents(); names["scratch-agent"] {
				t.Fatalf("agents after budget read = %v, want scratch-agent still gone", names)
			}

			// A proxied request goes through CheckBudget and RecordSpend.
			manager.CheckBudget("scratch-agent", 0.01)
			manager.RecordSpend("scratch-agent", 0.01)
			if names := listAgents(); !names["scratch-agent"] {
				t.Fatalf("agents after new request = %v, want scratch-agent rediscovered", names)
			}
		})
	}
}

func TestServer_DeleteAgentConcurrentRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		deletes int
	}{
		{name: "concurrent deletes yield one success and the rest not found", deletes: 6},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, manager, store := newTestServer(t)
			seedDiscoveredAgent(t, manager, store, "scratch-agent")

			codes := make([]int, tt.deletes)
			var wg sync.WaitGroup
			for index := 0; index < tt.deletes; index++ {
				wg.Add(1)
				go func(slot int) {
					defer wg.Done()
					req := httptest.NewRequest(http.MethodDelete, basePath+"/agents/scratch-agent", nil)
					addAuthenticatedSessionCookie(t, store, req)
					recorder := httptest.NewRecorder()
					server.ServeHTTP(recorder, req)
					codes[slot] = recorder.Code
				}(index)
			}
			wg.Wait()

			ok, notFound := 0, 0
			for _, code := range codes {
				switch code {
				case http.StatusOK:
					ok++
				case http.StatusNotFound:
					notFound++
				default:
					t.Fatalf("unexpected status %d in %v", code, codes)
				}
			}
			if ok != 1 || notFound != tt.deletes-1 {
				t.Fatalf("statuses = %v, want exactly one 200 and the rest 404", codes)
			}
		})
	}
}

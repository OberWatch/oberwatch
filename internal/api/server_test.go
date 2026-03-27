package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OberWatch/oberwatch/internal/alert"
	"github.com/OberWatch/oberwatch/internal/budget"
	"github.com/OberWatch/oberwatch/internal/config"
	"github.com/OberWatch/oberwatch/internal/storage"
)

const testAdminToken = "test-admin-token"

func TestServer_EndpointStatusAndShape(t *testing.T) {
	t.Parallel()

	updateBody := `{"limit_usd":25,"period":"daily","action_on_exceed":"reject","downgrade_chain":["claude-sonnet-4-6","claude-haiku-4-5"],"downgrade_threshold_pct":70,"alert_thresholds_pct":[50,80,100]}`

	//nolint:govet // Keep cases explicit for endpoint coverage readability.
	tests := []struct {
		name           string
		method         string
		path           string
		body           string
		auth           bool
		prepare        func(*testing.T, *budget.BudgetManager, storage.Store)
		assertResponse func(*testing.T, *http.Response, map[string]any)
		wantStatus     int
		wantContent    string
	}{
		{
			name:       "health endpoint without auth",
			method:     http.MethodGet,
			path:       basePath + "/health",
			auth:       false,
			wantStatus: http.StatusOK,
			assertResponse: func(t *testing.T, _ *http.Response, payload map[string]any) {
				t.Helper()
				mustHaveKeys(t, payload, "status", "version", "uptime_seconds", "providers", "storage_backend")
			},
		},
		{
			name:       "pricing endpoint",
			method:     http.MethodGet,
			path:       basePath + "/pricing",
			auth:       true,
			wantStatus: http.StatusOK,
			assertResponse: func(t *testing.T, _ *http.Response, payload map[string]any) {
				t.Helper()
				mustHaveKeys(t, payload, "pricing")
			},
		},
		{
			name:       "budgets list",
			method:     http.MethodGet,
			path:       basePath + "/budgets",
			auth:       true,
			wantStatus: http.StatusOK,
			assertResponse: func(t *testing.T, _ *http.Response, payload map[string]any) {
				t.Helper()
				mustHaveKeys(t, payload, "budgets", "global")
			},
		},
		{
			name:       "budget by agent",
			method:     http.MethodGet,
			path:       basePath + "/budgets/email-agent",
			auth:       true,
			wantStatus: http.StatusOK,
			assertResponse: func(t *testing.T, _ *http.Response, payload map[string]any) {
				t.Helper()
				mustHaveKeys(t, payload, "agent", "period", "limit_usd", "spent_usd", "remaining_usd", "percentage_used", "status", "action_on_exceed", "period_resets_at")
			},
		},
		{
			name:       "update budget",
			method:     http.MethodPut,
			path:       basePath + "/budgets/email-agent",
			body:       updateBody,
			auth:       true,
			wantStatus: http.StatusOK,
			assertResponse: func(t *testing.T, _ *http.Response, payload map[string]any) {
				t.Helper()
				got := mustFloat(t, payload, "limit_usd")
				if got != 25 {
					t.Fatalf("limit_usd = %v, want 25", got)
				}
			},
		},
		{
			name:       "reset budget",
			method:     http.MethodPost,
			path:       basePath + "/budgets/email-agent/reset",
			auth:       true,
			wantStatus: http.StatusOK,
			assertResponse: func(t *testing.T, _ *http.Response, payload map[string]any) {
				t.Helper()
				if payload["status"] != "ok" {
					t.Fatalf("status = %v, want ok", payload["status"])
				}
			},
		},
		{
			name:       "kill budget",
			method:     http.MethodPost,
			path:       basePath + "/budgets/email-agent/kill",
			auth:       true,
			wantStatus: http.StatusOK,
			assertResponse: func(t *testing.T, _ *http.Response, payload map[string]any) {
				t.Helper()
				if payload["status"] != "ok" {
					t.Fatalf("status = %v, want ok", payload["status"])
				}
			},
		},
		{
			name:   "enable budget",
			method: http.MethodPost,
			path:   basePath + "/budgets/email-agent/enable",
			auth:   true,
			prepare: func(t *testing.T, manager *budget.BudgetManager, _ storage.Store) {
				t.Helper()
				manager.KillAgent("email-agent")
			},
			wantStatus: http.StatusOK,
			assertResponse: func(t *testing.T, _ *http.Response, payload map[string]any) {
				t.Helper()
				if payload["status"] != "ok" {
					t.Fatalf("status = %v, want ok", payload["status"])
				}
			},
		},
		{
			name:       "kill all",
			method:     http.MethodPost,
			path:       basePath + "/kill-all",
			auth:       true,
			wantStatus: http.StatusOK,
			assertResponse: func(t *testing.T, _ *http.Response, payload map[string]any) {
				t.Helper()
				if payload["status"] != "ok" {
					t.Fatalf("status = %v, want ok", payload["status"])
				}
			},
		},
		{
			name:   "costs endpoint",
			method: http.MethodGet,
			path:   basePath + "/costs?group_by=agent",
			auth:   true,
			prepare: func(t *testing.T, _ *budget.BudgetManager, store storage.Store) {
				t.Helper()
				seedCostRecords(t, store, []storage.CostRecord{
					{Agent: "email-agent", Model: "gpt-4o", Provider: "openai", InputTokens: 10, OutputTokens: 5, CostUSD: 0.12, CreatedAt: time.Now().UTC()},
				})
			},
			wantStatus: http.StatusOK,
			assertResponse: func(t *testing.T, _ *http.Response, payload map[string]any) {
				t.Helper()
				mustHaveKeys(t, payload, "total_usd", "total_requests", "total_input_tokens", "total_output_tokens", "breakdown")
			},
		},
		{
			name:   "costs export endpoint",
			method: http.MethodGet,
			path:   basePath + "/costs/export",
			auth:   true,
			prepare: func(t *testing.T, _ *budget.BudgetManager, store storage.Store) {
				t.Helper()
				seedCostRecords(t, store, []storage.CostRecord{
					{Agent: "email-agent", Model: "gpt-4o", Provider: "openai", InputTokens: 10, OutputTokens: 5, CostUSD: 0.12, CreatedAt: time.Now().UTC()},
				})
			},
			wantStatus:  http.StatusOK,
			wantContent: "text/csv",
		},
		{
			name:   "agents endpoint",
			method: http.MethodGet,
			path:   basePath + "/agents",
			auth:   true,
			prepare: func(t *testing.T, _ *budget.BudgetManager, store storage.Store) {
				t.Helper()
				seedCostRecords(t, store, []storage.CostRecord{
					{Agent: "email-agent", Model: "gpt-4o", Provider: "openai", InputTokens: 10, OutputTokens: 5, CostUSD: 0.12, CreatedAt: time.Now().UTC()},
				})
			},
			wantStatus: http.StatusOK,
			assertResponse: func(t *testing.T, _ *http.Response, payload map[string]any) {
				t.Helper()
				mustHaveKeys(t, payload, "agents")
			},
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

			request := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			if tt.body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			if tt.auth {
				request.Header.Set("Authorization", "Bearer "+testAdminToken)
			}

			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			response := recorder.Result()
			t.Cleanup(func() {
				_ = response.Body.Close()
			})

			if response.StatusCode != tt.wantStatus {
				t.Fatalf("status code = %d, want %d", response.StatusCode, tt.wantStatus)
			}
			if tt.wantContent != "" {
				if !strings.Contains(response.Header.Get("Content-Type"), tt.wantContent) {
					t.Fatalf("Content-Type = %q, want contains %q", response.Header.Get("Content-Type"), tt.wantContent)
				}
			}

			if tt.assertResponse != nil {
				payload := decodeJSONMap(t, response.Body)
				tt.assertResponse(t, response, payload)
			}
		})
	}
}

func TestServer_StreamEndpointStatusAndContentType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		withAuth   bool
		wantStatus int
	}{
		{name: "stream requires auth", withAuth: false, wantStatus: http.StatusUnauthorized},
		{name: "stream returns sse when authorized", withAuth: true, wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, _, _ := newTestServer(t)
			ctx := context.Background()
			if tt.withAuth {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(context.Background())
				cancel()
			}
			request := httptest.NewRequest(http.MethodGet, basePath+"/stream", nil).WithContext(ctx)
			if tt.withAuth {
				request.Header.Set("Authorization", "Bearer "+testAdminToken)
			}

			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)

			response := recorder.Result()
			if response.StatusCode != tt.wantStatus {
				t.Fatalf("status code = %d, want %d", response.StatusCode, tt.wantStatus)
			}
			if tt.withAuth && !strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
				t.Fatalf("Content-Type = %q, want text/event-stream", response.Header.Get("Content-Type"))
			}
		})
	}
}

func TestServer_AuthMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		authorization string
		path          string
		wantStatus    int
	}{
		{name: "missing token rejected", authorization: "", path: basePath + "/budgets", wantStatus: http.StatusUnauthorized},
		{name: "wrong token rejected", authorization: "Bearer wrong", path: basePath + "/budgets", wantStatus: http.StatusUnauthorized},
		{name: "correct token allowed", authorization: "Bearer " + testAdminToken, path: basePath + "/budgets", wantStatus: http.StatusOK},
		{name: "health bypasses auth", authorization: "", path: basePath + "/health", wantStatus: http.StatusOK},
		{name: "pricing bypasses auth", authorization: "", path: basePath + "/pricing", wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, _, _ := newTestServer(t)
			request := httptest.NewRequest(http.MethodGet, tt.path, nil)
			if tt.authorization != "" {
				request.Header.Set("Authorization", tt.authorization)
			}
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)

			if recorder.Code != tt.wantStatus {
				t.Fatalf("status code = %d, want %d", recorder.Code, tt.wantStatus)
			}
		})
	}
}

func TestServer_BudgetUpdatePersists(t *testing.T) {
	t.Parallel()

	//nolint:govet // Keep table fields in assertion-friendly order.
	tests := []struct {
		name               string
		body               string
		wantLimit          float64
		wantActionOnExceed string
	}{
		{
			name:               "put budget then get reflects update",
			body:               `{"limit_usd":42,"period":"daily","action_on_exceed":"kill","downgrade_chain":["claude-sonnet-4-6","claude-haiku-4-5"],"downgrade_threshold_pct":66,"alert_thresholds_pct":[50,80,100]}`,
			wantLimit:          42,
			wantActionOnExceed: "kill",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, _, _ := newTestServer(t)

			putReq := httptest.NewRequest(http.MethodPut, basePath+"/budgets/email-agent", strings.NewReader(tt.body))
			putReq.Header.Set("Authorization", "Bearer "+testAdminToken)
			putReq.Header.Set("Content-Type", "application/json")
			putRecorder := httptest.NewRecorder()
			server.ServeHTTP(putRecorder, putReq)
			if putRecorder.Code != http.StatusOK {
				t.Fatalf("PUT status = %d, want %d", putRecorder.Code, http.StatusOK)
			}

			getReq := httptest.NewRequest(http.MethodGet, basePath+"/budgets/email-agent", nil)
			getReq.Header.Set("Authorization", "Bearer "+testAdminToken)
			getRecorder := httptest.NewRecorder()
			server.ServeHTTP(getRecorder, getReq)
			if getRecorder.Code != http.StatusOK {
				t.Fatalf("GET status = %d, want %d", getRecorder.Code, http.StatusOK)
			}

			payload := decodeJSONMap(t, getRecorder.Result().Body)
			limitUSD := mustFloat(t, payload, "limit_usd")
			if limitUSD != tt.wantLimit {
				t.Fatalf("limit_usd = %v, want %v", limitUSD, tt.wantLimit)
			}
			actionOnExceed := mustString(t, payload, "action_on_exceed")
			if actionOnExceed != tt.wantActionOnExceed {
				t.Fatalf("action_on_exceed = %v, want %v", actionOnExceed, tt.wantActionOnExceed)
			}
		})
	}
}

func TestServer_KillAndEnableToggleStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		actionPath string
		wantStatus string
	}{
		{name: "kill sets status to killed", actionPath: "/kill", wantStatus: "killed"},
		{name: "enable restores status to active", actionPath: "/enable", wantStatus: "active"},
	}

	server, _, _ := newTestServer(t)

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if tt.actionPath == "/enable" {
				killReq := httptest.NewRequest(http.MethodPost, basePath+"/budgets/email-agent/kill", nil)
				killReq.Header.Set("Authorization", "Bearer "+testAdminToken)
				killRecorder := httptest.NewRecorder()
				server.ServeHTTP(killRecorder, killReq)
			}

			actionReq := httptest.NewRequest(http.MethodPost, basePath+"/budgets/email-agent"+tt.actionPath, nil)
			actionReq.Header.Set("Authorization", "Bearer "+testAdminToken)
			actionRecorder := httptest.NewRecorder()
			server.ServeHTTP(actionRecorder, actionReq)
			if actionRecorder.Code != http.StatusOK {
				t.Fatalf("action status = %d, want %d", actionRecorder.Code, http.StatusOK)
			}

			getReq := httptest.NewRequest(http.MethodGet, basePath+"/budgets/email-agent", nil)
			getReq.Header.Set("Authorization", "Bearer "+testAdminToken)
			getRecorder := httptest.NewRecorder()
			server.ServeHTTP(getRecorder, getReq)
			payload := decodeJSONMap(t, getRecorder.Result().Body)
			status := mustString(t, payload, "status")
			if status != tt.wantStatus {
				t.Fatalf("status = %s, want %s", status, tt.wantStatus)
			}
		})
	}
}

func TestServer_CostFiltering(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Truncate(time.Second)
	from := now.Add(-1 * time.Hour).Format(time.RFC3339)
	to := now.Add(1 * time.Hour).Format(time.RFC3339)

	tests := []struct {
		name         string
		query        string
		wantRequests float64
		wantCost     float64
	}{
		{
			name:         "filters by agent model and time",
			query:        "?agent=agent-a&model=gpt-4o&from=" + from + "&to=" + to + "&group_by=none",
			wantRequests: 1,
			wantCost:     0.20,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, _, store := newTestServer(t)
			seedCostRecords(t, store, []storage.CostRecord{
				{Agent: "agent-a", Model: "gpt-4o", Provider: "openai", InputTokens: 100, OutputTokens: 50, CostUSD: 0.20, CreatedAt: now},
				{Agent: "agent-a", Model: "gpt-4o-mini", Provider: "openai", InputTokens: 100, OutputTokens: 50, CostUSD: 0.05, CreatedAt: now},
				{Agent: "agent-b", Model: "gpt-4o", Provider: "openai", InputTokens: 100, OutputTokens: 50, CostUSD: 0.30, CreatedAt: now},
			})

			req := httptest.NewRequest(http.MethodGet, basePath+"/costs"+tt.query, nil)
			req.Header.Set("Authorization", "Bearer "+testAdminToken)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusOK)
			}

			payload := decodeJSONMap(t, recorder.Result().Body)
			totalRequests := mustFloat(t, payload, "total_requests")
			if totalRequests != tt.wantRequests {
				t.Fatalf("total_requests = %v, want %v", totalRequests, tt.wantRequests)
			}
			totalUSD := mustFloat(t, payload, "total_usd")
			if totalUSD != tt.wantCost {
				t.Fatalf("total_usd = %v, want %v", totalUSD, tt.wantCost)
			}
		})
	}
}

func TestServer_StreamSendsCostAndAlertEvents(t *testing.T) {
	t.Parallel()

	//nolint:govet // Keep table fields in assertion-friendly order.
	tests := []struct {
		name       string
		trigger    func(*Server, *budget.BudgetManager)
		wantEvent  string
		assertData func(*testing.T, map[string]any)
	}{
		{
			name: "cost sink emits cost_update",
			trigger: func(server *Server, manager *budget.BudgetManager) {
				manager.RecordSpend("email-agent", 0.12)
				sink := server.WrapCostSink(nil)
				sink.Enqueue(storage.CostRecord{Agent: "email-agent", CostUSD: 0.12})
			},
			wantEvent: "cost_update",
			assertData: func(t *testing.T, payload map[string]any) {
				t.Helper()
				requestCost := mustFloat(t, payload, "request_cost_usd")
				if requestCost != 0.12 {
					t.Fatalf("request_cost_usd = %v, want 0.12", requestCost)
				}
			},
		},
		{
			name: "budget dispatcher emits budget_alert",
			trigger: func(server *Server, _ *budget.BudgetManager) {
				dispatcher := server.WrapDispatcher(nil)
				dispatcher.Dispatch(context.Background(), alert.NewBudgetThresholdAlert("email-agent", 80, 8, 10, "alert", time.Now().UTC()))
			},
			wantEvent: "budget_alert",
			assertData: func(t *testing.T, payload map[string]any) {
				t.Helper()
				threshold := mustFloat(t, payload, "threshold_pct")
				if threshold != 80 {
					t.Fatalf("threshold_pct = %v, want 80", threshold)
				}
			},
		},
		{
			name: "agent killed dispatcher emits agent_killed",
			trigger: func(server *Server, _ *budget.BudgetManager) {
				dispatcher := server.WrapDispatcher(nil)
				dispatcher.Dispatch(context.Background(), alert.NewAgentKilledAlert("email-agent", "budget_exceeded"))
			},
			wantEvent: "agent_killed",
			assertData: func(t *testing.T, payload map[string]any) {
				t.Helper()
				reason := mustString(t, payload, "reason")
				if reason != "budget_exceeded" {
					t.Fatalf("reason = %v, want budget_exceeded", reason)
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, manager, _ := newTestServer(t)
			eventCh := server.subscribe()
			t.Cleanup(func() {
				server.unsubscribe(eventCh)
			})

			tt.trigger(server, manager)

			select {
			case event := <-eventCh:
				if event.name != tt.wantEvent {
					t.Fatalf("event name = %q, want %q", event.name, tt.wantEvent)
				}
				tt.assertData(t, event.data)
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for SSE event")
			}
		})
	}
}

func newTestServer(t *testing.T) (*Server, *budget.BudgetManager, storage.Store) {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.Server.AdminToken = testAdminToken
	cfg.Gate.Agents = []config.AgentBudgetConfig{
		{
			Name:           "email-agent",
			LimitUSD:       10,
			Period:         config.BudgetPeriodDaily,
			ActionOnExceed: config.BudgetActionAlert,
		},
	}

	manager := budget.NewManager(cfg.Gate, nil)

	dsn := filepath.Join(t.TempDir(), "oberwatch-api-test.db")
	sqliteStore, err := storage.NewSQLiteStore(dsn, 0, nil)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() {
		_ = sqliteStore.Close()
	})

	server := New(cfg, manager, sqliteStore, "0.1.0")
	return server, manager, sqliteStore
}

func seedCostRecords(t *testing.T, store storage.Store, records []storage.CostRecord) {
	t.Helper()

	ctx := context.Background()
	for _, record := range records {
		if err := store.SaveCostRecord(ctx, record); err != nil {
			t.Fatalf("SaveCostRecord() error = %v", err)
		}
	}
}

func decodeJSONMap(t *testing.T, body io.Reader) map[string]any {
	t.Helper()

	var payload map[string]any
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	return payload
}

func mustHaveKeys(t *testing.T, payload map[string]any, keys ...string) {
	t.Helper()

	for _, key := range keys {
		if _, ok := payload[key]; !ok {
			t.Fatalf("payload missing key %q: %#v", key, payload)
		}
	}
}

func mustFloat(t *testing.T, payload map[string]any, key string) float64 {
	t.Helper()

	value, ok := payload[key]
	if !ok {
		t.Fatalf("payload missing key %q: %#v", key, payload)
	}
	asFloat, ok := value.(float64)
	if !ok {
		t.Fatalf("payload key %q type = %T, want float64", key, value)
	}
	return asFloat
}

func mustString(t *testing.T, payload map[string]any, key string) string {
	t.Helper()

	value, ok := payload[key]
	if !ok {
		t.Fatalf("payload missing key %q: %#v", key, payload)
	}
	asString, ok := value.(string)
	if !ok {
		t.Fatalf("payload key %q type = %T, want string", key, value)
	}
	return asString
}

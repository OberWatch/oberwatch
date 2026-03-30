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
	"golang.org/x/crypto/bcrypt"
)

const (
	testAdminPassword = "super-secret-password"
	testAdminUsername = "admin"
	testSessionToken  = "session-token-1234567890abcdefsession-token-1234567890abcdef"
)

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
			name:   "alerts endpoint",
			method: http.MethodGet,
			path:   basePath + "/alerts",
			auth:   true,
			prepare: func(t *testing.T, _ *budget.BudgetManager, store storage.Store) {
				t.Helper()
				seedAlerts(t, store, []alert.Alert{
					alert.NewBudgetThresholdAlert("email-agent", 80, 8, 10, "threshold", time.Now().UTC()),
				})
			},
			wantStatus: http.StatusOK,
			assertResponse: func(t *testing.T, _ *http.Response, payload map[string]any) {
				t.Helper()
				mustHaveKeys(t, payload, "alerts")
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
				addAuthenticatedSessionCookie(t, store, request)
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

			server, _, store := newTestServer(t)
			if tt.withAuth {
				authReq := httptest.NewRequest(http.MethodGet, basePath+"/stream", nil)
				addAuthenticatedSessionCookie(t, store, authReq)
				if !server.authorized(authReq) {
					t.Fatal("authorized(authReq) = false, want true")
				}

				ctx, cancel := context.WithCancel(context.Background())
				request := httptest.NewRequest(http.MethodGet, basePath+"/stream", nil).WithContext(ctx)
				addAuthenticatedSessionCookie(t, store, request)

				recorder := httptest.NewRecorder()
				done := make(chan struct{})
				go func() {
					server.handleStream(recorder, request)
					close(done)
				}()
				cancel()
				<-done

				response := recorder.Result()
				if response.StatusCode != tt.wantStatus {
					t.Fatalf("status code = %d, want %d", response.StatusCode, tt.wantStatus)
				}
				if !strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
					t.Fatalf("Content-Type = %q, want text/event-stream", response.Header.Get("Content-Type"))
				}
				return
			}

			request := httptest.NewRequest(http.MethodGet, basePath+"/stream", nil)
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
		name       string
		path       string
		withCookie bool
		wantStatus int
	}{
		{name: "missing session rejected", path: basePath + "/budgets", wantStatus: http.StatusUnauthorized},
		{name: "valid session allowed", path: basePath + "/budgets", withCookie: true, wantStatus: http.StatusOK},
		{name: "health bypasses auth", path: basePath + "/health", wantStatus: http.StatusOK},
		{name: "auth status bypasses auth", path: basePath + "/auth/status", wantStatus: http.StatusOK},
		{name: "setup bypasses auth", path: basePath + "/setup", wantStatus: http.StatusMethodNotAllowed},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, _, store := newTestServer(t)
			request := httptest.NewRequest(http.MethodGet, tt.path, nil)
			if tt.withCookie {
				addAuthenticatedSessionCookie(t, store, request)
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

			server, _, store := newTestServer(t)

			putReq := httptest.NewRequest(http.MethodPut, basePath+"/budgets/email-agent", strings.NewReader(tt.body))
			addAuthenticatedSessionCookie(t, store, putReq)
			putReq.Header.Set("Content-Type", "application/json")
			putRecorder := httptest.NewRecorder()
			server.ServeHTTP(putRecorder, putReq)
			if putRecorder.Code != http.StatusOK {
				t.Fatalf("PUT status = %d, want %d", putRecorder.Code, http.StatusOK)
			}

			getReq := httptest.NewRequest(http.MethodGet, basePath+"/budgets/email-agent", nil)
			addAuthenticatedSessionCookie(t, store, getReq)
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

	server, _, store := newTestServer(t)

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if tt.actionPath == "/enable" {
				killReq := httptest.NewRequest(http.MethodPost, basePath+"/budgets/email-agent/kill", nil)
				addAuthenticatedSessionCookie(t, store, killReq)
				killRecorder := httptest.NewRecorder()
				server.ServeHTTP(killRecorder, killReq)
			}

			actionReq := httptest.NewRequest(http.MethodPost, basePath+"/budgets/email-agent"+tt.actionPath, nil)
			addAuthenticatedSessionCookie(t, store, actionReq)
			actionRecorder := httptest.NewRecorder()
			server.ServeHTTP(actionRecorder, actionReq)
			if actionRecorder.Code != http.StatusOK {
				t.Fatalf("action status = %d, want %d", actionRecorder.Code, http.StatusOK)
			}

			getReq := httptest.NewRequest(http.MethodGet, basePath+"/budgets/email-agent", nil)
			addAuthenticatedSessionCookie(t, store, getReq)
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

func TestServer_RenameAgentEndpoint(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test case fields ordered for readability.
	tests := []struct {
		name       string
		oldName    string
		body       string
		wantStatus int
		prepare    func(*testing.T, storage.Store)
	}{
		{
			name:       "rename succeeds and migrates cost records",
			oldName:    "email-agent",
			body:       `{"new_name":"billing-agent"}`,
			wantStatus: http.StatusOK,
			prepare: func(t *testing.T, store storage.Store) {
				t.Helper()
				seedCostRecords(t, store, []storage.CostRecord{
					{Agent: "email-agent", Model: "gpt-4o", Provider: "openai", InputTokens: 10, OutputTokens: 5, CostUSD: 0.1, CreatedAt: time.Now().UTC()},
				})
			},
		},
		{
			name:       "rename rejects invalid names",
			oldName:    "email-agent",
			body:       `{"new_name":"bad name"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "rename rejects conflicts",
			oldName:    "email-agent",
			body:       `{"new_name":"other-agent"}`,
			wantStatus: http.StatusConflict,
			prepare: func(t *testing.T, store storage.Store) {
				t.Helper()
				if err := store.UpsertAgent(context.Background(), storage.AgentRecord{
					Name:            "other-agent",
					Status:          "active",
					BudgetPeriod:    config.BudgetPeriodDaily,
					ActionOnExceed:  config.BudgetActionAlert,
					FirstSeenAt:     time.Now().UTC(),
					LastSeenAt:      time.Now().UTC(),
					PeriodStartedAt: time.Now().UTC(),
					PeriodResetsAt:  time.Now().UTC().Add(24 * time.Hour),
				}); err != nil {
					t.Fatalf("UpsertAgent(conflict seed) error = %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, _, store := newTestServer(t)
			if tt.prepare != nil {
				tt.prepare(t, store)
			}

			req := httptest.NewRequest(http.MethodPut, basePath+"/agents/"+tt.oldName+"/rename", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			addAuthenticatedSessionCookie(t, store, req)

			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}

			if tt.wantStatus == http.StatusOK {
				renamed, found, err := store.GetAgent(context.Background(), "billing-agent")
				if err != nil {
					t.Fatalf("GetAgent(renamed) error = %v", err)
				}
				if !found || renamed.Name != "billing-agent" {
					t.Fatalf("renamed record = %#v, found = %v", renamed, found)
				}

				rows, err := store.QueryCosts(context.Background(), storage.CostQuery{Agent: "billing-agent", GroupBy: "agent"})
				if err != nil {
					t.Fatalf("QueryCosts(renamed) error = %v", err)
				}
				if len(rows) != 1 {
					t.Fatalf("len(QueryCosts(renamed)) = %d, want 1", len(rows))
				}
			}
		})
	}
}

func TestServer_EmergencyStopAndResume(t *testing.T) {
	t.Parallel()

	server, manager, store := newTestServer(t)

	for _, requestPath := range []string{"/kill-all", "/resume"} {
		req := httptest.NewRequest(http.MethodPost, basePath+requestPath, nil)
		addAuthenticatedSessionCookie(t, store, req)
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want %d", requestPath, recorder.Code, http.StatusOK)
		}
	}

	value, found, err := store.GetSetting(context.Background(), "emergency_stop")
	if err != nil {
		t.Fatalf("GetSetting(emergency_stop) error = %v", err)
	}
	if !found || value != "false" {
		t.Fatalf("emergency_stop setting = %q, found = %v, want false/true", value, found)
	}
	if manager.EmergencyStop() {
		t.Fatal("EmergencyStop() = true, want false after resume")
	}

	killReq := httptest.NewRequest(http.MethodPost, basePath+"/kill-all", nil)
	addAuthenticatedSessionCookie(t, store, killReq)
	killRecorder := httptest.NewRecorder()
	server.ServeHTTP(killRecorder, killReq)
	if killRecorder.Code != http.StatusOK {
		t.Fatalf("kill-all status = %d, want %d", killRecorder.Code, http.StatusOK)
	}

	healthReq := httptest.NewRequest(http.MethodGet, basePath+"/health", nil)
	healthRecorder := httptest.NewRecorder()
	server.ServeHTTP(healthRecorder, healthReq)
	if healthRecorder.Code != http.StatusOK {
		t.Fatalf("health status = %d, want %d", healthRecorder.Code, http.StatusOK)
	}
	payload := decodeJSONMap(t, healthRecorder.Result().Body)
	if got, ok := payload["emergency_stop"].(bool); !ok || !got {
		t.Fatalf("health emergency_stop = %v, want true", payload["emergency_stop"])
	}
}

func TestServer_ParseHelpers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		target     string
		wantAgent  string
		wantAction string
		wantOK     bool
	}{
		{name: "budget path with action", target: basePath + "/budgets/email-agent/reset", wantAgent: "email-agent", wantAction: "reset", wantOK: true},
		{name: "agent rename path", target: basePath + "/agents/unknown/rename", wantAgent: "unknown", wantAction: "rename", wantOK: true},
		{name: "invalid budget path", target: basePath + "/budgets/", wantOK: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if strings.Contains(tt.target, "/budgets/") {
				agent, action, ok := parseBudgetPath(tt.target)
				if agent != tt.wantAgent || action != tt.wantAction || ok != tt.wantOK {
					t.Fatalf("parseBudgetPath() = (%q, %q, %v), want (%q, %q, %v)", agent, action, ok, tt.wantAgent, tt.wantAction, tt.wantOK)
				}
				return
			}

			agent, action, ok := parseAgentPath(tt.target)
			if agent != tt.wantAgent || action != tt.wantAction || ok != tt.wantOK {
				t.Fatalf("parseAgentPath() = (%q, %q, %v), want (%q, %q, %v)", agent, action, ok, tt.wantAgent, tt.wantAction, tt.wantOK)
			}
		})
	}

	req := httptest.NewRequest(http.MethodGet, basePath+"/alerts?agent=email-agent&type=budget_threshold&limit=5&from=2026-03-28T10:00:00Z&to=2026-03-28T11:00:00Z", nil)
	query, err := parseAlertQuery(req)
	if err != nil {
		t.Fatalf("parseAlertQuery(valid) error = %v", err)
	}
	if query.Agent != "email-agent" || query.Type != alert.Type("budget_threshold") || query.Limit != 5 {
		t.Fatalf("parseAlertQuery(valid) = %#v", query)
	}

	badReq := httptest.NewRequest(http.MethodGet, basePath+"/alerts?limit=oops", nil)
	if _, err := parseAlertQuery(badReq); err == nil {
		t.Fatal("parseAlertQuery(invalid limit) error = nil, want non-nil")
	}

	if !validAgentName("email-agent_01") {
		t.Fatal("validAgentName(valid) = false, want true")
	}
	if validAgentName("bad name") {
		t.Fatal("validAgentName(invalid) = true, want false")
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
			addAuthenticatedSessionCookie(t, store, req)
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

func TestServer_PublishAlertPersistsAlert(t *testing.T) {
	t.Parallel()

	server, _, store := newTestServer(t)
	entry := alert.NewBudgetThresholdAlert("email-agent", 80, 8, 10, "alert", time.Now().UTC())

	server.PublishAlert(entry)

	results, err := store.QueryAlerts(context.Background(), storage.AlertQuery{Agent: "email-agent"})
	if err != nil {
		t.Fatalf("QueryAlerts() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(QueryAlerts()) = %d, want 1", len(results))
	}
	if results[0].Type != alert.TypeBudgetThreshold {
		t.Fatalf("stored alert type = %q, want %q", results[0].Type, alert.TypeBudgetThreshold)
	}
	if results[0].ThresholdPct != 80 {
		t.Fatalf("stored threshold = %v, want 80", results[0].ThresholdPct)
	}
}

func TestServer_SetupLoginLogoutAndPasswordChange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name func(*testing.T)
	}{
		{
			name: func(t *testing.T) {
				t.Helper()

				server, _, store := newTestServer(t)
				setupReq := httptest.NewRequest(http.MethodPost, basePath+"/setup", strings.NewReader(`{"username":"admin","password":"pw123","confirm_password":"pw123"}`))
				setupReq.Header.Set("Content-Type", "application/json")
				setupRecorder := httptest.NewRecorder()
				server.ServeHTTP(setupRecorder, setupReq)
				if setupRecorder.Code != http.StatusOK {
					t.Fatalf("setup status = %d, want %d", setupRecorder.Code, http.StatusOK)
				}

				setupValue, found, err := store.GetSetting(context.Background(), setupCompleteKey)
				if err != nil {
					t.Fatalf("GetSetting(setup_complete) error = %v", err)
				}
				if !found || setupValue != "true" {
					t.Fatalf("setup_complete = (%q, %v), want (true, true)", setupValue, found)
				}

				secondReq := httptest.NewRequest(http.MethodPost, basePath+"/setup", strings.NewReader(`{"username":"admin","password":"pw123","confirm_password":"pw123"}`))
				secondReq.Header.Set("Content-Type", "application/json")
				secondRecorder := httptest.NewRecorder()
				server.ServeHTTP(secondRecorder, secondReq)
				if secondRecorder.Code != http.StatusConflict {
					t.Fatalf("second setup status = %d, want %d", secondRecorder.Code, http.StatusConflict)
				}
			},
		},
		{
			name: func(t *testing.T) {
				t.Helper()

				server, _, store := newTestServer(t)
				seedAdminCredentials(t, store)

				loginCases := []struct {
					name       string
					body       string
					wantStatus int
				}{
					{name: "correct credentials", body: `{"username":"admin","password":"` + testAdminPassword + `"}`, wantStatus: http.StatusOK},
					{name: "incorrect credentials", body: `{"username":"admin","password":"wrong"}`, wantStatus: http.StatusUnauthorized},
				}

				for _, tc := range loginCases {
					tc := tc
					t.Run(tc.name, func(t *testing.T) {
						t.Parallel()

						req := httptest.NewRequest(http.MethodPost, basePath+"/login", strings.NewReader(tc.body))
						req.Header.Set("Content-Type", "application/json")
						recorder := httptest.NewRecorder()
						server.ServeHTTP(recorder, req)
						if recorder.Code != tc.wantStatus {
							t.Fatalf("login status = %d, want %d", recorder.Code, tc.wantStatus)
						}
					})
				}
			},
		},
		{
			name: func(t *testing.T) {
				t.Helper()

				server, _, store := newTestServer(t)
				req := httptest.NewRequest(http.MethodPost, basePath+"/logout", nil)
				addAuthenticatedSessionCookie(t, store, req)
				recorder := httptest.NewRecorder()
				server.ServeHTTP(recorder, req)
				if recorder.Code != http.StatusOK {
					t.Fatalf("logout status = %d, want %d", recorder.Code, http.StatusOK)
				}

				_, found, err := store.GetSetting(context.Background(), sessionTokenKey)
				if err != nil {
					t.Fatalf("GetSetting(session_token) error = %v", err)
				}
				if found {
					t.Fatal("session_token still exists after logout")
				}
			},
		},
		{
			name: func(t *testing.T) {
				t.Helper()

				server, _, store := newTestServer(t)
				seedAdminCredentials(t, store)
				sessionToken := seedSession(t, store, time.Now().UTC().Add(24*time.Hour))

				passwordCases := []struct {
					name       string
					body       string
					wantStatus int
				}{
					{
						name:       "correct current password",
						body:       `{"current_password":"` + testAdminPassword + `","new_password":"new-secret","confirm_password":"new-secret"}`,
						wantStatus: http.StatusOK,
					},
					{
						name:       "incorrect current password",
						body:       `{"current_password":"wrong","new_password":"new-secret","confirm_password":"new-secret"}`,
						wantStatus: http.StatusUnauthorized,
					},
				}

				for _, tc := range passwordCases {
					tc := tc
					t.Run(tc.name, func(t *testing.T) {
						t.Parallel()

						req := httptest.NewRequest(http.MethodPut, basePath+"/settings/password", strings.NewReader(tc.body))
						req.Header.Set("Content-Type", "application/json")
						req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
						recorder := httptest.NewRecorder()
						server.ServeHTTP(recorder, req)
						if recorder.Code != tc.wantStatus {
							t.Fatalf("password change status = %d, want %d", recorder.Code, tc.wantStatus)
						}
					})
				}
			},
		},
		{
			name: func(t *testing.T) {
				t.Helper()

				server, _, store := newTestServer(t)
				seedAdminCredentials(t, store)
				expiredToken := seedSession(t, store, time.Now().UTC().Add(-1*time.Minute))

				req := httptest.NewRequest(http.MethodGet, basePath+"/budgets", nil)
				req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: expiredToken})
				recorder := httptest.NewRecorder()
				server.ServeHTTP(recorder, req)
				if recorder.Code != http.StatusUnauthorized {
					t.Fatalf("expired session status = %d, want %d", recorder.Code, http.StatusUnauthorized)
				}
			},
		},
	}

	for idx, tt := range tests {
		t.Run(string(rune('a'+idx)), tt.name)
	}
}

func TestServer_AgentsEndpointIncludesConfiguredAgentsFromSQLite(t *testing.T) {
	t.Parallel()

	server, _, store := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, basePath+"/agents", nil)
	addAuthenticatedSessionCookie(t, store, req)

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusOK)
	}

	payload := decodeJSONMap(t, recorder.Result().Body)
	agentsValue, ok := payload["agents"].([]any)
	if !ok {
		t.Fatalf("agents type = %T, want []any", payload["agents"])
	}
	if len(agentsValue) != 1 {
		t.Fatalf("len(agents) = %d, want 1", len(agentsValue))
	}
}

func newTestServer(t *testing.T) (*Server, *budget.BudgetManager, storage.Store) {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.Gate.Agents = []config.AgentBudgetConfig{
		{
			Name:           "email-agent",
			LimitUSD:       10,
			Period:         config.BudgetPeriodDaily,
			ActionOnExceed: config.BudgetActionAlert,
		},
	}

	dsn := filepath.Join(t.TempDir(), "oberwatch-api-test.db")
	sqliteStore, err := storage.NewSQLiteStore(dsn, 0, nil)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() {
		_ = sqliteStore.Close()
	})

	if seedErr := budget.SeedConfiguredAgents(context.Background(), cfg.Gate, sqliteStore, nil); seedErr != nil {
		t.Fatalf("SeedConfiguredAgents() error = %v", seedErr)
	}

	manager, err := budget.NewPersistentManager(cfg.Gate, nil, sqliteStore)
	if err != nil {
		t.Fatalf("NewPersistentManager() error = %v", err)
	}
	t.Cleanup(func() {
		_ = manager.Close()
	})

	server := New(cfg, manager, sqliteStore, "0.1.0")
	return server, manager, sqliteStore
}

func addAuthenticatedSessionCookie(t *testing.T, store storage.Store, req *http.Request) {
	t.Helper()

	token := seedSession(t, store, time.Now().UTC().Add(24*time.Hour))
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
}

func seedAdminCredentials(t *testing.T, store storage.Store) {
	t.Helper()

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(testAdminPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("GenerateFromPassword() error = %v", err)
	}

	ctx := context.Background()
	settings := map[string]string{
		adminUsernameKey:     testAdminUsername,
		adminPasswordHashKey: string(passwordHash),
		setupCompleteKey:     "true",
	}
	for key, value := range settings {
		if err := store.SetSetting(ctx, key, value); err != nil {
			t.Fatalf("SetSetting(%q) error = %v", key, err)
		}
	}
}

func seedSession(t *testing.T, store storage.Store, expiresAt time.Time) string {
	t.Helper()

	seedAdminCredentials(t, store)

	ctx := context.Background()
	if err := store.SetSetting(ctx, sessionTokenKey, testSessionToken); err != nil {
		t.Fatalf("SetSetting(session_token) error = %v", err)
	}
	if err := store.SetSetting(ctx, sessionExpiresAtKey, expiresAt.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("SetSetting(session_expires_at) error = %v", err)
	}
	return testSessionToken
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

func seedAlerts(t *testing.T, store storage.Store, records []alert.Alert) {
	t.Helper()

	ctx := context.Background()
	for _, record := range records {
		if err := store.SaveAlert(ctx, record); err != nil {
			t.Fatalf("SaveAlert() error = %v", err)
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

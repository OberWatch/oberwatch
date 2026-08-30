package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/OberWatch/oberwatch/internal/alert"
	"github.com/OberWatch/oberwatch/internal/budget"
	"github.com/OberWatch/oberwatch/internal/config"
	"github.com/OberWatch/oberwatch/internal/pricing"
	oberproxy "github.com/OberWatch/oberwatch/internal/proxy"
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

func TestServer_BudgetUpdateTaskBudgetUSD(t *testing.T) {
	t.Parallel()

	server, _, store := newTestServer(t)

	putBody := `{"limit_usd":10,"period":"daily","action_on_exceed":"alert","task_budget_usd":5}`
	putReq := httptest.NewRequest(http.MethodPut, basePath+"/budgets/email-agent", strings.NewReader(putBody))
	addAuthenticatedSessionCookie(t, store, putReq)
	putReq.Header.Set("Content-Type", "application/json")
	putRecorder := httptest.NewRecorder()
	server.ServeHTTP(putRecorder, putReq)
	if putRecorder.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d, body = %s", putRecorder.Code, http.StatusOK, putRecorder.Body.String())
	}
	putPayload := decodeJSONMap(t, putRecorder.Result().Body)
	if got := mustFloat(t, putPayload, "task_budget_usd"); got != 5 {
		t.Fatalf("PUT response task_budget_usd = %v, want 5", got)
	}

	getReq := httptest.NewRequest(http.MethodGet, basePath+"/budgets/email-agent", nil)
	addAuthenticatedSessionCookie(t, store, getReq)
	getRecorder := httptest.NewRecorder()
	server.ServeHTTP(getRecorder, getReq)
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", getRecorder.Code, http.StatusOK)
	}
	getPayload := decodeJSONMap(t, getRecorder.Result().Body)
	if got := mustFloat(t, getPayload, "task_budget_usd"); got != 5 {
		t.Fatalf("GET response task_budget_usd = %v, want 5", got)
	}

	// Task budget must survive a restart: rebuild the manager and server
	// against the same SQLite file, the way a process restart would.
	cfg := config.DefaultConfig()
	restartedManager, err := budget.NewPersistentManager(cfg.Gate, nil, store)
	if err != nil {
		t.Fatalf("NewPersistentManager() error = %v", err)
	}
	t.Cleanup(func() { _ = restartedManager.Close() })
	restartedServer := New(cfg, restartedManager, store, "0.1.0")

	afterRestartReq := httptest.NewRequest(http.MethodGet, basePath+"/budgets/email-agent", nil)
	addAuthenticatedSessionCookie(t, store, afterRestartReq)
	afterRestartRecorder := httptest.NewRecorder()
	restartedServer.ServeHTTP(afterRestartRecorder, afterRestartReq)
	if afterRestartRecorder.Code != http.StatusOK {
		t.Fatalf("GET after restart status = %d, want %d", afterRestartRecorder.Code, http.StatusOK)
	}
	afterRestartPayload := decodeJSONMap(t, afterRestartRecorder.Result().Body)
	if got := mustFloat(t, afterRestartPayload, "task_budget_usd"); got != 5 {
		t.Fatalf("task_budget_usd after restart = %v, want 5", got)
	}

	// A zero task_budget_usd explicitly means "inherit the gate default".
	zeroPutBody := `{"limit_usd":10,"period":"daily","action_on_exceed":"alert","task_budget_usd":0}`
	zeroPutReq := httptest.NewRequest(http.MethodPut, basePath+"/budgets/email-agent", strings.NewReader(zeroPutBody))
	addAuthenticatedSessionCookie(t, store, zeroPutReq)
	zeroPutReq.Header.Set("Content-Type", "application/json")
	zeroPutRecorder := httptest.NewRecorder()
	server.ServeHTTP(zeroPutRecorder, zeroPutReq)
	if zeroPutRecorder.Code != http.StatusOK {
		t.Fatalf("PUT (zero) status = %d, want %d, body = %s", zeroPutRecorder.Code, http.StatusOK, zeroPutRecorder.Body.String())
	}
	zeroPutPayload := decodeJSONMap(t, zeroPutRecorder.Result().Body)
	if got := mustFloat(t, zeroPutPayload, "task_budget_usd"); got != 0 {
		t.Fatalf("PUT (zero) response task_budget_usd = %v, want 0", got)
	}

	// A negative task_budget_usd must be rejected like any other invalid budget.
	negativeBody := `{"limit_usd":10,"period":"daily","action_on_exceed":"alert","task_budget_usd":-1}`
	negativeReq := httptest.NewRequest(http.MethodPut, basePath+"/budgets/email-agent", strings.NewReader(negativeBody))
	addAuthenticatedSessionCookie(t, store, negativeReq)
	negativeReq.Header.Set("Content-Type", "application/json")
	negativeRecorder := httptest.NewRecorder()
	server.ServeHTTP(negativeRecorder, negativeReq)
	if negativeRecorder.Code != http.StatusBadRequest {
		t.Fatalf("PUT (negative) status = %d, want %d", negativeRecorder.Code, http.StatusBadRequest)
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
				{Agent: "agent-a", Model: "gpt-4o", Provider: "openai", InputTokens: 100, OutputTokens: 50, CostUSD: 0.40, CreatedAt: now.Add(-2 * time.Hour)},
				{Agent: "agent-a", Model: "gpt-4o", Provider: "openai", InputTokens: 100, OutputTokens: 50, CostUSD: 0.50, CreatedAt: now.Add(2 * time.Hour)},
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

func TestServer_CostQueryValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
	}{
		{name: "unsupported grouping is a client error", path: basePath + "/costs?group_by=provider"},
		{name: "calendar-ambiguous agent_day is a client error", path: basePath + "/costs?group_by=agent_day"},
		{name: "calendar-ambiguous agent_day is a client error for export", path: basePath + "/costs/export?group_by=agent_day"},
		{name: "unsupported grouping is a client error for export", path: basePath + "/costs/export?group_by=provider"},
		{name: "reversed range is a client error for costs", path: basePath + "/costs?from=2026-03-27T00:00:00Z&to=2026-03-26T00:00:00Z"},
		{name: "reversed range is a client error for export", path: basePath + "/costs/export?from=2026-03-27T00:00:00Z&to=2026-03-26T00:00:00Z"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, _, store := newTestServer(t)
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			addAuthenticatedSessionCookie(t, store, req)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestParseCostQuery_GroupByCompatibility(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		groupBy     string
		wantGroupBy string
	}{
		{name: "mixed case agent is normalized", groupBy: "%20Agent%20", wantGroupBy: "agent"},
		{name: "uppercase agent is normalized", groupBy: "AGENT", wantGroupBy: "agent"},
		{name: "legacy none remains supported", groupBy: "none", wantGroupBy: "none"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, basePath+"/costs?group_by="+tt.groupBy, nil)
			query, err := parseCostQuery(req)
			if err != nil {
				t.Fatalf("parseCostQuery() error = %v", err)
			}
			if query.GroupBy != tt.wantGroupBy {
				t.Fatalf("GroupBy = %q, want %q", query.GroupBy, tt.wantGroupBy)
			}
		})
	}
}

func TestServer_CostExportContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "exports grouped agents with persisted or ambiguous providers",
			path: basePath + "/costs/export?group_by=agent",
			want: "agent,model,provider,requests,input_tokens,output_tokens,cost_usd\n" +
				"agent-a,,,2,30,15,0.30000000\n" +
				"agent-b,,openai,1,30,15,0.30000000\n",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, _, store := newTestServer(t)
			seedCostRecords(t, store, []storage.CostRecord{
				{Agent: "agent-a", Model: "shared", Provider: "openai", InputTokens: 10, OutputTokens: 5, CostUSD: 0.1, CreatedAt: time.Now().UTC()},
				{Agent: "agent-a", Model: "shared", Provider: "anthropic", InputTokens: 20, OutputTokens: 10, CostUSD: 0.2, CreatedAt: time.Now().UTC()},
				{Agent: "agent-b", Model: "unique", Provider: "openai", InputTokens: 30, OutputTokens: 15, CostUSD: 0.3, CreatedAt: time.Now().UTC()},
			})
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			addAuthenticatedSessionCookie(t, store, req)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)

			response := recorder.Result()
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status code = %d, want %d", response.StatusCode, http.StatusOK)
			}
			if response.Header.Get("Content-Type") != "text/csv" {
				t.Fatalf("Content-Type = %q, want text/csv", response.Header.Get("Content-Type"))
			}
			if response.Header.Get("Content-Disposition") != `attachment; filename="costs.csv"` {
				t.Fatalf("Content-Disposition = %q, want attachment", response.Header.Get("Content-Disposition"))
			}
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}
			if string(body) != tt.want {
				t.Fatalf("CSV body = %q, want %q", string(body), tt.want)
			}
		})
	}
}

func TestServer_CostExportFiltering(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "export excludes matching records before and after time range",
			path: basePath + "/costs/export?agent=agent-a&model=gpt-4o&group_by=none&from=" +
				base.Add(-time.Hour).Format(time.RFC3339) + "&to=" + base.Add(time.Hour).Format(time.RFC3339),
			want: "agent,model,provider,requests,input_tokens,output_tokens,cost_usd\n" +
				"agent-a,gpt-4o,openai,1,10,5,0.20000000\n",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, _, store := newTestServer(t)
			seedCostRecords(t, store, []storage.CostRecord{
				{Agent: "agent-a", Model: "gpt-4o", Provider: "openai", InputTokens: 10, OutputTokens: 5, CostUSD: 0.1, CreatedAt: base.Add(-2 * time.Hour)},
				{Agent: "agent-a", Model: "gpt-4o", Provider: "openai", InputTokens: 10, OutputTokens: 5, CostUSD: 0.2, CreatedAt: base},
				{Agent: "agent-a", Model: "gpt-4o", Provider: "openai", InputTokens: 10, OutputTokens: 5, CostUSD: 0.3, CreatedAt: base.Add(2 * time.Hour)},
			})

			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			addAuthenticatedSessionCookie(t, store, req)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusOK)
			}
			if recorder.Body.String() != tt.want {
				t.Fatalf("CSV body = %q, want %q", recorder.Body.String(), tt.want)
			}
		})
	}
}

func TestServer_ProxyCostPersistenceAndExportIntegration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "real proxy requests retain agent and provider attribution through filters and export"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			openAIUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, err := w.Write([]byte(`{"id":"openai-response","usage":{"prompt_tokens":100,"completion_tokens":50}}`))
				if err != nil {
					t.Errorf("openai upstream Write() error = %v", err)
				}
			}))
			t.Cleanup(openAIUpstream.Close)
			anthropicUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, err := w.Write([]byte(`{"id":"anthropic-response","usage":{"input_tokens":20,"output_tokens":10}}`))
				if err != nil {
					t.Errorf("anthropic upstream Write() error = %v", err)
				}
			}))
			t.Cleanup(anthropicUpstream.Close)

			cfg := config.DefaultConfig()
			cfg.Upstream.OpenAI.BaseURL = openAIUpstream.URL
			cfg.Upstream.Anthropic.BaseURL = anthropicUpstream.URL
			cfg.Upstream.DefaultProvider = config.ProviderOpenAI

			store, storeErr := storage.NewSQLiteStore(filepath.Join(t.TempDir(), "proxy-cost-integration.db"), 0, nil)
			if storeErr != nil {
				t.Fatalf("NewSQLiteStore() error = %v", storeErr)
			}
			t.Cleanup(func() {
				if closeErr := store.Close(); closeErr != nil {
					t.Errorf("Close() error = %v", closeErr)
				}
			})
			manager, managerErr := budget.NewPersistentManager(cfg.Gate, nil, store)
			if managerErr != nil {
				t.Fatalf("NewPersistentManager() error = %v", managerErr)
			}
			t.Cleanup(func() {
				if closeErr := manager.Close(); closeErr != nil {
					t.Errorf("BudgetManager.Close() error = %v", closeErr)
				}
			})

			management := New(cfg, manager, store, "test")
			var sinkErr error
			costSink := sinkFunc(func(record storage.CostRecord) {
				if saveErr := store.SaveCostRecord(context.Background(), record); saveErr != nil {
					sinkErr = saveErr
				}
			})
			proxyHandler, proxyErr := oberproxy.New(cfg, oberproxy.Hooks{
				Budget:     manager,
				Pricing:    pricing.NewPricingTableFromConfig(cfg.Pricing, nil),
				CostSink:   costSink,
				Management: management,
			})
			if proxyErr != nil {
				t.Fatalf("proxy.New() error = %v", proxyErr)
			}
			proxyServer := httptest.NewServer(proxyHandler)
			t.Cleanup(proxyServer.Close)

			proxyRequests := []struct {
				name     string
				agent    string
				path     string
				body     string
				model    string
				provider string
			}{
				{name: "openai agent request", agent: "agent-openai", path: "/v1/chat/completions", body: `{"model":"gpt-4o","messages":[]}`, model: "gpt-4o", provider: "openai"},
				{name: "anthropic agent request", agent: "agent-anthropic", path: "/v1/messages", body: `{"model":"claude-haiku-4-5","messages":[]}`, model: "claude-haiku-4-5", provider: "anthropic"},
			}
			for _, requestCase := range proxyRequests {
				requestCase := requestCase
				t.Run(requestCase.name, func(t *testing.T) {
					req, err := http.NewRequest(http.MethodPost, proxyServer.URL+requestCase.path, strings.NewReader(requestCase.body))
					if err != nil {
						t.Fatalf("NewRequest() error = %v", err)
					}
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("X-Oberwatch-Agent", requestCase.agent)
					response, err := proxyServer.Client().Do(req)
					if err != nil {
						t.Fatalf("Do() error = %v", err)
					}
					body, err := io.ReadAll(response.Body)
					if closeErr := response.Body.Close(); closeErr != nil {
						t.Errorf("response Body.Close() error = %v", closeErr)
					}
					if err != nil {
						t.Fatalf("ReadAll() error = %v", err)
					}
					if response.StatusCode != http.StatusOK {
						t.Fatalf("status = %d, want %d; body = %s", response.StatusCode, http.StatusOK, body)
					}
				})
			}
			if sinkErr != nil {
				t.Fatalf("SaveCostRecord() error = %v", sinkErr)
			}

			token := seedSession(t, store, time.Now().UTC().Add(time.Hour))
			for _, requestCase := range proxyRequests {
				filterURL := proxyServer.URL + basePath + "/costs?agent=" + requestCase.agent + "&group_by=none"
				req, err := http.NewRequest(http.MethodGet, filterURL, nil)
				if err != nil {
					t.Fatalf("NewRequest(cost filter) error = %v", err)
				}
				req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
				response, err := proxyServer.Client().Do(req)
				if err != nil {
					t.Fatalf("Do(cost filter) error = %v", err)
				}
				payload := decodeJSONMap(t, response.Body)
				if closeErr := response.Body.Close(); closeErr != nil {
					t.Errorf("cost filter Body.Close() error = %v", closeErr)
				}
				if got := mustFloat(t, payload, "total_requests"); got != 1 {
					t.Fatalf("filtered total_requests for %s = %v, want 1", requestCase.agent, got)
				}
			}

			exportReq, err := http.NewRequest(http.MethodGet, proxyServer.URL+basePath+"/costs/export?group_by=none", nil)
			if err != nil {
				t.Fatalf("NewRequest(export) error = %v", err)
			}
			exportReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
			exportResponse, err := proxyServer.Client().Do(exportReq)
			if err != nil {
				t.Fatalf("Do(export) error = %v", err)
			}
			exportBody, err := io.ReadAll(exportResponse.Body)
			if closeErr := exportResponse.Body.Close(); closeErr != nil {
				t.Errorf("export Body.Close() error = %v", closeErr)
			}
			if err != nil {
				t.Fatalf("ReadAll(export) error = %v", err)
			}
			if exportResponse.StatusCode != http.StatusOK {
				t.Fatalf("export status = %d, want %d", exportResponse.StatusCode, http.StatusOK)
			}
			csvRows, err := csv.NewReader(strings.NewReader(string(exportBody))).ReadAll()
			if err != nil {
				t.Fatalf("CSV ReadAll() error = %v", err)
			}
			if len(csvRows) != len(proxyRequests)+1 {
				t.Fatalf("CSV row count = %d, want %d; CSV = %q", len(csvRows), len(proxyRequests)+1, exportBody)
			}
			attributionByAgent := make(map[string][]string, len(csvRows)-1)
			for _, row := range csvRows[1:] {
				if len(row) != 7 {
					t.Fatalf("CSV row = %#v, want 7 columns", row)
				}
				attributionByAgent[row[0]] = row
			}
			for _, requestCase := range proxyRequests {
				row, ok := attributionByAgent[requestCase.agent]
				if !ok || row[1] != requestCase.model || row[2] != requestCase.provider {
					t.Fatalf("CSV row for %q = %#v, want model %q and provider %q; CSV = %q", requestCase.agent, row, requestCase.model, requestCase.provider, exportBody)
				}
			}
		})
	}
}

func TestServer_EmptyCosts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
	}{
		{name: "default grouping returns numeric zeros and empty breakdown", path: basePath + "/costs?agent=missing"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, _, store := newTestServer(t)
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			addAuthenticatedSessionCookie(t, store, req)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusOK)
			}

			payload := decodeJSONMap(t, recorder.Result().Body)
			for _, key := range []string{"total_usd", "total_requests", "total_input_tokens", "total_output_tokens"} {
				if got := mustFloat(t, payload, key); got != 0 {
					t.Fatalf("%s = %v, want 0", key, got)
				}
			}
			breakdown, ok := payload["breakdown"].([]any)
			if !ok || len(breakdown) != 0 {
				t.Fatalf("breakdown = %#v, want empty array", payload["breakdown"])
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

func TestServer_AgentsEndpointReturnsModelUsage(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 25, 14, 0, 0, 0, time.UTC)
	tests := []struct {
		name           string
		renameTo       string
		wantName       string
		costs          []storage.CostRecord
		wantLastModel  string
		wantModelsUsed []string
	}{
		{
			name:           "configured agent without costs returns empty model values",
			wantName:       "email-agent",
			wantModelsUsed: []string{},
		},
		{
			name:     "cost history returns deterministic latest and distinct models",
			wantName: "email-agent",
			costs: []storage.CostRecord{
				{ID: "old", Agent: "email-agent", Model: "gpt-4o", Provider: "openai", CreatedAt: now.Add(-time.Minute)},
				{ID: "tie-a", Agent: "email-agent", Model: "gpt-4o", Provider: "openai", CreatedAt: now},
				{ID: "tie-z", Agent: "email-agent", Model: "claude-haiku-4-5", Provider: "anthropic", CreatedAt: now},
			},
			wantLastModel:  "claude-haiku-4-5",
			wantModelsUsed: []string{"claude-haiku-4-5", "gpt-4o"},
		},
		{
			name:     "renamed agent retains model history",
			renameTo: "renamed-agent",
			wantName: "renamed-agent",
			costs: []storage.CostRecord{
				{ID: "rename-cost", Agent: "email-agent", Model: "claude-sonnet-4-6", Provider: "anthropic", CreatedAt: now},
			},
			wantLastModel:  "claude-sonnet-4-6",
			wantModelsUsed: []string{"claude-sonnet-4-6"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, _, store := newTestServer(t)
			seedCostRecords(t, store, tt.costs)
			if tt.renameTo != "" {
				if err := store.RenameAgent(context.Background(), "email-agent", tt.renameTo); err != nil {
					t.Fatalf("RenameAgent() error = %v", err)
				}
			}
			req := httptest.NewRequest(http.MethodGet, basePath+"/agents", nil)
			addAuthenticatedSessionCookie(t, store, req)

			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusOK)
			}

			payload := decodeJSONMap(t, recorder.Result().Body)
			agents, ok := payload["agents"].([]any)
			if !ok || len(agents) != 1 {
				t.Fatalf("agents = %#v, want one agent", payload["agents"])
			}
			agent, ok := agents[0].(map[string]any)
			if !ok {
				t.Fatalf("agent type = %T, want map[string]any", agents[0])
			}
			if got := mustString(t, agent, "name"); got != tt.wantName {
				t.Fatalf("name = %q, want %q", got, tt.wantName)
			}
			if got := mustString(t, agent, "last_model"); got != tt.wantLastModel {
				t.Fatalf("last_model = %q, want %q", got, tt.wantLastModel)
			}
			models, ok := agent["models_used"].([]any)
			if !ok {
				t.Fatalf("models_used type = %T, want []any", agent["models_used"])
			}
			gotModels := make([]string, 0, len(models))
			for _, model := range models {
				value, ok := model.(string)
				if !ok {
					t.Fatalf("model type = %T, want string", model)
				}
				gotModels = append(gotModels, value)
			}
			if !reflect.DeepEqual(gotModels, tt.wantModelsUsed) {
				t.Fatalf("models_used = %#v, want %#v", gotModels, tt.wantModelsUsed)
			}
		})
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

// TestServer_AgentDeleteEndpoint_SucceedsForAnySQLiteAgent proves DELETE
// /agents/{name} deletes any agent that exists in SQLite and never tells the
// operator to edit a config file, regardless of how the agent was seeded.
func TestServer_AgentDeleteEndpoint_SucceedsForAnySQLiteAgent(t *testing.T) {
	t.Parallel()

	server, _, store := newTestServer(t)
	req := httptest.NewRequest(http.MethodDelete, basePath+"/agents/email-agent", nil)
	addAuthenticatedSessionCookie(t, store, req)

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d, body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if strings.Contains(strings.ToLower(recorder.Body.String()), "config") {
		t.Fatalf("response tells the operator to edit a config file: %s", recorder.Body.String())
	}
}

func newTestServer(t *testing.T) (*Server, *budget.BudgetManager, storage.Store) {
	t.Helper()

	cfg := config.DefaultConfig()

	dsn := filepath.Join(t.TempDir(), "oberwatch-api-test.db")
	sqliteStore, err := storage.NewSQLiteStore(dsn, 0, nil)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() {
		_ = sqliteStore.Close()
	})

	now := time.Now().UTC()
	if upsertErr := sqliteStore.UpsertAgent(context.Background(), storage.AgentRecord{
		Name:            "email-agent",
		Status:          "active",
		BudgetLimitUSD:  10,
		BudgetPeriod:    config.BudgetPeriodDaily,
		ActionOnExceed:  config.BudgetActionAlert,
		PeriodStartedAt: now,
		PeriodResetsAt:  now.Add(24 * time.Hour),
		FirstSeenAt:     now,
		LastSeenAt:      now,
	}); upsertErr != nil {
		t.Fatalf("UpsertAgent() error = %v", upsertErr)
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

func TestServer_CostStackedSeriesContract(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, time.March, 26, 10, 0, 0, 0, time.UTC)
	from := base.Add(-time.Hour).Format(time.RFC3339)
	to := base.Add(48 * time.Hour).Format(time.RFC3339)

	type wantPoint struct {
		agent    string
		bucket   string
		costUSD  float64
		requests float64
	}

	tests := []struct {
		name    string
		groupBy string
		want    []wantPoint
	}{
		{
			name:    "agent_hour breakdown keeps agent and hour bucket",
			groupBy: "agent_hour",
			want: []wantPoint{
				{agent: "agent-a", bucket: "2026-03-26T10:00:00Z", costUSD: 0.03, requests: 2},
				{agent: "agent-b", bucket: "2026-03-26T10:00:00Z", costUSD: 0.04, requests: 1},
				{agent: "agent-a", bucket: "2026-03-27T11:00:00Z", costUSD: 0.08, requests: 1},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, _, store := newTestServer(t)
			seedCostRecords(t, store, []storage.CostRecord{
				{Agent: "agent-a", Model: "gpt-4o", Provider: "openai", InputTokens: 10, OutputTokens: 5, CostUSD: 0.01, CreatedAt: base},
				{Agent: "agent-a", Model: "gpt-4o", Provider: "openai", InputTokens: 10, OutputTokens: 5, CostUSD: 0.02, CreatedAt: base.Add(20 * time.Minute)},
				{Agent: "agent-b", Model: "claude-sonnet-4-6", Provider: "anthropic", InputTokens: 20, OutputTokens: 8, CostUSD: 0.04, CreatedAt: base.Add(40 * time.Minute)},
				{Agent: "agent-a", Model: "gpt-4o", Provider: "openai", InputTokens: 15, OutputTokens: 6, CostUSD: 0.08, CreatedAt: base.Add(25 * time.Hour)},
			})

			path := basePath + "/costs?group_by=" + tt.groupBy + "&from=" + from + "&to=" + to
			req := httptest.NewRequest(http.MethodGet, path, nil)
			addAuthenticatedSessionCookie(t, store, req)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status code = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
			}

			payload := decodeJSONMap(t, recorder.Result().Body)
			rawBreakdown, ok := payload["breakdown"].([]any)
			if !ok {
				t.Fatalf("breakdown type = %T, want []any", payload["breakdown"])
			}
			if len(rawBreakdown) != len(tt.want) {
				t.Fatalf("len(breakdown) = %d, want %d; payload = %#v", len(rawBreakdown), len(tt.want), payload)
			}
			for index, want := range tt.want {
				row, ok := rawBreakdown[index].(map[string]any)
				if !ok {
					t.Fatalf("breakdown[%d] type = %T, want map[string]any", index, rawBreakdown[index])
				}
				if got := row["agent"]; got != want.agent {
					t.Errorf("breakdown[%d].agent = %v, want %q", index, got, want.agent)
				}
				if got := row["bucket"]; got != want.bucket {
					t.Errorf("breakdown[%d].bucket = %v, want %q", index, got, want.bucket)
				}
				if got := mustFloat(t, row, "cost_usd"); math.Abs(got-want.costUSD) > 1e-9 {
					t.Errorf("breakdown[%d].cost_usd = %v, want %v", index, got, want.costUSD)
				}
				if got := mustFloat(t, row, "requests"); got != want.requests {
					t.Errorf("breakdown[%d].requests = %v, want %v", index, got, want.requests)
				}
			}
		})
	}
}

func TestParseCostQuery_AgentBucketGroupingsAccepted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		groupBy     string
		wantGroupBy string
	}{
		{name: "agent_hour is accepted", groupBy: "agent_hour", wantGroupBy: "agent_hour"},
		{name: "agent_hour is case normalized", groupBy: "Agent_Hour", wantGroupBy: "agent_hour"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, basePath+"/costs?group_by="+tt.groupBy, nil)
			query, err := parseCostQuery(req)
			if err != nil {
				t.Fatalf("parseCostQuery() error = %v", err)
			}
			if query.GroupBy != tt.wantGroupBy {
				t.Fatalf("GroupBy = %q, want %q", query.GroupBy, tt.wantGroupBy)
			}
		})
	}
}

func TestServer_CostExportUsesSelectedRangeGrouping(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, time.March, 26, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "agent_hour export shares the costs range and grouping",
			path: basePath + "/costs/export?group_by=agent_hour&from=" +
				base.Add(-time.Hour).Format(time.RFC3339) + "&to=" + base.Add(time.Hour).Format(time.RFC3339),
			want: "agent,model,provider,requests,input_tokens,output_tokens,cost_usd\n" +
				"agent-a,,openai,2,20,10,0.03000000\n" +
				"agent-b,,anthropic,1,20,8,0.04000000\n",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, _, store := newTestServer(t)
			seedCostRecords(t, store, []storage.CostRecord{
				{Agent: "agent-a", Model: "gpt-4o", Provider: "openai", InputTokens: 10, OutputTokens: 5, CostUSD: 0.01, CreatedAt: base},
				{Agent: "agent-a", Model: "gpt-4o", Provider: "openai", InputTokens: 10, OutputTokens: 5, CostUSD: 0.02, CreatedAt: base.Add(20 * time.Minute)},
				{Agent: "agent-b", Model: "claude-sonnet-4-6", Provider: "anthropic", InputTokens: 20, OutputTokens: 8, CostUSD: 0.04, CreatedAt: base.Add(40 * time.Minute)},
				{Agent: "agent-a", Model: "gpt-4o", Provider: "openai", InputTokens: 15, OutputTokens: 6, CostUSD: 0.08, CreatedAt: base.Add(25 * time.Hour)},
			})

			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			addAuthenticatedSessionCookie(t, store, req)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)

			response := recorder.Result()
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status code = %d, want %d; body = %s", response.StatusCode, http.StatusOK, recorder.Body.String())
			}
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}
			if string(body) != tt.want {
				t.Fatalf("CSV body = %q, want %q", string(body), tt.want)
			}
		})
	}
}

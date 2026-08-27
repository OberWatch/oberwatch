package api

import (
	"context"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OberWatch/oberwatch/internal/budget"
	"github.com/OberWatch/oberwatch/internal/config"
	"github.com/OberWatch/oberwatch/internal/storage"
)

func settleTask(t *testing.T, manager *budget.BudgetManager, agent string, taskID string, cost float64) {
	t.Helper()
	decision, reservation := manager.ReserveTask(agent, taskID, cost)
	if !decision.Allowed {
		t.Fatalf("ReserveTask(%s) = %#v, want allowed", taskID, decision)
	}
	reservation.Settle(cost)
}

func TestServer_TaskEndpoints(t *testing.T) {
	t.Parallel()

	server, manager, store := newTestServer(t)
	settleTask(t, manager, "email-agent", "task-a", 0.25)
	settleTask(t, manager, "email-agent", "task-a", 0.25)
	settleTask(t, manager, "other-agent", "task-b", 0.10)

	t.Run("requires auth", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodGet, basePath+"/tasks", nil)
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", recorder.Code)
		}
	})

	t.Run("lists tasks", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodGet, basePath+"/tasks", nil)
		addAuthenticatedSessionCookie(t, store, req)
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
		}
		payload := decodeJSONMap(t, recorder.Body)
		tasks, ok := payload["tasks"].([]any)
		if !ok || len(tasks) != 2 {
			t.Fatalf("tasks = %#v, want two entries", payload["tasks"])
		}
		first, ok := tasks[0].(map[string]any)
		if !ok {
			t.Fatalf("tasks[0] type = %T", tasks[0])
		}
		mustHaveKeys(t, first, "task_id", "status", "last_agent", "limit_usd", "spent_usd", "reserved_usd", "remaining_usd", "percentage_used", "request_count", "in_flight", "first_seen_at", "last_seen_at")
		if first["task_id"] != "task-a" || first["spent_usd"] != 0.5 || first["request_count"] != 2.0 || first["last_agent"] != "email-agent" {
			t.Fatalf("tasks[0] = %#v, want task-a with spent 0.5 over 2 requests", first)
		}
	})

	t.Run("gets one task", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodGet, basePath+"/tasks/task-b", nil)
		addAuthenticatedSessionCookie(t, store, req)
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
		}
		payload := decodeJSONMap(t, recorder.Body)
		if payload["task_id"] != "task-b" || payload["spent_usd"] != 0.1 || payload["last_agent"] != "other-agent" {
			t.Fatalf("payload = %#v, want task-b", payload)
		}
	})

	t.Run("unknown task is 404", func(t *testing.T) {
		t.Parallel()
		for _, path := range []string{basePath + "/tasks/nope", basePath + "/tasks/nope/reset"} {
			method := http.MethodGet
			if strings.HasSuffix(path, "/reset") {
				method = http.MethodPost
			}
			req := httptest.NewRequest(method, path, nil)
			addAuthenticatedSessionCookie(t, store, req)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("%s %s status = %d, want 404", method, path, recorder.Code)
			}
			assertErrorCode(t, recorder.Body, "not_found")
		}
	})

	t.Run("wrong methods and unknown actions", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			method string
			path   string
			want   int
		}{
			{method: http.MethodPost, path: basePath + "/tasks", want: http.StatusMethodNotAllowed},
			{method: http.MethodDelete, path: basePath + "/tasks/task-a", want: http.StatusMethodNotAllowed},
			{method: http.MethodGet, path: basePath + "/tasks/task-a/reset", want: http.StatusMethodNotAllowed},
			{method: http.MethodPost, path: basePath + "/tasks/task-a/explode", want: http.StatusNotFound},
			{method: http.MethodGet, path: basePath + "/tasks/task-a/reset/extra", want: http.StatusNotFound},
			{method: http.MethodGet, path: basePath + "/tasks/", want: http.StatusNotFound},
		}
		for _, tc := range cases {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			addAuthenticatedSessionCookie(t, store, req)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)
			if recorder.Code != tc.want {
				t.Fatalf("%s %s status = %d, want %d", tc.method, tc.path, recorder.Code, tc.want)
			}
		}
	})
}

func TestServer_TaskResetPersists(t *testing.T) {
	t.Parallel()

	server, manager, store := newTestServer(t)
	settleTask(t, manager, "email-agent", "task-reset", 0.75)

	req := httptest.NewRequest(http.MethodPost, basePath+"/tasks/task-reset/reset", nil)
	addAuthenticatedSessionCookie(t, store, req)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	payload := decodeJSONMap(t, recorder.Body)
	if payload["task_id"] != "task-reset" || payload["spent_usd"] != 0.0 || payload["request_count"] != 0.0 || payload["status"] != "active" {
		t.Fatalf("payload = %#v, want zeroed task", payload)
	}

	record, found, err := store.GetTask(context.Background(), "task-reset")
	if err != nil || !found {
		t.Fatalf("GetTask() = %#v, %v, %v; want persisted record", record, found, err)
	}
	if record.SpentUSD != 0 || record.RequestCount != 0 {
		t.Fatalf("persisted record = %#v, want zero spend", record)
	}
}

func TestServer_CostsTaskFilter(t *testing.T) {
	t.Parallel()

	server, _, store := newTestServer(t)
	now := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	seedCostRecords(t, store, []storage.CostRecord{
		{Agent: "email-agent", Model: "gpt-5.6-terra", Provider: "openai", TaskID: "task-1", InputTokens: 10, OutputTokens: 1, CostUSD: 0.10, CreatedAt: now},
		{Agent: "other-agent", Model: "gpt-5.6-terra", Provider: "openai", TaskID: "task-1", InputTokens: 20, OutputTokens: 1, CostUSD: 0.20, CreatedAt: now.Add(time.Second)},
		{Agent: "email-agent", Model: "gpt-5.6-terra", Provider: "openai", TaskID: "task-10", InputTokens: 30, OutputTokens: 1, CostUSD: 0.40, CreatedAt: now.Add(2 * time.Second)},
		{Agent: "email-agent", Model: "gpt-5.6-terra", Provider: "openai", TaskID: "", InputTokens: 40, OutputTokens: 1, CostUSD: 0.80, CreatedAt: now.Add(3 * time.Second)},
	})

	tests := []struct {
		name         string
		query        string
		wantTotal    float64
		wantRequests float64
	}{
		{name: "exact task", query: "task=task-1", wantTotal: 0.30, wantRequests: 2},
		{name: "exact task with agent", query: "task=task-1&agent=other-agent", wantTotal: 0.20, wantRequests: 1},
		{name: "prefix does not match", query: "task=task-", wantTotal: 0, wantRequests: 0},
		{name: "wildcard is literal", query: "task=task-%25", wantTotal: 0, wantRequests: 0},
		{name: "trimmed", query: "task=%20task-10%20", wantTotal: 0.40, wantRequests: 1},
		{name: "no filter", query: "", wantTotal: 1.50, wantRequests: 4},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := basePath + "/costs?group_by=none"
			if tt.query != "" {
				path += "&" + tt.query
			}
			req := httptest.NewRequest(http.MethodGet, path, nil)
			addAuthenticatedSessionCookie(t, store, req)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
			}
			payload := decodeJSONMap(t, recorder.Body)
			total, _ := payload["total_usd"].(float64)
			if total < tt.wantTotal-1e-9 || total > tt.wantTotal+1e-9 {
				t.Fatalf("total_usd = %v, want %v", total, tt.wantTotal)
			}
			if payload["total_requests"] != tt.wantRequests {
				t.Fatalf("total_requests = %v, want %v", payload["total_requests"], tt.wantRequests)
			}
		})
	}

	t.Run("csv export applies the same filter", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodGet, basePath+"/costs/export?group_by=agent&task=task-1", nil)
		addAuthenticatedSessionCookie(t, store, req)
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
		}
		if got := recorder.Header().Get("Content-Type"); got != "text/csv" {
			t.Fatalf("Content-Type = %q, want text/csv", got)
		}
		rows, err := csv.NewReader(strings.NewReader(recorder.Body.String())).ReadAll()
		if err != nil {
			t.Fatalf("csv.ReadAll() error = %v", err)
		}
		if len(rows) != 3 {
			t.Fatalf("csv rows = %d, want header plus two agents: %q", len(rows), recorder.Body.String())
		}
		if strings.Join(rows[0], ",") != "agent,model,provider,requests,input_tokens,output_tokens,cost_usd" {
			t.Fatalf("csv header = %q, want unchanged contract", rows[0])
		}
		if rows[1][0] != "email-agent" || rows[1][6] != "0.10000000" || rows[2][0] != "other-agent" || rows[2][6] != "0.20000000" {
			t.Fatalf("csv rows = %q, want only task-1 spend", rows[1:])
		}
	})
}

func TestServer_TasksWithoutBudgetManager(t *testing.T) {
	t.Parallel()

	server := New(config.DefaultConfig(), nil, failingStore{}, "test")
	for _, path := range []string{basePath + "/tasks", basePath + "/tasks/task-a"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: testSessionToken})
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("%s status = %d, want 500", path, recorder.Code)
		}
		assertErrorCode(t, recorder.Body, "config_error")
	}
}

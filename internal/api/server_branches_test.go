package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OberWatch/oberwatch/internal/alert"
	"github.com/OberWatch/oberwatch/internal/budget"
	"github.com/OberWatch/oberwatch/internal/config"
	"github.com/OberWatch/oberwatch/internal/storage"
)

func TestServer_MethodNotAllowedResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		path   string
		auth   bool
	}{
		{name: "health post", method: http.MethodPost, path: basePath + "/health", auth: false},
		{name: "budgets post", method: http.MethodPost, path: basePath + "/budgets", auth: true},
		{name: "costs delete", method: http.MethodDelete, path: basePath + "/costs", auth: true},
		{name: "agents post", method: http.MethodPost, path: basePath + "/agents", auth: true},
		{name: "stream post", method: http.MethodPost, path: basePath + "/stream", auth: true},
		{name: "budget patch", method: http.MethodPatch, path: basePath + "/budgets/email-agent", auth: true},
		{name: "budget action get", method: http.MethodGet, path: basePath + "/budgets/email-agent/kill", auth: true},
		{name: "kill all get", method: http.MethodGet, path: basePath + "/kill-all", auth: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server, _, store := newTestServer(t)
			req := httptest.NewRequest(tt.method, tt.path, nil)
			if tt.auth {
				addAuthenticatedSessionCookie(t, store, req)
			}

			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
			}
			assertErrorCode(t, recorder.Body, "config_error")
		})
	}
}

func TestServer_ErrorResponses(t *testing.T) {
	t.Parallel()

	//nolint:govet // Keep table fields grouped by test setup and expected result.
	tests := []struct {
		name       string
		build      func(*testing.T) *Server
		method     string
		path       string
		body       string
		wantCode   string
		wantStatus int
	}{
		{
			name: "missing budget manager for budgets",
			build: func(t *testing.T) *Server {
				t.Helper()
				cfg := config.DefaultConfig()
				_, _, store := newTestServer(t)
				return New(cfg, nil, store, "0.1.0")
			},
			method:     http.MethodGet,
			path:       basePath + "/budgets",
			wantStatus: http.StatusInternalServerError,
			wantCode:   "config_error",
		},
		{
			name: "invalid budget update payload",
			build: func(t *testing.T) *Server {
				t.Helper()
				server, _, _ := newTestServer(t)
				return server
			},
			method:     http.MethodPut,
			path:       basePath + "/budgets/email-agent",
			body:       "{",
			wantStatus: http.StatusBadRequest,
			wantCode:   "config_error",
		},
		{
			name: "unknown budget action",
			build: func(t *testing.T) *Server {
				t.Helper()
				server, _, _ := newTestServer(t)
				return server
			},
			method:     http.MethodPost,
			path:       basePath + "/budgets/email-agent/noop",
			wantStatus: http.StatusNotFound,
			wantCode:   "config_error",
		},
		{
			name: "kill all without budget manager",
			build: func(t *testing.T) *Server {
				t.Helper()
				cfg := config.DefaultConfig()
				sqliteServer, _, store := newTestServer(t)
				_ = sqliteServer
				return New(cfg, nil, store, "0.1.0")
			},
			method:     http.MethodPost,
			path:       basePath + "/kill-all",
			wantStatus: http.StatusInternalServerError,
			wantCode:   "config_error",
		},
		{
			name: "costs with invalid from timestamp",
			build: func(t *testing.T) *Server {
				t.Helper()
				server, _, _ := newTestServer(t)
				return server
			},
			method:     http.MethodGet,
			path:       basePath + "/costs?from=not-a-time",
			wantStatus: http.StatusBadRequest,
			wantCode:   "config_error",
		},
		{
			name: "costs without store returns unauthorized",
			build: func(t *testing.T) *Server {
				t.Helper()
				cfg := config.DefaultConfig()
				manager := budget.NewManager(cfg.Gate, nil)
				return New(cfg, manager, nil, "0.1.0")
			},
			method:     http.MethodGet,
			path:       basePath + "/costs",
			wantStatus: http.StatusUnauthorized,
			wantCode:   "auth_required",
		},
		{
			name: "costs query store failure",
			build: func(t *testing.T) *Server {
				t.Helper()
				cfg := config.DefaultConfig()
				manager := budget.NewManager(cfg.Gate, nil)
				return New(cfg, manager, failingStore{queryCostsErr: errors.New("boom")}, "0.1.0")
			},
			method:     http.MethodGet,
			path:       basePath + "/costs",
			wantStatus: http.StatusInternalServerError,
			wantCode:   "config_error",
		},
		{
			name: "costs export invalid to timestamp",
			build: func(t *testing.T) *Server {
				t.Helper()
				server, _, _ := newTestServer(t)
				return server
			},
			method:     http.MethodGet,
			path:       basePath + "/costs/export?to=bad",
			wantStatus: http.StatusBadRequest,
			wantCode:   "config_error",
		},
		{
			name: "costs export store failure",
			build: func(t *testing.T) *Server {
				t.Helper()
				cfg := config.DefaultConfig()
				manager := budget.NewManager(cfg.Gate, nil)
				return New(cfg, manager, failingStore{queryCostsErr: errors.New("boom")}, "0.1.0")
			},
			method:     http.MethodGet,
			path:       basePath + "/costs/export",
			wantStatus: http.StatusInternalServerError,
			wantCode:   "config_error",
		},
		{
			name: "agents missing dependencies",
			build: func(t *testing.T) *Server {
				t.Helper()
				cfg := config.DefaultConfig()
				_, _, store := newTestServer(t)
				return New(cfg, nil, store, "0.1.0")
			},
			method:     http.MethodGet,
			path:       basePath + "/agents",
			wantStatus: http.StatusInternalServerError,
			wantCode:   "config_error",
		},
		{
			name: "unauthorized protected endpoint",
			build: func(t *testing.T) *Server {
				t.Helper()
				server, _, _ := newTestServer(t)
				return server
			},
			method:     http.MethodGet,
			path:       basePath + "/costs",
			wantStatus: http.StatusUnauthorized,
			wantCode:   "auth_required",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := tt.build(t)
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			if tt.wantCode != "auth_required" && server.store != nil {
				addAuthenticatedSessionCookie(t, server.store, req)
			}
			if tt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}

			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, req)

			if recorder.Code != tt.wantStatus {
				t.Fatalf("status code = %d, want %d", recorder.Code, tt.wantStatus)
			}
			assertErrorCode(t, recorder.Body, tt.wantCode)
		})
	}
}

func TestServer_WrapDelegatesAndPublishes(t *testing.T) {
	t.Parallel()

	//nolint:govet // Keep table fields grouped for readable subtest setup.
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{
			name: "cost sink delegates and publishes",
			run: func(t *testing.T) {
				t.Helper()

				server, manager, _ := newTestServer(t)
				events := server.subscribe()
				t.Cleanup(func() { server.unsubscribe(events) })

				called := false
				sink := server.WrapCostSink(testCostSink(func(record storage.CostRecord) {
					called = true
				}))

				manager.RecordSpend("email-agent", 0.50)
				sink.Enqueue(storage.CostRecord{Agent: "email-agent", CostUSD: 0.50})

				if !called {
					t.Fatal("wrapped sink did not delegate to next sink")
				}

				select {
				case event := <-events:
					if event.name != "cost_update" {
						t.Fatalf("event.name = %q, want cost_update", event.name)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("timed out waiting for cost event")
				}
			},
		},
		{
			name: "dispatcher delegates and publishes",
			run: func(t *testing.T) {
				t.Helper()

				server, _, _ := newTestServer(t)
				events := server.subscribe()
				t.Cleanup(func() { server.unsubscribe(events) })

				called := false
				dispatcher := server.WrapDispatcher(testDispatcher(func(_ context.Context, entry alert.Alert) {
					called = true
					if entry.Type != alert.TypeAgentKilled {
						t.Fatalf("entry.Type = %q, want %q", entry.Type, alert.TypeAgentKilled)
					}
				}))

				dispatcher.Dispatch(context.Background(), alert.NewAgentKilledAlert("email-agent", "manual_kill"))

				if !called {
					t.Fatal("wrapped dispatcher did not delegate to next dispatcher")
				}

				select {
				case event := <-events:
					if event.name != "agent_killed" {
						t.Fatalf("event.name = %q, want agent_killed", event.name)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("timed out waiting for alert event")
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.run(t)
		})
	}
}

func TestServer_StreamUnsupportedWriterAndHelpers(t *testing.T) {
	t.Parallel()

	//nolint:govet // Keep table fields grouped for readable subtest setup.
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{
			name: "stream handler returns config error without flusher",
			run: func(t *testing.T) {
				t.Helper()

				server, _, _ := newTestServer(t)
				req := httptest.NewRequest(http.MethodGet, basePath+"/stream", nil)
				writer := &noFlushWriter{header: make(http.Header)}

				server.handleStream(writer, req)

				if writer.statusCode != http.StatusInternalServerError {
					t.Fatalf("status code = %d, want %d", writer.statusCode, http.StatusInternalServerError)
				}
				assertErrorCode(t, strings.NewReader(writer.body.String()), "config_error")
			},
		},
		{
			name: "writeJSON marshal failure falls back to config error",
			run: func(t *testing.T) {
				t.Helper()

				recorder := httptest.NewRecorder()
				writeJSON(recorder, http.StatusOK, map[string]any{"bad": make(chan int)})

				if recorder.Code != http.StatusInternalServerError {
					t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusInternalServerError)
				}
				assertErrorCode(t, recorder.Body, "config_error")
			},
		},
		{
			name: "provider status helper",
			run: func(t *testing.T) {
				t.Helper()

				if got := providerStatus(""); got != "unreachable" {
					t.Fatalf("providerStatus(\"\") = %q, want %q", got, "unreachable")
				}
				if got := providerStatus("https://api.example.com"); got != "reachable" {
					t.Fatalf("providerStatus(non-empty) = %q, want %q", got, "reachable")
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.run(t)
		})
	}
}

type noFlushWriter struct {
	header     http.Header
	body       strings.Builder
	statusCode int
}

func (w *noFlushWriter) Header() http.Header {
	return w.header
}

func (w *noFlushWriter) Write(bytes []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	return w.body.Write(bytes)
}

func (w *noFlushWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
}

type testCostSink func(storage.CostRecord)

func (f testCostSink) Enqueue(record storage.CostRecord) {
	f(record)
}

type testDispatcher func(context.Context, alert.Alert)

func (f testDispatcher) Dispatch(ctx context.Context, entry alert.Alert) {
	f(ctx, entry)
}

type failingStore struct {
	queryCostsErr error
}

func (f failingStore) SaveCostRecord(context.Context, storage.CostRecord) error {
	return nil
}

func (f failingStore) QueryCosts(context.Context, storage.CostQuery) ([]storage.CostAggregate, error) {
	return nil, f.queryCostsErr
}

func (f failingStore) SaveAlert(context.Context, alert.Alert) error {
	return nil
}

func (f failingStore) QueryAlerts(context.Context, storage.AlertQuery) ([]alert.Alert, error) {
	return nil, nil
}

func (f failingStore) SaveBudgetSnapshot(context.Context, storage.BudgetSnapshot) error {
	return nil
}

func (f failingStore) LoadBudgetSnapshots(context.Context) ([]storage.BudgetSnapshot, error) {
	return nil, nil
}

func (f failingStore) GetSetting(_ context.Context, key string) (string, bool, error) {
	switch key {
	case sessionTokenKey:
		return testSessionToken, true, nil
	case sessionExpiresAtKey:
		return time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339Nano), true, nil
	case setupCompleteKey:
		return "true", true, nil
	default:
		return "", false, nil
	}
}

func (f failingStore) SetSetting(context.Context, string, string) error {
	return nil
}

func (f failingStore) DeleteSetting(context.Context, string) error {
	return nil
}

func assertErrorCode(t *testing.T, body io.Reader, wantCode string) {
	t.Helper()

	payload := decodeJSONMap(t, body)
	errorValue, ok := payload["error"]
	if !ok {
		t.Fatalf("payload missing error field: %#v", payload)
	}
	errorMap, ok := errorValue.(map[string]any)
	if !ok {
		t.Fatalf("payload error field type = %T, want map[string]any", errorValue)
	}
	code, ok := errorMap["code"].(string)
	if !ok {
		t.Fatalf("payload error.code type = %T, want string", errorMap["code"])
	}
	if code != wantCode {
		t.Fatalf("error code = %q, want %q", code, wantCode)
	}
}

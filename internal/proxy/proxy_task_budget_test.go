package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/OberWatch/oberwatch/internal/budget"
	"github.com/OberWatch/oberwatch/internal/pricing"
	"github.com/OberWatch/oberwatch/internal/storage"
)

// One million prompt tokens on gpt-5.6-terra costs $2.00 with the default catalog.
const millionTokenUsageResponse = `{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1000000,"completion_tokens":0}}`

type recordingSink struct {
	records []storage.CostRecord
	mu      sync.Mutex
}

func (s *recordingSink) Enqueue(record storage.CostRecord) {
	s.mu.Lock()
	s.records = append(s.records, record)
	s.mu.Unlock()
}

func (s *recordingSink) snapshot() []storage.CostRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]storage.CostRecord(nil), s.records...)
}

type taskProxyFixture struct {
	server       *httptestServer
	manager      *budget.BudgetManager
	sink         *recordingSink
	upstreamHits *int32
	seenTaskHdr  *atomic.Value
}

type httptestServer struct {
	client *http.Client
	URL    string
}

func newTaskProxyFixture(t *testing.T, taskLimit float64, upstream http.HandlerFunc) taskProxyFixture {
	t.Helper()

	var upstreamHits int32
	var seenTaskHeader atomic.Value
	seenTaskHeader.Store("unset")

	openAIServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamHits, 1)
		if _, present := r.Header["X-Oberwatch-Task"]; present {
			seenTaskHeader.Store(r.Header.Get("X-Oberwatch-Task"))
		} else {
			seenTaskHeader.Store("")
		}
		upstream(w, r)
	}))
	t.Cleanup(openAIServer.Close)

	anthropicServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(anthropicServer.Close)

	cfg := testConfig(openAIServer.URL, anthropicServer.URL)
	cfg.Gate.TaskBudgetUSD = taskLimit
	manager := budget.NewManager(cfg.Gate, nil)
	sink := &recordingSink{}

	proxyServer, err := New(cfg, Hooks{
		Budget:   manager,
		Pricing:  pricing.NewPricingTableFromConfig(cfg.Pricing, nil),
		CostSink: sink,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server := newTestServer(t, proxyServer)
	t.Cleanup(server.Close)

	return taskProxyFixture{
		server:       &httptestServer{URL: server.URL, client: server.Client()},
		manager:      manager,
		sink:         sink,
		upstreamHits: &upstreamHits,
		seenTaskHdr:  &seenTaskHeader,
	}
}

func (f taskProxyFixture) post(t *testing.T, taskHeader *string) (*http.Response, []byte) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, f.server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.6-terra","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("X-Oberwatch-Agent", "task-agent")
	if taskHeader != nil {
		req.Header.Set("X-Oberwatch-Task", *taskHeader)
	}

	resp, err := f.server.client.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	return resp, body
}

func strPtr(value string) *string {
	return &value
}

func okUpstream(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(millionTokenUsageResponse))
}

func TestProxy_TaskHeaderStrippedAndSpendSettled(t *testing.T) {
	t.Parallel()

	fixture := newTaskProxyFixture(t, 5, okUpstream)

	resp, _ := fixture.post(t, strPtr("  task-1  "))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := fixture.seenTaskHdr.Load(); got != "" {
		t.Fatalf("upstream saw X-Oberwatch-Task = %q, want header stripped", got)
	}

	view, found := fixture.manager.GetTask("task-1")
	if !found {
		t.Fatalf("GetTask(task-1) found = false; tasks = %#v", fixture.manager.ListTasks())
	}
	if view.SpentUSD < 1.999 || view.SpentUSD > 2.001 || view.ReservedUSD != 0 || view.InFlight != 0 || view.RequestCount != 1 {
		t.Fatalf("task view = %#v, want $2 settled and no reservation left", view)
	}
	if view.LastAgent != "task-agent" || view.LimitUSD != 5 {
		t.Fatalf("task view = %#v, want agent and limit recorded", view)
	}

	records := fixture.sink.snapshot()
	if len(records) != 1 || records[0].TaskID != "task-1" {
		t.Fatalf("cost records = %#v, want one record tagged task-1", records)
	}
}

func TestProxy_BlankTaskHeaderIsNotBudgetedOrShared(t *testing.T) {
	t.Parallel()

	// Cap far below a single request: without a task ID the cap must not apply.
	fixture := newTaskProxyFixture(t, 0.01, okUpstream)

	for _, header := range []*string{nil, strPtr(""), strPtr("   ")} {
		resp, body := fixture.post(t, header)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200 for blank task header: %s", resp.StatusCode, body)
		}
	}
	if got := atomic.LoadInt32(fixture.upstreamHits); got != 3 {
		t.Fatalf("upstream hits = %d, want 3", got)
	}
	if tasks := fixture.manager.ListTasks(); len(tasks) != 0 {
		t.Fatalf("ListTasks() = %#v, want no task bucket for blank IDs", tasks)
	}
	for _, record := range fixture.sink.snapshot() {
		if record.TaskID != "" {
			t.Fatalf("cost record task id = %q, want empty", record.TaskID)
		}
	}
}

func TestProxy_TaskCapRejectsBeforeUpstreamWithStructuredError(t *testing.T) {
	t.Parallel()

	fixture := newTaskProxyFixture(t, 3, okUpstream)

	resp, _ := fixture.post(t, strPtr("task-1"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first status = %d, want 200", resp.StatusCode)
	}
	if got := atomic.LoadInt32(fixture.upstreamHits); got != 1 {
		t.Fatalf("upstream hits after first request = %d, want 1", got)
	}

	// Spent is now $2 of a $3 cap. The projected cost of a tiny request still
	// fits, so a request whose estimate would exceed the cap must be simulated
	// by pushing settled spend past the cap.
	_, reservation := fixture.manager.ReserveTask("task-agent", "task-1", 0)
	reservation.Settle(1)

	resp, body := fixture.post(t, strPtr("task-1"))
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429: %s", resp.StatusCode, body)
	}
	if got := atomic.LoadInt32(fixture.upstreamHits); got != 1 {
		t.Fatalf("upstream hits after rejection = %d, want 1 (rejected before upstream)", got)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}

	var payload struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v: %s", err, body)
	}
	if payload.Error["code"] != budget.TaskBudgetExceededCode {
		t.Fatalf("error.code = %v, want %s", payload.Error["code"], budget.TaskBudgetExceededCode)
	}
	if payload.Error["task_id"] != "task-1" || payload.Error["agent"] != "task-agent" {
		t.Fatalf("error identity = %#v, want task-1 / task-agent", payload.Error)
	}
	for _, key := range []string{"message", "task_budget_limit_usd", "task_budget_spent_usd", "task_budget_reserved_usd", "task_budget_projected_usd"} {
		if _, ok := payload.Error[key]; !ok {
			t.Fatalf("error payload missing %q: %#v", key, payload.Error)
		}
	}
	if payload.Error["task_budget_limit_usd"] != 3.0 {
		t.Fatalf("task_budget_limit_usd = %v, want 3", payload.Error["task_budget_limit_usd"])
	}
	spent, _ := payload.Error["task_budget_spent_usd"].(float64)
	projected, _ := payload.Error["task_budget_projected_usd"].(float64)
	if spent < 2.999 || spent > 3.001 || projected <= spent {
		t.Fatalf("spent/projected = %v/%v, want spent 3 and projected above it", spent, projected)
	}

	// The rejection must not leak a reservation or count as a request.
	view, _ := fixture.manager.GetTask("task-1")
	if view.ReservedUSD != 0 || view.InFlight != 0 || view.RequestCount != 2 {
		t.Fatalf("task view after rejection = %#v, want no reservation and 2 billed requests", view)
	}
	if records := fixture.sink.snapshot(); len(records) != 1 {
		t.Fatalf("cost records = %d, want 1 (rejected request is not billed)", len(records))
	}

	// Other tasks for the same agent are unaffected.
	resp, body = fixture.post(t, strPtr("task-2"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("task-2 status = %d, want 200: %s", resp.StatusCode, body)
	}
}

func TestProxy_UpstreamFailureReleasesTaskReservation(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test case fields ordered for readability.
	tests := []struct {
		name       string
		upstream   http.HandlerFunc
		wantStatus int
	}{
		{
			name: "upstream error status",
			upstream: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"boom"}`))
			},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "upstream success without usage",
			upstream: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"choices":[]}`))
			},
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fixture := newTaskProxyFixture(t, 1, tt.upstream)
			resp, body := fixture.post(t, strPtr("task-1"))
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", resp.StatusCode, tt.wantStatus, body)
			}

			view, found := fixture.manager.GetTask("task-1")
			if !found {
				t.Fatal("GetTask(task-1) found = false, want task bucket created")
			}
			if view.ReservedUSD != 0 || view.InFlight != 0 {
				t.Fatalf("task view = %#v, want reservation released", view)
			}
			if tt.wantStatus >= http.StatusBadRequest && (view.SpentUSD != 0 || view.RequestCount != 0) {
				t.Fatalf("task view = %#v, want failed request not billed", view)
			}

			// Headroom is fully available again.
			decision, reservation := fixture.manager.ReserveTask("task-agent", "task-1", 1-view.SpentUSD)
			if !decision.Allowed {
				t.Fatalf("ReserveTask(full headroom) = %#v, want allowed", decision)
			}
			reservation.Release()
		})
	}
}

func TestProxy_UnreachableUpstreamReleasesTaskReservation(t *testing.T) {
	t.Parallel()

	deadUpstream := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	deadURL := deadUpstream.URL
	deadUpstream.Close()

	anthropicServer := newTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(anthropicServer.Close)

	cfg := testConfig(deadURL, anthropicServer.URL)
	cfg.Gate.TaskBudgetUSD = 1
	manager := budget.NewManager(cfg.Gate, nil)
	proxyServer, err := New(cfg, Hooks{Budget: manager, Pricing: pricing.NewPricingTableFromConfig(cfg.Pricing, nil)})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server := newTestServer(t, proxyServer)
	t.Cleanup(server.Close)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.6-terra","messages":[]}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("X-Oberwatch-Agent", "task-agent")
	req.Header.Set("X-Oberwatch-Task", "task-dead")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}

	view, found := manager.GetTask("task-dead")
	if !found || view.ReservedUSD != 0 || view.InFlight != 0 || view.SpentUSD != 0 {
		t.Fatalf("task view = %#v (found=%v), want released reservation and no spend", view, found)
	}
}

func TestProxy_TaskBudgetDisabledWithZeroLimit(t *testing.T) {
	t.Parallel()

	fixture := newTaskProxyFixture(t, 0, okUpstream)
	for i := 0; i < 3; i++ {
		resp, body := fixture.post(t, strPtr("task-1"))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200: %s", i, resp.StatusCode, body)
		}
	}
	view, found := fixture.manager.GetTask("task-1")
	if !found {
		t.Fatal("GetTask(task-1) found = false, want reporting even when unenforced")
	}
	if view.RequestCount != 3 || view.SpentUSD < 5.999 || view.LimitUSD != 0 || view.Status != "active" {
		t.Fatalf("task view = %#v, want 3 requests totalling $6 with no cap", view)
	}
}

func TestWriteTaskBudgetError_Defaults(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	writeTaskBudgetError(recorder, budget.TaskDecision{TaskID: "task-x", LimitUSD: 2})
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", recorder.Code)
	}
	var payload map[string]map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if payload["error"]["code"] != budget.TaskBudgetExceededCode {
		t.Fatalf("code = %v, want default %s", payload["error"]["code"], budget.TaskBudgetExceededCode)
	}
	if message, _ := payload["error"]["message"].(string); !strings.Contains(message, "task-x") {
		t.Fatalf("message = %q, want default mentioning task-x", message)
	}
}

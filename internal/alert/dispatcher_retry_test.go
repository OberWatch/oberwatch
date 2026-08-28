package alert

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OberWatch/oberwatch/internal/config"
)

// fakeClock fires every After immediately and records the requested delays.
//
//nolint:govet // keep fields grouped for readability.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	delays []time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	c.delays = append(c.delays, d)
	fired := c.now
	c.mu.Unlock()
	ch := make(chan time.Time, 1)
	ch <- fired
	return ch
}

func (c *fakeClock) Delays() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.delays...)
}

// scriptedResponse describes what the mock endpoint returns for one attempt.
//
//nolint:govet // keep fields grouped for readability.
type scriptedResponse struct {
	status    int
	body      string
	transport error
	hang      bool
}

// scriptedEndpoint is an httptest handler that answers attempts in order and
// repeats the last script entry once the script is exhausted.
//
//nolint:govet // keep fields grouped for readability.
type scriptedEndpoint struct {
	mu     sync.Mutex
	script []scriptedResponse
	calls  atomic.Int32
}

func (e *scriptedEndpoint) next() scriptedResponse {
	index := int(e.calls.Add(1)) - 1
	e.mu.Lock()
	defer e.mu.Unlock()
	if index >= len(e.script) {
		index = len(e.script) - 1
	}
	return e.script[index]
}

func (e *scriptedEndpoint) roundTrip(request *http.Request) (*http.Response, error) {
	step := e.next()
	if step.hang {
		<-request.Context().Done()
		return nil, request.Context().Err()
	}
	if step.transport != nil {
		return nil, step.transport
	}
	return responseWithStatus(step.status, step.body), nil
}

const secretToken = "T0SECRET/B0SECRET/xoxbVERYSECRETTOKEN"

func TestAlertDispatcher_RetryPolicy(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test table fields explicit.
	tests := []struct {
		name           string
		cfg            config.AlertsConfig
		script         []scriptedResponse
		attemptTimeout time.Duration
		backoffBase    time.Duration
		wantCalls      int32
		wantDelays     []time.Duration
		wantDelivered  uint64
		wantFailed     uint64
		wantRetries    uint64
		wantLogSubstr  string
	}{
		{
			name:          "500 then 500 then 200 succeeds on third attempt",
			cfg:           config.AlertsConfig{WebhookURL: "https://alerts.example/hook/" + secretToken},
			script:        []scriptedResponse{{status: 500, body: "boom"}, {status: 500, body: "boom"}, {status: 200, body: "ok"}},
			backoffBase:   100 * time.Millisecond,
			wantCalls:     3,
			wantDelays:    []time.Duration{100 * time.Millisecond, 200 * time.Millisecond},
			wantDelivered: 1,
			wantRetries:   2,
		},
		{
			name:          "400 is permanent and never retried",
			cfg:           config.AlertsConfig{WebhookURL: "https://alerts.example/hook/" + secretToken},
			script:        []scriptedResponse{{status: 400, body: "bad request"}},
			backoffBase:   100 * time.Millisecond,
			wantCalls:     1,
			wantDelays:    nil,
			wantFailed:    1,
			wantLogSubstr: "returned status 400",
		},
		{
			name:          "404 is permanent and never retried",
			cfg:           config.AlertsConfig{SlackWebhookURL: "https://hooks.slack.com/services/" + secretToken},
			script:        []scriptedResponse{{status: 404, body: "no_service"}},
			backoffBase:   100 * time.Millisecond,
			wantCalls:     1,
			wantFailed:    1,
			wantLogSubstr: "no_service",
		},
		{
			name:          "429 retries up to the limit then fails",
			cfg:           config.AlertsConfig{WebhookURL: "https://alerts.example/hook/" + secretToken},
			script:        []scriptedResponse{{status: 429, body: "slow down"}},
			backoffBase:   100 * time.Millisecond,
			wantCalls:     1 + MaxRetries,
			wantDelays:    []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond},
			wantFailed:    1,
			wantRetries:   MaxRetries,
			wantLogSubstr: "returned status 429",
		},
		{
			name:          "408 is retried",
			cfg:           config.AlertsConfig{WebhookURL: "https://alerts.example/hook/" + secretToken},
			script:        []scriptedResponse{{status: 408, body: ""}, {status: 200, body: "ok"}},
			backoffBase:   100 * time.Millisecond,
			wantCalls:     2,
			wantDelays:    []time.Duration{100 * time.Millisecond},
			wantDelivered: 1,
			wantRetries:   1,
		},
		{
			name:          "transport error is retried",
			cfg:           config.AlertsConfig{WebhookURL: "https://alerts.example/hook/" + secretToken},
			script:        []scriptedResponse{{transport: errors.New("connection refused")}, {status: 200, body: "ok"}},
			backoffBase:   100 * time.Millisecond,
			wantCalls:     2,
			wantDelays:    []time.Duration{100 * time.Millisecond},
			wantDelivered: 1,
			wantRetries:   1,
		},
		{
			name:           "attempt timeout is bounded retried and reported",
			cfg:            config.AlertsConfig{WebhookURL: "https://alerts.example/hook/" + secretToken},
			script:         []scriptedResponse{{hang: true}},
			attemptTimeout: 20 * time.Millisecond,
			backoffBase:    time.Millisecond,
			wantCalls:      1 + MaxRetries,
			wantDelays:     []time.Duration{time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond},
			wantFailed:     1,
			wantRetries:    MaxRetries,
			wantLogSubstr:  "context deadline exceeded",
		},
		{
			name:          "backoff is capped",
			cfg:           config.AlertsConfig{WebhookURL: "https://alerts.example/hook/" + secretToken},
			script:        []scriptedResponse{{status: 503, body: "unavailable"}},
			backoffBase:   6 * time.Second,
			wantCalls:     1 + MaxRetries,
			wantDelays:    []time.Duration{6 * time.Second, maxBackoff, maxBackoff},
			wantFailed:    1,
			wantRetries:   MaxRetries,
			wantLogSubstr: "returned status 503",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			endpoint := &scriptedEndpoint{script: tt.script}
			clock := newFakeClock()
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))

			dispatcher, err := New(Options{
				Config:         tt.cfg,
				AttemptTimeout: tt.attemptTimeout,
				BackoffBase:    tt.backoffBase,
				Logger:         logger,
				HTTPClient:     newTestClient(endpoint.roundTrip),
				Clock:          clock,
				Workers:        1,
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			defer dispatcher.Close()

			dispatcher.Dispatch(context.Background(), NewBudgetExceededAlert("agent-a", 11, 10, "reject"))
			drain(t, dispatcher)

			if got := endpoint.calls.Load(); got != tt.wantCalls {
				t.Fatalf("attempts = %d, want %d", got, tt.wantCalls)
			}
			if got := clock.Delays(); fmt.Sprint(got) != fmt.Sprint(tt.wantDelays) {
				t.Fatalf("backoff delays = %v, want %v", got, tt.wantDelays)
			}
			stats := dispatcher.Stats()
			if stats.Delivered != tt.wantDelivered || stats.Failed != tt.wantFailed || stats.Retries != tt.wantRetries {
				t.Fatalf("stats = %+v, want delivered=%d failed=%d retries=%d", stats, tt.wantDelivered, tt.wantFailed, tt.wantRetries)
			}
			if stats.Dropped != 0 || stats.Enqueued != 1 {
				t.Fatalf("stats = %+v, want enqueued=1 dropped=0", stats)
			}

			logText := logs.String()
			if tt.wantLogSubstr != "" && !strings.Contains(logText, tt.wantLogSubstr) {
				t.Fatalf("logs missing %q: %s", tt.wantLogSubstr, logText)
			}
			if tt.wantFailed > 0 && !strings.Contains(logText, "alert delivery failed") {
				t.Fatalf("logs missing final failure: %s", logText)
			}
			if tt.wantFailed == 0 && strings.Contains(logText, "alert delivery failed") {
				t.Fatalf("unexpected failure log: %s", logText)
			}
			if strings.Contains(logText, secretToken) || strings.Contains(logText, "SECRET") {
				t.Fatalf("logs leak webhook secret: %s", logText)
			}
		})
	}
}

func TestAlertDispatcher_RedactsSecretsFromLogs(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test table fields explicit.
	tests := []struct {
		name            string
		cfg             config.AlertsConfig
		script          []scriptedResponse
		useRealClient   bool
		secrets         []string
		wantDestination string
	}{
		{
			name:            "userinfo path and query are redacted from status errors",
			cfg:             config.AlertsConfig{WebhookURL: "https://user:pa55word@alerts.example/hook/" + secretToken + "?token=QUERYSECRET"},
			script:          []scriptedResponse{{status: 400, body: "rejected token QUERYSECRET for " + secretToken}},
			secrets:         []string{"pa55word", "user:pa55word", secretToken, "QUERYSECRET"},
			wantDestination: "https://alerts.example/[redacted]?[redacted]",
		},
		{
			name:            "url.Error from the http client is stripped of the url",
			cfg:             config.AlertsConfig{WebhookURL: "https://alerts.example/hook/" + secretToken},
			script:          []scriptedResponse{{transport: errors.New("dial tcp: connection refused")}},
			useRealClient:   true,
			secrets:         []string{secretToken, "/hook/"},
			wantDestination: "https://alerts.example/[redacted]",
		},
		{
			name:            "slack path secret is redacted",
			cfg:             config.AlertsConfig{SlackWebhookURL: "https://hooks.slack.com/services/" + secretToken},
			script:          []scriptedResponse{{status: 403, body: "invalid_token " + secretToken}},
			secrets:         []string{secretToken, "/services/"},
			wantDestination: "https://hooks.slack.com/[redacted]",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			endpoint := &scriptedEndpoint{script: tt.script}
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			client := newTestClient(endpoint.roundTrip)
			if tt.useRealClient {
				// http.Client wraps transport errors in *url.Error with the full URL.
				client = &http.Client{Transport: roundTripFunc(endpoint.roundTrip)}
			}

			dispatcher, err := New(Options{Config: tt.cfg, Logger: logger, HTTPClient: client, Clock: newFakeClock(), Workers: 1})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			defer dispatcher.Close()

			dispatcher.Dispatch(context.Background(), NewAgentKilledAlert("agent-a", "runaway"))
			drain(t, dispatcher)

			logText := logs.String()
			if !strings.Contains(logText, "alert delivery failed") {
				t.Fatalf("logs missing failure: %s", logText)
			}
			if !strings.Contains(logText, tt.wantDestination) {
				t.Fatalf("logs missing redacted destination %q: %s", tt.wantDestination, logText)
			}
			for _, secret := range tt.secrets {
				if strings.Contains(logText, secret) {
					t.Fatalf("logs leak %q: %s", secret, logText)
				}
			}
		})
	}
}

func TestAlertDispatcher_QueueSaturation(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test table fields explicit.
	tests := []struct {
		name        string
		queueSize   int
		dispatches  int
		wantDropped uint64
		wantSatLogs int
	}{
		{name: "one worker blocked and queue of one drops the rest", queueSize: 1, dispatches: 5, wantDropped: 3, wantSatLogs: 1},
		{name: "queue large enough drops nothing", queueSize: 8, dispatches: 5, wantDropped: 0, wantSatLogs: 0},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			release := make(chan struct{})
			started := make(chan struct{}, 1)
			var calls atomic.Int32
			client := newTestClient(func(request *http.Request) (*http.Response, error) {
				calls.Add(1)
				select {
				case started <- struct{}{}:
				default:
				}
				select {
				case <-release:
				case <-request.Context().Done():
					return nil, request.Context().Err()
				}
				return responseWithStatus(http.StatusOK, "ok"), nil
			})

			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			dispatcher, err := New(Options{
				Config:     config.AlertsConfig{WebhookURL: "https://alerts.example/hook/" + secretToken},
				Logger:     logger,
				HTTPClient: client,
				Clock:      newFakeClock(),
				QueueSize:  tt.queueSize,
				Workers:    1,
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			defer dispatcher.Close()

			// First dispatch occupies the single worker; wait until it is in flight so
			// the remaining dispatches contend for queue slots deterministically.
			dispatcher.Dispatch(context.Background(), NewAgentKilledAlert("agent-0", "runaway"))
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("first delivery did not start")
			}

			for i := 1; i < tt.dispatches; i++ {
				begin := time.Now()
				dispatcher.Dispatch(context.Background(), NewAgentKilledAlert(fmt.Sprintf("agent-%d", i), "runaway"))
				if elapsed := time.Since(begin); elapsed > 200*time.Millisecond {
					t.Fatalf("Dispatch blocked for %s while worker was busy", elapsed)
				}
			}

			stats := dispatcher.Stats()
			if stats.Dropped != tt.wantDropped {
				t.Fatalf("Dropped = %d, want %d (stats %+v)", stats.Dropped, tt.wantDropped, stats)
			}
			if stats.QueueCapacity != tt.queueSize {
				t.Fatalf("QueueCapacity = %d, want %d", stats.QueueCapacity, tt.queueSize)
			}

			close(release)
			drain(t, dispatcher)

			stats = dispatcher.Stats()
			if want := uint64(tt.dispatches) - tt.wantDropped; stats.Delivered != want {
				t.Fatalf("Delivered = %d, want %d", stats.Delivered, want)
			}
			if got := int32(tt.dispatches) - int32(tt.wantDropped); calls.Load() != got {
				t.Fatalf("transport calls = %d, want %d", calls.Load(), got)
			}

			logText := logs.String()
			if got := strings.Count(logText, "alert queue saturated"); got != tt.wantSatLogs {
				t.Fatalf("saturation logs = %d, want %d: %s", got, tt.wantSatLogs, logText)
			}
			if tt.wantSatLogs > 0 && !strings.Contains(logText, `"dropped_since_last_report":1`) {
				t.Fatalf("first saturation log should report one drop: %s", logText)
			}
			if strings.Contains(logText, secretToken) {
				t.Fatalf("saturation log leaks secret: %s", logText)
			}
		})
	}
}

func TestAlertDispatcher_SaturationLogRateLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		advance   time.Duration
		wantCount int
	}{
		{name: "drops within the interval are aggregated", advance: saturationLogInterval / 2, wantCount: 1},
		{name: "drops after the interval log again with the aggregate", advance: saturationLogInterval, wantCount: 2},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			clock := newFakeClock()
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			dispatcher, err := New(Options{
				Config:     config.AlertsConfig{WebhookURL: "https://alerts.example/hook"},
				Logger:     logger,
				HTTPClient: newTestClient(func(*http.Request) (*http.Response, error) { return responseWithStatus(200, "ok"), nil }),
				Clock:      clock,
				QueueSize:  1,
				Workers:    1,
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			defer dispatcher.Close()

			item := job{dest: dispatcher.destinations[0], event: NewAgentKilledAlert("agent", "x")}
			dispatcher.logSaturation(item)
			dispatcher.logSaturation(item)
			clock.Advance(tt.advance)
			dispatcher.logSaturation(item)

			logText := logs.String()
			if got := strings.Count(logText, "alert queue saturated"); got != tt.wantCount {
				t.Fatalf("saturation logs = %d, want %d: %s", got, tt.wantCount, logText)
			}
			if tt.wantCount == 2 && !strings.Contains(logText, `"dropped_since_last_report":2`) {
				t.Fatalf("second log should aggregate two drops: %s", logText)
			}
		})
	}
}

func TestAlertDispatcher_CloseCancelsInFlightAndRejectsNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "close returns promptly while an attempt hangs"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			started := make(chan struct{}, 1)
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			dispatcher, err := New(Options{
				Config:         config.AlertsConfig{WebhookURL: "https://alerts.example/hook/" + secretToken},
				AttemptTimeout: MaxAttemptTimeout,
				Logger:         logger,
				HTTPClient: newTestClient(func(request *http.Request) (*http.Response, error) {
					select {
					case started <- struct{}{}:
					default:
					}
					<-request.Context().Done()
					return nil, request.Context().Err()
				}),
				Clock:   newFakeClock(),
				Workers: 1,
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			dispatcher.Dispatch(context.Background(), NewAgentKilledAlert("agent-a", "runaway"))
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("delivery did not start")
			}

			begin := time.Now()
			dispatcher.Close()
			if elapsed := time.Since(begin); elapsed > 2*time.Second {
				t.Fatalf("Close() took %s, want prompt cancellation", elapsed)
			}
			dispatcher.Close()

			dispatcher.Dispatch(context.Background(), NewAgentKilledAlert("agent-b", "runaway"))
			stats := dispatcher.Stats()
			if stats.Dropped != 1 {
				t.Fatalf("Dropped after Close = %d, want 1", stats.Dropped)
			}
			if stats.Failed != 1 {
				t.Fatalf("Failed = %d, want 1 (cancelled attempt)", stats.Failed)
			}
			if stats.Retries != 0 {
				t.Fatalf("Retries = %d, want 0 after cancellation", stats.Retries)
			}
			logText := logs.String()
			if !strings.Contains(logText, "context canceled") {
				t.Fatalf("logs should mention cancellation: %s", logText)
			}
			if strings.Contains(logText, secretToken) {
				t.Fatalf("logs leak secret: %s", logText)
			}
		})
	}
}

func TestAlertDispatcher_DispatchDoesNotBlockOnSlowEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		dispatches int
	}{
		{name: "hundred dispatches return quickly against a hanging endpoint", dispatches: 100},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stop := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-r.Context().Done():
				case <-stop:
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			defer close(stop)

			dispatcher, err := New(Options{
				Config:         config.AlertsConfig{WebhookURL: server.URL + "/hook"},
				AttemptTimeout: 50 * time.Millisecond,
				BackoffBase:    time.Millisecond,
				HTTPClient:     server.Client(),
				Clock:          newFakeClock(),
				QueueSize:      4,
				Workers:        1,
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			defer dispatcher.Close()

			begin := time.Now()
			for i := 0; i < tt.dispatches; i++ {
				dispatcher.Dispatch(context.Background(), NewBudgetExceededAlert(fmt.Sprintf("agent-%d", i), 11, 10, "reject"))
			}
			if elapsed := time.Since(begin); elapsed > time.Second {
				t.Fatalf("%d dispatches took %s, want well under a second", tt.dispatches, elapsed)
			}

			stats := dispatcher.Stats()
			if stats.Enqueued+stats.Dropped != uint64(tt.dispatches) {
				t.Fatalf("enqueued+dropped = %d, want %d", stats.Enqueued+stats.Dropped, tt.dispatches)
			}
			if stats.Dropped == 0 {
				t.Fatalf("expected drops with queue of 4 and a hanging endpoint, stats %+v", stats)
			}
		})
	}
}

func TestIsRetryable_NonDeliveryError(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test table fields explicit.
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "plain error is not retryable", err: errors.New("x"), want: false},
		{name: "retryable delivery error", err: &deliveryError{err: errors.New("x"), retryable: true}, want: true},
		{name: "wrapped permanent delivery error", err: fmt.Errorf("wrap: %w", &deliveryError{err: errors.New("x")}), want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isRetryable(tt.err); got != tt.want {
				t.Fatalf("isRetryable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReadErrorSnippet_TruncatesAndCleans(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "control characters removed and newlines flattened", body: "line1\nline2\x00\x07 end", want: "line1 line2 end"},
		{name: "long body truncated", body: strings.Repeat("a", 1000), want: strings.Repeat("a", maxErrorBodyBytes)},
		{name: "secret segments replaced", body: "token " + secretToken, want: "token [redacted]/[redacted]/[redacted]"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dest := newDestination(KindWebhook, "https://alerts.example/hook/"+secretToken)
			got := readErrorSnippet(io.NopCloser(strings.NewReader(tt.body)), dest)
			if got != tt.want {
				t.Fatalf("readErrorSnippet() = %q, want %q", got, tt.want)
			}
		})
	}
}

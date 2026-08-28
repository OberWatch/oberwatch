package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OberWatch/oberwatch/internal/config"
)

func TestAlertDispatcher_DestinationsAreIsolated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "a failing webhook does not stop slack delivery"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var webhookCalls, slackCalls atomic.Int32
			slackBody := make(chan []byte, 1)
			client := newTestClient(func(request *http.Request) (*http.Response, error) {
				if request.URL.Host == config.SlackWebhookHost {
					slackCalls.Add(1)
					body, err := io.ReadAll(request.Body)
					if err != nil {
						t.Errorf("ReadAll() error = %v", err)
					}
					select {
					case slackBody <- body:
					default:
					}
					return responseWithStatus(http.StatusOK, "ok"), nil
				}
				webhookCalls.Add(1)
				return responseWithStatus(http.StatusInternalServerError, "boom "+secretToken), nil
			})

			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			dispatcher, err := New(Options{
				Config: config.AlertsConfig{
					WebhookURL:      "https://alerts.example/hook/" + secretToken,
					SlackWebhookURL: "https://hooks.slack.com/services/" + secretToken,
				},
				BackoffBase: time.Millisecond,
				Logger:      logger,
				HTTPClient:  client,
				Clock:       newFakeClock(),
				Workers:     1,
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			defer dispatcher.Close()

			dispatcher.Dispatch(context.Background(), NewBudgetExceededAlert("agent-a", 11, 10, "reject"))
			drain(t, dispatcher)

			if got := webhookCalls.Load(); got != 1+MaxRetries {
				t.Fatalf("webhook attempts = %d, want %d", got, 1+MaxRetries)
			}
			if got := slackCalls.Load(); got != 1 {
				t.Fatalf("slack attempts = %d, want 1", got)
			}

			stats := dispatcher.Stats()
			if stats.Enqueued != 2 || stats.Delivered != 1 || stats.Failed != 1 || stats.Retries != MaxRetries {
				t.Fatalf("stats = %+v, want enqueued=2 delivered=1 failed=1 retries=%d", stats, MaxRetries)
			}
			if stats.Dropped != 0 {
				t.Fatalf("Dropped = %d, want 0", stats.Dropped)
			}

			select {
			case body := <-slackBody:
				if !strings.Contains(string(body), "Oberwatch Alert") {
					t.Fatalf("slack payload = %s, want block payload", body)
				}
			default:
				t.Fatal("slack destination received no payload")
			}

			logText := logs.String()
			if !strings.Contains(logText, `"kind":"webhook"`) {
				t.Fatalf("logs missing failed webhook destination: %s", logText)
			}
			if strings.Contains(logText, `"kind":"slack"`) {
				t.Fatalf("logs report a slack failure that did not happen: %s", logText)
			}
			if strings.Contains(logText, secretToken) {
				t.Fatalf("logs leak webhook secret: %s", logText)
			}
		})
	}
}

func TestAlertDispatcher_LogsStatusOnlyWhenResponseArrived(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test table fields explicit.
	tests := []struct {
		name       string
		script     []scriptedResponse
		wantStatus string
		wantAbsent string
	}{
		{
			name:       "permanent status is reported",
			script:     []scriptedResponse{{status: 403, body: "invalid_token"}},
			wantStatus: `"status":403`,
		},
		{
			name:       "retried status is reported after the last attempt",
			script:     []scriptedResponse{{status: 503, body: "unavailable"}},
			wantStatus: `"status":503`,
		},
		{
			name:       "transport failure reports no status",
			script:     []scriptedResponse{{transport: errors.New("connection refused")}},
			wantAbsent: `"status"`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			endpoint := &scriptedEndpoint{script: tt.script}
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			dispatcher, err := New(Options{
				Config:      config.AlertsConfig{WebhookURL: "https://alerts.example/hook/" + secretToken},
				BackoffBase: time.Millisecond,
				Logger:      logger,
				HTTPClient:  newTestClient(endpoint.roundTrip),
				Clock:       newFakeClock(),
				Workers:     1,
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			defer dispatcher.Close()

			dispatcher.Dispatch(context.Background(), NewAgentKilledAlert("agent-a", "runaway"))
			drain(t, dispatcher)

			logText := logs.String()
			if tt.wantStatus != "" && !strings.Contains(logText, tt.wantStatus) {
				t.Fatalf("logs missing %q: %s", tt.wantStatus, logText)
			}
			if tt.wantAbsent != "" && strings.Contains(logText, tt.wantAbsent) {
				t.Fatalf("logs contain %q for a failure without a response: %s", tt.wantAbsent, logText)
			}
		})
	}
}

func TestBuildSlackPayload_BlockTextIsNeverEmpty(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test table fields explicit.
	tests := []struct {
		name  string
		event Alert
	}{
		{name: "empty message", event: Alert{Type: TypeBudgetExceeded, Agent: "agent-a"}},
		{name: "blank message", event: Alert{Type: TypeAgentKilled, Agent: "agent-a", Message: "   \n"}},
		{name: "zero value alert", event: Alert{}},
		{name: "populated alert", event: NewBudgetThresholdAlert("agent-a", 80, 8, 10, "alert", time.Now().UTC())},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			encoded, err := json.Marshal(buildSlackPayload(tt.event))
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			var decoded struct {
				Text   string `json:"text"`
				Blocks []struct {
					Text *struct {
						Text string `json:"text"`
					} `json:"text"`
					Fields []struct {
						Text string `json:"text"`
					} `json:"fields"`
				} `json:"blocks"`
			}
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			if strings.TrimSpace(decoded.Text) == "" {
				t.Fatalf("top level text is empty: %s", encoded)
			}
			if len(decoded.Blocks) != 3 {
				t.Fatalf("blocks = %d, want 3", len(decoded.Blocks))
			}
			for i, block := range decoded.Blocks {
				if block.Text != nil && strings.TrimSpace(block.Text.Text) == "" {
					t.Fatalf("block %d has empty text, which slack rejects: %s", i, encoded)
				}
				for j, field := range block.Fields {
					if strings.TrimSpace(field.Text) == "" {
						t.Fatalf("block %d field %d has empty text: %s", i, j, encoded)
					}
				}
			}
		})
	}
}

func TestAlertDispatcher_DrainReportsTimeoutWithoutLeakingURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "drain gives up when an attempt outlives the context"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			started := make(chan struct{}, 1)
			dispatcher, err := New(Options{
				Config:         config.AlertsConfig{WebhookURL: "https://alerts.example/hook/" + secretToken},
				AttemptTimeout: MaxAttemptTimeout,
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
			defer dispatcher.Close()

			dispatcher.Dispatch(context.Background(), NewAgentKilledAlert("agent-a", "runaway"))
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("delivery did not start")
			}

			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			drainErr := dispatcher.Drain(ctx)
			if drainErr == nil {
				t.Fatal("Drain() error = nil, want timeout")
			}
			if !errors.Is(drainErr, context.DeadlineExceeded) {
				t.Fatalf("Drain() error = %v, want context.DeadlineExceeded", drainErr)
			}
			if strings.Contains(drainErr.Error(), secretToken) {
				t.Fatalf("Drain() error leaks secret: %v", drainErr)
			}
		})
	}
}

func TestNew_BoundsAttemptTimeoutAndDefaults(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test table fields explicit.
	tests := []struct {
		name        string
		attempt     time.Duration
		backoff     time.Duration
		queueSize   int
		wantAttempt time.Duration
		wantBackoff time.Duration
		wantQueue   int
	}{
		{name: "zero falls back to defaults", wantAttempt: DefaultAttemptTimeout, wantBackoff: DefaultBackoffBase, wantQueue: DefaultQueueSize},
		{name: "negative falls back to defaults", attempt: -time.Second, backoff: -time.Second, queueSize: -1, wantAttempt: DefaultAttemptTimeout, wantBackoff: DefaultBackoffBase, wantQueue: DefaultQueueSize},
		{name: "below the cap is preserved", attempt: 3 * time.Second, backoff: time.Second, queueSize: 4, wantAttempt: 3 * time.Second, wantBackoff: time.Second, wantQueue: 4},
		{name: "at the cap is preserved", attempt: MaxAttemptTimeout, backoff: time.Second, queueSize: 4, wantAttempt: MaxAttemptTimeout, wantBackoff: time.Second, wantQueue: 4},
		{name: "above the cap is clamped to ten seconds", attempt: time.Hour, backoff: time.Second, queueSize: 4, wantAttempt: MaxAttemptTimeout, wantBackoff: time.Second, wantQueue: 4},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dispatcher, err := New(Options{
				Config:         config.AlertsConfig{WebhookURL: "https://alerts.example/hook"},
				AttemptTimeout: tt.attempt,
				BackoffBase:    tt.backoff,
				QueueSize:      tt.queueSize,
				Clock:          newFakeClock(),
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			defer dispatcher.Close()

			if dispatcher.attemptTimeout != tt.wantAttempt {
				t.Fatalf("attemptTimeout = %s, want %s", dispatcher.attemptTimeout, tt.wantAttempt)
			}
			if dispatcher.attemptTimeout > MaxAttemptTimeout {
				t.Fatalf("attemptTimeout = %s, exceeds the %s bound", dispatcher.attemptTimeout, MaxAttemptTimeout)
			}
			if dispatcher.backoffBase != tt.wantBackoff {
				t.Fatalf("backoffBase = %s, want %s", dispatcher.backoffBase, tt.wantBackoff)
			}
			if got := dispatcher.Stats().QueueCapacity; got != tt.wantQueue {
				t.Fatalf("QueueCapacity = %d, want %d", got, tt.wantQueue)
			}
		})
	}
}

// stalledClock never fires After, so a delivery parked in backoff can only be
// released by cancelling the dispatcher.
type stalledClock struct {
	waiting chan struct{}
}

func (c *stalledClock) Now() time.Time {
	return time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)
}

func (c *stalledClock) After(time.Duration) <-chan time.Time {
	select {
	case c.waiting <- struct{}{}:
	default:
	}
	return make(chan time.Time)
}

func TestAlertDispatcher_CancelDuringBackoffCountsNoRetry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "close while parked in backoff records the attempt but not the retry"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var calls atomic.Int32
			clock := &stalledClock{waiting: make(chan struct{}, 1)}
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			dispatcher, err := New(Options{
				Config:      config.AlertsConfig{WebhookURL: "https://alerts.example/hook/" + secretToken},
				BackoffBase: time.Hour,
				Logger:      logger,
				HTTPClient: newTestClient(func(*http.Request) (*http.Response, error) {
					calls.Add(1)
					return responseWithStatus(http.StatusServiceUnavailable, "unavailable"), nil
				}),
				Clock:   clock,
				Workers: 1,
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			dispatcher.Dispatch(context.Background(), NewAgentKilledAlert("agent-a", "runaway"))
			select {
			case <-clock.waiting:
			case <-time.After(5 * time.Second):
				t.Fatal("delivery never reached the backoff wait")
			}

			dispatcher.Close()

			if got := calls.Load(); got != 1 {
				t.Fatalf("attempts = %d, want 1", got)
			}
			stats := dispatcher.Stats()
			if stats.Retries != 0 {
				t.Fatalf("Retries = %d, want 0 because no retry attempt was made", stats.Retries)
			}
			if stats.Failed != 1 || stats.Delivered != 0 {
				t.Fatalf("stats = %+v, want failed=1 delivered=0", stats)
			}
			logText := logs.String()
			if !strings.Contains(logText, `"attempts":1`) {
				t.Fatalf("logs should report the single attempt: %s", logText)
			}
			if strings.Contains(logText, secretToken) {
				t.Fatalf("logs leak secret: %s", logText)
			}
		})
	}
}

func TestAlertDispatcher_ConcurrentDispatchKeepsCountersBalanced(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test table fields explicit.
	tests := []struct {
		name      string
		goroutine int
		perWorker int
		workers   int
	}{
		{name: "many producers and four workers", goroutine: 16, perWorker: 25, workers: 4},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var calls atomic.Int32
			client := newTestClient(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return responseWithStatus(http.StatusOK, "ok"), nil
			})
			dispatcher, err := New(Options{
				Config:      config.AlertsConfig{WebhookURL: "https://alerts.example/hook"},
				BackoffBase: time.Millisecond,
				HTTPClient:  client,
				Clock:       newFakeClock(),
				QueueSize:   DefaultQueueSize,
				Workers:     tt.workers,
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			defer dispatcher.Close()

			total := tt.goroutine * tt.perWorker
			var wg sync.WaitGroup
			for g := 0; g < tt.goroutine; g++ {
				wg.Add(1)
				go func(worker int) {
					defer wg.Done()
					for i := 0; i < tt.perWorker; i++ {
						dispatcher.Dispatch(context.Background(), NewBudgetExceededAlert(fmt.Sprintf("agent-%d-%d", worker, i), 11, 10, "reject"))
						// Reading stats concurrently must stay race free.
						_ = dispatcher.Stats()
					}
				}(g)
			}
			wg.Wait()
			drain(t, dispatcher)

			stats := dispatcher.Stats()
			if stats.Enqueued+stats.Dropped != uint64(total) {
				t.Fatalf("enqueued+dropped = %d, want %d (stats %+v)", stats.Enqueued+stats.Dropped, total, stats)
			}
			if stats.Delivered != stats.Enqueued {
				t.Fatalf("Delivered = %d, want %d", stats.Delivered, stats.Enqueued)
			}
			if stats.Failed != 0 || stats.Retries != 0 {
				t.Fatalf("stats = %+v, want failed=0 retries=0", stats)
			}
			if got := uint64(calls.Load()); got != stats.Delivered {
				t.Fatalf("transport calls = %d, want %d", got, stats.Delivered)
			}
			if stats.QueueDepth != 0 {
				t.Fatalf("QueueDepth = %d, want 0 after drain", stats.QueueDepth)
			}
		})
	}
}

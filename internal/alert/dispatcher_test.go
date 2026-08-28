package alert

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OberWatch/oberwatch/internal/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func newTestClient(fn roundTripFunc) *http.Client {
	return &http.Client{Transport: fn}
}

func responseWithStatus(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// newSyncDispatcher builds a dispatcher backed by the given client and registers
// cleanup. Tests call drain to wait for queued deliveries.
func newSyncDispatcher(t *testing.T, cfg config.AlertsConfig, client *http.Client, logger *slog.Logger) *AlertDispatcher {
	t.Helper()
	dispatcher, err := New(Options{
		Config:         cfg,
		AttemptTimeout: time.Second,
		Logger:         logger,
		HTTPClient:     client,
		Clock:          newFakeClock(),
		Workers:        1,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(dispatcher.Close)
	return dispatcher
}

func drain(t *testing.T, dispatcher *AlertDispatcher) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := dispatcher.Drain(ctx); err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
}

func TestAlertDispatcher_WebhookSendsJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "webhook receives alert json payload"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			captured := make(chan []byte, 1)
			client := newTestClient(func(request *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Errorf("ReadAll() error = %v", err)
				}
				captured <- body
				return responseWithStatus(http.StatusOK, "ok"), nil
			})

			dispatcher := newSyncDispatcher(t, config.AlertsConfig{WebhookURL: "https://alerts.example/webhook"}, client, nil)
			event := Alert{
				Type:            TypeBudgetThreshold,
				Agent:           "email-agent",
				ThresholdPct:    80,
				SpentUSD:        8,
				LimitUSD:        10,
				Action:          "downgrade",
				Message:         "threshold reached",
				Severity:        "warning",
				Timestamp:       time.Now().UTC(),
				PeriodStartedAt: time.Date(2026, time.March, 26, 0, 0, 0, 0, time.UTC),
			}

			dispatcher.Dispatch(context.Background(), event)
			drain(t, dispatcher)

			select {
			case payload := <-captured:
				var decoded Alert
				if err := json.Unmarshal(payload, &decoded); err != nil {
					t.Fatalf("Unmarshal() error = %v", err)
				}
				if decoded.Type != TypeBudgetThreshold {
					t.Fatalf("type = %q, want %q", decoded.Type, TypeBudgetThreshold)
				}
				if decoded.Agent != "email-agent" {
					t.Fatalf("agent = %q, want %q", decoded.Agent, "email-agent")
				}
			default:
				t.Fatal("no webhook payload captured")
			}
		})
	}
}

func TestAlertDispatcher_SlackFormatsMessage(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test table fields explicit.
	tests := []struct {
		name       string
		event      Alert
		wantFields []string
	}{
		{
			name: "threshold alert renders every field",
			event: Alert{
				Type:         TypeBudgetThreshold,
				Agent:        "finance-agent",
				ThresholdPct: 50,
				SpentUSD:     5,
				LimitUSD:     10,
				Action:       "alert",
				Message:      "Budget threshold reached",
				Severity:     "warning",
				Timestamp:    time.Now().UTC(),
			},
			wantFields: []string{"*Agent:*\nfinance-agent", "*Threshold:*\n50%", "*Spent/Limit:*\n$5.00 / $10.00", "*Action:*\nalert"},
		},
		{
			name: "runaway alert falls back to n/a for missing fields",
			event: Alert{
				Type:      TypeRunawayDetected,
				Agent:     "loop-agent",
				Message:   "runaway",
				Severity:  "critical",
				Timestamp: time.Now().UTC(),
			},
			wantFields: []string{"*Agent:*\nloop-agent", "*Threshold:*\nn/a", "*Spent/Limit:*\nn/a", "*Action:*\nn/a"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			captured := make(chan []byte, 1)
			client := newTestClient(func(request *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Errorf("ReadAll() error = %v", err)
				}
				captured <- body
				return responseWithStatus(http.StatusOK, "ok"), nil
			})

			dispatcher := newSyncDispatcher(t, config.AlertsConfig{SlackWebhookURL: "https://hooks.slack.com/services/T000/B000/abc"}, client, nil)
			dispatcher.Dispatch(context.Background(), tt.event)
			drain(t, dispatcher)

			var payload []byte
			select {
			case payload = <-captured:
			default:
				t.Fatal("no slack payload captured")
			}

			var decoded map[string]any
			if err := json.Unmarshal(payload, &decoded); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			text, ok := decoded["text"].(string)
			if !ok || !strings.Contains(text, string(tt.event.Type)) {
				t.Fatalf("text = %#v, want alert type", decoded["text"])
			}
			blocks, ok := decoded["blocks"].([]any)
			if !ok || len(blocks) != 3 {
				t.Fatalf("blocks = %#v, want three sections", decoded["blocks"])
			}
			raw := string(payload)
			for _, field := range tt.wantFields {
				encoded, err := json.Marshal(field)
				if err != nil {
					t.Fatalf("Marshal() error = %v", err)
				}
				if !strings.Contains(raw, strings.Trim(string(encoded), `"`)) {
					t.Fatalf("payload missing field %q: %s", field, raw)
				}
			}
			if !strings.Contains(raw, tt.event.Message) {
				t.Fatalf("payload missing message %q: %s", tt.event.Message, raw)
			}
		})
	}
}

func TestAlertDispatcher_DeduplicatesThresholdsPerPeriod(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test table fields explicit.
	tests := []struct {
		name            string
		secondThreshold float64
		wantCalls       int32
	}{
		{name: "same threshold same period deduped", secondThreshold: 80, wantCalls: 1},
		{name: "different threshold same period not deduped", secondThreshold: 100, wantCalls: 2},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var calls int32
			client := newTestClient(func(request *http.Request) (*http.Response, error) {
				atomic.AddInt32(&calls, 1)
				return responseWithStatus(http.StatusOK, "ok"), nil
			})
			dispatcher := newSyncDispatcher(t, config.AlertsConfig{WebhookURL: "https://alerts.example/webhook"}, client, nil)

			periodStart := time.Date(2026, time.March, 26, 0, 0, 0, 0, time.UTC)
			base := Alert{
				Type:            TypeBudgetThreshold,
				Agent:           "email-agent",
				ThresholdPct:    80,
				SpentUSD:        8,
				LimitUSD:        10,
				Action:          "downgrade",
				Message:         "threshold reached",
				Severity:        "warning",
				Timestamp:       time.Now().UTC(),
				PeriodStartedAt: periodStart,
			}
			second := base
			second.ThresholdPct = tt.secondThreshold

			dispatcher.Dispatch(context.Background(), base)
			dispatcher.Dispatch(context.Background(), second)
			drain(t, dispatcher)

			if got := atomic.LoadInt32(&calls); got != tt.wantCalls {
				t.Fatalf("dispatch calls = %d, want %d", got, tt.wantCalls)
			}
		})
	}
}

func TestAlertConstructors_AllTypes(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep constructor function field explicit.
	tests := []struct {
		name     string
		wantType Type
		build    func() Alert
	}{
		{
			name:     "budget threshold",
			wantType: TypeBudgetThreshold,
			build: func() Alert {
				return NewBudgetThresholdAlert("agent-a", 80, 8, 10, "downgrade", time.Now().UTC())
			},
		},
		{
			name:     "budget exceeded",
			wantType: TypeBudgetExceeded,
			build: func() Alert {
				return NewBudgetExceededAlert("agent-a", 11, 10, "reject")
			},
		},
		{
			name:     "runaway detected",
			wantType: TypeRunawayDetected,
			build: func() Alert {
				return NewRunawayDetectedAlert("agent-a", 120, 60)
			},
		},
		{
			name:     "error spike",
			wantType: TypeErrorSpike,
			build: func() Alert {
				return NewErrorSpikeAlert("agent-a", 42.5, 60)
			},
		},
		{
			name:     "agent killed",
			wantType: TypeAgentKilled,
			build: func() Alert {
				return NewAgentKilledAlert("agent-a", "runaway")
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.build()
			if got.Type != tt.wantType {
				t.Fatalf("type = %q, want %q", got.Type, tt.wantType)
			}
			if got.Agent == "" {
				t.Fatal("agent should not be empty")
			}
			if got.Message == "" {
				t.Fatal("message should not be empty")
			}
		})
	}
}

func TestAlertDispatcher_NilAndNoDestinations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		useNil bool
	}{
		{name: "nil dispatcher is safe", useNil: true},
		{name: "empty destinations does not send", useNil: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			event := Alert{Type: TypeBudgetExceeded, Agent: "a", Timestamp: time.Now().UTC()}
			if tt.useNil {
				var nilDispatcher *AlertDispatcher
				nilDispatcher.Dispatch(context.Background(), event)
				nilDispatcher.Close()
				if err := nilDispatcher.Drain(context.Background()); err != nil {
					t.Fatalf("Drain() error = %v", err)
				}
				if got := nilDispatcher.Stats(); got != (Stats{}) {
					t.Fatalf("Stats() = %+v, want zero", got)
				}
				return
			}
			var calls int32
			client := newTestClient(func(request *http.Request) (*http.Response, error) {
				atomic.AddInt32(&calls, 1)
				return responseWithStatus(http.StatusOK, "ok"), nil
			})
			dispatcher := newSyncDispatcher(t, config.AlertsConfig{}, client, nil)
			dispatcher.Dispatch(context.Background(), event)
			drain(t, dispatcher)
			if got := atomic.LoadInt32(&calls); got != 0 {
				t.Fatalf("transport calls = %d, want 0", got)
			}
		})
	}
}

func TestNew_RejectsInvalidDestinations(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test table fields explicit.
	tests := []struct {
		name       string
		cfg        config.AlertsConfig
		wantSubstr string
	}{
		{name: "webhook without scheme", cfg: config.AlertsConfig{WebhookURL: "alerts.example/webhook"}, wantSubstr: "alerts.webhook_url"},
		{name: "webhook with ftp scheme", cfg: config.AlertsConfig{WebhookURL: "ftp://alerts.example/webhook"}, wantSubstr: "scheme must be http or https"},
		{name: "slack on wrong host", cfg: config.AlertsConfig{SlackWebhookURL: "https://example.com/services/T/B/x"}, wantSubstr: "host must be hooks.slack.com"},
		{name: "slack over http", cfg: config.AlertsConfig{SlackWebhookURL: "http://hooks.slack.com/services/T/B/x"}, wantSubstr: "must use https"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dispatcher, err := New(Options{Config: tt.cfg})
			if err == nil {
				dispatcher.Close()
				t.Fatal("New() error = nil, want validation error")
			}
			if !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("New() error = %q, want substring %q", err.Error(), tt.wantSubstr)
			}
			if strings.Contains(err.Error(), "T/B/x") {
				t.Fatalf("New() error leaks URL path: %q", err.Error())
			}
		})
	}
}

func TestNewDispatcher_LegacyConstructors(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test table fields explicit.
	tests := []struct {
		name    string
		build   func() (*AlertDispatcher, error)
		wantErr bool
	}{
		{
			name: "NewDispatcher with valid config",
			build: func() (*AlertDispatcher, error) {
				return NewDispatcher(config.AlertsConfig{WebhookURL: "https://alerts.example/hook"}, 0, nil)
			},
		},
		{
			name: "NewDispatcherWithClient with nil client and oversized timeout",
			build: func() (*AlertDispatcher, error) {
				return NewDispatcherWithClient(config.AlertsConfig{}, time.Minute, nil, nil)
			},
		},
		{
			name: "NewDispatcher with invalid slack url",
			build: func() (*AlertDispatcher, error) {
				return NewDispatcher(config.AlertsConfig{SlackWebhookURL: "https://hooks.slack.com/"}, 0, nil)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dispatcher, err := tt.build()
			if tt.wantErr {
				if err == nil {
					dispatcher.Close()
					t.Fatal("error = nil, want validation error")
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			defer dispatcher.Close()
			if dispatcher.attemptTimeout <= 0 || dispatcher.attemptTimeout > MaxAttemptTimeout {
				t.Fatalf("attemptTimeout = %s, want within (0, %s]", dispatcher.attemptTimeout, MaxAttemptTimeout)
			}
			if got := dispatcher.Stats().QueueCapacity; got != DefaultQueueSize {
				t.Fatalf("QueueCapacity = %d, want %d", got, DefaultQueueSize)
			}
			if errors.Is(err, context.Canceled) {
				t.Fatal("unexpected cancellation")
			}
		})
	}
}

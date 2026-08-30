package alert

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/OberWatch/oberwatch/internal/config"
)

func TestAlertDispatcher_UpdateConfigSwapsDestinationsLive(t *testing.T) {
	t.Parallel()

	var gotHosts []string
	var mu sync.Mutex
	client := newTestClient(func(request *http.Request) (*http.Response, error) {
		mu.Lock()
		gotHosts = append(gotHosts, request.URL.Host)
		mu.Unlock()
		_, _ = io.ReadAll(request.Body)
		return responseWithStatus(http.StatusOK, "ok"), nil
	})

	dispatcher := newSyncDispatcher(t, config.AlertsConfig{WebhookURL: "https://original.example/hook"}, client, nil)

	dispatcher.Dispatch(context.Background(), NewAgentKilledAlert("agent-a", "runaway"))
	drain(t, dispatcher)

	if err := dispatcher.UpdateConfig(config.AlertsConfig{WebhookURL: "https://updated.example/hook"}); err != nil {
		t.Fatalf("UpdateConfig() error = %v", err)
	}

	dispatcher.Dispatch(context.Background(), NewAgentKilledAlert("agent-b", "runaway"))
	drain(t, dispatcher)

	mu.Lock()
	hosts := append([]string(nil), gotHosts...)
	mu.Unlock()

	if len(hosts) != 2 {
		t.Fatalf("hosts = %v, want 2 requests", hosts)
	}
	if hosts[0] != "original.example" {
		t.Fatalf("first request host = %q, want original.example", hosts[0])
	}
	if hosts[1] != "updated.example" {
		t.Fatalf("second request host = %q, want updated.example after UpdateConfig", hosts[1])
	}
}

func TestAlertDispatcher_UpdateConfigRejectsInvalidWithoutDisruption(t *testing.T) {
	t.Parallel()

	var calls int
	var mu sync.Mutex
	client := newTestClient(func(request *http.Request) (*http.Response, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		_, _ = io.ReadAll(request.Body)
		return responseWithStatus(http.StatusOK, "ok"), nil
	})

	dispatcher := newSyncDispatcher(t, config.AlertsConfig{WebhookURL: "https://original.example/hook"}, client, nil)

	err := dispatcher.UpdateConfig(config.AlertsConfig{WebhookURL: "not-a-url"})
	if err == nil {
		t.Fatal("UpdateConfig() error = nil, want validation error")
	}
	if !strings.Contains(err.Error(), "alerts.webhook_url") {
		t.Fatalf("UpdateConfig() error = %q, want alerts.webhook_url substring", err.Error())
	}

	dispatcher.Dispatch(context.Background(), NewAgentKilledAlert("agent-a", "runaway"))
	drain(t, dispatcher)

	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 1 {
		t.Fatalf("calls = %d, want 1 (still delivering with the original config)", got)
	}
}

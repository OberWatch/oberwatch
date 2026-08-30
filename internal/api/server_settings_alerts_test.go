package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OberWatch/oberwatch/internal/budget"
	"github.com/OberWatch/oberwatch/internal/config"
	"github.com/OberWatch/oberwatch/internal/storage"
)

const settingsAlertsPath = basePath + "/settings/alerts"

// fakeAlertConfigUpdater is a test double for the runtime dispatcher's
// UpdateConfig method. It never touches a network or a real destination. The
// mutex lets tests drive it from concurrent PATCH requests.
//
//nolint:govet // keep fields grouped by role rather than by alignment.
type fakeAlertConfigUpdater struct {
	mu    sync.Mutex
	calls []config.AlertsConfig
	err   error
}

func (f *fakeAlertConfigUpdater) UpdateConfig(cfg config.AlertsConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, cfg)
	if f.err != nil {
		return f.err
	}
	return nil
}

func TestServer_SettingsAlerts_RequiresAuth(t *testing.T) {
	t.Parallel()

	server, _, _ := newTestServer(t)

	for _, method := range []string{http.MethodGet, http.MethodPatch} {
		req := httptest.NewRequest(method, settingsAlertsPath, strings.NewReader("{}"))
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want %d", method, settingsAlertsPath, recorder.Code, http.StatusUnauthorized)
		}
	}
}

func TestServer_SettingsAlerts_GetReturnsMetadataWithoutSecrets(t *testing.T) {
	t.Parallel()

	server, _, store := newTestServer(t)
	sessionToken := seedSession(t, store, time.Now().UTC().Add(24*time.Hour))

	ctx := context.Background()
	current, err := storage.LoadAlertSettings(ctx, store)
	if err != nil {
		t.Fatalf("LoadAlertSettings() error = %v", err)
	}
	webhook := "https://alerts.example/hook"
	slack := "https://hooks.slack.com/services/T/B/xxxxxxxxxxxxxxxxxxxxxxxx"
	host := "smtp.example.com"
	port := 587
	user := "smtp-user@example.com"
	password := "super-secret-smtp-password"
	from := "alerts@example.com"
	to := []string{"ops@example.com"}
	enabled := true
	if _, err := storage.ApplyAlertSettingsPatch(ctx, store, current, storage.AlertSettingsPatch{
		WebhookURL:      &webhook,
		SlackWebhookURL: &slack,
		SMTPHost:        &host,
		SMTPPort:        &port,
		SMTPUser:        &user,
		SMTPPassword:    &password,
		SMTPFrom:        &from,
		SMTPTo:          &to,
		SMTPEnabled:     &enabled,
	}); err != nil {
		t.Fatalf("ApplyAlertSettingsPatch() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, settingsAlertsPath, nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	rawBody := recorder.Body.String()
	for _, secret := range []string{password, webhook, slack} {
		if strings.Contains(rawBody, secret) {
			t.Fatalf("response body leaked a secret value: %q\nbody=%s", secret, rawBody)
		}
	}

	payload := decodeJSONMap(t, recorder.Body)
	mustHaveKeys(t, payload,
		"smtp_host", "smtp_port", "smtp_user", "smtp_from", "smtp_to", "smtp_enabled",
		"smtp_password_is_set", "webhook_url_is_set", "slack_webhook_url_is_set", "live_applied",
	)

	if payload["smtp_host"] != host {
		t.Fatalf("smtp_host = %v, want %q", payload["smtp_host"], host)
	}
	if payload["smtp_user"] != user {
		t.Fatalf("smtp_user = %v, want %q", payload["smtp_user"], user)
	}
	if payload["smtp_from"] != from {
		t.Fatalf("smtp_from = %v, want %q", payload["smtp_from"], from)
	}
	if payload["smtp_password_is_set"] != true {
		t.Fatalf("smtp_password_is_set = %v, want true", payload["smtp_password_is_set"])
	}
	if payload["webhook_url_is_set"] != true {
		t.Fatalf("webhook_url_is_set = %v, want true", payload["webhook_url_is_set"])
	}
	if payload["slack_webhook_url_is_set"] != true {
		t.Fatalf("slack_webhook_url_is_set = %v, want true", payload["slack_webhook_url_is_set"])
	}
	if payload["live_applied"] != true {
		t.Fatalf("live_applied = %v, want true", payload["live_applied"])
	}
	if _, has := payload["smtp_password"]; has {
		t.Fatal("response must never include smtp_password")
	}
	if _, has := payload["webhook_url"]; has {
		t.Fatal("response must never include the raw webhook_url")
	}
	if _, has := payload["slack_webhook_url"]; has {
		t.Fatal("response must never include the raw slack_webhook_url")
	}
}

func TestServer_SettingsAlerts_GetDefaultsAllFalseWhenUnset(t *testing.T) {
	t.Parallel()

	server, _, store := newTestServer(t)
	sessionToken := seedSession(t, store, time.Now().UTC().Add(24*time.Hour))

	req := httptest.NewRequest(http.MethodGet, settingsAlertsPath, nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", recorder.Code, http.StatusOK)
	}
	payload := decodeJSONMap(t, recorder.Body)
	for _, key := range []string{"smtp_password_is_set", "webhook_url_is_set", "slack_webhook_url_is_set"} {
		if payload[key] != false {
			t.Fatalf("%s = %v, want false when unset", key, payload[key])
		}
	}
}

func TestServer_SettingsAlerts_PatchUpdatesPreservesAndClears(t *testing.T) {
	t.Parallel()

	server, _, store := newTestServer(t)
	sessionToken := seedSession(t, store, time.Now().UTC().Add(24*time.Hour))
	fakeDispatcher := &fakeAlertConfigUpdater{}
	server.SetAlertDispatcher(fakeDispatcher)

	patchReq := httptest.NewRequest(http.MethodPatch, settingsAlertsPath, strings.NewReader(
		`{"webhook_url":"https://alerts.example/hook","smtp_host":"smtp.example.com","smtp_port":587,"smtp_from":"alerts@example.com","smtp_to":["ops@example.com"]}`,
	))
	patchReq.Header.Set("Content-Type", "application/json")
	patchReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, patchReq)
	if recorder.Code != http.StatusOK {
		t.Fatalf("first PATCH status = %d, want %d, body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if len(fakeDispatcher.calls) != 1 {
		t.Fatalf("dispatcher UpdateConfig calls = %d, want 1", len(fakeDispatcher.calls))
	}
	if fakeDispatcher.calls[0].WebhookURL != "https://alerts.example/hook" {
		t.Fatalf("first UpdateConfig call webhook = %q, want the new webhook", fakeDispatcher.calls[0].WebhookURL)
	}

	// A patch that only sets smtp_user must preserve webhook_url and the rest
	// of the email fields set above.
	secondReq := httptest.NewRequest(http.MethodPatch, settingsAlertsPath, strings.NewReader(`{"smtp_user":"user@example.com"}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	secondRecorder := httptest.NewRecorder()
	server.ServeHTTP(secondRecorder, secondReq)
	if secondRecorder.Code != http.StatusOK {
		t.Fatalf("second PATCH status = %d, want %d, body=%s", secondRecorder.Code, http.StatusOK, secondRecorder.Body.String())
	}
	payload := decodeJSONMap(t, secondRecorder.Body)
	if payload["webhook_url_is_set"] != true {
		t.Fatal("webhook_url_is_set = false after unrelated patch, want preserved true")
	}
	if payload["smtp_user"] != "user@example.com" {
		t.Fatalf("smtp_user = %v, want user@example.com", payload["smtp_user"])
	}
	if len(fakeDispatcher.calls) != 2 {
		t.Fatalf("dispatcher UpdateConfig calls = %d, want 2", len(fakeDispatcher.calls))
	}

	// An explicit empty string clears webhook_url.
	clearWebhook := ""
	clearBody, err := json.Marshal(storage.AlertSettingsPatch{WebhookURL: &clearWebhook})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	clearReq := httptest.NewRequest(http.MethodPatch, settingsAlertsPath, strings.NewReader(string(clearBody)))
	clearReq.Header.Set("Content-Type", "application/json")
	clearReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	clearRecorder := httptest.NewRecorder()
	server.ServeHTTP(clearRecorder, clearReq)
	if clearRecorder.Code != http.StatusOK {
		t.Fatalf("clear PATCH status = %d, want %d, body=%s", clearRecorder.Code, http.StatusOK, clearRecorder.Body.String())
	}
	clearedPayload := decodeJSONMap(t, clearRecorder.Body)
	if clearedPayload["webhook_url_is_set"] != false {
		t.Fatal("webhook_url_is_set = true after explicit clear, want false")
	}

	reloaded, err := storage.LoadAlertSettings(context.Background(), store)
	if err != nil {
		t.Fatalf("LoadAlertSettings() error = %v", err)
	}
	if reloaded.WebhookURL != "" {
		t.Fatalf("reloaded WebhookURL = %q, want cleared", reloaded.WebhookURL)
	}
	if reloaded.SMTPUser != "user@example.com" {
		t.Fatalf("reloaded SMTPUser = %q, want preserved", reloaded.SMTPUser)
	}
}

func TestServer_SettingsAlerts_PatchInvalidLeavesStoreAndDispatcherUnchanged(t *testing.T) {
	t.Parallel()

	server, _, store := newTestServer(t)
	sessionToken := seedSession(t, store, time.Now().UTC().Add(24*time.Hour))
	fakeDispatcher := &fakeAlertConfigUpdater{}
	server.SetAlertDispatcher(fakeDispatcher)

	validWebhook := "https://alerts.example/hook"
	if _, err := storage.ApplyAlertSettingsPatch(context.Background(), store, storage.AlertSettings{}, storage.AlertSettingsPatch{WebhookURL: &validWebhook}); err != nil {
		t.Fatalf("ApplyAlertSettingsPatch() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPatch, settingsAlertsPath, strings.NewReader(`{"webhook_url":"not-a-url"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	if len(fakeDispatcher.calls) != 0 {
		t.Fatalf("dispatcher UpdateConfig calls = %d, want 0 for a rejected patch", len(fakeDispatcher.calls))
	}

	reloaded, err := storage.LoadAlertSettings(context.Background(), store)
	if err != nil {
		t.Fatalf("LoadAlertSettings() error = %v", err)
	}
	if reloaded.WebhookURL != validWebhook {
		t.Fatalf("WebhookURL = %q, want unchanged %q after a rejected patch", reloaded.WebhookURL, validWebhook)
	}
}

func TestServer_SettingsAlerts_PatchInvalidEmailWhenEnabledLeavesStoreUnchanged(t *testing.T) {
	t.Parallel()

	server, _, store := newTestServer(t)
	sessionToken := seedSession(t, store, time.Now().UTC().Add(24*time.Hour))
	fakeDispatcher := &fakeAlertConfigUpdater{}
	server.SetAlertDispatcher(fakeDispatcher)

	// Enabling email without a host, from, or recipients must fail validation
	// as a whole and must not partially persist smtp_enabled=true.
	req := httptest.NewRequest(http.MethodPatch, settingsAlertsPath, strings.NewReader(`{"smtp_enabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	if len(fakeDispatcher.calls) != 0 {
		t.Fatalf("dispatcher UpdateConfig calls = %d, want 0", len(fakeDispatcher.calls))
	}

	reloaded, err := storage.LoadAlertSettings(context.Background(), store)
	if err != nil {
		t.Fatalf("LoadAlertSettings() error = %v", err)
	}
	if reloaded.SMTPEnabled {
		t.Fatal("SMTPEnabled = true, want unchanged false after a rejected patch")
	}
}

func TestServer_SettingsAlerts_PatchLiveApplyFailureNeverPersists(t *testing.T) {
	t.Parallel()

	server, _, store := newTestServer(t)
	sessionToken := seedSession(t, store, time.Now().UTC().Add(24*time.Hour))
	fakeDispatcher := &fakeAlertConfigUpdater{err: errors.New("dispatcher rejected config")}
	server.SetAlertDispatcher(fakeDispatcher)

	req := httptest.NewRequest(http.MethodPatch, settingsAlertsPath, strings.NewReader(`{"webhook_url":"https://alerts.example/hook"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusInternalServerError, recorder.Body.String())
	}
	if len(fakeDispatcher.calls) != 1 {
		t.Fatalf("dispatcher UpdateConfig calls = %d, want 1 (the rejected candidate)", len(fakeDispatcher.calls))
	}

	reloaded, err := storage.LoadAlertSettings(context.Background(), store)
	if err != nil {
		t.Fatalf("LoadAlertSettings() error = %v", err)
	}
	if reloaded.WebhookURL != "" {
		t.Fatalf("WebhookURL = %q, want unchanged empty: it must never be committed when live-apply fails", reloaded.WebhookURL)
	}
}

// atomicFailingStore adds a failing AtomicSettingsStore.WriteSettingsBatch on
// top of failingStore, so PATCH exercises the same atomic-commit code path a
// real SQLiteStore uses instead of the per-key fallback.
type atomicFailingStore struct {
	failingStore
	writeBatchErr error
}

func (a atomicFailingStore) WriteSettingsBatch(context.Context, []storage.SettingWrite) error {
	return a.writeBatchErr
}

func TestServer_SettingsAlerts_PatchPersistFailureRestoresDispatcher(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	manager := budget.NewManager(cfg.Gate, nil)
	original := "https://alerts.example/original"
	store := atomicFailingStore{
		failingStore: failingStore{
			settings: map[string]string{
				"alerts.webhook_url": original,
			},
		},
		writeBatchErr: errors.New("disk full"),
	}
	server := New(cfg, manager, store, "0.1.0")

	fakeDispatcher := &fakeAlertConfigUpdater{}
	server.SetAlertDispatcher(fakeDispatcher)

	updated := "https://alerts.example/updated"
	slack := "https://hooks.slack.com/services/T/B/xxxxxxxxxxxxxxxxxxxxxxxx"
	body, err := json.Marshal(storage.AlertSettingsPatch{WebhookURL: &updated, SlackWebhookURL: &slack})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPatch, settingsAlertsPath, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: testSessionToken})
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d, body=%s", recorder.Code, http.StatusInternalServerError, recorder.Body.String())
	}
	// The dispatcher is live-applied before the commit is attempted, so it
	// records the candidate; when the commit then fails, the handler must
	// restore it back to the prior settings so it never diverges from what's
	// actually in SQLite.
	fakeDispatcher.mu.Lock()
	calls := append([]config.AlertsConfig{}, fakeDispatcher.calls...)
	fakeDispatcher.mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("dispatcher UpdateConfig calls = %d, want 2 (candidate then rollback)", len(calls))
	}
	if calls[0].WebhookURL != updated || calls[0].SlackWebhookURL != slack {
		t.Fatalf("first dispatcher config = %+v, want the rejected candidate", calls[0])
	}
	if calls[1].WebhookURL != original || calls[1].SlackWebhookURL != "" {
		t.Fatalf("final dispatcher config = %+v, want restored to the prior settings", calls[1])
	}

	reloaded, err := storage.LoadAlertSettings(context.Background(), store)
	if err != nil {
		t.Fatalf("LoadAlertSettings() error = %v", err)
	}
	if reloaded.WebhookURL != original {
		t.Fatalf("WebhookURL = %q, want unchanged %q after a failed multi-field commit", reloaded.WebhookURL, original)
	}
	if reloaded.SlackWebhookURL != "" {
		t.Fatalf("SlackWebhookURL = %q, want empty: it was never committed", reloaded.SlackWebhookURL)
	}
}

func TestServer_SettingsAlerts_ConcurrentPatchesRetainBothFields(t *testing.T) {
	t.Parallel()

	server, _, store := newTestServer(t)
	sessionToken := seedSession(t, store, time.Now().UTC().Add(24*time.Hour))
	fakeDispatcher := &fakeAlertConfigUpdater{}
	server.SetAlertDispatcher(fakeDispatcher)

	webhook := "https://alerts.example/webhook"
	slack := "https://hooks.slack.com/services/T/B/xxxxxxxxxxxxxxxxxxxxxxxx"

	patchOnce := func(body string) int {
		req := httptest.NewRequest(http.MethodPatch, settingsAlertsPath, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, req)
		return recorder.Code
	}

	var wg sync.WaitGroup
	codes := make([]int, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		codes[0] = patchOnce(fmt.Sprintf(`{"webhook_url":%q}`, webhook))
	}()
	go func() {
		defer wg.Done()
		codes[1] = patchOnce(fmt.Sprintf(`{"slack_webhook_url":%q}`, slack))
	}()
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("PATCH[%d] status = %d, want %d", i, code, http.StatusOK)
		}
	}

	reloaded, err := storage.LoadAlertSettings(context.Background(), store)
	if err != nil {
		t.Fatalf("LoadAlertSettings() error = %v", err)
	}
	if reloaded.WebhookURL != webhook {
		t.Fatalf("WebhookURL = %q, want %q retained alongside the concurrent slack update", reloaded.WebhookURL, webhook)
	}
	if reloaded.SlackWebhookURL != slack {
		t.Fatalf("SlackWebhookURL = %q, want %q retained alongside the concurrent webhook update", reloaded.SlackWebhookURL, slack)
	}

	fakeDispatcher.mu.Lock()
	defer fakeDispatcher.mu.Unlock()
	if len(fakeDispatcher.calls) != 2 {
		t.Fatalf("dispatcher UpdateConfig calls = %d, want 2", len(fakeDispatcher.calls))
	}
	last := fakeDispatcher.calls[len(fakeDispatcher.calls)-1]
	if last.WebhookURL != webhook || last.SlackWebhookURL != slack {
		t.Fatalf("last dispatcher config = %+v, want both fields retained (proves the runtime dispatcher never diverges from SQLite)", last)
	}
}

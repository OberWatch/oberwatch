package storage

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/OberWatch/oberwatch/internal/config"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(":memory:", 0, nil)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	return store
}

func TestLoadAlertSettings_DefaultsToZeroValue(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	settings, err := LoadAlertSettings(context.Background(), store)
	if err != nil {
		t.Fatalf("LoadAlertSettings() error = %v", err)
	}
	if !alertSettingsEqual(settings, AlertSettings{}) {
		t.Fatalf("LoadAlertSettings() = %+v, want zero value", settings)
	}
}

func TestImportAlertSettingsFromConfig_ImportsAbsentKeysOnce(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	cfg := config.AlertsConfig{
		WebhookURL:      "https://alerts.example/hook",
		SlackWebhookURL: "https://hooks.slack.com/services/T/B/x",
		Email: config.EmailConfig{
			Enabled:      true,
			SMTPHost:     "smtp.example.com",
			SMTPPort:     587,
			SMTPUser:     "user@example.com",
			SMTPPassword: "hunter2",
			From:         "alerts@example.com",
			To:           []string{"ops@example.com"},
		},
	}

	if err := ImportAlertSettingsFromConfig(context.Background(), store, cfg); err != nil {
		t.Fatalf("ImportAlertSettingsFromConfig() error = %v", err)
	}

	settings, err := LoadAlertSettings(context.Background(), store)
	if err != nil {
		t.Fatalf("LoadAlertSettings() error = %v", err)
	}
	want := AlertSettings{
		WebhookURL:      cfg.WebhookURL,
		SlackWebhookURL: cfg.SlackWebhookURL,
		SMTPHost:        cfg.Email.SMTPHost,
		SMTPPort:        cfg.Email.SMTPPort,
		SMTPUser:        cfg.Email.SMTPUser,
		SMTPPassword:    cfg.Email.SMTPPassword,
		SMTPFrom:        cfg.Email.From,
		SMTPTo:          cfg.Email.To,
		SMTPEnabled:     true,
	}
	if !alertSettingsEqual(settings, want) {
		t.Fatalf("LoadAlertSettings() = %+v, want %+v", settings, want)
	}
}

func TestImportAlertSettingsFromConfig_DoesNotOverwriteExistingValue(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	if err := store.SetSetting(context.Background(), "alerts.webhook_url", "https://dashboard.example/already-set"); err != nil {
		t.Fatalf("SetSetting() error = %v", err)
	}

	cfg := config.AlertsConfig{WebhookURL: "https://toml.example/should-not-win"}
	if err := ImportAlertSettingsFromConfig(context.Background(), store, cfg); err != nil {
		t.Fatalf("ImportAlertSettingsFromConfig() error = %v", err)
	}

	settings, err := LoadAlertSettings(context.Background(), store)
	if err != nil {
		t.Fatalf("LoadAlertSettings() error = %v", err)
	}
	if settings.WebhookURL != "https://dashboard.example/already-set" {
		t.Fatalf("WebhookURL = %q, want the pre-existing dashboard value preserved", settings.WebhookURL)
	}
}

func TestImportAlertSettingsFromConfig_NeverReimportsAfterExplicitClear(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	cfg := config.AlertsConfig{WebhookURL: "https://toml.example/hook"}

	if err := ImportAlertSettingsFromConfig(context.Background(), store, cfg); err != nil {
		t.Fatalf("ImportAlertSettingsFromConfig() first call error = %v", err)
	}

	// Simulate an operator explicitly clearing the setting from the dashboard.
	if err := store.DeleteSetting(context.Background(), "alerts.webhook_url"); err != nil {
		t.Fatalf("DeleteSetting() error = %v", err)
	}

	// A later boot with the same (or any) TOML value must not resurrect the
	// cleared setting: the one-time import already happened.
	if err := ImportAlertSettingsFromConfig(context.Background(), store, cfg); err != nil {
		t.Fatalf("ImportAlertSettingsFromConfig() second call error = %v", err)
	}

	settings, err := LoadAlertSettings(context.Background(), store)
	if err != nil {
		t.Fatalf("LoadAlertSettings() error = %v", err)
	}
	if settings.WebhookURL != "" {
		t.Fatalf("WebhookURL = %q, want empty after explicit clear survives reimport", settings.WebhookURL)
	}
}

func TestApplyAlertSettingsPatch_SetsAndClearsFields(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()

	webhook := "https://alerts.example/hook"
	current, err := LoadAlertSettings(ctx, store)
	if err != nil {
		t.Fatalf("LoadAlertSettings() error = %v", err)
	}
	updated, err := ApplyAlertSettingsPatch(ctx, store, current, AlertSettingsPatch{WebhookURL: &webhook})
	if err != nil {
		t.Fatalf("ApplyAlertSettingsPatch() error = %v", err)
	}
	if updated.WebhookURL != webhook {
		t.Fatalf("WebhookURL = %q, want %q", updated.WebhookURL, webhook)
	}

	reloaded, err := LoadAlertSettings(ctx, store)
	if err != nil {
		t.Fatalf("LoadAlertSettings() error = %v", err)
	}
	if reloaded.WebhookURL != webhook {
		t.Fatalf("reloaded WebhookURL = %q, want %q", reloaded.WebhookURL, webhook)
	}

	// A patch that omits webhook_url (nil pointer) must preserve it.
	host := "smtp.example.com"
	updated, err = ApplyAlertSettingsPatch(ctx, store, reloaded, AlertSettingsPatch{SMTPHost: &host})
	if err != nil {
		t.Fatalf("ApplyAlertSettingsPatch() error = %v", err)
	}
	if updated.WebhookURL != webhook {
		t.Fatalf("WebhookURL = %q, want preserved %q after unrelated patch", updated.WebhookURL, webhook)
	}
	if updated.SMTPHost != host {
		t.Fatalf("SMTPHost = %q, want %q", updated.SMTPHost, host)
	}

	// An explicit empty string clears the field.
	empty := ""
	updated, err = ApplyAlertSettingsPatch(ctx, store, updated, AlertSettingsPatch{WebhookURL: &empty})
	if err != nil {
		t.Fatalf("ApplyAlertSettingsPatch() error = %v", err)
	}
	if updated.WebhookURL != "" {
		t.Fatalf("WebhookURL = %q, want cleared", updated.WebhookURL)
	}

	reloaded, err = LoadAlertSettings(ctx, store)
	if err != nil {
		t.Fatalf("LoadAlertSettings() error = %v", err)
	}
	if reloaded.WebhookURL != "" {
		t.Fatalf("reloaded WebhookURL = %q, want cleared", reloaded.WebhookURL)
	}
}

func TestApplyAlertSettingsPatch_SQLiteMidBatchFailureLeavesNoPartialWrite(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	ctx := context.Background()

	// A baseline field, set before the injected failure, must survive
	// untouched: the failing patch below never mentions it.
	baselineWebhook := "https://alerts.example/baseline"
	current, err := LoadAlertSettings(ctx, store)
	if err != nil {
		t.Fatalf("LoadAlertSettings() error = %v", err)
	}
	current, err = ApplyAlertSettingsPatch(ctx, store, current, AlertSettingsPatch{WebhookURL: &baselineWebhook})
	if err != nil {
		t.Fatalf("ApplyAlertSettingsPatch() baseline error = %v", err)
	}

	// Fail before applying the second op in the batch below (smtp_from),
	// after the first op (smtp_host) has already been written inside the same
	// transaction. A correct implementation rolls both back together.
	testInjectSettingsBatchFailureAt = 1
	t.Cleanup(func() { testInjectSettingsBatchFailureAt = -1 })

	newHost := "smtp.example.com"
	newFrom := "alerts@example.com"
	_, err = ApplyAlertSettingsPatch(ctx, store, current, AlertSettingsPatch{SMTPHost: &newHost, SMTPFrom: &newFrom})
	if err == nil {
		t.Fatal("ApplyAlertSettingsPatch() error = nil, want the injected mid-batch failure")
	}

	reloaded, err := LoadAlertSettings(ctx, store)
	if err != nil {
		t.Fatalf("LoadAlertSettings() error = %v", err)
	}
	if reloaded.SMTPHost != "" {
		t.Fatalf("SMTPHost = %q, want empty: the first op in a failed batch must not survive", reloaded.SMTPHost)
	}
	if reloaded.SMTPFrom != "" {
		t.Fatalf("SMTPFrom = %q, want empty: it was never applied before the injected failure", reloaded.SMTPFrom)
	}
	if reloaded.WebhookURL != baselineWebhook {
		t.Fatalf("WebhookURL = %q, want the untouched baseline %q", reloaded.WebhookURL, baselineWebhook)
	}
}

func TestAlertSettings_ToAlertsConfigRoundTrip(t *testing.T) {
	t.Parallel()

	settings := AlertSettings{
		WebhookURL:      "https://alerts.example/hook",
		SlackWebhookURL: "https://hooks.slack.com/services/T/B/x",
		SMTPHost:        "smtp.example.com",
		SMTPPort:        587,
		SMTPUser:        "user@example.com",
		SMTPPassword:    "hunter2",
		SMTPFrom:        "alerts@example.com",
		SMTPTo:          []string{"ops@example.com"},
		SMTPEnabled:     true,
	}
	cfg := settings.ToAlertsConfig()
	if cfg.WebhookURL != settings.WebhookURL || cfg.SlackWebhookURL != settings.SlackWebhookURL {
		t.Fatalf("ToAlertsConfig() = %+v, webhook/slack mismatch", cfg)
	}
	if cfg.Email.SMTPHost != settings.SMTPHost || cfg.Email.SMTPPort != settings.SMTPPort ||
		cfg.Email.SMTPUser != settings.SMTPUser || cfg.Email.SMTPPassword != settings.SMTPPassword ||
		cfg.Email.From != settings.SMTPFrom || cfg.Email.Enabled != settings.SMTPEnabled {
		t.Fatalf("ToAlertsConfig() = %+v, email field mismatch", cfg)
	}
	if len(cfg.Email.To) != 1 || cfg.Email.To[0] != "ops@example.com" {
		t.Fatalf("ToAlertsConfig() To = %+v, want [ops@example.com]", cfg.Email.To)
	}
}

func TestMergeAlertSettingsPatch_PreservesUpdatesAndClearsWithoutStore(t *testing.T) {
	t.Parallel()

	current := AlertSettings{
		WebhookURL:  "https://alerts.example/hook",
		SMTPHost:    "smtp.example.com",
		SMTPPort:    587,
		SMTPTo:      []string{"ops@example.com"},
		SMTPEnabled: true,
	}

	// A patch that only touches SMTPHost must preserve every other field.
	newHost := "smtp2.example.com"
	merged := MergeAlertSettingsPatch(current, AlertSettingsPatch{SMTPHost: &newHost})
	if merged.SMTPHost != newHost {
		t.Fatalf("SMTPHost = %q, want %q", merged.SMTPHost, newHost)
	}
	if merged.WebhookURL != current.WebhookURL {
		t.Fatalf("WebhookURL = %q, want preserved %q", merged.WebhookURL, current.WebhookURL)
	}
	if !merged.SMTPEnabled {
		t.Fatal("SMTPEnabled = false, want preserved true")
	}

	// An explicit empty value clears the field.
	empty := ""
	merged = MergeAlertSettingsPatch(current, AlertSettingsPatch{WebhookURL: &empty})
	if merged.WebhookURL != "" {
		t.Fatalf("WebhookURL = %q, want cleared", merged.WebhookURL)
	}

	emptyTo := []string{}
	merged = MergeAlertSettingsPatch(current, AlertSettingsPatch{SMTPTo: &emptyTo})
	if len(merged.SMTPTo) != 0 {
		t.Fatalf("SMTPTo = %v, want cleared", merged.SMTPTo)
	}

	// MergeAlertSettingsPatch must be pure: current is never mutated, and no
	// store I/O happens (there is no store parameter to even call).
	if current.SMTPHost != "smtp.example.com" {
		t.Fatalf("current.SMTPHost mutated to %q", current.SMTPHost)
	}
}

func TestMergeAlertSettingsPatch_JSONTagsMatchAPIFieldNames(t *testing.T) {
	t.Parallel()

	host := "smtp.example.com"
	patch := AlertSettingsPatch{SMTPHost: &host}
	encoded, err := json.Marshal(patch)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !strings.Contains(string(encoded), `"smtp_host"`) {
		t.Fatalf("Marshal() = %s, want smtp_host key", encoded)
	}

	var decoded AlertSettingsPatch
	if err := json.Unmarshal([]byte(`{"webhook_url":""}`), &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if decoded.WebhookURL == nil || *decoded.WebhookURL != "" {
		t.Fatalf("decoded.WebhookURL = %v, want non-nil pointer to empty string", decoded.WebhookURL)
	}
	if decoded.SMTPHost != nil {
		t.Fatalf("decoded.SMTPHost = %v, want nil (absent key preserves)", decoded.SMTPHost)
	}
}

func alertSettingsEqual(a, b AlertSettings) bool {
	if a.WebhookURL != b.WebhookURL || a.SlackWebhookURL != b.SlackWebhookURL ||
		a.SMTPHost != b.SMTPHost || a.SMTPPort != b.SMTPPort || a.SMTPUser != b.SMTPUser ||
		a.SMTPPassword != b.SMTPPassword || a.SMTPFrom != b.SMTPFrom || a.SMTPEnabled != b.SMTPEnabled {
		return false
	}
	if len(a.SMTPTo) != len(b.SMTPTo) {
		return false
	}
	for i := range a.SMTPTo {
		if a.SMTPTo[i] != b.SMTPTo[i] {
			return false
		}
	}
	return true
}

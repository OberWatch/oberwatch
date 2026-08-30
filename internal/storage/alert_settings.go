package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/OberWatch/oberwatch/internal/config"
)

// Settings keys for dashboard-editable alert delivery configuration. These
// values are SQLite-authoritative: config.toml only seeds them once, on the
// first boot that finds them absent. See ImportAlertSettingsFromConfig.
const (
	keyAlertsWebhookURL      = "alerts.webhook_url"
	keyAlertsSlackWebhookURL = "alerts.slack_webhook_url"
	keyAlertsSMTPHost        = "alerts.smtp_host"
	keyAlertsSMTPPort        = "alerts.smtp_port"
	keyAlertsSMTPUser        = "alerts.smtp_user"
	keyAlertsSMTPPassword    = "alerts.smtp_password"
	keyAlertsSMTPFrom        = "alerts.smtp_from"
	keyAlertsSMTPTo          = "alerts.smtp_to"
	keyAlertsSMTPEnabled     = "alerts.smtp_enabled"
	keyAlertsImported        = "alerts.imported_from_config"
)

// AlertSettings is the SQLite-authoritative alert delivery configuration.
// Secret fields (SMTPPassword, WebhookURL, SlackWebhookURL) are included here
// for internal use (building the live dispatcher config); callers that expose
// this over an API must mask them.
//
//nolint:govet // keep fields grouped by destination for readability.
type AlertSettings struct {
	WebhookURL      string
	SlackWebhookURL string
	SMTPHost        string
	SMTPPort        int
	SMTPUser        string
	SMTPPassword    string
	SMTPFrom        string
	SMTPTo          []string
	SMTPEnabled     bool
}

// AlertSettingsPatch is a partial update. A nil field is left untouched; a
// non-nil field (including an empty string or empty slice) is applied,
// clearing the setting when the value is empty. This makes "absence preserves
// the existing value" and "explicit clear" unambiguous at the type level.
//
//nolint:govet // keep fields grouped by destination for readability.
type AlertSettingsPatch struct {
	WebhookURL      *string   `json:"webhook_url,omitempty"`
	SlackWebhookURL *string   `json:"slack_webhook_url,omitempty"`
	SMTPHost        *string   `json:"smtp_host,omitempty"`
	SMTPPort        *int      `json:"smtp_port,omitempty"`
	SMTPUser        *string   `json:"smtp_user,omitempty"`
	SMTPPassword    *string   `json:"smtp_password,omitempty"`
	SMTPFrom        *string   `json:"smtp_from,omitempty"`
	SMTPTo          *[]string `json:"smtp_to,omitempty"`
	SMTPEnabled     *bool     `json:"smtp_enabled,omitempty"`
}

// MergeAlertSettingsPatch applies patch to current in memory and returns the
// resulting settings. It performs no I/O, so callers can validate a candidate
// result before deciding whether to persist or live-apply it via
// ApplyAlertSettingsPatch.
func MergeAlertSettingsPatch(current AlertSettings, patch AlertSettingsPatch) AlertSettings {
	next := current

	if patch.WebhookURL != nil {
		next.WebhookURL = *patch.WebhookURL
	}
	if patch.SlackWebhookURL != nil {
		next.SlackWebhookURL = *patch.SlackWebhookURL
	}
	if patch.SMTPHost != nil {
		next.SMTPHost = *patch.SMTPHost
	}
	if patch.SMTPPort != nil {
		next.SMTPPort = *patch.SMTPPort
	}
	if patch.SMTPUser != nil {
		next.SMTPUser = *patch.SMTPUser
	}
	if patch.SMTPPassword != nil {
		next.SMTPPassword = *patch.SMTPPassword
	}
	if patch.SMTPFrom != nil {
		next.SMTPFrom = *patch.SMTPFrom
	}
	if patch.SMTPTo != nil {
		next.SMTPTo = append([]string(nil), *patch.SMTPTo...)
	}
	if patch.SMTPEnabled != nil {
		next.SMTPEnabled = *patch.SMTPEnabled
	}

	return next
}

// ToAlertsConfig converts the persisted settings into the config shape the
// alert dispatcher builds destinations from.
func (s AlertSettings) ToAlertsConfig() config.AlertsConfig {
	return config.AlertsConfig{
		WebhookURL:      s.WebhookURL,
		SlackWebhookURL: s.SlackWebhookURL,
		Email: config.EmailConfig{
			SMTPHost:     s.SMTPHost,
			SMTPPort:     s.SMTPPort,
			SMTPUser:     s.SMTPUser,
			SMTPPassword: s.SMTPPassword,
			From:         s.SMTPFrom,
			To:           append([]string(nil), s.SMTPTo...),
			Enabled:      s.SMTPEnabled,
		},
	}
}

// LoadAlertSettings reads the current SQLite-authoritative alert settings.
// Keys that were never set (or never imported) read back as zero values.
func LoadAlertSettings(ctx context.Context, store Store) (AlertSettings, error) {
	var settings AlertSettings
	var err error

	if settings.WebhookURL, _, err = store.GetSetting(ctx, keyAlertsWebhookURL); err != nil {
		return AlertSettings{}, fmt.Errorf("load %s: %w", keyAlertsWebhookURL, err)
	}
	if settings.SlackWebhookURL, _, err = store.GetSetting(ctx, keyAlertsSlackWebhookURL); err != nil {
		return AlertSettings{}, fmt.Errorf("load %s: %w", keyAlertsSlackWebhookURL, err)
	}
	if settings.SMTPHost, _, err = store.GetSetting(ctx, keyAlertsSMTPHost); err != nil {
		return AlertSettings{}, fmt.Errorf("load %s: %w", keyAlertsSMTPHost, err)
	}
	portRaw, _, err := store.GetSetting(ctx, keyAlertsSMTPPort)
	if err != nil {
		return AlertSettings{}, fmt.Errorf("load %s: %w", keyAlertsSMTPPort, err)
	}
	if strings.TrimSpace(portRaw) != "" {
		port, convErr := strconv.Atoi(strings.TrimSpace(portRaw))
		if convErr != nil {
			return AlertSettings{}, fmt.Errorf("parse %s: %w", keyAlertsSMTPPort, convErr)
		}
		settings.SMTPPort = port
	}
	if settings.SMTPUser, _, err = store.GetSetting(ctx, keyAlertsSMTPUser); err != nil {
		return AlertSettings{}, fmt.Errorf("load %s: %w", keyAlertsSMTPUser, err)
	}
	if settings.SMTPPassword, _, err = store.GetSetting(ctx, keyAlertsSMTPPassword); err != nil {
		return AlertSettings{}, fmt.Errorf("load %s: %w", keyAlertsSMTPPassword, err)
	}
	if settings.SMTPFrom, _, err = store.GetSetting(ctx, keyAlertsSMTPFrom); err != nil {
		return AlertSettings{}, fmt.Errorf("load %s: %w", keyAlertsSMTPFrom, err)
	}
	toRaw, _, err := store.GetSetting(ctx, keyAlertsSMTPTo)
	if err != nil {
		return AlertSettings{}, fmt.Errorf("load %s: %w", keyAlertsSMTPTo, err)
	}
	if strings.TrimSpace(toRaw) != "" {
		if unmarshalErr := json.Unmarshal([]byte(toRaw), &settings.SMTPTo); unmarshalErr != nil {
			return AlertSettings{}, fmt.Errorf("parse %s: %w", keyAlertsSMTPTo, unmarshalErr)
		}
	}
	enabledRaw, _, err := store.GetSetting(ctx, keyAlertsSMTPEnabled)
	if err != nil {
		return AlertSettings{}, fmt.Errorf("load %s: %w", keyAlertsSMTPEnabled, err)
	}
	settings.SMTPEnabled = strings.EqualFold(strings.TrimSpace(enabledRaw), "true")

	return settings, nil
}

// ImportAlertSettingsFromConfig seeds SQLite from config.toml exactly once,
// the first time it runs against a given database. It only fills keys that
// are entirely absent from SQLite; it never overwrites a value already saved
// there, including one an operator has explicitly cleared through the
// dashboard. That one-time behavior is tracked by the keyAlertsImported
// marker, not by re-checking key absence, so a later explicit clear can never
// be resurrected by a subsequent boot.
func ImportAlertSettingsFromConfig(ctx context.Context, store Store, cfg config.AlertsConfig) error {
	imported, found, err := store.GetSetting(ctx, keyAlertsImported)
	if err != nil {
		return fmt.Errorf("load %s: %w", keyAlertsImported, err)
	}
	if found && strings.EqualFold(strings.TrimSpace(imported), "true") {
		return nil
	}

	importIfAbsent := func(key, value string) error {
		if strings.TrimSpace(value) == "" {
			return nil
		}
		_, exists, getErr := store.GetSetting(ctx, key)
		if getErr != nil {
			return fmt.Errorf("load %s: %w", key, getErr)
		}
		if exists {
			return nil
		}
		if setErr := store.SetSetting(ctx, key, value); setErr != nil {
			return fmt.Errorf("import %s: %w", key, setErr)
		}
		return nil
	}

	if err := importIfAbsent(keyAlertsWebhookURL, cfg.WebhookURL); err != nil {
		return err
	}
	if err := importIfAbsent(keyAlertsSlackWebhookURL, cfg.SlackWebhookURL); err != nil {
		return err
	}
	if err := importIfAbsent(keyAlertsSMTPHost, cfg.Email.SMTPHost); err != nil {
		return err
	}
	if cfg.Email.SMTPPort > 0 {
		if err := importIfAbsent(keyAlertsSMTPPort, strconv.Itoa(cfg.Email.SMTPPort)); err != nil {
			return err
		}
	}
	if err := importIfAbsent(keyAlertsSMTPUser, cfg.Email.SMTPUser); err != nil {
		return err
	}
	if err := importIfAbsent(keyAlertsSMTPPassword, cfg.Email.SMTPPassword); err != nil {
		return err
	}
	if err := importIfAbsent(keyAlertsSMTPFrom, cfg.Email.From); err != nil {
		return err
	}
	if len(cfg.Email.To) > 0 {
		encoded, marshalErr := json.Marshal(cfg.Email.To)
		if marshalErr != nil {
			return fmt.Errorf("encode %s: %w", keyAlertsSMTPTo, marshalErr)
		}
		if err := importIfAbsent(keyAlertsSMTPTo, string(encoded)); err != nil {
			return err
		}
	}
	if cfg.Email.Enabled {
		if err := importIfAbsent(keyAlertsSMTPEnabled, "true"); err != nil {
			return err
		}
	}

	if err := store.SetSetting(ctx, keyAlertsImported, "true"); err != nil {
		return fmt.Errorf("set %s: %w", keyAlertsImported, err)
	}
	return nil
}

// SettingWrite is one key write (Clear false) or delete (Clear true) applied
// as part of an atomic settings batch.
type SettingWrite struct {
	Key   string
	Value string
	Clear bool
}

// AtomicSettingsStore is implemented by stores that can apply a batch of
// setting writes inside one transaction, so a storage error partway through a
// multi-field patch never leaves committed state partially updated.
// SQLiteStore implements this. ApplyAlertSettingsPatch uses it whenever the
// given store supports it, and falls back to writing each key individually
// otherwise so test doubles that only implement Store (e.g. fakes used by
// unrelated unit tests) keep working unmodified.
type AtomicSettingsStore interface {
	Store
	WriteSettingsBatch(ctx context.Context, ops []SettingWrite) error
}

// ApplyAlertSettingsPatch persists a partial update against current and
// returns the resulting settings. Only fields present in patch are written;
// a present field with an empty value clears the underlying setting. When
// store implements AtomicSettingsStore, every write in the patch (including
// clears) is applied in a single transaction: either the whole patch commits,
// or none of it does.
func ApplyAlertSettingsPatch(ctx context.Context, store Store, current AlertSettings, patch AlertSettingsPatch) (AlertSettings, error) {
	next := current
	ops := make([]SettingWrite, 0, 9)

	setOrClear := func(key string, value string) {
		if strings.TrimSpace(value) == "" {
			ops = append(ops, SettingWrite{Key: key, Clear: true})
			return
		}
		ops = append(ops, SettingWrite{Key: key, Value: value})
	}

	if patch.WebhookURL != nil {
		next.WebhookURL = *patch.WebhookURL
		setOrClear(keyAlertsWebhookURL, next.WebhookURL)
	}
	if patch.SlackWebhookURL != nil {
		next.SlackWebhookURL = *patch.SlackWebhookURL
		setOrClear(keyAlertsSlackWebhookURL, next.SlackWebhookURL)
	}
	if patch.SMTPHost != nil {
		next.SMTPHost = *patch.SMTPHost
		setOrClear(keyAlertsSMTPHost, next.SMTPHost)
	}
	if patch.SMTPPort != nil {
		next.SMTPPort = *patch.SMTPPort
		if next.SMTPPort == 0 {
			ops = append(ops, SettingWrite{Key: keyAlertsSMTPPort, Clear: true})
		} else {
			ops = append(ops, SettingWrite{Key: keyAlertsSMTPPort, Value: strconv.Itoa(next.SMTPPort)})
		}
	}
	if patch.SMTPUser != nil {
		next.SMTPUser = *patch.SMTPUser
		setOrClear(keyAlertsSMTPUser, next.SMTPUser)
	}
	if patch.SMTPPassword != nil {
		next.SMTPPassword = *patch.SMTPPassword
		setOrClear(keyAlertsSMTPPassword, next.SMTPPassword)
	}
	if patch.SMTPFrom != nil {
		next.SMTPFrom = *patch.SMTPFrom
		setOrClear(keyAlertsSMTPFrom, next.SMTPFrom)
	}
	if patch.SMTPTo != nil {
		next.SMTPTo = *patch.SMTPTo
		if len(next.SMTPTo) == 0 {
			ops = append(ops, SettingWrite{Key: keyAlertsSMTPTo, Clear: true})
		} else {
			encoded, marshalErr := json.Marshal(next.SMTPTo)
			if marshalErr != nil {
				return AlertSettings{}, fmt.Errorf("encode %s: %w", keyAlertsSMTPTo, marshalErr)
			}
			ops = append(ops, SettingWrite{Key: keyAlertsSMTPTo, Value: string(encoded)})
		}
	}
	if patch.SMTPEnabled != nil {
		next.SMTPEnabled = *patch.SMTPEnabled
		ops = append(ops, SettingWrite{Key: keyAlertsSMTPEnabled, Value: strconv.FormatBool(next.SMTPEnabled)})
	}

	if len(ops) == 0 {
		return next, nil
	}

	if atomicStore, ok := store.(AtomicSettingsStore); ok {
		if err := atomicStore.WriteSettingsBatch(ctx, ops); err != nil {
			return AlertSettings{}, fmt.Errorf("apply alert settings patch: %w", err)
		}
		return next, nil
	}

	for _, op := range ops {
		if op.Clear {
			if err := store.DeleteSetting(ctx, op.Key); err != nil {
				return AlertSettings{}, fmt.Errorf("clear %s: %w", op.Key, err)
			}
			continue
		}
		if err := store.SetSetting(ctx, op.Key, op.Value); err != nil {
			return AlertSettings{}, fmt.Errorf("set %s: %w", op.Key, err)
		}
	}

	return next, nil
}

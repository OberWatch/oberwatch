package config

import (
	"strings"
	"testing"
)

func TestValidateWebhookURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		raw        string
		wantSubstr string
	}{
		{name: "https url", raw: "https://alerts.example/hook"},
		{name: "http url with port and query", raw: "http://alerts.internal:8080/hook?token=abc"},
		{name: "uppercase scheme", raw: "HTTPS://alerts.example/hook"},
		{name: "surrounding whitespace trimmed", raw: "  https://alerts.example/hook  "},
		{name: "basic auth userinfo allowed", raw: "https://user:pass@alerts.example/hook"},
		{name: "empty", raw: "", wantSubstr: "must not be empty"},
		{name: "whitespace only", raw: "   ", wantSubstr: "must not be empty"},
		{name: "missing scheme", raw: "alerts.example/hook", wantSubstr: "scheme must be http or https"},
		{name: "missing scheme explains that the url must be absolute", raw: "alerts.example/hook", wantSubstr: "use an absolute URL"},
		{name: "scheme relative url", raw: "//alerts.example/hook", wantSubstr: "use an absolute URL"},
		{name: "ftp scheme", raw: "ftp://alerts.example/hook", wantSubstr: "scheme must be http or https"},
		{name: "file scheme", raw: "file:///etc/passwd", wantSubstr: "scheme must be http or https"},
		{name: "javascript scheme", raw: "javascript:alert(1)", wantSubstr: "scheme must be http or https"},
		{name: "opaque mailto", raw: "mailto:ops@example.com", wantSubstr: "scheme must be http or https"},
		{name: "missing host", raw: "https:///hook", wantSubstr: "must include a host"},
		{name: "port only", raw: "https://:8080/hook", wantSubstr: "must include a host"},
		{name: "embedded space", raw: "https://alerts.example/ho ok", wantSubstr: "whitespace or control"},
		{name: "embedded newline", raw: "https://alerts.example/ho\nok", wantSubstr: "whitespace or control"},
		{name: "trailing newline trimmed", raw: "https://alerts.example/hook\n"},
		{name: "fragment", raw: "https://alerts.example/hook#frag", wantSubstr: "must not contain a fragment"},
		{name: "garbage", raw: "://nope", wantSubstr: "must be a valid absolute URL"},
		{name: "invalid port", raw: "https://alerts.example:notaport/hook", wantSubstr: "must be a valid absolute URL"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateWebhookURL(tt.raw)
			if tt.wantSubstr == "" {
				if err != nil {
					t.Fatalf("ValidateWebhookURL(%q) error = %v, want nil", tt.raw, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateWebhookURL(%q) error = nil, want substring %q", tt.raw, tt.wantSubstr)
			}
			if !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("ValidateWebhookURL(%q) error = %q, want substring %q", tt.raw, err.Error(), tt.wantSubstr)
			}
		})
	}
}

func TestValidateSlackWebhookURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		raw        string
		wantSubstr string
	}{
		// The token segment keeps hyphens on purpose. A real Slack hook ends in 24
		// alphanumerics, and a fixture shaped like that trips secret scanning and
		// blocks the push. The validator only looks at scheme, host, port, userinfo,
		// query and the /services/ prefix, so the segment contents do not matter.
		{name: "full length slack webhook", raw: "https://hooks.slack.com/services/T0EXAMPLE/B0EXAMPLE/not-a-real-webhook-token"},
		{name: "uppercase host accepted", raw: "https://HOOKS.SLACK.COM/services/T/B/X"},
		{name: "explicit 443 accepted", raw: "https://hooks.slack.com:443/services/T/B/X"},
		{name: "empty", raw: "", wantSubstr: "must not be empty"},
		{name: "http scheme", raw: "http://hooks.slack.com/services/T/B/X", wantSubstr: "must use https"},
		{name: "wrong host", raw: "https://hooks.slack.com.evil.example/services/T/B/X", wantSubstr: "host must be hooks.slack.com"},
		{name: "lookalike host", raw: "https://hooks-slack.com/services/T/B/X", wantSubstr: "host must be hooks.slack.com"},
		{name: "subdomain host", raw: "https://api.hooks.slack.com/services/T/B/X", wantSubstr: "host must be hooks.slack.com"},
		{name: "custom port", raw: "https://hooks.slack.com:8443/services/T/B/X", wantSubstr: "must not use a custom port"},
		{name: "userinfo", raw: "https://user:pass@hooks.slack.com/services/T/B/X", wantSubstr: "must not contain credentials"},
		{name: "query string", raw: "https://hooks.slack.com/services/T/B/X?x=1", wantSubstr: "must not contain a query string"},
		{name: "wrong path", raw: "https://hooks.slack.com/api/chat.postMessage", wantSubstr: "path must start with /services/"},
		{name: "empty services path", raw: "https://hooks.slack.com/services/", wantSubstr: "path must start with /services/"},
		{name: "root path", raw: "https://hooks.slack.com/", wantSubstr: "path must start with /services/"},
		{name: "fragment", raw: "https://hooks.slack.com/services/T/B/X#f", wantSubstr: "must not contain a fragment"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateSlackWebhookURL(tt.raw)
			if tt.wantSubstr == "" {
				if err != nil {
					t.Fatalf("ValidateSlackWebhookURL(%q) error = %v, want nil", tt.raw, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateSlackWebhookURL(%q) error = nil, want substring %q", tt.raw, tt.wantSubstr)
			}
			if !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("ValidateSlackWebhookURL(%q) error = %q, want substring %q", tt.raw, err.Error(), tt.wantSubstr)
			}
		})
	}
}

func TestValidate_AlertDestinationURLs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		mutate     func(*Config)
		wantSubstr string
	}{
		{
			name: "empty destinations are valid",
			mutate: func(cfg *Config) {
				cfg.Alerts.WebhookURL = ""
				cfg.Alerts.SlackWebhookURL = ""
			},
		},
		{
			name: "valid destinations are accepted",
			mutate: func(cfg *Config) {
				cfg.Alerts.WebhookURL = "https://alerts.example/hook"
				cfg.Alerts.SlackWebhookURL = "https://hooks.slack.com/services/T/B/X"
			},
		},
		{
			name: "invalid webhook url is reported",
			mutate: func(cfg *Config) {
				cfg.Alerts.WebhookURL = "alerts.example/hook"
			},
			wantSubstr: "alerts.webhook_url scheme must be http or https",
		},
		{
			name: "non slack host is reported",
			mutate: func(cfg *Config) {
				cfg.Alerts.SlackWebhookURL = "https://example.com/services/T/B/X"
			},
			wantSubstr: "alerts.slack_webhook_url host must be hooks.slack.com",
		},
		{
			name: "http slack url is reported",
			mutate: func(cfg *Config) {
				cfg.Alerts.SlackWebhookURL = "http://hooks.slack.com/services/T/B/X"
			},
			wantSubstr: "alerts.slack_webhook_url must use https",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := DefaultConfig()
			tt.mutate(&cfg)
			err := Validate(cfg)
			if tt.wantSubstr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("Validate() error = %v, want substring %q", err, tt.wantSubstr)
			}
		})
	}
}

package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

// SlackWebhookHost is the only host accepted for alerts.slack_webhook_url.
const SlackWebhookHost = "hooks.slack.com"

// slackWebhookPathPrefix is the path prefix of Slack incoming webhooks.
const slackWebhookPathPrefix = "/services/"

// ValidateWebhookURL checks that raw is an absolute http or https URL with a host.
// Fragments, opaque URLs, and whitespace or control characters are rejected.
func ValidateWebhookURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return errors.New("must not be empty")
	}
	if strings.IndexFunc(trimmed, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return errors.New("must not contain whitespace or control characters")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return errors.New("must be a valid absolute URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		if parsed.Scheme == "" {
			return errors.New("scheme must be http or https; use an absolute URL like https://alerts.example/hook")
		}
		return fmt.Errorf("scheme must be http or https, got %q", parsed.Scheme)
	}
	if parsed.Opaque != "" {
		return errors.New("must be a hierarchical URL of the form scheme://host/path")
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return errors.New("must include a host")
	}
	if _, err := url.ParseRequestURI(trimmed); err != nil {
		return errors.New("must be a valid absolute URL")
	}
	if parsed.Fragment != "" || strings.Contains(trimmed, "#") {
		return errors.New("must not contain a fragment")
	}
	return nil
}

// ValidateSlackWebhookURL checks that raw is an https Slack incoming webhook URL
// on hooks.slack.com under /services/. Credentials in the URL, custom ports, and
// query strings are rejected.
func ValidateSlackWebhookURL(raw string) error {
	if err := ValidateWebhookURL(raw); err != nil {
		return err
	}

	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return errors.New("must be a valid absolute URL")
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return errors.New("must use https")
	}
	if !strings.EqualFold(parsed.Hostname(), SlackWebhookHost) {
		return fmt.Errorf("host must be %s", SlackWebhookHost)
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return errors.New("must not use a custom port")
	}
	if parsed.User != nil {
		return errors.New("must not contain credentials")
	}
	if parsed.RawQuery != "" || strings.Contains(raw, "?") {
		return errors.New("must not contain a query string")
	}
	if !strings.HasPrefix(parsed.Path, slackWebhookPathPrefix) || len(parsed.Path) == len(slackWebhookPathPrefix) {
		return fmt.Errorf("path must start with %s", slackWebhookPathPrefix)
	}
	return nil
}

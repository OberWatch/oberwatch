package alert

import (
	"net/url"
	"sort"
	"strings"
)

const redactedMarker = "[redacted]"

// RedactURL returns a log-safe form of a webhook URL. Only the scheme and host
// are kept; credentials, path, and query are replaced with a marker because
// webhook secrets commonly live in any of them.
func RedactURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return redactedMarker
	}
	var builder strings.Builder
	builder.WriteString(parsed.Scheme)
	builder.WriteString("://")
	builder.WriteString(parsed.Host)
	if parsed.Path != "" && parsed.Path != "/" {
		builder.WriteString("/")
		builder.WriteString(redactedMarker)
	}
	if parsed.RawQuery != "" {
		builder.WriteString("?")
		builder.WriteString(redactedMarker)
	}
	return builder.String()
}

// urlSecrets lists the substrings of a webhook URL that must never appear in
// logs or errors: the full URL, credentials, the path, and the query string.
func urlSecrets(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	secrets := make([]string, 0, 6)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if len(value) < 4 {
			return
		}
		for _, existing := range secrets {
			if existing == value {
				return
			}
		}
		secrets = append(secrets, value)
	}

	add(trimmed)
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return secrets
	}
	if parsed.User != nil {
		add(parsed.User.Username())
		if password, ok := parsed.User.Password(); ok {
			add(password)
		}
		add(parsed.User.String())
	}
	if parsed.Path != "" && parsed.Path != "/" {
		add(strings.TrimPrefix(parsed.Path, "/"))
		if parsed.RawPath != "" {
			add(strings.TrimPrefix(parsed.RawPath, "/"))
		}
		// Webhook tokens are often echoed back on their own, so cover each segment.
		for _, segment := range strings.Split(parsed.Path, "/") {
			add(segment)
		}
	}
	if parsed.RawQuery != "" {
		add(parsed.RawQuery)
		for _, values := range parsed.Query() {
			for _, value := range values {
				add(value)
			}
		}
	}
	// Replace longer secrets first so a short secret never breaks a longer match.
	sort.SliceStable(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	return secrets
}

// redactText replaces every known secret in text with the redaction marker.
func redactText(text string, secrets []string) string {
	for _, secret := range secrets {
		text = strings.ReplaceAll(text, secret, redactedMarker)
	}
	return text
}

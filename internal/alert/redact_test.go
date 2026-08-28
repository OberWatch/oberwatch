package alert

import (
	"strings"
	"testing"
)

func TestRedactURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "host only", raw: "https://alerts.example", want: "https://alerts.example"},
		{name: "root path", raw: "https://alerts.example/", want: "https://alerts.example"},
		{name: "path redacted", raw: "https://hooks.slack.com/services/T0/B0/xoxb", want: "https://hooks.slack.com/[redacted]"},
		{name: "userinfo dropped and query redacted", raw: "http://user:pass@alerts.example:8080/hook?token=abc", want: "http://alerts.example:8080/[redacted]?[redacted]"},
		{name: "query only", raw: "https://alerts.example?token=abc", want: "https://alerts.example?[redacted]"},
		{name: "invalid url", raw: "://nope", want: "[redacted]"},
		{name: "relative url", raw: "hook/path", want: "[redacted]"},
		{name: "empty", raw: "", want: "[redacted]"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := RedactURL(tt.raw); got != tt.want {
				t.Fatalf("RedactURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestRedactText(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test table fields explicit.
	tests := []struct {
		name        string
		raw         string
		text        string
		wantContain []string
		wantAbsent  []string
	}{
		{
			name:        "full url replaced",
			raw:         "https://alerts.example/hook/abcdef",
			text:        `Post "https://alerts.example/hook/abcdef": dial tcp: refused`,
			wantContain: []string{"[redacted]", "dial tcp: refused"},
			wantAbsent:  []string{"abcdef", "alerts.example/hook"},
		},
		{
			name:        "credentials and query values replaced",
			raw:         "https://bob:hunter22@alerts.example/hook?key=QUERYVAL&x=1",
			text:        "auth bob hunter22 failed key QUERYVAL",
			wantContain: []string{"auth bob [redacted] failed key [redacted]"},
			wantAbsent:  []string{"hunter22", "QUERYVAL"},
		},
		{
			name:        "short segments are left alone",
			raw:         "https://alerts.example/a/b",
			text:        "a b c",
			wantContain: []string{"a b c"},
		},
		{
			name:        "invalid url still redacts raw string",
			raw:         "://broken-secret",
			text:        "err ://broken-secret here",
			wantContain: []string{"err [redacted] here"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := redactText(tt.text, urlSecrets(tt.raw))
			for _, want := range tt.wantContain {
				if !strings.Contains(got, want) {
					t.Fatalf("redactText() = %q, want substring %q", got, want)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(got, absent) {
					t.Fatalf("redactText() = %q, must not contain %q", got, absent)
				}
			}
		})
	}
}

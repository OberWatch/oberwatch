package config

import (
	"strings"
	"testing"
)

func TestValidateEmailConfig(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test table fields explicit.
	tests := []struct {
		name       string
		email      EmailConfig
		wantSubstr string
	}{
		{
			name: "valid config passes",
			email: EmailConfig{
				SMTPHost: "smtp.example.com",
				SMTPPort: 587,
				From:     "alerts@example.com",
				To:       []string{"a@example.com", "b@example.com"},
			},
		},
		{
			name:       "empty host rejected",
			email:      EmailConfig{SMTPHost: "", SMTPPort: 587, From: "a@example.com", To: []string{"b@example.com"}},
			wantSubstr: "alerts.email.smtp_host",
		},
		{
			name:       "port out of range rejected",
			email:      EmailConfig{SMTPHost: "smtp.example.com", SMTPPort: 0, From: "a@example.com", To: []string{"b@example.com"}},
			wantSubstr: "alerts.email.smtp_port",
		},
		{
			name:       "port above range rejected",
			email:      EmailConfig{SMTPHost: "smtp.example.com", SMTPPort: 70000, From: "a@example.com", To: []string{"b@example.com"}},
			wantSubstr: "alerts.email.smtp_port",
		},
		{
			name:       "invalid from address rejected",
			email:      EmailConfig{SMTPHost: "smtp.example.com", SMTPPort: 587, From: "not-an-email", To: []string{"b@example.com"}},
			wantSubstr: "alerts.email.from",
		},
		{
			name:       "from with CRLF header injection rejected",
			email:      EmailConfig{SMTPHost: "smtp.example.com", SMTPPort: 587, From: "a@example.com\r\nBcc: evil@example.com", To: []string{"b@example.com"}},
			wantSubstr: "alerts.email.from",
		},
		{
			name:       "empty recipients rejected",
			email:      EmailConfig{SMTPHost: "smtp.example.com", SMTPPort: 587, From: "a@example.com", To: nil},
			wantSubstr: "alerts.email.to",
		},
		{
			name:       "recipient with CRLF header injection rejected",
			email:      EmailConfig{SMTPHost: "smtp.example.com", SMTPPort: 587, From: "a@example.com", To: []string{"b@example.com\r\nRCPT TO:<evil@example.com>"}},
			wantSubstr: "alerts.email.to[0]",
		},
		{
			name:       "host with whitespace rejected",
			email:      EmailConfig{SMTPHost: "smtp.example.com\r\nEVIL", SMTPPort: 587, From: "a@example.com", To: []string{"b@example.com"}},
			wantSubstr: "alerts.email.smtp_host",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			problems := ValidateEmailConfig(tt.email)
			if tt.wantSubstr == "" {
				if len(problems) != 0 {
					t.Fatalf("ValidateEmailConfig() = %v, want no problems", problems)
				}
				return
			}
			joined := strings.Join(problems, "; ")
			if !strings.Contains(joined, tt.wantSubstr) {
				t.Fatalf("ValidateEmailConfig() = %v, want substring %q", problems, tt.wantSubstr)
			}
		})
	}
}

func TestEnvelopeMailbox(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test table fields explicit.
	tests := []struct {
		name       string
		raw        string
		want       string
		wantErrSub string
	}{
		{name: "bare address unchanged", raw: "alerts@example.com", want: "alerts@example.com"},
		{name: "display name stripped", raw: "Alerts <alerts@example.com>", want: "alerts@example.com"},
		{name: "surrounding whitespace trimmed", raw: "  alerts@example.com  ", want: "alerts@example.com"},
		{name: "CRLF injection rejected", raw: "a@example.com\r\nRCPT TO:<evil@example.com>", wantErrSub: "CR or LF"},
		{name: "invalid address rejected", raw: "not-an-email", wantErrSub: "valid email address"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := EnvelopeMailbox(tt.raw)
			if tt.wantErrSub != "" {
				if err == nil {
					t.Fatalf("EnvelopeMailbox(%q) error = nil, want substring %q", tt.raw, tt.wantErrSub)
				}
				if !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("EnvelopeMailbox(%q) error = %q, want substring %q", tt.raw, err.Error(), tt.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("EnvelopeMailbox(%q) error = %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("EnvelopeMailbox(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

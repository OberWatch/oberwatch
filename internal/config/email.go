package config

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"unicode"
)

// ValidateEmailConfig checks alerts.email settings for use when email delivery
// is enabled. It defends against CR/LF header injection in the host, from
// address, and every recipient, since those values are placed directly into
// SMTP commands and message headers.
func ValidateEmailConfig(email EmailConfig) []string {
	problems := make([]string, 0)

	if err := validateSMTPHost(email.SMTPHost); err != nil {
		problems = append(problems, fmt.Sprintf("alerts.email.smtp_host %s", err))
	}
	if email.SMTPPort < 1 || email.SMTPPort > 65535 {
		problems = append(problems, fmt.Sprintf("alerts.email.smtp_port must be between 1 and 65535, got %d", email.SMTPPort))
	}
	if err := validateEmailAddress(email.From); err != nil {
		problems = append(problems, fmt.Sprintf("alerts.email.from %s", err))
	}
	if len(email.To) == 0 {
		problems = append(problems, "alerts.email.to must not be empty")
	}
	for i, to := range email.To {
		if err := validateEmailAddress(to); err != nil {
			problems = append(problems, fmt.Sprintf("alerts.email.to[%d] %s", i, err))
		}
	}

	return problems
}

func validateSMTPHost(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return errors.New("must not be empty")
	}
	if strings.IndexFunc(trimmed, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return errors.New("must not contain whitespace or control characters")
	}
	return nil
}

// validateEmailAddress checks that raw is a safe, parseable email address. CR
// and LF are rejected explicitly, before parsing, because a value that
// survives net/mail parsing could still carry an embedded header or SMTP
// command if it were ever allowed to contain a line break.
func validateEmailAddress(raw string) error {
	_, err := parseEmailAddress(raw)
	return err
}

// EnvelopeMailbox validates raw the same way validateEmailAddress does, then
// returns the bare mailbox address, e.g. "alice@example.com" out of
// "Alice <alice@example.com>". mail.ParseAddress accepts that display-name
// form, but it must never be placed unparsed into an SMTP MAIL FROM/RCPT TO
// command: every caller that builds an SMTP envelope (rather than a message
// header) must use this instead of the raw, validated string.
func EnvelopeMailbox(raw string) (string, error) {
	parsed, err := parseEmailAddress(raw)
	if err != nil {
		return "", err
	}
	return parsed.Address, nil
}

func parseEmailAddress(raw string) (*mail.Address, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("must not be empty")
	}
	if strings.ContainsAny(trimmed, "\r\n") {
		return nil, errors.New("must not contain CR or LF characters")
	}
	if strings.IndexFunc(trimmed, unicode.IsControl) >= 0 {
		return nil, errors.New("must not contain control characters")
	}
	parsed, err := mail.ParseAddress(trimmed)
	if err != nil {
		return nil, fmt.Errorf("must be a valid email address: %w", err)
	}
	return parsed, nil
}

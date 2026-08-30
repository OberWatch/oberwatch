package alert

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/OberWatch/oberwatch/internal/config"
)

// smtpImplicitTLSPort is the conventional port for implicit TLS (SMTPS).
// Every other port attempts opportunistic STARTTLS instead.
const smtpImplicitTLSPort = 465

//nolint:govet // keep smtp destination fields grouped by delivery role.
type smtpDestConfig struct {
	host     string
	port     int
	user     string
	password string
	// from and to are the configured, validated display forms used to
	// render RFC 5322 message headers (e.g. "Alerts <alerts@example.com>").
	from string
	to   []string
	// envelopeFrom and envelopeTo are the bare mailbox addresses used for the
	// SMTP envelope (MAIL FROM / RCPT TO). A display-name form must never
	// reach those commands raw: net/smtp writes it verbatim inside angle
	// brackets, so "Alerts <alerts@example.com>" would corrupt the command.
	envelopeFrom string
	envelopeTo   []string
}

// buildSMTPDestination validates email and, if valid, builds the destination
// the dispatcher delivers alerts to over SMTP. Validation defends against
// CR/LF header injection in the host, from address, and every recipient,
// since those values are placed directly into SMTP commands and message
// headers.
func buildSMTPDestination(email config.EmailConfig) (destination, error) {
	if problems := config.ValidateEmailConfig(email); len(problems) > 0 {
		return destination{}, errors.New(strings.Join(problems, "; "))
	}

	envelopeFrom, err := config.EnvelopeMailbox(email.From)
	if err != nil {
		return destination{}, fmt.Errorf("alerts.email.from %w", err)
	}
	envelopeTo := make([]string, len(email.To))
	for i, to := range email.To {
		addr, err := config.EnvelopeMailbox(to)
		if err != nil {
			return destination{}, fmt.Errorf("alerts.email.to[%d] %w", i, err)
		}
		envelopeTo[i] = addr
	}

	secrets := make([]string, 0, 1)
	if strings.TrimSpace(email.SMTPPassword) != "" {
		secrets = append(secrets, email.SMTPPassword)
	}

	return destination{
		kind:     KindEmail,
		redacted: fmt.Sprintf("smtp://%s:%d", email.SMTPHost, email.SMTPPort),
		secrets:  secrets,
		smtp: &smtpDestConfig{
			host:         email.SMTPHost,
			port:         email.SMTPPort,
			user:         email.SMTPUser,
			password:     email.SMTPPassword,
			from:         email.From,
			to:           append([]string(nil), email.To...),
			envelopeFrom: envelopeFrom,
			envelopeTo:   envelopeTo,
		},
	}, nil
}

// buildEmailBody renders a plain-text summary of the alert. It never includes
// destination secrets: only alert fields already safe to publish elsewhere
// (e.g. Slack, webhooks) go into it.
func buildEmailBody(event Alert) []byte {
	lines := []string{
		fmt.Sprintf("Oberwatch alert: %s", event.Type),
		fmt.Sprintf("Agent: %s", event.Agent),
		fmt.Sprintf("Severity: %s", event.Severity),
	}
	if event.ThresholdPct > 0 {
		lines = append(lines, fmt.Sprintf("Threshold: %.0f%%", event.ThresholdPct))
	}
	if event.LimitUSD > 0 || event.SpentUSD > 0 {
		lines = append(lines, fmt.Sprintf("Spent/Limit: $%.2f / $%.2f", event.SpentUSD, event.LimitUSD))
	}
	if event.Action != "" {
		lines = append(lines, fmt.Sprintf("Action: %s", event.Action))
	}
	message := strings.TrimSpace(event.Message)
	if message == "" {
		message = "n/a"
	}
	lines = append(lines, "", message)
	return []byte(strings.Join(lines, "\r\n"))
}

// buildEmailMessage renders the full RFC 5322 message for one delivery
// attempt. cfg.from, cfg.to, and cfg.host have already passed
// config.ValidateEmailConfig, which rejects CR/LF and other control
// characters, so building headers by direct interpolation here is safe.
func buildEmailMessage(cfg *smtpDestConfig, alertType Type, body []byte) []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: %s\r\n", cfg.from)
	fmt.Fprintf(&buf, "To: %s\r\n", strings.Join(cfg.to, ", "))
	fmt.Fprintf(&buf, "Subject: Oberwatch alert: %s\r\n", alertType)
	fmt.Fprintf(&buf, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	buf.WriteString("\r\n")
	buf.Write(body)
	buf.WriteString("\r\n")
	return buf.Bytes()
}

// tlsConfigFor returns the TLS client config used for STARTTLS and implicit
// TLS connections. Tests in this package override smtpTLSConfig directly to
// trust a self-signed test certificate; production dispatchers always use the
// system root pool.
func (d *AlertDispatcher) tlsConfigFor(host string) *tls.Config {
	if d.smtpTLSConfig != nil {
		return d.smtpTLSConfig(host)
	}
	return &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
}

// useImplicitTLS reports whether port should be dialed as implicit TLS
// (SMTPS) rather than plaintext-then-STARTTLS. Tests in this package override
// smtpImplicitTLS so they can exercise the implicit-TLS path on a local
// listener that isn't bound to port 465.
func (d *AlertDispatcher) useImplicitTLS(port int) bool {
	if d.smtpImplicitTLS != nil {
		return d.smtpImplicitTLS(port)
	}
	return port == smtpImplicitTLSPort
}

// sendEmail delivers one alert over SMTP: dial (implicit TLS or plaintext),
// opportunistic STARTTLS, AUTH when a user is configured, then MAIL/RCPT/DATA
// for every recipient in a single transaction. The whole attempt is bounded by
// d.attemptTimeout; a background goroutine closes the connection when that
// deadline (or dispatcher shutdown) fires, since net/smtp does not accept a
// context directly.
func (d *AlertDispatcher) sendEmail(item job) error {
	cfg := item.dest.smtp

	requestCtx, cancel := context.WithTimeout(d.ctx, d.attemptTimeout)
	defer cancel()

	addr := net.JoinHostPort(cfg.host, strconv.Itoa(cfg.port))
	conn, err := (&net.Dialer{}).DialContext(requestCtx, "tcp", addr)
	if err != nil {
		return &deliveryError{err: fmt.Errorf("dial %s: %w", item.dest.redacted, sanitizeError(err, item.dest)), retryable: true}
	}
	if d.useImplicitTLS(cfg.port) {
		conn = tls.Client(conn, d.tlsConfigFor(cfg.host))
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-requestCtx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()

	client, err := smtp.NewClient(conn, cfg.host)
	if err != nil {
		return &deliveryError{err: fmt.Errorf("smtp handshake %s: %w", item.dest.redacted, sanitizeError(err, item.dest)), retryable: true}
	}
	defer func() {
		_ = client.Close()
	}()

	// Alert content must never travel in plaintext. Every port other than the
	// implicit-TLS one (465) must upgrade with STARTTLS; a server that
	// doesn't advertise it is a hard failure, not a silent fallback to
	// plaintext delivery.
	if !d.useImplicitTLS(cfg.port) {
		ok, _ := client.Extension("STARTTLS")
		if !ok {
			return &deliveryError{err: fmt.Errorf("smtp %s does not support STARTTLS: refusing to send in plaintext", item.dest.redacted), retryable: true}
		}
		if tlsErr := client.StartTLS(d.tlsConfigFor(cfg.host)); tlsErr != nil {
			return &deliveryError{err: fmt.Errorf("starttls %s: %w", item.dest.redacted, sanitizeError(tlsErr, item.dest)), retryable: true}
		}
	}

	if strings.TrimSpace(cfg.user) != "" {
		if state, ok := client.TLSConnectionState(); !ok || !state.HandshakeComplete {
			return &deliveryError{err: fmt.Errorf("smtp auth to %s refused: connection is not encrypted", item.dest.redacted)}
		}
		if authErr := client.Auth(smtp.PlainAuth("", cfg.user, cfg.password, cfg.host)); authErr != nil {
			return classifySMTPError("auth", item.dest, authErr)
		}
	}

	if mailErr := client.Mail(cfg.envelopeFrom); mailErr != nil {
		return classifySMTPError("mail from", item.dest, mailErr)
	}
	for _, rcpt := range cfg.envelopeTo {
		if rcptErr := client.Rcpt(rcpt); rcptErr != nil {
			return classifySMTPError("rcpt to", item.dest, rcptErr)
		}
	}

	writer, err := client.Data()
	if err != nil {
		return classifySMTPError("data", item.dest, err)
	}
	if _, err := writer.Write(buildEmailMessage(cfg, item.event.Type, item.body)); err != nil {
		return classifySMTPError("write message", item.dest, err)
	}
	if err := writer.Close(); err != nil {
		return classifySMTPError("close message", item.dest, err)
	}

	_ = client.Quit()
	return nil
}

// classifySMTPError wraps a protocol-level SMTP failure as a deliveryError,
// treating 4xx replies as transient (retryable) and 5xx as permanent, matching
// the meaning those status codes have in the SMTP spec.
func classifySMTPError(step string, dest destination, err error) error {
	var protoErr *textproto.Error
	retryable := true
	statusCode := 0
	if errors.As(err, &protoErr) {
		statusCode = protoErr.Code
		retryable = protoErr.Code >= 400 && protoErr.Code < 500
	}
	return &deliveryError{
		err:        fmt.Errorf("smtp %s to %s: %w", step, dest.redacted, sanitizeError(err, dest)),
		statusCode: statusCode,
		retryable:  retryable,
	}
}

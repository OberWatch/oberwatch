package alert

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/OberWatch/oberwatch/internal/config"
)

func newTestLoggerTo(w *strings.Builder) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, nil))
}

func newSMTPDispatcher(t *testing.T, server *fakeSMTPServer, email config.EmailConfig, rootCAs *x509.CertPool) *AlertDispatcher {
	t.Helper()
	host, port := server.hostPort()
	email.SMTPHost = host
	email.SMTPPort = port
	email.Enabled = true

	dispatcher, err := New(Options{
		Config:         config.AlertsConfig{Email: email},
		AttemptTimeout: 2 * time.Second,
		Clock:          newFakeClock(),
		Workers:        1,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if rootCAs != nil {
		dispatcher.smtpTLSConfig = func(host string) *tls.Config {
			return &tls.Config{ServerName: host, RootCAs: rootCAs, MinVersion: tls.VersionTLS12}
		}
	}
	t.Cleanup(dispatcher.Close)
	return dispatcher
}

func TestAlertDispatcher_SMTPRefusesPlaintextWhenSTARTTLSUnsupported(t *testing.T) {
	t.Parallel()

	// This fake server never advertises STARTTLS, so a correct dispatcher
	// must refuse to hand over alert content rather than silently falling
	// back to a plaintext transaction.
	server := newFakeSMTPServer(t)
	var logs strings.Builder
	logger := newTestLoggerTo(&logs)

	host, port := server.hostPort()
	dispatcher, err := New(Options{
		Config: config.AlertsConfig{Email: config.EmailConfig{
			Enabled:  true,
			SMTPHost: host,
			SMTPPort: port,
			From:     "alerts@example.com",
			To:       []string{"a@example.com", "b@example.com"},
		}},
		AttemptTimeout: time.Second,
		Logger:         logger,
		Clock:          newFakeClock(),
		BackoffBase:    time.Millisecond,
		Workers:        1,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(dispatcher.Close)

	dispatcher.Dispatch(context.Background(), NewAgentKilledAlert("agent-a", "runaway"))
	drain(t, dispatcher)

	if len(server.recordedSessions()) != 0 {
		t.Fatal("a message was sent despite the server never advertising STARTTLS")
	}
	stats := dispatcher.Stats()
	if stats.Failed != 1 || stats.Delivered != 0 {
		t.Fatalf("stats = %+v, want failed=1 delivered=0", stats)
	}
	if !strings.Contains(logs.String(), "STARTTLS") {
		t.Fatalf("logs = %q, want a mention of the STARTTLS failure", logs.String())
	}
}

func TestAlertDispatcher_SMTPStartTLSUpgradesAndAuthenticates(t *testing.T) {
	t.Parallel()

	cert, pool := generateTestTLSCert(t)
	server := newFakeSMTPServer(t)
	server.tlsConf = &tls.Config{Certificates: []tls.Certificate{cert}}
	server.supportsSTARTTLS = true
	server.supportsAuth = true
	server.authUser = "smtp-user"
	server.authPass = "correct-horse"

	dispatcher := newSMTPDispatcher(t, server, config.EmailConfig{
		From:         "alerts@example.com",
		To:           []string{"a@example.com"},
		SMTPUser:     "smtp-user",
		SMTPPassword: "correct-horse",
	}, pool)

	dispatcher.Dispatch(context.Background(), NewAgentKilledAlert("agent-a", "runaway"))
	drain(t, dispatcher)

	sessions := server.recordedSessions()
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	if !sessions[0].usedTLS {
		t.Fatal("usedTLS = false, want STARTTLS upgrade")
	}
	if !sessions[0].usedAuth {
		t.Fatal("usedAuth = false, want AUTH PLAIN to have succeeded")
	}

	stats := dispatcher.Stats()
	if stats.Delivered != 1 {
		t.Fatalf("stats = %+v, want delivered=1", stats)
	}
}

func TestAlertDispatcher_SMTPImplicitTLS(t *testing.T) {
	t.Parallel()

	cert, pool := generateTestTLSCert(t)
	server := newFakeImplicitTLSSMTPServer(t, &tls.Config{Certificates: []tls.Certificate{cert}})

	dispatcher := newSMTPDispatcher(t, server, config.EmailConfig{
		From: "alerts@example.com",
		To:   []string{"a@example.com"},
	}, pool)
	dispatcher.smtpImplicitTLS = func(int) bool { return true }

	dispatcher.Dispatch(context.Background(), NewAgentKilledAlert("agent-a", "runaway"))
	drain(t, dispatcher)

	sessions := server.recordedSessions()
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	if !sessions[0].usedTLS {
		t.Fatal("usedTLS = false, want implicit TLS session")
	}

	stats := dispatcher.Stats()
	if stats.Delivered != 1 {
		t.Fatalf("stats = %+v, want delivered=1", stats)
	}
}

func TestAlertDispatcher_SMTPEnvelopeStripsDisplayNames(t *testing.T) {
	t.Parallel()

	// From/To use a display-name form. Headers may keep it, but the SMTP
	// envelope (MAIL FROM / RCPT TO) must carry only the bare mailbox: the
	// raw display-name string, written verbatim into an SMTP command,
	// would otherwise land inside net/smtp's own angle brackets and corrupt
	// the command.
	cert, pool := generateTestTLSCert(t)
	server := newFakeSMTPServer(t)
	server.tlsConf = &tls.Config{Certificates: []tls.Certificate{cert}}
	server.supportsSTARTTLS = true
	dispatcher := newSMTPDispatcher(t, server, config.EmailConfig{
		From: `Alerts <alerts@example.com>`,
		To:   []string{`Ops Team <ops@example.com>`, "b@example.com"},
	}, pool)

	dispatcher.Dispatch(context.Background(), NewAgentKilledAlert("agent-a", "runaway"))
	drain(t, dispatcher)

	sessions := server.recordedSessions()
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	session := sessions[0]
	if session.from != "alerts@example.com" {
		t.Fatalf("envelope MAIL FROM = %q, want the bare mailbox alerts@example.com", session.from)
	}
	if len(session.to) != 2 || session.to[0] != "ops@example.com" || session.to[1] != "b@example.com" {
		t.Fatalf("envelope RCPT TO = %v, want bare mailboxes [ops@example.com b@example.com]", session.to)
	}
	if !strings.Contains(session.data, "From: Alerts <alerts@example.com>") {
		t.Fatalf("message headers = %q, want the From header to keep the display name", session.data)
	}
	if !strings.Contains(session.data, "To: Ops Team <ops@example.com>, b@example.com") {
		t.Fatalf("message headers = %q, want the To header to keep the display name", session.data)
	}

	stats := dispatcher.Stats()
	if stats.Delivered != 1 || stats.Failed != 0 {
		t.Fatalf("stats = %+v, want delivered=1 failed=0", stats)
	}
}

func TestAlertDispatcher_SMTPAuthRequiresEncryption(t *testing.T) {
	t.Parallel()

	server := newFakeSMTPServer(t)
	server.supportsAuth = true
	server.allowAuthNoTLS = true
	server.authUser = "smtp-user"
	server.authPass = "correct-horse"

	var logs strings.Builder
	logger := newTestLoggerTo(&logs)

	dispatcher, err := New(Options{
		Config: config.AlertsConfig{Email: func() config.EmailConfig {
			host, port := server.hostPort()
			return config.EmailConfig{
				Enabled:      true,
				SMTPHost:     host,
				SMTPPort:     port,
				SMTPUser:     "smtp-user",
				SMTPPassword: "correct-horse",
				From:         "alerts@example.com",
				To:           []string{"a@example.com"},
			}
		}()},
		AttemptTimeout: time.Second,
		Logger:         logger,
		Clock:          newFakeClock(),
		BackoffBase:    time.Millisecond,
		Workers:        1,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(dispatcher.Close)

	dispatcher.Dispatch(context.Background(), NewAgentKilledAlert("agent-a", "runaway"))
	drain(t, dispatcher)

	if len(server.recordedSessions()) != 0 {
		t.Fatal("a message was sent despite refusing to authenticate over plaintext")
	}
	stats := dispatcher.Stats()
	if stats.Failed != 1 {
		t.Fatalf("stats = %+v, want failed=1", stats)
	}
	if strings.Contains(logs.String(), "correct-horse") {
		t.Fatalf("logs leak smtp password: %s", logs.String())
	}
}

func TestAlertDispatcher_SMTPRejectedRecipientDoesNotLeakSecrets(t *testing.T) {
	t.Parallel()

	server := newFakeSMTPServer(t)
	server.rejectRecipients = map[string]int{"bad@example.com": 550}

	var logs strings.Builder
	logger := newTestLoggerTo(&logs)

	dispatcher, err := New(Options{
		Config: config.AlertsConfig{Email: func() config.EmailConfig {
			host, port := server.hostPort()
			return config.EmailConfig{
				Enabled:      true,
				SMTPHost:     host,
				SMTPPort:     port,
				SMTPUser:     "smtp-user",
				SMTPPassword: "top-secret",
				From:         "alerts@example.com",
				To:           []string{"bad@example.com"},
			}
		}()},
		AttemptTimeout: time.Second,
		Logger:         logger,
		Clock:          newFakeClock(),
		BackoffBase:    time.Millisecond,
		Workers:        1,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(dispatcher.Close)

	dispatcher.Dispatch(context.Background(), NewAgentKilledAlert("agent-a", "runaway"))
	drain(t, dispatcher)

	stats := dispatcher.Stats()
	if stats.Failed != 1 || stats.Delivered != 0 {
		t.Fatalf("stats = %+v, want failed=1 delivered=0", stats)
	}
	if strings.Contains(logs.String(), "top-secret") {
		t.Fatalf("logs leak smtp password: %s", logs.String())
	}
}

func TestAlertDispatcher_SMTPTimeoutBound(t *testing.T) {
	t.Parallel()

	server := newFakeSMTPServerWithHang(t, true)

	host, port := server.hostPort()
	dispatcher, err := New(Options{
		Config: config.AlertsConfig{Email: config.EmailConfig{
			Enabled:  true,
			SMTPHost: host,
			SMTPPort: port,
			From:     "alerts@example.com",
			To:       []string{"a@example.com"},
		}},
		AttemptTimeout: 300 * time.Millisecond,
		Clock:          newFakeClock(),
		BackoffBase:    time.Millisecond,
		Workers:        1,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(dispatcher.Close)

	started := time.Now()
	dispatcher.Dispatch(context.Background(), NewAgentKilledAlert("agent-a", "runaway"))
	drain(t, dispatcher)
	elapsed := time.Since(started)

	if elapsed > 5*time.Second {
		t.Fatalf("elapsed = %s, want bounded by attempt timeout plus retries", elapsed)
	}
	stats := dispatcher.Stats()
	if stats.Failed != 1 {
		t.Fatalf("stats = %+v, want failed=1", stats)
	}
}

func TestAlertDispatcher_SMTPRejectsInvalidConfigAtStartup(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep test table fields explicit.
	tests := []struct {
		name       string
		email      config.EmailConfig
		wantSubstr string
	}{
		{
			name:       "missing host",
			email:      config.EmailConfig{Enabled: true, SMTPPort: 587, From: "a@example.com", To: []string{"b@example.com"}},
			wantSubstr: "alerts.email.smtp_host",
		},
		{
			name:       "no recipients",
			email:      config.EmailConfig{Enabled: true, SMTPHost: "smtp.example.com", SMTPPort: 587, From: "a@example.com"},
			wantSubstr: "alerts.email.to",
		},
		{
			name:       "from with header injection",
			email:      config.EmailConfig{Enabled: true, SMTPHost: "smtp.example.com", SMTPPort: 587, From: "a@example.com\r\nBcc: evil@example.com", To: []string{"b@example.com"}},
			wantSubstr: "alerts.email.from",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dispatcher, err := New(Options{Config: config.AlertsConfig{Email: tt.email}})
			if err == nil {
				dispatcher.Close()
				t.Fatal("New() error = nil, want validation error")
			}
			if !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("New() error = %q, want substring %q", err.Error(), tt.wantSubstr)
			}
		})
	}
}

func TestAlertDispatcher_SMTPDedupesThresholdsPerPeriod(t *testing.T) {
	t.Parallel()

	cert, pool := generateTestTLSCert(t)
	server := newFakeSMTPServer(t)
	server.tlsConf = &tls.Config{Certificates: []tls.Certificate{cert}}
	server.supportsSTARTTLS = true
	dispatcher := newSMTPDispatcher(t, server, config.EmailConfig{
		From: "alerts@example.com",
		To:   []string{"a@example.com"},
	}, pool)

	periodStart := time.Date(2026, time.March, 26, 0, 0, 0, 0, time.UTC)
	base := NewBudgetThresholdAlert("agent-a", 80, 8, 10, "downgrade", periodStart)
	dispatcher.Dispatch(context.Background(), base)
	dispatcher.Dispatch(context.Background(), base)
	drain(t, dispatcher)

	if got := len(server.recordedSessions()); got != 1 {
		t.Fatalf("sessions = %d, want 1 (duplicate threshold deduped)", got)
	}
}

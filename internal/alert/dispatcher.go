package alert

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OberWatch/oberwatch/internal/config"
)

// Delivery limits shared by every destination.
const (
	// DefaultAttemptTimeout bounds a single HTTP attempt when no timeout is configured.
	DefaultAttemptTimeout = 5 * time.Second
	// MaxAttemptTimeout is the hard upper bound for a single HTTP attempt.
	MaxAttemptTimeout = 10 * time.Second
	// MaxRetries is the number of retries after the first attempt for retryable failures.
	MaxRetries = 3
	// DefaultQueueSize is the number of pending deliveries held before new alerts are dropped.
	DefaultQueueSize = 256
	// DefaultWorkers is the number of goroutines draining the delivery queue.
	DefaultWorkers = 2
	// DefaultBackoffBase is the first retry delay; later delays double it.
	DefaultBackoffBase = 500 * time.Millisecond

	maxBackoff            = 8 * time.Second
	maxErrorBodyBytes     = 256
	saturationLogInterval = 30 * time.Second
)

// Destination kinds reported in logs and stats.
const (
	KindWebhook = "webhook"
	KindSlack   = "slack"
	KindEmail   = "email"
)

// Options configures an AlertDispatcher. Zero values fall back to the defaults above.
//
//nolint:govet // field grouping is deliberate.
type Options struct {
	Config         config.AlertsConfig
	Logger         *slog.Logger
	HTTPClient     *http.Client
	Clock          Clock
	AttemptTimeout time.Duration
	BackoffBase    time.Duration
	QueueSize      int
	Workers        int
}

// Stats is a snapshot of dispatcher counters.
type Stats struct {
	Enqueued      uint64
	Delivered     uint64
	Failed        uint64
	Dropped       uint64
	Retries       uint64
	QueueDepth    int
	QueueCapacity int
}

type destinationSecrets struct {
	values []string
}

type destination struct {
	kind     string
	url      string
	redacted string
	secrets  *destinationSecrets
	smtp     *smtpDestConfig
}

func (d destination) secretValues() []string {
	if d.secrets == nil {
		return nil
	}
	return d.secrets.values
}

//nolint:govet // field grouping is deliberate.
type job struct {
	dest  destination
	body  []byte
	event Alert
}

// AlertDispatcher routes alerts to configured destinations through a bounded
// queue so callers on the proxy hot path never wait on webhook latency.
//
//nolint:revive,govet // Name required by spec; field grouping is deliberate.
type AlertDispatcher struct {
	httpClient     *http.Client
	logger         *slog.Logger
	clock          Clock
	attemptTimeout time.Duration
	backoffBase    time.Duration

	destMu       sync.RWMutex
	destinations []destination

	// smtpTLSConfig and smtpImplicitTLS are test-only overrides (nil in
	// production) that let this package's tests exercise STARTTLS and
	// implicit TLS against a local fake server with a self-signed
	// certificate and a non-standard port.
	smtpTLSConfig   func(host string) *tls.Config
	smtpImplicitTLS func(port int) bool

	queue  chan job
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu             sync.Mutex
	closed         bool
	thresholdDedup map[string]struct{}

	pending   atomic.Int64
	enqueued  atomic.Uint64
	delivered atomic.Uint64
	failed    atomic.Uint64
	dropped   atomic.Uint64
	retries   atomic.Uint64

	saturationMu      sync.Mutex
	lastSaturationLog time.Time
	droppedSinceLog   uint64
}

// NewDispatcher creates a dispatcher from alerts config using the default HTTP client.
func NewDispatcher(cfg config.AlertsConfig, attemptTimeout time.Duration, logger *slog.Logger) (*AlertDispatcher, error) {
	return New(Options{Config: cfg, AttemptTimeout: attemptTimeout, Logger: logger})
}

// NewDispatcherWithClient creates a dispatcher with a custom HTTP client.
func NewDispatcherWithClient(cfg config.AlertsConfig, attemptTimeout time.Duration, logger *slog.Logger, client *http.Client) (*AlertDispatcher, error) {
	return New(Options{Config: cfg, AttemptTimeout: attemptTimeout, Logger: logger, HTTPClient: client})
}

// New creates a dispatcher and starts its delivery workers. It fails when a
// configured destination URL is invalid. Call Close to stop the workers.
func New(opts Options) (*AlertDispatcher, error) {
	destinations, err := buildDestinations(opts.Config)
	if err != nil {
		return nil, err
	}

	attemptTimeout := opts.AttemptTimeout
	if attemptTimeout <= 0 {
		attemptTimeout = DefaultAttemptTimeout
	}
	if attemptTimeout > MaxAttemptTimeout {
		attemptTimeout = MaxAttemptTimeout
	}
	backoffBase := opts.BackoffBase
	if backoffBase <= 0 {
		backoffBase = DefaultBackoffBase
	}
	queueSize := opts.QueueSize
	if queueSize <= 0 {
		queueSize = DefaultQueueSize
	}
	workers := opts.Workers
	if workers <= 0 {
		workers = DefaultWorkers
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	clock := opts.Clock
	if clock == nil {
		clock = realClock{}
	}

	ctx, cancel := context.WithCancel(context.Background())
	dispatcher := &AlertDispatcher{
		httpClient:     client,
		logger:         opts.Logger,
		clock:          clock,
		destinations:   destinations,
		attemptTimeout: attemptTimeout,
		backoffBase:    backoffBase,
		queue:          make(chan job, queueSize),
		ctx:            ctx,
		cancel:         cancel,
		thresholdDedup: make(map[string]struct{}),
	}
	for i := 0; i < workers; i++ {
		dispatcher.wg.Add(1)
		go dispatcher.worker()
	}
	return dispatcher, nil
}

func buildDestinations(cfg config.AlertsConfig) ([]destination, error) {
	destinations := make([]destination, 0, 2)

	if webhookURL := strings.TrimSpace(cfg.WebhookURL); webhookURL != "" {
		if err := config.ValidateWebhookURL(webhookURL); err != nil {
			return nil, fmt.Errorf("alerts.webhook_url %w", err)
		}
		destinations = append(destinations, newDestination(KindWebhook, webhookURL))
	}
	if slackURL := strings.TrimSpace(cfg.SlackWebhookURL); slackURL != "" {
		if err := config.ValidateSlackWebhookURL(slackURL); err != nil {
			return nil, fmt.Errorf("alerts.slack_webhook_url %w", err)
		}
		destinations = append(destinations, newDestination(KindSlack, slackURL))
	}
	if cfg.Email.Enabled {
		dest, err := buildSMTPDestination(cfg.Email)
		if err != nil {
			return nil, err
		}
		destinations = append(destinations, dest)
	}
	return destinations, nil
}

func newDestination(kind string, rawURL string) destination {
	return destination{
		kind:     kind,
		url:      rawURL,
		redacted: RedactURL(rawURL),
		secrets:  &destinationSecrets{values: urlSecrets(rawURL)},
	}
}

// Dispatch queues the alert for delivery to every configured destination and
// returns immediately. When the queue is full the alert is dropped and counted.
func (d *AlertDispatcher) Dispatch(_ context.Context, event Alert) {
	if d == nil {
		return
	}
	destinations := d.currentDestinations()
	if len(destinations) == 0 {
		return
	}
	if d.shouldSuppress(event) {
		return
	}

	for _, dest := range destinations {
		body, err := encodePayload(dest.kind, event)
		if err != nil {
			d.failed.Add(1)
			d.logWarn("alert payload encoding failed", err, dest, event, 0)
			continue
		}
		d.enqueue(job{dest: dest, body: body, event: event})
	}
}

// currentDestinations returns a snapshot of the destinations Dispatch should
// fan out to right now, safe to read while UpdateConfig swaps them out.
func (d *AlertDispatcher) currentDestinations() []destination {
	d.destMu.RLock()
	defer d.destMu.RUnlock()
	return d.destinations
}

// UpdateConfig atomically replaces the dispatcher's destinations, so alert
// settings changes apply to the next Dispatch call without a process restart.
// It validates cfg before applying; an invalid config is rejected and the
// dispatcher keeps delivering to whatever it already had configured.
func (d *AlertDispatcher) UpdateConfig(cfg config.AlertsConfig) error {
	if d == nil {
		return nil
	}
	destinations, err := buildDestinations(cfg)
	if err != nil {
		return err
	}
	d.destMu.Lock()
	d.destinations = destinations
	d.destMu.Unlock()
	return nil
}

func (d *AlertDispatcher) enqueue(item job) {
	d.mu.Lock()
	closed := d.closed
	d.mu.Unlock()
	if closed {
		d.dropped.Add(1)
		return
	}

	d.pending.Add(1)
	select {
	case d.queue <- item:
		d.enqueued.Add(1)
	default:
		d.pending.Add(-1)
		d.dropped.Add(1)
		d.logSaturation(item)
	}
}

// Drain blocks until every queued delivery has finished or ctx is done.
func (d *AlertDispatcher) Drain(ctx context.Context) error {
	if d == nil {
		return nil
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if d.pending.Load() == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("drain alert queue: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// Close stops accepting alerts, cancels in-flight attempts, and waits for the
// workers to exit. Queued alerts that have not started are discarded.
func (d *AlertDispatcher) Close() {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	d.mu.Unlock()

	d.cancel()
	d.wg.Wait()
}

// Stats returns a snapshot of delivery counters.
func (d *AlertDispatcher) Stats() Stats {
	if d == nil {
		return Stats{}
	}
	return Stats{
		Enqueued:      d.enqueued.Load(),
		Delivered:     d.delivered.Load(),
		Failed:        d.failed.Load(),
		Dropped:       d.dropped.Load(),
		Retries:       d.retries.Load(),
		QueueDepth:    len(d.queue),
		QueueCapacity: cap(d.queue),
	}
}

func (d *AlertDispatcher) worker() {
	defer d.wg.Done()
	for {
		select {
		case <-d.ctx.Done():
			return
		case item := <-d.queue:
			d.deliver(item)
			d.pending.Add(-1)
		}
	}
}

func (d *AlertDispatcher) deliver(item job) {
	var lastErr error
	attempts := 0
	for attempt := 0; attempt <= MaxRetries; attempt++ {
		if attempt > 0 {
			if !d.wait(d.backoff(attempt)) {
				lastErr = fmt.Errorf("alert delivery cancelled before retry: %w", d.ctx.Err())
				break
			}
			d.retries.Add(1)
		}
		attempts++

		err := d.sendOnce(item)
		if err == nil {
			d.delivered.Add(1)
			return
		}
		lastErr = err
		if d.ctx.Err() != nil || !isRetryable(err) {
			break
		}
	}

	d.failed.Add(1)
	d.logWarn("alert delivery failed", lastErr, item.dest, item.event, attempts)
}

func (d *AlertDispatcher) backoff(attempt int) time.Duration {
	delay := d.backoffBase
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= maxBackoff {
			return maxBackoff
		}
	}
	if delay > maxBackoff {
		return maxBackoff
	}
	return delay
}

func (d *AlertDispatcher) wait(delay time.Duration) bool {
	select {
	case <-d.ctx.Done():
		return false
	case <-d.clock.After(delay):
		return true
	}
}

// deliveryError is a single failed attempt. Its message never contains the destination URL.
type deliveryError struct {
	err        error
	statusCode int
	retryable  bool
}

func (e *deliveryError) Error() string {
	return e.err.Error()
}

func (e *deliveryError) Unwrap() error {
	return e.err
}

func isRetryable(err error) bool {
	var attemptErr *deliveryError
	if errors.As(err, &attemptErr) {
		return attemptErr.retryable
	}
	return false
}

func retryableStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}

// statusOf returns the HTTP status of the last attempt, or 0 when the attempt
// failed before a response arrived.
func statusOf(err error) int {
	var attemptErr *deliveryError
	if errors.As(err, &attemptErr) {
		return attemptErr.statusCode
	}
	return 0
}

func (d *AlertDispatcher) sendOnce(item job) error {
	if item.dest.kind == KindEmail {
		return d.sendEmail(item)
	}

	requestCtx, cancel := context.WithTimeout(d.ctx, d.attemptTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, item.dest.url, bytes.NewReader(item.body))
	if err != nil {
		return &deliveryError{err: fmt.Errorf("build alert request for %s: %w", item.dest.redacted, sanitizeError(err, item.dest))}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "oberwatch-alerts")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		retryable := !errors.Is(err, context.Canceled)
		return &deliveryError{
			err:       fmt.Errorf("send alert request to %s: %w", item.dest.redacted, sanitizeError(err, item.dest)),
			retryable: retryable,
		}
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBodyBytes))
		return nil
	}

	snippet := readErrorSnippet(resp.Body, item.dest)
	msg := fmt.Sprintf("alert request to %s returned status %d", item.dest.redacted, resp.StatusCode)
	if snippet != "" {
		msg += ": " + snippet
	}
	return &deliveryError{
		err:        errors.New(msg),
		statusCode: resp.StatusCode,
		retryable:  retryableStatus(resp.StatusCode),
	}
}

// sanitizeError strips the request URL that net/http embeds in *url.Error and
// redacts any remaining secret substrings.
func sanitizeError(err error, dest destination) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		err = urlErr.Err
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, context.Canceled):
		return context.Canceled
	default:
		return errors.New(redactText(err.Error(), dest.secretValues()))
	}
}

func readErrorSnippet(body io.Reader, dest destination) string {
	raw, err := io.ReadAll(io.LimitReader(body, maxErrorBodyBytes))
	if err != nil {
		return ""
	}
	snippet := strings.TrimSpace(strings.ToValidUTF8(string(raw), ""))
	snippet = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, snippet)
	return redactText(snippet, dest.secretValues())
}

func encodePayload(kind string, event Alert) ([]byte, error) {
	switch kind {
	case KindSlack:
		body, err := json.Marshal(buildSlackPayload(event))
		if err != nil {
			return nil, fmt.Errorf("marshal slack alert payload: %w", err)
		}
		return body, nil
	case KindEmail:
		return buildEmailBody(event), nil
	default:
		body, err := json.Marshal(event)
		if err != nil {
			return nil, fmt.Errorf("marshal webhook alert payload: %w", err)
		}
		return body, nil
	}
}

func (d *AlertDispatcher) shouldSuppress(event Alert) bool {
	if event.Type != TypeBudgetThreshold {
		return false
	}
	if event.ThresholdPct <= 0 {
		return false
	}

	periodStart := event.PeriodStartedAt.UTC()
	if periodStart.IsZero() {
		periodStart = event.Timestamp.UTC()
	}
	key := fmt.Sprintf("%s|%.4f|%s", strings.ToLower(strings.TrimSpace(event.Agent)), event.ThresholdPct, periodStart.Format(time.RFC3339Nano))

	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.thresholdDedup[key]; exists {
		return true
	}
	d.thresholdDedup[key] = struct{}{}
	return false
}

func buildSlackPayload(event Alert) map[string]any {
	thresholdText := "n/a"
	if event.ThresholdPct > 0 {
		thresholdText = fmt.Sprintf("%.0f%%", event.ThresholdPct)
	}
	spentLimitText := "n/a"
	if event.LimitUSD > 0 || event.SpentUSD > 0 {
		spentLimitText = fmt.Sprintf("$%.2f / $%.2f", event.SpentUSD, event.LimitUSD)
	}
	actionText := event.Action
	if actionText == "" {
		actionText = "n/a"
	}
	// Slack rejects a section block with empty text as invalid_blocks, and that
	// 400 is permanent, so an alert without a message would never be delivered.
	messageText := strings.TrimSpace(event.Message)
	if messageText == "" {
		messageText = "n/a"
	}

	return map[string]any{
		"text": fmt.Sprintf("Oberwatch alert: %s", event.Type),
		"blocks": []map[string]any{
			{
				"type": "section",
				"text": map[string]any{
					"type": "mrkdwn",
					"text": fmt.Sprintf("*Oberwatch Alert* `%s`", event.Type),
				},
			},
			{
				"type": "section",
				"fields": []map[string]any{
					{"type": "mrkdwn", "text": fmt.Sprintf("*Agent:*\n%s", event.Agent)},
					{"type": "mrkdwn", "text": fmt.Sprintf("*Threshold:*\n%s", thresholdText)},
					{"type": "mrkdwn", "text": fmt.Sprintf("*Spent/Limit:*\n%s", spentLimitText)},
					{"type": "mrkdwn", "text": fmt.Sprintf("*Action:*\n%s", actionText)},
				},
			},
			{
				"type": "section",
				"text": map[string]any{
					"type": "mrkdwn",
					"text": messageText,
				},
			},
		},
	}
}

func (d *AlertDispatcher) logSaturation(item job) {
	if d.logger == nil {
		return
	}
	now := d.clock.Now()

	d.saturationMu.Lock()
	d.droppedSinceLog++
	if !d.lastSaturationLog.IsZero() && now.Sub(d.lastSaturationLog) < saturationLogInterval {
		d.saturationMu.Unlock()
		return
	}
	droppedSinceLog := d.droppedSinceLog
	d.droppedSinceLog = 0
	d.lastSaturationLog = now
	d.saturationMu.Unlock()

	d.logger.Warn("alert queue saturated, dropping alert",
		"destination", item.dest.redacted,
		"kind", item.dest.kind,
		"type", item.event.Type,
		"agent", item.event.Agent,
		"queue_capacity", cap(d.queue),
		"dropped_since_last_report", droppedSinceLog,
		"dropped_total", d.dropped.Load(),
	)
}

func (d *AlertDispatcher) logWarn(message string, err error, dest destination, event Alert, attempts int) {
	if d.logger == nil {
		return
	}
	errText := ""
	if err != nil {
		errText = redactText(err.Error(), dest.secretValues())
	}
	args := []any{
		"error", errText,
		"destination", dest.redacted,
		"kind", dest.kind,
		"attempts", attempts,
		"type", event.Type,
		"agent", event.Agent,
	}
	if status := statusOf(err); status > 0 {
		args = append(args, "status", status)
	}
	d.logger.Warn(message, args...)
}

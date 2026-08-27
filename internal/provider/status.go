package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Status is the public availability of a provider's service, as reported by
// that provider's own public status feed (or, for a local server, a
// credential-free reachability check). It never reflects whether a
// configured API credential is valid.
type Status string

// Status values.
const (
	StatusOperational Status = "operational"
	StatusDegraded    Status = "degraded"
	StatusOutage      Status = "outage"
	StatusUnavailable Status = "status_unavailable"
)

// ProbeTimeout bounds every network call a Checker makes.
const ProbeTimeout = 3 * time.Second

const (
	defaultOpenAIStatusURL    = "https://status.openai.com/api/v2/status.json"
	defaultAnthropicStatusURL = "https://status.anthropic.com/api/v2/status.json"
)

// Detail strings shown next to each row. They must stay accurate about what
// was actually checked: a public feed, or a local credential-free GET.
const (
	openAIDetail    = "Public status feed at status.openai.com. Not a check of your API key or of the inference API."
	anthropicDetail = "Public status feed at status.anthropic.com. Not a check of your API key or of the inference API."
	ollamaDetail    = "Local Ollama server on loopback answered an unauthenticated GET /api/tags. Not a public status feed."
)

var (
	// errNotLoopback rejects an Ollama base URL, or a resolved address, that is
	// not on the loopback interface. Ollama is the one provider probed by
	// address rather than by public feed, so the address it is probed at is
	// operator-controlled input and must never be able to reach a private
	// network, a cloud metadata service, or any remote host.
	errNotLoopback = errors.New("ollama base URL must point at a loopback address")

	// errRedirectRefused stops a local server from bouncing a probe anywhere.
	errRedirectRefused = errors.New("provider status probes do not follow redirects")
)

// fallbackLoopbackClient serves zero-value Checkers, which are built directly
// in tests. Production code goes through NewChecker.
var fallbackLoopbackClient = sync.OnceValue(newLoopbackClient)

// StatusRow is one row of the provider status table shown to users.
//
//nolint:govet // Keep fields grouped by what they describe, not by width.
type StatusRow struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
	Status   Status `json:"status"`
	Detail   string `json:"detail"`

	// ObservedAt is when this row was actually observed. It is nil for a row
	// that has never been probed, so a reader can always tell "checked and
	// unavailable" from "not checked yet".
	ObservedAt *time.Time `json:"observed_at,omitempty"`

	// Public is true when the row came from the provider's public status feed
	// and false when it came from a local reachability check.
	Public bool `json:"public"`
}

// Checker probes provider availability. It never sends API credentials and
// never contacts a provider's inference endpoints: it only reads a
// provider's public status feed, or, for a local Ollama server, an
// unauthenticated GET to /api/tags.
type Checker struct {
	// HTTPClient is used for the public status feeds only. Local Ollama probes
	// always go through a hardened loopback-only client, so no caller can
	// widen what an Ollama probe is allowed to reach.
	HTTPClient         *http.Client
	OpenAIStatusURL    string
	AnthropicStatusURL string

	// ollamaClient is the hardened client used for local Ollama probes.
	ollamaClient *http.Client

	// ollamaTransport replaces the hardened transport in tests. It is
	// unexported and cannot bypass the loopback URL guard or the no-redirect
	// policy, both of which are applied around it.
	ollamaTransport http.RoundTripper
}

// NewChecker builds a Checker using the real public status feed URLs and HTTP
// clients bounded to ProbeTimeout.
func NewChecker() *Checker {
	return &Checker{
		HTTPClient:         &http.Client{Timeout: ProbeTimeout},
		OpenAIStatusURL:    defaultOpenAIStatusURL,
		AnthropicStatusURL: defaultAnthropicStatusURL,
		ollamaClient:       newLoopbackClient(),
	}
}

// CheckOpenAI reports OpenAI's public service availability.
func (c *Checker) CheckOpenAI(ctx context.Context) StatusRow {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()

	return StatusRow{
		Provider: "openai",
		Label:    "OpenAI",
		Status:   c.fetchStatuspage(ctx, c.OpenAIStatusURL),
		Public:   true,
		Detail:   openAIDetail,
	}
}

// CheckAnthropic reports Anthropic's public service availability.
func (c *Checker) CheckAnthropic(ctx context.Context) StatusRow {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()

	return StatusRow{
		Provider: "anthropic",
		Label:    "Anthropic",
		Status:   c.fetchStatuspage(ctx, c.AnthropicStatusURL),
		Public:   true,
		Detail:   anthropicDetail,
	}
}

// CheckOllama reports whether a locally configured Ollama server answers a
// credential-free GET /api/tags. Ollama publishes no status feed, so this is
// the one probe that talks to a configured address, and it is restricted to
// loopback: a non-loopback base URL is rejected without any request being
// made. The second return value is false whenever there is no usable base
// URL or the server does not answer — callers must omit the row in that case
// rather than show it as unreachable.
func (c *Checker) CheckOllama(ctx context.Context, baseURL string) (StatusRow, bool) {
	parsed, err := validateOllamaBaseURL(baseURL)
	if err != nil {
		return StatusRow{}, false
	}

	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()

	endpoint := (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host, Path: "/api/tags"}).String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return StatusRow{}, false
	}

	resp, err := c.ollamaHTTPClient().Do(req)
	if err != nil {
		return StatusRow{}, false
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return StatusRow{}, false
	}

	return StatusRow{
		Provider: "ollama",
		Label:    "Ollama (local)",
		Status:   StatusOperational,
		Public:   false,
		Detail:   ollamaDetail,
	}, true
}

// validateOllamaBaseURL accepts only a credential-free http or https URL whose
// host is a loopback hostname or IP: localhost, anything in 127.0.0.0/8, or
// ::1. Everything else — remote hostnames, RFC1918 ranges, link-local and
// cloud metadata addresses, other schemes, embedded credentials, and URLs
// carrying a query or fragment — is rejected before any request is made.
func validateOllamaBaseURL(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("ollama base URL is empty")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("parse ollama base URL: %w", err)
	}

	switch parsed.Scheme {
	case "http", "https":
	default:
		return nil, fmt.Errorf("ollama base URL scheme %q is not http or https", parsed.Scheme)
	}
	if parsed.User != nil {
		return nil, errors.New("ollama base URL must not carry credentials")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("ollama base URL must not carry a query or fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, errors.New("ollama base URL must not carry a path")
	}
	if parsed.RawPath != "" {
		return nil, errors.New("ollama base URL must not carry an encoded path")
	}
	if err := requireLoopbackHost(parsed.Hostname()); err != nil {
		return nil, err
	}

	return parsed, nil
}

// requireLoopbackHost checks the literal host of a URL. A hostname could still
// resolve elsewhere, which is why verifyLoopbackAddr re-checks at dial time.
func requireLoopbackHost(host string) error {
	if host == "" {
		return fmt.Errorf("%w: base URL has no host", errNotLoopback)
	}
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsLoopback() {
			return fmt.Errorf("%w: %s is not loopback", errNotLoopback, host)
		}
		return nil
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	return fmt.Errorf("%w: %s is not a loopback host", errNotLoopback, host)
}

// verifyLoopbackAddr re-checks the address the dialer is about to connect to.
// This closes the gap left by hostname validation: "localhost" is allowed by
// name, but only a loopback IP is allowed on the wire.
func verifyLoopbackAddr(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("split probe address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("%w: %q did not resolve to an IP", errNotLoopback, address)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("%w: %s resolved to %s", errNotLoopback, address, ip)
	}
	return nil
}

// newLoopbackClient builds the only client used for local Ollama probes: no
// proxy, no redirects, and a dial-time check that the address really is
// loopback.
func newLoopbackClient() *http.Client {
	dialer := &net.Dialer{
		Timeout: ProbeTimeout,
		Control: func(_, address string, _ syscall.RawConn) error {
			return verifyLoopbackAddr(address)
		},
	}

	return &http.Client{
		Timeout:       ProbeTimeout,
		CheckRedirect: refuseRedirect,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           dialer.DialContext,
			ResponseHeaderTimeout: ProbeTimeout,
		},
	}
}

func refuseRedirect(_ *http.Request, _ []*http.Request) error {
	return errRedirectRefused
}

// ollamaHTTPClient returns the loopback-only client. A test transport is
// wrapped in the same no-redirect policy, so no configuration reachable from
// a test can turn a probe into a redirect-following request.
func (c *Checker) ollamaHTTPClient() *http.Client {
	if c.ollamaTransport != nil {
		return &http.Client{
			Timeout:       ProbeTimeout,
			CheckRedirect: refuseRedirect,
			Transport:     c.ollamaTransport,
		}
	}
	if c.ollamaClient != nil {
		return c.ollamaClient
	}
	return fallbackLoopbackClient()
}

// fetchStatuspage reads a statuspage.io-style status.json document and maps
// its top-level indicator to a Status. Any network error, non-200 response,
// or unparseable body is reported as StatusUnavailable rather than a
// fabricated status.
func (c *Checker) fetchStatuspage(ctx context.Context, url string) Status {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return StatusUnavailable
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return StatusUnavailable
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return StatusUnavailable
	}

	var payload struct {
		Status struct {
			Indicator string `json:"indicator"`
		} `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return StatusUnavailable
	}

	switch payload.Status.Indicator {
	case "none":
		return StatusOperational
	case "minor":
		return StatusDegraded
	case "major", "critical":
		return StatusOutage
	default:
		return StatusUnavailable
	}
}

func (c *Checker) httpClient() *http.Client {
	if c.HTTPClient == nil {
		return &http.Client{Timeout: ProbeTimeout}
	}
	return c.HTTPClient
}

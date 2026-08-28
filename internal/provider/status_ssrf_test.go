package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// recordingTransport records every request URL a probe attempts. When next is
// nil the request fails, which lets a test prove that no request was made at
// all rather than that a request merely failed.
type recordingTransport struct {
	next http.RoundTripper
	urls []string
	mu   sync.Mutex
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.urls = append(r.urls, req.URL.String())
	r.mu.Unlock()

	if r.next == nil {
		return nil, errors.New("recordingTransport: no upstream configured")
	}
	return r.next.RoundTrip(req)
}

func (r *recordingTransport) attempted() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.urls...)
}

func TestValidateOllamaBaseURL_RejectsEverythingButLoopback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
	}{
		{name: "empty", baseURL: ""},
		{name: "whitespace only", baseURL: "   "},
		{name: "remote hostname", baseURL: "http://ollama.example.com:11434"},
		{name: "cloud metadata ipv4", baseURL: "http://169.254.169.254"},
		{name: "cloud metadata with ollama port", baseURL: "http://169.254.169.254:11434"},
		{name: "gcp metadata hostname", baseURL: "http://metadata.google.internal/computeMetadata/v1"},
		{name: "link local ipv6 metadata", baseURL: "http://[fe80::a9fe:a9fe]:11434"},
		{name: "rfc1918 ten dot", baseURL: "http://10.0.0.5:11434"},
		{name: "rfc1918 one seven two", baseURL: "http://172.16.0.1:11434"},
		{name: "rfc1918 one nine two", baseURL: "http://192.168.1.10:11434"},
		{name: "carrier grade nat", baseURL: "http://100.64.0.1:11434"},
		{name: "unique local ipv6", baseURL: "http://[fd00::1]:11434"},
		{name: "public dns resolver", baseURL: "http://8.8.8.8:11434"},
		{name: "wildcard bind address", baseURL: "http://0.0.0.0:11434"},
		{name: "ipv6 wildcard", baseURL: "http://[::]:11434"},
		{name: "decimal encoded loopback", baseURL: "http://2130706433:11434"},
		{name: "hostname that merely starts with a loopback literal", baseURL: "http://127.0.0.1.evil.example.com:11434"},
		{name: "hostname that merely ends with localhost", baseURL: "http://evil-localhost:11434"},
		{name: "file scheme", baseURL: "file:///etc/passwd"},
		{name: "ftp scheme", baseURL: "ftp://127.0.0.1"},
		{name: "gopher scheme", baseURL: "gopher://127.0.0.1:11434"},
		{name: "no scheme", baseURL: "127.0.0.1:11434"},
		{name: "scheme relative", baseURL: "//169.254.169.254/api"},
		{name: "credentials in url", baseURL: "http://user:pass@127.0.0.1:11434"},
		{name: "query string", baseURL: "http://127.0.0.1:11434/?next=http://169.254.169.254"},
		{name: "fragment", baseURL: "http://127.0.0.1:11434/#frag"},
		{name: "path prefix", baseURL: "http://127.0.0.1:11434/proxy"},
		{name: "encoded path", baseURL: "http://127.0.0.1:11434/%2Fproxy"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := validateOllamaBaseURL(tt.baseURL); err == nil {
				t.Fatalf("validateOllamaBaseURL(%q) error = nil, want a rejection", tt.baseURL)
			}
		})
	}
}

func TestValidateOllamaBaseURL_AcceptsLoopback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
	}{
		{name: "localhost with port", baseURL: "http://localhost:11434"},
		{name: "localhost without port", baseURL: "http://localhost"},
		{name: "localhost mixed case", baseURL: "http://LocalHost:11434"},
		{name: "ipv4 loopback", baseURL: "http://127.0.0.1:11434"},
		{name: "ipv4 loopback elsewhere in 127/8", baseURL: "http://127.9.9.9:11434"},
		{name: "ipv6 loopback", baseURL: "http://[::1]:11434"},
		{name: "ipv4 mapped ipv6 loopback", baseURL: "http://[::ffff:127.0.0.1]:11434"},
		{name: "https loopback", baseURL: "https://127.0.0.1:11434"},
		{name: "trailing slash", baseURL: "http://127.0.0.1:11434/"},
		{name: "surrounding whitespace", baseURL: "  http://127.0.0.1:11434  "},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := validateOllamaBaseURL(tt.baseURL); err != nil {
				t.Fatalf("validateOllamaBaseURL(%q) error = %v, want nil", tt.baseURL, err)
			}
		})
	}
}

func TestChecker_CheckOllama_NonLoopbackURLIssuesNoRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
	}{
		{name: "cloud metadata", baseURL: "http://169.254.169.254"},
		{name: "private network", baseURL: "http://10.0.0.5:11434"},
		{name: "private network 192", baseURL: "http://192.168.1.10:11434"},
		{name: "remote hostname", baseURL: "http://ollama.example.com:11434"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			transport := &recordingTransport{}
			checker := &Checker{ollamaTransport: transport}

			row, ok := checker.CheckOllama(context.Background(), tt.baseURL)
			if ok {
				t.Fatalf("ok = true for %q, want false, got row %+v", tt.baseURL, row)
			}
			if attempted := transport.attempted(); len(attempted) != 0 {
				t.Fatalf("probe attempted %v for %q, want no request at all", attempted, tt.baseURL)
			}
		})
	}
}

func TestChecker_CheckOllama_RedirectCannotReachRemote(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/api/tags", http.StatusFound)
	}))
	defer server.Close()

	transport := &recordingTransport{next: server.Client().Transport}
	checker := &Checker{ollamaTransport: transport}

	row, ok := checker.CheckOllama(context.Background(), server.URL)
	if !ok {
		t.Fatal("ok = false, want true: a configured loopback server keeps its row even when it misbehaves")
	}
	if row.Status != StatusUnreachable {
		t.Fatalf("Status = %q, want %q when the local server redirects instead of answering", row.Status, StatusUnreachable)
	}

	attempted := transport.attempted()
	if len(attempted) != 1 {
		t.Fatalf("attempted %v, want exactly the first loopback request", attempted)
	}
	if attempted[0] != server.URL+"/api/tags" {
		t.Fatalf("attempted[0] = %q, want %q", attempted[0], server.URL+"/api/tags")
	}
}

func TestChecker_CheckOllama_RedirectToLoopbackIsAlsoNotFollowed(t *testing.T) {
	t.Parallel()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/api/tags", http.StatusFound)
	}))
	defer redirector.Close()

	transport := &recordingTransport{next: redirector.Client().Transport}
	checker := &Checker{ollamaTransport: transport}

	row, ok := checker.CheckOllama(context.Background(), redirector.URL)
	if !ok {
		t.Fatal("ok = false, want true: the configured server keeps its row")
	}
	if row.Status == StatusOperational {
		t.Fatal("Status = operational after a redirect, want unreachable: provider status probes must not follow redirects")
	}
	if attempted := transport.attempted(); len(attempted) != 1 {
		t.Fatalf("attempted %v, want exactly one request", attempted)
	}
}

func TestChecker_CheckOllama_LoopbackServerAnswersThroughTheHardenedClient(t *testing.T) {
	t.Parallel()

	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("request must not carry credentials, got Authorization=%q", auth)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer server.Close()

	// NewChecker's own client, not an injected one: this exercises the real
	// dial-time loopback guard.
	checker := NewChecker()
	row, ok := checker.CheckOllama(context.Background(), server.URL)

	if !ok {
		t.Fatal("ok = false, want true for a loopback server answering /api/tags")
	}
	if gotPath != "/api/tags" {
		t.Fatalf("path = %q, want %q", gotPath, "/api/tags")
	}
	if row.Public {
		t.Fatal("Public = true, want false: a local reachability check is not a public status feed")
	}
}

func TestVerifyLoopbackAddr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		address string
		wantErr bool
	}{
		{name: "ipv4 loopback", address: "127.0.0.1:11434"},
		{name: "ipv4 loopback in 127/8", address: "127.5.6.7:80"},
		{name: "ipv6 loopback", address: "[::1]:11434"},
		{name: "cloud metadata", address: "169.254.169.254:80", wantErr: true},
		{name: "private network", address: "10.1.2.3:11434", wantErr: true},
		{name: "public address", address: "93.184.216.34:443", wantErr: true},
		{name: "missing port", address: "127.0.0.1", wantErr: true},
		{name: "not an ip", address: "ollama.example.com:11434", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := verifyLoopbackAddr(tt.address)
			if tt.wantErr && err == nil {
				t.Fatalf("verifyLoopbackAddr(%q) error = nil, want an error", tt.address)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("verifyLoopbackAddr(%q) error = %v, want nil", tt.address, err)
			}
		})
	}
}

func TestNewLoopbackClient_UsesNoProxyAndRefusesRedirects(t *testing.T) {
	t.Parallel()

	client := newLoopbackClient()

	if client.CheckRedirect == nil {
		t.Fatal("CheckRedirect = nil, want a policy that refuses redirects")
	}
	if err := client.CheckRedirect(nil, nil); err == nil {
		t.Fatal("CheckRedirect returned nil, want an error so no redirect is followed")
	}
	if client.Timeout != ProbeTimeout {
		t.Fatalf("Timeout = %v, want %v", client.Timeout, ProbeTimeout)
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("Proxy is set, want nil so a probe can never be tunnelled off the loopback interface")
	}
	if transport.DialContext == nil {
		t.Fatal("DialContext = nil, want the loopback-verifying dialer")
	}
}

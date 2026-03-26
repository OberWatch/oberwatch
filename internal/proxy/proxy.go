package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/OberWatch/oberwatch/internal/config"
)

const (
	healthPath                 = "/_oberwatch/api/v1/health"
	oberwatchHeaderPrefixLower = "x-oberwatch-"
)

// Hook is a middleware callback executed for each request.
type Hook func(*http.Request)

// Hooks contains middleware callbacks used by the proxy chain.
type Hooks struct {
	Gate  Hook
	Trace Hook
}

// Server is the HTTP reverse proxy for upstream LLM providers.
type Server struct {
	handler http.Handler
}

// New builds a reverse proxy server using upstream configuration and middleware hooks.
func New(cfg config.Config, hooks Hooks) (*Server, error) {
	targets, err := buildTargets(cfg.Upstream)
	if err != nil {
		return nil, err
	}

	defaultProvider := cfg.Upstream.DefaultProvider
	if _, ok := targets[defaultProvider]; !ok {
		return nil, fmt.Errorf("default upstream provider %q is not configured", defaultProvider)
	}

	reverseProxy := &httputil.ReverseProxy{
		FlushInterval: -1,
		Rewrite: func(req *httputil.ProxyRequest) {
			targetProvider := detectProvider(req.In.URL.Path, defaultProvider)
			target := targets[targetProvider]

			req.SetURL(target)
			req.SetXForwarded()
			stripOberwatchHeaders(req.Out.Header)
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, fmt.Sprintf("upstream proxy error: %v", err), http.StatusBadGateway)
		},
	}

	proxyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == healthPath {
			writeHealthResponse(w)
			return
		}

		reverseProxy.ServeHTTP(w, r)
	})

	chained := chain(proxyHandler, gateMiddleware(hooks.Gate), traceMiddleware(hooks.Trace))

	return &Server{handler: chained}, nil
}

// ServeHTTP handles incoming requests through middleware and proxy routing.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func buildTargets(cfg config.UpstreamConfig) (map[config.ProviderConfigName]*url.URL, error) {
	targets := make(map[config.ProviderConfigName]*url.URL)

	type endpoint struct {
		name    config.ProviderConfigName
		baseURL string
	}

	endpoints := []endpoint{
		{name: config.ProviderOpenAI, baseURL: cfg.OpenAI.BaseURL},
		{name: config.ProviderAnthropic, baseURL: cfg.Anthropic.BaseURL},
		{name: config.ProviderOllama, baseURL: cfg.Ollama.BaseURL},
		{name: config.ProviderCustom, baseURL: cfg.Custom.BaseURL},
	}

	for _, endpoint := range endpoints {
		raw := strings.TrimSpace(endpoint.baseURL)
		if raw == "" {
			continue
		}

		parsed, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("parse upstream %q base URL %q: %w", endpoint.name, raw, err)
		}
		if parsed.Scheme == "" || parsed.Host == "" {
			return nil, fmt.Errorf("upstream %q base URL %q must include scheme and host", endpoint.name, raw)
		}

		targets[endpoint.name] = parsed
	}

	required := []config.ProviderConfigName{config.ProviderOpenAI, config.ProviderAnthropic}
	for _, name := range required {
		if _, ok := targets[name]; !ok {
			return nil, fmt.Errorf("upstream %q base URL must be configured", name)
		}
	}

	return targets, nil
}

func detectProvider(path string, defaultProvider config.ProviderConfigName) config.ProviderConfigName {
	normalized := strings.TrimSuffix(path, "/")
	if normalized == "" {
		normalized = path
	}

	switch normalized {
	case "/v1/chat/completions", "/v1/completions":
		return config.ProviderOpenAI
	case "/v1/messages":
		return config.ProviderAnthropic
	default:
		return defaultProvider
	}
}

func stripOberwatchHeaders(header http.Header) {
	for key := range header {
		if strings.HasPrefix(strings.ToLower(key), oberwatchHeaderPrefixLower) {
			delete(header, key)
		}
	}
}

func chain(base http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	chained := base
	for i := len(middlewares) - 1; i >= 0; i-- {
		chained = middlewares[i](chained)
	}
	return chained
}

func gateMiddleware(hook Hook) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if hook != nil {
				hook(r)
			}
			next.ServeHTTP(w, r)
		})
	}
}

func traceMiddleware(hook Hook) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if hook != nil {
				hook(r)
			}
			next.ServeHTTP(w, r)
		})
	}
}

func writeHealthResponse(w http.ResponseWriter) {
	payload, err := json.Marshal(map[string]string{
		"status": "ok",
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("encode health response: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(payload); err != nil {
		return
	}
}

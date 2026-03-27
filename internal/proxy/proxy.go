package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/OberWatch/oberwatch/internal/budget"
	"github.com/OberWatch/oberwatch/internal/config"
	"github.com/OberWatch/oberwatch/internal/pricing"
	"github.com/OberWatch/oberwatch/internal/storage"
)

const (
	healthPath                 = "/_oberwatch/api/v1/health"
	managementPathPrefix       = "/_oberwatch/api/v1/"
	oberwatchHeaderPrefixLower = "x-oberwatch-"
)

type budgetContextKey struct{}

type budgetRequestMeta struct {
	agent         string
	model         string
	provider      string
	traceID       string
	taskID        string
	originalModel string
	streaming     bool
	downgraded    bool
}

// Hook is a middleware callback executed for each request.
type Hook func(*http.Request)

// Hooks contains middleware callbacks used by the proxy chain.
type Hooks struct {
	Gate       Hook
	Trace      Hook
	Budget     *budget.BudgetManager
	Pricing    *pricing.PricingTable
	CostSink   storage.CostRecordSink
	Management http.Handler
	Dashboard  http.Handler
	Logger     *slog.Logger
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
		ModifyResponse: func(response *http.Response) error {
			if hooks.Budget == nil || hooks.Pricing == nil {
				return nil
			}

			value := response.Request.Context().Value(budgetContextKey{})
			meta, ok := value.(budgetRequestMeta)
			if !ok {
				return nil
			}
			response.Body = newBudgetTrackingBody(
				response.Body,
				response.StatusCode,
				response.Header.Get("Content-Type"),
				meta,
				hooks.Budget,
				hooks.Pricing,
				hooks.CostSink,
				hooks.Logger,
			)
			return nil
		},
	}

	proxyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, managementPathPrefix) && hooks.Management != nil {
			hooks.Management.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == healthPath {
			writeHealthResponse(w)
			return
		}
		if hooks.Dashboard != nil && shouldServeDashboard(r.Method, r.URL.Path) {
			hooks.Dashboard.ServeHTTP(w, r)
			return
		}

		reverseProxy.ServeHTTP(w, r)
	})

	chained := chain(proxyHandler, gateMiddleware(hooks), traceMiddleware(hooks.Trace))

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

func shouldServeDashboard(method string, requestPath string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	return !isKnownProxyPath(requestPath)
}

func isKnownProxyPath(requestPath string) bool {
	normalized := strings.TrimSuffix(strings.TrimSpace(requestPath), "/")
	switch normalized {
	case "/v1/chat/completions", "/v1/completions", "/v1/embeddings", "/v1/models", "/v1/messages":
		return true
	default:
		return false
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

func gateMiddleware(hooks Hooks) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if hooks.Gate != nil {
				hooks.Gate(r)
			}
			if hooks.Budget != nil && hooks.Pricing != nil {
				agent := hooks.Budget.IdentifyAgent(r)

				requestBody, err := io.ReadAll(r.Body)
				if err != nil {
					writeConfigError(w, fmt.Sprintf("read request body: %v", err))
					return
				}
				model, streaming := extractModelAndStream(requestBody)
				originalModel := model
				downgraded := false

				estimatedInputTokens := len(requestBody) / 4
				estimatedCost := hooks.Pricing.CalculateCost(model, estimatedInputTokens, 0)
				decision := hooks.Budget.CheckBudgetDetailed(agent, estimatedCost)

				switch decision.Action {
				case budget.ActionKill:
					writeBudgetError(w, decision, http.StatusTooManyRequests)
					return
				case budget.ActionReject:
					writeBudgetError(w, decision, http.StatusTooManyRequests)
					return
				case budget.ActionDowngrade:
					rewritten, currentModel, newModel, changed, rewriteErr := hooks.Budget.RewriteModelForDowngrade(agent, requestBody)
					if rewriteErr != nil {
						writeConfigError(w, fmt.Sprintf("rewrite downgrade body: %v", rewriteErr))
						return
					}

					if changed {
						requestBody = rewritten
						model = newModel
						originalModel = currentModel
						downgraded = true
						logDowngrade(hooks.Logger, agent, currentModel, newModel)
					} else if decision.Over {
						writeBudgetError(w, decision, http.StatusTooManyRequests)
						return
					}
				case budget.ActionAlert, budget.ActionAllow:
				default:
				}

				r.Body = io.NopCloser(bytes.NewReader(requestBody))
				r.ContentLength = int64(len(requestBody))
				r.GetBody = func() (io.ReadCloser, error) {
					return io.NopCloser(bytes.NewReader(requestBody)), nil
				}
				meta := budgetRequestMeta{
					agent:         agent,
					model:         model,
					provider:      string(detectProvider(r.URL.Path, config.ProviderOpenAI)),
					traceID:       strings.TrimSpace(r.Header.Get("X-Oberwatch-Trace-ID")),
					taskID:        strings.TrimSpace(r.Header.Get("X-Oberwatch-Task")),
					originalModel: originalModel,
					streaming:     streaming,
					downgraded:    downgraded,
				}
				*r = *r.WithContext(context.WithValue(r.Context(), budgetContextKey{}, meta))
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

//nolint:govet // keep fields grouped by response interception lifecycle.
type budgetTrackingBody struct {
	statusCode  int
	buffer      bytes.Buffer
	once        sync.Once
	inner       io.ReadCloser
	manager     *budget.BudgetManager
	pricing     *pricing.PricingTable
	sink        storage.CostRecordSink
	logger      *slog.Logger
	meta        budgetRequestMeta
	contentType string
}

func newBudgetTrackingBody(
	inner io.ReadCloser,
	statusCode int,
	contentType string,
	meta budgetRequestMeta,
	manager *budget.BudgetManager,
	pricingTable *pricing.PricingTable,
	sink storage.CostRecordSink,
	logger *slog.Logger,
) io.ReadCloser {
	return &budgetTrackingBody{
		inner:       inner,
		statusCode:  statusCode,
		contentType: contentType,
		meta:        meta,
		manager:     manager,
		pricing:     pricingTable,
		sink:        sink,
		logger:      logger,
	}
}

func (b *budgetTrackingBody) Read(payload []byte) (int, error) {
	n, err := b.inner.Read(payload)
	if n > 0 {
		if _, writeErr := b.buffer.Write(payload[:n]); writeErr != nil && b.logger != nil {
			b.logger.Warn("failed buffering response body for budget accounting", "error", writeErr)
		}
	}
	if err == io.EOF {
		b.finalize()
	}
	return n, err
}

// Close closes the upstream response body and triggers final accounting once.
func (b *budgetTrackingBody) Close() error {
	err := b.inner.Close()
	b.finalize()
	return err
}

func (b *budgetTrackingBody) finalize() {
	b.once.Do(func() {
		if b.statusCode < http.StatusOK || b.statusCode >= http.StatusBadRequest {
			return
		}
		if b.meta.model == "" {
			return
		}

		body := b.buffer.Bytes()
		var usage pricing.Usage
		if b.meta.streaming || strings.Contains(strings.ToLower(b.contentType), "text/event-stream") {
			usage = pricing.AccumulateStreamingUsage(b.meta.provider, body, b.logger)
		} else {
			usage = pricing.ExtractUsageFromResponse(b.meta.provider, body, b.logger)
		}

		cost := b.pricing.CalculateCost(b.meta.model, usage.InputTokens, usage.OutputTokens)
		b.manager.RecordSpend(b.meta.agent, cost)
		if b.sink != nil {
			b.sink.Enqueue(storage.CostRecord{
				Agent:         b.meta.agent,
				Model:         b.meta.model,
				Provider:      b.meta.provider,
				TraceID:       b.meta.traceID,
				TaskID:        b.meta.taskID,
				OriginalModel: b.meta.originalModel,
				InputTokens:   usage.InputTokens,
				OutputTokens:  usage.OutputTokens,
				CostUSD:       cost,
				Downgraded:    b.meta.downgraded,
				CreatedAt:     time.Now().UTC(),
			})
		}
	})
}

func extractModelAndStream(requestBody []byte) (string, bool) {
	if len(bytes.TrimSpace(requestBody)) == 0 {
		return "", false
	}

	var payload struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(requestBody, &payload); err != nil {
		return "", false
	}
	return strings.TrimSpace(payload.Model), payload.Stream
}

func logDowngrade(logger *slog.Logger, agent string, originalModel string, newModel string) {
	if logger == nil {
		return
	}
	logger.Info(
		"auto-downgraded request model",
		"agent",
		agent,
		"original_model",
		originalModel,
		"new_model",
		newModel,
	)
}

func writeConfigError(w http.ResponseWriter, message string) {
	payload, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"code":    "config_error",
			"message": message,
		},
	})
	if err != nil {
		http.Error(w, message, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	if _, err := w.Write(payload); err != nil {
		return
	}
}

func writeBudgetError(w http.ResponseWriter, decision budget.Decision, statusCode int) {
	code := decision.Code
	if code == "" {
		code = "budget_exceeded"
	}

	message := decision.Message
	if message == "" {
		message = fmt.Sprintf(
			"Agent '%s' has exceeded its %s budget of $%.2f (spent: $%.2f)",
			decision.Agent,
			decision.Period,
			decision.LimitUSD,
			decision.SpentUSD,
		)
	}

	payload, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"code":             code,
			"message":          message,
			"agent":            decision.Agent,
			"budget_limit_usd": decision.LimitUSD,
			"budget_spent_usd": decision.SpentUSD,
		},
	})
	if err != nil {
		http.Error(w, message, statusCode)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if _, err := w.Write(payload); err != nil {
		return
	}
}

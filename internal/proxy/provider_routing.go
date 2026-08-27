package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/OberWatch/oberwatch/internal/config"
	"github.com/OberWatch/oberwatch/internal/pricing"
	"github.com/OberWatch/oberwatch/internal/storage"
)

const providerOverrideHeader = "X-Oberwatch-Provider"

type resolvedRouteContextKey struct{}

type resolvedRoute struct {
	provider        config.ProviderConfigName
	anthropicCompat bool
}

func resolvedRouteFromContext(ctx context.Context) (resolvedRoute, bool) {
	route, ok := ctx.Value(resolvedRouteContextKey{}).(resolvedRoute)
	return route, ok
}

func providerRoutingMiddleware(defaultProvider config.ProviderConfigName, targets map[config.ProviderConfigName]*url.URL, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isKnownProxyPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			var requestBody []byte
			if r.Body != nil {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					writeConfigError(w, fmt.Sprintf("read request body: %v", err))
					return
				}
				requestBody = body
				r.Body = io.NopCloser(bytes.NewReader(requestBody))
				r.ContentLength = int64(len(requestBody))
				r.GetBody = func() (io.ReadCloser, error) {
					return io.NopCloser(bytes.NewReader(requestBody)), nil
				}
			}

			route, err := resolveProvider(r, requestBody, defaultProvider)
			if err != nil {
				writeProviderResolutionError(w, err.Error(), http.StatusBadRequest)
				return
			}
			if _, ok := targets[route.provider]; !ok {
				writeProviderResolutionError(w, fmt.Sprintf("provider %q is not configured", route.provider), http.StatusBadRequest)
				return
			}
			if logger != nil {
				logger.Debug("resolved upstream provider", "path", r.URL.Path, "provider", route.provider, "anthropic_compat", route.anthropicCompat)
			}

			*r = *r.WithContext(context.WithValue(r.Context(), resolvedRouteContextKey{}, route))
			next.ServeHTTP(w, r)
		})
	}
}

func resolveProvider(r *http.Request, requestBody []byte, defaultProvider config.ProviderConfigName) (resolvedRoute, error) {
	normalized := strings.TrimSuffix(strings.TrimSpace(r.URL.Path), "/")
	if normalized == "" {
		normalized = "/"
	}

	switch {
	case normalized == "/v1/messages":
		return resolvedRoute{provider: config.ProviderAnthropic}, nil
	case strings.HasPrefix(normalized, "/api/"):
		return resolvedRoute{provider: config.ProviderOllama}, nil
	case normalized == "/v1/models":
		provider, ok := parseProviderOverride(r)
		if !ok {
			return resolvedRoute{}, fmt.Errorf("%s header is required for /v1/models", providerOverrideHeader)
		}
		return resolvedRoute{provider: provider}, nil
	case isOpenAICompatiblePath(normalized):
		if provider, ok := parseProviderOverride(r); ok {
			route := resolvedRoute{provider: provider}
			if provider == config.ProviderAnthropic {
				if normalized != "/v1/chat/completions" {
					return resolvedRoute{}, fmt.Errorf("anthropic compatibility only supports /v1/chat/completions")
				}
				route.anthropicCompat = true
			}
			return route, nil
		}

		model, _ := extractModelAndStream(requestBody)
		if model == "" {
			return resolvedRoute{}, fmt.Errorf("could not resolve provider: request model is required")
		}

		provider, ok := detectProviderForModel(model)
		if !ok {
			return resolvedRoute{}, fmt.Errorf("could not resolve provider for model %q", model)
		}
		if provider == config.ProviderAnthropic {
			if normalized != "/v1/chat/completions" {
				return resolvedRoute{}, fmt.Errorf("anthropic compatibility only supports /v1/chat/completions")
			}
			return resolvedRoute{provider: provider, anthropicCompat: true}, nil
		}
		return resolvedRoute{provider: provider}, nil
	default:
		if defaultProvider == "" {
			return resolvedRoute{}, fmt.Errorf("could not resolve provider for path %q", r.URL.Path)
		}
		return resolvedRoute{provider: defaultProvider}, nil
	}
}

func parseProviderOverride(r *http.Request) (config.ProviderConfigName, bool) {
	raw := strings.TrimSpace(strings.ToLower(r.Header.Get(providerOverrideHeader)))
	switch config.ProviderConfigName(raw) {
	case config.ProviderOpenAI, config.ProviderAnthropic, config.ProviderGoogle, config.ProviderOllama, config.ProviderCustom:
		return config.ProviderConfigName(raw), true
	default:
		return "", false
	}
}

func detectProviderForModel(model string) (config.ProviderConfigName, bool) {
	normalized := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(normalized, "claude-"):
		return config.ProviderAnthropic, true
	case strings.HasPrefix(normalized, "gemini-"):
		return config.ProviderGoogle, true
	case strings.HasPrefix(normalized, "gpt-") || strings.HasPrefix(normalized, "o1") || strings.HasPrefix(normalized, "o3") || strings.HasPrefix(normalized, "o4") || strings.HasPrefix(normalized, "text-embedding-"):
		return config.ProviderOpenAI, true
	case strings.HasPrefix(normalized, "llama"), strings.HasPrefix(normalized, "mistral"), strings.HasPrefix(normalized, "mixtral"), strings.HasPrefix(normalized, "qwen"), strings.HasPrefix(normalized, "phi"), strings.HasPrefix(normalized, "gemma"), strings.HasPrefix(normalized, "deepseek"), strings.HasPrefix(normalized, "codellama"), strings.HasPrefix(normalized, "dolphin"), strings.HasPrefix(normalized, "orca"), strings.HasPrefix(normalized, "wizard"), strings.HasPrefix(normalized, "vicuna"), strings.HasPrefix(normalized, "solar"), strings.HasPrefix(normalized, "granite"), strings.HasPrefix(normalized, "smollm"):
		return config.ProviderOllama, true
	default:
		return "", false
	}
}

func isOpenAICompatiblePath(path string) bool {
	switch strings.TrimSuffix(strings.TrimSpace(path), "/") {
	case "/v1/chat/completions", "/v1/completions", "/v1/embeddings":
		return true
	default:
		return false
	}
}

func writeProviderResolutionError(w http.ResponseWriter, message string, statusCode int) {
	payload, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"code":    "provider_resolution_failed",
			"message": message,
		},
	})
	if err != nil {
		http.Error(w, message, statusCode)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_, _ = w.Write(payload)
}

func serveAnthropicOpenAICompat(w http.ResponseWriter, r *http.Request, route resolvedRoute, targets map[config.ProviderConfigName]*url.URL, hooks Hooks) {
	target := targets[route.provider]
	if target == nil {
		writeProviderResolutionError(w, fmt.Sprintf("provider %q is not configured", route.provider), http.StatusBadRequest)
		return
	}

	requestBody, err := io.ReadAll(r.Body)
	if err != nil {
		writeConfigError(w, fmt.Sprintf("read request body: %v", err))
		return
	}

	anthropicBody, model, streaming, err := openAIChatToAnthropic(requestBody)
	if err != nil {
		writeProviderResolutionError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if streaming {
		writeProviderResolutionError(w, "anthropic OpenAI SDK compatibility currently supports non-streaming chat completions only", http.StatusBadRequest)
		return
	}

	upstreamURL := *target
	upstreamURL.Path = "/v1/messages"
	upstreamURL.RawQuery = r.URL.RawQuery

	upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL.String(), bytes.NewReader(anthropicBody))
	if err != nil {
		writeConfigError(w, fmt.Sprintf("build anthropic compatibility request: %v", err))
		return
	}
	copyCompatibilityHeaders(upstreamReq.Header, r.Header)
	upstreamReq.Header.Set("Content-Type", "application/json")
	if upstreamReq.Header.Get("anthropic-version") == "" {
		upstreamReq.Header.Set("anthropic-version", "2023-06-01")
	}
	if apiKey := extractBearerToken(r); apiKey != "" {
		upstreamReq.Header.Set("x-api-key", apiKey)
	} else if apiKey := strings.TrimSpace(r.Header.Get("x-api-key")); apiKey != "" {
		upstreamReq.Header.Set("x-api-key", apiKey)
	}

	resp, err := http.DefaultClient.Do(upstreamReq)
	if err != nil {
		http.Error(w, fmt.Sprintf("upstream proxy error: %v", err), http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("read upstream response: %v", err), http.StatusBadGateway)
		return
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		copyCompatibilityResponseHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}

	translated, err := anthropicToOpenAIChatCompletion(body, model)
	if err != nil {
		writeConfigError(w, fmt.Sprintf("translate anthropic response: %v", err))
		return
	}

	if hooks.Budget != nil && hooks.Pricing != nil {
		if meta, ok := r.Context().Value(budgetContextKey{}).(budgetRequestMeta); ok {
			usage := pricing.ExtractUsageFromResponse(string(config.ProviderAnthropic), body, hooks.Logger)
			cost := hooks.Pricing.CalculateCost(meta.model, usage.InputTokens, usage.OutputTokens)
			hooks.Budget.RecordSpend(meta.agent, cost)
			meta.taskReservation.Settle(cost)
			if hooks.CostSink != nil {
				hooks.CostSink.Enqueue(storage.CostRecord{
					Agent:         meta.agent,
					Model:         meta.model,
					Provider:      meta.provider,
					TraceID:       meta.traceID,
					TaskID:        meta.taskID,
					OriginalModel: meta.originalModel,
					InputTokens:   usage.InputTokens,
					OutputTokens:  usage.OutputTokens,
					CostUSD:       cost,
					Downgraded:    meta.downgraded,
					CreatedAt:     time.Now().UTC(),
				})
			}
			if meta.downgraded && meta.originalModel != "" {
				w.Header().Set("X-Oberwatch-Downgraded", "true")
				w.Header().Set("X-Oberwatch-Original-Model", meta.originalModel)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(translated)
}

func openAIChatToAnthropic(raw []byte) ([]byte, string, bool, error) {
	//nolint:govet // local decode structs mirror the external payload shape.
	var request struct {
		Model string `json:"model"`
		//nolint:govet // local decode structs mirror the external payload shape.
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
		MaxTokens   int      `json:"max_tokens"`
		Temperature *float64 `json:"temperature,omitempty"`
		Stream      bool     `json:"stream"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, "", false, fmt.Errorf("decode OpenAI chat completion request: %w", err)
	}
	model := strings.TrimSpace(request.Model)
	if model == "" {
		return nil, "", false, fmt.Errorf("request model is required")
	}

	var systemParts []string
	messages := make([]map[string]any, 0, len(request.Messages))
	for _, message := range request.Messages {
		content := extractMessageContentText(message.Content)
		if content == "" {
			continue
		}
		if strings.EqualFold(message.Role, "system") {
			systemParts = append(systemParts, content)
			continue
		}
		messages = append(messages, map[string]any{
			"role":    message.Role,
			"content": content,
		})
	}
	if len(messages) == 0 {
		return nil, "", false, fmt.Errorf("at least one non-system message is required")
	}

	maxTokens := request.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}

	payload := map[string]any{
		"model":      model,
		"messages":   messages,
		"max_tokens": maxTokens,
	}
	if len(systemParts) > 0 {
		payload["system"] = strings.Join(systemParts, "\n\n")
	}
	if request.Temperature != nil {
		payload["temperature"] = *request.Temperature
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, "", false, fmt.Errorf("encode anthropic compatibility request: %w", err)
	}
	return encoded, model, request.Stream, nil
}

func anthropicToOpenAIChatCompletion(raw []byte, model string) ([]byte, error) {
	var response struct {
		ID         string `json:"id"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("decode anthropic response: %w", err)
	}
	if strings.TrimSpace(response.Model) != "" {
		model = response.Model
	}

	var contentBuilder strings.Builder
	for _, block := range response.Content {
		if block.Type != "text" {
			continue
		}
		contentBuilder.WriteString(block.Text)
	}

	payload := map[string]any{
		"id":      response.ID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": contentBuilder.String(),
				},
				"finish_reason": mapAnthropicStopReason(response.StopReason),
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     response.Usage.InputTokens,
			"completion_tokens": response.Usage.OutputTokens,
			"total_tokens":      response.Usage.InputTokens + response.Usage.OutputTokens,
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode OpenAI-compatible response: %w", err)
	}
	return encoded, nil
}

func extractMessageContentText(content any) string {
	switch value := content.(type) {
	case string:
		return strings.TrimSpace(value)
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if strings.TrimSpace(fmt.Sprintf("%v", block["type"])) != "text" {
				continue
			}
			text := strings.TrimSpace(fmt.Sprintf("%v", block["text"]))
			if text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func extractBearerToken(r *http.Request) string {
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if authorization == "" {
		return ""
	}
	parts := strings.Fields(authorization)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func copyCompatibilityHeaders(dst http.Header, src http.Header) {
	for key, values := range src {
		switch {
		case strings.EqualFold(key, "Authorization"):
			continue
		case strings.EqualFold(key, "X-API-Key"):
			continue
		case strings.HasPrefix(strings.ToLower(key), oberwatchHeaderPrefixLower):
			continue
		case strings.EqualFold(key, "Content-Length"):
			continue
		case strings.EqualFold(key, "Host"):
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func copyCompatibilityResponseHeaders(dst http.Header, src http.Header) {
	for key, values := range src {
		if strings.EqualFold(key, "Content-Length") {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func mapAnthropicStopReason(reason string) string {
	switch strings.TrimSpace(reason) {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	default:
		return "stop"
	}
}

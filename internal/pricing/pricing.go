package pricing

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"

	"github.com/OberWatch/oberwatch/internal/config"
)

const tokensPerByteEstimateDivisor = 4

var anthropicSnapshotSuffixPattern = regexp.MustCompile(`-\d{8}$`)

var defaultPricingTable = NewPricingTable(nil, nil)

// ModelPricing defines token pricing for one model in USD per million tokens.
type ModelPricing struct {
	Model            string
	Provider         string
	InputPerMillion  float64
	OutputPerMillion float64
}

// Usage captures extracted token usage from an upstream response.
type Usage struct {
	InputTokens  int
	OutputTokens int
	Estimated    bool
}

// PricingTable stores known model pricing and calculates request costs.
//
//nolint:revive // Name is required by product spec.
type PricingTable struct {
	logger  *slog.Logger
	pricing map[string]ModelPricing
}

// DefaultModelPricing returns the built-in pricing defaults from the published
// provider rate cards. Keep in sync with publishedDefaultRates in pricing_test.go.
//
// Only models whose published rate is a single input/output pair belong here.
// A model billed at different rates above a context threshold cannot be
// expressed by ModelPricing and would undercount long requests, so it is left
// out and priced by a [[pricing]] override instead. See contextTieredModels in
// pricing_catalog_test.go.
func DefaultModelPricing() []ModelPricing {
	return []ModelPricing{
		{Model: "gpt-5.6-sol", Provider: "openai", InputPerMillion: 4.00, OutputPerMillion: 20.00},
		{Model: "gpt-5.6-terra", Provider: "openai", InputPerMillion: 2.00, OutputPerMillion: 12.00},
		{Model: "gpt-5.6-luna", Provider: "openai", InputPerMillion: 0.20, OutputPerMillion: 1.20},
		{Model: "claude-fable-5", Provider: "anthropic", InputPerMillion: 10.00, OutputPerMillion: 50.00},
		{Model: "claude-opus-5", Provider: "anthropic", InputPerMillion: 5.00, OutputPerMillion: 25.00},
		{Model: "claude-sonnet-5", Provider: "anthropic", InputPerMillion: 2.00, OutputPerMillion: 10.00},
		{Model: "claude-haiku-4-5", Provider: "anthropic", InputPerMillion: 1.00, OutputPerMillion: 5.00},
		{Model: "gemini-3.7-flash", Provider: "google", InputPerMillion: 0.75, OutputPerMillion: 3.75},
		{Model: "gemini-3.5-flash-lite", Provider: "google", InputPerMillion: 0.30, OutputPerMillion: 2.50},
	}
}

// NewPricingTable builds a pricing table from built-in defaults plus optional overrides.
func NewPricingTable(overrides []ModelPricing, logger *slog.Logger) *PricingTable {
	pricingMap := make(map[string]ModelPricing)
	for _, entry := range DefaultModelPricing() {
		pricingMap[normalizeModel(entry.Model)] = entry
	}
	for _, override := range overrides {
		pricingMap[normalizeModel(override.Model)] = override
	}
	return &PricingTable{
		pricing: pricingMap,
		logger:  logger,
	}
}

// NewPricingTableFromConfig builds a pricing table using overrides from config pricing entries.
func NewPricingTableFromConfig(entries []config.PricingEntry, logger *slog.Logger) *PricingTable {
	overrides := make([]ModelPricing, 0, len(entries))
	for _, entry := range entries {
		overrides = append(overrides, ModelPricing{
			Model:            entry.Model,
			Provider:         string(entry.Provider),
			InputPerMillion:  entry.InputPerMillion,
			OutputPerMillion: entry.OutputPerMillion,
		})
	}
	return NewPricingTable(overrides, logger)
}

// PriceForModel returns pricing metadata for a model.
func (t *PricingTable) PriceForModel(model string) (ModelPricing, bool) {
	entry, ok := t.pricing[normalizeModel(model)]
	return entry, ok
}

// CalculateCost calculates request cost in USD for the given model and token counts.
func (t *PricingTable) CalculateCost(model string, inputTokens int, outputTokens int) float64 {
	if inputTokens < 0 {
		inputTokens = 0
	}
	if outputTokens < 0 {
		outputTokens = 0
	}

	entry, ok := t.PriceForModel(model)
	if !ok {
		if t.logger != nil {
			t.logger.Warn("unknown model pricing, returning zero cost", "model", model)
		}
		return 0
	}

	inputCost := (float64(inputTokens) / 1_000_000.0) * entry.InputPerMillion
	outputCost := (float64(outputTokens) / 1_000_000.0) * entry.OutputPerMillion
	return inputCost + outputCost
}

// CalculateCost calculates request cost in USD using the built-in pricing table.
func CalculateCost(model string, inputTokens int, outputTokens int) float64 {
	return defaultPricingTable.CalculateCost(model, inputTokens, outputTokens)
}

// ExtractUsageFromResponse extracts token usage from a non-streaming provider response body.
func ExtractUsageFromResponse(provider string, responseBody []byte, logger *slog.Logger) Usage {
	inputTokens, outputTokens, ok := extractUsage(responseBody)
	if ok {
		return Usage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			Estimated:    false,
		}
	}

	estimated := estimateTokensFromBodyLength(len(responseBody))
	warnMissingUsage(logger, provider, len(responseBody), false)
	return Usage{
		InputTokens:  estimated,
		OutputTokens: estimated,
		Estimated:    true,
	}
}

// AccumulateStreamingUsage extracts usage from a fully buffered SSE response.
// When no usage field is found, it falls back to accumulating delta.content byte length
// before resorting to whole-body-length estimation.
func AccumulateStreamingUsage(provider string, sseChunks []byte, logger *slog.Logger) Usage {
	var inputTokens, outputTokens, deltaContentLen int
	scanner := bufio.NewScanner(bytes.NewReader(sseChunks))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		raw := []byte(payload)
		if input, output, ok := extractUsage(raw); ok {
			if input > 0 {
				inputTokens = input
			}
			if output > 0 {
				outputTokens = output
			}
			continue
		}
		if content, ok := extractDeltaContent(raw); ok {
			deltaContentLen += len(content)
		}
	}

	if inputTokens > 0 || outputTokens > 0 {
		return Usage{InputTokens: inputTokens, OutputTokens: outputTokens}
	}
	if deltaContentLen > 0 {
		tokens := estimateTokensFromBodyLength(deltaContentLen)
		warnMissingUsage(logger, provider, deltaContentLen, true)
		return Usage{OutputTokens: tokens, Estimated: true}
	}
	estimated := estimateTokensFromBodyLength(len(sseChunks))
	warnMissingUsage(logger, provider, len(sseChunks), true)
	return Usage{InputTokens: estimated, OutputTokens: estimated, Estimated: true}
}

func extractUsage(rawJSON []byte) (int, int, bool) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(rawJSON, &envelope); err != nil {
		return 0, 0, false
	}

	inputTokens, outputTokens, ok := parseUsageEnvelope(envelope)
	if ok {
		return inputTokens, outputTokens, true
	}

	messageRaw, found := envelope["message"]
	if !found {
		return 0, 0, false
	}

	var messageEnvelope map[string]json.RawMessage
	if err := json.Unmarshal(messageRaw, &messageEnvelope); err != nil {
		return 0, 0, false
	}
	return parseUsageEnvelope(messageEnvelope)
}

func parseUsageEnvelope(envelope map[string]json.RawMessage) (int, int, bool) {
	usageRaw, found := envelope["usage"]
	if !found {
		return 0, 0, false
	}

	var usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		InputTokens      int `json:"input_tokens"`
		OutputTokens     int `json:"output_tokens"`
	}
	if err := json.Unmarshal(usageRaw, &usage); err != nil {
		return 0, 0, false
	}

	inputTokens := usage.InputTokens
	if inputTokens == 0 {
		inputTokens = usage.PromptTokens
	}
	outputTokens := usage.OutputTokens
	if outputTokens == 0 {
		outputTokens = usage.CompletionTokens
	}
	if inputTokens == 0 && outputTokens == 0 {
		return 0, 0, false
	}

	return inputTokens, outputTokens, true
}

func warnMissingUsage(logger *slog.Logger, provider string, responseLength int, streaming bool) {
	if logger == nil {
		return
	}
	logger.Warn(
		"usage data missing, estimating tokens from response body length",
		"provider",
		strings.ToLower(strings.TrimSpace(provider)),
		"streaming",
		streaming,
		"response_bytes",
		responseLength,
	)
}

func estimateTokensFromBodyLength(responseLength int) int {
	if responseLength <= 0 {
		return 0
	}

	estimated := responseLength / tokensPerByteEstimateDivisor
	if estimated < 1 {
		return 1
	}
	return estimated
}

func normalizeModel(model string) string {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if strings.HasPrefix(normalized, "claude-") {
		normalized = anthropicSnapshotSuffixPattern.ReplaceAllString(normalized, "")
	}
	return normalized
}

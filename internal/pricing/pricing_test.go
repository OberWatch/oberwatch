package pricing

import (
	"bytes"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/OberWatch/oberwatch/internal/config"
)

// publishedDefaultRates mirrors the provider list prices recorded in the source
// note on DefaultModelPricing. Update both together, never one alone.
var publishedDefaultRates = []ModelPricing{
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

func TestDefaultModelPricing_MatchesPublishedProviderRates(t *testing.T) {
	t.Parallel()

	defaults := DefaultModelPricing()
	if len(defaults) != len(publishedDefaultRates) {
		t.Fatalf("DefaultModelPricing() length = %d, want %d", len(defaults), len(publishedDefaultRates))
	}
	if !reflect.DeepEqual(defaults, publishedDefaultRates) {
		t.Fatalf("DefaultModelPricing() = %#v, want %#v", defaults, publishedDefaultRates)
	}

	table := NewPricingTable(nil, nil)
	for _, want := range publishedDefaultRates {
		want := want
		t.Run(want.Model, func(t *testing.T) {
			t.Parallel()
			got, ok := table.PriceForModel(want.Model)
			if !ok {
				t.Fatalf("PriceForModel(%q) found = false, want true", want.Model)
			}
			if got != want {
				t.Fatalf("PriceForModel(%q) = %#v, want %#v", want.Model, got, want)
			}
		})
	}
}

func TestDefaultModelPricing_DropsSupersededCatalogEntries(t *testing.T) {
	t.Parallel()

	// Superseded by the current provider line-ups. They stay usable through
	// [[pricing]] overrides, but must not ship as defaults.
	superseded := []string{
		"gpt-4o", "gpt-4o-mini", "gpt-4.1", "gpt-4.1-mini",
		"claude-opus-4-6", "claude-sonnet-4-6",
		"gemini-2.5-pro", "gemini-2.5-flash",
	}

	table := NewPricingTable(nil, nil)
	for _, model := range superseded {
		model := model
		t.Run(model, func(t *testing.T) {
			t.Parallel()
			if _, ok := table.PriceForModel(model); ok {
				t.Fatalf("PriceForModel(%q) found = true, want false", model)
			}
		})
	}
}

func TestDefaultModelPricing_MatchesConfigDefaults(t *testing.T) {
	t.Parallel()

	configDefaults := config.DefaultConfig().Pricing
	defaults := DefaultModelPricing()
	if len(configDefaults) != len(defaults) {
		t.Fatalf("config default pricing length = %d, want %d", len(configDefaults), len(defaults))
	}
	for i, want := range defaults {
		got := configDefaults[i]
		if got.Model != want.Model || string(got.Provider) != want.Provider ||
			got.InputPerMillion != want.InputPerMillion || got.OutputPerMillion != want.OutputPerMillion {
			t.Fatalf("config pricing[%d] = %#v, want %#v", i, got, want)
		}
	}
}

func TestPriceForModel_AliasesAndVersionedIDs(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep table fields readable for rate assertions.
	tests := []struct {
		name       string
		model      string
		wantInput  float64
		wantOutput float64
	}{
		{
			name:       "anthropic dated snapshot resolves to its alias rate",
			model:      "claude-haiku-4-5-20251001",
			wantInput:  1.00,
			wantOutput: 5.00,
		},
		{
			name:       "anthropic dateless id is its own pinned snapshot",
			model:      "claude-opus-5",
			wantInput:  5.00,
			wantOutput: 25.00,
		},
		{
			name:       "model ids are matched case-insensitively after trimming",
			model:      "  GPT-5.6-Sol  ",
			wantInput:  4.00,
			wantOutput: 20.00,
		},
	}

	table := NewPricingTable(nil, nil)
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := table.PriceForModel(tt.model)
			if !ok {
				t.Fatalf("PriceForModel(%q) found = false, want true", tt.model)
			}
			if got.InputPerMillion != tt.wantInput || got.OutputPerMillion != tt.wantOutput {
				t.Fatalf("PriceForModel(%q) = input:%v output:%v, want input:%v output:%v",
					tt.model, got.InputPerMillion, got.OutputPerMillion, tt.wantInput, tt.wantOutput)
			}
		})
	}
}

func TestCalculateCost_AllDefaultModels(t *testing.T) {
	t.Parallel()

	defaults := DefaultModelPricing()
	tests := make([]struct {
		model       string
		name        string
		wantCostUSD float64
	}, 0, len(defaults))
	for _, entry := range defaults {
		tests = append(tests, struct {
			model       string
			name        string
			wantCostUSD float64
		}{
			name:        "cost for " + entry.Model,
			model:       entry.Model,
			wantCostUSD: entry.InputPerMillion + entry.OutputPerMillion,
		})
	}

	table := NewPricingTable(nil, nil)
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := table.CalculateCost(tt.model, 1_000_000, 1_000_000)
			if math.Abs(got-tt.wantCostUSD) > 1e-9 {
				t.Fatalf("CalculateCost(%q) = %v, want %v", tt.model, got, tt.wantCostUSD)
			}
		})
	}
}

func TestCalculateCost_AnthropicSnapshotModelUsesBasePricing(t *testing.T) {
	t.Parallel()

	table := NewPricingTable(nil, nil)

	got := table.CalculateCost("claude-haiku-4-5-20251001", 1_000_000, 1_000_000)
	want := 6.0 // $1.00 input + $5.00 output per million tokens
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("CalculateCost(snapshot model) = %v, want %v", got, want)
	}
}

func TestPricingOverrides_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		model       string
		overrides   []ModelPricing
		wantCost    float64
		inputToken  int
		outputToken int
	}{
		{
			name:  "override existing model price",
			model: "gpt-5.6-terra",
			overrides: []ModelPricing{
				{Model: "gpt-5.6-terra", Provider: "openai", InputPerMillion: 3.00, OutputPerMillion: 12.00},
			},
			inputToken:  1_000_000,
			outputToken: 1_000_000,
			wantCost:    15.00,
		},
		{
			name:  "add custom model pricing",
			model: "custom-model-x",
			overrides: []ModelPricing{
				{Model: "custom-model-x", Provider: "custom", InputPerMillion: 4.00, OutputPerMillion: 6.00},
			},
			inputToken:  500_000,
			outputToken: 500_000,
			wantCost:    5.00,
		},
		{
			name:        "unknown model without override",
			model:       "missing-model",
			overrides:   nil,
			inputToken:  1_000,
			outputToken: 1_000,
			wantCost:    0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			table := NewPricingTable(tt.overrides, nil)
			got := table.CalculateCost(tt.model, tt.inputToken, tt.outputToken)
			if math.Abs(got-tt.wantCost) > 1e-9 {
				t.Fatalf("CalculateCost() = %v, want %v", got, tt.wantCost)
			}
		})
	}
}

func TestNewPricingTableFromConfig_TableDriven(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep table fields readable for money-math test intent.
	tests := []struct {
		name            string
		entries         []config.PricingEntry
		model           string
		wantInputPrice  float64
		wantOutputPrice float64
	}{
		{
			name: "applies config override on default model",
			entries: []config.PricingEntry{
				{Model: "claude-sonnet-5", Provider: config.ProviderAnthropic, InputPerMillion: 9.00, OutputPerMillion: 9.50},
			},
			model:           "claude-sonnet-5",
			wantInputPrice:  9.00,
			wantOutputPrice: 9.50,
		},
		{
			name: "adds new custom model from config",
			entries: []config.PricingEntry{
				{Model: "my-custom", Provider: config.ProviderCustom, InputPerMillion: 1.10, OutputPerMillion: 2.20},
			},
			model:           "my-custom",
			wantInputPrice:  1.10,
			wantOutputPrice: 2.20,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			table := NewPricingTableFromConfig(tt.entries, nil)
			got, ok := table.PriceForModel(tt.model)
			if !ok {
				t.Fatalf("PriceForModel(%q) found = false, want true", tt.model)
			}
			if got.InputPerMillion != tt.wantInputPrice || got.OutputPerMillion != tt.wantOutputPrice {
				t.Fatalf("price = input:%v output:%v, want input:%v output:%v", got.InputPerMillion, got.OutputPerMillion, tt.wantInputPrice, tt.wantOutputPrice)
			}
		})
	}
}

func TestExtractUsageFromResponse_Fixtures(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep table fields readable for fixture assertions.
	tests := []struct {
		fixture        string
		provider       string
		name           string
		wantInput      int
		wantOutput     int
		wantEstimated  bool
		wantWarnSubstr string
	}{
		{
			name:          "extract openai usage",
			fixture:       "openai_response.json",
			provider:      "openai",
			wantInput:     120,
			wantOutput:    80,
			wantEstimated: false,
		},
		{
			name:          "extract anthropic usage",
			fixture:       "anthropic_response.json",
			provider:      "anthropic",
			wantInput:     210,
			wantOutput:    95,
			wantEstimated: false,
		},
		{
			name:           "missing usage falls back to estimate and warns",
			fixture:        "missing_usage_response.json",
			provider:       "openai",
			wantEstimated:  true,
			wantWarnSubstr: "usage data missing",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw := readFixture(t, tt.fixture)
			logger, logs := newTestLogger()

			got := ExtractUsageFromResponse(tt.provider, raw, logger)
			if got.Estimated != tt.wantEstimated {
				t.Fatalf("Estimated = %v, want %v", got.Estimated, tt.wantEstimated)
			}
			if tt.wantInput > 0 && got.InputTokens != tt.wantInput {
				t.Fatalf("InputTokens = %d, want %d", got.InputTokens, tt.wantInput)
			}
			if tt.wantOutput > 0 && got.OutputTokens != tt.wantOutput {
				t.Fatalf("OutputTokens = %d, want %d", got.OutputTokens, tt.wantOutput)
			}
			if tt.wantEstimated {
				if got.InputTokens < 1 || got.OutputTokens < 1 {
					t.Fatalf("fallback estimated usage must be >=1 token, got input=%d output=%d", got.InputTokens, got.OutputTokens)
				}
			}
			if tt.wantWarnSubstr != "" && !strings.Contains(logs.String(), tt.wantWarnSubstr) {
				t.Fatalf("logs = %q, want substring %q", logs.String(), tt.wantWarnSubstr)
			}
		})
	}
}

func TestAccumulateStreamingUsage_Fixtures(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep table fields readable for fixture assertions.
	tests := []struct {
		fixture        string
		provider       string
		name           string
		wantInput      int
		wantOutput     int
		wantEstimated  bool
		wantWarnSubstr string
	}{
		{
			name:          "openai stream final chunk includes usage",
			fixture:       "openai_stream_with_usage.sse",
			provider:      "openai",
			wantInput:     120,
			wantOutput:    80,
			wantEstimated: false,
		},
		{
			name:          "anthropic stream message usage is accumulated",
			fixture:       "anthropic_stream_with_usage.sse",
			provider:      "anthropic",
			wantInput:     210,
			wantOutput:    95,
			wantEstimated: false,
		},
		{
			name:           "stream without usage falls back and warns",
			fixture:        "stream_missing_usage.sse",
			provider:       "openai",
			wantEstimated:  true,
			wantWarnSubstr: "usage data missing",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw := readFixture(t, tt.fixture)
			logger, logs := newTestLogger()

			got := AccumulateStreamingUsage(tt.provider, raw, logger)
			if got.Estimated != tt.wantEstimated {
				t.Fatalf("Estimated = %v, want %v", got.Estimated, tt.wantEstimated)
			}
			if tt.wantInput > 0 && got.InputTokens != tt.wantInput {
				t.Fatalf("InputTokens = %d, want %d", got.InputTokens, tt.wantInput)
			}
			if tt.wantOutput > 0 && got.OutputTokens != tt.wantOutput {
				t.Fatalf("OutputTokens = %d, want %d", got.OutputTokens, tt.wantOutput)
			}
			if tt.wantEstimated {
				// Delta-content fallback sets OutputTokens only; body-length fallback sets both.
				if got.OutputTokens < 1 {
					t.Fatalf("fallback estimated output tokens must be >=1, got input=%d output=%d", got.InputTokens, got.OutputTokens)
				}
			}
			if tt.wantWarnSubstr != "" && !strings.Contains(logs.String(), tt.wantWarnSubstr) {
				t.Fatalf("logs = %q, want substring %q", logs.String(), tt.wantWarnSubstr)
			}
		})
	}
}

func TestCalculateCost_PackageLevelFunction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		model          string
		inputTokens    int
		outputTokens   int
		wantCostBounds [2]float64
	}{
		{
			name:           "package helper uses built-in defaults",
			model:          "gpt-5.6-terra",
			inputTokens:    1000,
			outputTokens:   500,
			wantCostBounds: [2]float64{0.0079, 0.0081},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := CalculateCost(tt.model, tt.inputTokens, tt.outputTokens)
			if got < tt.wantCostBounds[0] || got > tt.wantCostBounds[1] {
				t.Fatalf("CalculateCost() = %v, want in range [%v, %v]", got, tt.wantCostBounds[0], tt.wantCostBounds[1])
			}
		})
	}
}

func TestPricingInternalBranches_TableDriven(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep table fields readable for branch testing.
	tests := []struct {
		name             string
		model            string
		inputTokens      int
		outputTokens     int
		wantCost         float64
		expectWarnSubstr string
	}{
		{
			name:         "negative tokens are clamped to zero",
			model:        "gpt-4o",
			inputTokens:  -10,
			outputTokens: -20,
			wantCost:     0,
		},
		{
			name:             "unknown model logs warning and returns zero",
			model:            "does-not-exist",
			inputTokens:      100,
			outputTokens:     200,
			wantCost:         0,
			expectWarnSubstr: "unknown model pricing",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			logger, logs := newTestLogger()
			table := NewPricingTable(nil, logger)
			got := table.CalculateCost(tt.model, tt.inputTokens, tt.outputTokens)
			if got != tt.wantCost {
				t.Fatalf("CalculateCost() = %v, want %v", got, tt.wantCost)
			}
			if tt.expectWarnSubstr != "" && !strings.Contains(logs.String(), tt.expectWarnSubstr) {
				t.Fatalf("logs = %q, want substring %q", logs.String(), tt.expectWarnSubstr)
			}
		})
	}
}

func TestExtractUsage_InternalParsers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		raw        string
		wantOK     bool
		wantInput  int
		wantOutput int
	}{
		{
			name:       "message nested usage is supported",
			raw:        `{"message":{"usage":{"input_tokens":11,"output_tokens":7}}}`,
			wantOK:     true,
			wantInput:  11,
			wantOutput: 7,
		},
		{
			name:   "invalid json returns false",
			raw:    `{"usage":`,
			wantOK: false,
		},
		{
			name:   "missing usage returns false",
			raw:    `{"id":"x"}`,
			wantOK: false,
		},
		{
			name:   "message is invalid object returns false",
			raw:    `{"message":"bad"}`,
			wantOK: false,
		},
		{
			name:   "usage with zero values returns false",
			raw:    `{"usage":{"input_tokens":0,"output_tokens":0}}`,
			wantOK: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotInput, gotOutput, gotOK := extractUsage([]byte(tt.raw))
			if gotOK != tt.wantOK {
				t.Fatalf("extractUsage() ok = %v, want %v", gotOK, tt.wantOK)
			}
			if tt.wantOK && (gotInput != tt.wantInput || gotOutput != tt.wantOutput) {
				t.Fatalf("extractUsage() = (%d, %d), want (%d, %d)", gotInput, gotOutput, tt.wantInput, tt.wantOutput)
			}
		})
	}
}

func TestEstimateTokensFromBodyLength_TableDriven(t *testing.T) {
	t.Parallel()

	//nolint:govet // keep table fields readable for estimator cases.
	tests := []struct {
		responseLength int
		name           string
		want           int
	}{
		{name: "zero length", responseLength: 0, want: 0},
		{name: "negative length", responseLength: -10, want: 0},
		{name: "small payload rounds up to one", responseLength: 1, want: 1},
		{name: "larger payload divides by divisor", responseLength: 400, want: 100},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := estimateTokensFromBodyLength(tt.responseLength)
			if got != tt.want {
				t.Fatalf("estimateTokensFromBodyLength(%d) = %d, want %d", tt.responseLength, got, tt.want)
			}
		})
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("testdata", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	return raw
}

func newTestLogger() (*slog.Logger, *bytes.Buffer) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{}))
	return logger, &logs
}

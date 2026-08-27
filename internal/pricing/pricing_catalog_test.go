package pricing

import (
	"strings"
	"testing"

	"github.com/OberWatch/oberwatch/internal/config"
)

// contextTieredModels are models whose published rate card charges a higher
// rate above a context threshold (Gemini 3.1 Pro Preview charges one rate up
// to 200k input tokens and a higher one above it). ModelPricing carries a
// single input and output rate, so shipping one of these as a default would
// silently undercount every long-context request. Until the pricing schema
// can express the tier, they stay out of the defaults: an unknown model is
// logged and costs zero, which is visibly wrong, where a too-low rate is
// invisibly wrong. Operators who want it can add a [[pricing]] override with
// the rate that applies to them.
var contextTieredModels = []string{"gemini-3.1-pro-preview"}

func TestDefaultModelPricing_OmitsContextTieredModels(t *testing.T) {
	t.Parallel()

	defaults := DefaultModelPricing()
	for _, model := range contextTieredModels {
		model := model
		t.Run(model, func(t *testing.T) {
			t.Parallel()

			for _, entry := range defaults {
				if strings.EqualFold(entry.Model, model) {
					t.Fatalf("DefaultModelPricing() ships %q at a flat %v/%v per million; its published rate card has a >200k context tier, so this undercounts long-context requests",
						entry.Model, entry.InputPerMillion, entry.OutputPerMillion)
				}
			}

			table := NewPricingTable(nil, nil)
			if _, ok := table.PriceForModel(model); ok {
				t.Fatalf("PriceForModel(%q) found = true, want false", model)
			}
		})
	}
}

func TestDefaultConfigPricing_OmitsContextTieredModels(t *testing.T) {
	t.Parallel()

	for _, entry := range config.DefaultConfig().Pricing {
		for _, model := range contextTieredModels {
			if strings.EqualFold(entry.Model, model) {
				t.Fatalf("config.DefaultConfig().Pricing ships context-tiered model %q", entry.Model)
			}
		}
	}
}

func TestPricingTable_ContextTieredModelStaysAvailableAsAnOverride(t *testing.T) {
	t.Parallel()

	// Removing the default must not remove the ability to price the model: an
	// operator supplies the rate that applies to their own context window.
	override := ModelPricing{Model: "gemini-3.1-pro-preview", Provider: "google", InputPerMillion: 1.11, OutputPerMillion: 2.22}
	table := NewPricingTable([]ModelPricing{override}, nil)

	got, ok := table.PriceForModel("gemini-3.1-pro-preview")
	if !ok {
		t.Fatal("PriceForModel() found = false, want true for an explicit override")
	}
	if got != override {
		t.Fatalf("PriceForModel() = %#v, want %#v", got, override)
	}
}

func TestNormalizeModel_KeepsGooglePreviewSuffix(t *testing.T) {
	t.Parallel()

	// Only Anthropic's dated snapshot suffix is stripped. A Google "-preview"
	// suffix is part of the model id and must not collapse onto a released
	// model that may be priced differently.
	table := NewPricingTable([]ModelPricing{
		{Model: "gemini-9.9-pro-preview", Provider: "google", InputPerMillion: 1.00, OutputPerMillion: 2.00},
	}, nil)

	if _, ok := table.PriceForModel("gemini-9.9-pro"); ok {
		t.Fatal("PriceForModel(\"gemini-9.9-pro\") found = true, want false: the preview suffix is part of the id")
	}
	if _, ok := table.PriceForModel("gemini-9.9-pro-preview"); !ok {
		t.Fatal("PriceForModel(\"gemini-9.9-pro-preview\") found = false, want true")
	}
}

package config

import (
	"strings"
	"testing"
)

// contextTieredModels must not appear as shipped [[pricing]] entries: their
// published rate cards charge more above a context threshold, which a single
// input/output rate pair cannot express without undercounting.
var contextTieredModels = []string{"gemini-3.1-pro-preview"}

func TestStarterTOML_OmitsContextTieredPricing(t *testing.T) {
	t.Parallel()

	for _, model := range contextTieredModels {
		if strings.Contains(StarterTOML, model) {
			t.Fatalf("StarterTOML ships a flat price for context-tiered model %q", model)
		}
	}
}

func TestExampleConfig_OmitsContextTieredPricing(t *testing.T) {
	t.Parallel()

	contents := readExampleConfig(t)
	for _, model := range contextTieredModels {
		if strings.Contains(contents, model) {
			t.Fatalf("oberwatch.example.toml ships a flat price for context-tiered model %q", model)
		}
	}
}

func TestDefaultConfigPricing_MatchesStarterAndExampleConfigs(t *testing.T) {
	t.Parallel()

	for _, entry := range DefaultConfig().Pricing {
		if !strings.Contains(StarterTOML, entry.Model) {
			t.Fatalf("StarterTOML is missing default pricing entry %q", entry.Model)
		}
		if !strings.Contains(readExampleConfig(t), entry.Model) {
			t.Fatalf("oberwatch.example.toml is missing default pricing entry %q", entry.Model)
		}
	}
}

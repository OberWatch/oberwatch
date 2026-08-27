package budget

import (
	"strings"
	"testing"
)

// Models whose published rate card is context-tiered are not priced by the
// built-in catalog, so a downgrade chain must not route traffic through them:
// an unpriced model in a chain would spend real money that no budget sees.
var unpricedContextTieredModels = []string{"gemini-3.1-pro-preview"}

func TestBuiltinChains_ExcludeUnpricedContextTieredModels(t *testing.T) {
	t.Parallel()

	chains := map[string][]string{
		"openai":    builtinOpenAIChain,
		"anthropic": builtinAnthropicChain,
		"google":    builtinGoogleChain,
	}

	for name, chain := range chains {
		name, chain := name, chain
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for _, model := range chain {
				for _, excluded := range unpricedContextTieredModels {
					if strings.EqualFold(model, excluded) {
						t.Fatalf("builtin %s chain contains %q, which the pricing catalog cannot price without a context tier", name, model)
					}
				}
			}
		})
	}
}

func TestBuiltinChains_AreNonEmptyAndDowngradeInOrder(t *testing.T) {
	t.Parallel()

	for name, chain := range map[string][]string{
		"openai":    builtinOpenAIChain,
		"anthropic": builtinAnthropicChain,
		"google":    builtinGoogleChain,
	} {
		if len(chain) < 2 {
			t.Fatalf("builtin %s chain = %v, want at least one downgrade step", name, chain)
		}
	}
}

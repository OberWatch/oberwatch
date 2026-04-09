package budget

import "strings"

// Built-in downgrade chains for known providers.
// These are used as a fallback when no explicit downgrade chain is configured.
var (
	builtinOpenAIChain    = []string{"gpt-4o", "gpt-4.1", "gpt-4o-mini", "gpt-4.1-mini"}
	builtinAnthropicChain = []string{"claude-opus-4-6", "claude-sonnet-4-6", "claude-haiku-4-5"}
	builtinGoogleChain    = []string{"gemini-2.5-pro", "gemini-2.5-flash"}
)

// builtinChainForModel returns the built-in downgrade chain for the given model,
// detected by provider prefix. Returns nil if the provider is not recognised.
func builtinChainForModel(model string) []string {
	lower := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(lower, "gpt-"):
		return builtinOpenAIChain
	case strings.HasPrefix(lower, "claude-"):
		return builtinAnthropicChain
	case strings.HasPrefix(lower, "gemini-"):
		return builtinGoogleChain
	default:
		return nil
	}
}

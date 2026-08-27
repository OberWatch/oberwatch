package budget

import "strings"

// Built-in downgrade chains for known providers.
// These are used as a fallback when no explicit downgrade chain is configured.
//
// Every model in a chain must be priced by the built-in pricing catalog,
// otherwise a downgrade would route spend that no budget can see.
var (
	builtinOpenAIChain    = []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"}
	builtinAnthropicChain = []string{"claude-fable-5", "claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5"}
	builtinGoogleChain    = []string{"gemini-3.7-flash", "gemini-3.5-flash-lite"}
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

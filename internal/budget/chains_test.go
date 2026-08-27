package budget

import (
	"reflect"
	"testing"
	"time"
)

func TestBuiltinChainForModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		model string
		want  []string
	}{
		{
			name:  "OpenAI gpt-5.6-sol selects OpenAI chain",
			model: "gpt-5.6-sol",
			want:  builtinOpenAIChain,
		},
		{
			name:  "OpenAI gpt-5.6-luna selects OpenAI chain",
			model: "gpt-5.6-luna",
			want:  builtinOpenAIChain,
		},
		{
			name:  "Anthropic claude-opus-5 selects Anthropic chain",
			model: "claude-opus-5",
			want:  builtinAnthropicChain,
		},
		{
			name:  "Anthropic claude-haiku-4-5 selects Anthropic chain",
			model: "claude-haiku-4-5",
			want:  builtinAnthropicChain,
		},
		{
			name:  "Google gemini-3.1-pro-preview selects Google chain",
			model: "gemini-3.1-pro-preview",
			want:  builtinGoogleChain,
		},
		{
			name:  "Google gemini-3.5-flash-lite selects Google chain",
			model: "gemini-3.5-flash-lite",
			want:  builtinGoogleChain,
		},
		{
			name:  "Unknown model returns nil",
			model: "llama-3.1-70b",
			want:  nil,
		},
		{
			name:  "Empty model returns nil",
			model: "",
			want:  nil,
		},
		{
			name:  "Mixed case OpenAI model matches",
			model: "GPT-5.6-Terra",
			want:  builtinOpenAIChain,
		},
		{
			name:  "Mixed case Anthropic model matches",
			model: "Claude-Sonnet-5",
			want:  builtinAnthropicChain,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := builtinChainForModel(tt.model)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("builtinChainForModel(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

func TestRewriteModelForDowngrade_BuiltinChain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		model         string
		wantNext      string
		wantDowngrade bool
	}{
		{
			name:          "OpenAI gpt-5.6-sol downgrades via builtin chain",
			model:         "gpt-5.6-sol",
			wantNext:      "gpt-5.6-terra",
			wantDowngrade: true,
		},
		{
			name:          "OpenAI gpt-5.6-luna is last in chain, no downgrade",
			model:         "gpt-5.6-luna",
			wantDowngrade: false,
		},
		{
			name:          "Anthropic claude-fable-5 downgrades via builtin chain",
			model:         "claude-fable-5",
			wantNext:      "claude-opus-5",
			wantDowngrade: true,
		},
		{
			name:          "Anthropic claude-opus-5 downgrades via builtin chain",
			model:         "claude-opus-5",
			wantNext:      "claude-sonnet-5",
			wantDowngrade: true,
		},
		{
			name:          "Anthropic claude-haiku-4-5 is last in chain, no downgrade",
			model:         "claude-haiku-4-5",
			wantDowngrade: false,
		},
		{
			name:          "Google gemini-3.5-flash-lite is last in chain, no downgrade",
			model:         "gemini-3.5-flash-lite",
			wantDowngrade: false,
		},
		{
			name:          "Unknown model has no builtin chain, no downgrade",
			model:         "llama-3.1-70b",
			wantDowngrade: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Manager with no explicit chain configured.
			cfg := baseGateConfig()
			cfg.DefaultDowngradeChain = nil
			manager := NewManagerWithClock(cfg, nil, newMockClock(time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC)))

			body := []byte(`{"model":"` + tt.model + `","messages":[]}`)
			_, originalModel, newModel, downgraded, err := manager.RewriteModelForDowngrade("agent-x", body)
			if err != nil {
				t.Fatalf("RewriteModelForDowngrade() error = %v", err)
			}
			if downgraded != tt.wantDowngrade {
				t.Fatalf("downgraded = %v, want %v", downgraded, tt.wantDowngrade)
			}
			if tt.wantDowngrade {
				if originalModel != tt.model {
					t.Fatalf("originalModel = %q, want %q", originalModel, tt.model)
				}
				if newModel != tt.wantNext {
					t.Fatalf("newModel = %q, want %q", newModel, tt.wantNext)
				}
			}
		})
	}
}

func TestRewriteModelForDowngrade_ExplicitChainOverridesBuiltin(t *testing.T) {
	t.Parallel()

	cfg := baseGateConfig()
	cfg.DefaultDowngradeChain = []string{"gpt-4o", "gpt-4o-mini"} // explicit 2-model chain
	manager := NewManagerWithClock(cfg, nil, newMockClock(time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC)))

	// gpt-4o should downgrade to gpt-4o-mini (explicit chain), not gpt-4.1 (builtin chain)
	_, original, next, downgraded, err := manager.RewriteModelForDowngrade("agent-x", []byte(`{"model":"gpt-4o"}`))
	if err != nil {
		t.Fatalf("RewriteModelForDowngrade() error = %v", err)
	}
	if !downgraded {
		t.Fatal("downgraded = false, want true")
	}
	if original != "gpt-4o" || next != "gpt-4o-mini" {
		t.Fatalf("got (%q -> %q), want (gpt-4o -> gpt-4o-mini)", original, next)
	}
}

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
			name:  "OpenAI gpt-4o selects OpenAI chain",
			model: "gpt-4o",
			want:  builtinOpenAIChain,
		},
		{
			name:  "OpenAI gpt-4.1-mini selects OpenAI chain",
			model: "gpt-4.1-mini",
			want:  builtinOpenAIChain,
		},
		{
			name:  "Anthropic claude-opus-4-6 selects Anthropic chain",
			model: "claude-opus-4-6",
			want:  builtinAnthropicChain,
		},
		{
			name:  "Anthropic claude-haiku-4-5 selects Anthropic chain",
			model: "claude-haiku-4-5",
			want:  builtinAnthropicChain,
		},
		{
			name:  "Google gemini-2.5-pro selects Google chain",
			model: "gemini-2.5-pro",
			want:  builtinGoogleChain,
		},
		{
			name:  "Google gemini-2.5-flash selects Google chain",
			model: "gemini-2.5-flash",
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
			model: "GPT-4o",
			want:  builtinOpenAIChain,
		},
		{
			name:  "Mixed case Anthropic model matches",
			model: "Claude-Sonnet-4-6",
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
			name:          "OpenAI gpt-4o downgrades via builtin chain",
			model:         "gpt-4o",
			wantNext:      "gpt-4.1",
			wantDowngrade: true,
		},
		{
			name:          "OpenAI gpt-4.1-mini is last in chain, no downgrade",
			model:         "gpt-4.1-mini",
			wantDowngrade: false,
		},
		{
			name:          "Anthropic claude-opus-4-6 downgrades via builtin chain",
			model:         "claude-opus-4-6",
			wantNext:      "claude-sonnet-4-6",
			wantDowngrade: true,
		},
		{
			name:          "Anthropic claude-haiku-4-5 is last in chain, no downgrade",
			model:         "claude-haiku-4-5",
			wantDowngrade: false,
		},
		{
			name:          "Google gemini-2.5-pro downgrades via builtin chain",
			model:         "gemini-2.5-pro",
			wantNext:      "gemini-2.5-flash",
			wantDowngrade: true,
		},
		{
			name:          "Google gemini-2.5-flash is last in chain, no downgrade",
			model:         "gemini-2.5-flash",
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

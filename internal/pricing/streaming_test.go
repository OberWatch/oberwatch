package pricing

import (
	"strings"
	"testing"
)

func TestSSEAccumulator_WithUsageField(t *testing.T) {
	t.Parallel()

	acc := NewSSEAccumulator()
	// Chunk with delta content followed by final chunk with usage.
	acc.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n"))
	acc.Write([]byte("data: {\"usage\":{\"prompt_tokens\":30,\"completion_tokens\":15}}\n\n"))
	acc.Write([]byte("data: [DONE]\n\n"))

	logger, _ := newTestLogger()
	got := acc.Usage("openai", logger)

	if got.Estimated {
		t.Fatal("Estimated = true, want false (usage field present)")
	}
	if got.InputTokens != 30 {
		t.Fatalf("InputTokens = %d, want 30", got.InputTokens)
	}
	if got.OutputTokens != 15 {
		t.Fatalf("OutputTokens = %d, want 15", got.OutputTokens)
	}
}

func TestSSEAccumulator_DeltaContentFallback(t *testing.T) {
	t.Parallel()

	acc := NewSSEAccumulator()
	// Two chunks with content, no usage field.
	acc.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello \"}}]}\n\n"))
	acc.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"world\"}}]}\n\n"))
	acc.Write([]byte("data: [DONE]\n\n"))

	logger, logs := newTestLogger()
	got := acc.Usage("openai", logger)

	if !got.Estimated {
		t.Fatal("Estimated = false, want true (no usage field, delta content only)")
	}
	// "Hello " (6) + "world" (5) = 11 bytes → max(1, 11/4) = 2 output tokens
	if got.OutputTokens < 1 {
		t.Fatalf("OutputTokens = %d, want >= 1", got.OutputTokens)
	}
	if got.InputTokens != 0 {
		t.Fatalf("InputTokens = %d, want 0 (unknown for delta-only fallback)", got.InputTokens)
	}
	if !strings.Contains(logs.String(), "usage data missing") {
		t.Fatalf("expected warning log, got %q", logs.String())
	}
}

func TestSSEAccumulator_MalformedChunksPassThrough(t *testing.T) {
	t.Parallel()

	acc := NewSSEAccumulator()
	// Malformed JSON in data line — should not panic or error.
	acc.Write([]byte("data: {not valid json}\n\n"))
	acc.Write([]byte("data: \n\n"))
	acc.Write([]byte(": comment line\n\n"))
	acc.Write([]byte("data: [DONE]\n\n"))

	logger, logs := newTestLogger()
	got := acc.Usage("openai", logger)

	// No usage found, no delta content → minimum fallback.
	if !got.Estimated {
		t.Fatal("Estimated = false, want true (no parseable data)")
	}
	if got.OutputTokens < 1 {
		t.Fatalf("OutputTokens = %d, want >= 1 (minimum fallback)", got.OutputTokens)
	}
	if !strings.Contains(logs.String(), "usage data missing") {
		t.Fatalf("expected warning log, got %q", logs.String())
	}
}

func TestSSEAccumulator_DoneTriggersRecording(t *testing.T) {
	t.Parallel()

	acc := NewSSEAccumulator()
	// Usage arrives before [DONE] — should be captured regardless of [DONE].
	acc.Write([]byte("data: {\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3}}\n\n"))
	acc.Write([]byte("data: [DONE]\n\n"))

	logger, _ := newTestLogger()
	got := acc.Usage("openai", logger)

	if got.Estimated {
		t.Fatal("Estimated = true, want false")
	}
	if got.InputTokens != 5 || got.OutputTokens != 3 {
		t.Fatalf("tokens = (%d, %d), want (5, 3)", got.InputTokens, got.OutputTokens)
	}
}

func TestSSEAccumulator_PartialLineSpansChunks(t *testing.T) {
	t.Parallel()

	acc := NewSSEAccumulator()
	// Split "data: {...}\n" across two Write calls.
	acc.Write([]byte("data: {\"usage\":{\"prompt_tokens\":10,"))
	acc.Write([]byte("\"completion_tokens\":7}}\n\n"))

	logger, _ := newTestLogger()
	got := acc.Usage("openai", logger)

	if got.Estimated {
		t.Fatal("Estimated = true, want false (usage spread across two chunks)")
	}
	if got.InputTokens != 10 || got.OutputTokens != 7 {
		t.Fatalf("tokens = (%d, %d), want (10, 7)", got.InputTokens, got.OutputTokens)
	}
}

func TestExtractDeltaContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		raw    string
		want   string
		wantOK bool
	}{
		{
			name:   "extracts content from delta",
			raw:    `{"choices":[{"delta":{"content":"Hello"}}]}`,
			want:   "Hello",
			wantOK: true,
		},
		{
			name:   "empty content returns false",
			raw:    `{"choices":[{"delta":{"content":""}}]}`,
			wantOK: false,
		},
		{
			name:   "no choices returns false",
			raw:    `{"choices":[]}`,
			wantOK: false,
		},
		{
			name:   "usage-only chunk returns false",
			raw:    `{"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
			wantOK: false,
		},
		{
			name:   "malformed json returns false",
			raw:    `{not json}`,
			wantOK: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := extractDeltaContent([]byte(tt.raw))
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Fatalf("content = %q, want %q", got, tt.want)
			}
		})
	}
}

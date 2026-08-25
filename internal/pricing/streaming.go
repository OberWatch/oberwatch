package pricing

import (
	"encoding/json"
	"log/slog"
	"strings"
)

// SSEAccumulator processes SSE response chunks in-flight for zero-copy token accounting.
// Feed each response chunk through Write as it arrives; call Usage once the stream ends.
//
//nolint:govet // field order kept for readability over alignment.
type SSEAccumulator struct {
	inputTokens     int
	outputTokens    int
	deltaContentLen int             // byte length of accumulated delta.content strings
	partialLine     strings.Builder // incomplete SSE line waiting for \n
}

// NewSSEAccumulator returns a ready-to-use accumulator.
func NewSSEAccumulator() *SSEAccumulator {
	return &SSEAccumulator{}
}

// Write processes a response chunk in-flight. Safe to call multiple times.
// Chunks may contain partial SSE lines; state is carried across calls.
func (a *SSEAccumulator) Write(chunk []byte) {
	for _, b := range chunk {
		if b == '\n' {
			a.processLine(strings.TrimSpace(a.partialLine.String()))
			a.partialLine.Reset()
		} else {
			a.partialLine.WriteByte(b)
		}
	}
}

// Usage returns the final token counts after the stream has been fully consumed.
// Priority: usage field from any chunk > delta content estimation > minimum fallback.
func (a *SSEAccumulator) Usage(provider string, logger *slog.Logger) Usage {
	// Flush any partial line that arrived without a trailing newline.
	if a.partialLine.Len() > 0 {
		a.processLine(strings.TrimSpace(a.partialLine.String()))
		a.partialLine.Reset()
	}
	if a.inputTokens > 0 || a.outputTokens > 0 {
		return Usage{InputTokens: a.inputTokens, OutputTokens: a.outputTokens}
	}
	if a.deltaContentLen > 0 {
		tokens := estimateTokensFromBodyLength(a.deltaContentLen)
		warnMissingUsage(logger, provider, a.deltaContentLen, true)
		return Usage{OutputTokens: tokens, Estimated: true}
	}
	warnMissingUsage(logger, provider, 0, true)
	return Usage{InputTokens: 1, OutputTokens: 1, Estimated: true}
}

func (a *SSEAccumulator) processLine(line string) {
	if !strings.HasPrefix(line, "data:") {
		return
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if payload == "" || payload == "[DONE]" {
		return
	}
	raw := []byte(payload)

	// First try the usage field (final chunk from most providers).
	if input, output, ok := extractUsage(raw); ok {
		if input > 0 {
			a.inputTokens = input
		}
		if output > 0 {
			a.outputTokens = output
		}
		return
	}

	// Fall back to accumulating delta content bytes.
	if content, ok := extractDeltaContent(raw); ok {
		a.deltaContentLen += len(content)
	}
}

// extractDeltaContent pulls the delta.content string from an OpenAI streaming chunk.
func extractDeltaContent(raw []byte) (string, bool) {
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &chunk); err != nil {
		return "", false
	}
	if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
		return chunk.Choices[0].Delta.Content, true
	}
	return "", false
}

package llm

import (
	"context"
	"strings"
	"testing"
)

// TestParseSSEUsage verifies the final SSE chunk's usage field is captured —
// the gateway only sends usage on the last chunk when stream_options.include_usage
// is set, so the parser must take it from there.
func TestParseSSEUsage(t *testing.T) {
	body := `data: {"choices":[{"delta":{"content":"hel"}}]}

data: {"choices":[{"delta":{"content":"lo"}}]}

data: {"choices":[{"delta":{}}],"usage":{"prompt_tokens":42,"completion_tokens":7,"total_tokens":49}}

data: [DONE]
`
	resp, err := parseSSE(context.Background(), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "hello" {
		t.Errorf("content = %q, want hello", resp.Message.Content)
	}
	if resp.Usage.TotalTokens != 49 || resp.Usage.PromptTokens != 42 || resp.Usage.CompletionTokens != 7 {
		t.Errorf("usage = %+v, want 42/7/49", resp.Usage)
	}
}

// TestParseSSEWithoutUsage: gateways that ignore stream_options simply never
// send usage; the parser must not fail or invent numbers.
func TestParseSSEWithoutUsage(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\ndata: [DONE]\n"
	resp, err := parseSSE(context.Background(), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.TotalTokens != 0 {
		t.Errorf("usage should be zero when gateway omits it, got %+v", resp.Usage)
	}
}

// TestParseNonStreamUsage covers the non-streaming path (LLM_STREAM=false),
// where usage is a top-level response field.
func TestParseNonStreamUsage(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`
	resp, err := parseNonStream(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "hi" {
		t.Errorf("content = %q, want hi", resp.Message.Content)
	}
	if resp.Usage.TotalTokens != 12 {
		t.Errorf("usage = %+v, want total 12", resp.Usage)
	}
}

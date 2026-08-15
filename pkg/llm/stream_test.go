package llm

import (
	"context"
	"strings"
	"testing"
)

// TestParseSSEStreamsDeltas proves the onDelta callback delivers each content
// chunk as it arrives — the "thinking out loud" a live terminal renders — and
// that the fully assembled response is still returned.
func TestParseSSEStreamsDeltas(t *testing.T) {
	const sse = "data: {\"choices\":[{\"delta\":{\"content\":\"He\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"llo\"}}]}\n\n" +
		"data: [DONE]\n\n"
	var got []string
	resp, err := parseSSE(context.Background(), strings.NewReader(sse), func(d string) {
		got = append(got, d)
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "Hello" {
		t.Errorf("content = %q, want Hello", resp.Message.Content)
	}
	if len(got) != 2 || got[0] != "He" || got[1] != "llo" {
		t.Errorf("deltas = %v, want [He llo]", got)
	}
}

// TestParseSSEWithoutDeltaCallback keeps the zero-callback path working (all
// existing callers).
func TestParseSSEWithoutDeltaCallback(t *testing.T) {
	const sse = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: [DONE]\n\n"
	resp, err := parseSSE(context.Background(), strings.NewReader(sse))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "hi" {
		t.Errorf("content = %q, want hi", resp.Message.Content)
	}
}

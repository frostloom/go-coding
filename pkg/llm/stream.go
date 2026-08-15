package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// openAIChatChunk is one SSE "data:" line of a streaming response.
type openAIChatChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"` // present on the final chunk when stream_options.include_usage is set
}

// parseSSE consumes a text/event-stream body and accumulates the response.
// It streams token-by-token so the first tokens are available early; the
// caller (agent) can stop reading as soon as it has what it needs,
// avoiding the full generation latency. An optional onDelta callback is
// invoked for each content chunk as it arrives — the "thinking out loud"
// a live terminal renders (see ChatStreamer).
func parseSSE(ctx context.Context, r io.Reader, onDelta ...func(string)) (ChatResponse, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var content strings.Builder
	finish := ""
	var usage Usage

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return ChatResponse{}, err
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk openAIChatChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // ignore keep-alive / comment lines
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				content.WriteString(choice.Delta.Content)
				if len(onDelta) > 0 && onDelta[0] != nil {
					onDelta[0](choice.Delta.Content)
				}
			}
			if choice.FinishReason != "" {
				finish = choice.FinishReason
			}
		}
		if chunk.Usage != nil {
			usage = *chunk.Usage // last usage chunk wins
		}
	}
	if err := scanner.Err(); err != nil {
		return ChatResponse{}, fmt.Errorf("llm stream read: %w", err)
	}
	if finish == "" {
		finish = "stop" // some gateways omit finish_reason
	}
	return ChatResponse{
		Message:      Message{Role: RoleAssistant, Content: content.String()},
		FinishReason: finish,
		Truncated:    finish == "length",
		Usage:        usage,
	}, nil
}

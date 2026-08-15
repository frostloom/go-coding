// Package llm provides a hand-written OpenAI-compatible chat client.
package llm

import (
	"context"
	"encoding/json"
)

// Role is the message role in a chat conversation.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is a single chat message.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall is a function-call request emitted by the assistant.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall names the tool and carries its JSON arguments.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolSchema describes a tool to the model (OpenAI tools schema).
type ToolSchema struct {
	Type     string         `json:"type"`
	Function FunctionSchema `json:"function"`
}

// FunctionSchema is the name/description/parameters of a tool.
type FunctionSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ChatRequest is the request body for /chat/completions.
type ChatRequest struct {
	Model         string         `json:"model,omitempty"`
	Messages      []Message      `json:"messages"`
	Tools         []ToolSchema   `json:"tools,omitempty"`
	MaxTokens     int            `json:"max_tokens,omitempty"`
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *StreamOptions `json:"stream_options,omitempty"`
}

// StreamOptions asks the gateway to include usage in the final SSE chunk.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// Usage reports token consumption for one completion. The agent accumulates
// it across calls so Harness can turn "the LLM did something" into a real
// per-task token/cost number.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatResponse is the normalized result of a chat completion.
type ChatResponse struct {
	Message      Message
	FinishReason string // "stop" | "length" | "tool_calls" | ...
	Truncated    bool   // true when FinishReason == "length" (output cut by max_tokens)
	Usage        Usage  // token consumption for this completion (zero if gateway omits it)
}

// ChatClient is the interface the agent depends on. Live and mock
// implementations both satisfy it, which is what makes Harness's
// deterministic mock mode possible.
type ChatClient interface {
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}

// ChatStreamer is the optional streaming variant of ChatClient. Live clients
// implement it so an interactive terminal can render tokens as they arrive
// ("thinking out loud"); scripted mock clients do not, which is what keeps
// mock mode deterministic. A caller detects it with a type assertion and
// falls back to Chat when it is absent.
type ChatStreamer interface {
	// ChatStream is Chat with the response streamed: every content delta is
	// delivered to onDelta as it arrives, and the fully assembled response is
	// returned as usual (usage included).
	ChatStream(ctx context.Context, req ChatRequest, onDelta func(string)) (ChatResponse, error)
}

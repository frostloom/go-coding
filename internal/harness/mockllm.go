package harness

import (
	"context"
	"fmt"
	"sync"

	"github.com/zlx05/go-coding/pkg/llm"
)

// MockClient is a scripted LLM that returns pre-recorded replies in order.
// It makes the agent loop fully deterministic: no network, no tokens spent,
// CI-safe. It implements the same llm.ChatClient interface as the live
// OpenAI client, so Harness exercises the real loop against a fake brain.
type MockClient struct {
	mu     sync.Mutex
	script []string // replies returned on successive Chat calls
	idx    int
}

// NewMockClient builds a client that replays the given script.
func NewMockClient(script []string) *MockClient {
	return &MockClient{script: script}
}

// Chat returns the next scripted reply.
func (m *MockClient) Chat(_ context.Context, _ llm.ChatRequest) (llm.ChatResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idx >= len(m.script) {
		return llm.ChatResponse{}, fmt.Errorf("mock llm: script exhausted at call %d", m.idx+1)
	}
	reply := m.script[m.idx]
	m.idx++
	return llm.ChatResponse{
		Message:      llm.Message{Role: llm.RoleAssistant, Content: reply},
		FinishReason: "stop",
	}, nil
}

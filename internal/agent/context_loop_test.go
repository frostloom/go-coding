package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zlx05/go-coding/internal/fsutil"
	"github.com/zlx05/go-coding/pkg/llm"
)

// recordingClient wraps a mock ChatClient and captures every request's
// message history so we can assert what the LLM actually saw.
type recordingClient struct {
	llm.ChatClient
	requests []llm.ChatRequest
}

func (rc *recordingClient) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	rc.requests = append(rc.requests, req)
	return rc.ChatClient.Chat(ctx, req)
}

// TestAgentRetainsHistoryAcrossRepairs runs a real agent loop against a
// fixture with a scripted mock that fails the first two repairs. It asserts
// that the second repair round's LLM request actually contained the earlier
// failure output — i.e. the conversation memory is working, not just the
// prompt being rebuilt from scratch each round.
func TestAgentRetainsHistoryAcrossRepairs(t *testing.T) {
	src := filepath.Join("..", "..", "testdata", "cases", "fix_add")
	dir := t.TempDir()
	if err := fsutil.CopyDir(src, dir); err != nil {
		t.Fatal(err)
	}

	mock := newMockChat([]string{
		`{"steps":[{"file":"mathops.go","content":"package fixadd\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a - b\n}\n","desc":"wrong: same as original"}]}`,
		`{"steps":[{"file":"mathops.go","content":"package fixadd\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a * b\n}\n","desc":"wrong again: multiply"}]}`,
		`{"steps":[{"file":"mathops.go","content":"package fixadd\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n","desc":"correct: add"}]}`,
	})
	rc := &recordingClient{ChatClient: mock}
	a := New(rc, Config{RepoPath: dir, Requirement: "Fix the Add function", MaxRounds: 5})
	res := a.Run(context.Background())
	if !res.Passed() {
		t.Fatalf("expected agent to pass after 3 attempts, got state=%s note=%s", res.State, res.Note)
	}
	if len(rc.requests) != 3 {
		t.Fatalf("expected 3 LLM calls (plan + 2 repairs), got %d", len(rc.requests))
	}

	// The third request (second repair) must contain the failure output from
	// the FIRST repair — the whole point of conversation memory.
	third := rc.requests[2]
	var sawFail string
	for _, m := range third.Messages {
		sawFail += m.Content
	}
	if !strings.Contains(sawFail, "FAIL") || !strings.Contains(sawFail, "Add") {
		t.Errorf("third request should contain earlier failure output; got:\n%s", sawFail)
	}
}

// TestAgentFormatRetryOnProsePlan covers the format-retry layer: a reply that
// is correct prose but not JSON at all (observed live on gpt-5.6-terra —
// self_codeindex) is fed back with a "STRICT JSON only" reminder and retried,
// instead of failing the task outright. The script returns prose first, then
// the real JSON plan; the second LLM request must carry the reminder.
func TestAgentFormatRetryOnProsePlan(t *testing.T) {
	src := filepath.Join("..", "..", "testdata", "cases", "fix_add")
	dir := t.TempDir()
	if err := fsutil.CopyDir(src, dir); err != nil {
		t.Fatal(err)
	}

	mock := newMockChat([]string{
		"Implementation plan:\n\n1. **Fix the Add function** so it returns the sum of its arguments.\n2. Validate with the existing test.\n",
		`{"steps":[{"file":"mathops.go","content":"package fixadd\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n","desc":"fix Add to sum"}]}`,
	})
	rc := &recordingClient{ChatClient: mock}
	a := New(rc, Config{RepoPath: dir, Requirement: "Fix the Add function", MaxRounds: 2})
	res := a.Run(context.Background())
	if !res.Passed() {
		t.Fatalf("expected prose->format-retry->JSON to pass, got state=%s note=%s", res.State, res.Note)
	}
	if len(rc.requests) != 2 {
		t.Fatalf("expected 2 LLM calls (prose + JSON retry), got %d", len(rc.requests))
	}
	second := rc.requests[1]
	var saw string
	for _, m := range second.Messages {
		saw += m.Content
	}
	if !strings.Contains(saw, "STRICT JSON") {
		t.Errorf("second request should contain the format reminder; got:\n%s", saw)
	}
}

// newMockChat is a tiny scripted client (inline to avoid importing harness).
func newMockChat(script []string) llm.ChatClient {
	return &scriptedClient{script: script}
}

type scriptedClient struct {
	script []string
	idx    int
}

func (s *scriptedClient) Chat(_ context.Context, _ llm.ChatRequest) (llm.ChatResponse, error) {
	reply := s.script[s.idx%len(s.script)]
	s.idx++
	return llm.ChatResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: reply}, FinishReason: "stop"}, nil
}

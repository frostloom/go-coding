package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/zlx05/go-coding/pkg/llm"
)

// newTestMgr builds a ContextManager without a code index (fast, isolated).
func newTestMgr() *ContextManager {
	cm := NewContextManager(nil)
	return cm
}

func TestMessagesIncludeHistory(t *testing.T) {
	cm := newTestMgr()
	s := &Session{Requirement: "fix the bug"}

	// Simulate: plan round produced an assistant reply, then the test failed
	// and we recorded the outcome.
	cm.RecordPlan(llm.Message{Role: llm.RoleAssistant, Content: `{"steps":[{"file":"a.go","content":"package x"}]}`})
	cm.RecordOutcome("go test failed: expected 5, got 3")

	msgs := cm.Messages(context.Background(), s, llm.Message{Role: llm.RoleUser, Content: "Fix it"})

	// system + requirement + [history: assistant + outcome] + current
	if len(msgs) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(msgs))
	}
	if msgs[0].Role != llm.RoleSystem {
		t.Error("first message should be system")
	}
	if !strings.Contains(msgs[1].Content, "fix the bug") {
		t.Error("requirement should be in second message")
	}
	// The outcome (failure output) must survive into the conversation.
	joined := ""
	for _, m := range msgs {
		joined += m.Content
	}
	if !strings.Contains(joined, "expected 5, got 3") {
		t.Error("failure output should be in the conversation history")
	}
}

func TestHistoryTrimmed(t *testing.T) {
	cm := newTestMgr()
	// historyCap=4 rounds ⇒ max 8 history messages. Record 6 rounds.
	for i := 0; i < 6; i++ {
		cm.RecordPlan(llm.Message{Role: llm.RoleAssistant, Content: "plan"})
		cm.RecordOutcome("outcome")
	}
	s := &Session{Requirement: "req"}
	msgs := cm.Messages(context.Background(), s, llm.Message{Role: llm.RoleUser, Content: "q"})
	if len(msgs) > 4+cm.historyCap*2 {
		t.Errorf("history should be trimmed to cap, got %d messages", len(msgs))
	}
}

func TestRecordOutcomeTruncatesFailure(t *testing.T) {
	cm := NewContextManager(nil)
	cm.failCap = 20
	cm.RecordOutcome(strings.Repeat("x", 100))
	// The recorded message content should be capped.
	last := cm.history[len(cm.history)-1]
	if len(last.Content) > 20+3 { // + "..." suffix
		t.Errorf("outcome should be truncated, len=%d", len(last.Content))
	}
}

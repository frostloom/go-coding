package agent

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/zlx05/go-coding/pkg/llm"
)

// TestContextManagerAutoCompactEvicts verifies the ContextManager budget: with
// a compactor enabled, overflowing history is moved into pendingCompact (to be
// summarized) and the newest message is always kept, instead of being dropped.
func TestContextManagerAutoCompactEvicts(t *testing.T) {
	cm := NewContextManager(nil)
	cm.SetCompactor(50)
	cm.RecordOutcome(strings.Repeat("old failure a", 30))
	cm.RecordOutcome(strings.Repeat("old failure b", 30))
	cm.RecordOutcome(strings.Repeat("latest outcome ", 30))
	if got := cm.PendingCount(); got != 2 {
		t.Fatalf("pendingCompact = %d, want 2 (two oldest evicted)", got)
	}
	if len(cm.history) != 1 {
		t.Fatalf("history len = %d, want 1 (newest kept)", len(cm.history))
	}
	if !strings.Contains(cm.history[0].Content, "latest outcome") {
		t.Fatalf("kept message = %q, want the newest outcome", cm.history[0].Content)
	}
	// Eviction never removes the newest message even when it alone exceeds the
	// budget — the just-recorded outcome is what the next round depends on.
	if totalRunes(cm.history) > 50 && len(cm.history) != 1 {
		t.Fatalf("history after eviction = %d runes over budget with %d messages", totalRunes(cm.history), len(cm.history))
	}
}

// TestContextManagerNoCompactorTruncates verifies the default path is
// unchanged: without a compactor, only the round count cap applies and the
// overflow is dropped, not set aside.
func TestContextManagerNoCompactorTruncates(t *testing.T) {
	cm := NewContextManager(nil) // compactRunes == 0
	for i := 0; i < cm.historyCap*2+2; i++ {
		cm.RecordOutcome("round " + strings.Repeat("x", 20))
	}
	if cm.PendingCount() != 0 {
		t.Fatalf("pendingCompact = %d, want 0 without a compactor", cm.PendingCount())
	}
	if len(cm.history) > cm.historyCap*2 {
		t.Fatalf("history len = %d, want capped at %d", len(cm.history), cm.historyCap*2)
	}
}

// TestAgentAutoCompactSummary verifies the end-to-end compaction: when the
// history overflows, the next planning call spends one LLM call on a summary
// and the gist of earlier attempts survives at the head of the history.
func TestAgentAutoCompactSummary(t *testing.T) {
	dir := t.TempDir()
	writeToolFixture(t, dir)
	mock := newMockChat([]string{
		"SUMMARY: earlier attempts changed Add to a-b; the fix is a+b.", // compaction call
		`{"steps":[{"file":"calc.go","content":` + strconv.Quote(fixedAddContent) + `,"desc":"fix Add"}]}`,
	})
	a := New(mock, Config{RepoPath: dir, Requirement: "Fix the Add function.", MaxRounds: 2})
	a.SetAutoCompact(50)
	// Simulate a long past: three failing outcomes overflow the 50-rune budget.
	a.ctxMgr.RecordOutcome(strings.Repeat("attempt A failed ", 30))
	a.ctxMgr.RecordOutcome(strings.Repeat("attempt B failed ", 30))
	a.ctxMgr.RecordOutcome(strings.Repeat("attempt C failed ", 30))

	res := a.Run(context.Background())
	if !res.Passed() {
		t.Fatalf("run failed after compaction: %s", res.Note)
	}
	if res.LLMCalls != 2 {
		t.Fatalf("llmCalls = %d, want 2 (one summary + one plan)", res.LLMCalls)
	}
	if !strings.Contains(a.ctxMgr.compactedSummary, "SUMMARY") {
		t.Logf("history (%d):", len(a.ctxMgr.history))
		for i, m := range a.ctxMgr.history {
			t.Logf("  [%d] %s: %q", i, m.Role, truncate(m.Content, 80))
		}
		t.Fatalf("compactedSummary = %q, want the auto-compacted summary", a.ctxMgr.compactedSummary)
	}
	// The summary must be injected into the prompt right after the requirement.
	msgs := a.ctxMgr.Messages(context.Background(), a.sess, llm.Message{Role: llm.RoleUser, Content: "Q"})
	if len(msgs) < 3 || !strings.Contains(msgs[2].Content, "auto-compacted summary") {
		t.Fatalf("Messages() did not inject the summary in position; got %d messages", len(msgs))
	}
}

// TestAgentAutoCompactEmptySummaryFallsBack verifies the fallback: when the
// summary call returns empty text, the overflow is dropped (classic truncation)
// instead of blocking the task.
func TestAgentAutoCompactEmptySummaryFallsBack(t *testing.T) {
	dir := t.TempDir()
	writeToolFixture(t, dir)
	mock := newMockChat([]string{
		"   ", // empty summary
		`{"steps":[{"file":"calc.go","content":` + strconv.Quote(fixedAddContent) + `,"desc":"fix Add"}]}`,
	})
	a := New(mock, Config{RepoPath: dir, Requirement: "Fix the Add function.", MaxRounds: 2})
	a.SetAutoCompact(50)
	a.ctxMgr.RecordOutcome(strings.Repeat("attempt A failed ", 30))
	a.ctxMgr.RecordOutcome(strings.Repeat("attempt B failed ", 30))
	a.ctxMgr.RecordOutcome(strings.Repeat("attempt C failed ", 30))

	res := a.Run(context.Background())
	if !res.Passed() {
		t.Fatalf("run failed with an empty summary: %s", res.Note)
	}
	// An empty summary must not be persisted as the compacted memory.
	if a.ctxMgr.compactedSummary != "" {
		t.Fatalf("compactedSummary = %q, want empty after an empty summary", a.ctxMgr.compactedSummary)
	}
	if res.LLMCalls != 2 {
		t.Fatalf("llmCalls = %d, want 2 (failed summary still counts, plan follows)", res.LLMCalls)
	}
}

// TestAgentAutoCompactSummaryErrorFallsBack verifies the fallback when the
// summary LLM call itself errors: the overflow is dropped, the task continues.
func TestAgentAutoCompactSummaryErrorFallsBack(t *testing.T) {
	dir := t.TempDir()
	writeToolFixture(t, dir)
	a := New(failChat{}, Config{RepoPath: dir, Requirement: "Fix the Add function.", MaxRounds: 2})
	a.SetAutoCompact(50)
	a.ctxMgr.RecordOutcome(strings.Repeat("attempt A failed ", 30))
	a.ctxMgr.RecordOutcome(strings.Repeat("attempt B failed ", 30))

	a.maybeCompact(context.Background())
	if a.ctxMgr.PendingCount() != 0 {
		t.Fatalf("pendingCompact = %d, want drained on summary error", a.ctxMgr.PendingCount())
	}
	// The newest message survives the fallback.
	if len(a.ctxMgr.history) != 1 {
		t.Fatalf("history len = %d, want 1", len(a.ctxMgr.history))
	}
}

// failChat always errors — drives the compaction failure path.
type failChat struct{}

func (failChat) Chat(context.Context, llm.ChatRequest) (llm.ChatResponse, error) {
	return llm.ChatResponse{}, errors.New("boom")
}

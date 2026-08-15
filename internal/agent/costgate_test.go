package agent

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/zlx05/go-coding/internal/fsutil"
	"github.com/zlx05/go-coding/pkg/llm"
)

const wrongPlan = `{"steps":[{"file":"mathops.go","content":"package fixadd\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a - b\n}\n","desc":"wrong: subtract"}]}`
const rightPlan = `{"steps":[{"file":"mathops.go","content":"package fixadd\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n","desc":"correct: add"}]}`

func costGateFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := fsutil.CopyDir(filepath.Join("..", "..", "testdata", "cases", "fix_add"), dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestHookBeforeChatCostGateStopsRun verifies the cost-gate flow: a
// before_chat hook refuses the SECOND LLM call once the accumulated session
// cost passes the budget. The run stops immediately — no REPAIRING (repairing
// would spend more tokens) — and the task fails with the gate's reason.
func TestHookBeforeChatCostGateStopsRun(t *testing.T) {
	dir := costGateFixture(t)

	mock := newMockChat([]string{wrongPlan, rightPlan})
	per := llm.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}
	uc := &usageClient{ChatClient: mock, perCall: per}
	a := New(uc, Config{
		RepoPath:    dir,
		Requirement: "Fix the Add function",
		MaxRounds:   3,
		Hooks: map[HookPoint][]Hook{
			HookBeforeChat: {func(_ context.Context, ev Event) error {
				if ev.Tokens >= 100 {
					return errTestHook("cost budget exceeded: at " + strconv.Itoa(ev.Tokens))
				}
				return nil
			}},
		},
	})
	res := a.Run(context.Background())
	if res.Passed() {
		t.Fatalf("expected the run to be stopped by the cost gate, got state=%s note=%s", res.State, res.Note)
	}
	if !strings.Contains(res.Note, "cost gate") {
		t.Errorf("note should carry the cost-gate reason, got: %s", res.Note)
	}
	// The veto happened before the second call: only one LLM call was made.
	if res.LLMCalls != 1 {
		t.Errorf("expected 1 LLM call before the gate stopped the run, got %d", res.LLMCalls)
	}
	// The gate saw the accumulated tokens from call 1 when it vetoed.
	if !strings.Contains(res.Note, "at 150") {
		t.Errorf("veto reason should report 150 accumulated tokens, got: %s", res.Note)
	}
}

// TestHookBeforeChatAllowsUnderBudget: a budget above the run's cost must not
// interfere — the same hook shape with a higher threshold lets the task pass.
func TestHookBeforeChatAllowsUnderBudget(t *testing.T) {
	dir := costGateFixture(t)

	mock := newMockChat([]string{wrongPlan, rightPlan})
	per := llm.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}
	uc := &usageClient{ChatClient: mock, perCall: per}
	a := New(uc, Config{
		RepoPath:    dir,
		Requirement: "Fix the Add function",
		MaxRounds:   3,
		Hooks: map[HookPoint][]Hook{
			HookBeforeChat: {func(_ context.Context, ev Event) error {
				if ev.Tokens >= 1000 {
					return errTestHook("over budget")
				}
				return nil
			}},
		},
	})
	res := a.Run(context.Background())
	if !res.Passed() {
		t.Fatalf("expected the run to pass under budget, got state=%s note=%s", res.State, res.Note)
	}
	if res.LLMCalls != 2 {
		t.Errorf("expected 2 LLM calls (wrong plan + repair), got %d", res.LLMCalls)
	}
}

// TestEventTokensVisibleToHooks verifies every hook event carries the running
// token total (the enabler of any cost gate): by the time before_test fires,
// two Chat calls have accumulated 300 tokens and the hook sees exactly that.
func TestEventTokensVisibleToHooks(t *testing.T) {
	dir := costGateFixture(t)

	mock := newMockChat([]string{wrongPlan, rightPlan})
	per := llm.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}
	uc := &usageClient{ChatClient: mock, perCall: per}
	var seen int
	a := New(uc, Config{
		RepoPath:    dir,
		Requirement: "Fix the Add function",
		MaxRounds:   3,
		Hooks: map[HookPoint][]Hook{
			HookBeforeTest: {func(_ context.Context, ev Event) error {
				seen = ev.Tokens
				return nil
			}},
		},
	})
	res := a.Run(context.Background())
	if !res.Passed() {
		t.Fatalf("run should pass, got state=%s note=%s", res.State, res.Note)
	}
	if seen != 300 {
		t.Errorf("before_test saw tokens=%d, want 300 (2 calls x 150 accumulated)", seen)
	}
}

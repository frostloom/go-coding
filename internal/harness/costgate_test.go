package harness

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zlx05/go-coding/pkg/llm"
)

// wrongAdd is the executor worker's first scripted plan in the budget tests:
// the worker "fixes" Add to subtract, the visible test fails, and the worker's
// repair round would call the LLM a second time — which is exactly when the
// per-type cost gate must fire (P22 executor mode: each role's session is
// gated, and worker hooks are threaded through newChildAgent).
const wrongAdd = `package fixadd

// Add returns the sum of a and b.
func Add(a, b int) int {
	return a - b
}
`

// usageMock wraps a scripted MockClient and stamps every reply with a fixed
// per-call usage — the harness counterpart of the agent package's usageClient.
// The stock MockClient reports zero usage (mock "LLMs" spend nothing), so a
// cost gate can never fire against it; this wrapper is how the gate is proven
// to fire without paying a real LLM.
type usageMock struct {
	*MockClient
	perCall llm.Usage
}

func (u *usageMock) Chat(ctx context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	res, err := u.MockClient.Chat(ctx, req)
	res.Usage = u.perCall
	return res, err
}

// bugfixCase is a minimal bugfix-class case exercising the executor repair
// path end to end (P22 default): the parent outlines the step (call 1), the
// worker writes the WRONG file first (call 2), fails its test, and repairs
// with the right content (call 3). A cost-gate test can then fire on the
// worker's repair call or let all three calls through.
func bugfixCase() Case {
	return Case{
		Name:        "fix_add",
		Type:        TypeBugfix,
		FixtureDir:  filepath.Join("..", "..", "testdata", "cases", "fix_add"),
		Requirement: "Fix the Add function",
		MockScript: []string{
			OutlineStep("mathops.go", "fix Add to sum", "Add must return the sum: the visible Add test must pass"),
			PlanStep("mathops.go", wrongAdd, "wrong: subtract"),
			PlanStep("mathops.go", addFix, "fix Add to sum"),
		},
		WantPass: true,
	}
}

// TestCostGatePerTypeStopsCase proves the per-type budget actually vetoes in
// executor mode: a bugfix case with a 100-token budget spends 150 on the
// parent's outline plan and 150 on the worker's wrong plan, so the worker's
// repair call — and then the parent's repair call — are refused BEFORE they
// spend. The case stops at 2 completed LLM calls with the gate's reason, no
// further spending.
func TestCostGatePerTypeStopsCase(t *testing.T) {
	h := New()
	h.MaxRounds = 3
	h.MaxTokens = map[CaseType]int{TypeBugfix: 100}

	per := llm.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}
	mc := &usageMock{NewMockClient(bugfixCase().MockScript), per}

	res := h.runOne(context.Background(), bugfixCase(), mc, nil)
	if res.Passed {
		t.Fatalf("expected the cost gate to stop the case, got passed=%v state=%s note=%s", res.Passed, res.AgentState, res.Note)
	}
	if !strings.Contains(res.Note, "cost budget exceeded for bugfix") {
		t.Errorf("note should carry the per-type gate reason, got: %s", res.Note)
	}
	if res.LLMCalls != 2 {
		t.Errorf("gate should veto the worker's repair call: got %d calls", res.LLMCalls)
	}
	if res.Tokens != 300 {
		t.Errorf("case should report the 300 tokens spent before the gate, got %d", res.Tokens)
	}
}

// TestCostGateAllowsUnderBudget: the same shape with a budget above the run's
// cost must not interfere — all three calls run (outline + wrong worker plan +
// worker repair), the repair lands, and the case passes.
func TestCostGateAllowsUnderBudget(t *testing.T) {
	h := New()
	h.MaxRounds = 3
	h.MaxTokens = map[CaseType]int{TypeBugfix: 1000}

	per := llm.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}
	mc := &usageMock{NewMockClient(bugfixCase().MockScript), per}

	res := h.runOne(context.Background(), bugfixCase(), mc, nil)
	if !res.Passed {
		t.Fatalf("expected the run to pass under budget, got state=%s note=%s", res.AgentState, res.Note)
	}
	if res.LLMCalls != 3 {
		t.Errorf("expected 3 LLM calls (outline + wrong plan + repair), got %d", res.LLMCalls)
	}
}

// TestCostGateIsPerType: the budget lives on the case's type, not globally. An
// impl-only budget must leave a bugfix case untouched even when it burns more
// than the impl ceiling — the gate is keyed by case complexity.
func TestCostGateIsPerType(t *testing.T) {
	h := New()
	h.MaxRounds = 3
	h.MaxTokens = map[CaseType]int{TypeImpl: 100} // bugfix has no budget

	per := llm.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}
	mc := &usageMock{NewMockClient(bugfixCase().MockScript), per}

	res := h.runOne(context.Background(), bugfixCase(), mc, nil)
	if !res.Passed {
		t.Fatalf("a bugfix case must not inherit the impl budget, got state=%s note=%s", res.AgentState, res.Note)
	}
	if res.LLMCalls != 3 {
		t.Errorf("expected the full 3-call run, got %d", res.LLMCalls)
	}
}

// TestCostGateInertWithoutUsage: budgets set, but the stock MockClient reports
// zero usage (the real mock gate condition). ev.Tokens is always 0, so the
// gate provably never fires — this is what keeps the 12/12 mock baseline
// byte-identical no matter which budgets the CLI passes.
func TestCostGateInertWithoutUsage(t *testing.T) {
	h := New()
	h.MaxRounds = 3
	h.MaxTokens = map[CaseType]int{TypeBugfix: 1} // a 1-token budget is still inert

	mc := NewMockClient(bugfixCase().MockScript) // no usage stamp

	res := h.runOne(context.Background(), bugfixCase(), mc, nil)
	if !res.Passed {
		t.Fatalf("the gate must be inert when the client reports no usage, got state=%s note=%s", res.AgentState, res.Note)
	}
	if res.Tokens != 0 {
		t.Errorf("no-usage client should report 0 tokens, got %d", res.Tokens)
	}
}

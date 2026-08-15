// Package go_coding_test hosts the Agent Harness entry point: running
// `go test` here exercises the real agent loop deterministically via a
// scripted mock LLM — no network, no tokens, CI-safe.
package go_coding_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/zlx05/go-coding/internal/harness"
	"github.com/zlx05/go-coding/pkg/llm"
)

// TestHarnessMockSuite runs the full M5 evaluation set in deterministic mock
// mode. Every case must pass; the summary is the CI-gated capability metric.
func TestHarnessMockSuite(t *testing.T) {
	h := harness.New()
	for _, c := range harness.Cases() {
		h.AddCase(c)
	}

	results := h.RunMock(t, context.Background())
	summary := h.Summarize(results)
	t.Logf("summary: %s", summary.String())

	want := len(harness.Cases())
	if summary.Passed != summary.Total || summary.Total != want {
		t.Errorf("expected all %d cases to pass, summary: %s", want, summary.String())
	}
	// Happy-path runs: planning is round 1, tests pass on the first edit.
	if summary.AvgRounds != 1 {
		t.Errorf("expected avgRounds=1 (correct answer first try), got %.1f", summary.AvgRounds)
	}
}

// TestHarnessMockFixAdd drives the repair cycle against the fix_add fixture
// through the P22 executor path. The mock script makes the WORKER's first edit
// wrong (multiply), forcing the worker's own test loop to repair (add), so the
// test verifies the whole state machine — not just the happy path. Because the
// repair happens inside the executor worker, the parent finishes in one round
// and the repair is observable as the extra worker LLM call.
func TestHarnessMockFixAdd(t *testing.T) {
	h := harness.New()
	h.AddCase(harness.Case{
		Name:        "fix_add",
		FixtureDir:  filepath.Join("testdata", "cases", "fix_add"),
		Requirement: "The Add function is broken. Fix it so it returns the correct sum of its two integer arguments.",
		MockScript: []string{
			harness.OutlineStep("mathops.go", "fix Add to sum", "Add must return the sum"),
			harness.PlanStep("mathops.go", `package fixadd

// Add returns the sum of a and b.
func Add(a, b int) int {
	return a * b
}
`, "attempt 1: use multiply"),
			harness.PlanStep("mathops.go", `package fixadd

// Add returns the sum of a and b.
func Add(a, b int) int {
	return a + b
}
`, "fix: use add"),
		},
		WantPass: true,
	})

	results := h.RunMock(t, context.Background())
	summary := h.Summarize(results)
	t.Logf("summary: %s", summary.String())

	if len(results) != 1 {
		t.Fatalf("expected 1 case result, got %d", len(results))
	}
	// The parent (planner) never needs to repair: its worker repairs internally,
	// so the parent finishes in round 1 and the repair shows up as the extra LLM
	// call (parent outline + worker wrong plan + worker repair = 3).
	if results[0].Rounds != 1 {
		t.Errorf("expected the parent to finish in 1 round (the executor repairs internally), got %d", results[0].Rounds)
	}
	if results[0].LLMCalls != 3 {
		t.Errorf("expected 3 LLM calls (parent outline + worker wrong plan + worker repair), got %d", results[0].LLMCalls)
	}
	if results[0].ToolCalls == 0 {
		t.Errorf("expected tool calls to be recorded, got 0")
	}
	if summary.Passed != 1 {
		t.Fatalf("expected the single case to pass, summary: %s", summary.String())
	}
}

// TestHarnessMockPEMode runs the full case set in P6 evaluator-optimizer
// mode. Each case's default EvalScript approves first try, so the
// deterministic baseline holds: 100%, one producer round each, plus a PASS
// evaluator call. This is the architecture-comparison gate (single vs pe).
func TestHarnessMockPEMode(t *testing.T) {
	h := harness.New()
	h.UsePE = true
	for _, c := range harness.Cases() {
		h.AddCase(c)
	}

	results := h.RunMock(t, context.Background())
	summary := h.Summarize(results)
	t.Logf("summary (pe): %s", summary.String())

	want := len(harness.Cases())
	if summary.Passed != summary.Total || summary.Total != want {
		t.Errorf("expected all %d cases to pass in PE mode, summary: %s", want, summary.String())
	}
	if summary.AvgRounds != 1 {
		t.Errorf("expected avgRounds=1 in PE mode (producer correct + evaluator approves), got %.1f", summary.AvgRounds)
	}
}

// TestHarnessPE_EvalRejectLoop drives the official evaluator-optimizer loop
// end to end at the harness level, in PLAN-level terms: the evaluator rejects
// the first outline plan (NEEDS_IMPROVEMENT), the planner revises, and the
// second review approves — all inside obtainPlan, before any edit lands. One
// test round total; the rejected plan cost an extra producer+review pair, not
// a wasted edit+test round. In P22 executor mode the approved outline is then
// dispatched to a worker, so a third script entry carries the worker's content
// plan.
func TestHarnessPE_EvalRejectLoop(t *testing.T) {
	h := harness.New()
	h.UsePE = true
	h.AddCase(harness.Case{
		Name:        "pe_reject_loop",
		FixtureDir:  filepath.Join("testdata", "cases", "fix_add"),
		Requirement: "The Add function is broken. Fix it so it returns the correct sum of its two integer arguments.",
		MockScript: []string{
			harness.OutlineStep("mathops.go", "fix Add to sum", "Add must return the sum"),
			harness.OutlineStep("mathops.go", "revised plan after evaluator feedback", "Add must return the sum"),
			harness.PlanStep("mathops.go", `package fixadd

// Add returns the sum of a and b.
func Add(a, b int) int {
	return a + b
}
`, "executor: implement Add to sum"),
		},
		EvalScript: []string{
			`{"evaluation":"NEEDS_IMPROVEMENT","feedback":"document the return semantics","findings":["missing doc"]}`,
			`{"evaluation":"PASS","feedback":"requirement met","findings":[]}`,
		},
		WantPass: true,
	})

	results := h.RunMock(t, context.Background())
	summary := h.Summarize(results)
	t.Logf("summary: %s", summary.String())

	if len(results) != 1 {
		t.Fatalf("expected 1 case result, got %d", len(results))
	}
	if !results[0].Passed {
		t.Fatalf("expected pe_reject_loop to pass after plan rewrite, got state=%s note=%s", results[0].AgentState, results[0].Note)
	}
	if results[0].Rounds != 1 {
		t.Errorf("expected 1 round (plan rewrite costs no test round), got %d", results[0].Rounds)
	}
	// 2 producer plans + 2 reviews (all in the parent's obtainPlan) + 1 worker
	// content plan = 5; the executor's internal calls merge into the parent.
	if results[0].LLMCalls != 5 {
		t.Errorf("expected 5 LLM calls (2 producer plans + 2 reviews + 1 executor), got %d", results[0].LLMCalls)
	}
}

// TestHarnessLive runs the full case set against a real LLM and writes the
// report to harness-out/live.json. Skipped unless OPENAI_API_KEY is set —
// this is the live baseline, not a CI gate.
func TestHarnessLive(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("live mode: OPENAI_API_KEY not set (run: export OPENAI_API_KEY=... && go test -run TestHarnessLive . -v)")
	}
	cfg := llm.ConfigFromEnv()
	lc, err := llm.NewOpenAI(cfg)
	if err != nil {
		t.Fatalf("llm client: %v", err)
	}

	h := harness.New()
	for _, c := range harness.Cases() {
		h.AddCase(c)
	}

	results := h.RunLive(t, context.Background(), lc)
	summary := h.Summarize(results)
	t.Logf("summary: %s", summary.String())

	if err := os.MkdirAll("harness-out", 0o755); err != nil {
		t.Fatalf("mkdir harness-out: %v", err)
	}
	if err := harness.WriteReport(filepath.Join("harness-out", "live.json"), "live", results, summary); err != nil {
		t.Fatalf("write live report: %v", err)
	}
	t.Logf("live report -> harness-out/live.json")
}

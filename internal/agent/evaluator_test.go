package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseVerdict verifies the evaluator's structured reply tolerates the
// same model deviations as plan parsing: code fences, preamble text, JSON
// typos from small models.
func TestParseVerdict(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantEval string
		wantErr  bool
	}{
		{"plain", `{"evaluation":"PASS","feedback":"ok","findings":[]}`, "PASS", false},
		{"code fence", "```json\n{\"evaluation\":\"NEEDS_IMPROVEMENT\",\"feedback\":\"x\",\"findings\":[]}\n```", "NEEDS_IMPROVEMENT", false},
		{"preamble + trailing", "Here is my verdict:\n{\"evaluation\":\"FAIL\",\"feedback\":\"bad\",\"findings\":[\"a\"]} hope that helps", "FAIL", false},
		{"trailing comma", `{"evaluation":"PASS","feedback":"ok","findings":[],}`, "PASS", false},
		{"lowercase", `{"evaluation":"pass","feedback":"ok","findings":[]}`, "PASS", false},
		{"missing field", `{"feedback":"no eval"}`, "", true},
		{"no json", `hello world`, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, err := parseVerdict(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", v)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if v.Evaluation != tc.wantEval {
				t.Errorf("evaluation = %q, want %q", v.Evaluation, tc.wantEval)
			}
		})
	}
}

// TestCheckSymbolRegression verifies the objective gate: a removed symbol or
// a renamed package is reported, while a newly added symbol is fine.
func TestCheckSymbolRegression(t *testing.T) {
	base := &SymbolInventory{Symbols: []SymbolInfo{
		{File: "calc.go", Pkg: "demo", Name: "Add"},
		{File: "calc.go", Pkg: "demo", Name: "Mul"},
		{File: "use.go", Pkg: "demo", Name: "useAdd"},
	}}
	// Producer deleted Mul and renamed the package in use.go — the exact
	// "tests happened to pass but the structure is broken" failure mode.
	cur := &SymbolInventory{Symbols: []SymbolInfo{
		{File: "calc.go", Pkg: "demo", Name: "Add"},
		{File: "use.go", Pkg: "demo2", Name: "useAdd"},
		{File: "calc.go", Pkg: "demo", Name: "NewHelper"}, // additions are fine
	}}
	problems := checkSymbolRegression(base, cur)
	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, "Mul") {
		t.Errorf("expected deleted-symbol problem, got:\n%s", joined)
	}
	if !strings.Contains(joined, "demo2") {
		t.Errorf("expected package-change problem, got:\n%s", joined)
	}
	if strings.Contains(joined, "NewHelper") {
		t.Errorf("new symbols must not be reported as regressions:\n%s", joined)
	}
}

// TestEvaluatorApprovesOnPass runs the full loop in P6 mode where the
// producer fixes the bug and the evaluator approves first try: one round, done.
func TestEvaluatorApprovesOnPass(t *testing.T) {
	dir := writeEvalFixture(t)
	fix := `{"steps":[{"file":"mathops.go","content":"package demo\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n\n// Mul returns the product of a and b.\nfunc Mul(a, b int) int {\n\treturn a * b\n}\n","desc":"fix Add"}]}`
	prod := newMockChat([]string{fix})
	prodRec := &recordingClient{ChatClient: prod}
	evl := newMockChat([]string{`{"evaluation":"PASS","feedback":"requirement met","findings":[]}`})
	evlRec := &recordingClient{ChatClient: evl}

	a := New(prodRec, Config{
		RepoPath:    dir,
		Requirement: "The Add function is broken. Fix it so it returns the correct sum.",
		MaxRounds:   3,
		Evaluator:   evlRec,
	})
	res := a.Run(context.Background())
	if !res.Passed() {
		t.Fatalf("expected done, got state=%s note=%s", res.State, res.Note)
	}
	if res.Rounds != 1 {
		t.Errorf("expected 1 round, got %d", res.Rounds)
	}
	if res.LLMCalls != 2 {
		t.Errorf("expected 2 LLM calls (1 plan + 1 review), got %d", res.LLMCalls)
	}
	if len(evlRec.requests) != 1 {
		t.Fatalf("expected 1 evaluator request, got %d", len(evlRec.requests))
	}
	var joined string
	for _, m := range evlRec.requests[0].Messages {
		joined += m.Content
	}
	if !strings.Contains(joined, "Requirement") {
		t.Errorf("evaluator should receive the requirement, got:\n%s", joined)
	}
}

// TestEvaluatorRejectDrivesRepair verifies the official feedback loop in
// PLAN-level terms: the evaluator rejects the first plan (NEEDS_IMPROVEMENT),
// the feedback reaches the producer's next request, the revised plan is
// approved, and only then does anything touch disk.
func TestEvaluatorRejectDrivesRepair(t *testing.T) {
	dir := writeEvalFixture(t)
	fix := `{"steps":[{"file":"mathops.go","content":"package demo\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n\n// Mul returns the product of a and b.\nfunc Mul(a, b int) int {\n\treturn a * b\n}\n","desc":"fix Add"}]}`
	// Two producer plans: the first is rejected by the evaluator, the second
	// (revised) is approved. Both happen inside obtainPlan — no test round is
	// spent on the rejected plan.
	prod := newMockChat([]string{fix, fix})
	prodRec := &recordingClient{ChatClient: prod}
	evl := newMockChat([]string{
		`{"evaluation":"NEEDS_IMPROVEMENT","feedback":"handle negative numbers too","findings":["missing negative handling"]}`,
		`{"evaluation":"PASS","feedback":"requirement met","findings":[]}`,
	})

	a := New(prodRec, Config{
		RepoPath:      dir,
		Requirement:   "The Add function is broken. Fix it so it returns the correct sum.",
		MaxRounds:     3,
		Evaluator:     evl,
		MaxEvalRounds: 2,
	})
	res := a.Run(context.Background())
	if !res.Passed() {
		t.Fatalf("expected done after plan rewrite, got state=%s note=%s", res.State, res.Note)
	}
	// The rejected plan never reached editing: one round (one edit+test cycle).
	if res.Rounds != 1 {
		t.Errorf("expected 1 round (plan rewrite costs no test round), got %d", res.Rounds)
	}
	// The producer's second request must carry the evaluator's feedback —
	// proof the review loop fed back instead of just failing.
	if len(prodRec.requests) < 2 {
		t.Fatalf("expected >= 2 producer requests, got %d", len(prodRec.requests))
	}
	var joined string
	for _, m := range prodRec.requests[1].Messages {
		joined += m.Content
	}
	if !strings.Contains(joined, "negative") {
		t.Errorf("repair request should carry evaluator feedback, got:\n%s", joined)
	}
}

// TestMaxEvalRoundsBounds verifies the stop criterion: a strict evaluator
// that keeps rejecting cannot loop the plan rewrite forever. Past
// maxEvalRounds the latest plan is accepted ("know when good enough is good
// enough") and the edit+test round proceeds.
func TestMaxEvalRoundsBounds(t *testing.T) {
	dir := writeEvalFixture(t)
	fix := `{"steps":[{"file":"mathops.go","content":"package demo\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n\n// Mul returns the product of a and b.\nfunc Mul(a, b int) int {\n\treturn a * b\n}\n","desc":"fix Add"}]}`
	prod := newMockChat([]string{fix})
	// Evaluator rejects twice; MaxEvalRounds=1 means the second rejection is
	// overridden and the plan proceeds.
	evl := newMockChat([]string{
		`{"evaluation":"NEEDS_IMPROVEMENT","feedback":"do better","findings":["nitpick"]}`,
		`{"evaluation":"NEEDS_IMPROVEMENT","feedback":"do better again","findings":["more nitpicks"]}`,
	})

	a := New(prod, Config{
		RepoPath:      dir,
		Requirement:   "The Add function is broken. Fix it so it returns the correct sum.",
		MaxRounds:     3,
		Evaluator:     evl,
		MaxEvalRounds: 1,
	})
	res := a.Run(context.Background())
	if !res.Passed() {
		t.Fatalf("expected done (override), got state=%s note=%s", res.State, res.Note)
	}
	if res.Rounds != 1 {
		t.Errorf("expected 1 round (plan rewrites cost no test round), got %d", res.Rounds)
	}
	// 2 producer plans + 2 evaluator reviews, all inside obtainPlan.
	if res.LLMCalls != 4 {
		t.Errorf("expected 4 LLM calls (2 plans + 2 reviews), got %d", res.LLMCalls)
	}
}

// TestEvaluatorSymbolGateRejectsStructuralBreak runs the whole loop against
// a producer that deletes a symbol AND makes the tests pass (the local-7B
// hallucination pattern). The objective symbol gate must reject it even
// though the evaluator LLM would never be asked (zero tokens spent).
func TestEvaluatorSymbolGateRejectsStructuralBreak(t *testing.T) {
	dir := writeEvalFixture(t)
	// This "fix" removes the Mul function (used by the test? no — only Add is
	// tested) but keeps Add correct, so tests pass while a symbol is gone.
	removed := `{"steps":[{"file":"mathops.go","content":"package demo\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n","desc":"fix Add but drop Mul"}]}`
	prod := newMockChat([]string{removed})
	// Evaluator script would approve — but it must never be reached: the
	// symbol gate rejects first, so the producer repairs. Exhausting the eval
	// script proves the gate short-circuited before the LLM review.
	evl := newMockChat([]string{`{"evaluation":"PASS","feedback":"unreachable","findings":[]}`})

	a := New(prod, Config{
		RepoPath:      dir,
		Requirement:   "The Add function is broken. Fix it so it returns the correct sum.",
		MaxRounds:     2,
		Evaluator:     evl,
		MaxEvalRounds: 1,
	})
	res := a.Run(context.Background())
	// The gate rejects, repair runs out of rounds → the task fails (its tests
	// pass but the structure is broken and never restored). This is the
	// desired behavior: silently passing structural damage is the bug.
	if res.Passed() {
		t.Fatalf("expected structural break to be caught, got passed with note=%s", res.Note)
	}
	// Round 1 edits (gate rejects → repair), round 2 re-edits (gate rejects
	// again → repair), round 3 exceeds MaxRounds=2 → failed.
	if res.Rounds != 3 {
		t.Errorf("expected 3 rounds (edit + 2 gate-driven repairs, exhausted), got %d", res.Rounds)
	}
}

// TestSymbolPlanGateOutlineStepsIsNoOp is the P22 regression: an OUTLINE step
// (empty content — the executor-mode protocol) must NOT be reported as removing
// every baseline symbol. With the executor protocol the gate becomes a no-op
// for outline steps — structural protection for those lands in the executor's
// edit-time call-graph gate — while a content plan that really deletes a
// symbol is still rejected.
func TestSymbolPlanGateOutlineStepsIsNoOp(t *testing.T) {
	dir := writeEvalFixture(t)
	a := New(newMockChat(nil), Config{RepoPath: dir})
	inv, err := symbolInventory(dir)
	if err != nil {
		t.Fatal(err)
	}
	a.sess.SymbolBase = inv

	// Outline step carries no content, so the gate cannot judge structure from
	// it — and must not infer that the whole file was replaced.
	outline := []Step{{File: "mathops.go", Desc: "fix Add", Accept: "Add returns sum"}}
	if problems := a.symbolPlanGate(outline); len(problems) != 0 {
		t.Errorf("outline plan falsely reported structural problems: %v", problems)
	}

	// A content plan that really deletes Mul is still caught.
	del := `package demo

// Add returns the sum of a and b.
func Add(a, b int) int {
	return a + b
}
`
	content := []Step{{File: "mathops.go", Content: del, Desc: "drop Mul"}}
	if problems := a.symbolPlanGate(content); len(problems) == 0 {
		t.Errorf("a content plan that removes Mul must be rejected by the symbol gate")
	}
}

// writeEvalFixture creates a minimal Go module with a buggy Add and a
// passing-able test, plus an extra Mul the producer is not supposed to remove.
func writeEvalFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module demo\n\ngo 1.25\n")
	write("mathops.go", `package demo

// Add returns the sum of a and b.
func Add(a, b int) int {
	return a - b // bug
}

// Mul returns the product of a and b.
func Mul(a, b int) int {
	return a * b
}
`)
	write("mathops_test.go", `package demo

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatal("Add broken")
	}
}
`)
	return dir
}

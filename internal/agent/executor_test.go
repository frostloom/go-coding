package agent

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// outlineStep builds the P22 OUTLINE plan shape the planner produces in
// executor mode: file + description + acceptance criteria, NO content — a
// worker executor derives the content from the description.
func outlineStep(file, desc, accept string) string {
	return `{"steps":[{"file":` + strconv.Quote(file) + `,"desc":` + strconv.Quote(desc) + `,"accept":` + strconv.Quote(accept) + `}]}`
}

// contentStep builds the classic full-content plan shape, used by executor
// workers and by the legacy atomic path (Executor=false).
func contentStep(file, content, desc string) string {
	return `{"steps":[{"file":` + strconv.Quote(file) + `,"content":` + strconv.Quote(content) + `,"desc":` + strconv.Quote(desc) + `}]}`
}

// TestExecutorHappyPath is the plan-and-execute end to end (P22): the parent
// produces an OUTLINE plan, editing() dispatches the step to an executor
// worker that derives the full content and lands it in the shared workspace,
// and the parent's integration test then passes. The recording client proves
// per-role model tiering: the worker's request carried ExecutorModel, the
// planner's did not.
func TestExecutorHappyPath(t *testing.T) {
	dir := t.TempDir()
	writeToolFixture(t, dir)

	rc := &recordingClient{ChatClient: newMockChat([]string{
		outlineStep("calc.go", "fix Add to sum", "Add must return the sum"),
		contentStep("calc.go", fixedAddContent, "fix Add to sum"),
	})}
	a := New(rc, Config{
		RepoPath:      dir,
		Requirement:   "The Add function is broken. Fix it so it returns the correct sum.",
		MaxRounds:     2,
		Executor:      true,
		ExecutorModel: "cheap-model",
	})
	res := a.Run(context.Background())
	if !res.Passed() {
		t.Fatalf("expected executor run to pass, got state=%s note=%s", res.State, res.Note)
	}
	// The worker wrote the fix into the shared workspace.
	b, err := os.ReadFile(filepath.Join(dir, "calc.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "a + b") {
		t.Errorf("executor did not land the fix; calc.go:\n%s", b)
	}
	// Two LLM calls total: parent outline + worker content. The worker's calls
	// merge into the parent session (runExecutor), so cost accounting stays whole.
	if res.LLMCalls != 2 {
		t.Errorf("LLMCalls = %d, want 2 (parent outline + worker content)", res.LLMCalls)
	}
	if len(rc.requests) < 2 {
		t.Fatalf("expected 2 recorded requests, got %d", len(rc.requests))
	}
	// Per-role model tiering rides ChatRequest.Model.
	if rc.requests[0].Model != "" {
		t.Errorf("planner request model = %q, want empty (inherit the client's model)", rc.requests[0].Model)
	}
	if rc.requests[1].Model != "cheap-model" {
		t.Errorf("executor request model = %q, want %q", rc.requests[1].Model, "cheap-model")
	}
	// Role prompts: the planner runs on the OUTLINE system prompt; the executor
	// prepends its implementation-worker role instructions to the content prompt.
	plannerSys := rc.requests[0].Messages[0].Content
	if !strings.Contains(plannerSys, "OUTLINE plan") {
		t.Errorf("planner system prompt should be the executor-plan prompt; got:\n%s", plannerSys)
	}
	workerSys := rc.requests[1].Messages[0].Content
	if !strings.Contains(workerSys, "implementation worker") {
		t.Errorf("executor system prompt should carry the worker role instructions; got:\n%s", workerSys)
	}
	// The dispatch is replay-visible as executor events (dispatch + done).
	var execEvents int
	for _, ev := range res.Events {
		if ev.Tool == "executor" {
			execEvents++
		}
	}
	if execEvents < 2 {
		t.Errorf("expected dispatch+done executor events, got %d", execEvents)
	}
}

// wrongMultiplyContent is a worker plan that still fails the visible test.
const wrongMultiplyContent = `package demo

// Add returns the sum of a and b.
func Add(a, b int) int {
	return a * b
}
`

// TestExecutorFailureRoutesParentToRepair: the worker's two attempts are both
// wrong, exhausting its MaxRounds=2, so editing() routes the parent into
// REPAIRING with the worker's failure report in context. The parent produces a
// fresh outline, a new worker implements it correctly, and the task passes.
func TestExecutorFailureRoutesParentToRepair(t *testing.T) {
	dir := t.TempDir()
	writeToolFixture(t, dir)

	// The first worker's two attempts are both wrong (multiply), so it exhausts
	// its own MaxRounds=2 and fails; the parent repairs with a fresh outline and
	// a second worker that implements the fix correctly.
	script := []string{
		outlineStep("calc.go", "fix Add to sum", "Add must return the sum"),
		contentStep("calc.go", wrongMultiplyContent, "wrong: multiply"),
		contentStep("calc.go", wrongMultiplyContent, "wrong again: multiply"),
		outlineStep("calc.go", "fix Add to sum", "Add must return the sum"),
		contentStep("calc.go", fixedAddContent, "correct: add"),
	}
	a := New(newMockChat(script), Config{
		RepoPath:    dir,
		Requirement: "The Add function is broken. Fix it so it returns the correct sum.",
		MaxRounds:   3,
		Executor:    true,
	})
	res := a.Run(context.Background())
	if !res.Passed() {
		t.Fatalf("expected the parent repair to succeed, got state=%s note=%s", res.State, res.Note)
	}
	// The parent went through one repair round (worker failure → fresh plan).
	if res.Rounds != 2 {
		t.Errorf("Rounds = %d, want 2 (parent plan + parent repair)", res.Rounds)
	}
	// Parent outline + parent repair outline + worker's 3 content calls (2
	// attempts in the first worker + 1 correct in the second worker).
	if res.LLMCalls != 5 {
		t.Errorf("LLMCalls = %d, want 5 (parent 2 + workers 3)", res.LLMCalls)
	}
	b, err := os.ReadFile(filepath.Join(dir, "calc.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "a + b") {
		t.Errorf("repair did not land the fix; calc.go:\n%s", b)
	}
	// The worker failure report reached the parent's repair context.
	var repaired bool
	for _, ev := range res.Events {
		if ev.Tool == "executor_failed" {
			repaired = true
		}
	}
	if !repaired {
		t.Error("expected an executor_failed event when the worker ran out of rounds")
	}
}

// TestExecutorDisabledKeepsAtomicPath is the escape hatch: with Executor=false
// the legacy atomic path still works — the parent produces and applies the
// full-content plan itself in one LLM call, with no worker dispatch.
func TestExecutorDisabledKeepsAtomicPath(t *testing.T) {
	dir := t.TempDir()
	writeToolFixture(t, dir)

	a := New(newMockChat([]string{contentStep("calc.go", fixedAddContent, "fix Add")}), Config{
		RepoPath:    dir,
		Requirement: "The Add function is broken. Fix it so it returns the correct sum.",
		MaxRounds:   2,
		Executor:    false, // escape hatch: the legacy atomic whole-content path
	})
	res := a.Run(context.Background())
	if !res.Passed() {
		t.Fatalf("expected the atomic path to pass, got state=%s note=%s", res.State, res.Note)
	}
	if res.LLMCalls != 1 {
		t.Errorf("LLMCalls = %d, want 1 (single atomic plan)", res.LLMCalls)
	}
	for _, ev := range res.Events {
		if ev.Tool == "executor" || ev.Tool == "executor_failed" {
			t.Errorf("atomic path must not dispatch executors; found event %+v", ev)
		}
	}
}

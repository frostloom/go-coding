package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAgentRepairsBeforeTest verifies the M4 fast-feedback loop: an edit that
// breaks a cross-file reference is rejected at the symbol layer (milliseconds,
// no compiler run), the rejection reason is fed back, and the agent repairs —
// all BEFORE the tests ever run. This is the check Claude Code cannot do: it
// has no call graph, so a broken reference surfaces only when the compiler
// complains, seconds later.
func TestAgentRepairsBeforeTest(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module demo\n\ngo 1.25\n")
	write("calc.go", `package demo

// Add returns the sum of a and b.
func Add(a, b int) int {
	return a - b // bug
}

// Mul returns the product.
func Mul(a, b int) int {
	return a * b
}
`)
	write("use.go", `package demo

// useAdd calls Add from another file.
func useAdd(a, b int) int {
	return Add(a, b)
}
`)
	write("calc_test.go", `package demo

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatal("Add broken")
	}
	if Mul(2, 3) != 6 {
		t.Fatal("Mul broken")
	}
}
`)

	// Plan 1 removes Add entirely — the symbol validation must reject it
	// (use.go still calls Add). The repair then restores Add correctly.
	mock := newMockChat([]string{
		`{"steps":[{"file":"calc.go","content":"package demo\n\n// Mul returns the product.\nfunc Mul(a, b int) int {\n\treturn a * b\n}\n","desc":"attempt 1: drop Add"}]}`,
		`{"steps":[{"file":"calc.go","content":"package demo\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n\n// Mul returns the product.\nfunc Mul(a, b int) int {\n\treturn a * b\n}\n","desc":"repair: restore Add correctly"}]}`,
	})
	rc := &recordingClient{ChatClient: mock}
	a := New(rc, Config{RepoPath: dir, Requirement: "Fix Add so it returns the sum", MaxRounds: 3})
	res := a.Run(context.Background())
	if !res.Passed() {
		t.Fatalf("expected agent to pass after the rejection + repair, got state=%s note=%s", res.State, res.Note)
	}
	if res.Rounds != 2 {
		t.Errorf("expected 2 rounds (plan + repair), got %d", res.Rounds)
	}

	// The repair request must carry the rejection reason — proof the loop fed
	// the symbol-layer failure back instead of just failing the task.
	if len(rc.requests) < 2 {
		t.Fatalf("expected >= 2 LLM requests, got %d", len(rc.requests))
	}
	var joined string
	for _, m := range rc.requests[1].Messages {
		joined += m.Content
	}
	if !strings.Contains(joined, "still called") {
		t.Errorf("repair request should contain the rejection reason, got:\n%s", joined)
	}
}

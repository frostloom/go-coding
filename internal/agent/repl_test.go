package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zlx05/go-coding/internal/fsutil"
)

// TestRunTurnReusesWorkspaceAndMemory drives two turns against one agent and
// asserts the contract the interactive REPL depends on:
//
//   - workspace persistence: turn 2 edits land in the same sandbox as turn 1,
//     so follow-up requests build on prior edits, not a fresh copy
//   - memory: turn 2's LLM request still carries turn 1's plan in the
//     conversation history, so a follow-up isn't starting over
func TestRunTurnReusesWorkspaceAndMemory(t *testing.T) {
	src := filepath.Join("..", "..", "testdata", "cases", "fix_add")
	dir := t.TempDir()
	if err := fsutil.CopyDir(src, dir); err != nil {
		t.Fatal(err)
	}

	planFix := `{"steps":[{"file":"mathops.go","content":"package fixadd\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n","desc":"fix Add"}]}`
	planSubtract := `{"steps":[{"file":"mathops.go","content":"package fixadd\n\n// Add returns the sum of a and b.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n\n// Subtract returns a minus b.\nfunc Subtract(a, b int) int {\n\treturn a - b\n}\n","desc":"add Subtract"}]}`
	rc := &recordingClient{ChatClient: newMockChat([]string{planFix, planSubtract})}
	a := New(rc, Config{RepoPath: dir, Requirement: "first turn", MaxRounds: 5})

	r1 := a.RunTurn(context.Background(), "The Add function is broken. Fix it so it returns the sum.")
	if !r1.Passed() {
		t.Fatalf("turn 1 expected pass, got state=%s note=%s", r1.State, r1.Note)
	}

	r2 := a.RunTurn(context.Background(), "Add a Subtract function that returns a minus b.")
	if !r2.Passed() {
		t.Fatalf("turn 2 expected pass, got state=%s note=%s", r2.State, r2.Note)
	}

	// (a) workspace persistence: both symbols live in the same file after turn 2.
	b, err := os.ReadFile(filepath.Join(dir, "mathops.go"))
	if err != nil {
		t.Fatal(err)
	}
	code := string(b)
	if !strings.Contains(code, "func Add") || !strings.Contains(code, "func Subtract") {
		t.Errorf("turn 2 should persist into the turn-1 workspace; got:\n%s", code)
	}

	// (b) memory: the turn-2 planning request carries turn-1's assistant plan.
	if len(rc.requests) != 2 {
		t.Fatalf("expected 2 LLM calls (one per turn), got %d", len(rc.requests))
	}
	var turn2 string
	for _, m := range rc.requests[1].Messages {
		turn2 += m.Content
	}
	if !strings.Contains(turn2, "fix Add") {
		t.Errorf("turn 2 request should carry turn-1 plan in history; got:\n%s", turn2)
	}
}

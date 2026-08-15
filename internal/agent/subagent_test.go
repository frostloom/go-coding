package agent

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/zlx05/go-coding/internal/subagent"
)

// TestSubagentResearchDispatch is the researcher end-to-end path: the parent
// LLM calls the dynamic tool "subagent:researcher", the child explores the
// shared workspace and reports in prose, the report is fed back to the parent,
// and the parent then produces a plan and finishes the task.
func TestSubagentResearchDispatch(t *testing.T) {
	dir := t.TempDir()
	writeToolFixture(t, dir)

	script := []string{
		`{"call_tool":{"name":"subagent:researcher","args":{"task":"Find the bug in Add"}}}`,
		`{"explore":[{"op":"read_file","path":"calc.go"}]}`,
		"The bug is in calc.go: Add returns a-b instead of a+b. Change the body to a+b.",
		`{"steps":[{"file":"calc.go","content":` + strconv.Quote(fixedAddContent) + `,"desc":"fix Add"}]}`,
	}
	rc := &recordingClient{ChatClient: newMockChat(script)}
	a := New(rc, Config{
		RepoPath:    dir,
		Requirement: "The Add function is broken. Fix it.",
		MaxRounds:   2,
		SubAgents: []subagent.Def{
			{Name: "researcher", Description: "read-only investigation", Prompt: "You are a research specialist.", ReadOnly: true},
		},
	})
	res := a.Run(context.Background())
	if !res.Passed() {
		t.Fatalf("expected parent to finish after the research report, got state=%s note=%s", res.State, res.Note)
	}
	// Parent: tool call + plan (2). Child: explore + report (2).
	if res.LLMCalls != 4 {
		t.Errorf("LLMCalls = %d, want 4 (parent 2 + child 2)", res.LLMCalls)
	}
	// The report must have reached the parent model: the parent's final LLM
	// request (the plan) carries the subagent result in its context. The
	// recording client captures child requests too, so the parent's plan is
	// the last request in the sequence.
	last := rc.requests[len(rc.requests)-1]
	var saw string
	for _, m := range last.Messages {
		saw += m.Content
	}
	if !strings.Contains(saw, "subagent:researcher") || !strings.Contains(saw, "returns a-b") {
		t.Errorf("parent plan request should carry the research report; got:\n%s", saw)
	}
	// The dispatch is replay-visible as subagent events.
	var subagentEvents int
	for _, ev := range res.Events {
		if ev.Tool == "subagent" {
			subagentEvents++
		}
	}
	if subagentEvents < 2 {
		t.Errorf("expected dispatch+done subagent events, got %d", subagentEvents)
	}
}

// TestSubagentWorkerEditsSharedWorkspace covers the full-access child: the
// worker runs the real plan→edit→test loop on the SAME sandbox workspace, so
// the file on disk changes and the parent's symbol index sees the edit.
func TestSubagentWorkerEditsSharedWorkspace(t *testing.T) {
	dir := t.TempDir()
	writeToolFixture(t, dir)

	script := []string{
		`{"call_tool":{"name":"subagent:coder","args":{"task":"Fix the Add function"}}}`,
		`{"steps":[{"file":"calc.go","content":` + strconv.Quote(fixedAddContent) + `,"desc":"fix Add"}]}`,
		`{"steps":[{"file":"calc.go","content":` + strconv.Quote(fixedAddContent) + `,"desc":"fix Add (already fixed by subagent)"}]}`,
	}
	a := New(newMockChat(script), Config{
		RepoPath:    dir,
		Requirement: "The Add function is broken. Fix it.",
		MaxRounds:   2,
		SubAgents: []subagent.Def{
			{Name: "coder", Description: "implements features", Prompt: "You are a coder subagent."},
		},
	})
	res := a.Run(context.Background())
	if !res.Passed() {
		t.Fatalf("expected run to pass, got state=%s note=%s", res.State, res.Note)
	}
	if res.LLMCalls != 3 {
		t.Errorf("LLMCalls = %d, want 3 (parent 2 + child 1)", res.LLMCalls)
	}
	// The worker wrote the fix into the shared workspace; the parent's second
	// edit re-applies the same content (idempotent) and still passes.
	b, err := os.ReadFile(filepath.Join(dir, "calc.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "a + b") {
		t.Errorf("shared workspace not edited by worker; calc.go:\n%s", b)
	}
}

// TestSubagentReadOnlyChildCannotEdit asserts the structural safety of the
// researcher: its toolset has no edit/run tools, it uses the research system
// prompt with its role instructions prepended, and its registry is NOT
// re-seeded with the subagent tools (no grandchild dispatch).
func TestSubagentReadOnlyChildCannotEdit(t *testing.T) {
	dir := t.TempDir()
	writeToolFixture(t, dir)

	rc := &recordingClient{ChatClient: newMockChat([]string{
		"Findings: calc.go's Add subtracts instead of adding. Report over.",
	})}
	a := New(rc, Config{
		RepoPath: dir,
		SubAgents: []subagent.Def{
			{Name: "researcher", Description: "read-only", Prompt: "You are a research specialist.", ReadOnly: true},
		},
	})
	def := subagent.Def{Name: "researcher", Prompt: "You are a research specialist.", ReadOnly: true}
	child := a.newChildAgent(def, "Investigate the Add bug")
	if child.tools.edit != nil || child.tools.run != nil || child.tools.cmd != nil {
		t.Error("read-only child must not get edit/run/cmd tools")
	}
	if child.tools.read == nil || child.tools.grep == nil || child.tools.list == nil {
		t.Error("read-only child must keep the explore tools")
	}
	if !child.ctxMgr.research {
		t.Error("read-only child must use the research system prompt")
	}
	if child.ctxMgr.rolePrompt != def.Prompt {
		t.Errorf("rolePrompt = %q, want %q", child.ctxMgr.rolePrompt, def.Prompt)
	}
	// The child's registry holds only the parent's external tools (skills/MCP),
	// never the subagent tools — no recursion.
	if _, ok := child.reg.Get("subagent:researcher"); ok {
		t.Error("child must not inherit subagent dispatch tools")
	}
	// Run the researcher to completion: free text is the report.
	res := child.runResearch(context.Background())
	if res.State != StateDone || !strings.Contains(res.Note, "subtracts") {
		t.Fatalf("researcher report = state=%s note=%s", res.State, res.Note)
	}
	// The research request carried the research prompt + the role instructions.
	if len(rc.requests) == 0 {
		t.Fatal("researcher made no LLM call")
	}
	sys := rc.requests[0].Messages[0].Content
	if !strings.Contains(sys, "research subagent") || !strings.Contains(sys, "You are a research specialist.") {
		t.Errorf("research system prompt not injected; got:\n%s", sys)
	}
}

// TestSubagentWorkerChildHasFullTools is the mirror assertion: a non-read-only
// child keeps the full toolset and the normal coding prompt.
func TestSubagentWorkerChildHasFullTools(t *testing.T) {
	dir := t.TempDir()
	writeToolFixture(t, dir)
	a := New(newMockChat(nil), Config{RepoPath: dir})
	child := a.newChildAgent(subagent.Def{Name: "coder", Prompt: "You are a coder."}, "task")
	if child.tools.edit == nil || child.tools.run == nil {
		t.Error("worker child must keep the full toolset")
	}
	if child.ctxMgr.research {
		t.Error("worker child must use the normal coding prompt")
	}
}

// TestChildAgentModelOverride verifies per-role model tiering (P22): a child
// agent's modelOverride comes from its subagent.Def.Model (which the executor
// path sets from Config.ExecutorModel), and the top-level agent stays empty so
// it inherits the client's configured model.
func TestChildAgentModelOverride(t *testing.T) {
	dir := t.TempDir()
	writeToolFixture(t, dir)
	a := New(newMockChat(nil), Config{RepoPath: dir})
	child := a.newChildAgent(subagent.Def{Name: "coder", Prompt: "You are a coder.", Model: "cheap-model"}, "task")
	if child.modelOverride != "cheap-model" {
		t.Errorf("child.modelOverride = %q, want %q", child.modelOverride, "cheap-model")
	}
	if a.modelOverride != "" {
		t.Errorf("top-level modelOverride = %q, want empty", a.modelOverride)
	}
}

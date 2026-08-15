package agent

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/zlx05/go-coding/internal/registry"
)

func TestParseToolCall(t *testing.T) {
	tc, ok := parseToolCall(`{"call_tool":{"name":"github_search","args":{"q":"calc"}}}`)
	if !ok || tc.Name != "github_search" || tc.Args["q"] != "calc" {
		t.Fatalf("parse = %+v, %v", tc, ok)
	}
	if _, ok := parseToolCall(`{"steps":[{"file":"a.go","content":"x","desc":"d"}]}`); ok {
		t.Error("a plan must not parse as a tool call")
	}
	if _, ok := parseToolCall("here is my plan in prose"); ok {
		t.Error("prose must not parse as a tool call")
	}
	if _, ok := parseToolCall(`{"explore":[{"op":"list_dir"}]}`); ok {
		t.Error("an explore request must not parse as a tool call")
	}
}

// TestAgentCallToolLoop is the end-to-end path of the dynamic tool registry:
// the LLM calls an external tool, the agent executes it and feeds the result
// back, then the model produces a plan. This is the loop skills / MCP clients /
// subagents will drive.
func TestAgentCallToolLoop(t *testing.T) {
	dir := t.TempDir()
	writeToolFixture(t, dir)

	var called []map[string]any
	mock := newMockChat([]string{
		`{"call_tool":{"name":"probe","args":{"q":"Add"}}}`,
		`{"steps":[{"file":"calc.go","content":` + strconv.Quote(fixedAddContent) + `,"desc":"fix Add"}]}`,
	})
	a := New(mock, Config{
		RepoPath:    dir,
		Requirement: "The Add function is broken. Fix it.",
		MaxRounds:   2,
		Tools: []registry.ToolDef{
			{
				Name:        "probe",
				Description: "search the codebase for a symbol",
				Schema:      map[string]any{"type": "object"},
				Run: func(_ context.Context, args map[string]any) (any, error) {
					called = append(called, args)
					return "found Add in calc.go (buggy)", nil
				},
			},
		},
	})
	res := a.Run(context.Background())
	if !res.Passed() {
		t.Fatalf("run failed: %s", res.Note)
	}
	if len(called) != 1 {
		t.Errorf("external tool called %d times, want 1", len(called))
	}
	if called[0]["q"] != "Add" {
		t.Errorf("tool args = %v, want q=Add", called[0])
	}
	// The tool result must have reached the model: the second LLM reply is a
	// plan (not a tool call), which the loop would only reach after feeding
	// the first tool result back.
	if !res.Passed() {
		t.Fatalf("loop did not pass after a tool round-trip: %s", res.Note)
	}
}

// writeToolFixture is the minimal calc demo for the tool-call loop.
func writeToolFixture(t *testing.T, dir string) {
	t.Helper()
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module demo\n\ngo 1.25\n")
	write("calc.go", `package demo

// Add returns the sum of a and b.
func Add(a, b int) int {
	return a - b
}
`)
	write("calc_test.go", `package demo

import "testing"

func TestAdd(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Fatal("Add broken")
	}
}
`)
}

const fixedAddContent = `package demo

// Add returns the sum of a and b.
func Add(a, b int) int {
	return a + b
}
`

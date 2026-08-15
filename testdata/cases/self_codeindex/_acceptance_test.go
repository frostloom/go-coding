package codeindex

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// This hidden acceptance test is stashed out of the agent's view during a
// run and applied only afterwards (see internal/harness). It grades the fix:
// a "lazy" edit that disables the graph layer entirely would fail here too.

func absRoot(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func buildIndex(t *testing.T) *Index {
	t.Helper()
	b := NewBuilder(absRoot(t))
	if _, err := b.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	return b.Index()
}

// The graph layer must expand a hit's CALLEES. A swapped direction (bug)
// returns callers instead, which this test rejects.
func TestGraphIncludesCallee(t *testing.T) {
	ix := buildIndex(t)
	r := NewRetriever(ix, absRoot(t))
	res := r.Retrieve(context.Background(), "parseFile", 0)

	var got []string
	hasCallee := false
	for _, x := range res {
		got = append(got, x.Symbol.ID()+" ["+x.Reason+"]")
		if x.Reason == "graph" && strings.Contains(x.Symbol.ID(), " walk") {
			hasCallee = true
		}
	}
	if !hasCallee {
		t.Fatalf("graph expansion must include parseFile's callee walk; got:\n%s", strings.Join(got, "\n"))
	}
}

// The index must actually cover the real multi-file project, and the call
// graph must resolve across files (parseFileSymbols in builder.go calls
// parseFile in parser.go).
func TestIndexCoversRealProject(t *testing.T) {
	ix := buildIndex(t)
	files := strings.Join(ix.SortedFiles(), "\n")
	for _, f := range []string{"builder.go", "parser.go", "retriever.go", "symbol.go"} {
		if !strings.Contains(files, f) {
			t.Errorf("index missing real source file %s", f)
		}
	}
	callees := ix.Callees("codeindex (*Builder).(*Builder).parseFileSymbols")
	found := false
	for _, id := range callees {
		if id == "codeindex parseFile" {
			found = true
		}
	}
	if !found {
		t.Errorf("call graph should link parseFileSymbols -> parseFile across files; got %v", callees)
	}
}

// Duplicate query tokens must not duplicate results, and maxResults must cap.
func TestRetrieveDedupAndCap(t *testing.T) {
	ix := buildIndex(t)
	r := NewRetriever(ix, absRoot(t))
	ctx := context.Background()
	full := r.Retrieve(ctx, "parseFile", 0)
	dup := r.Retrieve(ctx, "parseFile parseFile", 0)
	if len(dup) != len(full) {
		t.Errorf("duplicate tokens should not duplicate results: full=%d dup=%d", len(full), len(dup))
	}
	capped := r.Retrieve(ctx, "parseFile", 2)
	if len(capped) > 2 {
		t.Errorf("maxResults cap not honored: got %d", len(capped))
	}
}

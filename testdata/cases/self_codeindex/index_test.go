package codeindex

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// buildRetrieve builds the index over this fixture and runs one retrieval.
// It is the "smoke" entry the agent reads: fixing the retrieval engine must
// make this test pass.
func buildRetrieve(t *testing.T, query string) []Result {
	t.Helper()
	abs, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	b := NewBuilder(abs)
	if _, err := b.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	r := NewRetriever(b.Index(), abs)
	return r.Retrieve(context.Background(), query, 0)
}

// TestRetrieveReturnsCallees drives the visible repair loop: a query for a
// function must surface the functions it calls (its callees) as graph
// context. When the expansion direction is swapped this test fails, telling
// the agent exactly what is broken.
func TestRetrieveReturnsCallees(t *testing.T) {
	results := buildRetrieve(t, "parseFile")

	var got []string
	found := false
	for _, res := range results {
		got = append(got, res.Symbol.ID()+" ["+res.Reason+"]")
		if res.Reason == "graph" && strings.Contains(res.Symbol.ID(), " walk") {
			found = true
		}
	}
	if !found {
		t.Fatalf("retrieval for parseFile should include its callee walk as graph context; got:\n%s", strings.Join(got, "\n"))
	}
}
